package voicecall

import (
	"encoding/binary"
	"io"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
)

// float32ToPCM16LE converts normalized [-1,1] float samples to little-endian
// signed 16-bit PCM, clamping out-of-range values.
func float32ToPCM16LE(samples []float32) []byte {
	out := make([]byte, len(samples)*2)
	for i, sample := range samples {
		v := sample * 32768.0
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(v)))
	}
	return out
}

// pcm16LEToFloat32 converts little-endian signed 16-bit PCM back to normalized
// [-1,1] float samples.
func pcm16LEToFloat32(b []byte) []float32 {
	n := len(b) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = float32(int16(binary.LittleEndian.Uint16(b[i*2:]))) / 32768.0
	}
	return out
}

// applyGain scales samples by volume, clamping to normalized PCM bounds.
func applyGain(samples []float32, volume float64) []float32 {
	if volume == defaultVolume || len(samples) == 0 {
		return samples
	}
	gain := float32(clampVolume(volume))
	for i, sample := range samples {
		v := sample * gain
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		samples[i] = v
	}
	return samples
}

// sampleRateConverter is a stateful linear resampler that carries the last
// sample across Process calls so streamed chunks join without clicks. Reset it
// whenever a new, discontinuous stream begins.
type sampleRateConverter struct {
	inRate   int
	outRate  int
	pos      float64
	last     float32
	havePrev bool
}

func newSampleRateConverter(inRate, outRate int) *sampleRateConverter {
	return &sampleRateConverter{inRate: inRate, outRate: outRate}
}

func (c *sampleRateConverter) Reset() {
	inRate := c.inRate
	outRate := c.outRate
	*c = sampleRateConverter{inRate: inRate, outRate: outRate}
}

func (c *sampleRateConverter) Process(samples []float32) []float32 {
	if len(samples) == 0 {
		return nil
	}
	if c.inRate == c.outRate {
		out := make([]float32, len(samples))
		copy(out, samples)
		return out
	}

	step := float64(c.inRate) / float64(c.outRate)
	src := samples
	base := 0.0
	if c.havePrev {
		src = make([]float32, 0, len(samples)+1)
		src = append(src, c.last)
		src = append(src, samples...)
		base = 1.0
	}
	if c.pos+base < 0 {
		c.pos = -base
	}

	out := make([]float32, 0, int(float64(len(samples))*float64(c.outRate)/float64(c.inRate))+2)
	for {
		idx := c.pos + base
		i := int(idx)
		if i+1 >= len(src) {
			break
		}
		frac := idx - float64(i)
		out = append(out, src[i]*(1-float32(frac))+src[i+1]*float32(frac))
		c.pos += step
	}
	c.pos -= float64(len(samples))
	c.last = samples[len(samples)-1]
	c.havePrev = true
	return out
}

// streamingAudioSource is an AudioSource that plays samples appended to it over
// time, returning silence frames while it waits for more and io.EOF once closed
// and drained. It lets the realtime bridge start playback before every audio
// delta has arrived.
type streamingAudioSource struct {
	mu            sync.Mutex
	samples       []float32
	closed        bool
	playedSamples int
}

func newStreamingAudioSource() *streamingAudioSource {
	return &streamingAudioSource{}
}

// Append copies samples into the buffer. It reports false once the source is
// closed so the caller knows to start a fresh source for the next response.
func (s *streamingAudioSource) Append(samples []float32) bool {
	if len(samples) == 0 {
		return true
	}
	cp := make([]float32, len(samples))
	copy(cp, samples)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.samples = append(s.samples, cp...)
	return true
}

func (s *streamingAudioSource) ReadFrame() ([]float32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.samples) >= meowcaller.FrameSamples {
		frame := make([]float32, meowcaller.FrameSamples)
		copy(frame, s.samples[:meowcaller.FrameSamples])
		s.samples = s.samples[meowcaller.FrameSamples:]
		s.playedSamples += len(frame)
		return frame, nil
	}
	if s.closed {
		if len(s.samples) == 0 {
			return nil, io.EOF
		}
		frame := make([]float32, meowcaller.FrameSamples)
		n := copy(frame, s.samples)
		s.samples = nil
		s.playedSamples += n
		return frame, nil
	}
	return make([]float32, meowcaller.FrameSamples), nil
}

func (s *streamingAudioSource) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *streamingAudioSource) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *streamingAudioSource) BufferedSamples() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.samples)
}

// PlayedMS reports how much audio has been read out so far, used to truncate the
// model's turn on barge-in at the exact point the caller stopped hearing it.
func (s *streamingAudioSource) PlayedMS() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playedSamples * 1000 / audioSampleRate
}

// multiSink fans one incoming audio frame out to several sinks (for example an
// optional WAV recorder alongside the realtime bridge).
type multiSink []meowcaller.AudioSink

func (m multiSink) WriteFrame(frame []float32) error {
	var firstErr error
	for _, sink := range m {
		if err := sink.WriteFrame(frame); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m multiSink) Close() error {
	var firstErr error
	for _, sink := range m {
		if err := sink.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// asyncAudioSink keeps optional observers such as WAV recording out of the
// live receive callback. A slow disk must never delay VAD or streaming STT.
// When the observer falls behind, the oldest debug frame is discarded; the
// conversation path itself remains lossless because it is not wrapped here.
type asyncAudioSink struct {
	sink      meowcaller.AudioSink
	frames    chan []float32
	done      chan struct{}
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
}

func newAsyncAudioSink(sink meowcaller.AudioSink, capacity int) *asyncAudioSink {
	if capacity < 1 {
		capacity = 1
	}
	a := &asyncAudioSink{
		sink:   sink,
		frames: make(chan []float32, capacity),
		done:   make(chan struct{}),
	}
	go a.run()
	return a
}

func (a *asyncAudioSink) WriteFrame(frame []float32) error {
	cp := append([]float32(nil), frame...)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	select {
	case a.frames <- cp:
		return nil
	default:
	}
	select {
	case <-a.frames:
	default:
	}
	select {
	case a.frames <- cp:
	default:
	}
	return nil
}

func (a *asyncAudioSink) Close() error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		close(a.frames)
		a.mu.Unlock()
	})
	<-a.done
	return nil
}

func (a *asyncAudioSink) run() {
	defer close(a.done)
	for frame := range a.frames {
		_ = a.sink.WriteFrame(frame)
	}
	_ = a.sink.Close()
}

// frameDuration is how much audio one 16 kHz call frame carries.
const frameDuration = time.Duration(meowcaller.FrameSamples) * time.Second / audioSampleRate

// silenceFilledSink keeps audio arriving at the voice bridge at the call's own
// frame rate. WhatsApp stops sending RTP while the caller is silent (DTX), so
// the caller's pauses never reach the transcriber as audio at all: the
// trailing-silence timer that decides their turn is over then advances only as
// the occasional comfort frame trickles in, and the turn hangs for seconds
// after they stopped talking. Filling the gaps with real silence makes a pause
// of half a second look like half a second.
//
// Filling stops once a gap has run past fillFor, because by then the caller is
// simply not talking and there is no turn waiting to be closed out.
type silenceFilledSink struct {
	sink    meowcaller.AudioSink
	fillFor time.Duration

	mu       sync.Mutex
	lastSent time.Time
	// started stays false until real audio arrives, so a bridge is never fed
	// silence before the call has media.
	started bool
	filled  time.Duration
	closed  bool

	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// newSilenceFilledSink wraps sink so gaps in the incoming audio are filled for
// up to fillFor. A fillFor of zero or less returns sink untouched.
func newSilenceFilledSink(sink meowcaller.AudioSink, fillFor time.Duration) meowcaller.AudioSink {
	if fillFor <= 0 {
		return sink
	}
	s := &silenceFilledSink{
		sink:    sink,
		fillFor: fillFor,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *silenceFilledSink) WriteFrame(frame []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.lastSent = time.Now()
	s.started = true
	s.filled = 0
	return s.sink.WriteFrame(frame)
}

func (s *silenceFilledSink) run() {
	defer close(s.done)
	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()
	silence := make([]float32, meowcaller.FrameSamples)
	for {
		select {
		case <-s.stop:
			return
		case now := <-ticker.C:
			s.mu.Lock()
			if !s.closed && s.started && s.filled < s.fillFor && now.Sub(s.lastSent) >= frameDuration {
				s.lastSent = now
				s.filled += frameDuration
				// Written under the lock so a real frame arriving now queues behind
				// this one rather than racing it out of order.
				_ = s.sink.WriteFrame(silence)
			}
			s.mu.Unlock()
		}
	}
}

func (s *silenceFilledSink) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.stop)
		<-s.done
	})
	return s.sink.Close()
}

// turnDetector decides when the caller has stopped talking, from the same
// frames that go to the transcriber.
//
// It exists because asking the transcriber to make that call is expensive:
// Scribe's own VAD will not take a trailing-silence window below 300 ms and
// then adds roughly 600 ms of finalization on top of it, so the caller waits
// close to a second after their last word before the agent has even been asked
// a question. A segment committed from here is finalized in ~400 ms flat, and
// the silence window in front of it is ours to choose.
//
// Detection is plain frame energy, which is all that is needed: the frames
// arriving here are already gap-filled with real silence, so a pause looks like
// a pause whether WhatsApp sent packets through it or not.
type turnDetector struct {
	threshold       float64
	minSpeech       time.Duration
	endpointSilence time.Duration
	maxUtterance    time.Duration

	speaking     bool
	voicedFor    time.Duration
	lastVoicedAt time.Time
	startedAt    time.Time
}

func newTurnDetector(threshold float64, minSpeechMS, endpointSilenceMS, maxUtteranceMS int) *turnDetector {
	if threshold <= 0 {
		threshold = defaultOpenAIVADThreshold
	}
	milliseconds := func(value, fallback int) time.Duration {
		if value <= 0 {
			value = fallback
		}
		return time.Duration(value) * time.Millisecond
	}
	return &turnDetector{
		threshold:       threshold,
		minSpeech:       milliseconds(minSpeechMS, defaultOpenAIMinSpeechMS),
		endpointSilence: milliseconds(endpointSilenceMS, defaultOpenAISilenceMS),
		maxUtterance:    milliseconds(maxUtteranceMS, defaultOpenAIMaxUtteranceMS),
	}
}

// Observe folds one frame of caller audio in and reports whether it ended the
// caller's turn — either because they have gone quiet for the endpoint window
// or because they have been talking long enough that the turn has to be cut.
func (d *turnDetector) Observe(frame []float32, now time.Time) bool {
	if frameRMS(frame) >= d.threshold {
		d.voicedFor += frameDuration
		d.lastVoicedAt = now
		if !d.speaking && d.voicedFor >= d.minSpeech {
			d.speaking = true
			d.startedAt = now
		}
		return d.speaking && now.Sub(d.startedAt) >= d.maxUtterance
	}
	if !d.speaking {
		// A click or a single noisy frame is not the start of a turn, so the
		// speech budget has to be contiguous rather than accumulated over a
		// whole call.
		d.voicedFor = 0
		return false
	}
	return now.Sub(d.lastVoicedAt) >= d.endpointSilence
}

// Idle is Observe for the frames DTX never sent. The silence filler gives up
// after its window, and past that no audio arrives at all — but the caller is
// still silent, and their turn still has to be closed out.
func (d *turnDetector) Idle(now time.Time) bool {
	return d.speaking && now.Sub(d.lastVoicedAt) >= d.endpointSilence
}

// Speaking reports whether the caller is part-way through an utterance.
func (d *turnDetector) Speaking() bool { return d.speaking }

// SpokeFor reports how long the utterance that just ended ran for.
func (d *turnDetector) SpokeFor(now time.Time) time.Duration {
	if !d.speaking {
		return 0
	}
	return now.Sub(d.startedAt)
}

// Reset arms the detector for the caller's next turn.
func (d *turnDetector) Reset() {
	d.speaking = false
	d.voicedFor = 0
	d.lastVoicedAt = time.Time{}
	d.startedAt = time.Time{}
}
