package meowcaller

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
)

func TestCallAcceptedCallback(t *testing.T) {
	call := &Call{}
	called := 0
	call.OnAccepted(func() { called++ })
	call.setAccepted()
	call.setAccepted()
	if called != 1 {
		t.Fatalf("accepted callback count = %d, want 1", called)
	}
}

func TestCallAcceptedCallbackRegisteredLate(t *testing.T) {
	call := &Call{}
	call.setAccepted()
	called := 0
	call.OnAccepted(func() { called++ })
	if called != 1 {
		t.Fatalf("late accepted callback count = %d, want 1", called)
	}
}

func TestEngineOutboundAcceptActivatesCall(t *testing.T) {
	call := &Call{phase: CallPhaseConnecting}
	client := &Client{log: zerolog.Nop()}
	eng := &engine{
		c: client,
		calls: map[string]*engineCall{
			"call-id": {call: call, direction: CallDirectionOutgoing},
		},
	}
	accepted := 0
	call.OnAccepted(func() { accepted++ })

	eng.onAccept("call-id")
	eng.onAccept("call-id")

	if accepted != 1 {
		t.Fatalf("accepted callback count = %d, want 1", accepted)
	}
	if got := call.State(); got != CallPhaseActive {
		t.Fatalf("call phase = %d, want active", got)
	}
}

// regSession builds an outgoing session for the registry tests (reuses the JID
// helpers from session_test.go in this package).
func regSession(id string) *CallSession {
	return NewOutgoingSession(id, peerJID(), creatorJID())
}

func isCancelled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// TestInsertTransitionRemove pins the registry bookkeeping contract.
func TestInsertTransitionRemove(t *testing.T) {
	reg := NewCallRegistry()
	if !reg.Insert(regSession("CID")) {
		t.Fatal("first insert should succeed")
	}
	if reg.Insert(regSession("CID")) {
		t.Error("duplicate insert should fail")
	}
	if ph, ok := reg.Phase("CID"); !ok || ph != CallPhaseIdle {
		t.Errorf("phase = (%d, %v), want (Idle, true)", ph, ok)
	}
	if !reg.Transition("CID", CallPhaseCalling) {
		t.Error("legal transition rejected")
	}
	if ph, _ := reg.Phase("CID"); ph != CallPhaseCalling {
		t.Errorf("phase = %d, want Calling", ph)
	}
	if reg.Transition("UNKNOWN", CallPhaseCalling) {
		t.Error("transition on unknown call should fail")
	}
	if !reg.Remove("CID") {
		t.Error("remove of existing call should return true")
	}
	if reg.Remove("CID") {
		t.Error("remove of absent call should return false")
	}
	if reg.ActiveCount() != 0 {
		t.Errorf("active count = %d, want 0", reg.ActiveCount())
	}
}

// TestRemoveCancelsMediaTask confirms Remove cancels the call's media task.
func TestRemoveCancelsMediaTask(t *testing.T) {
	reg := NewCallRegistry()
	reg.Insert(regSession("A"))
	ctx, cancel := context.WithCancel(context.Background())
	reg.SetMediaTask("A", cancel)
	if !reg.Remove("A") {
		t.Fatal("remove failed")
	}
	if !isCancelled(ctx) {
		t.Error("removed call's media task must be cancelled")
	}
}

// TestAbortAllCancelsMediaTasks confirms AbortAll cancels every task and empties the registry.
func TestAbortAllCancelsMediaTasks(t *testing.T) {
	reg := NewCallRegistry()
	reg.Insert(regSession("A"))
	reg.Insert(regSession("B"))
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	reg.SetMediaTask("A", cancelA)
	reg.SetMediaTask("B", cancelB)
	if reg.AbortAll() != 2 {
		t.Errorf("AbortAll returned %d, want 2", reg.AbortAll())
	}
	if !isCancelled(ctxA) || !isCancelled(ctxB) {
		t.Error("AbortAll must cancel every media task")
	}
	if reg.ActiveCount() != 0 {
		t.Error("AbortAll must empty the registry")
	}
}

// TestReplaceCancelsOldMediaTask confirms a replacing SetMediaTask cancels the prior handle.
func TestReplaceCancelsOldMediaTask(t *testing.T) {
	reg := NewCallRegistry()
	reg.Insert(regSession("A"))
	oldCtx, oldCancel := context.WithCancel(context.Background())
	reg.SetMediaTask("A", oldCancel)
	newCtx, newCancel := context.WithCancel(context.Background())
	reg.SetMediaTask("A", newCancel)
	if !isCancelled(oldCtx) {
		t.Error("replaced media task must be cancelled")
	}
	if isCancelled(newCtx) {
		t.Error("replacement task must stay live")
	}
	reg.Remove("A")
	if !isCancelled(newCtx) {
		t.Error("replacement cancelled on remove")
	}
}

// TestSetMediaTaskOnUnknownCallCancels confirms an orphan handle is cancelled immediately.
func TestSetMediaTaskOnUnknownCallCancels(t *testing.T) {
	reg := NewCallRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	reg.SetMediaTask("GONE", cancel) // never inserted
	if !isCancelled(ctx) {
		t.Error("orphan media task must be cancelled immediately")
	}
}
