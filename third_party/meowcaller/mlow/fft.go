package mlow

import (
	"math"
	"sync"
)

// cpx is a single-precision complex value.
//
// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L318-L343
type cpx struct {
	re, im float32
}

func (a cpx) add(b cpx) cpx {
	return cpx{re: a.re + b.re, im: a.im + b.im}
}

func (a cpx) mul(b cpx) cpx {
	return cpx{
		re: a.re*b.re - a.im*b.im,
		im: a.re*b.im + a.im*b.re,
	}
}

// smallestFactor returns the smallest prime factor of n (>= 2).
func smallestFactor(n int) int {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L346-L358
	if n%2 == 0 {
		return 2
	}
	p := 3
	for p*p <= n {
		if n%p == 0 {
			return p
		}
		p += 2
	}
	return n
}

// fftRec is the recursive mixed-radix Cooley-Tukey DFT. sign is -1 forward, +1
// inverse (unnormalized). x holds n inputs at the given stride; out is contiguous.
// scratch holds the sub-transforms of every level below this one: n for this
// level plus what the next level needs, at most 2n in all.
func fftRec(x []cpx, stride, n int, sign float32, out, scratch []cpx) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L362-L405
	if n == 1 {
		out[0] = x[0]
		return
	}
	p := smallestFactor(n)
	if p == n {
		w := primeTwiddles(n, sign)
		for k := 0; k < n; k++ {
			var acc cpx
			row := w[k*n : (k+1)*n]
			for j := 0; j < n; j++ {
				acc = acc.add(x[j*stride].mul(row[j]))
			}
			out[k] = acc
		}
		return
	}
	m := n / p
	sub, rest := scratch[:n], scratch[n:]
	for q := 0; q < p; q++ {
		fftRec(x[q*stride:], stride*p, m, sign, sub[q*m:(q+1)*m], rest)
	}
	tw := stageTwiddles(n, p, sign)
	for k := 0; k < n; k++ {
		kmod := k % m
		var acc cpx
		row := tw[k*p : (k+1)*p]
		for q := 0; q < p; q++ {
			acc = acc.add(sub[q*m+kmod].mul(row[q]))
		}
		out[k] = acc
	}
}

// twiddleKey names one cached twiddle table: prime marks the direct-DFT table
// of a prime length, otherwise it is a mixed-radix stage of radix p.
type twiddleKey struct {
	n, p  int
	sign  float32
	prime bool
}

// twiddleCache maps twiddleKey to a []cpx that is never written after it is
// stored, so concurrent calls share it safely.
var twiddleCache sync.Map

// primeTwiddles is the n*n factor table of fftRec's prime-length DFT, row k
// for output bin k, built with the exact float32 angle expression fftRec used
// inline, so every value is bit-identical.
func primeTwiddles(n int, sign float32) []cpx {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L362-L405
	key := twiddleKey{n: n, p: n, sign: sign, prime: true}
	if v, ok := twiddleCache.Load(key); ok {
		return v.([]cpx)
	}
	w := make([]cpx, n*n)
	for k := 0; k < n; k++ {
		angK := sign * 2.0 * smplPI * float32(k) / float32(n)
		for j := 0; j < n; j++ {
			ang := angK * float32(j)
			w[k*n+j] = cpx{re: float32(math.Cos(float64(ang))), im: float32(math.Sin(float64(ang)))}
		}
	}
	v, _ := twiddleCache.LoadOrStore(key, w)
	return v.([]cpx)
}

// stageTwiddles is the n*p factor table of one mixed-radix combine stage, row
// k for output bin k, from the exact float32 angle expression fftRec used
// inline.
func stageTwiddles(n, p int, sign float32) []cpx {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L362-L405
	key := twiddleKey{n: n, p: p, sign: sign}
	if v, ok := twiddleCache.Load(key); ok {
		return v.([]cpx)
	}
	tw := make([]cpx, n*p)
	for k := 0; k < n; k++ {
		for q := 0; q < p; q++ {
			ang := sign * 2.0 * smplPI * float32(k) * float32(q) / float32(n)
			tw[k*p+q] = cpx{re: float32(math.Cos(float64(ang))), im: float32(math.Sin(float64(ang)))}
		}
	}
	v, _ := twiddleCache.LoadOrStore(key, tw)
	return v.([]cpx)
}

// cfft computes the complex FFT of a mixed-radix length into out. sign=-1 forward,
// +1 inverse.
func cfft(input, out []cpx, sign float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L408-L412
	n := len(input)
	scratch := getCpx(2 * n)
	fftRec(input, 1, n, sign, out, *scratch)
	putCpx(scratch)
}

// cpxPool recycles the FFT's working buffers. The encoder runs several FFTs a
// frame on every live call, and allocating them each time was most of its
// garbage.
var cpxPool sync.Pool

// getCpx returns a buffer of length n with unspecified contents.
func getCpx(n int) *[]cpx {
	if b, _ := cpxPool.Get().(*[]cpx); b != nil && cap(*b) >= n {
		*b = (*b)[:n]
		return b
	}
	b := make([]cpx, n)
	return &b
}

func putCpx(b *[]cpx) { cpxPool.Put(b) }

// rfftForwardOrdered is the forward real FFT of n real samples, re-packed into the
// ordered REAL layout: f[0]=DC.re, f[1]=Nyquist.re, then [re,im] pairs for bins
// 1..n/2-1. Output length is n.
func rfftForwardOrdered(time, f []float32) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/674e85164b35ca19115dfebcf605708d15951ee7/wacore/src/voip/mlow/smpl_perc.rs#L416-L432
	n := len(time)
	cinBuf, specBuf := getCpx(n), getCpx(n)
	defer putCpx(cinBuf)
	defer putCpx(specBuf)
	cin, spec := *cinBuf, *specBuf
	for i := 0; i < n; i++ {
		cin[i] = cpx{re: time[i]}
	}
	cfft(cin, spec, -1.0)
	f[0] = spec[0].re
	f[1] = spec[n/2].re
	for i := 1; i < n/2; i++ {
		f[2*i] = spec[i].re
		f[2*i+1] = spec[i].im
	}
}
