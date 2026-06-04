package main

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"image/color"
)

// dominantHex extracts the most visually prominent color from album-art bytes
// and returns it as a CSS hex string like "#1db954". Returns "" on failure.
//
// Algorithm mirrors ui/dominant.go exactly: 64×64 sample grid, discard
// near-black / near-white / low-saturation pixels, 16³ histogram weighted
// by saturation, weighted centroid of the heaviest bucket.
func dominantHex(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return ""
	}

	const samples    = 64
	const bucketBits = 4

	type accum struct{ r, g, b, weight uint64 }
	hist := make(map[uint16]*accum, 256)

	for sy := 0; sy < samples; sy++ {
		py := bounds.Min.Y + sy*h/samples
		for sx := 0; sx < samples; sx++ {
			px := bounds.Min.X + sx*w/samples
			r16, g16, b16, a16 := img.At(px, py).RGBA()
			if a16 < 0x8000 {
				continue
			}
			r, g, b := uint32(r16>>8), uint32(g16>>8), uint32(b16>>8)

			maxC, minC := r, r
			if g > maxC { maxC = g } else if g < minC { minC = g }
			if b > maxC { maxC = b } else if b < minC { minC = b }
			if maxC < 40 || maxC > 240 { continue }
			sat := maxC - minC
			if sat < 25 { continue }

			wt  := uint64(sat)
			key := uint16(r>>(8-bucketBits))<<(2*bucketBits) |
				uint16(g>>(8-bucketBits))<<bucketBits |
				uint16(b>>(8-bucketBits))
			a := hist[key]
			if a == nil { a = &accum{}; hist[key] = a }
			a.r += uint64(r) * wt
			a.g += uint64(g) * wt
			a.b += uint64(b) * wt
			a.weight += wt
		}
	}

	if len(hist) == 0 {
		return ""
	}
	var best *accum
	for _, a := range hist {
		if best == nil || a.weight > best.weight {
			best = a
		}
	}
	c := color.NRGBA{
		R: uint8(best.r / best.weight),
		G: uint8(best.g / best.weight),
		B: uint8(best.b / best.weight),
	}
	// Brighten toward white by 30 % so the extracted color stays readable on dark bg.
	c = brightenColor(c, 0.30)
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

func brightenColor(c color.NRGBA, t float32) color.NRGBA {
	if t <= 0 { return c }
	if t > 1  { t = 1 }
	lift := func(v uint8) uint8 {
		f := float32(v) + (255-float32(v))*t
		if f > 255 { f = 255 }
		return uint8(f)
	}
	return color.NRGBA{R: lift(c.R), G: lift(c.G), B: lift(c.B), A: 0xff}
}
