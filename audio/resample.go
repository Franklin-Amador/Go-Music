package audio

import (
	"math"
	"runtime"
	"sync"
)

// Polyphase windowed-sinc resampler for offline (load-time) PCM rate conversion.
//
// Quality: Kaiser-windowed sinc with 32-tap kernels per phase. Stopband is
// ~80 dB down — comparable to scipy.signal.resample_poly with default settings.
// Anti-aliasing filter is automatically applied when downsampling.
//
// This is used for PCM only. The DSD path bypasses this entirely by decimating
// directly to the output rate with the existing Kaiser FIR — that single-stage
// pipeline is what audiophile DSD playback requires.

const (
	// 16 taps per side (32 total) gives ~70 dB stopband attenuation with a
	// Kaiser β of 8.6 — well above the threshold of audibility for noise floor.
	// Larger kernels add CPU cost without practical benefit for music.
	resampleHalfTaps = 16
	resampleBeta     = 8.6
)

// resamplePCM converts src (interleaved channels) from srcRate to dstRate.
// Polyphase Kaiser-sinc; precomputed phase kernels; float32 throughout.
// Parallelized across CPU cores for fast load times.
func resamplePCM(src []float32, channels int, srcRate, dstRate uint32) []float32 {
	if srcRate == dstRate {
		out := make([]float32, len(src))
		copy(out, src)
		return out
	}
	srcFrames := len(src) / channels
	if srcFrames == 0 {
		return nil
	}

	g := gcdU32(srcRate, dstRate)
	L := int(dstRate / g) // upsample factor
	M := int(srcRate / g) // downsample factor

	cutoffScale := 1.0
	if dstRate < srcRate {
		cutoffScale = float64(dstRate) / float64(srcRate)
	}

	kernelLen := 2 * resampleHalfTaps
	// Flat kernel array — better cache locality than [][]float32.
	kernels := make([]float32, L*kernelLen)
	for p := 0; p < L; p++ {
		frac := float64(p) / float64(L)
		base := p * kernelLen
		for i := 0; i < kernelLen; i++ {
			off := float64(i - resampleHalfTaps + 1)
			x := off - frac
			kernels[base+i] = float32(windowedSinc(x*cutoffScale, resampleHalfTaps, resampleBeta) * cutoffScale)
		}
	}

	dstFrames := int(int64(srcFrames) * int64(L) / int64(M))
	dst := make([]float32, dstFrames*channels)

	// Pre-compute the boundary between edge frames (need bounds checks) and
	// interior frames (full kernel fits in source).
	edge := resampleHalfTaps + 1
	interiorStart := edge * L / M
	interiorEnd := dstFrames - edge*L/M - 1
	if interiorEnd < 0 {
		interiorEnd = 0
	}

	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	// For small files, parallel overhead isn't worth it.
	if dstFrames < workers*4096 {
		workers = 1
	}

	if workers == 1 {
		resampleRange(src, dst, channels, srcFrames, kernels, kernelLen, L, M, 0, dstFrames, interiorStart, interiorEnd)
		return dst
	}

	var wg sync.WaitGroup
	chunkSize := (dstFrames + workers - 1) / workers
	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > dstFrames {
			end = dstFrames
		}
		if start >= end {
			continue
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			resampleRange(src, dst, channels, srcFrames, kernels, kernelLen, L, M, start, end, interiorStart, interiorEnd)
		}(start, end)
	}
	wg.Wait()
	return dst
}

// resampleRange computes output frames [dstStart, dstEnd) into dst.
// Workers operate on disjoint output ranges; src is read-only so no
// synchronization is needed.
func resampleRange(src, dst []float32, channels, srcFrames int, kernels []float32,
	kernelLen, L, M, dstStart, dstEnd, interiorStart, interiorEnd int) {

	for n := dstStart; n < dstEnd; n++ {
		srcCenter := (n * M) / L
		phase := (n * M) % L
		kBase := phase * kernelLen
		baseIdx := srcCenter - resampleHalfTaps + 1

		if n >= interiorStart && n <= interiorEnd {
			// Interior: no bounds checks needed.
			if channels == 2 {
				var sumL, sumR float32
				for i := 0; i < kernelLen; i++ {
					k := kernels[kBase+i]
					sIdx := (baseIdx + i) * 2
					sumL += k * src[sIdx]
					sumR += k * src[sIdx+1]
				}
				dst[n*2] = sumL
				dst[n*2+1] = sumR
			} else {
				for c := 0; c < channels; c++ {
					var sum float32
					for i := 0; i < kernelLen; i++ {
						sum += kernels[kBase+i] * src[(baseIdx+i)*channels+c]
					}
					dst[n*channels+c] = sum
				}
			}
		} else {
			// Edge: with bounds checks.
			for c := 0; c < channels; c++ {
				var sum float32
				for i := 0; i < kernelLen; i++ {
					idx := baseIdx + i
					if idx < 0 || idx >= srcFrames {
						continue
					}
					sum += kernels[kBase+i] * src[idx*channels+c]
				}
				dst[n*channels+c] = sum
			}
		}
	}
}

// ── Streaming polyphase resampler ─────────────────────────────────────────────
//
// Same Kaiser-sinc kernel as the batch resamplePCM above, but processable
// chunk-by-chunk. State carried between Process() calls:
//   - srcBuf: rolling window of decoded source frames. Holds enough tail
//     (≥ halfTaps frames) so the next chunk's first output frames have full
//     kernel context. Trimmed from the front as the output cursor advances.
//   - srcBufStart: absolute source-frame index of srcBuf[0]. Lets us map
//     output frame n's required source window into a local buf index.
//   - outputAbsPos: total output frames produced across every Process() call.
//
// Invariant: after Process() returns, the next call can be made with the
// next contiguous chunk of source frames and the math stays correct.
//
// Used by the streaming PCM loader in engine.go so Play() can start within
// ~250 ms instead of waiting for the full file to resample. The batch
// resamplePCM stays in place for the crossfade Preload path, where the
// background goroutine has all the time it needs anyway.
type streamingResampler struct {
	channels int
	L, M     int // up/down factors
	halfTaps int
	kernels  []float32 // L × (2·halfTaps) flat layout — same as batch path

	// Rolling source-frame window.
	srcBuf      []float32 // interleaved, [frame0_c0, frame0_c1, frame1_c0, ...]
	srcBufStart int64     // absolute source frame index of srcBuf[0]

	outputAbsPos int64
}

func newStreamingResampler(channels int, srcRate, dstRate uint32) *streamingResampler {
	g := gcdU32(srcRate, dstRate)
	L := int(dstRate / g)
	M := int(srcRate / g)
	halfTaps := resampleHalfTaps

	cutoffScale := 1.0
	if dstRate < srcRate {
		cutoffScale = float64(dstRate) / float64(srcRate)
	}

	kernelLen := 2 * halfTaps
	kernels := make([]float32, L*kernelLen)
	for p := 0; p < L; p++ {
		frac := float64(p) / float64(L)
		base := p * kernelLen
		for i := 0; i < kernelLen; i++ {
			off := float64(i - halfTaps + 1)
			x := off - frac
			kernels[base+i] = float32(windowedSinc(x*cutoffScale, halfTaps, resampleBeta) * cutoffScale)
		}
	}

	return &streamingResampler{
		channels: channels,
		L:        L,
		M:        M,
		halfTaps: halfTaps,
		kernels:  kernels,
	}
}

// Process consumes srcChunk (interleaved, channels-matched) and returns the
// output frames it can produce given history + this chunk.
//
// If isLast is true, the resampler treats the source as ending at the end of
// srcChunk and flushes the trailing edge frames with zero-padded kernel reads
// — matching the total-output-frame count of the batch resamplePCM.
//
// Returns nil if not enough source has accumulated to produce any new output.
func (sr *streamingResampler) Process(srcChunk []float32, isLast bool) []float32 {
	if len(srcChunk) > 0 {
		sr.srcBuf = append(sr.srcBuf, srcChunk...)
	}

	chFrames := int64(len(sr.srcBuf) / sr.channels)
	inputAbsEnd := sr.srcBufStart + chFrames

	// Highest output frame index we can produce. Without isLast we keep
	// halfTaps frames in reserve so the next chunk's first samples have full
	// kernel context. With isLast we let the edge path zero-pad past EOF.
	var maxN int64
	L64, M64, halfTaps64 := int64(sr.L), int64(sr.M), int64(sr.halfTaps)
	if isLast {
		// Match the batch formula: dstFrames = srcFrames * L / M.
		// inputAbsEnd is the total source frames seen so far across the
		// whole stream — which equals srcFrames when isLast is set.
		maxN = inputAbsEnd * L64 / M64
	} else {
		safeInput := inputAbsEnd - halfTaps64
		if safeInput <= 0 {
			return nil
		}
		// ceil-equivalent isn't needed — floor gives the last n whose
		// kernel center+halfTaps fits within inputAbsEnd-halfTaps.
		maxN = safeInput * L64 / M64
	}

	if maxN <= sr.outputAbsPos {
		return nil
	}

	outFrames := maxN - sr.outputAbsPos
	out := make([]float32, outFrames*int64(sr.channels))

	kernelLen := 2 * sr.halfTaps
	bufFrames := int64(len(sr.srcBuf) / sr.channels)

	for n := sr.outputAbsPos; n < maxN; n++ {
		srcCenter := (n * M64) / L64
		phase := int((n * M64) % L64)
		kBase := phase * kernelLen
		baseIdxAbs := srcCenter - halfTaps64 + 1
		baseIdxLocal := baseIdxAbs - sr.srcBufStart
		outRow := (n - sr.outputAbsPos) * int64(sr.channels)

		// Hot path: full kernel fits in srcBuf, no zero-pad needed.
		if baseIdxLocal >= 0 && baseIdxLocal+int64(kernelLen) <= bufFrames {
			if sr.channels == 2 {
				var sumL, sumR float32
				bIdx := baseIdxLocal * 2
				for i := 0; i < kernelLen; i++ {
					k := sr.kernels[kBase+i]
					sumL += k * sr.srcBuf[bIdx]
					sumR += k * sr.srcBuf[bIdx+1]
					bIdx += 2
				}
				out[outRow] = sumL
				out[outRow+1] = sumR
			} else {
				for c := 0; c < sr.channels; c++ {
					var sum float32
					for i := 0; i < kernelLen; i++ {
						sum += sr.kernels[kBase+i] * sr.srcBuf[(baseIdxLocal+int64(i))*int64(sr.channels)+int64(c)]
					}
					out[outRow+int64(c)] = sum
				}
			}
			continue
		}

		// Edge path: zero-pad outside [0, bufFrames). Only happens at the
		// very start (baseIdxAbs < 0) and at EOF when isLast=true.
		for c := 0; c < sr.channels; c++ {
			var sum float32
			for i := 0; i < kernelLen; i++ {
				idx := baseIdxLocal + int64(i)
				if idx < 0 || idx >= bufFrames {
					continue
				}
				sum += sr.kernels[kBase+i] * sr.srcBuf[idx*int64(sr.channels)+int64(c)]
			}
			out[outRow+int64(c)] = sum
		}
	}

	sr.outputAbsPos = maxN

	// Drop fully-consumed history. The next output frame that could ever be
	// requested is maxN; its kernel reaches back to srcCenter - halfTaps + 1.
	// Keep one extra halfTaps margin to avoid edge-case off-by-ones.
	nextSrcCenter := (maxN * M64) / L64
	keepFromAbs := nextSrcCenter - halfTaps64*2
	if keepFromAbs > sr.srcBufStart {
		toDrop := keepFromAbs - sr.srcBufStart
		if toDrop*int64(sr.channels) >= int64(len(sr.srcBuf)) {
			sr.srcBuf = sr.srcBuf[:0]
			sr.srcBufStart = inputAbsEnd
		} else {
			copy(sr.srcBuf, sr.srcBuf[toDrop*int64(sr.channels):])
			sr.srcBuf = sr.srcBuf[:int64(len(sr.srcBuf))-toDrop*int64(sr.channels)]
			sr.srcBufStart = keepFromAbs
		}
	}

	return out
}

// windowedSinc returns sinc(x) * Kaiser(x/halfTaps, beta) for |x| < halfTaps.
// Outside that range it returns 0 (kernel truncation).
func windowedSinc(x float64, halfTaps int, beta float64) float64 {
	absX := math.Abs(x)
	if absX >= float64(halfTaps) {
		return 0
	}
	var sincPart float64
	if x == 0 {
		sincPart = 1.0
	} else {
		px := math.Pi * x
		sincPart = math.Sin(px) / px
	}
	arg := x / float64(halfTaps)
	windowPart := besselI0(beta*math.Sqrt(1-arg*arg)) / besselI0(beta)
	return sincPart * windowPart
}

func gcdU32(a, b uint32) uint32 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// adaptChannels converts a chunk from srcCh to dstCh layout (interleaved).
//   - mono   → stereo: duplicate
//   - stereo → mono:   average
//   - >2     → 2:      take first two channels (L, R)
//   - same:            pass-through
func adaptChannels(src []float32, srcCh, dstCh int) []float32 {
	if srcCh == dstCh {
		return src
	}
	frames := len(src) / srcCh

	if srcCh == 1 && dstCh == 2 {
		dst := make([]float32, frames*2)
		for i := 0; i < frames; i++ {
			v := src[i]
			dst[i*2] = v
			dst[i*2+1] = v
		}
		return dst
	}
	if srcCh == 2 && dstCh == 1 {
		dst := make([]float32, frames)
		for i := 0; i < frames; i++ {
			dst[i] = (src[i*2] + src[i*2+1]) * 0.5
		}
		return dst
	}
	if srcCh > 2 && dstCh == 2 {
		dst := make([]float32, frames*2)
		for i := 0; i < frames; i++ {
			dst[i*2] = src[i*srcCh]
			dst[i*2+1] = src[i*srcCh+1]
		}
		return dst
	}
	dst := make([]float32, frames*dstCh)
	minCh := srcCh
	if dstCh < minCh {
		minCh = dstCh
	}
	for i := 0; i < frames; i++ {
		for c := 0; c < minCh; c++ {
			dst[i*dstCh+c] = src[i*srcCh+c]
		}
	}
	return dst
}
