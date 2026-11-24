// Package voicecall runs WhatsApp voice calls in both directions — answering
// incoming ones and placing outbound ones — on one OpenAI Realtime
// speech-to-speech session per call.
package voicecall

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/purpshell/meowcaller"
	callerdiag "github.com/purpshell/meowcaller/diag"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"whatsapp-ai-caller-server/internal/models"
)

// callRecordTimeout bounds each calls-table write so a slow or unreachable
// database can never hold up answering or tearing down a call.
const callRecordTimeout = 5 * time.Second

// outboundMediaGrace bounds how long an accepted outbound call waits for the
// callee's media to reach the relay before the agent starts speaking anyway. The
// <accept> stanza arrives before the peer's media path is up, so playing on it
// alone throws the opening words away; waiting forever would answer a call whose
// inbound media never arrives with silence. This is the compromise.
const outboundMediaGrace = 1500 * time.Millisecond

// outboundRingTimeout hangs up an outbound call nobody accepted in time.
// WhatsApp stops ringing well before this and sends a terminate; this catches
// the call whose terminate never arrived (the socket dropped while it rang),
// which would otherwise stream silence to the relay forever: the watchdog only
// starts once a call is live.
const outboundRingTimeout = 90 * time.Second

// defaultAnswerWaitMS is how long an inbound call may keep ringing while its
// Realtime session connects. Past it the call is answered anyway; the caller's
// first words are buffered until the session is up.
const defaultAnswerWaitMS = 4000

// defaultInlineKnowledgeMaxChars is the largest knowledge base put straight
// into a call's instructions (roughly two to four thousand tokens). A typical
// small-business menu, price list or FAQ fits; anything larger is searched.
const defaultInlineKnowledgeMaxChars = 8000

// CallStore persists the lifecycle of the calls this client handles, in either
// direction. It is injected via UseCallStore; when absent, inbound calls are
// handled exactly as before but not recorded (outbound calls require it, see
// PlaceCall). Every method takes its own context so the client can bound each
// write.
type CallStore interface {
	Insert(ctx context.Context, params models.NewCall) (id string, err error)
	MarkAnswered(ctx context.Context, id string) error
	MarkEnded(ctx context.Context, id, reason string) error
	MarkDeclined(ctx context.Context, id, reason string) error
	MarkFailed(ctx context.Context, id, reason string) error
	// SaveTranscript persists the call's user/assistant conversation once it has
	// ended. id is the calls-table row id returned by Insert. It returns the
	// number of turns written.
	SaveTranscript(ctx context.Context, id string, messages []models.CallMessage) (int, error)
}

// Client answers incoming WhatsApp voice calls with OpenAI Realtime. New()
// reads credentials once; Attach wires the handler onto each per-number
// whatsmeow client the login Manager creates.
type Client struct {
	openAI openAIRealtimeConfig
	// gemini runs calls whose agent picked a Gemini Live model.
	gemini    geminiLiveConfig
	diag      *callerdiag.Recorder
	recordDir string

	// store persists answered calls in the calls table. Nil disables persistence
	// (see UseCallStore); a nil store makes every call-record method a no-op.
	store CallStore

	// retriever reads the agent's knowledge bases during a call. Nil (see
	// UseKnowledgeRetriever) or disabled leaves every call answering from its
	// prompt alone, which is exactly how calls behaved before retrieval existed.
	retriever KnowledgeRetriever

	// inlineKnowledgeMaxChars is the largest knowledge base (all of an agent's
	// sources together, in characters) put straight into the call's
	// instructions instead of behind the lookup tool. 0 always uses the tool.
	inlineKnowledgeMaxChars int

	// answerWait bounds how long an inbound call keeps ringing while its
	// Realtime session connects (~1–1.8 s measured), so the caller is picked up
	// by an agent that can already hear and speak rather than into silence.
	answerWait time.Duration

	watchdog watchdogConfig

	// active tracks the call IDs currently being answered. WhatsApp re-delivers
	// an <offer> for the same call, and meowcaller fires OnIncomingCall for each
	// one; without this a repeat offer would start a second realtime session on
	// the same call, and the caller would hear every reply twice.
	activeMu sync.Mutex
	active   map[string]bool

	// live maps a recorded call's calls-table row id to the call on the wire, so
	// the dashboard can hang up a call it is following by the id it was handed.
	// Guarded by activeMu.
	live map[string]liveCall
}

// liveCall is one call HangupCall can reach: the call itself and the slot its
// OnEnd reads the end reason from when the wire gives none.
type liveCall struct {
	call   *meowcaller.Call
	reason *atomic.Value
}

// ErrCallNotLive is returned by HangupCall for a call this process is not
// running: it already ended, or it was never placed here.
var ErrCallNotLive = errors.New("call is not in progress")

// HangupCall ends the live call recorded under rowID (ringing or answered),
// recording reason as why it ended. The call's own OnEnd then writes its final
// status, exactly as when the other side hangs up.
func (c *Client) HangupCall(rowID, reason string) error {
	if c == nil {
		return ErrCallNotLive
	}
	c.activeMu.Lock()
	entry, ok := c.live[rowID]
	c.activeMu.Unlock()
	if !ok {
		return ErrCallNotLive
	}
	if strings.TrimSpace(reason) != "" {
		entry.reason.Store(reason)
	}
	return entry.call.Hangup()
}

func (c *Client) trackLive(rowID string, entry liveCall) {
	c.activeMu.Lock()
	c.live[rowID] = entry
	c.activeMu.Unlock()
}

func (c *Client) forgetLive(rowID string) {
	c.activeMu.Lock()
	delete(c.live, rowID)
	c.activeMu.Unlock()
}

// CallAgent is the per-call configuration resolved at answer time for one
// incoming call. The Attach callback returns it (or nil) to decide, per call,
// whether the AI answers and how it should behave. It carries the live-call
// model, prompt, greeting, transcription, and voice settings resolved from the
// assigned agent.
type CallAgent struct {
	// UserID, PhoneNumberID, and AgentID identify who the call is recorded under
	// in the calls table. UserID is required to persist a call; an empty
	// PhoneNumberID or AgentID is stored as NULL. All three are empty for the
	// default (no resolveAgent) path, which is therefore not persisted.
	UserID        string
	PhoneNumberID string
	AgentID       string

	// Instructions is the agent's prompt for the Realtime session.
	Instructions string

	// KnowledgeBases are the knowledge bases attached to the agent, resolved to
	// the vector-store namespaces the call retrieves from. Empty means the agent
	// answers from its prompt alone and no lookup tool is offered to the model.
	KnowledgeBases []models.AgentKnowledgeBase

	// KnowledgeDocuments is the agent's whole knowledge base when it is small
	// enough to go straight into the instructions (see InlineKnowledgeLimit).
	// When set, it replaces the lookup tool and KnowledgeBases is ignored.
	KnowledgeDocuments []models.KnowledgeDocument

	// Tools are the actions attached to the agent, offered to the model as
	// callable functions for the length of the call. Empty means the agent can
	// only talk, which is how every call behaved before tools existed.
	Tools []models.AgentTool

	// SendMessage sends a WhatsApp message to the other party on this call, on
	// the number the call is running on. It backs the send_text tool and is the
	// one action the voice bridge cannot perform through the call itself, so it
	// is supplied by whoever owns the WhatsApp session. Nil leaves send_text
	// reporting a failure to the model rather than pretending it sent something.
	SendMessage func(ctx context.Context, peer types.JID, body string) error

	// BeginMessageMode controls how the call opens: "agent_speaks_first" (speak
	// WelcomeMessage verbatim), "agent_waits_for_user" (say nothing until the
	// caller speaks), or "agent_speaks_first_with_model_generated_message" (speak
	// an AI-generated opening line). Empty falls back to the configured default
	// (silent, i.e. agent_waits_for_user).
	BeginMessageMode string
	// WelcomeMessage is the literal greeting spoken when BeginMessageMode is
	// "agent_speaks_first".
	WelcomeMessage string
	// WelcomeDelayMs delays the opening message by this many milliseconds after
	// the realtime session is ready. 0 means no delay.
	WelcomeDelayMs int

	// Voice and RealtimeModel select the Realtime voice and model; empty falls
	// back to the server configuration. Language pins the caller transcription.
	Voice         string
	RealtimeModel string
	Language      string

	// Speed is the spoken rate (1 = normal, 0.25–1.5). Volume is a gain applied
	// to the audio played into the call (1 = unchanged). Zero means "not set" for
	// Speed and falls back to the configured default; Volume is only overridden
	// when positive, so an unset agent never silences the call.
	Speed  float64
	Volume float64

	// MinutesLeft is how much talk time the owner's account has left
	// (voiceusage). When positive and shorter than the server's call-length
	// cap, the call is wound down politely once it runs out. Zero means no
	// account limit applies to this call.
	MinutesLeft time.Duration
}

// New builds a voice-call client from the environment, or returns nil when
// neither OpenAI Realtime nor Gemini Live has credentials (or is switched off).
// A nil *Client is inert: the Manager simply never attaches a call handler, and
// every method below is safe to call on it.
func New() *Client {
	openAI := loadOpenAIRealtimeConfig()
	gemini := loadGeminiLiveConfig()
	if !openAI.Enabled() && !gemini.Enabled() {
		return nil
	}

	c := &Client{
		openAI:                  openAI,
		gemini:                  gemini,
		recordDir:               strings.TrimSpace(os.Getenv("VOICECALL_RECORD_DIR")),
		active:                  make(map[string]bool),
		live:                    make(map[string]liveCall),
		inlineKnowledgeMaxChars: envInt("VOICE_KB_INLINE_MAX_CHARS", defaultInlineKnowledgeMaxChars),
		answerWait:              time.Duration(envInt("VOICE_ANSWER_WAIT_MS", defaultAnswerWaitMS)) * time.Millisecond,
		watchdog:                loadWatchdogConfig(),
	}

	// MEOWCALLER_DIAG_DIR turns on the signalling/audio recorder shared by every
	// call; off unless it points somewhere writable. Treat the files as sensitive
	// call data. Matches meowcaller-test's opt-in diagnostics.
	if diagDir := strings.TrimSpace(os.Getenv("MEOWCALLER_DIAG_DIR")); diagDir != "" {
		rec, err := callerdiag.NewRecorder(diagDir)
		if err != nil {
			log.Printf("voicecall: could not enable meowcaller diagnostics in %s: %v", diagDir, err)
		} else {
			c.diag = rec
			log.Printf("voicecall: meowcaller diagnostics enabled in %s (treat as sensitive call data)", diagDir)
		}
	}

	if openAI.Enabled() {
		log.Printf("voicecall: OpenAI Realtime configured model=%s voice=%s", c.openAI.RealtimeModel, c.openAI.Voice)
	}
	if gemini.Enabled() {
		log.Printf("voicecall: Gemini Live configured model=%s voice=%s", c.gemini.Model, c.gemini.Voice)
	}
	return c
}

// UseCallStore wires a persistence backend so answered calls are recorded in the
// calls table. Safe on a nil receiver (feature disabled) and a nil store (leaves
// persistence off). Set once at startup, before any call is answered.
func (c *Client) UseCallStore(store CallStore) {
	if c == nil {
		return
	}
	c.store = store
}

// UseKnowledgeRetriever wires the vector-store reader that backs the in-call
// knowledge base lookup. Safe on a nil receiver (feature disabled) and a nil
// retriever (leaves retrieval off). Set once at startup, before any call is
// answered.
func (c *Client) UseKnowledgeRetriever(retriever KnowledgeRetriever) {
	if c == nil {
		return
	}
	c.retriever = retriever
}

// InlineKnowledgeLimit is the largest knowledge base, in characters across all
// of an agent's sources, that a call puts straight into its instructions
// instead of searching. 0 (or a nil client) means always search.
func (c *Client) InlineKnowledgeLimit() int {
	if c == nil || c.inlineKnowledgeMaxChars < 0 {
		return 0
	}
	return c.inlineKnowledgeMaxChars
}

// CanRetrieveKnowledge reports whether in-call knowledge base retrieval is
// configured, so the call-resolution path can skip loading an agent's
// attachments on a server that could not query them anyway.
func (c *Client) CanRetrieveKnowledge() bool {
	return c != nil && c.retriever != nil && c.retriever.Enabled()
}

// Attach registers the incoming-call handler on a per-number whatsmeow client
// and returns the meowcaller client that owns it. It MUST be called after
// whatsmeow.NewClient and before wa.Connect so the low-level call handlers are
// installed before any call node arrives — including for a not-yet-paired QR
// login, whose device only becomes call-answerable once pairing completes. The
// caller must retain the returned client for the session's lifetime so it is not
// garbage-collected. Returns nil when the feature is disabled (nil receiver) or
// wa is nil.
//
// resolveAgent is evaluated once per incoming call, at call time (when the device
// is paired and its identity is known), to decide whether this client answers and
// with which agent. Returning a non-nil *CallAgent answers the call with that
// agent's prompt; returning nil declines the call, leaving normal WhatsApp ringing
// behavior so the caller reaches the human on the phone as usual. Gating here
// rather than at attach time is what lets a freshly paired device still receive
// its handler before Connect yet answer only when a live agent is assigned to it.
// A nil resolveAgent answers every call with the default configuration.
func (c *Client) Attach(wa *whatsmeow.Client, resolveAgent func() *CallAgent) *meowcaller.Client {
	if c == nil || wa == nil {
		return nil
	}

	// Route meowcaller's zerolog through the standard logger like the whatsmeow
	// client logger does, so call diagnostics land in the same tee'd server log.
	callLog := zerolog.New(log.Writer()).Level(zerolog.InfoLevel).With().
		Str("component", "meowcaller").
		Logger()

	opts := []meowcaller.Option{meowcaller.WithLogger(callLog)}
	if c.diag != nil {
		opts = append(opts, meowcaller.WithDiagnostics(c.diag))
	}

	callClient := meowcaller.NewClient(wa, opts...)
	callClient.OnIncomingCall(func(call *meowcaller.Call) {
		agent := &CallAgent{}
		if resolveAgent != nil {
			agent = resolveAgent()
			if agent == nil {
				log.Printf("voicecall: not answering incoming call %s from %s — no live agent for this number; leaving normal WhatsApp behavior", call.ID(), peerAddress(call))
				return
			}
		}
		if !c.claimCall(call.ID()) {
			log.Printf("voicecall: ignoring repeat offer for call %s; already answering it", call.ID())
			return
		}
		c.handleCall(call, agent)
	})
	return callClient
}

// PlaceCall places an outbound call from caller to target and runs the agent's
// realtime conversation on it for the call's lifetime. target is a phone number
// or a WhatsApp JID; agent carries the same per-call configuration Attach
// resolves for an incoming call, and names the user the call is recorded under.
// caller is the meowcaller client returned by Attach for the number the call is
// placed from.
//
// It returns once the callee's phone is ringing, handing back the calls-table
// row id of the newly recorded call. The call itself continues in the
// background and advances to "answered" (media flowing) and "ended" through its
// own lifecycle, so the returned id is how the API caller follows it. An error
// means no call is in progress.
//
// Unlike an incoming call, an outbound one is only placed when it can be
// recorded: the caller is handed an id to follow it by, so a call store is
// required, and a failed insert hangs the call up rather than leaving a live
// call nobody can look up.
func (c *Client) PlaceCall(ctx context.Context, caller *meowcaller.Client, target string, agent *CallAgent) (string, error) {
	if c == nil {
		return "", errors.New("voice calling is not configured on this server")
	}
	if caller == nil {
		return "", errors.New("this phone number has no call client attached")
	}
	if agent == nil || strings.TrimSpace(agent.UserID) == "" {
		return "", errors.New("an owning agent is required to place a call")
	}
	if c.store == nil {
		return "", errors.New("call persistence is not configured")
	}

	log.Printf("voicecall: placing outbound call to %s for agent %s", target, agent.AgentID)
	call, err := caller.Call(ctx, target)
	if err != nil {
		return "", fmt.Errorf("place call: %w", err)
	}

	// Claim the id the same way an inbound offer does, so the two paths can never
	// both drive the same call.
	if !c.claimCall(call.ID()) {
		_ = call.Hangup()
		return "", fmt.Errorf("call %s is already being handled", call.ID())
	}

	// Recording before wiring the bridge costs one INSERT of dead air, which the
	// callee's phone spends ringing — media cannot arrive until someone accepts.
	record := c.beginCallRecord(call, agent, models.CallTypeOutbound)
	if record == nil {
		log.Printf("voicecall: hanging up outbound call %s because it could not be recorded", call.ID())
		c.releaseCall(call.ID())
		_ = call.Hangup()
		return "", errors.New("the call could not be recorded")
	}

	// runCall records and releases whatever it fails on, so the only thing left
	// to do here is take the call back off the wire.
	if err := c.runCall(call, agent, record, models.CallTypeOutbound); err != nil {
		_ = call.Hangup()
		return "", err
	}
	return record.id, nil
}

// claimCall reserves callID for this handler, reporting false when another
// handler is already answering it (a re-delivered offer). releaseCall gives the
// ID back when the call ends.
func (c *Client) claimCall(callID string) bool {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()
	if c.active[callID] {
		return false
	}
	c.active[callID] = true
	return true
}

func (c *Client) releaseCall(callID string) {
	c.activeMu.Lock()
	delete(c.active, callID)
	c.activeMu.Unlock()
}

// peerAddress is how a call's remote party is identified in call history and logs:
// their phone JID when the offer disclosed it, otherwise the LID we answered — the
// LID is an opaque WhatsApp identifier, so it is only ever a fallback.
func peerAddress(call *meowcaller.Call) string {
	if pn := call.PeerPhone(); !pn.IsEmpty() {
		return pn.String()
	}
	return call.Peer().String()
}

// callRecord tracks the calls-table row for one handled call so handleCall can
// advance its status through the call lifecycle. A nil *callRecord — no store
// configured, no user to record under, or the initial insert failed — makes every
// method a no-op, so persistence never affects whether a call is answered.
type callRecord struct {
	store CallStore
	id    string
}

// beginCallRecord inserts the initial "received" row for a call in the given
// direction (models.CallTypeInbound or CallTypeOutbound) and returns a handle to
// advance it. It returns nil (persistence off for this call) when no store is
// configured or the agent carries no owning user — including the default
// no-agent path. A failed insert is logged and also yields nil so an inbound
// call still proceeds; PlaceCall treats the nil as fatal instead, because an
// outbound call the caller cannot be handed back is worse than no call at all.
func (c *Client) beginCallRecord(call *meowcaller.Call, agent *CallAgent, callType string) *callRecord {
	if c.store == nil || agent == nil || strings.TrimSpace(agent.UserID) == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), callRecordTimeout)
	defer cancel()

	id, err := c.store.Insert(ctx, models.NewCall{
		CallID:        call.ID(),
		UserID:        agent.UserID,
		PhoneNumberID: agent.PhoneNumberID,
		AgentID:       agent.AgentID,
		Peer:          peerAddress(call),
		CallType:      callType,
	})
	if err != nil {
		log.Printf("voicecall: could not record %s call %s: %v", callType, call.ID(), err)
		return nil
	}
	return &callRecord{store: c.store, id: id}
}

func (r *callRecord) answered() {
	r.update("answered", func(ctx context.Context) error {
		return r.store.MarkAnswered(ctx, r.id)
	})
}

func (r *callRecord) ended(reason string) {
	r.update("ended", func(ctx context.Context) error {
		return r.store.MarkEnded(ctx, r.id, reason)
	})
}

func (r *callRecord) declined(reason string) {
	r.update("declined", func(ctx context.Context) error {
		return r.store.MarkDeclined(ctx, r.id, reason)
	})
}

func (r *callRecord) failed(reason string) {
	r.update("failed", func(ctx context.Context) error {
		return r.store.MarkFailed(ctx, r.id, reason)
	})
}

// saveTranscript persists the conversation of the ended call. Like every
// call-record method it is nil-safe (persistence off for this call) and
// best-effort: an empty transcript is skipped and a write failure is logged but
// never propagated, so recording the conversation cannot disrupt the call.
func (r *callRecord) saveTranscript(messages []models.CallMessage) {
	if r == nil || len(messages) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callRecordTimeout)
	defer cancel()
	saved, err := r.store.SaveTranscript(ctx, r.id, messages)
	if err != nil {
		log.Printf("voicecall: could not save transcript for call %s: %v", r.id, err)
		return
	}
	log.Printf("voicecall: saved %d transcript messages for call %s", saved, r.id)
}

// toCallMessages converts a bridge's internal conversation turns into the
// persistence model, dropping any turn with empty content.
func toCallMessages(messages []conversationMessage) []models.CallMessage {
	out := make([]models.CallMessage, 0, len(messages))
	for _, m := range messages {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		out = append(out, models.CallMessage{Role: m.Role, Content: m.Content})
	}
	return out
}

// update runs one bounded status transition. It is nil-safe so callers never
// have to guard the handle, and a write failure is logged but not propagated —
// call persistence is best-effort and must not disrupt the call itself.
func (r *callRecord) update(status string, fn func(context.Context) error) {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callRecordTimeout)
	defer cancel()
	if err := fn(ctx); err != nil {
		log.Printf("voicecall: could not mark call %s as %s: %v", r.id, status, err)
	}
}

// Close releases the shared diagnostics recorder, if one is open. Safe on nil.
func (c *Client) Close() error {
	if c == nil || c.diag == nil {
		return nil
	}
	return c.diag.Close()
}

// handleCall answers one incoming call and runs the realtime conversation for
// its lifetime. The agent supplies per-call overrides; a nil agent (or empty
// field) falls back to the configured server default.
func (c *Client) handleCall(call *meowcaller.Call, agent *CallAgent) {
	log.Printf("voicecall: incoming call from %s (id=%s, video=%t)", peerAddress(call), call.ID(), call.IsVideo())

	// Record the call from the moment it is handled, so even a call we fail to
	// answer below is captured in its history.
	record := c.beginCallRecord(call, agent, models.CallTypeInbound)

	// An inbound offer is ours to answer; runCall does that once the bridge is
	// wired. A failure there is already logged and recorded, and there is no
	// caller to hand it back to.
	_ = c.runCall(call, agent, record, models.CallTypeInbound)
}

// runCall wires the voice bridge onto an established call and runs the realtime
// conversation for its lifetime, in either direction. It is a trimmed port of
// meowcaller-test's handleCall: it wires the realtime bridge as the
// incoming-audio sink and streams the bridge's output back into the call.
//
// callType decides how the call is put through. An inbound call is answered
// here, and is "answered" in its history the moment Answer succeeds. An outbound
// call is already ringing by the time runCall is reached — the peer answers it —
// so its record advances to "answered" when media first flows.
//
// It returns an error only for a failure that means no conversation will happen:
// an answer the server refused. It is logged and recorded before returning.
// Everything past that point is reported through the call's own lifecycle
// callbacks.
func (c *Client) runCall(call *meowcaller.Call, agent *CallAgent, record *callRecord, callType string) error {
	// Per-call config starts from environment defaults, then applies the assigned
	// agent's overrides without mutating the shared client configuration.
	openAICfg := c.openAI
	instructions := c.openAI.Instructions
	beginMessageMode := c.openAI.BeginMessageMode
	welcomeMessage := c.openAI.WelcomeMessage
	welcomeDelayMs := c.openAI.WelcomeDelayMs
	speed := c.openAI.Speed
	volume := c.openAI.Volume
	if agent != nil && strings.TrimSpace(agent.Instructions) != "" {
		instructions = agent.Instructions
		openAICfg.Instructions = agent.Instructions
		log.Printf("voicecall: call %s using agent prompt (%d chars)", call.ID(), len(agent.Instructions))
	}

	// The agent's knowledge bases and its attached tools become the functions the
	// model may call mid-turn. Both are resolved here, once per call rather than
	// once per invocation, and the prompt gains the directives that make the
	// model reach for them — a tool nothing tells the model to use is rarely
	// called.
	//
	// controls is declared before the toolbox and filled in below, because the
	// actions it performs (hanging up after the closing line, sending a message
	// to the peer) are owned by the playback and lifecycle machinery further
	// down this function.
	controls := &liveCallControls{call: call}
	agentVoice := ""
	var toolbox *combinedToolbox
	inlineKnowledge := ""
	if agent != nil {
		controls.useSender(agent.SendMessage)
		// A small knowledge base goes straight into the instructions; only a
		// large one is searched mid-call (see renderInlineKnowledge).
		inlineKnowledge = renderInlineKnowledge(agent.KnowledgeDocuments)
		var knowledge *knowledgeToolbox
		if inlineKnowledge == "" {
			knowledge = newKnowledgeToolbox(c.retriever, agent.KnowledgeBases, call.ID())
		} else {
			log.Printf("voicecall: call %s has its knowledge base in the prompt (%d sources, %d chars)", call.ID(), len(agent.KnowledgeDocuments), len(inlineKnowledge))
		}
		var reserved []string
		if knowledge != nil {
			reserved = append(reserved, knowledgeToolName)
		}
		custom := newCustomToolbox(agent.Tools, reserved, controls, call.ID())
		toolbox = newCombinedToolbox(knowledge, custom)
	}
	toolDirectives := ""
	if toolbox != nil {
		toolDirectives = toolbox.Instructions()
		openAICfg.Tools = toolbox
		log.Printf("voicecall: call %s can use %s", call.ID(), toolbox.describe())
	}
	// The owner's prompt first, then what the agent knows and can do, and the
	// phone-manner and accuracy rules last, where they carry the most weight.
	instructions = composeCallInstructions(instructions, inlineKnowledge, toolDirectives)
	openAICfg.Instructions = instructions
	if agent != nil {
		if mode := strings.TrimSpace(agent.BeginMessageMode); mode != "" {
			beginMessageMode = mode
		}
		welcomeMessage = agent.WelcomeMessage
		welcomeDelayMs = agent.WelcomeDelayMs
		openAICfg.BeginMessageMode = beginMessageMode
		openAICfg.WelcomeMessage = welcomeMessage
		openAICfg.WelcomeDelayMs = welcomeDelayMs
		if beginMessageMode != "" {
			log.Printf("voicecall: call %s begin_message_mode=%s", call.ID(), beginMessageMode)
		}
		agentVoice = strings.TrimSpace(agent.Voice)
		if agentVoice != "" {
			openAICfg.Voice = normalizeOpenAIRealtimeVoice(agentVoice)
		}
		if model := strings.TrimSpace(agent.RealtimeModel); model != "" {
			openAICfg.RealtimeModel = model
		} else if c.gemini.Enabled() {
			// No model saved: Gemini Live is the default engine.
			openAICfg.RealtimeModel = c.gemini.Model
		}
		openAICfg.Language = strings.TrimSpace(agent.Language)
		if agent.Speed > 0 {
			speed = clampSpeed(agent.Speed)
			openAICfg.Speed = speed
		}
		if agent.Volume > 0 {
			volume = clampVolume(agent.Volume)
			openAICfg.Volume = volume
		}
		if speed != defaultSpeed || volume != defaultVolume {
			log.Printf("voicecall: call %s using agent speed=%.2f volume=%.2f", call.ID(), speed, volume)
		}
	}

	// The greeting is synthesized before anyone can hear it — while an outbound
	// callee's phone is still ringing, or while an inbound call is still being
	// answered — so a delay left on the bridge would elapse against the ringing
	// instead of against the live call, and the agent's configured pause would
	// never be heard. Hold it here and apply it from the moment the call goes
	// live, which is what the setting means.
	liveDelayMs := 0
	if agentSpeaksFirst(beginMessageMode) {
		liveDelayMs = welcomeDelayMs
		welcomeDelayMs = 0
		openAICfg.WelcomeDelayMs = 0
	}

	// Only one player streams into the call at a time. playSource swaps in a new
	// one (stopping the old) for each response. Audio generated before the call
	// is live (an outbound greeting while the destination rings, an inbound one
	// while the answer is in flight) is held without being consumed until then
	// (see markLive and the answer below), so its first words are not lost.
	var playerMu sync.Mutex
	var player *meowcaller.Player
	var pendingSource meowcaller.AudioSource
	var pendingLabel string
	playbackEnabled := false

	// The watchdog starts when the call goes live and stops when it ends. An
	// account with fewer voice minutes left than the call-length cap gets the
	// shorter cap.
	watchdogCfg := c.watchdog
	if agent != nil {
		watchdogCfg = watchdogCfg.withMinutesLeft(agent.MinutesLeft)
		if watchdogCfg.minutesCapped {
			log.Printf("voicecall: call %s limited to %s by the account's remaining voice minutes", call.ID(), watchdogCfg.maxDuration)
		}
	}
	watchdog := newCallWatchdog(watchdogCfg)
	watchdogStop := make(chan struct{})
	var watchdogStopOnce sync.Once
	stopWatchdog := func() { watchdogStopOnce.Do(func() { close(watchdogStop) }) }

	// hangupOnFailure says whether a Realtime session that dies should take
	// the call down with it. It is off while an inbound call is still waiting
	// to be answered — that path declines the call instead, leaving it to ring
	// for a human — and on for everything already in progress.
	var hangupOnFailure atomic.Bool
	hangupOnFailure.Store(callType == models.CallTypeOutbound)
	var failureReason atomic.Value
	var callEnded atomic.Bool

	// Assigned once the bridge exists, below; the closures that read it only
	// run after that.
	var voiceBridge voiceSession

	stopPlayer := func() {
		playerMu.Lock()
		current := player
		pending := pendingSource
		player = nil
		pendingSource = nil
		pendingLabel = ""
		playerMu.Unlock()
		if current != nil {
			current.Stop()
		}
		if pending != nil {
			_ = pending.Close()
		}
	}

	playNow := func(source meowcaller.AudioSource, label string) {
		next := meowcaller.NewPlayer()
		// The sequence number is what lets a tool-armed hangup wait for the
		// agent's closing line specifically, rather than firing on whatever
		// happened to be playing when the tool ran.
		seq := controls.playbackStarted()
		watchdog.touch()
		next.OnFinish(func() {
			log.Printf("voicecall: finished playing %s into call %s", label, call.ID())
			playerMu.Lock()
			if player == next {
				player = nil
			}
			playerMu.Unlock()
			// Silence is counted from the end of what the agent said.
			watchdog.touch()
			controls.playbackFinished(seq)
		})

		playerMu.Lock()
		old := player
		player = next
		playerMu.Unlock()

		if old != nil {
			old.Stop()
		}

		call.Subscribe(next)
		next.Play(source)
		log.Printf("voicecall: playing %s into call %s", label, call.ID())
	}

	playSource := func(source meowcaller.AudioSource, label string) {
		playerMu.Lock()
		if playbackEnabled {
			playerMu.Unlock()
			playNow(source, label)
			return
		}
		oldPending := pendingSource
		pendingSource = source
		pendingLabel = label
		playerMu.Unlock()
		if oldPending != nil {
			_ = oldPending.Close()
		}
		log.Printf("voicecall: holding %s until call %s is live", label, call.ID())
	}

	// startWatchdog begins the silence and call-length checks once the call is
	// live in either direction.
	startWatchdog := func() {
		watchdog.start(call.ID(), watchdogStop,
			func() bool {
				playerMu.Lock()
				playing := player != nil
				playerMu.Unlock()
				return playing || voiceBridge.Busy()
			},
			voiceBridge.RequestResponse,
			controls.EndCall,
		)
	}

	enablePlayback := func() {
		playerMu.Lock()
		if playbackEnabled {
			playerMu.Unlock()
			return
		}
		playbackEnabled = true
		pending := pendingSource
		label := pendingLabel
		pendingSource = nil
		pendingLabel = ""
		playerMu.Unlock()
		if pending != nil {
			playNow(pending, label)
		}
	}

	// An outbound call is only ready for the agent's voice once BOTH of its
	// signals have arrived: CallAccept says the callee picked up, and OnReady says
	// the relay is actually carrying their media. Accept lands first — before the
	// peer's media path is up — and every frame played into that window is encoded,
	// sent to the relay and dropped, so the callee hears the greeting start
	// mid-sentence. Waiting for media alone is not enough either: it can arrive
	// while the phone is still ringing, and the agent would talk to a ringtone.
	var liveMu sync.Mutex
	var callAccepted, mediaFlowing, wentLive bool
	var graceTimer, delayTimer *time.Timer

	// markLive releases the held audio once, after the agent's welcome delay,
	// and drops the grace timer that guards against media that never arrives.
	markLive := func(reason string) {
		liveMu.Lock()
		if wentLive {
			liveMu.Unlock()
			return
		}
		wentLive = true
		grace := graceTimer
		graceTimer = nil
		if liveDelayMs > 0 {
			delayTimer = time.AfterFunc(time.Duration(liveDelayMs)*time.Millisecond, enablePlayback)
		}
		liveMu.Unlock()

		if grace != nil {
			grace.Stop()
		}
		log.Printf("voicecall: outbound call %s is live (%s); agent audio starts in %dms", call.ID(), reason, liveDelayMs)
		if liveDelayMs <= 0 {
			enablePlayback()
		}
		startWatchdog()
	}

	var answeredOnce sync.Once
	call.OnStateChange(func(phase meowcaller.CallPhase) {
		log.Printf("voicecall: call %s state: %s", call.ID(), callPhaseName(phase))
	})
	call.OnAccepted(func() {
		log.Printf("voicecall: outbound call %s was accepted", call.ID())
		if callType != models.CallTypeOutbound {
			return
		}
		liveMu.Lock()
		callAccepted = true
		ready := mediaFlowing
		if !ready && !wentLive && graceTimer == nil {
			graceTimer = time.AfterFunc(outboundMediaGrace, func() {
				markLive("no inbound media within " + outboundMediaGrace.String())
			})
		}
		liveMu.Unlock()

		// Published to OnEnd first: a call that ends while this write is in flight
		// must still be recorded as one that connected, not as declined.
		answeredOnce.Do(record.answered)

		if ready {
			markLive("media was already flowing")
		}
	})
	call.OnReady(func() {
		log.Printf("voicecall: call %s media is flowing", call.ID())
		if callType != models.CallTypeOutbound {
			return
		}
		liveMu.Lock()
		mediaFlowing = true
		accepted := callAccepted
		liveMu.Unlock()

		if accepted {
			markLive("media flowing")
		}
	})

	var incomingSinks []meowcaller.AudioSink

	// Optional debug aid: dump the caller's audio to a per-call WAV. Off unless
	// VOICECALL_RECORD_DIR is set.
	if c.recordDir != "" {
		path := filepath.Join(c.recordDir, "caller-"+sanitizeCallID(call.ID())+".wav")
		if recorder, err := meowcaller.WAVRecorder(path); err != nil {
			log.Printf("voicecall: could not create recording %s: %v", path, err)
		} else {
			// Recording is diagnostic and may touch a slow filesystem. Keep it
			// asynchronous so it cannot hold up VAD/STT on the call audio path.
			incomingSinks = append(incomingSinks, newAsyncAudioSink(recorder, 64))
			log.Printf("voicecall: recording caller audio to %s", path)
		}
	}

	hooks := bridgeHooks{
		onCallerSpeech: watchdog.callerSpoke,
		onFailure: func(err error) {
			if callEnded.Load() || !hangupOnFailure.Load() {
				return
			}
			// The agent can no longer hear or speak; an open line with nobody
			// on it is worse for the caller than an ended call.
			log.Printf("voicecall: call %s lost its AI session (%v); hanging up", call.ID(), err)
			failureReason.Store("ai session lost: " + err.Error())
			if hangupErr := call.Hangup(); hangupErr != nil {
				log.Printf("voicecall: call %s could not be hung up: %v", call.ID(), hangupErr)
			}
		},
	}
	if c.useGemini(openAICfg.RealtimeModel) {
		geminiCfg := c.geminiCallConfig(openAICfg, agentVoice)
		log.Printf("voicecall: call %s using Gemini Live model=%q voice=%q language=%q", call.ID(), geminiCfg.Model, geminiCfg.Voice, openAICfg.Language)
		voiceBridge = newGeminiLiveBridge(geminiCfg, func(source meowcaller.AudioSource) {
			playSource(source, "Gemini Live response")
		}, stopPlayer, hooks)
	} else {
		if isGeminiModel(openAICfg.RealtimeModel) {
			// The agent picked Gemini but this server has no Gemini key.
			log.Printf("voicecall: call %s: agent model %q needs GEMINI_API_KEY; using OpenAI Realtime %q instead", call.ID(), openAICfg.RealtimeModel, c.openAI.RealtimeModel)
			openAICfg.RealtimeModel = c.openAI.RealtimeModel
		}
		log.Printf("voicecall: call %s using OpenAI Realtime model=%q voice=%q language=%q", call.ID(), openAICfg.RealtimeModel, openAICfg.Voice, openAICfg.Language)
		voiceBridge = newOpenAIRealtimeVoiceBridgeWithHooks(openAICfg, func(source meowcaller.AudioSource) {
			playSource(source, "OpenAI Realtime response")
		}, stopPlayer, hooks)
	}
	// The bridge is the one sink that needs the caller's pauses delivered as
	// audio rather than as a gap in the stream: the Realtime VAD decides the turn
	// is over by how much silence it has heard.
	incomingSinks = append(incomingSinks, newSilenceFilledSink(voiceBridge, silenceFillWindow()))

	// Wire the incoming sink before answering so no caller audio is missed once
	// media starts flowing.
	incomingAudio := multiSink(incomingSinks)
	call.Receive(incomingAudio)

	// declinedToHuman marks an inbound call this server chose not to answer
	// after all (the AI session could not start, or the answer failed). Its
	// outcome is already recorded, so OnEnd — which still fires when the call
	// finally ends elsewhere — must not record it again.
	var declinedToHuman atomic.Bool

	// cleanup tears down everything this call holds, once, whichever path gets
	// there first: the call ending, or an inbound call given up before answer.
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			stopWatchdog()
			c.releaseCall(call.ID())
			if record != nil {
				c.forgetLive(record.id)
			}
			stopPlayer()
			// Close first: it waits for the bridge's goroutines to finish, so the
			// transcript read below sees every completed turn and no concurrent
			// write.
			_ = voiceBridge.Close()
			record.saveTranscript(toCallMessages(voiceBridge.Transcript()))
			if err := incomingAudio.Close(); err != nil {
				log.Printf("voicecall: could not close incoming audio sinks for call %s: %v", call.ID(), err)
			}
		})
	}

	call.OnEnd(func(reason string) {
		log.Printf("voicecall: call %s ended: %s", call.ID(), reason)
		callEnded.Store(true)
		stopWatchdog()
		// The wire reason is usually empty when this side hung up; say why.
		if strings.TrimSpace(reason) == "" {
			if failed, ok := failureReason.Load().(string); ok {
				reason = failed
			} else if released := controls.releaseReason(); released != "" {
				reason = released
			}
		}
		// A release a tool armed moments ago must not fire against a call that is
		// already gone.
		controls.stop()
		// A call that ends while still waiting to go live must not be woken by its
		// own timers afterwards.
		liveMu.Lock()
		accepted := callAccepted
		wentLive = true
		pending := []*time.Timer{graceTimer, delayTimer}
		graceTimer, delayTimer = nil, nil
		liveMu.Unlock()

		// An outbound call that ends without the callee ever accepting it was cut on
		// their side while it rang. Recording that as "ended" would make it
		// indistinguishable in history from a conversation that ran its course, so it
		// gets its own status; the wire reason is kept either way.
		switch {
		case declinedToHuman.Load():
			// Already recorded as failed when this server gave the call up.
		case callType == models.CallTypeOutbound && !accepted:
			record.declined(reason)
		default:
			record.ended(reason)
		}
		for _, timer := range pending {
			if timer != nil {
				timer.Stop()
			}
		}
		cleanup()
	})

	// Reachable by HangupCall from here on. A call that ended before it was
	// tracked has already run cleanup, so it is taken straight back out.
	if record != nil {
		c.trackLive(record.id, liveCall{call: call, reason: &failureReason})
		if callEnded.Load() {
			c.forgetLive(record.id)
		}
	}

	if callType == models.CallTypeOutbound {
		// The offer is already on the wire and the callee's phone is ringing. From
		// here the call runs entirely on its lifecycle callbacks, which is why this
		// returns while the call is still live.
		log.Printf("voicecall: outbound call %s is ringing %s", call.ID(), peerAddress(call))
		time.AfterFunc(outboundRingTimeout, func() {
			liveMu.Lock()
			accepted := callAccepted
			liveMu.Unlock()
			if accepted || callEnded.Load() {
				return
			}
			log.Printf("voicecall: outbound call %s not accepted within %s; hanging up", call.ID(), outboundRingTimeout)
			failureReason.Store("no answer within " + outboundRingTimeout.String())
			if err := call.Hangup(); err != nil {
				log.Printf("voicecall: call %s could not be hung up: %v", call.ID(), err)
			}
		})
		return nil
	}

	// Let the phone ring while the Realtime session connects, so the caller is
	// picked up by an agent that can already hear and speak. A session that
	// cannot start at all means the call is never answered here: it keeps
	// ringing on the owner's phone, exactly as for a number with no live agent.
	waitStarted := time.Now()
	select {
	case <-voiceBridge.Ready():
		if err := voiceBridge.Err(); err != nil {
			if callEnded.Load() {
				return nil
			}
			log.Printf("voicecall: not answering call %s: the AI session could not start (%v); leaving it to ring for a person", call.ID(), err)
			declinedToHuman.Store(true)
			record.failed("ai session unavailable: " + err.Error())
			cleanup()
			return fmt.Errorf("realtime session: %w", err)
		}
		log.Printf("voicecall: call %s AI session ready after %dms of ringing", call.ID(), time.Since(waitStarted).Milliseconds())
	case <-time.After(c.answerWait):
		log.Printf("voicecall: call %s AI session not ready after %s; answering anyway", call.ID(), c.answerWait)
	}
	if callEnded.Load() {
		// The caller gave up while it rang; OnEnd has already recorded it.
		return nil
	}

	log.Printf("voicecall: answering call %s", call.ID())
	hangupOnFailure.Store(true)
	if err := call.Answer(); err != nil {
		log.Printf("voicecall: could not answer call %s: %v", call.ID(), err)
		declinedToHuman.Store(true)
		record.failed("answer failed: " + err.Error())
		cleanup()
		return fmt.Errorf("answer call: %w", err)
	}
	record.answered()
	log.Printf("voicecall: answered call %s", call.ID())

	// The call is live: release any greeting generated while it was being
	// answered, after the agent's configured pause.
	if liveDelayMs > 0 {
		liveMu.Lock()
		if !wentLive {
			delayTimer = time.AfterFunc(time.Duration(liveDelayMs)*time.Millisecond, enablePlayback)
		}
		liveMu.Unlock()
	} else {
		enablePlayback()
	}
	startWatchdog()
	// A session that failed in the instant before the answer was not hung up
	// by onFailure; do it now.
	if err := voiceBridge.Err(); err != nil && !callEnded.Load() {
		log.Printf("voicecall: call %s lost its AI session while answering (%v); hanging up", call.ID(), err)
		failureReason.Store("ai session lost: " + err.Error())
		_ = call.Hangup()
	}
	return nil
}

func callPhaseName(phase meowcaller.CallPhase) string {
	switch phase {
	case meowcaller.CallPhaseIdle:
		return "idle"
	case meowcaller.CallPhaseCalling:
		return "calling"
	case meowcaller.CallPhaseRinging:
		return "ringing"
	case meowcaller.CallPhaseConnecting:
		return "connecting"
	case meowcaller.CallPhaseActive:
		return "active"
	case meowcaller.CallPhaseEnded:
		return "ended"
	default:
		return fmt.Sprintf("unknown(%d)", phase)
	}
}

// sanitizeCallID makes a call ID safe to use as a filename component for the
// optional WAV recorder.
func sanitizeCallID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "call"
	}
	return b.String()
}
