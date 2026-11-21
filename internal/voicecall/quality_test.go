package voicecall

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"whatsapp-ai-caller-server/internal/models"
)

func TestComposeCallInstructionsPutsRulesLast(t *testing.T) {
	got := composeCallInstructions("OWNER PROMPT", "KNOWLEDGE", "TOOLS")
	owner, knowledge, tools, rules := strings.Index(got, "OWNER PROMPT"), strings.Index(got, "KNOWLEDGE"), strings.Index(got, "TOOLS"), strings.Index(got, "Rules for this phone call")
	if !(owner == 0 && owner < knowledge && knowledge < tools && tools < rules) {
		t.Fatalf("sections out of order: owner=%d knowledge=%d tools=%d rules=%d", owner, knowledge, tools, rules)
	}
	if !strings.HasSuffix(got, strings.TrimSpace(callRules)) {
		t.Fatal("call rules are not the last section")
	}
	// Empty sections leave no gaps.
	if strings.Contains(composeCallInstructions("P", "", ""), "\n\n\n") {
		t.Fatal("empty sections left blank lines")
	}
}

func TestRenderInlineKnowledge(t *testing.T) {
	if renderInlineKnowledge(nil) != "" {
		t.Fatal("no documents should render nothing")
	}
	got := renderInlineKnowledge([]models.KnowledgeDocument{
		{KnowledgeBase: "Menu", Title: "prices.txt", Content: "Burger 250 taka"},
		{KnowledgeBase: "Menu", Title: "empty", Content: "   "},
	})
	if !strings.Contains(got, "### Menu — prices.txt\nBurger 250 taka") {
		t.Fatalf("document not rendered: %q", got)
	}
	if strings.Contains(got, "empty") {
		t.Fatalf("empty document rendered: %q", got)
	}
	if !strings.Contains(got, "<business_knowledge>") || !strings.Contains(got, "</business_knowledge>") {
		t.Fatalf("knowledge not fenced: %q", got)
	}
}

func TestTruncateUTF8NeverSplitsACharacter(t *testing.T) {
	s := strings.Repeat("আ", 10) // 3 bytes each
	for limit := 0; limit <= len(s); limit++ {
		got := truncateUTF8(s, limit)
		if len(got) > limit || !strings.HasPrefix(s, got) || len(got)%3 != 0 {
			t.Fatalf("limit %d: got %d bytes", limit, len(got))
		}
	}
}

func TestCallWatchdogAsksThenEnds(t *testing.T) {
	now := time.Unix(0, 0)
	w := newCallWatchdog(watchdogConfig{idlePrompt: 20 * time.Second, idleHangup: 20 * time.Second, maxDuration: 10 * time.Minute})
	w.now = func() time.Time { return now }
	w.startedAt, w.last = now, now

	now = now.Add(19 * time.Second)
	if got := w.check(false); got != watchdogNone {
		t.Fatalf("19s: %v", got)
	}
	// The agent talking is never silence.
	now = now.Add(30 * time.Second)
	if got := w.check(true); got != watchdogNone {
		t.Fatalf("busy: %v", got)
	}
	now = now.Add(20 * time.Second)
	if got := w.check(false); got != watchdogAskStillThere {
		t.Fatalf("20s idle: %v", got)
	}
	w.markNudged()
	// The caller answering re-arms the question.
	now = now.Add(5 * time.Second)
	w.callerSpoke()
	now = now.Add(20 * time.Second)
	if got := w.check(false); got != watchdogAskStillThere {
		t.Fatalf("after reply: %v", got)
	}
	w.markNudged()
	now = now.Add(20 * time.Second)
	if got := w.check(false); got != watchdogIdleGoodbye {
		t.Fatalf("no answer to the question: %v", got)
	}
	if got := w.check(false); got != watchdogNone {
		t.Fatalf("ended twice: %v", got)
	}
}

func TestCallWatchdogTimeLimit(t *testing.T) {
	now := time.Unix(0, 0)
	w := newCallWatchdog(watchdogConfig{maxDuration: time.Minute})
	w.now = func() time.Time { return now }
	w.startedAt, w.last = now, now
	now = now.Add(time.Minute)
	if got := w.check(true); got != watchdogTimeLimit {
		t.Fatalf("got %v", got)
	}
}

func TestTranscriptKeepsConversationOrder(t *testing.T) {
	b := &openAIRealtimeVoiceBridge{}
	b.reserveTranscriptSlot("user", "u1")
	// The reply's transcript lands before the caller's turn is transcribed.
	b.fillTranscript("assistant", "a1", "Our hours are nine to five.")
	b.fillTranscript("user", "u1", "When are you open?")
	b.reserveTranscriptSlot("user", "u2") // never transcribed
	got := b.Transcript()
	if len(got) != 2 || got[0].Role != "user" || got[0].Content != "When are you open?" || got[1].Role != "assistant" {
		t.Fatalf("transcript = %#v", got)
	}
}

// realtimeTestServer runs a fake Realtime endpoint that hands each connection
// to handle.
func realtimeTestServer(t *testing.T, handle func(ctx context.Context, conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		handle(ctx, conn)
	}))
}

func testRealtimeConfig(server *httptest.Server) openAIRealtimeConfig {
	return openAIRealtimeConfig{
		APIKey:        "test-key",
		RealtimeURL:   "ws" + strings.TrimPrefix(server.URL, "http"),
		RealtimeModel: "gpt-realtime-2.1-mini",
		Voice:         "coral",
		Speed:         1,
		Volume:        1,
	}
}

func TestBridgeFailsWhenSessionConfigIsRejected(t *testing.T) {
	server := realtimeTestServer(t, func(ctx context.Context, conn *websocket.Conn) {
		_, _, _ = conn.Read(ctx)
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"error","error":{"message":"Unknown parameter"}}`))
		_, _, _ = conn.Read(ctx)
	})
	defer server.Close()

	failed := make(chan error, 1)
	bridge := newOpenAIRealtimeVoiceBridgeWithHooks(testRealtimeConfig(server), nil, nil, bridgeHooks{
		onFailure: func(err error) { failed <- err },
	})
	defer bridge.Close()

	select {
	case <-bridge.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("bridge never settled")
	}
	if bridge.Err() == nil {
		t.Fatal("a rejected session.update must not count as ready")
	}
	select {
	case err := <-failed:
		if !strings.Contains(err.Error(), "Unknown parameter") {
			t.Fatalf("failure = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner was not told the session failed")
	}
}

func TestBridgeCloseIsNotAFailure(t *testing.T) {
	server := realtimeTestServer(t, func(ctx context.Context, conn *websocket.Conn) {
		_, _, _ = conn.Read(ctx)
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"session.updated"}`))
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	})
	defer server.Close()

	var mu sync.Mutex
	var failures []error
	bridge := newOpenAIRealtimeVoiceBridgeWithHooks(testRealtimeConfig(server), nil, nil, bridgeHooks{
		onFailure: func(err error) { mu.Lock(); failures = append(failures, err); mu.Unlock() },
	})
	<-bridge.Ready()
	if err := bridge.Err(); err != nil {
		t.Fatalf("ready error = %v", err)
	}
	_ = bridge.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(failures) != 0 {
		t.Fatalf("closing for a normal hang-up reported failures: %v", failures)
	}
}

// stubTools answers every call with the same text after a delay.
type stubTools struct{ delay time.Duration }

func (stubTools) Definitions() []toolDefinition { return []toolDefinition{{Name: "lookup"}} }
func (s stubTools) Run(ctx context.Context, _, _ string) string {
	time.Sleep(s.delay)
	return "result"
}

func TestToolFollowUpSkippedWhenCallerSpokeMeanwhile(t *testing.T) {
	events := make(chan map[string]any, 16)
	server := realtimeTestServer(t, func(ctx context.Context, conn *websocket.Conn) {
		_, _, _ = conn.Read(ctx) // session.update
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"session.updated"}`))
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.done","response":{"status":"completed","output":[{"type":"function_call","name":"lookup","call_id":"c1","arguments":"{}"}]}}`))
		// The caller talks while the tool runs.
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"input_audio_buffer.speech_started"}`))
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var event map[string]any
			if json.Unmarshal(data, &event) == nil {
				events <- event
			}
		}
	})
	defer server.Close()

	cfg := testRealtimeConfig(server)
	cfg.Tools = stubTools{delay: 200 * time.Millisecond}
	bridge := newOpenAIRealtimeVoiceBridge(cfg, nil, nil)
	defer bridge.Close()

	sawOutput := false
	deadline := time.After(1500 * time.Millisecond)
	for {
		select {
		case event := <-events:
			switch event["type"] {
			case "conversation.item.create":
				sawOutput = true
			case "response.create":
				t.Fatal("follow-up response requested although the caller had spoken since the tool started")
			}
		case <-deadline:
			if !sawOutput {
				t.Fatal("tool result was never delivered")
			}
			return
		}
	}
}

func TestNormalizeNoiseReduction(t *testing.T) {
	for in, want := range map[string]string{"near_field": "near_field", "FAR_FIELD": "far_field", "off": "", "": "", "bogus": ""} {
		if got := normalizeNoiseReduction(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

