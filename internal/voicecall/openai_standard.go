package voicecall

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
)

const (
	defaultOpenAITranscriptionURL = "https://api.openai.com/v1/audio/transcriptions"
	defaultOpenAISpeechURL        = "https://api.openai.com/v1/audio/speech"
	defaultOpenAIRealtimeURL      = "wss://api.openai.com/v1/realtime"
	// The mini Realtime model is the latency-oriented production default for
	// phone calls. Operators can select a larger Realtime model through env when
	// quality matters more than response time.
	defaultOpenAIRealtimeModel = "gpt-realtime-2.1-mini"
	openAIRealtimeSampleRate   = 24000
	defaultOpenAITTSModel      = "gpt-4o-mini-tts"
	defaultOpenAIVoice         = "coral"
	defaultOpenAIVADThreshold  = 0.015
	// How long the caller has to stop making sound before their turn is treated
	// as finished. Every millisecond here is silence the caller sits through on
	// every single turn, so it is kept just above a natural mid-sentence pause
	// rather than at the half second most speech-to-speech APIs default to.
	defaultOpenAISilenceMS      = 250
	defaultOpenAIPrefixMS       = 200
	defaultOpenAIMinSpeechMS    = 180
	defaultOpenAIMaxUtteranceMS = 30000

	// How many of a reply's speech chunks may be synthesized at once. A small
	// look-ahead keeps the next sentence's audio ready before the current one
	// finishes playing without firing every request in the reply at the API.
	ttsLookahead = 2
)

// openAIStandardConfig describes the request-based OpenAI call pipeline:
// bounded caller audio -> Audio Transcriptions -> selected text LLM -> Audio
// Speech. It deliberately contains no Realtime API endpoint or model.
type openAIStandardConfig struct {
	APIKey             string
	TranscriptionURL   string
	SpeechURL          string
	TranscriptionModel string
	TTSModel           string
	Voice              string
	VoiceInstructions  string
	Speed              float64
	Volume             float64
	Language           string
	LLM                conversationLLM
	LLMProvider        string
	LLMModel           string
	LLMTemperature     float64
	Instructions       string
	// Tools are the functions the model may call during the call — the agent's
	// knowledge base lookup — or nil when it answers from its prompt alone. The
	// Realtime session declares them itself; the request-based pipeline reaches
	// them through LLM, which carries the same runner.
	Tools             toolRunner
	BeginMessageMode  string
	WelcomeMessage    string
	WelcomeDelayMs    int
	VADThreshold      float64
	SilenceMS         int
	PrefixMS          int
	MinSpeechMS       int
	MaxUtteranceMS    int
	HTTPClient        *http.Client
	RealtimeEnabled   bool
	RealtimeURL       string
	RealtimeModel     string
	RealtimeVADMode   string
	RealtimeEagerness string
	RealtimeThreshold float64
	RealtimeSilenceMS int
	RealtimePrefixMS  int
}

func loadOpenAIStandardConfig() openAIStandardConfig {
	return openAIStandardConfig{
		APIKey:             strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		TranscriptionURL:   envString("OPENAI_TRANSCRIPTIONS_URL", defaultOpenAITranscriptionURL),
		SpeechURL:          envString("OPENAI_SPEECH_URL", defaultOpenAISpeechURL),
		TranscriptionModel: envString("OPENAI_TRANSCRIBE_MODEL", defaultTranscribeModel),
		TTSModel:           normalizeOpenAITTSModel(envString("OPENAI_TTS_MODEL", defaultOpenAITTSModel)),
		Voice:              normalizeOpenAITTSVoice(envString("OPENAI_TTS_VOICE", defaultOpenAIVoice)),
		Speed:              clampSpeed(envFloat("OPENAI_TTS_SPEED", defaultSpeed)),
		Volume:             clampVolume(envFloat("OPENAI_TTS_VOLUME", defaultVolume)),
		LLM:                loadConversationLLM(),
		Instructions:       envString("OPENAI_RESPONSE_INSTRUCTIONS", defaultInstructions),
		VADThreshold:       envFloat("OPENAI_VAD_RMS_THRESHOLD", defaultOpenAIVADThreshold),
		SilenceMS:          envInt("OPENAI_VAD_SILENCE_MS", defaultOpenAISilenceMS),
		PrefixMS:           envInt("OPENAI_VAD_PREFIX_MS", defaultOpenAIPrefixMS),
		MinSpeechMS:        envInt("OPENAI_VAD_MIN_SPEECH_MS", defaultOpenAIMinSpeechMS),
		MaxUtteranceMS:     envInt("OPENAI_VAD_MAX_UTTERANCE_MS", defaultOpenAIMaxUtteranceMS),
		HTTPClient:         voiceHTTPClient(60 * time.Second),
		RealtimeEnabled:    envBoolDefault("OPENAI_REALTIME_ENABLED", true),
		RealtimeURL:        envString("OPENAI_REALTIME_URL", defaultOpenAIRealtimeURL),
		RealtimeModel:      envString("OPENAI_REALTIME_MODEL", defaultOpenAIRealtimeModel),
		RealtimeVADMode:    envString("OPENAI_REALTIME_VAD_MODE", "server_vad"),
		RealtimeEagerness:  envString("OPENAI_REALTIME_EAGERNESS", "high"),
		RealtimeThreshold:  envFloat("OPENAI_REALTIME_VAD_THRESHOLD", 0.5),
		RealtimeSilenceMS:  envInt("OPENAI_REALTIME_SILENCE_MS", defaultOpenAISilenceMS),
		RealtimePrefixMS:   envInt("OPENAI_REALTIME_PREFIX_PADDING_MS", 120),
	}
}

func (c openAIStandardConfig) Enabled() bool {
	key := strings.TrimSpace(c.APIKey)
	return key != "" && key != placeholderAPIKey && envBoolDefault("OPENAI_VOICE_ENABLED", true)
}

var openAITTSVoices = map[string]bool{
	"alloy": true, "ash": true, "ballad": true, "coral": true, "echo": true,
	"fable": true, "nova": true, "onyx": true, "sage": true, "shimmer": true,
	"verse": true, "marin": true, "cedar": true,
}

var legacyOpenAITTSVoices = map[string]bool{
	"alloy": true, "ash": true, "coral": true, "echo": true, "fable": true,
	"nova": true, "onyx": true, "sage": true, "shimmer": true,
}

func normalizeOpenAITTSVoice(voice string) string {
	voice = strings.ToLower(strings.TrimSpace(voice))
	if openAITTSVoices[voice] {
		return voice
	}
	return defaultOpenAIVoice
}

func normalizeOpenAITTSVoiceForModel(voice, model string) string {
	voice = normalizeOpenAITTSVoice(voice)
	if model == "tts-1" || model == "tts-1-hd" {
		if !legacyOpenAITTSVoices[voice] {
			return defaultOpenAIVoice
		}
	}
	return voice
}

func normalizeOpenAITTSModel(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "tts-1", "tts-1-hd", "gpt-4o-mini-tts":
		return strings.ToLower(strings.TrimSpace(model))
	default:
		return defaultOpenAITTSModel
	}
}

type openAIStandardTurn struct {
	id        int64
	startedAt time.Time
	ctx       context.Context
	cancel    context.CancelFunc
}

// openAIStandardVoiceBridge performs local turn detection so it can use the
// request-based Audio APIs without opening a speech-to-speech WebSocket.
type openAIStandardVoiceBridge struct {
	cfg          openAIStandardConfig
	playSource   func(meowcaller.AudioSource)
	stopPlayback func()

	ctx    context.Context
	cancel context.CancelFunc
	frames chan []float32
	done   chan struct{}

	mu           sync.Mutex
	closed       bool
	callerSpoke  bool
	greetingOnce sync.Once

	turnMu     sync.Mutex
	nextTurnID int64
	current    *openAIStandardTurn
	history    []conversationMessage
	// transcript is the full, uncapped conversation for post-call persistence.
	// history is trimmed to the model's context window; transcript is not.
	transcript []conversationMessage

	outputMu   sync.Mutex
	output     *streamingAudioSource
	outputTurn int64
}

func newOpenAIStandardVoiceBridge(cfg openAIStandardConfig, playSource func(meowcaller.AudioSource), stopPlayback func()) *openAIStandardVoiceBridge {
	ctx, cancel := context.WithCancel(context.Background())
	b := &openAIStandardVoiceBridge{
		cfg:          cfg,
		playSource:   playSource,
		stopPlayback: stopPlayback,
		ctx:          ctx,
		cancel:       cancel,
		frames:       make(chan []float32, 64),
		done:         make(chan struct{}),
	}
	// The caller is still hearing the call connect, so spend that time opening
	// the connections the first turn will need.
	go prewarmEndpoints(ctx, b.httpClient(), cfg.TranscriptionURL, cfg.SpeechURL, cfg.LLM.endpointFor(cfg.LLMProvider))
	go b.run()
	return b
}

func (b *openAIStandardVoiceBridge) WriteFrame(frame []float32) error {
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

func (b *openAIStandardVoiceBridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	close(b.frames)
	b.mu.Unlock()
	b.cancel()
	b.interruptCurrentTurn()
	<-b.done
	return nil
}

func (b *openAIStandardVoiceBridge) run() {
	defer close(b.done)
	defer b.finishOutput(0, true)
	b.greetingOnce.Do(func() { go b.triggerGreeting() })

	threshold := b.cfg.VADThreshold
	if threshold <= 0 {
		threshold = defaultOpenAIVADThreshold
	}
	silenceSamples := millisecondsToSamples(b.cfg.SilenceMS, defaultOpenAISilenceMS)
	prefixLimit := millisecondsToSamples(b.cfg.PrefixMS, defaultOpenAIPrefixMS)
	minSpeechSamples := millisecondsToSamples(b.cfg.MinSpeechMS, defaultOpenAIMinSpeechMS)
	maxUtteranceSamples := millisecondsToSamples(b.cfg.MaxUtteranceMS, defaultOpenAIMaxUtteranceMS)

	var prefix, utterance []float32
	var trailingSilence, voicedSamples int
	speaking := false
	commit := func() {
		if speaking && voicedSamples >= minSpeechSamples {
			audio := append([]float32(nil), utterance...)
			turn := b.beginTurn()
			go b.processUserTurn(turn, audio)
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
			voiced := frameRMS(frame) >= threshold
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

func millisecondsToSamples(value, fallback int) int {
	if value <= 0 {
		value = fallback
	}
	return value * audioSampleRate / 1000
}

func frameRMS(frame []float32) float64 {
	if len(frame) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range frame {
		v := float64(sample)
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(frame)))
}

func (b *openAIStandardVoiceBridge) triggerGreeting() {
	if delay := time.Duration(b.cfg.WelcomeDelayMs) * time.Millisecond; delay > 0 {
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(delay):
		}
	}
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

func (b *openAIStandardVoiceBridge) processUserTurn(turn *openAIStandardTurn, audio []float32) {
	text, err := b.transcribe(turn.ctx, audio)
	log.Printf("voicecall: latency provider=openai turn=%d stage=transcription duration_ms=%d", turn.id, time.Since(turn.startedAt).Milliseconds())
	if err != nil {
		if turn.ctx.Err() == nil && b.ctx.Err() == nil {
			log.Printf("voicecall: OpenAI transcription error: %v", err)
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

func (b *openAIStandardVoiceBridge) startLLMTurn(input string, recordUser bool) {
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
	turn := b.beginTurn()
	go b.completeLLMTurn(turn, messages)
}

// completeLLMTurn speaks the reply as the model writes it. Each clause the
// chunker releases is synthesized right away, so the caller hears the opening
// words while the rest of the answer is still being generated instead of after
// it — which is where most of a turn's latency used to go.
func (b *openAIStandardVoiceBridge) completeLLMTurn(turn *openAIStandardTurn, messages []conversationMessage) {
	speech := b.newSpeechPipeline(turn)
	var chunker speechChunker
	llmStartedAt := time.Now()
	var firstToken sync.Once
	reply, err := b.cfg.LLM.ReplyStream(turn.ctx, b.cfg.LLMProvider, b.cfg.LLMModel, b.cfg.LLMTemperature, b.cfg.Instructions, messages, func(delta string) {
		if delta != "" {
			firstToken.Do(func() {
				log.Printf("voicecall: latency provider=openai turn=%d stage=first_model_token duration_ms=%d total_ms=%d", turn.id, time.Since(llmStartedAt).Milliseconds(), time.Since(turn.startedAt).Milliseconds())
			})
		}
		for _, chunk := range chunker.Push(delta) {
			speech.Speak(chunk)
		}
	})
	if err != nil {
		speech.Cancel()
		if turn.ctx.Err() == nil && b.ctx.Err() == nil {
			log.Printf("voicecall: dynamic conversation LLM error provider=%s model=%s: %v", b.cfg.LLMProvider, b.cfg.LLMModel, err)
		}
		b.clearTurn(turn.id)
		return
	}
	if !b.turnIsCurrent(turn.id) {
		speech.Cancel()
		return
	}
	speech.Speak(chunker.Flush())
	speech.CloseInput()

	reply = strings.TrimSpace(reply)
	log.Printf("voicecall: AI reply transcript: %s", reply)
	b.appendHistory(conversationMessage{Role: "assistant", Content: reply})
	if err := speech.Wait(); err != nil && turn.ctx.Err() == nil && b.ctx.Err() == nil {
		log.Printf("voicecall: OpenAI TTS error: %v", err)
	}
	b.clearTurn(turn.id)
}

func (b *openAIStandardVoiceBridge) startSpokenTurn(text string, recordAssistant bool) {
	turn := b.beginTurn()
	if recordAssistant {
		b.appendHistory(conversationMessage{Role: "assistant", Content: text})
	}
	go func() {
		if err := b.speak(turn, text); err != nil && turn.ctx.Err() == nil && b.ctx.Err() == nil {
			log.Printf("voicecall: OpenAI greeting TTS error: %v", err)
		}
		b.clearTurn(turn.id)
	}()
}

func (b *openAIStandardVoiceBridge) transcribe(ctx context.Context, samples []float32) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "caller.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(pcm16WAV(samples, audioSampleRate)); err != nil {
		return "", err
	}
	model := strings.TrimSpace(b.cfg.TranscriptionModel)
	if model == "" {
		model = defaultTranscribeModel
	}
	_ = w.WriteField("model", model)
	_ = w.WriteField("response_format", "json")
	if language := transcriptionLanguage(b.cfg.Language); language != "" {
		_ = w.WriteField("language", language)
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.TranscriptionURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := b.httpClient().Do(req)
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

// speak synthesizes one complete piece of text and plays it, for replies that
// are already written in full (the fixed welcome message).
func (b *openAIStandardVoiceBridge) speak(turn *openAIStandardTurn, text string) error {
	speech := b.newSpeechPipeline(turn)
	speech.Speak(text)
	speech.CloseInput()
	return speech.Wait()
}

// speechPipeline turns a sequence of text chunks into one continuous audio
// stream. Each chunk's Audio Speech request starts as soon as the chunk is
// queued — up to ttsLookahead at a time — while a single writer appends the
// results in submission order, so a later sentence is already being synthesized
// while the caller is still hearing the first one. Everything lands in one
// audio source, so the call hears one uninterrupted reply.
//
// Speak, CloseInput and Cancel are called in order from the goroutine driving
// the turn; the writer and per-chunk fetchers are the only other goroutines.
type speechPipeline struct {
	bridge *openAIStandardVoiceBridge
	turn   *openAIStandardTurn
	source *streamingAudioSource

	inFlight  chan struct{}
	chunks    chan *speechChunk
	done      chan struct{}
	closeOnce sync.Once
	started   bool

	errMu sync.Mutex
	err   error
}

type speechChunk struct {
	text string
	// audio carries raw PCM exactly as the API returns it; the writer resamples
	// so that conversion stays sequential across the whole reply.
	audio chan []byte
}

func (b *openAIStandardVoiceBridge) newSpeechPipeline(turn *openAIStandardTurn) *speechPipeline {
	p := &speechPipeline{
		bridge:   b,
		turn:     turn,
		source:   newStreamingAudioSource(),
		inFlight: make(chan struct{}, ttsLookahead),
		chunks:   make(chan *speechChunk, 32),
		done:     make(chan struct{}),
	}
	b.setOutput(turn.id, p.source)
	go p.write()
	return p
}

// Speak queues one piece of the reply. Empty text is ignored so callers can
// hand over a chunker's flush unconditionally.
func (p *speechPipeline) Speak(text string) {
	text = strings.TrimSpace(text)
	if text == "" || p.turn.ctx.Err() != nil {
		return
	}
	chunk := &speechChunk{text: text, audio: make(chan []byte, 64)}
	select {
	case p.chunks <- chunk:
		go p.fetch(chunk)
	case <-p.turn.ctx.Done():
	}
}

// CloseInput reports that the reply is complete; the pipeline finishes the
// chunks already queued.
func (p *speechPipeline) CloseInput() {
	p.closeOnce.Do(func() { close(p.chunks) })
}

// Cancel abandons the reply, dropping any audio that has not been played.
// Cancelling the turn is what stops synthesis requests that are still in
// flight, so this does not wait on the API for a response nobody will hear.
func (p *speechPipeline) Cancel() {
	p.turn.cancel()
	p.CloseInput()
	p.bridge.finishOutput(p.turn.id, true)
	<-p.done
}

// Wait blocks until the whole reply has been synthesized and played into the
// call, returning the first synthesis error if there was one. It stays
// cancellable throughout so a caller who barges in can stop audio that is
// still buffered.
func (p *speechPipeline) Wait() error {
	<-p.done
	p.bridge.finishOutput(p.turn.id, false)
	for p.source.BufferedSamples() > 0 {
		select {
		case <-p.turn.ctx.Done():
			return p.turn.ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return p.errValue()
}

func (p *speechPipeline) fetch(chunk *speechChunk) {
	defer close(chunk.audio)
	select {
	case p.inFlight <- struct{}{}:
	case <-p.turn.ctx.Done():
		return
	}
	defer func() { <-p.inFlight }()

	if err := p.bridge.synthesize(p.turn.ctx, chunk.text, chunk.audio); err != nil {
		p.setErr(err)
	}
}

// write is the single consumer: it converts each chunk's audio in submission
// order and starts playback the moment a full frame exists.
func (p *speechPipeline) write() {
	defer close(p.done)
	resampler := newSampleRateConverter(openAITTSSampleRate, audioSampleRate)
	for chunk := range p.chunks {
		var carry []byte
		for raw := range chunk.audio {
			if !p.bridge.turnIsCurrent(p.turn.id) {
				continue // drain the abandoned response without playing it
			}
			data := append(carry, raw...)
			carry = nil
			if len(data)%2 != 0 {
				carry = append(carry, data[len(data)-1])
				data = data[:len(data)-1]
			}
			if len(data) == 0 {
				continue
			}
			samples := applyGain(resampler.Process(pcm16LEToFloat32(data)), p.bridge.cfg.Volume)
			if p.source.Append(samples) {
				p.startPlaybackOnce(meowcaller.FrameSamples)
			}
		}
	}
	// A reply too short to fill one frame still has to be played.
	p.startPlaybackOnce(1)
}

func (p *speechPipeline) startPlaybackOnce(minSamples int) {
	if p.started || p.source.BufferedSamples() < minSamples {
		return
	}
	if !p.bridge.turnIsCurrent(p.turn.id) {
		return
	}
	p.started = true
	log.Printf("voicecall: latency provider=openai turn=%d stage=first_audio_playback total_ms=%d", p.turn.id, time.Since(p.turn.startedAt).Milliseconds())
	if p.bridge.playSource != nil {
		p.bridge.playSource(p.source)
	}
}

func (p *speechPipeline) setErr(err error) {
	p.errMu.Lock()
	if p.err == nil {
		p.err = err
	}
	p.errMu.Unlock()
}

func (p *speechPipeline) errValue() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.err
}

// synthesize streams one Audio Speech response into out as raw PCM.
func (b *openAIStandardVoiceBridge) synthesize(ctx context.Context, text string, out chan<- []byte) error {
	model := normalizeOpenAITTSModel(b.cfg.TTSModel)
	payload := map[string]any{
		"model":           model,
		"voice":           normalizeOpenAITTSVoiceForModel(b.cfg.Voice, model),
		"input":           text,
		"response_format": "pcm",
		"speed":           clampSpeed(b.cfg.Speed),
	}
	if model == "gpt-4o-mini-tts" && strings.TrimSpace(b.cfg.VoiceInstructions) != "" {
		payload["instructions"] = b.cfg.VoiceInstructions
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.SpeechURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(responseBody)))
	}

	buf := make([]byte, 8192)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			raw := make([]byte, n)
			copy(raw, buf[:n])
			select {
			case out <- raw:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return readErr
			}
			return nil
		}
	}
}

func (b *openAIStandardVoiceBridge) httpClient() *http.Client {
	if b.cfg.HTTPClient != nil {
		return b.cfg.HTTPClient
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (b *openAIStandardVoiceBridge) beginTurn() *openAIStandardTurn {
	b.interruptCurrentTurn()
	ctx, cancel := context.WithCancel(b.ctx)
	b.turnMu.Lock()
	b.nextTurnID++
	turn := &openAIStandardTurn{id: b.nextTurnID, startedAt: time.Now(), ctx: ctx, cancel: cancel}
	b.current = turn
	b.turnMu.Unlock()
	return turn
}

func (b *openAIStandardVoiceBridge) hasCurrentTurn() bool {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return b.current != nil
}

func (b *openAIStandardVoiceBridge) turnIsCurrent(turnID int64) bool {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return b.current != nil && b.current.id == turnID
}

func (b *openAIStandardVoiceBridge) clearTurn(turnID int64) {
	b.turnMu.Lock()
	if b.current != nil && b.current.id == turnID {
		b.current.cancel()
		b.current = nil
	}
	b.turnMu.Unlock()
}

func (b *openAIStandardVoiceBridge) interruptCurrentTurn() {
	var turnID int64
	b.turnMu.Lock()
	if b.current != nil {
		turnID = b.current.id
		b.current.cancel()
		b.current = nil
	}
	b.turnMu.Unlock()
	if turnID != 0 {
		b.finishOutput(turnID, true)
		if b.stopPlayback != nil {
			b.stopPlayback()
		}
	}
}

func (b *openAIStandardVoiceBridge) markCallerSpoke() {
	b.mu.Lock()
	b.callerSpoke = true
	b.mu.Unlock()
}

func (b *openAIStandardVoiceBridge) callerAlreadySpoke() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.callerSpoke
}

func (b *openAIStandardVoiceBridge) appendHistory(message conversationMessage) {
	b.turnMu.Lock()
	b.history = append(b.history, message)
	if len(b.history) > maxConversationMessages {
		b.history = append([]conversationMessage(nil), b.history[len(b.history)-maxConversationMessages:]...)
	}
	b.transcript = append(b.transcript, message)
	b.turnMu.Unlock()
}

func (b *openAIStandardVoiceBridge) historySnapshot() []conversationMessage {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return append([]conversationMessage(nil), b.history...)
}

// Transcript returns the full user/assistant conversation for the call, in the
// order the turns happened, for post-call persistence.
func (b *openAIStandardVoiceBridge) Transcript() []conversationMessage {
	b.turnMu.Lock()
	defer b.turnMu.Unlock()
	return append([]conversationMessage(nil), b.transcript...)
}

func (b *openAIStandardVoiceBridge) setOutput(turnID int64, source *streamingAudioSource) {
	b.outputMu.Lock()
	b.output = source
	b.outputTurn = turnID
	b.outputMu.Unlock()
}

func (b *openAIStandardVoiceBridge) finishOutput(turnID int64, force bool) {
	var source *streamingAudioSource
	b.outputMu.Lock()
	if b.output != nil && (force || turnID == 0 || b.outputTurn == turnID) {
		source = b.output
		b.output = nil
		b.outputTurn = 0
	}
	b.outputMu.Unlock()
	if source != nil {
		_ = source.Close()
	}
}

func transcriptionLanguage(locale string) string {
	language := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(language, "-_"); i >= 0 {
		language = language[:i]
	}
	if len(language) == 2 {
		return language
	}
	return ""
}

func pcm16WAV(samples []float32, sampleRate int) []byte {
	pcm := float32ToPCM16LE(samples)
	buf := bytes.NewBuffer(make([]byte, 0, 44+len(pcm)))
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+len(pcm)))
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate*2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(pcm)))
	buf.Write(pcm)
	return buf.Bytes()
}
