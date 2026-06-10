// ── Visualizer: spring-physics spectrum bars + waveform overlay ─────────────
//
// Architecture:
//   Go pushes "viz-data" events (~30 fps, only while playing) with spectrum +
//   waveform in a single payload — no per-frame IPC polling. The rAF loop runs
//   at display refresh rate (60 fps) while there is anything to animate:
//   every frame it steps springs → draws. Springs decouple the draw rate from
//   the push rate — smooth even though data arrives at 30 fps. Spectrum (FFT)
//   data reacts much faster than peak envelope, making bars feel musically
//   alive.
//
//   When playback stops, the springs wind down to zero over a few frames and
//   the loop then STOPS entirely (no rAF, no CSS-var churn → near-zero idle
//   CPU). Any wake-worthy change (play, new data, accent target, mode switch,
//   canvas resize/swap) restarts it via wake().
//
// Also owns the accent palette tween: the rAF loop eases the current accent
// rgb toward the track's target colour and re-publishes the CSS vars each
// frame, so switching tracks fades the ambience instead of snapping — even
// while paused/stopped (the accent change wakes the loop for the fade, then
// the loop settles and stops again).
import { s, d } from './stores.svelte'

// ── CSS variable sync (rgba variants for glow effects) ───────────────────────
// WCAG relative luminance — used to pick a foreground (black/white) that stays
// legible on top of the accent (e.g. the play-button glyph), whatever colour
// the album art yields. A fixed black glyph vanished on dark accents.
function relLuminance(r: number, g: number, b: number): number {
  const f = (c: number) => { c /= 255; return c <= 0.03928 ? c/12.92 : Math.pow((c+0.055)/1.055, 2.4) }
  return 0.2126*f(r) + 0.7152*f(g) + 0.0722*f(b)
}
// The whole palette glides between tracks: accentColor sets a TARGET rgb and
// the rAF loop eases accentCur toward it, re-publishing the CSS vars each
// frame. So switching tracks fades the ambience instead of snapping.
let accentCur    = [29, 185, 84]
let accentTarget = [29, 185, 84]
function applyAccentVars(r: number, g: number, b: number) {
  const L  = relLuminance(r, g, b)
  const fg = (L + 0.05) / 0.05 >= 1.05 / (L + 0.05) ? '#0a0a0c' : '#ffffff'
  const st = document.documentElement.style
  st.setProperty('--accent',     `rgb(${r},${g},${b})`)
  st.setProperty('--accent-rgb', `${r},${g},${b}`)
  st.setProperty('--accent-fg',  fg)
  st.setProperty('--accent-08',  `rgba(${r},${g},${b},0.08)`)
  st.setProperty('--accent-15',  `rgba(${r},${g},${b},0.15)`)
  st.setProperty('--accent-30',  `rgba(${r},${g},${b},0.30)`)
  st.setProperty('--accent-50',  `rgba(${r},${g},${b},0.50)`)
  cachedRgb = `${r},${g},${b}`
}

// Module-scope spring state (not $state — plain arrays, no Svelte tracking)
const N_BARS = 32
const N_WAVE = 48
const barPos  = new Float32Array(N_BARS).fill(0)
const barVel  = new Float32Array(N_BARS).fill(0)
const wavePos = new Float32Array(N_WAVE).fill(0)
const waveVel = new Float32Array(N_WAVE).fill(0)
let   latestSpectrum: number[] = []
let   latestWaveform: number[] = []
let   cachedRgb = '29,185,84'
let   vizMode: 'bars'|'wave'|'radial' = 'bars'   // module-scope mirror of visualizerMode

function stepSprings(pos: Float32Array, vel: Float32Array, src: number[], k: number, dmp: number) {
  const n = pos.length
  const sn = src.length
  for (let i = 0; i < n; i++) {
    const t = sn ? (src[Math.round(i * (sn-1) / (n-1))] ?? 0) : 0
    vel[i] = vel[i] * dmp + (t - pos[i]) * k
    pos[i] = Math.max(0, pos[i] + vel[i])
  }
}

// drawFrame dispatches on the selected visualizer mode:
//   bars   → centre-mirrored spectrum bars + waveform overlay (original look)
//   wave   → waveform overlay only, taller amplitude
//   radial → spectrum laid out around a ring (great in Now Playing)
function drawFrame(ctx: CanvasRenderingContext2D, W: number, H: number) {
  const rgb = cachedRgb
  ctx.clearRect(0, 0, W, H)
  if (vizMode === 'radial') { drawRadial(ctx, W, H, rgb); return }
  if (vizMode === 'bars')   drawBars(ctx, W, H, rgb)
  drawWave(ctx, W, H, rgb, vizMode === 'wave')
}

function drawBars(ctx: CanvasRenderingContext2D, W: number, H: number, rgb: string) {
  const cy  = H / 2
  const gap = 1.5
  const bW  = (W - gap * (N_BARS + 1)) / N_BARS
  for (let i = 0; i < N_BARS; i++) {
    const amp  = Math.min(1.15, barPos[i])
    const edge = Math.min(1, Math.min(i, N_BARS-1-i) / (N_BARS * 0.1))
    const h    = Math.max(1, amp * cy * 0.9 * edge)
    const x    = gap + i * (bW + gap)
    const r    = Math.min(bW / 2 - 0.5, 4)

    const g  = ctx.createLinearGradient(0, cy - h, 0, cy + h)
    const a  = (0.35 + amp * 0.65).toFixed(2)
    const aD = (parseFloat(a) * 0.25).toFixed(2)
    g.addColorStop(0,    `rgba(${rgb},${a})`)
    g.addColorStop(0.35, `rgba(${rgb},${aD})`)
    g.addColorStop(0.5,  `rgba(${rgb},0.02)`)
    g.addColorStop(0.65, `rgba(${rgb},${aD})`)
    g.addColorStop(1,    `rgba(${rgb},${a})`)

    ctx.beginPath()
    ctx.roundRect(x, cy - h, bW, h * 2, r)
    ctx.fillStyle = g
    ctx.fill()
  }
}

function drawWave(ctx: CanvasRenderingContext2D, W: number, H: number, rgb: string, big: boolean) {
  // No data (stopped/paused): keep drawing while the springs wind down so the
  // wave settles instead of vanishing, but skip once flat (avoids a bare
  // centre line and lets the idle loop stop).
  if (!latestWaveform.length) {
    let live = false
    for (let i = 0; i < N_WAVE; i++) if (wavePos[i] > 0.004) { live = true; break }
    if (!live) return
  }
  const cy   = H / 2
  const step = W / (N_WAVE - 1)
  const amp  = big ? 0.94 : 0.72   // taller in dedicated wave mode

  const drawLine = (sign: number, alpha: string, width: number) => {
    ctx.beginPath()
    for (let i = 0; i < N_WAVE; i++) {
      const edge = Math.min(1, Math.min(i, N_WAVE-1-i) / (N_WAVE * 0.07))
      const y    = cy - wavePos[i] * cy * amp * edge * sign
      if (i === 0) {
        ctx.moveTo(0, y)
      } else {
        const px = (i - 1) * step
        const pe = Math.min(1, Math.min(i-1, N_WAVE-2-i) / (N_WAVE * 0.07))
        const py = cy - wavePos[i-1] * cy * amp * pe * sign
        ctx.quadraticCurveTo(px, py, (px + i * step) / 2, (py + y) / 2)
        ctx.lineTo(i * step, y)
      }
    }
    ctx.strokeStyle = `rgba(${rgb},${alpha})`
    ctx.lineWidth = width
    ctx.lineJoin = 'round'
    ctx.lineCap  = 'round'
    ctx.stroke()
  }

  ctx.save(); ctx.filter = 'blur(3px)'
  drawLine( 1, '0.35', big ? 5 : 4)
  drawLine(-1, '0.35', big ? 5 : 4)
  ctx.restore()
  drawLine( 1, '0.85', big ? 2 : 1.5)
  drawLine(-1, '0.85', big ? 2 : 1.5)
}

function drawRadial(ctx: CanvasRenderingContext2D, W: number, H: number, rgb: string) {
  const cx = W / 2, cy = H / 2
  const baseR = Math.min(W, H) * 0.22
  const maxL  = Math.min(W, H) * 0.26

  // faint inner ring
  ctx.beginPath(); ctx.arc(cx, cy, baseR * 0.92, 0, Math.PI * 2)
  ctx.strokeStyle = `rgba(${rgb},0.14)`; ctx.lineWidth = 1; ctx.stroke()

  ctx.save(); ctx.filter = 'blur(2px)'
  for (let pass = 0; pass < 2; pass++) {
    const glow = pass === 0
    for (let i = 0; i < N_BARS; i++) {
      const amp = Math.min(1.3, barPos[i])
      const ang = (i / N_BARS) * Math.PI * 2 - Math.PI / 2
      const len = Math.max(2, amp * maxL)
      const x1 = cx + Math.cos(ang) * baseR
      const y1 = cy + Math.sin(ang) * baseR
      const x2 = cx + Math.cos(ang) * (baseR + len)
      const y2 = cy + Math.sin(ang) * (baseR + len)
      ctx.beginPath(); ctx.moveTo(x1, y1); ctx.lineTo(x2, y2)
      ctx.strokeStyle = `rgba(${rgb},${glow ? (0.25 + amp*0.3).toFixed(2) : (0.5 + amp*0.5).toFixed(2)})`
      ctx.lineWidth = glow ? 5 : 2.5
      ctx.lineCap = 'round'
      ctx.stroke()
    }
    if (glow) ctx.restore()   // sharp pass runs unfiltered
  }
}

// ── rAF loop (self-stopping when idle) ───────────────────────────────────────
let accentInit = false   // false until the first accent publish
let beatEnergy = 0       // smoothed low-band energy → drives --beat
const reduceMotion = typeof window !== 'undefined'
  && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches

let rafId      = 0
let rafActive  = false   // a frame is scheduled
let vizDisposed = true   // true until initViz mounts (and after cleanup)
let playing    = false   // module mirror of d.isPlaying

// wake schedules a frame if the loop is currently stopped. Safe to call from
// anywhere, any number of times.
function wake() {
  if (vizDisposed || rafActive) return
  rafActive = true
  rafId = requestAnimationFrame(loop)
}

// setVizData receives the pushed "viz-data" payload (wired in App.svelte).
// Data only arrives while playing — the Go ticker is gated on engine state —
// but a last in-flight emit can land just AFTER the pause/stop state-change.
// Ignore it then: stale data would pin the springs above zero and keep the
// idle loop alive. (s.playerState is read live, so this check is in sync with
// the event handler that just ran, not the effect-flushed mirror.)
export function setVizData(data: { spectrum: number[] | null; waveform: number[] | null } | null) {
  if (s.playerState !== 'Playing') return
  latestSpectrum = data?.spectrum ?? []
  latestWaveform = data?.waveform ?? []
  wake()
}

// True when every spring is at rest near zero — nothing left to animate.
function springsSettled(): boolean {
  for (let i = 0; i < N_BARS; i++) if (barPos[i] > 0.002 || Math.abs(barVel[i]) > 0.002) return false
  for (let i = 0; i < N_WAVE; i++) if (wavePos[i] > 0.002 || Math.abs(waveVel[i]) > 0.002) return false
  return true
}

function loop() {
  if (vizDisposed) { rafActive = false; return }

  stepSprings(barPos,  barVel,  latestSpectrum, 0.22, 0.60)
  stepSprings(wavePos, waveVel, latestWaveform, 0.16, 0.68)

  // Ease the accent palette toward the current track's target colour, then
  // republish the CSS vars. Only when it actually moved (idle = no churn).
  let accentMoved = false
  for (let i = 0; i < 3; i++) {
    const delta = accentTarget[i] - accentCur[i]
    if (Math.abs(delta) > 0.4) { accentCur[i] += delta * 0.08; accentMoved = true }
    else accentCur[i] = accentTarget[i]
  }
  if (accentMoved || !accentInit) {
    accentInit = true
    applyAccentVars(Math.round(accentCur[0]), Math.round(accentCur[1]), Math.round(accentCur[2]))
  }

  // Beat energy from the low spectrum bands → pulses the album-art glow.
  // Skipped under prefers-reduced-motion (it's a JS-driven transform the
  // CSS media query can't reach).
  let beatActive = false
  if (!reduceMotion) {
    let lo = 0; for (let i = 0; i < 6; i++) lo += barPos[i]
    beatEnergy += (Math.min(1, lo / 6) - beatEnergy) * 0.25
    if (beatEnergy < 0.002) beatEnergy = 0          // snap so idle can stop
    document.documentElement.style.setProperty('--beat', beatEnergy.toFixed(3))
    beatActive = beatEnergy > 0
  }

  // Draw to whichever visualizer surface is on screen. The Now Playing
  // canvas, when open, takes over (it's larger / more prominent).
  // The backing store is DPR-scaled (see syncCanvasSize); the transform maps
  // the existing CSS-pixel draw code onto it for crisp HiDPI output.
  const target = (s.showNowPlaying && s.npCanvasEl) ? s.npCanvasEl : s.canvasEl
  if (target) {
    const ctx = target.getContext('2d')
    if (ctx) {
      const dpr = window.devicePixelRatio || 1
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
      drawFrame(ctx, target.width / dpr, target.height / dpr)
    }
  }

  // Keep running while playing or while anything is still settling. Once
  // fully at rest, stop — wake() restarts on the next relevant change.
  if (playing || accentMoved || beatActive || !springsSettled()) {
    rafId = requestAnimationFrame(loop)
  } else {
    rafActive = false
  }
}

// ── HiDPI canvas sizing ───────────────────────────────────────────────────────
// Size the backing store to CSS size × devicePixelRatio. Layout size stays
// CSS-controlled (width:100% etc.) — only the pixel buffer changes.
function syncCanvasSize(c: HTMLCanvasElement) {
  const dpr = window.devicePixelRatio || 1
  const w = Math.max(1, Math.round(c.clientWidth  * dpr))
  const h = Math.max(1, Math.round(c.clientHeight * dpr))
  if (c.width !== w || c.height !== h) { c.width = w; c.height = h }
}

// Must be called during component initialisation (App.svelte) — sets up the
// reactive bridges and the rAF loop as effects tied to the app's lifetime.
export function initViz() {
  $effect(() => {
    const hex = d.accentColor.replace('#','')
    accentTarget = [parseInt(hex.slice(0,2),16), parseInt(hex.slice(2,4),16), parseInt(hex.slice(4,6),16)]
    // Track changed while paused/stopped? Still fade the ambience: run the
    // loop until the accent settles, then it stops again on its own.
    wake()
  })

  // cachedRgb is published by applyAccentVars() from the eased accent in
  // the rAF loop. Bridge only the reactive visualizer mode into that loop.
  $effect(() => { vizMode = s.visualizerMode; wake() })

  // Playback gate: while not playing, clear the data targets so the springs
  // wind down to zero (the loop draws the settle, then stops itself).
  $effect(() => {
    playing = d.isPlaying
    if (!playing) { latestSpectrum = []; latestWaveform = [] }
    wake()
  })

  // HiDPI: keep both canvases' backing stores at CSS-size × DPR. Re-runs when
  // either canvas element (re)mounts (immersive open/close swaps the surface);
  // the ResizeObserver covers window/layout resizes; the resolution media
  // query catches DPR changes (monitor move / zoom) at constant CSS size.
  $effect(() => {
    const els = [s.canvasEl, s.npCanvasEl].filter((c): c is HTMLCanvasElement => !!c)
    if (!els.length) return
    const apply = () => { for (const el of els) syncCanvasSize(el); wake() }
    apply()
    const ro = new ResizeObserver(apply)
    for (const el of els) ro.observe(el)

    let mq: MediaQueryList | null = null
    const onDpr = () => { apply(); listenDpr() }   // re-arm at the new DPR
    const listenDpr = () => {
      mq?.removeEventListener('change', onDpr)
      mq = window.matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`)
      mq.addEventListener('change', onDpr)
    }
    listenDpr()
    return () => { ro.disconnect(); mq?.removeEventListener('change', onDpr) }
  })

  // Loop lifetime: alive while the app component is mounted.
  $effect(() => {
    vizDisposed = false
    wake()
    return () => {
      vizDisposed = true
      rafActive = false
      cancelAnimationFrame(rafId)
    }
  })
}
