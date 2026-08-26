package voicecall

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/purpshell/meowcaller"
)

const (
	defaultElevenLabsSTTURL      = "wss://api.elevenlabs.io/v1/speech-to-text/realtime"
	defaultElevenLabsBatchSTTURL = "https://api.elevenlabs.io/v1/speech-to-text"
	defaultElevenLabsTTSBase     = "wss://api.elevenlabs.io/v1/text-to-speech"
	defaultElevenLabsSTTModel    = "scribe_v2_realtime"
	// defaultElevenLabsTTSModel is what an agent that never picked a voice model
	// gets. Leaving model_id off entirely makes ElevenLabs fall back to
	// Multilingual v2, which returns the first audio of a reply in ~530 ms
	// against Flash v2.5's ~290 ms — a quarter of a second of the caller's wait,
	// on every turn of every call.
	defaultElevenLabsTTSModel = "eleven_flash_v2_5"

	// Scribe can segment the caller's speech itself ("vad") or leave it to the
	// client ("manual"). Manual is the default here because its finalization is
	// both faster and ours to trigger; see turnDetector.
	commitStrategyManual = "manual"
	commitStrategyVAD    = "vad"

	// transcriberIdleWindow is how long a partial transcript may sit unchanged
	// before the turn is committed on its account alone. It is deliberately far
	// longer than the endpoint window: this is a fallback for a caller the energy
	// detector never hears, not the path a normal turn takes.
	transcriberIdleWindow = 1200 * time.Millisecond
)

func websocketResponseBody(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil || len(body) == 0 {
		return ""
	}
	return ": " + strings.TrimSpace(string(body))
}

// elevenLabsConfig describes the dynamic voice pipeline. Unlike ElevenAgents,
// it needs no hosted agent resource: the selected Scribe batch or realtime
// model handles STT, the selected local LLM handles the conversation, and
// ElevenLabs TTS speaks the response.
type elevenLabsConfig struct {
	APIKey            string
	STTEndpoint       string
	BatchSTTEndpoint  string
	TTSBaseURL        string
	STTModel          string
	STTSilenceMS      int
	CommitStrategy    string
	EndpointSilenceMS int
	EndpointThreshold float64
	MinSpeechMS       int
	MaxUtteranceMS    int
	EnabledFlag       bool
	LLM               conversationLLM
	HTTPClient        *http.Client

	Instructions   string
	LLMProvider    string
	LLMModel       string
	LLMTemperature float64
	Language       string
	VoiceID        string
	ModelID        string
	Speed          float64
	Volume         float64

	BeginMessageMode string
	WelcomeMessage   string
	WelcomeDelayMs   int
}

func loadElevenLabsConfig() elevenLabsConfig {
	return elevenLabsConfig{
		APIKey:            strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY")),
		STTEndpoint:       envString("ELEVENLABS_REALTIME_STT_URL", defaultElevenLabsSTTURL),
		BatchSTTEndpoint:  envString("ELEVENLABS_STT_URL", defaultElevenLabsBatchSTTURL),
		TTSBaseURL:        envString("ELEVENLABS_REALTIME_TTS_URL", defaultElevenLabsTTSBase),
		STTModel:          envString("ELEVENLABS_STT_MODEL", envString("ELEVENLABS_REALTIME_STT_MODEL", defaultElevenLabsSTTModel)),
		STTSilenceMS:      envInt("ELEVENLABS_VAD_SILENCE_MS", defaultOpenAISilenceMS),
		CommitStrategy:    normalizeCommitStrategy(envString("ELEVENLABS_COMMIT_STRATEGY", commitStrategyManual)),
		EndpointSilenceMS: envInt("ELEVENLABS_ENDPOINT_SILENCE_MS", defaultOpenAISilenceMS),
		EndpointThreshold: envFloat("ELEVENLABS_ENDPOINT_RMS_THRESHOLD", defaultOpenAIVADThreshold),
		MinSpeechMS:       envInt("ELEVENLABS_ENDPOINT_MIN_SPEECH_MS", defaultOpenAIMinSpeechMS),
		MaxUtteranceMS:    envInt("ELEVENLABS_ENDPOINT_MAX_UTTERANCE_MS", defaultOpenAIMaxUtteranceMS),
		EnabledFlag:       envBoolDefault("ELEVENLABS_REALTIME_ENABLED", true),
		LLM:               loadConversationLLM(),
		HTTPClient:        voiceHTTPClient(60 * time.Second),
		Speed:             defaultSpeed,
		Volume:            defaultVolume,
	}
}

func (c elevenLabsConfig) Enabled() bool {
	key := strings.TrimSpace(c.APIKey)
	return c.EnabledFlag && key != "" && key != elevenLabsPlaceholderAPIKey
}

func (c elevenLabsConfig) SupportsLLM(provider string) bool {
	return c.LLM.Enabled(provider)
}

type elevenLabsTurn struct {
	id        int64
	contextID string
	startedAt time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	ttsOpen   bool
}

// elevenLabsVoiceBridge is one dynamic conversation session:
// WhatsApp PCM -> ElevenLabs Scribe -> selected local LLM -> ElevenLabs TTS ->
// WhatsApp PCM. Each response has its own TTS context so barge-in can cancel it
// without closing the WebSocket used by the rest of the call.
type elevenLabsVoiceBridge struct {
	cfg          elevenLabsConfig
	playSource   func(meowcaller.AudioSource)
	stopPlayback func()

	ctx    context.Context
	cancel context.CancelFunc
	frames chan []float32
	done   chan struct{}
	ready  chan struct{}

	mu           sync.Mutex
	sttConn      *websocket.Conn
	ttsConn      *websocket.Conn
	closed       bool
	readyOnce    sync.Once
	greetingOnce sync.Once
	sttSendMu    sync.Mutex
	ttsSendMu    sync.Mutex

	turnMu     sync.Mutex
	nextTurnID int64
	current    *elevenLabsTurn
	history    []conversationMessage
	// transcript is the full, uncapped conversation for post-call persistence.
	// history is trimmed to the model's context window; transcript is not.
	transcript  []conversationMessage
	ignored     map[string]bool
	callerSpoke bool
	// endOfSpeechAt is when the endpoint detector decided the caller had stopped
	// talking and the segment was committed. The gap between it and the committed
	// transcript is what the transcriber costs on its own.
	endOfSpeechAt time.Time
	// lastPartialAt is when Scribe last recognized words. It only backs the
	// safety net in transcriberIdle.
	lastPartialAt time.Time
	// segmentStartedAt is when the transcriber first reported the caller saying
	// something in the current segment. The gap between it and the commit is the
	// part of the turn the caller waits through before the AI has even been asked.
	segmentStartedAt time.Time

	outputMu      sync.Mutex
	output        *streamingAudioSource
	outputStarted bool
	outputContext string
	outputTimer   *time.Timer
}

func newElevenLabsVoiceBridge(cfg elevenLabsConfig, playSource func(meowcaller.AudioSource), stopPlayback func()) *elevenLabsVoiceBridge {
	ctx, cancel := context.WithCancel(context.Background())
	b := &elevenLabsVoiceBridge{
		cfg:          cfg,
		playSource:   playSource,
		stopPlayback: stopPlayback,
		ctx:          ctx,
		cancel:       cancel,
		frames:       make(chan []float32, 24),
		done:         make(chan struct{}),
		ready:        make(chan struct{}),
		ignored:      make(map[string]bool),
	}
	// The caller is still hearing the call connect, so spend that time opening
	// the connections the first turn will need.
	go prewarmEndpoints(ctx, cfg.HTTPClient, cfg.LLM.endpointFor(cfg.LLMProvider), cfg.BatchSTTEndpoint)
	go b.run()
	return b
}

func (b *elevenLabsVoiceBridge) WriteFrame(frame []float32) error {
	cp := append([]float32(nil), frame...)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	select {
	case b.frames <- cp:
	default:
		select {
		case <-b.frames:
		default:
		}
		select {
		case b.frames <- cp:
		default:
		}
	}
	return nil
}

func (b *elevenLabsVoiceBridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	close(b.frames)
	sttConn, ttsConn := b.sttConn, b.ttsConn
	b.mu.Unlock()

	b.cancel()
	b.interruptCurrentTurn()
	if sttConn != nil {
		_ = sttConn.Close(websocket.StatusNormalClosure, "call ended")
	}
	if ttsConn != nil {
		_ = ttsConn.Close(websocket.StatusNormalClosure, "call ended")
	}
	<-b.done
	return nil
}

func (b *elevenLabsVoiceBridge) run() {
	defer close(b.done)
	defer b.finishOutput("", true)

	if strings.TrimSpace(b.cfg.VoiceID) == "" {
		log.Println("voicecall: ElevenLabs voice pipeline needs a voice_id")
		return
	}

	if !isRealtimeScribeModel(b.cfg.STTModel) {
		b.runBatchPipeline()
		return
	}
	b.runRealtimePipeline()
}

type elevenLabsDialResult struct {
	conn *websocket.Conn
	resp *http.Response
	err  error
}

func dialElevenLabsSocket(ctx context.Context, endpoint string, headers http.Header) elevenLabsDialResult {
	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: headers})
	return elevenLabsDialResult{conn: conn, resp: resp, err: err}
}

func (b *elevenLabsVoiceBridge) runBatchPipeline() {
	ttsURL := elevenLabsTTSURL(b.cfg.TTSBaseURL, b.cfg.VoiceID, b.cfg.ModelID, b.cfg.Language)
	result := dialElevenLabsSocket(b.ctx, ttsURL, elevenLabsHeaders(b.cfg.APIKey))
	if result.err != nil {
		log.Printf("voicecall: ElevenLabs TTS connect error: %v%s", result.err, websocketResponseBody(result.resp))
		return
	}
	ttsConn := result.conn
	ttsConn.SetReadLimit(4 << 20)
	defer ttsConn.CloseNow()
	b.mu.Lock()
	b.ttsConn = ttsConn
	b.mu.Unlock()

	ttsDone := make(chan struct{})
	go func() {
		defer close(ttsDone)
		b.readTTSLoop(ttsConn)
	}()

	log.Printf("voicecall: ElevenLabs batch Scribe connected model=%s voice=%s tts_model=%s llm=%s/%s", b.cfg.STTModel, b.cfg.VoiceID, b.cfg.ModelID, normalizeLLMProvider(b.cfg.LLMProvider), b.cfg.LLMModel)
	b.greetingOnce.Do(func() { go b.triggerGreeting() })
	b.runBatchSTTLoop()
	b.cancel()
	<-ttsDone
}

// runRealtimePipeline opens speech recognition and synthesis concurrently.
// These are independent TLS/WebSocket handshakes; serializing them delayed the
// first Scribe audio by a full TTS connection setup and could drop the beginning
// of a caller's first sentence when the network was slow.
func (b *elevenLabsVoiceBridge) runRealtimePipeline() {
	ttsURL := elevenLabsTTSURL(b.cfg.TTSBaseURL, b.cfg.VoiceID, b.cfg.ModelID, b.cfg.Language)

	sttURL := elevenLabsSTTURL(b.cfg.STTEndpoint, b.cfg.STTModel, b.cfg.Language, b.cfg.STTSilenceMS, b.cfg.CommitStrategy)
	dialCtx, cancelDial := context.WithCancel(b.ctx)
	defer cancelDial()
	ttsResultCh := make(chan elevenLabsDialResult, 1)
	sttResultCh := make(chan elevenLabsDialResult, 1)
	go func() { ttsResultCh <- dialElevenLabsSocket(dialCtx, ttsURL, elevenLabsHeaders(b.cfg.APIKey)) }()
	go func() { sttResultCh <- dialElevenLabsSocket(dialCtx, sttURL, elevenLabsHeaders(b.cfg.APIKey)) }()

	ttsResult := <-ttsResultCh
	sttResult := <-sttResultCh
	if ttsResult.err != nil || sttResult.err != nil {
		cancelDial()
		if ttsResult.conn != nil {
			ttsResult.conn.CloseNow()
		}
		if sttResult.conn != nil {
			sttResult.conn.CloseNow()
		}
		if ttsResult.err != nil {
			log.Printf("voicecall: ElevenLabs TTS connect error: %v%s", ttsResult.err, websocketResponseBody(ttsResult.resp))
		}
		if sttResult.err != nil {
			log.Printf("voicecall: ElevenLabs Scribe connect error: %v%s", sttResult.err, websocketResponseBody(sttResult.resp))
		}
		return
	}

	ttsConn := ttsResult.conn
	ttsConn.SetReadLimit(4 << 20)
	defer ttsConn.CloseNow()
	sttConn := sttResult.conn
	sttConn.SetReadLimit(2 << 20)
	defer sttConn.CloseNow()

	b.mu.Lock()
	b.sttConn = sttConn
	b.ttsConn = ttsConn
	b.mu.Unlock()

	ttsDone := make(chan struct{})
	go func() {
		defer close(ttsDone)
		b.readTTSLoop(ttsConn)
	}()

	audioDone := make(chan struct{})
	go func() {
		defer close(audioDone)
		b.sendAudioLoop(sttConn)
	}()

	for {
		messageType, data, err := sttConn.Read(b.ctx)
		if err != nil {
			ctxErr := b.ctx.Err()
			b.cancel()
			<-audioDone
			<-ttsDone
			if ctxErr == nil && websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				log.Printf("voicecall: ElevenLabs Scribe read error: %v", err)
			}
			return
		}
		if messageType == websocket.MessageText {
			b.handleSTTEvent(data)
		}
	}
}

func elevenLabsHeaders(apiKey string) http.Header {
	headers := http.Header{}
	headers.Set("xi-api-key", apiKey)
	return headers
}

// normalizeCommitStrategy renders the configured segmentation strategy. Anything
// but an explicit "vad" means turns are committed from here.
func normalizeCommitStrategy(strategy string) string {
	if strings.EqualFold(strings.TrimSpace(strategy), commitStrategyVAD) {
		return commitStrategyVAD
	}
	return commitStrategyManual
}

func elevenLabsSTTURL(endpoint, model, language string, silenceMS int, strategy string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	q := u.Query()
	q.Set("model_id", strings.TrimSpace(model))
	q.Set("audio_format", "pcm_16000")
	if strategy = normalizeCommitStrategy(strategy); strategy == commitStrategyVAD {
		q.Set("commit_strategy", commitStrategyVAD)
		q.Set("vad_silence_threshold_secs", vadSilenceSeconds(silenceMS))
		q.Set("vad_threshold", "0.4")
	} else {
		// Manual: the segment is committed by the bridge the moment its own
		// endpoint detector says the caller stopped, and Scribe's trailing-silence
		// window never enters the turn at all.
		q.Set("commit_strategy", commitStrategyManual)
	}
	if language = elevenLabsLanguage(language); language != "" {
		q.Set("language_code", language)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func elevenLabsTTSURL(baseURL, voiceID, modelID, language string) string {
	endpoint := strings.ReplaceAll(strings.TrimSpace(baseURL), "{voice_id}", url.PathEscape(strings.TrimSpace(voiceID)))
	if !strings.Contains(baseURL, "{voice_id}") {
		endpoint = strings.TrimRight(endpoint, "/") + "/" + url.PathEscape(strings.TrimSpace(voiceID)) + "/multi-stream-input"
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	q := u.Query()
	if modelID = strings.TrimSpace(modelID); modelID != "" {
		q.Set("model_id", modelID)
	}
	q.Set("output_format", "pcm_16000")
	q.Set("inactivity_timeout", "180")
	q.Set("auto_mode", "true")
	if language = elevenLabsTTSLanguage(modelID, language); language != "" {
		q.Set("language_code", language)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// vadSilenceSeconds renders the trailing-silence window Scribe waits through
// before it commits the caller's turn. It is the ElevenLabs-side counterpart of
// OPENAI_VAD_SILENCE_MS and is kept as short as natural mid-sentence pauses
// allow, because the caller sits through it on every turn.
func vadSilenceSeconds(silenceMS int) string {
	if silenceMS < 150 {
		silenceMS = defaultOpenAISilenceMS
	}
	if silenceMS > 3000 {
		silenceMS = 3000
	}
	return strconv.FormatFloat(float64(silenceMS)/1000, 'f', 2, 64)
}

func clampElevenLabsSpeed(speed float64) float64 {
	if speed <= 0 {
		return defaultSpeed
	}
	if speed < 0.7 {
		return 0.7
	}
	if speed > 1.2 {
		return 1.2
	}
	return speed
}

func elevenLabsLanguage(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if i := strings.IndexAny(language, "-_"); i >= 0 {
		language = language[:i]
	}
	return language
}

// elevenLabsV25TTSLanguages are the languages the Flash v2.5 and Turbo v2.5 TTS
// models can be pinned to. They are the only models that accept language_code at
// all, and they accept nothing outside this set.
var elevenLabsV25TTSLanguages = map[string]bool{
	"ar": true, "bg": true, "cs": true, "da": true, "de": true, "el": true,
	"en": true, "es": true, "fi": true, "fil": true, "fr": true, "hi": true,
	"hr": true, "hu": true, "id": true, "it": true, "ja": true, "ko": true,
	"ms": true, "nl": true, "no": true, "pl": true, "pt": true, "ro": true,
	"ru": true, "sk": true, "sv": true, "ta": true, "tr": true, "uk": true,
	"vi": true, "zh": true,
}

// elevenLabsTTSLanguage renders the agent's language as a language_code the given
// TTS model actually accepts, or "" to leave the parameter off.
//
// Pinning a language the model does not support is not a soft failure:
// ElevenLabs closes the synthesis socket with a policy violation ("Model
// 'eleven_multilingual_v2' does not support language_code 'bn'"), so the call
// plays no audio at all and the caller sits in silence until they hang up. Only
// Flash/Turbo v2.5 take the parameter; every other model — Multilingual v2,
// Turbo/Flash v2, v3, an empty selection that lets ElevenLabs pick its own
// default, or any model added later — infers the language from the text, so the
// parameter is simply omitted for them.
func elevenLabsTTSLanguage(modelID, language string) string {
	language = elevenLabsLanguage(language)
	if language == "" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(modelID)) {
	case "eleven_flash_v2_5", "eleven_turbo_v2_5":
		if elevenLabsV25TTSLanguages[language] {
			return language
		}
	}
	return ""
}

// resolveElevenLabsTTSModel fills in the voice model an agent never chose.
//
// Leaving model_id off the synthesis socket is not neutral: ElevenLabs falls
// back to Multilingual v2, which is the slowest model it offers — measured at
// ~530 ms to the first audio of a reply against Flash v2.5's ~290 ms. Flash is
// therefore the default wherever it speaks the agent's language; for the
// languages it does not, the parameter is still left off, because ElevenLabs'
// own fallback covers more of them and speaking the right language beats
// speaking a quarter-second sooner.
func resolveElevenLabsTTSModel(modelID, language string) string {
	if modelID = strings.TrimSpace(modelID); modelID != "" {
		return modelID
	}
	if elevenLabsSpeaksLanguage(defaultElevenLabsTTSModel, language) {
		return defaultElevenLabsTTSModel
	}
	return ""
}

// elevenLabsSpeaksLanguage reports whether the TTS model can speak language at
// all, pinned or not. A false result is not a call-breaking failure the way an
// unsupported language_code is — the model still speaks, it just renders the
// reply in a language it was never trained on. It backs the warning that sends
// the operator to an OpenAI voice for that agent.
func elevenLabsSpeaksLanguage(modelID, language string) bool {
	language = elevenLabsLanguage(language)
	if language == "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(modelID)) {
	case "eleven_flash_v2_5", "eleven_turbo_v2_5":
		return elevenLabsV25TTSLanguages[language]
	case "eleven_turbo_v2", "eleven_flash_v2":
		return language == "en"
	case "eleven_multilingual_v2", "":
		// Multilingual v2 — also what ElevenLabs falls back to when no model is
		// set — speaks the v2.5 set apart from the three v2.5 added.
		switch language {
		case "hu", "no", "vi":
			return false
		}
		return elevenLabsV25TTSLanguages[language]
	default:
		// v3 covers 70+ languages, and a model added later is not ours to
		// second-guess. Assume it speaks the selection.
		return true
	}
}

func normalizeScribeModel(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "scribe_v1", "scribe_v2", "scribe_v2_realtime":
		return strings.ToLower(strings.TrimSpace(model))
	default:
		return defaultElevenLabsSTTModel
	}
}

func isRealtimeScribeModel(model string) bool {
	return normalizeScribeModel(model) == "scribe_v2_realtime"
}

func (b *elevenLabsVoiceBridge) runBatchSTTLoop() {
	const (
		vadThreshold   = defaultOpenAIVADThreshold
		silenceMS      = defaultOpenAISilenceMS
		prefixMS       = defaultOpenAIPrefixMS
		minSpeechMS    = defaultOpenAIMinSpeechMS
		maxUtteranceMS = defaultOpenAIMaxUtteranceMS
	)
	silenceSamples := millisecondsToSamples(silenceMS, silenceMS)
	prefixLimit := millisecondsToSamples(prefixMS, prefixMS)
	minSpeechSamples := millisecondsToSamples(minSpeechMS, minSpeechMS)
	maxUtteranceSamples := millisecondsToSamples(maxUtteranceMS, maxUtteranceMS)

	var prefix, utterance []float32
	var trailingSilence, voicedSamples int
	speaking := false
	commit := func() {
		if speaking && voicedSamples >= minSpeechSamples {
			audio := append([]float32(nil), utterance...)
			turn, _ := b.beginTurn()
			go b.processBatchSTTTurn(turn, audio)
		}
		prefix = nil
		utterance = nil
		trailingSilence = 0
		voicedSamples = 0
		speaking = false
	}

	for {
		select {
		case <-b.ctx.Done():
			return
		case frame, ok := <-b.frames:
			if !ok {
				commit()
				return
			}
			voiced := frameRMS(frame) >= vadThreshold
			if !speaking {
				if !voiced {
					prefix = append(prefix, frame...)
					if len(prefix) > prefixLimit {
						prefix = append([]float32(nil), prefix[len(prefix)-prefixLimit:]...)
					}
					continue
				}
				speaking = true
				b.markCallerSpoke()
				b.interruptCurrentTurn()
				utterance = append(utterance, prefix...)
				prefix = nil
			}
			utterance = append(utterance, frame...)
			if voiced {
				voicedSamples += len(frame)
				trailingSilence = 0
			} else {
				trailingSilence += len(frame)
			}
			if trailingSilence >= silenceSamples || len(utterance) >= maxUtteranceSamples {
				commit()
			}
		}
	}
}

func (b *elevenLabsVoiceBridge) processBatchSTTTurn(turn *elevenLabsTurn, audio []float32) {
	text, err := b.transcribeBatch(turn.ctx, audio)
	if err != nil {
		if turn.ctx.Err() == nil && b.ctx.Err() == nil {
			log.Printf("voicecall: ElevenLabs batch transcription error model=%s: %v", b.cfg.STTModel, err)
		}
		b.clearTurn(turn.id)
		return
	}
	text = strings.TrimSpace(text)
	if text == "" || !b.turnIsCurrent(turn.id) {
		b.clearTurn(turn.id)
		return
	}
	log.Printf("voicecall: caller said: %s", text)
	b.appendHistory(conversationMessage{Role: "user", Content: text})
	b.completeLLMTurn(turn, b.historySnapshot())
}

func (b *elevenLabsVoiceBridge) transcribeBatch(ctx context.Context, samples []float32) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "caller.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(pcm16WAV(samples, audioSampleRate)); err != nil {
		return "", err
	}
	_ = w.WriteField("model_id", normalizeScribeModel(b.cfg.STTModel))
	if language := elevenLabsLanguage(b.cfg.Language); language != "" {
		_ = w.WriteField("language_code", language)
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.BatchSTTEndpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("xi-api-key", b.cfg.APIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())
	client := b.cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(responseBody)))
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", fmt.Errorf("decode transcription: %w", err)
	}
	return strings.TrimSpace(result.Text), nil
}

func (b *elevenLabsVoiceBridge) sendAudioLoop(conn *websocket.Conn) {
	select {
	case <-b.ctx.Done():
		return
	case <-b.ready:
	}

	var pending []float32
	// One call frame, so every frame the caller speaks goes straight out to
	// Scribe instead of waiting for a larger chunk to fill.
	const chunkSamples = meowcaller.FrameSamples

	// Under the manual commit strategy the caller's turn is closed out from
	// here. The detector reads the same frames that go to Scribe; the ticker
	// covers the stretch after the silence filler has given up, when no frames
	// arrive at all and the caller is simply quiet.
	var detector *turnDetector
	var idle <-chan time.Time
	if normalizeCommitStrategy(b.cfg.CommitStrategy) == commitStrategyManual {
		detector = newTurnDetector(b.cfg.EndpointThreshold, b.cfg.MinSpeechMS, b.cfg.EndpointSilenceMS, b.cfg.MaxUtteranceMS)
		ticker := time.NewTicker(frameDuration)
		defer ticker.Stop()
		idle = ticker.C
	}

	for {
		select {
		case <-b.ctx.Done():
			return
		case now := <-idle:
			if detector.Idle(now) || b.transcriberIdle(detector, now) {
				b.commitSegment(conn, detector, now)
			}
		case frame, ok := <-b.frames:
			if !ok {
				return
			}
			pending = append(pending, frame...)
			for len(pending) >= chunkSamples {
				chunk := pending[:chunkSamples]
				payload := map[string]any{
					"message_type":  "input_audio_chunk",
					"audio_base_64": base64.StdEncoding.EncodeToString(float32ToPCM16LE(chunk)),
					"sample_rate":   audioSampleRate,
				}
				if err := b.sendSTTJSON(conn, payload); err != nil {
					if b.ctx.Err() == nil {
						log.Printf("voicecall: ElevenLabs Scribe audio send error: %v", err)
					}
					b.cancel()
					return
				}
				if detector != nil {
					if now := time.Now(); detector.Observe(chunk, now) {
						b.commitSegment(conn, detector, now)
					}
				}
				pending = pending[chunkSamples:]
			}
		}
	}
}

// commitSegment tells Scribe the caller's turn is over. This is what the manual
// strategy buys: finalization starts on the word the caller actually stopped
// on, instead of after Scribe's own trailing-silence timer has run its course
// on top of it.
func (b *elevenLabsVoiceBridge) commitSegment(conn *websocket.Conn, detector *turnDetector, now time.Time) {
	spoke := detector.SpokeFor(now)
	detector.Reset()
	b.markEndOfSpeech(now)
	err := b.sendSTTJSON(conn, map[string]any{
		"message_type":  "input_audio_chunk",
		"audio_base_64": "",
		"commit":        true,
		"sample_rate":   audioSampleRate,
	})
	if err != nil {
		if b.ctx.Err() == nil {
			log.Printf("voicecall: ElevenLabs Scribe commit error: %v", err)
		}
		return
	}
	if spoke > 0 {
		log.Printf("voicecall: caller stopped after %dms of speech; committing their turn", spoke.Milliseconds())
	}
}

// transcriberIdle is the safety net for a caller the energy detector cannot
// hear — a very quiet line, or a threshold set too high for it. Scribe
// recognizing words is itself proof that someone is talking, so a partial
// transcript that has gone stale ends the turn even when no frame ever crossed
// the speech threshold. Without it a caller the detector misses would never be
// committed at all, because the manual strategy leaves Scribe no VAD to fall
// back on.
func (b *elevenLabsVoiceBridge) transcriberIdle(detector *turnDetector, now time.Time) bool {
	if detector.Speaking() {
		return false
	}
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	if b.lastPartialAt.IsZero() {
		return false
	}
	return now.Sub(b.lastPartialAt) >= transcriberIdleWindow
}

func (b *elevenLabsVoiceBridge) sendSTTJSON(conn *websocket.Conn, value any) error {
	return sendElevenLabsJSON(b.ctx, conn, &b.sttSendMu, value)
}

func (b *elevenLabsVoiceBridge) sendTTSJSON(value any) error {
	return b.sendTTSJSONContext(b.ctx, value)
}

func (b *elevenLabsVoiceBridge) sendTTSJSONContext(ctx context.Context, value any) error {
	b.mu.Lock()
	conn := b.ttsConn
	b.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("TTS websocket is not connected")
	}
	return sendElevenLabsJSON(ctx, conn, &b.ttsSendMu, value)
}

func sendElevenLabsJSON(ctx context.Context, conn *websocket.Conn, sendMu *sync.Mutex, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	sendMu.Lock()
	defer sendMu.Unlock()
	return conn.Write(ctx, websocket.MessageText, data)
}

type elevenLabsSTTEvent struct {
	MessageType string `json:"message_type"`
	SessionID   string `json:"session_id"`
	Text        string `json:"text"`
	Error       string `json:"error"`
	Message     string `json:"message"`
}

func (b *elevenLabsVoiceBridge) handleSTTEvent(data []byte) {
	var event elevenLabsSTTEvent
	if err := json.Unmarshal(data, &event); err != nil {
		log.Printf("voicecall: ElevenLabs Scribe event decode error: %v", err)
		return
	}
	switch event.MessageType {
	case "session_started":
		b.readyOnce.Do(func() { close(b.ready) })
		log.Printf("voicecall: ElevenLabs dynamic realtime connected scribe_session_id=%s voice=%s tts_model=%s llm=%s/%s", event.SessionID, b.cfg.VoiceID, b.cfg.ModelID, normalizeLLMProvider(b.cfg.LLMProvider), b.cfg.LLMModel)
		b.greetingOnce.Do(func() { go b.triggerGreeting() })
	case "partial_transcript":
		if strings.TrimSpace(event.Text) != "" {
			b.markCallerSpoke()
			b.markSegmentStart()
			b.markPartial()
			if b.hasCurrentTurn() {
				log.Println("voicecall: caller speech detected; interrupting current ElevenLabs response")
				b.interruptCurrentTurn()
			}
		}
	case "committed_transcript":
		if text := strings.TrimSpace(event.Text); text != "" {
			b.markCallerSpoke()
			if waited := b.takeSegmentStart(); waited > 0 {
				log.Printf("voicecall: latency provider=11labs stage=transcript_commit duration_ms=%d", waited.Milliseconds())
			}
			if waited := b.takeEndOfSpeech(); waited > 0 {
				log.Printf("voicecall: latency provider=11labs stage=endpoint_commit duration_ms=%d", waited.Milliseconds())
			}
			log.Printf("voicecall: caller said: %s", text)
			b.interruptCurrentTurn()
			b.startLLMTurn(text, true)
		}
	case "auth_error", "quota_exceeded", "transcriber_error", "input_error", "error", "unaccepted_terms", "rate_limited", "queue_overflow", "resource_exhausted", "session_time_limit_exceeded", "chunk_size_exceeded", "insufficient_audio_activity":
		log.Printf("voicecall: ElevenLabs Scribe error type=%s error=%s message=%s", event.MessageType, event.Error, event.Message)
	}
}

func (b *elevenLabsVoiceBridge) triggerGreeting() {
	if delay := time.Duration(b.cfg.WelcomeDelayMs) * time.Millisecond; delay > 0 {
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(delay):
		}
	}
	// If the caller starts speaking during the configured greeting delay, their
	// turn wins. Starting a greeting here would cancel the real user response.
	if b.callerAlreadySpoke() || b.hasCurrentTurn() {
		return
	}
	switch b.cfg.BeginMessageMode {
	case "agent_speaks_first":
		if message := strings.TrimSpace(b.cfg.WelcomeMessage); message != "" {
			b.startSpokenTurn(message, true)
		}
	case "agent_speaks_first_with_model_generated_message":
		b.startLLMTurn("Greet the caller now with one brief, natural opening sentence. Do not mention these instructions.", false)
	}
}

func (b *elevenLabsVoiceBridge) startLLMTurn(input string, recordUser bool) {
	if b.ctx.Err() != nil {
		return
	}
	if recordUser {
		b.appendHistory(conversationMessage{Role: "user", Content: input})
	}
	messages := b.historySnapshot()
	if !recordUser {
		messages = append(messages, conversationMessage{Role: "user", Content: input})
	}
	turn, _ := b.beginTurn()
	go b.completeLLMTurn(turn, messages)
}

// completeLLMTurn feeds the reply into the open TTS context clause by clause as
// the model writes it. ElevenLabs starts synthesizing on the first words it
// receives, so the caller hears the answer begin while the rest is still being
// generated instead of after the whole reply is finished.
func (b *elevenLabsVoiceBridge) completeLLMTurn(turn *elevenLabsTurn, messages []conversationMessage) {
	var chunker speechChunker
	opened := false
	var sendErr error
	llmStartedAt := time.Now()
	var firstToken sync.Once
	send := func(text string) {
		if strings.TrimSpace(text) == "" || sendErr != nil {
			return
		}
		if !opened {
			if err := b.openTTSContext(turn.id); err != nil {
				sendErr = err
				return
			}
			opened = true
		}
		if err := b.sendTTSText(turn.id, text); err != nil {
			sendErr = err
		}
	}

	reply, err := b.cfg.LLM.ReplyStream(turn.ctx, b.cfg.LLMProvider, b.cfg.LLMModel, b.cfg.LLMTemperature, b.cfg.Instructions, messages, func(delta string) {
		if delta != "" {
			firstToken.Do(func() {
				log.Printf("voicecall: latency provider=11labs turn=%d stage=first_model_token duration_ms=%d total_ms=%d", turn.id, time.Since(llmStartedAt).Milliseconds(), time.Since(turn.startedAt).Milliseconds())
			})
		}
		for _, chunk := range chunker.Push(delta) {
			send(chunk)
		}
	})
	if err != nil {
		if turn.ctx.Err() == nil && b.ctx.Err() == nil {
			log.Printf("voicecall: dynamic conversation LLM error provider=%s model=%s: %v", b.cfg.LLMProvider, b.cfg.LLMModel, err)
		}
		b.clearTurn(turn.id)
		return
	}
	if !b.turnIsCurrent(turn.id) {
		return
	}
	send(chunker.Flush())

	reply = strings.TrimSpace(reply)
	if reply == "" || !opened {
		if sendErr != nil && turn.ctx.Err() == nil && b.ctx.Err() == nil {
			log.Printf("voicecall: ElevenLabs TTS send error: %v", sendErr)
		}
		b.clearTurn(turn.id)
		return
	}
	log.Printf("voicecall: AI reply transcript: %s", reply)
	b.appendHistory(conversationMessage{Role: "assistant", Content: reply})
	if sendErr == nil {
		sendErr = b.closeTTSContext(turn.id)
	}
	if sendErr != nil && turn.ctx.Err() == nil && b.ctx.Err() == nil {
		log.Printf("voicecall: ElevenLabs TTS send error: %v", sendErr)
		b.clearTurn(turn.id)
	}
}

func (b *elevenLabsVoiceBridge) startSpokenTurn(text string, recordAssistant bool) {
	b.interruptCurrentTurn()
	turn, _ := b.beginTurn()
	if recordAssistant {
		b.appendHistory(conversationMessage{Role: "assistant", Content: text})
	}
	if err := b.speakTurn(turn.id, text); err != nil {
		log.Printf("voicecall: ElevenLabs greeting TTS send error: %v", err)
		b.clearTurn(turn.id)
	}
}

// speakTurn speaks one piece of text that is already complete — the fixed
// welcome message.
func (b *elevenLabsVoiceBridge) speakTurn(turnID int64, text string) error {
	if err := b.openTTSContext(turnID); err != nil {
		return err
	}
	if err := b.sendTTSText(turnID, text); err != nil {
		return err
	}
	return b.closeTTSContext(turnID)
}

// turnContext returns the TTS context and cancellation scope of turnID, or
// false once that turn has been superseded or the call has ended.
func (b *elevenLabsVoiceBridge) turnContext(turnID int64) (string, context.Context, bool) {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	if b.current == nil || b.current.id != turnID {
		return "", nil, false
	}
	return b.current.contextID, b.current.ctx, true
}

// openTTSContext opens the turn's TTS context and fixes its voice settings.
// Marking the turn ttsOpen is what lets a barge-in close the context.
func (b *elevenLabsVoiceBridge) openTTSContext(turnID int64) error {
	b.turnMu.Lock()
	if b.current == nil || b.current.id != turnID {
		b.turnMu.Unlock()
		return context.Canceled
	}
	b.current.ttsOpen = true
	contextID := b.current.contextID
	turnCtx := b.current.ctx
	b.turnMu.Unlock()

	return b.sendTTSJSONContext(turnCtx, map[string]any{
		"context_id": contextID,
		"text":       " ",
		"voice_settings": map[string]any{
			"speed":            clampElevenLabsSpeed(b.cfg.Speed),
			"stability":        0.5,
			"similarity_boost": 0.8,
			// Speaker boost adds synthesis latency for a marginal similarity
			// gain; on a live call the milliseconds matter more.
			"use_speaker_boost": false,
		},
	})
}

// sendTTSText streams one more piece of the reply into the turn's context. The
// trailing space marks a word boundary, which is what ElevenLabs needs to start
// speaking a piece without waiting for the next one.
func (b *elevenLabsVoiceBridge) sendTTSText(turnID int64, text string) error {
	contextID, turnCtx, ok := b.turnContext(turnID)
	if !ok {
		return context.Canceled
	}
	return b.sendTTSJSONContext(turnCtx, map[string]any{
		"context_id": contextID,
		"text":       strings.TrimSpace(text) + " ",
	})
}

// closeTTSContext tells ElevenLabs no more text is coming for this turn.
func (b *elevenLabsVoiceBridge) closeTTSContext(turnID int64) error {
	contextID, turnCtx, ok := b.turnContext(turnID)
	if !ok {
		return context.Canceled
	}
	if err := b.sendTTSJSONContext(turnCtx, map[string]any{
		"context_id": contextID,
		"flush":      true,
	}); err != nil {
		return err
	}
	return b.sendTTSJSONContext(turnCtx, map[string]any{
		"context_id":    contextID,
		"close_context": true,
	})
}

func (b *elevenLabsVoiceBridge) beginTurn() (*elevenLabsTurn, context.Context) {
	turnCtx, cancel := context.WithCancel(b.ctx)
	b.turnMu.Lock()
	b.nextTurnID++
	turn := &elevenLabsTurn{
		id:        b.nextTurnID,
		contextID: fmt.Sprintf("call_turn_%d", b.nextTurnID),
		startedAt: time.Now(),
		ctx:       turnCtx,
		cancel:    cancel,
	}
	b.current = turn
	b.turnMu.Unlock()
	return turn, turnCtx
}

func (b *elevenLabsVoiceBridge) hasCurrentTurn() bool {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return b.current != nil
}

func (b *elevenLabsVoiceBridge) markCallerSpoke() {
	b.turnMu.Lock()
	b.callerSpoke = true
	b.turnMu.Unlock()
}

func (b *elevenLabsVoiceBridge) callerAlreadySpoke() bool {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return b.callerSpoke
}

func (b *elevenLabsVoiceBridge) turnIsCurrent(turnID int64) bool {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return b.current != nil && b.current.id == turnID
}

func (b *elevenLabsVoiceBridge) clearTurn(turnID int64) {
	b.turnMu.Lock()
	if b.current != nil && b.current.id == turnID {
		b.current.cancel()
		b.current = nil
	}
	b.turnMu.Unlock()
}

func (b *elevenLabsVoiceBridge) interruptCurrentTurn() {
	var contextID string
	var closeContext bool
	b.turnMu.Lock()
	if b.current != nil {
		b.current.cancel()
		contextID = b.current.contextID
		closeContext = b.current.ttsOpen
		b.ignored[contextID] = true
		b.current = nil
	}
	b.turnMu.Unlock()

	if closeContext {
		_ = b.sendTTSJSON(map[string]any{"context_id": contextID, "close_context": true})
	}
	b.finishOutput(contextID, true)
	if contextID != "" && b.stopPlayback != nil {
		b.stopPlayback()
	}
}

func (b *elevenLabsVoiceBridge) appendHistory(message conversationMessage) {
	b.turnMu.Lock()
	b.history = append(b.history, message)
	if len(b.history) > maxConversationMessages {
		b.history = append([]conversationMessage(nil), b.history[len(b.history)-maxConversationMessages:]...)
	}
	b.transcript = append(b.transcript, message)
	b.turnMu.Unlock()
}

func (b *elevenLabsVoiceBridge) historySnapshot() []conversationMessage {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return append([]conversationMessage(nil), b.history...)
}

// Transcript returns the full user/assistant conversation for the call, in the
// order the turns happened, for post-call persistence.
func (b *elevenLabsVoiceBridge) Transcript() []conversationMessage {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return append([]conversationMessage(nil), b.transcript...)
}

type elevenLabsTTSEvent struct {
	Audio     string          `json:"audio"`
	ContextID string          `json:"contextId"`
	IsFinal   bool            `json:"is_final"`
	Error     json.RawMessage `json:"error"`
}

func (b *elevenLabsVoiceBridge) readTTSLoop(conn *websocket.Conn) {
	for {
		messageType, data, err := conn.Read(b.ctx)
		if err != nil {
			if b.ctx.Err() == nil && websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				log.Printf("voicecall: ElevenLabs TTS read error: %v", err)
				b.cancel()
			}
			return
		}
		if messageType != websocket.MessageText {
			continue
		}
		var event elevenLabsTTSEvent
		if err := json.Unmarshal(data, &event); err != nil {
			log.Printf("voicecall: ElevenLabs TTS event decode error: %v", err)
			continue
		}
		if len(event.Error) > 0 && string(event.Error) != "null" {
			log.Printf("voicecall: ElevenLabs TTS error: %s", strings.TrimSpace(string(data)))
			continue
		}
		if event.Audio != "" {
			b.appendOutputAudio(event.ContextID, event.Audio)
		}
		if event.IsFinal {
			b.finishOutput(event.ContextID, false)
			b.finishTurnContext(event.ContextID)
		}
	}
}

func (b *elevenLabsVoiceBridge) finishTurnContext(contextID string) {
	b.turnMu.Lock()
	if b.current != nil && b.current.contextID == contextID {
		b.current.cancel()
		b.current = nil
	}
	delete(b.ignored, contextID)
	b.turnMu.Unlock()
}

func (b *elevenLabsVoiceBridge) contextIsCurrent(contextID string) bool {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return !b.ignored[contextID] && b.current != nil && b.current.contextID == contextID
}

func (b *elevenLabsVoiceBridge) contextStartedAt(contextID string) time.Time {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	if b.current != nil && b.current.contextID == contextID {
		return b.current.startedAt
	}
	return time.Time{}
}

func (b *elevenLabsVoiceBridge) appendOutputAudio(contextID, encoded string) {
	if encoded == "" || !b.contextIsCurrent(contextID) {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		log.Printf("voicecall: ElevenLabs TTS audio decode error: %v", err)
		return
	}
	samples := applyGain(pcm16LEToFloat32(raw), b.cfg.Volume)

	b.outputMu.Lock()
	if b.output != nil && b.outputContext != contextID {
		b.outputMu.Unlock()
		b.finishOutput("", true)
		b.outputMu.Lock()
	}
	if b.output == nil || b.output.IsClosed() {
		b.output = newStreamingAudioSource()
		b.outputStarted = false
		b.outputContext = contextID
	}
	var play meowcaller.AudioSource
	if b.output.Append(samples) && !b.outputStarted && b.output.BufferedSamples() >= meowcaller.FrameSamples {
		b.outputStarted = true
		play = b.output
	}
	if b.outputTimer != nil {
		b.outputTimer.Stop()
	}
	b.outputTimer = time.AfterFunc(2*time.Second, func() { b.finishOutput(contextID, false) })
	b.outputMu.Unlock()

	if play != nil && b.playSource != nil {
		if startedAt := b.contextStartedAt(contextID); !startedAt.IsZero() {
			log.Printf("voicecall: latency provider=11labs stage=first_audio_playback total_ms=%d", time.Since(startedAt).Milliseconds())
		}
		b.playSource(play)
	}
}

func (b *elevenLabsVoiceBridge) finishOutput(contextID string, force bool) {
	var source *streamingAudioSource
	var play meowcaller.AudioSource
	b.outputMu.Lock()
	if b.output != nil && (force || contextID == "" || b.outputContext == contextID) {
		if b.outputTimer != nil {
			b.outputTimer.Stop()
			b.outputTimer = nil
		}
		source = b.output
		if !b.outputStarted && source.BufferedSamples() > 0 {
			b.outputStarted = true
			play = source
		}
		b.output = nil
		b.outputStarted = false
		b.outputContext = ""
	}
	b.outputMu.Unlock()
	if source != nil {
		_ = source.Close()
	}
	if play != nil && b.playSource != nil {
		b.playSource(play)
	}
}

// markPartial records that the transcriber is still recognizing words.
func (b *elevenLabsVoiceBridge) markPartial() {
	b.turnMu.Lock()
	b.lastPartialAt = time.Now()
	b.turnMu.Unlock()
}

// markEndOfSpeech records when the caller was judged to have stopped talking,
// so the transcriber's own finalization can be measured apart from the wait for
// the caller to finish their sentence.
func (b *elevenLabsVoiceBridge) markEndOfSpeech(at time.Time) {
	b.turnMu.Lock()
	b.endOfSpeechAt = at
	b.lastPartialAt = time.Time{}
	b.turnMu.Unlock()
}

// takeEndOfSpeech reports how long the transcriber took to finalize the turn
// that just committed, and clears it for the next one.
func (b *elevenLabsVoiceBridge) takeEndOfSpeech() time.Duration {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	if b.endOfSpeechAt.IsZero() {
		return 0
	}
	waited := time.Since(b.endOfSpeechAt)
	b.endOfSpeechAt = time.Time{}
	return waited
}

// markSegmentStart records the first sign that the caller is speaking again, so
// the wait for the transcriber to call their turn finished can be measured.
func (b *elevenLabsVoiceBridge) markSegmentStart() {
	b.turnMu.Lock()
	if b.segmentStartedAt.IsZero() {
		b.segmentStartedAt = time.Now()
	}
	b.turnMu.Unlock()
}

// takeSegmentStart reports how long the segment that just committed was open
// and clears it for the next one.
func (b *elevenLabsVoiceBridge) takeSegmentStart() time.Duration {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	if b.segmentStartedAt.IsZero() {
		return 0
	}
	waited := time.Since(b.segmentStartedAt)
	b.segmentStartedAt = time.Time{}
	return waited
}
