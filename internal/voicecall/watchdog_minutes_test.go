package voicecall

import (
	"testing"
	"time"
)

func TestWatchdogWithMinutesLeft(t *testing.T) {
	base := watchdogConfig{maxDuration: 20 * time.Minute}

	if got := base.withMinutesLeft(0); got.maxDuration != 20*time.Minute || got.minutesCapped {
		t.Fatalf("no limit: got %+v", got)
	}
	if got := base.withMinutesLeft(30 * time.Minute); got.maxDuration != 20*time.Minute || got.minutesCapped {
		t.Fatalf("more minutes than the cap: got %+v", got)
	}
	got := base.withMinutesLeft(90 * time.Second)
	if got.maxDuration != 90*time.Second || !got.minutesCapped {
		t.Fatalf("fewer minutes than the cap: got %+v", got)
	}
	if got.limitReason() != "voice minutes used up" {
		t.Fatalf("limitReason = %q", got.limitReason())
	}
	if base.limitReason() != "maximum call length reached" {
		t.Fatalf("base limitReason = %q", base.limitReason())
	}
	// A server with the call-length cap switched off still stops at the minutes.
	if got := (watchdogConfig{}).withMinutesLeft(time.Minute); got.maxDuration != time.Minute || !got.minutesCapped {
		t.Fatalf("cap off: got %+v", got)
	}
}

// The watchdog winds the call down once the account's minutes run out.
func TestWatchdogStopsAtMinutesLeft(t *testing.T) {
	now := time.Unix(1_000, 0)
	w := newCallWatchdog(watchdogConfig{maxDuration: 20 * time.Minute}.withMinutesLeft(45 * time.Second))
	w.now = func() time.Time { return now }
	w.startedAt, w.last = now, now

	now = now.Add(44 * time.Second)
	if a := w.check(true); a != watchdogNone {
		t.Fatalf("at 44s: %v", a)
	}
	now = now.Add(time.Second)
	if a := w.check(true); a != watchdogTimeLimit {
		t.Fatalf("at 45s: %v, want time limit", a)
	}
}
