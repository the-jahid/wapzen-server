package voicecall

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/purpshell/meowcaller"
)

// voiceSession is what a call needs from its speech-to-speech engine. OpenAI
// Realtime and Gemini Live both provide it; runCall picks one per call from the
// agent's model.
type voiceSession interface {
	meowcaller.AudioSink
	// Ready closes once the session is configured or has failed; Err says which.
	Ready() <-chan struct{}
	Err() error
	// Busy reports the model generating (or about to generate) a reply.
	Busy() bool
	// RequestResponse asks the model to speak now with extra direction, or
	// reports false when it cannot (not ready, or already speaking).
	RequestResponse(extra string) bool
	// Transcript is both sides of the call in conversation order.
	Transcript() []conversationMessage
}

var (
	_ voiceSession = (*openAIRealtimeVoiceBridge)(nil)
	_ voiceSession = (*geminiLiveBridge)(nil)
)

// geminiLiveBridge runs a call on one Google Gemini Live session. The shape
// matches the OpenAI bridge — caller audio streams in, the model's audio
// streams out, and its own turn detector decides when the caller has finished —
// with two Gemini-specific parts: tool calls arrive as their own message and
// the model carries on by itself once they are answered, and a connection
// lives about ten minutes, so the session is resumed on a fresh one.
type geminiLiveBridge struct {
	cfg          geminiLiveConfig
	playSource   func(meowcaller.AudioSource)
	stopPlayback func()
	hooks        bridgeHooks

	ctx    context.Context
	cancel context.CancelFunc
	frames chan []float32
	done   chan struct{}

	ready     chan struct{}
	readyOnce sync.Once
	readyErr  error

	sendMu sync.Mutex

	mu           sync.Mutex
	conn         *websocket.Conn
	closed       bool
	callerSpoke  bool
	greetingOnce sync.Once
	// turnActive is true from the moment the model starts (or is asked for) a
	// reply until its turn completes or the caller interrupts it.
	turnActive bool
	// toolsPending counts tool calls being run; the model is mid-reply then.
	toolsPending int
	toolRounds   int
	// cancelledTools are tool calls the model withdrew (the caller interrupted);
	// their results are not sent.
	cancelledTools map[string]bool
	// resumeHandle resumes this session on a new connection; reconnectDue says
	// Google announced the current one will close (goAway).
	resumeHandle string
	reconnectDue bool

	outputResampler *sampleRateConverter
	outputMu        sync.Mutex
	output          *streamingAudioSource
	outputStarted   bool

	latencyMu        sync.Mutex
	callerHeardAt    time.Time
	firstAudioLogged bool

	// The transcript is built from Gemini's incremental transcriptions: the
	// caller's words keep landing in the open caller turn (even when they trail
	// in after the reply started), the model's in the open reply.
	transcriptMu  sync.Mutex
	transcript    []conversationMessage
	callerTurn    int
	assistantTurn int
	callerOpen    bool
}

func newGeminiLiveBridge(cfg geminiLiveConfig, playSource func(meowcaller.AudioSource), stopPlayback func(), hooks bridgeHooks) *geminiLiveBridge {
	ctx, cancel := context.WithCancel(context.Background())
	b := &geminiLiveBridge{
		cfg:             cfg,
		playSource:      playSource,
		stopPlayback:    stopPlayback,
		hooks:           hooks,
		ctx:             ctx,
		cancel:          cancel,
		frames:          make(chan []float32, 64),
		done:            make(chan struct{}),
		ready:           make(chan struct{}),
		cancelledTools:  make(map[string]bool),
		outputResampler: newSampleRateConverter(geminiOutputSampleRate, audioSampleRate),
		callerTurn:      -1,
		assistantTurn:   -1,
	}
	go b.run()
	return b
}

func (b *geminiLiveBridge) WriteFrame(frame []float32) error {
	cp := append([]float32(nil), frame...)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	select {
	case b.frames <- cp:
	default:
		// Keep the most recent audio when the queue backs up (connecting or
		// resuming on a new connection).
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

func (b *geminiLiveBridge) Close() error {
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

func (b *geminiLiveBridge) Ready() <-chan struct{} { return b.ready }

func (b *geminiLiveBridge) Err() error {
	select {
	case <-b.ready:
		return b.readyErr
	default:
		return nil
	}
}

func (b *geminiLiveBridge) markReady(err error) {
	b.readyOnce.Do(func() {
		b.readyErr = err
		close(b.ready)
	})
}

func (b *geminiLiveBridge) fail(err error) {
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

func (b *geminiLiveBridge) run() {
	defer close(b.done)
	defer b.finishOutput()
	defer b.markReady(errors.New("gemini live session ended before it was ready"))

	for reconnects := 0; ; reconnects++ {
		resume, err := b.session(reconnects > 0)
		if b.ctx.Err() != nil {
			return
		}
		if !resume || reconnects >= maxGeminiReconnects {
			if err == nil {
				err = errors.New("gemini live session closed")
			}
			b.fail(err)
			b.cancel()
			return
		}
		log.Printf("voicecall: Gemini Live resuming session on a new connection (%d/%d): %v", reconnects+1, maxGeminiReconnects, err)
	}
}

// session runs one WebSocket connection of the call. It reports whether the
// session can be resumed on a new connection (it was up and Google gave a
// resumption handle) and why this one ended.
func (b *geminiLiveBridge) session(resuming bool) (resume bool, err error) {
	startedAt := time.Now()
	conn, resp, err := websocket.Dial(b.ctx, geminiLiveWebSocketURL(b.cfg.URL, b.cfg.APIKey), nil)
	if err != nil {
		if b.ctx.Err() == nil {
			log.Printf("voicecall: Gemini Live connect error: %v%s", err, websocketResponseBody(resp))
		}
		return false, fmt.Errorf("connect: %w", err)
	}
	conn.SetReadLimit(8 << 20)
	defer conn.CloseNow()

	b.mu.Lock()
	b.conn = conn
	handle := ""
	if resuming {
		handle = b.resumeHandle
	}
	b.reconnectDue = false
	b.turnActive = false
	b.toolsPending = 0
	b.mu.Unlock()
	b.finishOutput()

	if err := b.sendJSON(geminiLiveSetup(b.cfg, handle)); err != nil {
		return false, fmt.Errorf("setup: %w", err)
	}

	connCtx, connCancel := context.WithCancel(b.ctx)
	defer connCancel()
	audioDone := make(chan struct{})
	audioStarted := false
	defer func() {
		connCancel()
		if audioStarted {
			<-audioDone
		}
	}()

	setupDone := false
	for {
		messageType, data, err := conn.Read(b.ctx)
		if err != nil {
			if b.ctx.Err() != nil {
				return false, err
			}
			status := websocket.CloseStatus(err)
			if status != websocket.StatusNormalClosure {
				log.Printf("voicecall: Gemini Live read error: %v", err)
			}
			if !setupDone {
				// Closed before setupComplete: Google refused the setup (bad model,
				// key, or field). Never talk to a caller on a session like that.
				return false, fmt.Errorf("session setup rejected: %w", err)
			}
			b.mu.Lock()
			canResume := b.resumeHandle != ""
			b.mu.Unlock()
			return canResume, fmt.Errorf("gemini live connection dropped: %w", err)
		}
		if messageType != websocket.MessageText && messageType != websocket.MessageBinary {
			continue
		}
		action := b.handleMessage(data)
		if action.setupComplete && !setupDone {
			setupDone = true
			log.Printf("voicecall: Gemini Live connected model=%s voice=%s connect_ms=%d resumed=%t", b.cfg.Model, b.cfg.Voice, time.Since(startedAt).Milliseconds(), handle != "")
			audioStarted = true
			go func() {
				defer close(audioDone)
				b.sendAudioLoop(connCtx, conn)
			}()
			b.markReady(nil)
			b.greetingOnce.Do(func() { go b.triggerGreeting() })
		}
		if action.reconnect {
			_ = conn.Close(websocket.StatusNormalClosure, "resuming on a new connection")
			return true, errors.New("connection ending (goAway)")
		}
	}
}

func geminiLiveWebSocketURL(endpoint, apiKey string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return endpoint
	}
	q := u.Query()
	if q.Get("key") == "" {
		q.Set("key", strings.TrimSpace(apiKey))
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// geminiLiveSetup renders the session's opening message. handle resumes an
// earlier connection's session ("" starts a new one).
func geminiLiveSetup(cfg geminiLiveConfig, handle string) map[string]any {
	model := strings.TrimSpace(cfg.Model)
	if !strings.HasPrefix(model, "models/") {
		model = "models/" + model
	}
	detection := map[string]any{
		"disabled":          false,
		"silenceDurationMs": max(cfg.SilenceMS, 150),
		"prefixPaddingMs":   max(cfg.PrefixMS, 60),
	}
	if s := geminiSensitivity("START", cfg.StartSensitivity); s != "" {
		detection["startOfSpeechSensitivity"] = s
	}
	if s := geminiSensitivity("END", cfg.EndSensitivity); s != "" {
		detection["endOfSpeechSensitivity"] = s
	}
	resumption := map[string]any{}
	if handle != "" {
		resumption["handle"] = handle
	}
	setup := map[string]any{
		"model": model,
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{
				"voiceConfig": map[string]any{
					"prebuiltVoiceConfig": map[string]any{"voiceName": normalizeGeminiVoice(cfg.Voice)},
				},
			},
		},
		"realtimeInputConfig": map[string]any{
			"automaticActivityDetection": detection,
			// The caller talking over the agent stops the reply, as on a real call.
			"activityHandling": "START_OF_ACTIVITY_INTERRUPTS",
		},
		// Both sides are transcribed for the saved call transcript.
		"inputAudioTranscription":  map[string]any{},
		"outputAudioTranscription": map[string]any{},
		// Without compression an audio session stops at 15 minutes; resumption
		// carries it over the ~10-minute connection limit.
		"contextWindowCompression": map[string]any{"slidingWindow": map[string]any{}},
		"sessionResumption":        resumption,
	}
	if instructions := strings.TrimSpace(cfg.Instructions); instructions != "" {
		setup["systemInstruction"] = map[string]any{"parts": []map[string]any{{"text": instructions}}}
	}
	if tools := geminiLiveTools(cfg.Tools); len(tools) > 0 {
		setup["tools"] = []map[string]any{{"functionDeclarations": tools}}
	}
	return map[string]any{"setup": setup}
}

// geminiLiveTools declares the call's tools. Their parameters are full JSON
// Schema (additionalProperties and all), which parametersJsonSchema takes as is.
func geminiLiveTools(runner toolRunner) []map[string]any {
	if runner == nil {
		return nil
	}
	definitions := runner.Definitions()
	if len(definitions) == 0 {
		return nil
	}
	tools := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		tool := map[string]any{
			"name":        definition.Name,
			"description": definition.Description,
		}
		if len(definition.Parameters) > 0 {
			tool["parametersJsonSchema"] = definition.Parameters
		}
		tools = append(tools, tool)
	}
	return tools
}

func (b *geminiLiveBridge) sendAudioLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-b.frames:
			if !ok {
				return
			}
			if len(frame) == 0 {
				continue
			}
			if err := b.sendJSONOn(ctx, conn, map[string]any{
				"realtimeInput": map[string]any{
					"audio": map[string]any{
						"mimeType": fmt.Sprintf("audio/pcm;rate=%d", geminiInputSampleRate),
						"data":     base64.StdEncoding.EncodeToString(float32ToPCM16LE(frame)),
					},
				},
			}); err != nil {
				if ctx.Err() == nil {
					log.Printf("voicecall: Gemini Live audio send error: %v", err)
				}
				return
			}
		}
	}
}

// sendJSON writes to the current connection.
func (b *geminiLiveBridge) sendJSON(value any) error {
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if conn == nil {
		return errors.New("not connected")
	}
	return b.sendJSONOn(b.ctx, conn, value)
}

func (b *geminiLiveBridge) sendJSONOn(ctx context.Context, conn *websocket.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	return conn.Write(ctx, websocket.MessageText, data)
}

type geminiServerMessage struct {
	SetupComplete json.RawMessage `json:"setupComplete"`
	ServerContent *struct {
		ModelTurn *struct {
			Parts []struct {
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"modelTurn"`
		TurnComplete        bool             `json:"turnComplete"`
		Interrupted         bool             `json:"interrupted"`
		InputTranscription  *geminiTextChunk `json:"inputTranscription"`
		OutputTranscription *geminiTextChunk `json:"outputTranscription"`
	} `json:"serverContent"`
	ToolCall *struct {
		FunctionCalls []geminiFunctionCall `json:"functionCalls"`
	} `json:"toolCall"`
	ToolCallCancellation *struct {
		IDs []string `json:"ids"`
	} `json:"toolCallCancellation"`
	GoAway *struct {
		TimeLeft string `json:"timeLeft"`
	} `json:"goAway"`
	SessionResumptionUpdate *struct {
		NewHandle string `json:"newHandle"`
		Resumable bool   `json:"resumable"`
	} `json:"sessionResumptionUpdate"`
}

type geminiTextChunk struct {
	Text string `json:"text"`
}

type geminiFunctionCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type geminiAction struct {
	setupComplete bool
	reconnect     bool
}

func (b *geminiLiveBridge) handleMessage(data []byte) geminiAction {
	var msg geminiServerMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("voicecall: Gemini Live message decode error: %v", err)
		return geminiAction{}
	}
	var action geminiAction
	if len(msg.SetupComplete) > 0 {
		action.setupComplete = true
	}
	if update := msg.SessionResumptionUpdate; update != nil && update.Resumable && update.NewHandle != "" {
		b.mu.Lock()
		b.resumeHandle = update.NewHandle
		b.mu.Unlock()
	}
	if sc := msg.ServerContent; sc != nil {
		if sc.InputTranscription != nil && sc.InputTranscription.Text != "" {
			b.callerText(sc.InputTranscription.Text)
		}
		if sc.ModelTurn != nil {
			for _, part := range sc.ModelTurn.Parts {
				if part.InlineData != nil && strings.HasPrefix(part.InlineData.MimeType, "audio/") {
					b.mu.Lock()
					b.turnActive = true
					b.mu.Unlock()
					b.appendOutputAudio(part.InlineData.Data)
				}
			}
		}
		if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
			b.assistantText(sc.OutputTranscription.Text)
		}
		if sc.Interrupted {
			b.interruptOutput()
			b.endTurn()
		}
		if sc.TurnComplete {
			b.finishOutput()
			b.endTurn()
		}
	}
	if call := msg.ToolCall; call != nil && len(call.FunctionCalls) > 0 {
		b.runFunctionCalls(call.FunctionCalls)
	}
	if cancel := msg.ToolCallCancellation; cancel != nil {
		b.mu.Lock()
		for _, id := range cancel.IDs {
			b.cancelledTools[id] = true
		}
		b.mu.Unlock()
	}
	if msg.GoAway != nil {
		log.Printf("voicecall: Gemini Live connection closing soon (time left %s)", msg.GoAway.TimeLeft)
		b.mu.Lock()
		b.reconnectDue = true
		b.mu.Unlock()
	}
	// Move to the new connection between replies, never in the middle of one.
	b.mu.Lock()
	if b.reconnectDue && !b.turnActive && b.toolsPending == 0 && b.resumeHandle != "" {
		action.reconnect = true
	}
	b.mu.Unlock()
	return action
}

// callerText records what the caller said. The first words after the agent's
// last reply open a new caller turn: that is this bridge's "caller started
// speaking" signal, since Gemini reports the turn detector only through it.
func (b *geminiLiveBridge) callerText(text string) {
	b.transcriptMu.Lock()
	newTurn := !b.callerOpen
	if newTurn {
		b.callerOpen = true
		b.transcript = append(b.transcript, conversationMessage{Role: "user"})
		b.callerTurn = len(b.transcript) - 1
	}
	b.transcript[b.callerTurn].Content += text
	b.transcriptMu.Unlock()

	b.latencyMu.Lock()
	b.callerHeardAt = time.Now()
	b.firstAudioLogged = false
	b.latencyMu.Unlock()

	if newTurn {
		b.mu.Lock()
		b.callerSpoke = true
		b.toolRounds = 0
		b.mu.Unlock()
		if b.hooks.onCallerSpeech != nil {
			b.hooks.onCallerSpeech()
		}
	}
}

func (b *geminiLiveBridge) assistantText(text string) {
	b.transcriptMu.Lock()
	defer b.transcriptMu.Unlock()
	if b.assistantTurn < 0 {
		b.transcript = append(b.transcript, conversationMessage{Role: "assistant"})
		b.assistantTurn = len(b.transcript) - 1
	}
	b.transcript[b.assistantTurn].Content += text
}

// endTurn closes the agent's reply (finished or interrupted), so the next
// words on either side start new turns.
func (b *geminiLiveBridge) endTurn() {
	b.mu.Lock()
	b.turnActive = false
	b.mu.Unlock()

	b.transcriptMu.Lock()
	var said, heard string
	if b.callerTurn >= 0 && b.callerOpen {
		heard = strings.TrimSpace(b.transcript[b.callerTurn].Content)
	}
	if b.assistantTurn >= 0 {
		said = strings.TrimSpace(b.transcript[b.assistantTurn].Content)
	}
	b.callerOpen = false
	b.assistantTurn = -1
	b.transcriptMu.Unlock()

	if heard != "" {
		log.Printf("voicecall: caller said: %s", heard)
	}
	if said != "" {
		log.Printf("voicecall: AI reply transcript: %s", said)
	}
}

func (b *geminiLiveBridge) Busy() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.turnActive || b.toolsPending > 0
}

// RequestResponse asks the model to speak now. Gemini has no per-response
// instructions, so the direction goes in as a marked note in the conversation;
// the session's system instruction stays in force.
func (b *geminiLiveBridge) RequestResponse(extra string) bool {
	select {
	case <-b.ready:
		if b.readyErr != nil {
			return false
		}
	default:
		return false
	}
	b.mu.Lock()
	if b.turnActive || b.toolsPending > 0 || b.closed || b.conn == nil {
		b.mu.Unlock()
		return false
	}
	b.turnActive = true
	b.mu.Unlock()
	if err := b.sendText(geminiSystemNote(extra)); err != nil {
		b.mu.Lock()
		b.turnActive = false
		b.mu.Unlock()
		if b.ctx.Err() == nil {
			log.Printf("voicecall: Gemini Live response request error: %v", err)
		}
		return false
	}
	return true
}

func (b *geminiLiveBridge) sendText(text string) error {
	return b.sendJSON(map[string]any{"realtimeInput": map[string]any{"text": text}})
}

// geminiSystemNote marks direction to the model as not coming from the caller,
// so it acts on it instead of answering it.
func geminiSystemNote(direction string) string {
	return "[Note from the phone system, not words from the caller. Do not mention this note; just do what it says, speaking to the caller.] " + strings.TrimSpace(direction)
}

// geminiGreetingInstruction is the opening line the agent is asked to speak,
// or "" when it waits for the caller.
func geminiGreetingInstruction(cfg geminiLiveConfig) string {
	switch cfg.BeginMessageMode {
	case "agent_speaks_first":
		if message := strings.TrimSpace(cfg.WelcomeMessage); message != "" {
			return geminiSystemNote(fmt.Sprintf("The call has just connected. Say exactly this opening message and nothing else: %q", message))
		}
	case "agent_speaks_first_with_model_generated_message":
		return geminiSystemNote("The call has just connected. Greet the caller now with one brief, natural opening sentence.")
	}
	return ""
}

func (b *geminiLiveBridge) triggerGreeting() {
	if delay := time.Duration(b.cfg.WelcomeDelayMs) * time.Millisecond; delay > 0 {
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(delay):
		}
	}
	instruction := geminiGreetingInstruction(b.cfg)
	if instruction == "" {
		return
	}
	b.mu.Lock()
	if b.callerSpoke {
		b.mu.Unlock()
		return
	}
	b.turnActive = true
	b.mu.Unlock()
	if err := b.sendText(instruction); err != nil && b.ctx.Err() == nil {
		log.Printf("voicecall: Gemini Live greeting error: %v", err)
	}
}

// runFunctionCalls runs the tools the model called and sends their results
// back; Gemini then continues the reply by itself. It runs off the read loop,
// since a lookup is a network round trip and the caller's audio keeps coming.
func (b *geminiLiveBridge) runFunctionCalls(calls []geminiFunctionCall) {
	b.mu.Lock()
	b.toolsPending += len(calls)
	b.toolRounds++
	overBudget := b.toolRounds >= maxToolRounds
	b.mu.Unlock()

	go func() {
		defer func() {
			b.mu.Lock()
			b.toolsPending -= len(calls)
			if b.toolsPending < 0 {
				b.toolsPending = 0
			}
			b.mu.Unlock()
		}()
		responses := make([]map[string]any, 0, len(calls))
		for _, call := range calls {
			result := "This tool is not available on this call."
			if b.cfg.Tools != nil {
				args := strings.TrimSpace(string(call.Args))
				if args == "" || args == "null" {
					args = "{}"
				}
				result = b.cfg.Tools.Run(b.ctx, call.Name, args)
			}
			if b.ctx.Err() != nil {
				return
			}
			if overBudget {
				// The lookup budget for this question is spent: answer from what is
				// known rather than keep the caller waiting on another lookup.
				result += "\n\nAnswer the caller now with what you already have; do not call any more tools for this question."
			}
			b.mu.Lock()
			cancelled := b.cancelledTools[call.ID]
			b.mu.Unlock()
			if cancelled {
				continue
			}
			responses = append(responses, map[string]any{
				"id":       call.ID,
				"name":     call.Name,
				"response": map[string]any{"result": result},
			})
		}
		if len(responses) == 0 {
			return
		}
		if err := b.sendJSON(map[string]any{"toolResponse": map[string]any{"functionResponses": responses}}); err != nil && b.ctx.Err() == nil {
			log.Printf("voicecall: Gemini Live tool result send error: %v", err)
		}
	}()
}

func (b *geminiLiveBridge) appendOutputAudio(encoded string) {
	if encoded == "" {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		log.Printf("voicecall: Gemini Live audio decode error: %v", err)
		return
	}
	samples24 := pcm16LEToFloat32(raw)

	b.outputMu.Lock()
	if b.output == nil || b.output.IsClosed() {
		b.outputResampler.Reset()
		b.output = newStreamingAudioSource()
		b.outputStarted = false
	}
	samples16 := applyGain(b.outputResampler.Process(samples24), b.cfg.Volume)
	var play meowcaller.AudioSource
	if b.output.Append(samples16) && !b.outputStarted && b.output.BufferedSamples() >= meowcaller.FrameSamples {
		b.outputStarted = true
		play = b.output
	}
	b.outputMu.Unlock()

	if play != nil && b.playSource != nil {
		b.latencyMu.Lock()
		heardAt := b.callerHeardAt
		logIt := !b.firstAudioLogged && !heardAt.IsZero()
		b.firstAudioLogged = true
		b.latencyMu.Unlock()
		if logIt {
			log.Printf("voicecall: latency provider=gemini_live stage=first_audio_playback since_last_caller_words_ms=%d", time.Since(heardAt).Milliseconds())
		}
		b.playSource(play)
	}
}

func (b *geminiLiveBridge) finishOutput() {
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

// interruptOutput drops the rest of the reply the caller talked over. Gemini
// already knows (it sent "interrupted"), so nothing goes back to it.
func (b *geminiLiveBridge) interruptOutput() {
	b.outputMu.Lock()
	source := b.output
	b.output = nil
	b.outputStarted = false
	b.outputResampler.Reset()
	b.outputMu.Unlock()
	if source != nil {
		_ = source.Close()
	}
	if b.stopPlayback != nil {
		b.stopPlayback()
	}
}

func (b *geminiLiveBridge) Transcript() []conversationMessage {
	b.transcriptMu.Lock()
	defer b.transcriptMu.Unlock()
	out := make([]conversationMessage, 0, len(b.transcript))
	for _, m := range b.transcript {
		if text := strings.TrimSpace(m.Content); text != "" {
			out = append(out, conversationMessage{Role: m.Role, Content: text})
		}
	}
	return out
}
