package audio

import (
	"math"
	"math/cmplx"
	"sync"
	"sync/atomic"
)

// Visualizer collects a rolling window of recent audio samples and exposes
// frequency-band magnitudes for UI rendering.
//
// Producer side: Push() is called from the audio producer/Reader on every
// chunk. It's a memcpy into a lock-free ring — safe to call from the audio
// callback path because it does no allocation and no DSP.
//
// Consumer side: Snapshot() runs an FFT over the latest window and returns
// N log-spaced band magnitudes in [0, 1]. The UI calls this from its
// animation ticker (~30 fps).
type Visualizer struct {
	ring     []float32
	cap      int
	writeIdx atomic.Int64

	channels   int
	sampleRate uint32

	bands int

	// Internal scratch (only accessed from Snapshot, so single-goroutine)
	mu          sync.Mutex
	scratch     []float64
	complexBuf  []complex128
	hannWindow  []float64
	bandBuckets [][2]int // per-band: [startBin, endBin] inclusive
	displayHold []float32
}

// NewVisualizer allocates a visualizer for stereo float32 audio at the given
// sample rate. windowSize must be a power of two; 1024 is a good default
// (≈12 ms of audio at 88.2 kHz, ≈6 ms at 176.4 kHz).
func NewVisualizer(sampleRate uint32, channels, windowSize, bands int) *Visualizer {
	if !isPow2(windowSize) {
		panic("visualizer windowSize must be a power of two")
	}
	v := &Visualizer{
		ring:        make([]float32, windowSize*4), // 4× window for safety
		cap:         windowSize * 4,
		channels:    channels,
		sampleRate:  sampleRate,
		bands:       bands,
		scratch:     make([]float64, windowSize),
		complexBuf:  make([]complex128, windowSize),
		hannWindow:  buildHannWindow(windowSize),
		displayHold: make([]float32, bands),
	}
	v.bandBuckets = buildLogBands(bands, windowSize/2, sampleRate)
	return v
}

// Push appends interleaved samples to the ring. Cheap — pure memcpy.
// Safe to call from the audio Reader thread.
func (v *Visualizer) Push(interleaved []float32) {
	if len(interleaved) == 0 {
		return
	}
	w := v.writeIdx.Load()
	for _, s := range interleaved {
		v.ring[int(w%int64(v.cap))] = s
		w++
	}
	v.writeIdx.Store(w)
}

// Snapshot computes a fresh FFT over the latest window and returns N band
// magnitudes in [0, 1]. Falls back to zeros if not enough samples have been
// pushed yet. Includes exponential-decay smoothing so bars fall gracefully.
func (v *Visualizer) Snapshot() []float32 {
	v.mu.Lock()
	defer v.mu.Unlock()

	winSize := len(v.scratch)
	w := v.writeIdx.Load()
	available := w
	if available < int64(winSize) {
		// Not enough data yet — decay-only update so existing bars fade out.
		for i := range v.displayHold {
			v.displayHold[i] *= 0.85
		}
		out := make([]float32, len(v.displayHold))
		copy(out, v.displayHold)
		return out
	}

	// Pull the latest winSize samples (mono-summed across channels) into scratch.
	// We read interleaved samples; sum channel 0+1 / 2 for mono visualization.
	samplesPerFrame := v.channels
	framesNeeded := winSize
	startSample := w - int64(framesNeeded*samplesPerFrame)
	if startSample < 0 {
		startSample = 0
	}
	for i := 0; i < winSize; i++ {
		base := startSample + int64(i*samplesPerFrame)
		var sum float32
		for c := 0; c < samplesPerFrame; c++ {
			sum += v.ring[int((base+int64(c))%int64(v.cap))]
		}
		v.scratch[i] = float64(sum) / float64(samplesPerFrame)
	}

	// Apply Hann window.
	for i := range v.scratch {
		v.scratch[i] *= v.hannWindow[i]
	}

	// FFT (in-place radix-2).
	for i, x := range v.scratch {
		v.complexBuf[i] = complex(x, 0)
	}
	fftRadix2(v.complexBuf)

	// Aggregate magnitudes into log-spaced bands.
	out := make([]float32, v.bands)
	for b, br := range v.bandBuckets {
		var sum float64
		count := 0
		for i := br[0]; i <= br[1]; i++ {
			mag := cmplx.Abs(v.complexBuf[i])
			sum += mag
			count++
		}
		if count == 0 {
			continue
		}
		avg := sum / float64(count)
		// Normalize: rough scale to [0, 1] in dB
		db := 20 * math.Log10(avg+1e-9)
		// Map -60 dB → 0, 0 dB → 1
		const minDB, maxDB = -60.0, 0.0
		norm := (db - minDB) / (maxDB - minDB)
		if norm < 0 {
			norm = 0
		}
		if norm > 1 {
			norm = 1
		}
		out[b] = float32(norm)
	}

	// Smooth: bars rise instantly, fall exponentially.
	for i := range out {
		decayed := v.displayHold[i] * 0.78
		if out[i] > decayed {
			v.displayHold[i] = out[i]
		} else {
			v.displayHold[i] = decayed
		}
		out[i] = v.displayHold[i]
	}
	return out
}

// Waveform returns n peak-amplitude samples covering the last windowMs of
// audio, in playback order (oldest → newest). Each output sample is the
// maximum |sample| within its time slice, so the resulting curve traces the
// envelope of the music rather than the underlying high-frequency content.
// Values are clipped to [0, 1]. Safe to call from any goroutine; pure read
// of the lock-free ring.
func (v *Visualizer) Waveform(n int, windowMs int) []float32 {
	if n <= 0 {
		return nil
	}
	out := make([]float32, n)
	if windowMs <= 0 {
		windowMs = 35
	}

	samplesPerFrame := v.channels
	totalFrames := int(uint32(windowMs) * v.sampleRate / 1000)
	if totalFrames < n {
		totalFrames = n
	}

	w := v.writeIdx.Load()
	if w < int64(totalFrames*samplesPerFrame) {
		return out // not enough samples buffered yet
	}
	startSample := w - int64(totalFrames*samplesPerFrame)

	framesPerSlot := totalFrames / n
	if framesPerSlot < 1 {
		framesPerSlot = 1
	}

	for i := 0; i < n; i++ {
		var peak float32
		base := startSample + int64(i*framesPerSlot*samplesPerFrame)
		for f := 0; f < framesPerSlot; f++ {
			for c := 0; c < samplesPerFrame; c++ {
				s := v.ring[int((base+int64(f*samplesPerFrame+c))%int64(v.cap))]
				if s < 0 {
					s = -s
				}
				if s > peak {
					peak = s
				}
			}
		}
		if peak > 1 {
			peak = 1
		}
		out[i] = peak
	}
	return out
}

// ── FFT (radix-2 Cooley-Tukey, in-place) ─────────────────────────────────────

func fftRadix2(x []complex128) {
	n := len(x)
	// Bit-reversal permutation
	j := 0
	for i := 1; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	// Butterflies
	for size := 2; size <= n; size *= 2 {
		half := size / 2
		omega := -2 * math.Pi / float64(size)
		wStep := cmplx.Exp(complex(0, omega))
		for start := 0; start < n; start += size {
			w := complex(1, 0)
			for k := 0; k < half; k++ {
				t := w * x[start+k+half]
				x[start+k+half] = x[start+k] - t
				x[start+k] += t
				w *= wStep
			}
		}
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func isPow2(n int) bool { return n > 0 && (n&(n-1)) == 0 }

func buildHannWindow(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(n-1)))
	}
	return w
}

// buildLogBands assigns FFT bins (0..nBins-1) to log-spaced visualization
// bands roughly covering 20 Hz to 20 kHz.
func buildLogBands(numBands, nBins int, sampleRate uint32) [][2]int {
	const fMin, fMax = 20.0, 20000.0
	out := make([][2]int, numBands)
	binHz := float64(sampleRate) / 2 / float64(nBins)
	prevBin := 0
	for b := 0; b < numBands; b++ {
		// Edge frequencies of this band
		hi := fMin * math.Pow(fMax/fMin, float64(b+1)/float64(numBands))
		hiBin := int(hi/binHz) + 1
		if hiBin >= nBins {
			hiBin = nBins - 1
		}
		if hiBin < prevBin {
			hiBin = prevBin
		}
		out[b] = [2]int{prevBin, hiBin}
		prevBin = hiBin + 1
		if prevBin >= nBins {
			prevBin = nBins - 1
		}
	}
	return out
}
