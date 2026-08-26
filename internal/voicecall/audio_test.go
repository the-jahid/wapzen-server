package voicecall

import (
	"sync"
	"testing"
	"time"
)

type blockingAudioSink struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	closed  chan struct{}
}

func (s *blockingAudioSink) WriteFrame([]float32) error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return nil
}

func (s *blockingAudioSink) Close() error {
	close(s.closed)
	return nil
}

func TestAsyncAudioSinkDoesNotBlockRealtimeWriter(t *testing.T) {
	underlying := &blockingAudioSink{
		started: make(chan struct{}),
		release: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	sink := newAsyncAudioSink(underlying, 1)

	if err := sink.WriteFrame([]float32{1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-underlying.started:
	case <-time.After(time.Second):
		t.Fatal("observer did not receive the first frame")
	}

	returned := make(chan struct{})
	go func() {
		_ = sink.WriteFrame([]float32{2})
		_ = sink.WriteFrame([]float32{3})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("slow observer blocked the realtime writer")
	}

	close(underlying.release)
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-underlying.closed:
	default:
		t.Fatal("underlying observer was not closed")
	}
}
