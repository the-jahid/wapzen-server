// Package agenttools executes the actions an agent may take mid-conversation.
//
// A tool row only defines the action; running one is the same work whether the
// agent is on a call or in a WhatsApp chat — the arguments the model filled in
// are checked against the tool's parameters, the request goes out through a
// client that refuses internal addresses, and the response comes back as text
// the model reads. That is what lives here, so the voice bridge and the chat
// responder run an agent's tools the same way rather than each having their own.
//
// What stays with the caller is everything conversation-specific: which tools
// are offered, how they are declared to the provider, and the actions that only
// exist in one medium (ending a call has no meaning in a chat).
package agenttools

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
	"syscall"
	"time"

	"whatsapp-ai-caller-server/internal/models"
)

const (
	// ResponseMaxChars bounds what an api_request tool hands back to the model.
	// Everything here is read before the reply begins, and the other party is
	// waiting through that, so a verbose endpoint is truncated rather than
	// allowed to set the turn's latency on its own.
	ResponseMaxChars = 3000

	// bodyReadLimit bounds what is read off the wire in the first place, so a
	// misconfigured URL pointing at a large download cannot be pulled into memory
	// before the truncation above ever applies.
	bodyReadLimit = 1 << 20

	// dialTimeout bounds connection setup separately from the tool's own
	// timeout: a host that is simply unroutable should fail fast rather than
	// spend the whole budget the tool was given.
	dialTimeout = 5 * time.Second

	// AllowPrivateTargetsEnv opts a deployment into letting tools reach private
	// and loopback addresses. It is off by default: tool URLs are supplied by
	// account holders, and a server that will fetch any URL on their behalf is a
	// way to reach services that are only reachable because they are next to it.
	// Self-hosted installs pointing an agent at an internal API set it.
	//
	// The name is unchanged from when this lived in the voicecall package, so an
	// existing deployment's configuration keeps working.
	AllowPrivateTargetsEnv = "VOICECALL_TOOL_ALLOW_PRIVATE_TARGETS"
)

// Definition is one function the model may call, in provider-neutral form.
// Each provider renders it into its own schema shape.
type Definition struct {
	Name        string
	Description string
	// Parameters is a JSON Schema object describing the arguments.
	Parameters map[string]any
}

// Runner performs the tools of one conversation. label prefixes its log lines
// with whatever identifies that conversation to whoever built it — a call id, an
// agent id — so a tool's behaviour can be traced back to the exchange it ran in.
type Runner struct {
	client *http.Client
	label  string
}

// NewRunner builds the runner for one conversation.
func NewRunner(label string) *Runner {
	return &Runner{client: HTTPClient(), label: label}
}

// ParameterSchema renders a tool's arguments as the JSON Schema the model fills
// in. Only api_request declares arguments of its own; send_text asks for the
// message when the tool does not carry a fixed one, and the two call-ending
// tools take an optional reason, which is recorded against the conversation.
func ParameterSchema(tool models.AgentTool) map[string]any {
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
				"description": "The message to send on WhatsApp.",
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

// ParseArguments decodes the JSON argument object the model produced.
// Arguments arrive as a string built token by token, so a truncated or
// malformed one is a real possibility; an empty map lets the tool report which
// argument is missing rather than failing on the parse.
func ParseArguments(label, name, arguments string) map[string]any {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return map[string]any{}
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
		log.Printf("%s tool %s received malformed arguments %q: %v", label, name, arguments, err)
		return map[string]any{}
	}
	if args == nil {
		return map[string]any{}
	}
	return args
}

// RunAPIRequest performs the tool's HTTP call and hands the response back to the
// model. On a GET or DELETE the model's arguments become the query string; on
// every other method they become the JSON request body, which is what an API
// expects in each case.
//
// It never returns an error: a failure is reported to the model as text, so the
// agent can say it could not do something instead of the turn dying silently.
func (r *Runner) RunAPIRequest(ctx context.Context, tool models.AgentTool, args map[string]any) string {
	cfg := tool.APIRequest
	if cfg == nil || strings.TrimSpace(cfg.URL) == "" {
		log.Printf("%s tool %s has no request configured", r.label, tool.Name)
		return "This action is not configured correctly. Tell them you cannot do that right now."
	}

	if missing := MissingRequired(cfg.Parameters, args); len(missing) > 0 {
		return fmt.Sprintf("Missing required argument(s): %s. Ask for them, then call the tool again.",
			strings.Join(missing, ", "))
	}

	request, err := BuildRequest(ctx, cfg, args)
	if err != nil {
		log.Printf("%s tool %s could not be prepared: %v", r.label, tool.Name, err)
		return "This action is not configured correctly. Tell them you cannot do that right now."
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
			status, _, err := r.send(request.WithContext(bgCtx))
			if err != nil {
				log.Printf("%s async tool %s failed: %v", r.label, tool.Name, err)
				return
			}
			log.Printf("%s async tool %s returned %d", r.label, tool.Name, status)
		}()
		return "The request was sent. Carry on with the conversation; there is no result to report."
	}

	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startedAt := time.Now()
	status, body, err := r.send(request.WithContext(requestCtx))
	elapsed := time.Since(startedAt).Milliseconds()
	if err != nil {
		log.Printf("%s tool %s failed after %dms: %v", r.label, tool.Name, elapsed, err)
		return "The request could not be completed. Tell them you cannot look that up right now."
	}
	log.Printf("%s tool %s %s %s returned %d in %dms",
		r.label, tool.Name, cfg.Method, redactedURL(cfg.URL), status, elapsed)

	if status >= 400 {
		// The status is worth handing over — a 404 is "no such booking", which the
		// agent can say — but the body of an error page is not, and repeating one
		// back is exactly the failure mode the prompt warns against.
		return fmt.Sprintf("The request failed with status %d. Tell them you could not complete that.", status)
	}
	if body == "" {
		return fmt.Sprintf("The request succeeded (status %d) and returned no content.", status)
	}
	return fmt.Sprintf("The request succeeded (status %d). Response:\n%s", status, TruncateText(body))
}

// send performs the request and reads a bounded amount of the response.
func (r *Runner) send(request *http.Request) (int, string, error) {
	response, err := r.client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, bodyReadLimit))
	if err != nil {
		return response.StatusCode, "", err
	}
	return response.StatusCode, strings.TrimSpace(string(body)), nil
}

// MissingRequired reports the required parameters the model left out, so the
// result can name them instead of the request going out half-filled.
func MissingRequired(parameters []models.ToolParameter, args map[string]any) []string {
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
		if !present || value == nil || strings.TrimSpace(ArgumentToString(value)) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

// BuildRequest renders the tool's configuration and the model's arguments into
// one HTTP request.
func BuildRequest(ctx context.Context, cfg *models.ToolAPIRequestConfig, args map[string]any) (*http.Request, error) {
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
			query.Set(name, ArgumentToString(value))
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

// ArgumentToString renders one model-supplied argument for a query string.
// Numbers arrive as float64 from encoding/json, and rendering 3 as "3" rather
// than "3.000000" is what an API expects to receive.
func ArgumentToString(value any) string {
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

// StringArgument reads one named argument as trimmed text.
func StringArgument(args map[string]any, key string) string {
	value, ok := args[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(ArgumentToString(value))
}

// TruncateText caps a tool result at what the model is given to read.
func TruncateText(text string) string {
	if len(text) <= ResponseMaxChars {
		return text
	}
	return text[:ResponseMaxChars] + "\n… (response truncated)"
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

// HTTPClient builds the client every api_request tool shares. Redirects are
// followed but capped, and every connection attempt — the first one and each
// redirect's — passes through guardDial.
func HTTPClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
		Control:   guardDial,
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

// guardDial refuses connections to addresses that are only reachable because
// the request originates from this server: loopback, private ranges, link-local
// (including the cloud metadata address) and the carrier-grade NAT range.
//
// The check runs at dial time, on the address actually being connected to,
// rather than on the hostname. A name that resolves to a public address when it
// is validated and to 127.0.0.1 when it is fetched would pass any check made
// earlier; here there is nothing left to change.
func guardDial(network, address string, _ syscall.RawConn) error {
	if allowPrivateTargets() {
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
	if isBlockedIP(ip) {
		return fmt.Errorf("refusing to reach internal address %s; set %s=true to allow it", ip, AllowPrivateTargetsEnv)
	}
	return nil
}

// carrierGradeNAT is 100.64.0.0/10, which is routable inside a provider's
// network but never on the public internet.
var carrierGradeNAT = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isBlockedIP(ip net.IP) bool {
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

func allowPrivateTargets() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(AllowPrivateTargetsEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
