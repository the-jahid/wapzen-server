package voicecall

import (
	"sync"
	"testing"
	"time"

	"github.com/purpshell/meowcaller"
)

type countingSink struct {
	mu     sync.Mutex
	real   int
	silent int
	closed int
}

func (c *countingSink) WriteFrame(frame []float32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sample := range frame {
		if sample != 0 {
			c.real++
			return nil
		}
	}
	c.silent++
	return nil
}

func (c *countingSink) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	return nil
}

func (c *countingSink) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.real, c.silent
}

func TestSilenceFilledSinkFillsGapsAndStops(t *testing.T) {
	counter := &countingSink{}
	// Six frames' worth of fill, so the cap is reached well inside the test.
	sink := newSilenceFilledSink(counter, 6*frameDuration)
	defer sink.Close()

	if real, silent := counter.counts(); real != 0 || silent != 0 {
		t.Fatalf("filled before any real audio: real=%d silent=%d", real, silent)
	}

	voiced := make([]float32, meowcaller.FrameSamples)
	for i := range voiced {
		voiced[i] = 0.5
	}
	if err := sink.WriteFrame(voiced); err != nil {
		t.Fatal(err)
	}

	// The gap the caller's DTX would leave: no frames at all.
	time.Sleep(6*frameDuration + 4*frameDuration)

	real, silent := counter.counts()
	if real != 1 {
		t.Fatalf("real frames = %d, want 1", real)
	}
	if silent < 4 || silent > 6 {
		t.Fatalf("silent frames = %d, want the 6-frame fill window (allowing tick jitter)", silent)
	}

	// Speech resuming restarts the budget.
	if err := sink.WriteFrame(voiced); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * frameDuration)
	if _, refilled := counter.counts(); refilled <= silent {
		t.Fatalf("fill did not resume after speech: %d then %d", silent, refilled)
	}
}

func TestSilenceFilledSinkDisabled(t *testing.T) {
	counter := &countingSink{}
	if got := newSilenceFilledSink(counter, 0); got != meowcaller.AudioSink(counter) {
		t.Fatal("a zero window should hand back the sink untouched")
	}
}

func TestSilenceFilledSinkCloseIsIdempotent(t *testing.T) {
	counter := &countingSink{}
	sink := newSilenceFilledSink(counter, frameDuration)
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.WriteFrame(make([]float32, meowcaller.FrameSamples)); err != nil {
		t.Fatal(err)
	}
	counter.mu.Lock()
	defer counter.mu.Unlock()
	if counter.silent != 0 {
		t.Fatalf("wrote %d frames after close", counter.silent)
	}
}
