package voicecall

import (
	"math"
	"testing"
	"time"

	"github.com/purpshell/meowcaller"
)

// voicedFrame is one frame of audio loud enough to read as speech.
func voicedFrame() []float32 {
	frame := make([]float32, meowcaller.FrameSamples)
	for i := range frame {
		frame[i] = float32(0.2 * math.Sin(float64(i)/8))
	}
	return frame
}

func silentFrame() []float32 { return make([]float32, meowcaller.FrameSamples) }

// feed plays frames through the detector at the call's own frame rate, without
// waiting for real time, and reports how far in the turn ended.
func feed(d *turnDetector, start time.Time, frames [][]float32) (time.Duration, bool) {
	now := start
	for _, frame := range frames {
		now = now.Add(frameDuration)
		if d.Observe(frame, now) {
			return now.Sub(start), true
		}
	}
	return now.Sub(start), false
}

func repeatFrames(frame []float32, count int) [][]float32 {
	frames := make([][]float32, count)
	for i := range frames {
		frames[i] = frame
	}
	return frames
}

func TestTurnDetectorEndsTurnAfterEndpointSilence(t *testing.T) {
	// 180 ms of speech to arm, 250 ms of silence to end: three voiced frames and
	// then five silent ones, of which the fifth crosses the window.
	d := newTurnDetector(defaultOpenAIVADThreshold, 180, 250, 30000)
	start := time.Now()
	frames := append(repeatFrames(voicedFrame(), 10), repeatFrames(silentFrame(), 20)...)
	at, ended := feed(d, start, frames)
	if !ended {
		t.Fatal("turn never ended")
	}
	speech := 10 * frameDuration
	if silence := at - speech; silence < 250*time.Millisecond || silence > 250*time.Millisecond+frameDuration {
		t.Errorf("turn ended %v after the last voiced frame, want ~250ms", silence)
	}
}

// A single noisy frame is a click, not a turn, and must not leave the detector
// waiting to close a turn that never started.
func TestTurnDetectorIgnoresNoiseShorterThanMinSpeech(t *testing.T) {
	d := newTurnDetector(defaultOpenAIVADThreshold, 180, 250, 30000)
	start := time.Now()
	frames := append([][]float32{voicedFrame()}, repeatFrames(silentFrame(), 40)...)
	if _, ended := feed(d, start, frames); ended {
		t.Error("a single voiced frame started a turn")
	}
	if d.Speaking() {
		t.Error("detector is speaking after one voiced frame")
	}
}

// Once the silence filler gives up, no frames arrive at all. The caller is
// still quiet, and their turn still has to be closed out.
func TestTurnDetectorIdleEndsTurnWhenFramesStop(t *testing.T) {
	d := newTurnDetector(defaultOpenAIVADThreshold, 180, 250, 30000)
	start := time.Now()
	if _, ended := feed(d, start, repeatFrames(voicedFrame(), 10)); ended {
		t.Fatal("turn ended while the caller was still speaking")
	}
	lastFrame := start.Add(10 * frameDuration)
	if d.Idle(lastFrame.Add(200 * time.Millisecond)) {
		t.Error("turn ended before the endpoint window elapsed")
	}
	if !d.Idle(lastFrame.Add(300 * time.Millisecond)) {
		t.Error("turn did not end once the endpoint window elapsed")
	}
}

// A caller who never stops talking still has to be transcribed at some point.
func TestTurnDetectorCutsAnEndlessUtterance(t *testing.T) {
	d := newTurnDetector(defaultOpenAIVADThreshold, 180, 250, 600)
	at, ended := feed(d, time.Now(), repeatFrames(voicedFrame(), 60))
	if !ended {
		t.Fatal("an endless utterance was never cut")
	}
	if at > time.Second {
		t.Errorf("utterance cut after %v, want ~600ms", at)
	}
}

func TestTurnDetectorResetArmsTheNextTurn(t *testing.T) {
	d := newTurnDetector(defaultOpenAIVADThreshold, 180, 250, 30000)
	start := time.Now()
	if _, ended := feed(d, start, append(repeatFrames(voicedFrame(), 10), repeatFrames(silentFrame(), 20)...)); !ended {
		t.Fatal("first turn never ended")
	}
	d.Reset()
	if d.Speaking() {
		t.Error("detector still speaking after reset")
	}
	if d.Idle(start.Add(time.Hour)) {
		t.Error("a reset detector ended a turn that never started")
	}
}
