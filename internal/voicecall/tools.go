package voicecall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types"

	"whatsapp-ai-caller-server/internal/models"
)

const (
	// toolResponseMaxChars bounds what an api_request tool hands back to the
	// model. Everything here is read before the reply begins, and the caller is
	// waiting through that, so a verbose endpoint is truncated rather than
	// allowed to set the turn's latency on its own.
	toolResponseMaxChars = 3000

	// toolBodyReadLimit bounds what is read off the wire in the first place, so a
	// misconfigured URL pointing at a large download cannot be pulled into memory
	// before the truncation above ever applies.
	toolBodyReadLimit = 1 << 20

	// toolDialTimeout bounds connection setup separately from the tool's own
	// timeout: a host that is simply unroutable should fail fast rather than
	// spend the whole budget the tool was given.
	toolDialTimeout = 5 * time.Second

	// allowPrivateTargetsEnv opts a deployment into letting tools reach private
	// and loopback addresses. It is off by default: tool URLs are supplied by
	// account holders, and a server that will fetch any URL on their behalf is a
	// way to reach services that are only reachable because they are next to it.
	// Self-hosted installs pointing an agent at an internal API set it.
	allowPrivateTargetsEnv = "VOICECALL_TOOL_ALLOW_PRIVATE_TARGETS"
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
	client   *http.Client
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
		client:   toolHTTPClient(),
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
			Parameters:  toolParameterSchema(tool),
		})
	}
	return definitions
}

// toolParameterSchema renders a tool's arguments as the JSON Schema the model
// fills in. Only api_request declares arguments of its own; send_text asks for
// the message when the tool does not carry a fixed one, and the two hangup
// tools take an optional reason, which is recorded in the call's end reason.
func toolParameterSchema(tool models.AgentTool) map[string]any {
	properties := map[string]any{}
	required := []string{}

	switch tool.Type {
	case models.ToolTypeAPIRequest:
		if tool.APIRequest != nil {
			for _, param := range tool.APIRequest.Parameters {
				name := strings.TrimSpace(param.Name)
				if name == "" {
					continue
				}
				paramType := strings.TrimSpace(param.Type)
				if paramType == "" {
					paramType = "string"
				}
				properties[name] = map[string]any{
					"type":        paramType,
					"description": param.Description,
				}
				if param.Required {
					required = append(required, name)
				}
			}
		}

	case models.ToolTypeSendText:
		if tool.SendText == nil || strings.TrimSpace(tool.SendText.Body) == "" {
			properties["message"] = map[string]any{
				"type":        "string",
				"description": "The message to send to the caller on WhatsApp.",
			}
			required = append(required, "message")
		}

	case models.ToolTypeEndCall, models.ToolTypeTransferCall:
		properties["reason"] = map[string]any{
			"type":        "string",
			"description": "Why the call is being wrapped up, in a few words. Recorded in the call history.",
		}
	}

	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
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

	args := parseToolArguments(t.callID, name, arguments)

	switch tool.Type {
	case models.ToolTypeAPIRequest:
		return t.runAPIRequest(ctx, tool, args)
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

// parseToolArguments decodes the JSON argument object the model produced.
// Arguments arrive as a string built token by token, so a truncated or
// malformed one is a real possibility; an empty map lets the tool report which
// argument is missing rather than failing on the parse.
func parseToolArguments(callID, name, arguments string) map[string]any {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return map[string]any{}
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
		log.Printf("voicecall: call %s tool %s received malformed arguments %q: %v", callID, name, arguments, err)
		return map[string]any{}
	}
	if args == nil {
		return map[string]any{}
	}
	return args
}

// runAPIRequest performs the tool's HTTP call and hands the response back to the
// model. On a GET or DELETE the model's arguments become the query string; on
// every other method they become the JSON request body, which is what an API
// expects in each case.
func (t *customToolbox) runAPIRequest(ctx context.Context, tool models.AgentTool, args map[string]any) string {
	cfg := tool.APIRequest
	if cfg == nil || strings.TrimSpace(cfg.URL) == "" {
		log.Printf("voicecall: call %s tool %s has no request configured", t.callID, tool.Name)
		return "This action is not configured correctly. Tell the caller you cannot do that right now."
	}

	if missing := missingRequiredArguments(cfg.Parameters, args); len(missing) > 0 {
		return fmt.Sprintf("Missing required argument(s): %s. Ask the caller for them, then call the tool again.",
			strings.Join(missing, ", "))
	}

	request, err := buildToolRequest(ctx, cfg, args)
	if err != nil {
		log.Printf("voicecall: call %s tool %s could not be prepared: %v", t.callID, tool.Name, err)
		return "This action is not configured correctly. Tell the caller you cannot do that right now."
	}

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if cfg.TimeoutSeconds <= 0 {
		timeout = models.ToolDefaultTimeoutSeconds * time.Second
	}

	// An async tool is fired and forgotten so the agent can keep talking. The
	// request outlives the turn, so it runs on its own context: the turn's
	// context is cancelled as soon as the reply is done, which would cancel the
	// very request this setting exists to let run unattended.
	if cfg.Async {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			status, _, err := t.send(request.WithContext(bgCtx))
			if err != nil {
				log.Printf("voicecall: call %s async tool %s failed: %v", t.callID, tool.Name, err)
				return
			}
			log.Printf("voicecall: call %s async tool %s returned %d", t.callID, tool.Name, status)
		}()
		return "The request was sent. Carry on with the conversation; there is no result to report."
	}

	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startedAt := time.Now()
	status, body, err := t.send(request.WithContext(requestCtx))
	elapsed := time.Since(startedAt).Milliseconds()
	if err != nil {
		log.Printf("voicecall: call %s tool %s failed after %dms: %v", t.callID, tool.Name, elapsed, err)
		return "The request could not be completed. Tell the caller you cannot look that up right now."
	}
	log.Printf("voicecall: call %s tool %s %s %s returned %d in %dms",
		t.callID, tool.Name, cfg.Method, redactedURL(cfg.URL), status, elapsed)

	if status >= 400 {
		// The status is worth handing over — a 404 is "no such booking", which the
		// agent can say — but the body of an error page is not, and reading one out
		// is exactly the failure mode the prompt warns against.
		return fmt.Sprintf("The request failed with status %d. Tell the caller you could not complete that.", status)
	}
	if body == "" {
		return fmt.Sprintf("The request succeeded (status %d) and returned no content.", status)
	}
	return fmt.Sprintf("The request succeeded (status %d). Response:\n%s", status, truncateToolText(body))
}

// send performs the request and reads a bounded amount of the response.
func (t *customToolbox) send(request *http.Request) (int, string, error) {
	response, err := t.client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, toolBodyReadLimit))
	if err != nil {
		return response.StatusCode, "", err
	}
	return response.StatusCode, strings.TrimSpace(string(body)), nil
}

// runEndCall releases the caller. The hangup is armed rather than performed:
// the model has not spoken its closing line yet when this returns, and cutting
// the line here would end the call on silence.
func (t *customToolbox) runEndCall(tool models.AgentTool, args map[string]any) string {
	reason := stringArgument(args, "reason")
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

	reason := stringArgument(args, "reason")
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
		body = stringArgument(args, "message")
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

// missingRequiredArguments reports the required parameters the model left out,
// so the result can name them instead of the request going out half-filled.
func missingRequiredArguments(parameters []models.ToolParameter, args map[string]any) []string {
	var missing []string
	for _, param := range parameters {
		if !param.Required {
			continue
		}
		name := strings.TrimSpace(param.Name)
		if name == "" {
			continue
		}
		value, present := args[name]
		// A whitespace-only value is as unusable as an absent one: the request
		// would go out asking for nothing, and the endpoint would answer about
		// nothing. false and 0 render non-empty, so a real value is never lost.
		if !present || value == nil || strings.TrimSpace(argumentToString(value)) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

// buildToolRequest renders the tool's configuration and the model's arguments
// into one HTTP request.
func buildToolRequest(ctx context.Context, cfg *models.ToolAPIRequestConfig, args map[string]any) (*http.Request, error) {
	method := models.NormalizeToolMethod(cfg.Method)
	endpoint, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("url must use http or https")
	}

	// Only the parameters the tool declares are forwarded. A model that invents
	// an extra field would otherwise get it sent to somebody's API as if the
	// tool had asked for it.
	declared := declaredArguments(cfg.Parameters, args)

	var body io.Reader
	if method == http.MethodGet || method == http.MethodDelete {
		query := endpoint.Query()
		for name, value := range declared {
			query.Set(name, argumentToString(value))
		}
		endpoint.RawQuery = query.Encode()
	} else {
		encoded, err := json.Marshal(declared)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	for _, header := range cfg.Headers {
		key := strings.TrimSpace(header.Key)
		if key == "" {
			continue
		}
		request.Header.Set(key, header.Value)
	}
	return request, nil
}

// declaredArguments narrows the model's arguments to the parameters the tool
// declares, keeping the declared order irrelevant and the extras out.
func declaredArguments(parameters []models.ToolParameter, args map[string]any) map[string]any {
	out := make(map[string]any, len(parameters))
	for _, param := range parameters {
		name := strings.TrimSpace(param.Name)
		if name == "" {
			continue
		}
		if value, ok := args[name]; ok && value != nil {
			out[name] = value
		}
	}
	return out
}

// argumentToString renders one model-supplied argument for a query string.
// Numbers arrive as float64 from encoding/json, and rendering 3 as "3" rather
// than "3.000000" is what an API expects to receive.
func argumentToString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case nil:
		return ""
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}

func stringArgument(args map[string]any, key string) string {
	value, ok := args[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(argumentToString(value))
}

func truncateToolText(text string) string {
	if len(text) <= toolResponseMaxChars {
		return text
	}
	return text[:toolResponseMaxChars] + "\n… (response truncated)"
}

// redactedURL renders a tool URL for the log without its query string, which is
// where the model's arguments — a caller's phone number, a booking reference —
// end up on a GET.
func redactedURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "(unparseable url)"
	}
	parsed.RawQuery = ""
	parsed.User = nil
	return parsed.String()
}

// toolHTTPClient builds the client every api_request tool shares. Redirects are
// followed but capped, and every connection attempt — the first one and each
// redirect's — passes through guardToolDial.
func toolHTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   toolDialTimeout,
		KeepAlive: 30 * time.Second,
		Control:   guardToolDial,
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConns:          32,
			IdleConnTimeout:       60 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

// guardToolDial refuses connections to addresses that are only reachable
// because the request originates from this server: loopback, private ranges,
// link-local (including the cloud metadata address) and the carrier-grade NAT
// range.
//
// The check runs at dial time, on the address actually being connected to,
// rather than on the hostname. A name that resolves to a public address when it
// is validated and to 127.0.0.1 when it is fetched would pass any check made
// earlier; here there is nothing left to change.
func guardToolDial(network, address string, _ syscall.RawConn) error {
	if allowPrivateToolTargets() {
		return nil
	}
	if !strings.HasPrefix(network, "tcp") {
		return fmt.Errorf("tool requests may only use tcp")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("unreadable address %q", address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("unresolved address %q", address)
	}
	if isBlockedToolIP(ip) {
		return fmt.Errorf("refusing to reach internal address %s; set %s=true to allow it", ip, allowPrivateTargetsEnv)
	}
	return nil
}

// carrierGradeNAT is 100.64.0.0/10, which is routable inside a provider's
// network but never on the public internet.
var carrierGradeNAT = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isBlockedToolIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && carrierGradeNAT.Contains(v4) {
		return true
	}
	return false
}

func allowPrivateToolTargets() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(allowPrivateTargetsEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
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
