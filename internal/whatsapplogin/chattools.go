package whatsapplogin

import (
	"context"
	"fmt"
	"log"
	"strings"

	"whatsapp-ai-caller-server/internal/agenttools"
	"whatsapp-ai-caller-server/internal/models"
)

// chatToolbox exposes one chat agent's attached tools as callable functions for
// one incoming message. The definitions are resolved when the message arrives,
// so calling one is the action itself and nothing else.
//
// It is the chat counterpart of voicecall's customToolbox and runs the same
// actions through the same runner; what differs is which tools a chat can offer
// at all, and what the model is told about using them.
type chatToolbox struct {
	byName map[string]models.AgentTool
	order  []string
	runner *agenttools.Runner
	// send delivers a send_text tool's message to the person in this chat. Nil
	// leaves the tool reporting a failure rather than silently doing nothing.
	send  func(ctx context.Context, body string) error
	label string
}

// newChatToolbox builds the toolbox for one reply, or returns nil when the agent
// has nothing it can do in a chat. A nil toolbox is the "no tools" case
// everywhere below, so no caller has to special-case an agent that only talks.
//
// end_call and transfer_call are dropped: both exist to release a phone call,
// and a WhatsApp conversation has no call to release. They are logged rather
// than silently ignored, because an agent configured with one is configured for
// something this medium cannot do.
func newChatToolbox(tools []models.AgentTool, agentID string, send func(ctx context.Context, body string) error) *chatToolbox {
	if len(tools) == 0 {
		return nil
	}
	label := chatLogLabel(agentID)
	box := &chatToolbox{
		byName: make(map[string]models.AgentTool, len(tools)),
		runner: agenttools.NewRunner(label),
		send:   send,
		label:  label,
	}
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		switch tool.Type {
		case models.ToolTypeAPIRequest, models.ToolTypeSendText:
		default:
			log.Printf("%s skipping tool %s (%s): %s only applies to a call, not a chat", label, tool.ID, name, tool.Type)
			continue
		}
		if _, clash := box.byName[name]; clash {
			log.Printf("%s skipping tool %s (%s): the name is already in use in this chat", label, tool.ID, name)
			continue
		}
		box.byName[name] = tool
		box.order = append(box.order, name)
	}
	if len(box.order) == 0 {
		return nil
	}
	return box
}

// chatLogLabel is the prefix one chat agent's tool activity is logged under.
func chatLogLabel(agentID string) string {
	return "whatsapp chat agent: agent " + agentID
}

// Definitions describes each attached tool to the model. The stored name and
// description are passed through untouched: they are prompt text, and they are
// the whole basis on which the model decides whether to call the tool.
func (t *chatToolbox) Definitions() []agenttools.Definition {
	if t == nil {
		return nil
	}
	definitions := make([]agenttools.Definition, 0, len(t.order))
	for _, name := range t.order {
		tool := t.byName[name]
		definitions = append(definitions, agenttools.Definition{
			Name:        name,
			Description: tool.Description,
			Parameters:  agenttools.ParameterSchema(tool),
		})
	}
	return definitions
}

// Instructions is appended to the agent's prompt when the toolbox is active. A
// tool the prompt never mentions is rarely called, and the rules that matter in
// a chat — call it instead of describing it, do not paste raw data back — have
// nowhere else to live.
func (t *chatToolbox) Instructions() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("You can take these actions in this conversation:")
	for _, name := range t.order {
		tool := t.byName[name]
		b.WriteString("\n- ")
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(tool.Description))
	}
	b.WriteString("\nCall a tool as soon as the conversation calls for it, rather than saying what you are about to do and waiting for the next message. ")
	b.WriteString("Never quote a tool name, a URL, or raw response data back — reply with the part that answers the person, in your own words. ")
	b.WriteString("If a tool reports a failure, say you could not complete that right now instead of inventing a result.")
	return b.String()
}

// Run executes one tool call and returns the text the model reads as the
// result. It never returns an error: a failure is reported to the model as
// text, so the agent can say it could not do something instead of the reply
// dying silently.
func (t *chatToolbox) Run(ctx context.Context, name, arguments string) string {
	if t == nil {
		return fmt.Sprintf("Unknown tool %q.", name)
	}
	tool, ok := t.byName[name]
	if !ok {
		return fmt.Sprintf("Unknown tool %q.", name)
	}

	args := agenttools.ParseArguments(t.label, name, arguments)

	switch tool.Type {
	case models.ToolTypeAPIRequest:
		return t.runner.RunAPIRequest(ctx, tool, args)
	case models.ToolTypeSendText:
		return t.runSendText(ctx, tool, args)
	default:
		return fmt.Sprintf("Tool %q is not something this conversation can do.", name)
	}
}

// runSendText sends the configured message as a WhatsApp message of its own —
// a link, an address, a reference number the agent should hand over as a
// separate message rather than fold into its reply.
func (t *chatToolbox) runSendText(ctx context.Context, tool models.AgentTool, args map[string]any) string {
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
	if t.send == nil {
		log.Printf("%s tool %s cannot send a message: this chat has no sender", t.label, tool.Name)
		return "The message could not be sent. Tell them you will follow up another way."
	}
	if err := t.send(ctx, body); err != nil {
		log.Printf("%s tool %s could not send a WhatsApp message: %v", t.label, tool.Name, err)
		return "The message could not be sent. Tell them you will follow up another way."
	}
	log.Printf("%s tool %s sent a WhatsApp message (%d chars)", t.label, tool.Name, len(body))
	return "The message has been sent on WhatsApp. It arrives as its own message, so do not repeat it."
}

// describe summarises what the agent can do, for the one log line that records
// it when a reply is prepared.
func (t *chatToolbox) describe() string {
	if t == nil || len(t.order) == 0 {
		return "none"
	}
	return fmt.Sprintf("%d tool(s) [%s]", len(t.order), strings.Join(t.order, ", "))
}
