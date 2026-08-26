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
	cfg          openAIStandardConfig
	playSource   func(meowcaller.AudioSource)
	stopPlayback func()

	ctx    context.Context
	cancel context.CancelFunc
	frames chan []float32
	done   chan struct{}

	mu           sync.Mutex
	conn         *websocket.Conn
	closed       bool
	callerSpoke  bool
	greetingOnce sync.Once
	sendMu       sync.Mutex
	// toolRounds counts the tool calls made since the caller last spoke, so a
	// model that keeps looking things up is eventually made to answer.
	toolRounds int

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

func newOpenAIRealtimeVoiceBridge(cfg openAIStandardConfig, playSource func(meowcaller.AudioSource), stopPlayback func()) *openAIRealtimeVoiceBridge {
	ctx, cancel := context.WithCancel(context.Background())
	b := &openAIRealtimeVoiceBridge{
		cfg:          cfg,
		playSource:   playSource,
		stopPlayback: stopPlayback,
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

func (b *openAIRealtimeVoiceBridge) run() {
	defer close(b.done)
	defer b.finishOutput()

	startedAt := time.Now()
	conn, resp, err := websocket.Dial(b.ctx, openAIRealtimeWebSocketURL(b.cfg.RealtimeURL, b.cfg.RealtimeModel), &websocket.DialOptions{
		HTTPHeader: openAIRealtimeHeaders(b.cfg.APIKey),
	})
	if err != nil {
		log.Printf("voicecall: OpenAI Realtime connect error: %v%s", err, websocketResponseBody(resp))
		return
	}
	conn.SetReadLimit(4 << 20)
	b.mu.Lock()
	b.conn = conn
	b.mu.Unlock()
	defer conn.CloseNow()

	if err := b.sendJSON(conn, openAIRealtimeSessionUpdate(b.cfg)); err != nil {
		log.Printf("voicecall: OpenAI Realtime session update error: %v", err)
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
			b.cancel()
			<-inputDone
			if ctxErr == nil && websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				log.Printf("voicecall: OpenAI Realtime read error: %v", err)
			}
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

func openAIRealtimeSessionUpdate(cfg openAIStandardConfig) map[string]any {
	session := map[string]any{
		"type":              "realtime",
		"model":             cfg.RealtimeModel,
		"instructions":      cfg.Instructions,
		"output_modalities": []string{"audio"},
		"max_output_tokens": 256,
		"audio": map[string]any{
			"input": map[string]any{
				"format":         map[string]any{"type": "audio/pcm", "rate": openAIRealtimeSampleRate},
				"turn_detection": openAIRealtimeTurnDetection(cfg),
				// Transcribe the caller's audio so both sides of the conversation
				// can be persisted after the call. It is delivered asynchronously
				// as conversation.item.input_audio_transcription.completed events
				// and does not gate the spoken response.
				"transcription": openAIRealtimeTranscription(cfg),
			},
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

func openAIRealtimeTurnDetection(cfg openAIStandardConfig) map[string]any {
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
func openAIRealtimeTranscription(cfg openAIStandardConfig) map[string]any {
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
	Status        string `json:"status"`
	StatusDetails any    `json:"status_details"`
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
		if event.Error != nil && event.Error.Message != "" {
			log.Printf("voicecall: OpenAI Realtime error: %s", event.Error.Message)
		} else {
			log.Printf("voicecall: OpenAI Realtime error: %s", strings.TrimSpace(string(data)))
		}
	case "session.updated":
		log.Println("voicecall: OpenAI Realtime session ready")
		b.greetingOnce.Do(func() { go b.triggerGreeting(conn) })
	case "input_audio_buffer.speech_started":
		b.mu.Lock()
		b.callerSpoke = true
		// Each thing the caller says gets its own tool budget, so a question that
		// needed two lookups does not leave the next one unable to look anything
		// up at all.
		b.toolRounds = 0
		b.mu.Unlock()
		b.interruptOutput(conn)
	case "input_audio_buffer.speech_stopped":
		b.latencyMu.Lock()
		b.speechStoppedAt = time.Now()
		b.firstAudioLogged = false
		b.latencyMu.Unlock()
	case "response.output_audio.delta", "response.audio.delta":
		b.logFirstAudioDelta()
		b.appendOutputAudio(event)
	case "response.output_audio.done", "response.audio.done", "response.done", "response.cancelled":
		if event.Response != nil && event.Response.Status != "" && event.Response.Status != "completed" && event.Response.Status != "cancelled" {
			log.Printf("voicecall: OpenAI Realtime response status: %s", event.Response.Status)
		}
		b.finishOutput()
		if event.Type == "response.done" && event.Response != nil {
			b.runFunctionCalls(conn, event.Response.Output)
		}
	case "conversation.item.input_audio_transcription.completed":
		if text := strings.TrimSpace(event.Transcript); text != "" {
			log.Printf("voicecall: caller said: %s", text)
			b.appendTranscript(conversationMessage{Role: "user", Content: text})
		}
	case "response.output_audio_transcript.done", "response.audio_transcript.done":
		if text := strings.TrimSpace(event.Transcript); text != "" {
			log.Printf("voicecall: AI reply transcript: %s", text)
			b.appendTranscript(conversationMessage{Role: "assistant", Content: text})
		}
	}
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
func openAIRealtimeGreetingInstruction(cfg openAIStandardConfig) string {
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

func (b *openAIRealtimeVoiceBridge) appendTranscript(message conversationMessage) {
	b.transcriptMu.Lock()
	b.transcript = append(b.transcript, message)
	b.transcriptMu.Unlock()
}

// Transcript returns the full user/assistant conversation for the call, in the
// order the transcription events arrived, for post-call persistence.
func (b *openAIRealtimeVoiceBridge) Transcript() []conversationMessage {
	b.transcriptMu.Lock()
	defer b.transcriptMu.Unlock()
	return append([]conversationMessage(nil), b.transcript...)
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
