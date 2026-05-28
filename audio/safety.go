package audio

// Hearing-protection / DAC-protection stages.
//
// Two defensive filters apply to every PCM sample before it reaches the ring
// buffer (and therefore before it reaches oto / WASAPI / the DAC):
//
//  1. dcBlocker: a single-pole high-pass at ~5 Hz that removes any sub-sonic
//     DC-ish energy. Sustained DC offsets damage tweeters and woofers (voice
//     coils heat up because they sit off-center) — this stage guarantees we
//     never send any.
//
//  2. softLimiter: a smooth tanh-style waveshaper on samples whose magnitude
//     exceeds 0.95. Beyond 0.95 the limiter compresses gracefully toward
//     ±0.99 instead of letting the waveform clip. Hard clipping creates
//     square-wave edges with massive ultrasonic harmonics that can damage
//     tweeters and produce harsh, unpleasant distortion at the ear. This
//     stage guarantees the output never exceeds ±0.99 even if EQ, source
//     content, or volume control would otherwise cause clipping.
//
// Together with the 25 kHz DSD reconstruction cutoff (which prevents
// inaudible-but-damaging ultrasonic energy from reaching the DAC) these are
// the three lines of defense for safe playback at any volume.

const (
	dcBlockerR     = float32(0.9995) // pole radius — corner ≈ 5 Hz at 176.4 kHz
	limiterThresh  = float32(0.95)
	limiterCeiling = float32(0.99)
)

// dcBlocker is a single-pole high-pass DC blocker per channel.
// y[n] = x[n] - x[n-1] + R * y[n-1]
type dcBlocker struct {
	xPrev []float32
	yPrev []float32
}

func newDCBlocker(channels int) *dcBlocker {
	return &dcBlocker{
		xPrev: make([]float32, channels),
		yPrev: make([]float32, channels),
	}
}

// Process filters interleaved audio in-place. channels must match the
// blocker's configuration.
func (d *dcBlocker) Process(buf []float32, channels int) {
	if len(buf) == 0 {
		return
	}
	r := dcBlockerR
	for c := 0; c < channels; c++ {
		xp, yp := d.xPrev[c], d.yPrev[c]
		for i := c; i < len(buf); i += channels {
			x := buf[i]
			y := x - xp + r*yp
			buf[i] = y
			xp, yp = x, y
		}
		d.xPrev[c], d.yPrev[c] = xp, yp
	}
}

// softLimit clamps every sample in buf to [-limiterCeiling, +limiterCeiling]
// using a smooth (tanh-like) transition above limiterThresh. Hard clipping is
// avoided because the harmonic harshness it produces is what damages tweeters
// and ears. Operates in-place.
func softLimit(buf []float32) {
	for i, x := range buf {
		ax := x
		if ax < 0 {
			ax = -ax
		}
		if ax <= limiterThresh {
			continue
		}
		// Map the over-threshold portion smoothly toward the ceiling.
		// f(x) = thresh + (ceiling - thresh) * tanh((x - thresh) / (ceiling - thresh))
		// Approximated with a polynomial knee for cheap evaluation.
		over := ax - limiterThresh
		room := limiterCeiling - limiterThresh
		// Smooth knee: rational approximation of tanh, clipped at room.
		t := over / room
		if t > 3 {
			t = 1
		} else {
			t = t / (1 + t*t/3) // quick tanh-like
			if t > 1 {
				t = 1
			}
		}
		newAbs := limiterThresh + room*t
		if x < 0 {
			buf[i] = -newAbs
		} else {
			buf[i] = newAbs
		}
	}
}
