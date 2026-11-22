package mlow

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"testing"
)

// goldenEncodeDecodeHash is the SHA-256 of every payload the encoder produced
// for goldenSignal, followed by the decoder's PCM for those payloads, captured
// before the codec's speed work (cached FFT twiddles, reused FCB search
// scratch). Those changes must not alter a single output bit; if this hash
// moves, the codec's output changed and calls would sound different.
const goldenEncodeDecodeHash = "87bdd0654e012b59611fcc3214490d7d8767b7e6bb7fbdaf87766b7874c05753"

// goldenSignal is 240 frames (14.4 s) mixing silence, tones, a chirp, noise
// and loud clipped bursts, so every encoder path (VAD on/off, low and high
// energy, pitch tracking) is exercised.
func goldenSignal() [][]float32 {
	const frames = 240
	out := make([][]float32, frames)
	seed := uint32(12345)
	noise := func() float32 {
		seed = seed*1664525 + 1013904223
		return float32(int32(seed))/float32(math.MaxInt32)
	}
	n := 0
	for f := 0; f < frames; f++ {
		frame := make([]float32, 960)
		for i := range frame {
			t := float64(n) / 16000
			var v float64
			switch (f / 20) % 6 {
			case 0: // silence
			case 1: // voiced-like harmonics at 140 Hz
				for h := 1; h <= 8; h++ {
					v += 0.3 / float64(h) * math.Sin(2*math.Pi*140*float64(h)*t)
				}
			case 2: // chirp 200 -> 3000 Hz
				v = 0.5 * math.Sin(2*math.Pi*(200*t+700*t*t))
			case 3: // white noise
				v = 0.4 * float64(noise())
			case 4: // quiet tone near the VAD threshold
				v = 0.01 * math.Sin(2*math.Pi*440*t)
			case 5: // loud clipped square-ish burst
				v = math.Max(-1, math.Min(1, 3*math.Sin(2*math.Pi*300*t)))
			}
			frame[i] = float32(v)
			n++
		}
		out[f] = frame
	}
	return out
}

func goldenHash(t testing.TB) string {
	enc := NewMlowEncoder()
	dec := NewMlowDecoder()
	h := sha256.New()
	var payloads [][]byte
	for _, frame := range goldenSignal() {
		p, err := enc.Encode(frame)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		payloads = append(payloads, p)
		var l [4]byte
		binary.LittleEndian.PutUint32(l[:], uint32(len(p)))
		h.Write(l[:])
		h.Write(p)
	}
	for _, p := range payloads {
		for _, s := range dec.Decode(p) {
			var b [4]byte
			binary.LittleEndian.PutUint32(b[:], math.Float32bits(s))
			h.Write(b[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestEncodeDecodeBitExact(t *testing.T) {
	got := goldenHash(t)
	if got != goldenEncodeDecodeHash {
		t.Fatalf("codec output changed: hash %s, want %s", got, goldenEncodeDecodeHash)
	}
	// Two encoders run back to back (as two calls would) give the same bits:
	// nothing reused between calls leaks state from one into the other.
	if again := goldenHash(t); again != got {
		t.Fatalf("second run differs: %s vs %s", again, got)
	}
}

func BenchmarkEncodeFrame(b *testing.B) {
	frames := goldenSignal()
	enc := NewMlowEncoder()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := enc.Encode(frames[i%len(frames)]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeFrame(b *testing.B) {
	frames := goldenSignal()
	enc := NewMlowEncoder()
	payloads := make([][]byte, len(frames))
	for i, f := range frames {
		p, err := enc.Encode(f)
		if err != nil {
			b.Fatal(err)
		}
		payloads[i] = p
	}
	dec := NewMlowDecoder()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec.Decode(payloads[i%len(payloads)])
	}
}
