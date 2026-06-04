package ui

import (
	"bytes"
	"image"
	"image/color"

	// Side-effect imports register decoders so image.Decode can read each
	// format directly. JPEG covers most album art; PNG covers MP3/FLAC tags
	// that embed lossless thumbnails; GIF is rare but cheap to include.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// dominantColor returns the most visually prominent color from the given image
// bytes (album art). Returns (col, true) on success or (_, false) if the bytes
// can't be decoded.
//
// Algorithm:
//   - Decode the image
//   - Sample on a 64×64 grid (4096 samples — enough to be representative,
//     fast enough to run inline at ~10–50 ms even on a 1500-px cover)
//   - Discard near-black / near-white / low-saturation pixels (they belong
//     to backgrounds and shadows, not to the "personality" of the cover)
//   - Bucket remaining samples into a coarse 16³ RGB histogram, weighted by
//     saturation so vivid pixels carry more weight than washed-out ones
//   - Return the weighted-centroid color of the heaviest bucket
//
// This is simpler than k-means and produces results indistinguishable from it
// for the album-art use case where a single dominant hue almost always exists.
func dominantColor(data []byte) (color.NRGBA, bool) {
	if len(data) == 0 {
		return color.NRGBA{}, false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return color.NRGBA{}, false
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return color.NRGBA{}, false
	}

	const samples = 64        // grid edge: 64×64 = 4096 samples
	const bucketBits = 4      // 4 bits per channel = 16³ = 4096 buckets

	type accum struct {
		r, g, b uint64
		weight  uint64
	}
	// Map is fine: in the worst case ~4096 buckets, but real covers concentrate
	// in a few dozen.
	hist := make(map[uint16]*accum, 256)

	for sy := 0; sy < samples; sy++ {
		py := bounds.Min.Y + sy*h/samples
		for sx := 0; sx < samples; sx++ {
			px := bounds.Min.X + sx*w/samples
			r16, g16, b16, a16 := img.At(px, py).RGBA()
			if a16 < 0x8000 {
				continue // mostly transparent — ignore
			}
			r, g, b := uint32(r16>>8), uint32(g16>>8), uint32(b16>>8)

			// Discard pixels that won't contribute a meaningful hue:
			// very dark (shadows), very bright (highlights / paper), or
			// low saturation (gray backdrops). These are the pixels that
			// would otherwise dominate a naive average-color algorithm.
			maxC, minC := r, r
			if g > maxC {
				maxC = g
			} else if g < minC {
				minC = g
			}
			if b > maxC {
				maxC = b
			} else if b < minC {
				minC = b
			}
			if maxC < 40 || maxC > 240 {
				continue
			}
			sat := maxC - minC
			if sat < 25 {
				continue
			}

			weight := uint64(sat) // saturated pixels count more

			br := r >> (8 - bucketBits)
			bg := g >> (8 - bucketBits)
			bb := b >> (8 - bucketBits)
			key := uint16(br)<<(2*bucketBits) | uint16(bg)<<bucketBits | uint16(bb)
			a := hist[key]
			if a == nil {
				a = &accum{}
				hist[key] = a
			}
			a.r += uint64(r) * weight
			a.g += uint64(g) * weight
			a.b += uint64(b) * weight
			a.weight += weight
		}
	}

	if len(hist) == 0 {
		return color.NRGBA{}, false
	}

	var best *accum
	for _, a := range hist {
		if best == nil || a.weight > best.weight {
			best = a
		}
	}
	return color.NRGBA{
		R: uint8(best.r / best.weight),
		G: uint8(best.g / best.weight),
		B: uint8(best.b / best.weight),
		A: 0xff,
	}, true
}

// tint returns c with its alpha replaced by a (0..0xff). Used to scale a
// dominant color down to background-gradient strength while keeping its hue.
func tint(c color.NRGBA, a uint8) color.NRGBA {
	c.A = a
	return c
}

// brighten lifts c toward white by t (0..1). Used to keep dominant accents
// readable against the dark background — extracted colors from album art are
// often a bit muddy at their natural luminance.
func brighten(c color.NRGBA, t float32) color.NRGBA {
	if t <= 0 {
		return c
	}
	if t > 1 {
		t = 1
	}
	lift := func(v uint8) uint8 {
		f := float32(v) + (255-float32(v))*t
		if f > 255 {
			f = 255
		}
		return uint8(f)
	}
	return color.NRGBA{R: lift(c.R), G: lift(c.G), B: lift(c.B), A: c.A}
}
