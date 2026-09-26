package whatsapplogin

import (
	"strings"
	"testing"
)

// TestChatAgentRuntime pins what the dashboard is told about a quiet agent: a
// provider without a key here, and the last failure until a reply clears it.
func TestChatAgentRuntime(t *testing.T) {
	m := &Manager{ai: &aiResponder{openAIKey: "sk-test"}}

	runtime := m.ChatAgentRuntime("agent_1", "anthropic")
	if runtime.ProviderConfigured || !strings.Contains(runtime.LastError, "ANTHROPIC_API_KEY") {
		t.Errorf("anthropic without a key = %+v, want unconfigured naming ANTHROPIC_API_KEY", runtime)
	}

	if runtime := m.ChatAgentRuntime("agent_1", "openai"); !runtime.ProviderConfigured || runtime.LastError != "" {
		t.Errorf("openai with a key and no failures = %+v, want configured and clean", runtime)
	}

	m.noteChatFailure("agent_1", "The model request failed: "+strings.Repeat("x", 1000))
	runtime = m.ChatAgentRuntime("agent_1", "openai")
	if runtime.LastErrorAt == nil || !strings.HasPrefix(runtime.LastError, "The model request failed") || len(runtime.LastError) > chatFailureMaxLen+len("…") {
		t.Errorf("after a failure = %+v, want the truncated failure with a time", runtime)
	}

	m.chatFailures.Delete("agent_1")
	if runtime := m.ChatAgentRuntime("agent_1", "openai"); runtime.LastError != "" {
		t.Errorf("after a successful reply = %+v, want the failure cleared", runtime)
	}

	var disabled *Manager = &Manager{}
	if runtime := disabled.ChatAgentRuntime("agent_1", "openai"); runtime.ProviderConfigured {
		t.Error("a server with no AI keys at all reported the provider configured")
	}
}
