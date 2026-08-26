package voicecall

import "testing"

func TestClampSpeedKeepsTheAPIRange(t *testing.T) {
	cases := []struct {
		speed float64
		want  float64
	}{
		{1, 1},
		{0.8, 0.8},
		{0, defaultSpeed},
		{-1, defaultSpeed},
		{0.1, speedMin},
		{9, speedMax},
	}
	for _, tc := range cases {
		if got := clampSpeed(tc.speed); got != tc.want {
			t.Errorf("clampSpeed(%v) = %v, want %v", tc.speed, got, tc.want)
		}
	}
}

func TestClampVolume(t *testing.T) {
	cases := []struct {
		volume float64
		want   float64
	}{
		{1, 1},
		{0, 0},
		{-1, defaultVolume},
		{9, volumeMax},
	}
	for _, tc := range cases {
		if got := clampVolume(tc.volume); got != tc.want {
			t.Errorf("clampVolume(%v) = %v, want %v", tc.volume, got, tc.want)
		}
	}
}

// TestAgentSpeaksFirst pins which begin-message modes carry a welcome delay: an
// outbound call defers that delay until the callee answers, so a mode that never
// opens with a message must not hold the agent's audio back at all.
func TestAgentSpeaksFirst(t *testing.T) {
	cases := []struct {
		mode string
		want bool
	}{
		{"agent_speaks_first", true},
		{"agent_speaks_first_with_model_generated_message", true},
		{" agent_speaks_first ", true},
		{"agent_waits_for_user", false},
		{"", false},
		{"something_else", false},
	}
	for _, tc := range cases {
		if got := agentSpeaksFirst(tc.mode); got != tc.want {
			t.Errorf("agentSpeaksFirst(%q) = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestApplyGainScalesAndClamps(t *testing.T) {
	samples := []float32{0.5, -0.5, 0.9}
	got := applyGain(samples, 2)
	want := []float32{1, -1, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("applyGain[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	unchanged := []float32{0.5, -0.25}
	if out := applyGain(unchanged, defaultVolume); out[0] != 0.5 || out[1] != -0.25 {
		t.Errorf("applyGain with neutral volume changed the samples: %v", out)
	}
}
