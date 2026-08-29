package voicecall

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types"

	"whatsapp-ai-caller-server/internal/agenttools"
	"whatsapp-ai-caller-server/internal/models"
)

// callControls are the operations a tool performs on the live call itself, as
// opposed to on the outside world. Implemented by runCall, which owns the call
// and its playback; kept as an interface so the toolbox can be exercised
// without a call on the wire.
type callControls interface {
	// EndCall releases the caller once whatever the agent is about to say has
	// been spoken. It never hangs up on the spot: the model's closing line is
	// generated after the tool returns, so cutting the line here would end the
	// call on silence.
	EndCall(reason string)
	// SendText sends a WhatsApp message to the other party on this call.
	SendText(ctx context.Context, body string) error
}

// endCallGrace bounds how long a call armed for release waits for the agent's
// closing line to be spoken. The release normally fires when that line finishes
// playing; this covers the turn that never produces one — the model answered
// with the tool call alone, or the reply failed — so the caller is not left
// holding an open line with nothing on it.
const endCallGrace = 12 * time.Second

// liveCallControls performs a tool's call-level actions on the real call. It
// owns the release, which is why it also counts playback: a hangup asked for
// mid-turn has to wait for the agent's closing line, and "the line after the
// tool ran" is identifiable only by having started after the tool ran.
type liveCallControls struct {
	call *meowcaller.Call

	mu sync.Mutex
	// send is the WhatsApp sender for this call's number, supplied by whoever
	// owns the session. Nil leaves send_text reporting a failure rather than
	// silently doing nothing.
	send func(ctx context.Context, peer types.JID, body string) error
	// playSeq counts playbacks started on this call. armedSeq records the value
	// at the moment a release was armed, so only a playback that started after
	// it can trigger the hangup.
	playSeq  int
	armedSeq int
	armed    bool
	reason   string
	timer    *time.Timer
	released bool
}

// useSender installs the WhatsApp sender the send_text tool uses.
func (c *liveCallControls) useSender(send func(ctx context.Context, peer types.JID, body string) error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.send = send
	c.mu.Unlock()
}

// playbackStarted records a new playback and returns its sequence number, which
// the caller hands back to playbackFinished when that playback ends.
func (c *liveCallControls) playbackStarted() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.playSeq++
	return c.playSeq
}

// playbackFinished releases the call when the playback that just ended is one
// that started after the release was armed — the agent's closing line.
func (c *liveCallControls) playbackFinished(seq int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	fire := c.armed && !c.released && seq > c.armedSeq
	c.mu.Unlock()
	if fire {
		c.release("closing line finished")
	}
}

// EndCall arms the release. It deliberately does not hang up here: the model
// generates its goodbye after the tool returns, so cutting the line now would
// end the call on silence.
func (c *liveCallControls) EndCall(reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.armed || c.released {
		c.mu.Unlock()
		return
	}
	c.armed = true
	c.reason = reason
	c.armedSeq = c.playSeq
	c.timer = time.AfterFunc(endCallGrace, func() {
		c.release("no closing line within " + endCallGrace.String())
	})
	c.mu.Unlock()
	log.Printf("voicecall: call %s will be released after the agent's closing line (%s)", c.call.ID(), reason)
}

// release hangs the call up, once.
func (c *liveCallControls) release(trigger string) {
	c.mu.Lock()
	if c.released {
		c.mu.Unlock()
		return
	}
	c.released = true
	reason := c.reason
	timer := c.timer
	c.timer = nil
	c.mu.Unlock()

	if timer != nil {
		timer.Stop()
	}
	log.Printf("voicecall: call %s hanging up (%s): %s", c.call.ID(), trigger, reason)
	if err := c.call.Hangup(); err != nil {
		log.Printf("voicecall: call %s could not be hung up: %v", c.call.ID(), err)
	}
}

// stop abandons a pending release. It is called when the call ends on its own,
// so a timer armed moments earlier cannot fire against a call that is gone.
func (c *liveCallControls) stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.released = true
	timer := c.timer
	c.timer = nil
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

// SendText sends the message to the other party on this call. The phone JID is
// preferred over the LID because that is the address a message is addressed to;
// a LID-only offer falls back to the LID, which whatsmeow can still route.
func (c *liveCallControls) SendText(ctx context.Context, body string) error {
	if c == nil {
		return fmt.Errorf("no call controls")
	}
	c.mu.Lock()
	send := c.send
	c.mu.Unlock()
	if send == nil {
		return fmt.Errorf("no WhatsApp sender is attached to this call")
	}
	peer := c.call.PeerPhone()
	if peer.IsEmpty() {
		peer = c.call.Peer()
	}
	if peer.IsEmpty() {
		return fmt.Errorf("this call has no addressable peer")
	}
	return send(ctx, peer, body)
}

// customToolbox exposes one agent's attached tools as callable functions for
// the length of a call. The definitions are resolved once, when the call is
// answered, so invoking one mid-call is the action itself and nothing else.
//
// It implements toolRunner, the same interface the knowledge lookup implements,
// which is what lets all three provider pipelines offer these tools without
// knowing anything about them.
type customToolbox struct {
	byName   map[string]models.AgentTool
	order    []string
	controls callControls
	runner   *agenttools.Runner
	callID   string

	// ended guards EndCall against a model that calls a hangup tool twice in one
	// turn, which would arm the release a second time after the first is already
	// counting down.
	ended sync.Once
}

// newCustomToolbox builds the toolbox for one call, or returns nil when the
// agent has no tools attached. A nil toolbox is the "no custom tools" case
// everywhere below, so no caller has to special-case an agent that only talks.
//
// reserved holds function names the call already offers (the knowledge lookup),
// so an agent whose tool happens to share one of those names cannot shadow it —
// the model would have no way to reach the built-in behind it.
func newCustomToolbox(tools []models.AgentTool, reserved []string, controls callControls, callID string) *customToolbox {
	if len(tools) == 0 {
		return nil
	}
	box := &customToolbox{
		byName:   make(map[string]models.AgentTool, len(tools)),
		controls: controls,
		runner:   agenttools.NewRunner(callLogLabel(callID)),
		callID:   callID,
	}
	taken := make(map[string]struct{}, len(reserved)+len(tools))
	for _, name := range reserved {
		taken[name] = struct{}{}
	}
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		if _, clash := taken[name]; clash {
			log.Printf("voicecall: call %s skipping tool %s (%s): the name is already in use on this call", callID, tool.ID, name)
			continue
		}
		taken[name] = struct{}{}
		box.byName[name] = tool
		box.order = append(box.order, name)
	}
	if len(box.order) == 0 {
		return nil
	}
	return box
}

// Definitions describes each attached tool to the model. The stored name and
// description are passed through untouched: they are prompt text, and they are
// the whole basis on which the model decides whether to call the tool.
func (t *customToolbox) Definitions() []toolDefinition {
	if t == nil {
		return nil
	}
	definitions := make([]toolDefinition, 0, len(t.order))
	for _, name := range t.order {
		tool := t.byName[name]
		definitions = append(definitions, toolDefinition{
			Name:        name,
			Description: tool.Description,
			Parameters:  agenttools.ParameterSchema(tool),
		})
	}
	return definitions
}

// Instructions is appended to the agent's prompt when the toolbox is active. A
// tool the prompt never mentions is rarely called, and the rules that matter on
// a phone call — do not narrate the tool, do not promise the outcome before it
// happened — have nowhere else to live.
func (t *customToolbox) Instructions() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("You can take these actions during this call:")
	for _, name := range t.order {
		tool := t.byName[name]
		b.WriteString("\n- ")
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(tool.Description))
	}
	b.WriteString("\nCall a tool as soon as the conversation calls for it, rather than describing what you are about to do. ")
	b.WriteString("Never read out a tool name, a URL, or raw data you get back — say only the part that answers the caller, in your own words. ")
	b.WriteString("If a tool reports a failure, tell the caller you could not complete that right now instead of inventing a result.")
	if t.hasHangupTool() {
		b.WriteString(" When you end the call, say a short goodbye in the same turn — the line stays open just long enough for it.")
	}
	return b.String()
}

func (t *customToolbox) hasHangupTool() bool {
	for _, name := range t.order {
		switch t.byName[name].Type {
		case models.ToolTypeEndCall, models.ToolTypeTransferCall:
			return true
		}
	}
	return false
}

// Run executes one tool call and returns the text the model reads as the
// result. It never returns an error: a failure is reported to the model as
// text, so the agent can tell the caller it could not do something instead of
// the turn dying silently.
func (t *customToolbox) Run(ctx context.Context, name, arguments string) string {
	if t == nil {
		return fmt.Sprintf("Unknown tool %q.", name)
	}
	tool, ok := t.byName[name]
	if !ok {
		return fmt.Sprintf("Unknown tool %q.", name)
	}

	args := agenttools.ParseArguments(callLogLabel(t.callID), name, arguments)

	switch tool.Type {
	case models.ToolTypeAPIRequest:
		return t.runner.RunAPIRequest(ctx, tool, args)
	case models.ToolTypeEndCall:
		return t.runEndCall(tool, args)
	case models.ToolTypeTransferCall:
		return t.runTransferCall(tool, args)
	case models.ToolTypeSendText:
		return t.runSendText(ctx, tool, args)
	default:
		return fmt.Sprintf("Tool %q is not something this call can do.", name)
	}
}

// callLogLabel is the prefix the shared tool runner logs one call's tool
// activity under, so those lines read exactly as they did when running a tool
// lived in this package.
func callLogLabel(callID string) string {
	return "voicecall: call " + callID
}

// runEndCall releases the caller. The hangup is armed rather than performed:
// the model has not spoken its closing line yet when this returns, and cutting
// the line here would end the call on silence.
func (t *customToolbox) runEndCall(tool models.AgentTool, args map[string]any) string {
	reason := agenttools.StringArgument(args, "reason")
	if reason == "" {
		reason = "agent ended the call"
	}
	t.armEnd(reason)

	if tool.EndCall != nil && strings.TrimSpace(tool.EndCall.Message) != "" {
		return "The call is being wrapped up. Say exactly this and nothing more: " +
			strings.TrimSpace(tool.EndCall.Message)
	}
	return "The call is being wrapped up. Say a short goodbye and nothing more."
}

// runTransferCall announces the handover and releases the caller.
//
// WhatsApp calling has no way to bridge a second leg onto a live call, so this
// cannot connect the two parties. What it does is the honest half of a
// transfer: the agent says the configured line, the destination is logged
// against the call, and the caller is released rather than left with an agent
// that has nothing left to offer them.
func (t *customToolbox) runTransferCall(tool models.AgentTool, args map[string]any) string {
	destination := ""
	message := ""
	if tool.TransferCall != nil {
		destination = strings.TrimSpace(tool.TransferCall.Destination)
		message = strings.TrimSpace(tool.TransferCall.Message)
	}

	reason := agenttools.StringArgument(args, "reason")
	endReason := "transferred to " + destination
	if destination == "" {
		endReason = "agent handed the call over"
	}
	if reason != "" {
		endReason += " (" + reason + ")"
	}
	log.Printf("voicecall: call %s tool %s handing the caller to %s", t.callID, tool.Name, destination)
	t.armEnd(endReason)

	if message != "" {
		return "Say exactly this and nothing more: " + message
	}
	return "Tell the caller you are handing them over now, in one short sentence, and nothing more."
}

// armEnd releases the call once, whichever tool asked for it.
func (t *customToolbox) armEnd(reason string) {
	if t.controls == nil {
		log.Printf("voicecall: call %s cannot end the call: no call controls on this bridge", t.callID)
		return
	}
	t.ended.Do(func() { t.controls.EndCall(reason) })
}

// runSendText sends the caller a WhatsApp message mid-call — a link, an
// address, a reference number: the things a phone call is bad at carrying.
func (t *customToolbox) runSendText(ctx context.Context, tool models.AgentTool, args map[string]any) string {
	body := ""
	if tool.SendText != nil {
		body = strings.TrimSpace(tool.SendText.Body)
	}
	if body == "" {
		body = agenttools.StringArgument(args, "message")
	}
	if body == "" {
		return "No message was given to send. Provide the message text and call the tool again."
	}
	if t.controls == nil {
		log.Printf("voicecall: call %s cannot send a message: no call controls on this bridge", t.callID)
		return "The message could not be sent. Tell the caller you will follow up another way."
	}

	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := t.controls.SendText(sendCtx, body); err != nil {
		log.Printf("voicecall: call %s tool %s could not send a WhatsApp message: %v", t.callID, tool.Name, err)
		return "The message could not be sent. Tell the caller you will follow up another way."
	}
	log.Printf("voicecall: call %s tool %s sent a WhatsApp message (%d chars)", t.callID, tool.Name, len(body))
	return "The message has been sent on WhatsApp. Tell the caller it is on its way."
}

// combinedToolbox offers the knowledge lookup and the agent's own tools as one
// set. Either half may be nil, which is what an agent with only one of them
// looks like; a combined box is only built when at least one is present.
type combinedToolbox struct {
	knowledge *knowledgeToolbox
	custom    *customToolbox
}

// newCombinedToolbox returns the runner for a call, or nil when the agent has
// neither knowledge bases nor tools. A single-sided box is returned as the
// concrete half rather than wrapped, so an agent that only retrieves behaves
// exactly as it did before tools existed.
func newCombinedToolbox(knowledge *knowledgeToolbox, custom *customToolbox) *combinedToolbox {
	if knowledge == nil && custom == nil {
		return nil
	}
	return &combinedToolbox{knowledge: knowledge, custom: custom}
}

func (t *combinedToolbox) Definitions() []toolDefinition {
	if t == nil {
		return nil
	}
	return append(t.knowledge.Definitions(), t.custom.Definitions()...)
}

// Run dispatches by name. The knowledge lookup is tried first because its name
// is reserved: newCustomToolbox drops an agent tool that would collide with it,
// so at most one half can answer to any given name.
func (t *combinedToolbox) Run(ctx context.Context, name, arguments string) string {
	if t == nil {
		return fmt.Sprintf("Unknown tool %q.", name)
	}
	if t.knowledge != nil && name == knowledgeToolName {
		return t.knowledge.Run(ctx, name, arguments)
	}
	if t.custom != nil {
		return t.custom.Run(ctx, name, arguments)
	}
	return fmt.Sprintf("Unknown tool %q.", name)
}

// Instructions is both halves' prompt directives, in the order they are
// declared to the model.
func (t *combinedToolbox) Instructions() string {
	if t == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if directive := t.knowledge.Instructions(); directive != "" {
		parts = append(parts, directive)
	}
	if directive := t.custom.Instructions(); directive != "" {
		parts = append(parts, directive)
	}
	return strings.Join(parts, "\n\n")
}

// describe summarises what the call can do, for the one log line that records
// it when the call is answered.
func (t *combinedToolbox) describe() string {
	if t == nil {
		return "none"
	}
	var parts []string
	if t.knowledge != nil && len(t.knowledge.namespaces) > 0 {
		parts = append(parts, fmt.Sprintf("%d knowledge base(s) [%s]",
			len(t.knowledge.namespaces), strings.Join(t.knowledge.names, ", ")))
	}
	if t.custom != nil && len(t.custom.order) > 0 {
		parts = append(parts, fmt.Sprintf("%d tool(s) [%s]",
			len(t.custom.order), strings.Join(t.custom.order, ", ")))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " and ")
}
