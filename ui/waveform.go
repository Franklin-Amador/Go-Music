package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

// Ensure waveformWidget satisfies the tappable interface at compile time.
var _ fyne.Tappable = (*waveformWidget)(nil)

// waveformWidget draws a continuous, mirrored audio waveform — a single
// flowing curve traced top and bottom around a horizontal mid-line. Each
// frame, SetValues receives N peak-amplitude samples (0..1) covering the last
// few tens of ms of audio; the widget renders them as two glowing polylines
// connected segment by segment.
//
// Performance:
//   - Line segments are allocated once at construction. SetValues only mutates
//     each segment's Position1/Position2 and triggers one canvas.Refresh.
//   - Two glow passes (a thick translucent stroke underneath, a thin bright
//     stroke on top) give the line a soft halo without a real shader.
//   - Temporal smoothing is applied: each frame, the displayed envelope eases
//     toward the latest sample data, so the line flows instead of snapping.
type waveformWidget struct {
	widget.BaseWidget

	n       int       // number of sample points (line segments = n-1)
	target  []float32 // most recent input from SetValues
	display []float32 // smoothed envelope actually rendered

	// Layered strokes: idx 0..n-2 = bottom-glow top half, n-1..2n-3 = bottom-glow bottom half,
	// then sharp top half, then sharp bottom half. Drawing two passes (thick dim
	// behind, thin bright in front) gives the line its glow.
	glowTop  []*canvas.Line
	glowBot  []*canvas.Line
	sharpTop []*canvas.Line
	sharpBot []*canvas.Line
	midLine  *canvas.Line // faint horizontal axis
	objects  []fyne.CanvasObject

	smoothing float32 // 0..1, how much of the new value to take per frame

	// Seek support
	// OnSeek is called with a 0..1 fraction when the user taps the widget.
	// Nil means the widget is not interactive (nothing loaded).
	OnSeek  func(fraction float64)
	posFrac float32      // 0..1 current playback position, updated by SetPosition
	posLine *canvas.Line // vertical cursor showing playback position
}

var (
	colorWaveGlow  = color.NRGBA{R: 0x1d, G: 0xb9, B: 0x54, A: 0x55} // accent @ ~33% alpha
	colorWaveSharp = color.NRGBA{R: 0x9a, G: 0xff, B: 0xc0, A: 0xff} // pale accent
	colorWaveAxis  = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x18}
	colorWavePos   = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xb0} // position cursor
)

func newWaveformWidget(n int) *waveformWidget {
	if n < 4 {
		n = 4
	}
	w := &waveformWidget{
		n:         n,
		target:    make([]float32, n),
		display:   make([]float32, n),
		smoothing: 0.35,
	}
	segs := n - 1
	w.glowTop = makeLines(segs, colorWaveGlow, 4)
	w.glowBot = makeLines(segs, colorWaveGlow, 4)
	w.sharpTop = makeLines(segs, colorWaveSharp, 1.5)
	w.sharpBot = makeLines(segs, colorWaveSharp, 1.5)
	w.midLine = canvas.NewLine(colorWaveAxis)
	w.midLine.StrokeWidth = 1
	w.posLine = canvas.NewLine(colorWavePos)
	w.posLine.StrokeWidth = 2
	w.posLine.Hidden = true // hidden until something is loaded

	// Z-order: axis → glow → sharp wave → position cursor on top.
	w.objects = []fyne.CanvasObject{w.midLine}
	for _, l := range w.glowTop {
		w.objects = append(w.objects, l)
	}
	for _, l := range w.glowBot {
		w.objects = append(w.objects, l)
	}
	for _, l := range w.sharpTop {
		w.objects = append(w.objects, l)
	}
	for _, l := range w.sharpBot {
		w.objects = append(w.objects, l)
	}
	w.objects = append(w.objects, w.posLine)

	w.ExtendBaseWidget(w)
	return w
}

func makeLines(count int, col color.Color, stroke float32) []*canvas.Line {
	out := make([]*canvas.Line, count)
	for i := range out {
		l := canvas.NewLine(col)
		l.StrokeWidth = stroke
		out[i] = l
	}
	return out
}

// SetValues feeds the latest waveform envelope (length must equal n, values
// in [0, 1]). Call from the UI goroutine.
func (w *waveformWidget) SetValues(values []float32) {
	if len(values) != w.n {
		return
	}
	copy(w.target, values)
	for i := range w.display {
		w.display[i] += (w.target[i] - w.display[i]) * w.smoothing
	}
	w.layout(w.Size())
	canvas.Refresh(w)
}

// layout positions every line based on current size and the smoothed display
// envelope. Pure mutation — no allocations.
func (w *waveformWidget) layout(size fyne.Size) {
	if size.Width <= 0 || size.Height <= 0 {
		return
	}
	mid := size.Height / 2
	stepX := size.Width / float32(w.n-1)
	// Reserve a small margin so the wave never touches the top/bottom edges.
	const margin = float32(3)
	maxAmp := mid - margin
	if maxAmp < 1 {
		maxAmp = 1
	}

	// Axis line spans the full width at mid-height.
	w.midLine.Position1 = fyne.NewPos(0, mid)
	w.midLine.Position2 = fyne.NewPos(size.Width, mid)

	// Position cursor — vertical line at the current playback fraction.
	if !w.posLine.Hidden {
		x := w.posFrac * size.Width
		w.posLine.Position1 = fyne.NewPos(x, 0)
		w.posLine.Position2 = fyne.NewPos(x, size.Height)
	}

	pointAt := func(i int) (float32, float32, float32) {
		x := float32(i) * stepX
		// Slight curve at the edges: fade amplitude near the ends so the wave
		// doesn't clip against the container boundary.
		envEdge := edgeFade(i, w.n)
		amp := w.display[i] * envEdge * maxAmp
		return x, mid - amp, mid + amp
	}

	for i := 0; i < w.n-1; i++ {
		x1, yTop1, yBot1 := pointAt(i)
		x2, yTop2, yBot2 := pointAt(i + 1)
		w.glowTop[i].Position1 = fyne.NewPos(x1, yTop1)
		w.glowTop[i].Position2 = fyne.NewPos(x2, yTop2)
		w.glowBot[i].Position1 = fyne.NewPos(x1, yBot1)
		w.glowBot[i].Position2 = fyne.NewPos(x2, yBot2)
		w.sharpTop[i].Position1 = fyne.NewPos(x1, yTop1)
		w.sharpTop[i].Position2 = fyne.NewPos(x2, yTop2)
		w.sharpBot[i].Position1 = fyne.NewPos(x1, yBot1)
		w.sharpBot[i].Position2 = fyne.NewPos(x2, yBot2)
	}
}

// edgeFade returns 1 in the middle of the wave and tapers to ~0 at the two
// edges over the outermost ~10% of points on each side. Visually this makes
// the wave look like it emerges from and dissolves into the background
// instead of cutting off sharply against the container.
func edgeFade(i, n int) float32 {
	taper := float32(n) * 0.1
	if taper < 1 {
		taper = 1
	}
	pos := float32(i)
	if pos < taper {
		return pos / taper
	}
	if pos > float32(n-1)-taper {
		return (float32(n-1) - pos) / taper
	}
	return 1
}

// SetAccent retints the waveform with the given hue. The glow layer uses the
// hue at ~33 % alpha; the sharp layer uses a brightened version near white so
// it stays readable against any extracted album color. Pass color.Transparent
// (or any zero-alpha color) to restore the default green accent.
func (w *waveformWidget) SetAccent(c color.NRGBA) {
	glow := colorWaveGlow
	sharp := colorWaveSharp
	if c.A != 0 {
		glow = color.NRGBA{R: c.R, G: c.G, B: c.B, A: 0x55}
		// Lift toward white so the sharp wave reads as "bright accent",
		// not "muddy mid-tone of cover".
		sharp = brighten(color.NRGBA{R: c.R, G: c.G, B: c.B, A: 0xff}, 0.55)
	}
	for _, l := range w.glowTop {
		l.StrokeColor = glow
	}
	for _, l := range w.glowBot {
		l.StrokeColor = glow
	}
	for _, l := range w.sharpTop {
		l.StrokeColor = sharp
	}
	for _, l := range w.sharpBot {
		l.StrokeColor = sharp
	}
	canvas.Refresh(w)
}

// SetPosition updates the playback-position cursor to frac (0..1).
// Pass a negative value to hide the cursor (e.g. nothing loaded).
// Must be called from the UI goroutine.
func (w *waveformWidget) SetPosition(frac float32) {
	if frac < 0 {
		w.posLine.Hidden = true
		canvas.Refresh(w)
		return
	}
	if frac > 1 {
		frac = 1
	}
	w.posFrac = frac
	w.posLine.Hidden = false
	w.layout(w.Size())
	canvas.Refresh(w)
}

// Tapped implements fyne.Tappable. A tap at position X seeks the song to
// fraction X/Width of its total duration.
func (w *waveformWidget) Tapped(ev *fyne.PointEvent) {
	if w.OnSeek == nil || w.Size().Width <= 0 {
		return
	}
	frac := float64(ev.Position.X) / float64(w.Size().Width)
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	w.OnSeek(frac)
}

func (w *waveformWidget) CreateRenderer() fyne.WidgetRenderer {
	return &waveformRenderer{w: w}
}

type waveformRenderer struct{ w *waveformWidget }

func (r *waveformRenderer) Layout(size fyne.Size)        { r.w.layout(size) }
func (r *waveformRenderer) MinSize() fyne.Size           { return fyne.NewSize(220, 64) }
func (r *waveformRenderer) Objects() []fyne.CanvasObject { return r.w.objects }
func (r *waveformRenderer) Refresh()                     { r.w.layout(r.w.Size()) }
func (r *waveformRenderer) Destroy()                     {}
