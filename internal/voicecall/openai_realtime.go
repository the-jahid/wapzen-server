package voicecall

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/purpshell/meowcaller"
)

// openAIRealtimeVoiceBridge is the low-latency OpenAI call path. It keeps one
// speech-to-speech WebSocket open for the call instead of serializing three
// request/response round trips for transcription, text generation, and TTS.
type openAIRealtimeVoiceBridge struct {
	cfg          openAIRealtimeConfig
	playSource   func(meowcaller.AudioSource)
	stopPlayback func()

	ctx    context.Context
	cancel context.CancelFunc
	frames chan []float32
	done   chan struct{}

	hooks bridgeHooks

	// ready is closed once the session has accepted its configuration (or the
	// bridge has failed before it could); readyErr says which. Answering an
	// inbound call waits on it, so the caller is not picked up into silence
	// while the WebSocket is still connecting, and a session that cannot start
	// at all leaves the phone ringing for a human instead.
	ready     chan struct{}
	readyOnce sync.Once
	readyErr  error

	mu           sync.Mutex
	conn         *websocket.Conn
	closed       bool
	callerSpoke  bool
	greetingOnce sync.Once
	sendMu       sync.Mutex
	// toolRounds counts the tool calls made since the caller last spoke, so a
	// model that keeps looking things up is eventually made to answer.
	toolRounds int
	// turnSeq counts the caller's turns (speech_started events). A tool round
	// remembers the value it started under, so its follow-up response.create is
	// dropped when the caller has spoken since — the turn detector then creates
	// the next response itself, and a second one would collide with it
	// ("conversation already has an active response") or answer a stale question.
	turnSeq int
	// responseActive is true between response.created and response.done, so a
	// response this bridge asks for itself never collides with one in flight.
	responseActive bool

	inputResampler  *sampleRateConverter
	outputResampler *sampleRateConverter

	outputMu           sync.Mutex
	output             *streamingAudioSource
	outputStarted      bool
	outputItemID       string
	outputContentIndex int

	latencyMu        sync.Mutex
	speechStoppedAt  time.Time
	firstAudioLogged bool

	// transcript accumulates both sides of the conversation for post-call
	// persistence: the caller's turns from input-audio transcription events and
	// the AI's turns from response transcript events, in arrival order.
	transcriptMu sync.Mutex
	transcript   []conversationMessage
}

// bridgeHooks lets the call that owns a bridge react to what happens inside the
// session. Every field is optional.
type bridgeHooks struct {
	// onCallerSpeech fires when the turn detector hears the caller start talking.
	onCallerSpeech func()
	// onFailure fires once if the session dies on its own — it never connected,
	// its configuration was rejected, or the WebSocket dropped mid-call — but
	// not when the bridge is closed because the call ended. A call whose AI is
	// gone is dead air, so the owner hangs it up.
	onFailure func(error)
}

func newOpenAIRealtimeVoiceBridge(cfg openAIRealtimeConfig, playSource func(meowcaller.AudioSource), stopPlayback func()) *openAIRealtimeVoiceBridge {
	return newOpenAIRealtimeVoiceBridgeWithHooks(cfg, playSource, stopPlayback, bridgeHooks{})
}

func newOpenAIRealtimeVoiceBridgeWithHooks(cfg openAIRealtimeConfig, playSource func(meowcaller.AudioSource), stopPlayback func(), hooks bridgeHooks) *openAIRealtimeVoiceBridge {
	ctx, cancel := context.WithCancel(context.Background())
	b := &openAIRealtimeVoiceBridge{
		cfg:          cfg,
		playSource:   playSource,
		stopPlayback: stopPlayback,
		hooks:        hooks,
		ready:        make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
		// Almost four seconds of WhatsApp audio can wait here while the call and
		// WebSocket finish connecting without losing the caller's opening words.
		frames:          make(chan []float32, 64),
		done:            make(chan struct{}),
		inputResampler:  newSampleRateConverter(audioSampleRate, openAIRealtimeSampleRate),
		outputResampler: newSampleRateConverter(openAIRealtimeSampleRate, audioSampleRate),
	}
	go b.run()
	return b
}

func (b *openAIRealtimeVoiceBridge) WriteFrame(frame []float32) error {
	cp := append([]float32(nil), frame...)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	select {
	case b.frames <- cp:
	default:
		// Preserve the most recent audio if setup exceeds the queue window.
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

func (b *openAIRealtimeVoiceBridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	close(b.frames)
	conn := b.conn
	b.mu.Unlock()

	b.cancel()
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "call ended")
	}
	<-b.done
	return nil
}

// Ready is closed once the session is configured or has failed; Err reports
// which after it closes.
func (b *openAIRealtimeVoiceBridge) Ready() <-chan struct{} { return b.ready }

// Err is nil when the session came up, or why it did not. Only meaningful once
// Ready is closed.
func (b *openAIRealtimeVoiceBridge) Err() error {
	select {
	case <-b.ready:
		return b.readyErr
	default:
		return nil
	}
}

func (b *openAIRealtimeVoiceBridge) markReady(err error) {
	b.readyOnce.Do(func() {
		b.readyErr = err
		close(b.ready)
	})
}

// fail records a session that died on its own and tells the owner, unless the
// bridge is being closed because the call ended — that is not a failure.
func (b *openAIRealtimeVoiceBridge) fail(err error) {
	b.markReady(err)
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed || b.ctx.Err() != nil {
		return
	}
	if b.hooks.onFailure != nil {
		b.hooks.onFailure(err)
	}
}

func (b *openAIRealtimeVoiceBridge) run() {
	defer close(b.done)
	defer b.finishOutput()
	// Whatever path ends the session, nobody may be left waiting on Ready.
	defer b.markReady(fmt.Errorf("realtime session ended before it was ready"))

	startedAt := time.Now()
	conn, resp, err := websocket.Dial(b.ctx, openAIRealtimeWebSocketURL(b.cfg.RealtimeURL, b.cfg.RealtimeModel), &websocket.DialOptions{
		HTTPHeader: openAIRealtimeHeaders(b.cfg.APIKey),
	})
	if err != nil {
		if b.ctx.Err() == nil {
			log.Printf("voicecall: OpenAI Realtime connect error: %v%s", err, websocketResponseBody(resp))
		}
		b.fail(fmt.Errorf("connect: %w", err))
		return
	}
	conn.SetReadLimit(4 << 20)
	b.mu.Lock()
	b.conn = conn
	b.mu.Unlock()
	defer conn.CloseNow()

	if err := b.sendJSON(conn, openAIRealtimeSessionUpdate(b.cfg)); err != nil {
		log.Printf("voicecall: OpenAI Realtime session update error: %v", err)
		b.fail(fmt.Errorf("session update: %w", err))
		return
	}
	log.Printf("voicecall: OpenAI Realtime connected model=%s connect_ms=%d vad=%s silence_ms=%d", b.cfg.RealtimeModel, time.Since(startedAt).Milliseconds(), b.cfg.RealtimeVADMode, b.cfg.RealtimeSilenceMS)

	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		b.sendAudioLoop(conn)
	}()

	for {
		messageType, data, err := conn.Read(b.ctx)
		if err != nil {
			ctxErr := b.ctx.Err()
			if ctxErr == nil {
				if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
					log.Printf("voicecall: OpenAI Realtime read error: %v", err)
				}
				// Reported before cancelling, so fail can still tell a session
				// that dropped on its own from one closed because the call ended.
				b.fail(fmt.Errorf("realtime session dropped: %w", err))
			}
			b.cancel()
			<-inputDone
			return
		}
		if messageType == websocket.MessageText {
			b.handleEvent(conn, data)
		}
	}
}

func openAIRealtimeHeaders(apiKey string) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+apiKey)
	return headers
}

func openAIRealtimeWebSocketURL(endpoint, model string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return endpoint
	}
	q := u.Query()
	if q.Get("model") == "" && strings.TrimSpace(model) != "" {
		q.Set("model", strings.TrimSpace(model))
	}
	u.RawQuery = q.Encode()
	return u.String()
}

var openAIRealtimeVoices = map[string]bool{
	"alloy": true, "ash": true, "ballad": true, "coral": true, "echo": true,
	"sage": true, "shimmer": true, "verse": true, "marin": true, "cedar": true,
}

func normalizeOpenAIRealtimeVoice(voice string) string {
	voice = strings.ToLower(strings.TrimSpace(voice))
	if openAIRealtimeVoices[voice] {
		return voice
	}
	switch voice {
	case "fable":
		return "ballad"
	case "nova":
		return "shimmer"
	case "onyx":
		return "ash"
	default:
		return defaultOpenAIVoice
	}
}

// openAIRealtimeMaxOutputTokens renders the per-response cap: a positive number
// is passed through (the API takes at most 4096), anything else means no cap.
func openAIRealtimeMaxOutputTokens(limit int) any {
	if limit <= 0 {
		return "inf"
	}
	return min(limit, 4096)
}

func openAIRealtimeSessionUpdate(cfg openAIRealtimeConfig) map[string]any {
	input := map[string]any{
		"format":         map[string]any{"type": "audio/pcm", "rate": openAIRealtimeSampleRate},
		"turn_detection": openAIRealtimeTurnDetection(cfg),
		// Transcribe the caller's audio so both sides of the conversation
		// can be persisted after the call. It is delivered asynchronously
		// as conversation.item.input_audio_transcription.completed events
		// and does not gate the spoken response.
		"transcription": openAIRealtimeTranscription(cfg),
	}
	if nr := normalizeNoiseReduction(cfg.NoiseReduction); nr != "" {
		input["noise_reduction"] = map[string]any{"type": nr}
	}
	session := map[string]any{
		"type":              "realtime",
		"model":             cfg.RealtimeModel,
		"instructions":      cfg.Instructions,
		"output_modalities": []string{"audio"},
		"max_output_tokens": openAIRealtimeMaxOutputTokens(cfg.MaxOutputTokens),
		"audio": map[string]any{
			"input": input,
			"output": map[string]any{
				"format": map[string]any{"type": "audio/pcm", "rate": openAIRealtimeSampleRate},
				"voice":  normalizeOpenAIRealtimeVoice(cfg.Voice),
				"speed":  min(clampSpeed(cfg.Speed), 1.5),
			},
		},
	}
	if tools := openAIRealtimeTools(cfg.Tools); len(tools) > 0 {
		session["tools"] = tools
		session["tool_choice"] = "auto"
	}
	return map[string]any{"type": "session.update", "session": session}
}

// openAIRealtimeTools renders the call's tools into the session's schema. The
// Realtime model calls them itself inside the live session, so this is the only
// place they have to be declared.
func openAIRealtimeTools(runner toolRunner) []map[string]any {
	if runner == nil {
		return nil
	}
	definitions := runner.Definitions()
	if len(definitions) == 0 {
		return nil
	}
	tools := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        definition.Name,
			"description": definition.Description,
			"parameters":  definition.Parameters,
		})
	}
	return tools
}

func openAIRealtimeTurnDetection(cfg openAIRealtimeConfig) map[string]any {
	if strings.EqualFold(strings.TrimSpace(cfg.RealtimeVADMode), "semantic_vad") {
		return map[string]any{
			"type":               "semantic_vad",
			"eagerness":          cfg.RealtimeEagerness,
			"create_response":    true,
			"interrupt_response": true,
		}
	}
	return map[string]any{
		"type":                "server_vad",
		"threshold":           cfg.RealtimeThreshold,
		"prefix_padding_ms":   max(cfg.RealtimePrefixMS, 60),
		"silence_duration_ms": max(cfg.RealtimeSilenceMS, 150),
		"create_response":     true,
		"interrupt_response":  true,
	}
}

// openAIRealtimeTranscription configures the input transcriber. Telling it which
// language to expect is what stops a Bengali (or any non-English) caller from
// being transcribed as English-sounding nonsense, which the model then answers
// as if it were what the caller said.
func openAIRealtimeTranscription(cfg openAIRealtimeConfig) map[string]any {
	transcription := map[string]any{"model": openAIRealtimeInputTranscriptionModel(cfg.TranscriptionModel)}
	if language := transcriptionLanguage(cfg.Language); language != "" {
		transcription["language"] = language
	}
	return transcription
}

// openAIRealtimeInputTranscriptionModel maps the configured transcription model
// onto one the Realtime input transcriber accepts, defaulting to the fast mini
// model when unset or unsupported (e.g. the diarizing model, which the input
// transcriber does not take).
func openAIRealtimeInputTranscriptionModel(model string) string {
	switch strings.TrimSpace(model) {
	case "gpt-4o-transcribe", "gpt-4o-mini-transcribe", "whisper-1":
		return strings.TrimSpace(model)
	default:
		return defaultTranscribeModel
	}
}

func (b *openAIRealtimeVoiceBridge) sendAudioLoop(conn *websocket.Conn) {
	for {
		select {
		case <-b.ctx.Done():
			return
		case frame, ok := <-b.frames:
			if !ok {
				return
			}
			samples := b.inputResampler.Process(frame)
			if len(samples) == 0 {
				continue
			}
			if err := b.sendJSON(conn, map[string]any{
				"type":  "input_audio_buffer.append",
				"audio": base64.StdEncoding.EncodeToString(float32ToPCM16LE(samples)),
			}); err != nil {
				if b.ctx.Err() == nil {
					log.Printf("voicecall: OpenAI Realtime audio send error: %v", err)
				}
				b.cancel()
				return
			}
		}
	}
}

func (b *openAIRealtimeVoiceBridge) sendJSON(conn *websocket.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	return conn.Write(b.ctx, websocket.MessageText, data)
}

type openAIRealtimeServerEvent struct {
	Type         string              `json:"type"`
	Delta        string              `json:"delta"`
	Transcript   string              `json:"transcript"`
	ItemID       string              `json:"item_id"`
	ContentIndex int                 `json:"content_index"`
	Error        *apiError           `json:"error"`
	Response     *openAIRealtimeResp `json:"response"`
}

type openAIRealtimeResp struct {
	Status        string          `json:"status"`
	StatusDetails json.RawMessage `json:"status_details"`
	// Output carries the response's items, which is where a function call the
	// model made arrives complete — name, call id, and arguments together.
	Output []openAIRealtimeItem `json:"output"`
}

type openAIRealtimeItem struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments"`
}

func (b *openAIRealtimeVoiceBridge) handleEvent(conn *websocket.Conn, data []byte) {
	var event openAIRealtimeServerEvent
	if err := json.Unmarshal(data, &event); err != nil {
		log.Printf("voicecall: OpenAI Realtime event decode error: %v", err)
		return
	}
	switch event.Type {
	case "error":
		message := event.Error.message()
		if message == "" {
			message = strings.TrimSpace(string(data))
		}
		log.Printf("voicecall: OpenAI Realtime error: %s", message)
		// An error before the session is configured means session.update itself
		// was refused, and the session would carry on with OpenAI's defaults —
		// no prompt, no language lock, no knowledge. Never talk to a caller like
		// that: fail the session so the call is left to a human.
		select {
		case <-b.ready:
		default:
			b.fail(fmt.Errorf("session configuration rejected: %s", message))
			b.cancel()
		}
	case "session.updated":
		log.Println("voicecall: OpenAI Realtime session ready")
		b.markReady(nil)
		b.greetingOnce.Do(func() { go b.triggerGreeting(conn) })
	case "input_audio_buffer.speech_started":
		b.mu.Lock()
		b.callerSpoke = true
		// Each thing the caller says gets its own tool budget, so a question that
		// needed two lookups does not leave the next one unable to look anything
		// up at all.
		b.toolRounds = 0
		b.turnSeq++
		b.mu.Unlock()
		b.interruptOutput(conn)
		if b.hooks.onCallerSpeech != nil {
			b.hooks.onCallerSpeech()
		}
	case "input_audio_buffer.speech_stopped":
		b.latencyMu.Lock()
		b.speechStoppedAt = time.Now()
		b.firstAudioLogged = false
		b.latencyMu.Unlock()
	case "input_audio_buffer.committed":
		// The caller's turn takes its place in the transcript now, in
		// conversation order; its text arrives later, asynchronously, and would
		// otherwise often land after the reply it prompted.
		b.reserveTranscriptSlot("user", event.ItemID)
	case "response.created":
		b.mu.Lock()
		b.responseActive = true
		b.mu.Unlock()
	case "response.output_audio.delta", "response.audio.delta":
		b.logFirstAudioDelta()
		b.appendOutputAudio(event)
	case "response.output_audio.done", "response.audio.done", "response.done", "response.cancelled":
		if event.Type == "response.done" || event.Type == "response.cancelled" {
			b.mu.Lock()
			b.responseActive = false
			b.mu.Unlock()
		}
		if event.Response != nil && event.Response.Status != "" && event.Response.Status != "completed" && event.Response.Status != "cancelled" {
			log.Printf("voicecall: OpenAI Realtime response status: %s %s", event.Response.Status, strings.TrimSpace(string(event.Response.StatusDetails)))
		}
		b.finishOutput()
		if event.Type == "response.done" && event.Response != nil {
			b.runFunctionCalls(conn, event.Response.Output)
		}
	case "conversation.item.input_audio_transcription.completed":
		if text := strings.TrimSpace(event.Transcript); text != "" {
			log.Printf("voicecall: caller said: %s", text)
			b.fillTranscript("user", event.ItemID, text)
		}
	case "response.output_audio_transcript.done", "response.audio_transcript.done":
		if text := strings.TrimSpace(event.Transcript); text != "" {
			log.Printf("voicecall: AI reply transcript: %s", text)
			b.fillTranscript("assistant", event.ItemID, text)
		}
	}
}

// Busy reports a response in flight — the model is generating a reply.
func (b *openAIRealtimeVoiceBridge) Busy() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.responseActive
}

// RequestResponse asks the model to speak now, with extra direction for this
// one response on top of the session's instructions (a response's own
// instructions replace the session's, so they are carried along). It does
// nothing and reports false while the session is not ready or a response is
// already in flight.
func (b *openAIRealtimeVoiceBridge) RequestResponse(extra string) bool {
	select {
	case <-b.ready:
		if b.readyErr != nil {
			return false
		}
	default:
		return false
	}
	b.mu.Lock()
	conn := b.conn
	busy := b.responseActive || b.closed
	b.mu.Unlock()
	if conn == nil || busy {
		return false
	}
	instructions := strings.TrimSpace(extra)
	if base := strings.TrimSpace(b.cfg.Instructions); base != "" {
		instructions = base + "\n\n" + instructions
	}
	if err := b.sendJSON(conn, map[string]any{
		"type": "response.create",
		"response": map[string]any{
			"output_modalities": []string{"audio"},
			"instructions":      instructions,
		},
	}); err != nil {
		if b.ctx.Err() == nil {
			log.Printf("voicecall: OpenAI Realtime response request error: %v", err)
		}
		return false
	}
	return true
}

// runFunctionCalls answers the function calls a finished response asked for and
// asks the model to continue. Unlike the request-based pipeline, the Realtime
// session drives its own turn: the results go back as conversation items and a
// fresh response.create is what turns them into speech.
//
// It runs in its own goroutine because the lookup is a network round trip and
// this is the WebSocket's read loop — blocking here would stop the session from
// reading anything, including the caller's own audio.
func (b *openAIRealtimeVoiceBridge) runFunctionCalls(conn *websocket.Conn, output []openAIRealtimeItem) {
	if b.cfg.Tools == nil {
		return
	}
	var calls []openAIRealtimeItem
	for _, item := range output {
		if item.Type == "function_call" && item.CallID != "" {
			calls = append(calls, item)
		}
	}
	if len(calls) == 0 {
		return
	}

	b.mu.Lock()
	b.toolRounds++
	rounds := b.toolRounds
	turn := b.turnSeq
	b.mu.Unlock()

	go func() {
		for _, call := range calls {
			result := b.cfg.Tools.Run(b.ctx, call.Name, call.Arguments)
			if b.ctx.Err() != nil {
				return
			}
			if err := b.sendJSON(conn, map[string]any{
				"type": "conversation.item.create",
				"item": map[string]any{
					"type":    "function_call_output",
					"call_id": call.CallID,
					"output":  result,
				},
			}); err != nil {
				if b.ctx.Err() == nil {
					log.Printf("voicecall: OpenAI Realtime tool result send error: %v", err)
				}
				return
			}
		}

		// The results are in the conversation either way. If the caller spoke
		// while the tool ran, the turn detector answers them itself (and sees the
		// results); asking for another response here would collide with it or
		// answer the question they have since moved on from.
		b.mu.Lock()
		superseded := b.turnSeq != turn
		busy := b.responseActive
		b.mu.Unlock()
		if superseded || busy {
			log.Printf("voicecall: tool results delivered; skipping follow-up response (caller spoke=%t, response in flight=%t)", superseded, busy)
			return
		}

		response := map[string]any{"output_modalities": []string{"audio"}}
		if rounds >= maxToolRounds {
			// The budget is spent: the model answers from what it already has
			// rather than looking anything else up while the caller waits.
			response["tool_choice"] = "none"
		}
		if err := b.sendJSON(conn, map[string]any{"type": "response.create", "response": response}); err != nil && b.ctx.Err() == nil {
			log.Printf("voicecall: OpenAI Realtime tool response error: %v", err)
		}
	}()
}

// openAIRealtimeGreetingInstruction renders the opening line the agent is asked
// to speak, or "" when it waits for the caller instead.
//
// A response's own instructions REPLACE the session's for that response, so
// sending the greeting line by itself would drop the agent's prompt — the
// language lock included — and the call would open in English no matter which
// language the agent is set to. The session instructions are carried along.
func openAIRealtimeGreetingInstruction(cfg openAIRealtimeConfig) string {
	var instruction string
	switch cfg.BeginMessageMode {
	case "agent_speaks_first":
		if message := strings.TrimSpace(cfg.WelcomeMessage); message != "" {
			instruction = fmt.Sprintf("Say exactly this opening message and nothing else: %q", message)
		}
	case "agent_speaks_first_with_model_generated_message":
		instruction = "Greet the caller now with one brief, natural opening sentence."
	}
	if instruction == "" {
		return ""
	}
	if base := strings.TrimSpace(cfg.Instructions); base != "" {
		instruction = base + "\n\n" + instruction
	}
	return instruction
}

func (b *openAIRealtimeVoiceBridge) triggerGreeting(conn *websocket.Conn) {
	if delay := time.Duration(b.cfg.WelcomeDelayMs) * time.Millisecond; delay > 0 {
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(delay):
		}
	}
	b.mu.Lock()
	callerSpoke := b.callerSpoke
	b.mu.Unlock()
	if callerSpoke {
		return
	}
	instruction := openAIRealtimeGreetingInstruction(b.cfg)
	if instruction == "" {
		return
	}
	if err := b.sendJSON(conn, map[string]any{
		"type": "response.create",
		"response": map[string]any{
			"output_modalities": []string{"audio"},
			"instructions":      instruction,
		},
	}); err != nil && b.ctx.Err() == nil {
		log.Printf("voicecall: OpenAI Realtime greeting error: %v", err)
	}
}

func (b *openAIRealtimeVoiceBridge) logFirstAudioDelta() {
	b.latencyMu.Lock()
	defer b.latencyMu.Unlock()
	if b.firstAudioLogged || b.speechStoppedAt.IsZero() {
		return
	}
	b.firstAudioLogged = true
	log.Printf("voicecall: latency provider=openai_realtime stage=first_audio_delta total_ms=%d", time.Since(b.speechStoppedAt).Milliseconds())
}

func (b *openAIRealtimeVoiceBridge) appendOutputAudio(event openAIRealtimeServerEvent) {
	if event.Delta == "" {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(event.Delta)
	if err != nil {
		log.Printf("voicecall: OpenAI Realtime audio delta decode error: %v", err)
		return
	}
	samples24 := pcm16LEToFloat32(raw)

	b.outputMu.Lock()
	if b.output == nil || b.output.IsClosed() {
		b.outputResampler.Reset()
	}
	samples16 := applyGain(b.outputResampler.Process(samples24), b.cfg.Volume)
	if b.output == nil || b.output.IsClosed() {
		b.output = newStreamingAudioSource()
		b.outputStarted = false
		b.outputItemID = ""
		b.outputContentIndex = 0
	}
	if event.ItemID != "" {
		b.outputItemID = event.ItemID
		b.outputContentIndex = event.ContentIndex
	}
	var play meowcaller.AudioSource
	if b.output.Append(samples16) && !b.outputStarted && b.output.BufferedSamples() >= meowcaller.FrameSamples {
		b.outputStarted = true
		play = b.output
	}
	b.outputMu.Unlock()

	if play != nil && b.playSource != nil {
		b.latencyMu.Lock()
		stoppedAt := b.speechStoppedAt
		b.latencyMu.Unlock()
		if !stoppedAt.IsZero() {
			log.Printf("voicecall: latency provider=openai_realtime stage=first_audio_playback total_ms=%d", time.Since(stoppedAt).Milliseconds())
		}
		b.playSource(play)
	}
}

// reserveTranscriptSlot appends an empty turn for itemID so its text, when it
// arrives, lands in conversation order rather than arrival order. A turn with
// no item id, or one already reserved, is left alone.
func (b *openAIRealtimeVoiceBridge) reserveTranscriptSlot(role, itemID string) {
	if itemID == "" {
		return
	}
	b.transcriptMu.Lock()
	defer b.transcriptMu.Unlock()
	for _, m := range b.transcript {
		if m.itemID == itemID {
			return
		}
	}
	b.transcript = append(b.transcript, conversationMessage{Role: role, itemID: itemID})
}

// fillTranscript sets the text of the turn reserved for itemID, or appends a
// new turn when none was reserved.
func (b *openAIRealtimeVoiceBridge) fillTranscript(role, itemID, text string) {
	b.transcriptMu.Lock()
	defer b.transcriptMu.Unlock()
	if itemID != "" {
		for i := range b.transcript {
			if b.transcript[i].itemID == itemID {
				b.transcript[i].Content = text
				return
			}
		}
	}
	b.transcript = append(b.transcript, conversationMessage{Role: role, Content: text, itemID: itemID})
}

// Transcript returns the full user/assistant conversation for the call, in
// conversation order, for post-call persistence. Turns whose text never arrived
// (a transcription that failed) are left out.
func (b *openAIRealtimeVoiceBridge) Transcript() []conversationMessage {
	b.transcriptMu.Lock()
	defer b.transcriptMu.Unlock()
	out := make([]conversationMessage, 0, len(b.transcript))
	for _, m := range b.transcript {
		if strings.TrimSpace(m.Content) != "" {
			out = append(out, conversationMessage{Role: m.Role, Content: m.Content})
		}
	}
	return out
}

func (b *openAIRealtimeVoiceBridge) finishOutput() {
	var source *streamingAudioSource
	var play meowcaller.AudioSource
	b.outputMu.Lock()
	if b.output != nil {
		source = b.output
		if !b.outputStarted && source.BufferedSamples() > 0 {
			b.outputStarted = true
			play = source
		}
		b.output = nil
		b.outputStarted = false
		b.outputItemID = ""
		b.outputContentIndex = 0
		b.outputResampler.Reset()
	}
	b.outputMu.Unlock()
	if source != nil {
		_ = source.Close()
	}
	if play != nil && b.playSource != nil {
		b.playSource(play)
	}
}

func (b *openAIRealtimeVoiceBridge) interruptOutput(conn *websocket.Conn) {
	var source *streamingAudioSource
	var itemID string
	var contentIndex int
	b.outputMu.Lock()
	if b.output != nil {
		source = b.output
		itemID = b.outputItemID
		contentIndex = b.outputContentIndex
		b.output = nil
		b.outputStarted = false
		b.outputItemID = ""
		b.outputContentIndex = 0
		b.outputResampler.Reset()
	}
	b.outputMu.Unlock()

	if source != nil {
		audioEndMS := source.PlayedMS()
		_ = source.Close()
		if itemID != "" {
			_ = b.sendJSON(conn, map[string]any{
				"type":          "conversation.item.truncate",
				"item_id":       itemID,
				"content_index": contentIndex,
				"audio_end_ms":  audioEndMS,
			})
		}
	}
	if b.stopPlayback != nil {
		b.stopPlayback()
	}
}
