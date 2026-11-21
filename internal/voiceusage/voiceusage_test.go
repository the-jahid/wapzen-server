package voiceusage

import (
	"testing"
	"time"
)

func TestNewUsage(t *testing.T) {
	cases := []struct {
		name          string
		used, limit   int
		wantRemaining int
		wantExhausted bool
	}{
		{"fresh account", 0, DefaultLimitSeconds, 600, false},
		{"part used", 125, 600, 475, false},
		{"exactly used up", 600, 600, 0, true},
		{"overshot by a goodbye", 607, 600, 0, true},
		{"bought more", 607, 3600, 2993, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUsage(tc.used, tc.limit)
			if u.RemainingSeconds != tc.wantRemaining || u.Exhausted != tc.wantExhausted {
				t.Fatalf("newUsage(%d, %d) = %+v, want remaining %d exhausted %t", tc.used, tc.limit, u, tc.wantRemaining, tc.wantExhausted)
			}
			if u.Remaining() != time.Duration(tc.wantRemaining)*time.Second {
				t.Fatalf("Remaining() = %s", u.Remaining())
			}
		})
	}
}
