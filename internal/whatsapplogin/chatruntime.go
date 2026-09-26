package whatsapplogin

import (
	"strings"
	"time"

	"whatsapp-ai-caller-server/internal/chatagents"
)

// chatFailureMaxLen keeps a provider's error body from flooding the dashboard.
const chatFailureMaxLen = 400

type chatFailure struct {
	message string
	at      time.Time
}

func (m *Manager) noteChatFailure(agentID, message string) {
	message = strings.TrimSpace(message)
	if len(message) > chatFailureMaxLen {
		message = message[:chatFailureMaxLen] + "…"
	}
	m.chatFailures.Store(agentID, chatFailure{message: message, at: time.Now().UTC()})
}

// ChatAgentRuntime reports what this running server knows about serving the
// chat agent: whether its provider has an API key here, and the last reply
// failure since the agent last answered. Both are facts about this process —
// its environment and its memory — not the saved agent, which is why they are
// reported alongside the agent rather than stored with it.
func (m *Manager) ChatAgentRuntime(agentID, provider string) *chatagents.RuntimeSection {
	runtime := &chatagents.RuntimeSection{ProviderConfigured: m.ai.Available(provider)}
	if !runtime.ProviderConfigured {
		runtime.LastError = providerKeyMissing(provider)
	}
	if value, ok := m.chatFailures.Load(agentID); ok {
		failure := value.(chatFailure)
		at := failure.at
		runtime.LastError, runtime.LastErrorAt = failure.message, &at
	}
	return runtime
}

func providerKeyMissing(provider string) string {
	key := "OPENAI_API_KEY"
	if provider == "anthropic" {
		key = "ANTHROPIC_API_KEY"
	}
	return "The server has no " + key + ", so this agent cannot reply. Set it in the server's environment and restart the server."
}
