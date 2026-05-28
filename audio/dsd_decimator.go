package audio

import (
	"math"
	"sync"
)

// dsdReconstructionCutoffHz is the lowpass cutoff applied to the DSD bitstream
// before decimation to PCM. 25 kHz preserves all human-audible content while
// strongly attenuating the steep rise of DSD's noise-shaping spectrum that
// begins above ~20 kHz — which would otherwise alias into the audible band.
const dsdReconstructionCutoffHz = 25000.0

// DSDDecimatingFIR is a streaming, single-stage anti-aliasing decimator for
// DSD bitstreams. It applies a long Kaiser-windowed lowpass FIR at the DSD
// bit rate, producing PCM samples at the output rate in one filter pass.
//
// This replaces the previous "block-mean + post-decimation Kaiser FIR" pipeline.
// That pipeline left audible buzz because the block-mean (a rectangular FIR)
// has only ~-18 dB stopband attenuation in the worst-case alias bands — DSD
// shaped-noise around 200 kHz, 380 kHz, etc. would alias right back into the
// audible passband. The long FIR achieves ~-115 dB stopband everywhere it
// matters so aliased noise stays below the noise floor of any real DAC.
//
// Filter design:
//
//	passband:  0 .. 25 kHz     (flat, ripple <0.1 dB)
//	stopband:  outputRate/2 .. fs_DSD/2, attenuation -115 dB (Kaiser β=12)
//	length:    chosen so the transition (25 kHz .. outputRate/2) fits the
//	           Kaiser rule N ≈ (A-8) / (2.285·Δω). Scales with DSD rate.
//
// Per-channel cost (≈ N mul-adds × output rate):
//
//	DSD64  → ~340 taps  →  ~60  MOPS / channel
//	DSD128 → ~670 taps  →  ~120 MOPS / channel
//	DSD256 → ~1340 taps →  ~240 MOPS / channel
//
// Channels are processed in parallel goroutines, so a stereo file uses both.
type DSDDecimatingFIR struct {
	fir      []float32
	N        int
	D        int
	channels int

	// Per-channel sliding stream of the last (N-1) ±1 values from previous
	// chunks. Initialized to zeros so the very first samples ramp from
	// silence over ~N/fs_DSD seconds (well under a millisecond).
	history [][]float32
}

// newDSDDecimatingFIR builds a decimator that maps a DSD bitstream at dsdSR
// down to PCM at outputSR. dsdSR must be an integer multiple of outputSR.
func newDSDDecimatingFIR(dsdSR, outputSR uint32, channels int) *DSDDecimatingFIR {
	D := int(dsdSR / outputSR)
	if D < 1 {
		D = 1
	}

	// Cutoff normalized to fs_DSD/2 (Nyquist of the DSD sample rate)
	cutoff := 2.0 * dsdReconstructionCutoffHz / float64(dsdSR)

	// Transition band runs from cutoff up to outputSR/2 — that's where alias
	// protection becomes mandatory: anything above outputSR/2 in the DSD
	// signal would fold into the output passband if not suppressed here.
	transitionNorm := 2.0 * (float64(outputSR)/2 - dsdReconstructionCutoffHz) / float64(dsdSR)
	if transitionNorm < 0.001 {
		transitionNorm = 0.001
	}

	beta := 12.0
	// Kaiser length formula: N ≈ (A-8) / (2.285·Δω) with Δω = π·transitionNorm
	N := int((115.0-8.0)/(2.285*math.Pi*transitionNorm)) + 1
	if N < 64 {
		N = 64
	}
	if N%2 != 0 {
		N++
	}

	fir := buildKaiserFIR(N, cutoff, beta)

	history := make([][]float32, channels)
	for c := range history {
		history[c] = make([]float32, N-1)
	}

	return &DSDDecimatingFIR{
		fir:      fir,
		N:        N,
		D:        D,
		channels: channels,
		history:  history,
	}
}

// Process consumes per-channel bit arrays (each byte = single bit, 0 or 1)
// and emits interleaved PCM samples at the output rate. All channels are
// expected to carry the same number of bits per call.
func (d *DSDDecimatingFIR) Process(channelBits [][]byte) []float32 {
	if len(channelBits) == 0 {
		return nil
	}
	nBits := len(channelBits[0])
	for c := 1; c < len(channelBits); c++ {
		if len(channelBits[c]) < nBits {
			nBits = len(channelBits[c])
		}
	}
	if nBits == 0 {
		return nil
	}

	nOutputs := nBits / d.D
	if nOutputs == 0 {
		// Not enough bits for an output sample; just slide history forward.
		for c := 0; c < d.channels; c++ {
			d.advanceHistory(c, channelBits[c], nBits)
		}
		return nil
	}

	// Per-channel output buffers, computed in parallel, then interleaved.
	perChannel := make([][]float32, d.channels)

	var wg sync.WaitGroup
	for c := 0; c < d.channels; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			perChannel[c] = d.processChannel(channelBits[c], nBits, nOutputs, c)
		}(c)
	}
	wg.Wait()

	out := make([]float32, nOutputs*d.channels)
	for c := 0; c < d.channels; c++ {
		ch := perChannel[c]
		for i := 0; i < nOutputs; i++ {
			out[i*d.channels+c] = ch[i]
		}
	}
	return out
}

// processChannel runs the FIR + decimation for one channel. Builds an
// effective ±1 float32 stream (history concatenated with this chunk's bits)
// for tight-loop FIR access, then convolves.
func (d *DSDDecimatingFIR) processChannel(bits []byte, nBits, nOutputs, c int) []float32 {
	N := d.N
	D := d.D
	fir := d.fir
	hist := d.history[c]

	effLen := (N - 1) + nBits
	eff := make([]float32, effLen)
	copy(eff, hist)
	base := N - 1
	for i := 0; i < nBits; i++ {
		if bits[i] != 0 {
			eff[base+i] = 1.0
		} else {
			eff[base+i] = -1.0
		}
	}

	out := make([]float32, nOutputs)
	for i := 0; i < nOutputs; i++ {
		var sum float32
		winStart := i * D
		// Tight FIR convolution. Sequential access on both arrays — the
		// hardware prefetcher and L2 cache make this very fast.
		for k := 0; k < N; k++ {
			sum += fir[k] * eff[winStart+k]
		}
		out[i] = sum
	}

	// Save last (N-1) effective-stream values as next chunk's history.
	copy(hist, eff[effLen-(N-1):])
	return out
}

// advanceHistory updates the history when this chunk produced no output
// (very short input). Keeps the streaming state consistent.
func (d *DSDDecimatingFIR) advanceHistory(c int, bits []byte, nBits int) {
	hist := d.history[c]
	N := d.N
	if nBits >= N-1 {
		for k := 0; k < N-1; k++ {
			if bits[nBits-(N-1)+k] != 0 {
				hist[k] = 1.0
			} else {
				hist[k] = -1.0
			}
		}
	} else {
		copy(hist, hist[nBits:])
		offset := (N - 1) - nBits
		for k := 0; k < nBits; k++ {
			if bits[k] != 0 {
				hist[offset+k] = 1.0
			} else {
				hist[offset+k] = -1.0
			}
		}
	}
}
