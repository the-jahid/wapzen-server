package whatsapplogin

import (
	"context"
	"sync"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

type recordingPresenceSender struct {
	mu     sync.Mutex
	states []types.ChatPresence
}

func (s *recordingPresenceSender) SendChatPresence(
	_ context.Context,
	_ types.JID,
	state types.ChatPresence,
	_ types.ChatPresenceMedia,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = append(s.states, state)
	return nil
}

func (s *recordingPresenceSender) snapshot() []types.ChatPresence {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]types.ChatPresence(nil), s.states...)
}

func TestChatTypingStartsAndStopsPresence(t *testing.T) {
	sender := &recordingPresenceSender{}
	chat := types.NewJID("15551234567", types.DefaultUserServer)

	stop := startChatTyping(context.Background(), sender, chat, "phone_123")
	stop()
	stop() // stopping is safe from both the explicit path and its deferred call

	states := sender.snapshot()
	if len(states) != 2 {
		t.Fatalf("presence states = %v, want composing then paused", states)
	}
	if states[0] != types.ChatPresenceComposing || states[1] != types.ChatPresencePaused {
		t.Fatalf("presence states = %v, want [%s %s]", states, types.ChatPresenceComposing, types.ChatPresencePaused)
	}
}
