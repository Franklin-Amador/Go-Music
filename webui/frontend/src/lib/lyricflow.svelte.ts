// ── Lyric flow engine: continuous, transform-only synced-lyric motion ───────
//
// The old renderer was stepped at every level: the engine reports position
// only 4×/s, line distances were DISCRETE buckets, font-size was animated
// (reflow) and scrollIntoView lurched once per line. This module replaces all
// of that with one self-stopping rAF loop (same lifecycle pattern as
// viz.svelte.ts):
//
//   livePos()    extrapolates the playback clock between position events
//                (frozen while paused/stopped; while scrubbing it follows the
//                optimistic seek preview exactly, so it never fights the
//                commit-on-release logic in player.ts).
//   activeFloat  fractional line index — active idx + progress through the
//                line — so every visual property is a continuous function of
//                (i − activeFloat) and the whole column DRIFTS, never steps.
//   styling      direct el.style writes on a ±6-line window around the active
//                line (the falloff is flat beyond distance 4, so lines outside
//                the window are set ONCE to the far style). Font-size is fixed
//                per surface and the size hierarchy is pure transform: scale(),
//                so no frame ever causes layout.
//   glide        a single eased float (dispFloat) tracks the DISCRETE active
//                line — it holds steady through instrumental gaps and eases
//                over ~0.3 s only when the sung line changes (no slow drift,
//                no progress fill). Drives both the per-line styling and the
//                scroll position; a screenful jump (seek / re-show) snaps.
//   highlight    the active line's <span> is set to solid var(--accent) on
//                handover; a CSS colour transition cross-fades it softly. The
//                accent glow is a filter: drop-shadow on the <p>.
//
// Surfaces (side panel / immersive) register via registerLyricLines() from
// LyricLines.svelte; only the one actually on screen is animated each frame.
// Under prefers-reduced-motion the engine stays fully inert — LyricLines
// keeps the static bucket renderer and App.svelte keeps jump scrolling.
import { s } from './stores.svelte'

export const reduceMotion = typeof window !== 'undefined'
  && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches

// ── interpolated playback clock ───────────────────────────────────────────────
// The engine emits position-change every ~250 ms; stamping each event lets the
// loop extrapolate a smooth clock in between.
let clockPos   = 0
let clockStamp = 0

function livePos(now: number): number {
  if (s.seekPreview !== null) return s.seekPreview
  if (s.playerState !== 'Playing') return s.pos
  const p = clockPos + (now - clockStamp) / 1000
  return s.dur > 0 ? Math.min(p, s.dur) : p
}

// livePosNow exposes the interpolated clock for one-shot consumers (the
// tap-to-sync lyric calibration needs sub-250ms precision, not the raw tick).
export function livePosNow(): number {
  return livePos(performance.now())
}

// ── visual curves ─────────────────────────────────────────────────────────────
// Same tier values as the old bucket renderer at integer distances (a paused
// song still looks like the familiar static render), linearly interpolated in
// between. The scale tiers bake in the old font-size ratios (size[d]/size[0])
// because font-size is now fixed — the size hierarchy is transform-only.
const OPAS  = [1, 0.70, 0.42, 0.22, 0.12]
const BLURS = [0, 0.6, 1.4, 2.4, 3.2]
const SCALES_BIG   = [1.040, 0.673, 0.536, 0.445, 0.389]   // 1.7rem fixed base
const SCALES_SMALL = [1.040, 0.794, 0.659, 0.573, 0.514]   // 1.10rem fixed base
const WINDOW   = 6      // lines styled per side of the active float
const EASE_TAU = 0.11   // s — exponential time constant for the line-to-line glide
const HOLD_MS  = 3000   // wheel-interrupt: auto-follow pause
const GLOW = 'drop-shadow(0 0 14px rgba(var(--accent-rgb),0.45)) drop-shadow(0 0 38px rgba(var(--accent-rgb),0.18))'

function tier(arr: number[], dist: number): number {
  if (dist >= 4) return arr[4]
  const i = Math.floor(dist)
  return arr[i] + (arr[i + 1] - arr[i]) * (dist - i)
}

// ── surfaces ──────────────────────────────────────────────────────────────────
interface Surface {
  big: boolean
  els: HTMLElement[]
  container: HTMLElement
  times: number[]      // per-line timestamp (s), -1 = untimed
  timed: number[]      // indices of timed lines, ascending (LRC order)
  centers: number[]    // line centres in scroll coordinates (cached)
  viewH: number
  maxScroll: number
  dispFloat: number    // eased displayed line index (NaN until first frame)
  holdUntil: number    // wheel-interrupt deadline (performance.now ms)
  winLo: number
  winHi: number        // last styled window ([1,0] = empty)
  lastFloat: number
  activeIdx: number    // currently-highlighted line index (-1 = none)
  activeEl: HTMLElement | null
}

const surfaces = new Map<boolean, Surface>()   // keyed by `big`

// Geometry is read once per lyrics change / resize — NEVER per frame. Scale
// has transform-origin center, so each line's centre is invariant while it
// animates; the cache stays valid even when measured mid-motion.
function measure(sf: Surface) {
  const base = sf.container.getBoundingClientRect().top - sf.container.scrollTop
  sf.viewH = sf.container.clientHeight
  sf.centers = sf.els.map(el => {
    const r = el.getBoundingClientRect()
    return r.top - base + r.height / 2
  })
  sf.maxScroll = Math.max(0, sf.container.scrollHeight - sf.viewH)
}

// ── per-line style writers ────────────────────────────────────────────────────
// The accent colour lives on the inner <span> (CSS gives it a 0.28 s colour
// transition, so the highlight cross-fades softly when the sung line changes —
// no hard snap, and no progress "fill").
function colourTarget(el: HTMLElement): HTMLElement {
  return (el.firstElementChild as HTMLElement) ?? el
}
function activate(el: HTMLElement) {
  colourTarget(el).style.color = 'var(--accent)'
}
function deactivate(el: HTMLElement) {
  colourTarget(el).style.color = ''
}
function applyFar(el: HTMLElement, farScale: number) {
  el.style.opacity = '0.12'
  el.style.transform = `scale(${farScale})`
  el.style.filter = 'blur(3.2px)'
}

// ── position → line space ─────────────────────────────────────────────────────
// idx    = the line currently sung (-1 before the first timestamp)
// target = the float index the view eases toward. It's the active line itself,
//          so the display HOLDS STEADY on a line through an instrumental gap
//          (no slow drift), then glides one step when the next line begins.
//          Before the first lyric it parks on the first line.
function flowAt(sf: Surface, pos: number): { idx: number; target: number } {
  const { times, timed } = sf
  if (!timed.length) return { idx: -1, target: 0 }
  let lo = 0, hi = timed.length - 1, k = -1
  while (lo <= hi) {
    const m = (lo + hi) >> 1
    if (times[timed[m]] <= pos) { k = m; lo = m + 1 } else { hi = m - 1 }
  }
  if (k < 0) return { idx: -1, target: timed[0] }   // intro: park on the first line
  return { idx: timed[k], target: timed[k] }
}

function centerAt(sf: Surface, f: number): number {
  const c = sf.centers
  if (!c.length) return 0
  if (f <= 0) return c[0]
  const n = c.length - 1
  if (f >= n) return c[n]
  const i = Math.floor(f)
  return c[i] + (c[i + 1] - c[i]) * (f - i)
}

// ── per-surface frame step ────────────────────────────────────────────────────
// Returns true when there is nothing left to animate (styles match the float,
// spring at rest, no wheel hold pending).
function stepSurface(sf: Surface, pos: number, now: number, dt: number): boolean {
  const { idx, target } = flowAt(sf, pos)
  const scales = sf.big ? SCALES_BIG : SCALES_SMALL
  let settled = true

  // ── glide the displayed float toward the active line ──
  // Holds rock-steady through instrumental breaks (target doesn't move), then
  // eases over ~0.3 s when the sung line changes. A big gap (seek / first
  // frame / surface re-shown) snaps instead of crawling.
  let float = sf.dispFloat
  if (!Number.isFinite(float) || Math.abs(target - float) > WINDOW) {
    float = target
  } else if (Math.abs(target - float) > 1e-3) {
    float += (target - float) * (1 - Math.exp(-dt / EASE_TAU))
    settled = false
  } else {
    float = target
  }
  sf.dispFloat = float

  // ── window styling — only when the float moved or the active line changed ──
  if (Math.abs(float - sf.lastFloat) > 1e-4 || idx !== sf.activeIdx) {
    const lo = Math.max(0, Math.round(float) - WINDOW)
    const hi = Math.min(sf.els.length - 1, Math.round(float) + WINDOW)
    // lines that left the window return to the static far style
    for (let i = sf.winLo; i <= sf.winHi; i++)
      if (i < lo || i > hi) applyFar(sf.els[i], scales[4])
    // active-line handover — solid accent; CSS cross-fades the colour
    const active = idx >= 0 ? sf.els[idx] : null
    if (active !== sf.activeEl) {
      if (sf.activeEl) deactivate(sf.activeEl)
      if (active) activate(active)
      sf.activeEl = active
    }
    for (let i = lo; i <= hi; i++) {
      const el = sf.els[i]
      const dist = Math.abs(i - float)
      el.style.opacity = tier(OPAS, dist).toFixed(3)
      el.style.transform = `scale(${tier(scales, dist).toFixed(4)})`
      el.style.filter = el === active ? GLOW : `blur(${tier(BLURS, dist).toFixed(2)}px)`
    }
    sf.winLo = lo; sf.winHi = hi
    sf.lastFloat = float
    sf.activeIdx = idx
  }

  // ── scroll: follow the eased float; yield while the user is wheel-browsing ──
  if (now < sf.holdUntil) return false
  const targetTop = Math.min(sf.maxScroll, Math.max(0, centerAt(sf, float) - sf.viewH / 2))
  if (Math.abs(targetTop - sf.container.scrollTop) > 0.5) {
    sf.container.scrollTop = targetTop
    settled = false
  }
  return settled
}

// ── rAF loop (self-stopping when idle) ────────────────────────────────────────
let rafId     = 0
let rafActive = false
let disposed  = true
let lastFrame = 0

// wake schedules a frame if the loop is currently stopped. Safe to call from
// anywhere, any number of times.
function wake() {
  if (disposed || rafActive) return
  rafActive = true
  lastFrame = performance.now()
  rafId = requestAnimationFrame(loop)
}

function loop(now: number) {
  if (disposed) { rafActive = false; return }
  const dt = Math.min(0.032, Math.max(0.001, (now - lastFrame) / 1000))
  lastFrame = now
  const pos = livePos(now)

  let busy = false
  let anyVisible = false
  for (const sf of surfaces.values()) {
    // Surface scoping mirrors App.svelte: the immersive overlay, when open,
    // covers the side panel — only the surface actually on screen animates.
    // A hidden one catches up on its next visible frame (lastFloat differs).
    const visible = sf.big ? s.showNowPlaying : !s.showNowPlaying
    if (!visible || sf.viewH <= 0 || !sf.timed.length) continue
    anyVisible = true
    if (!stepSurface(sf, pos, now, dt)) busy = true
  }

  // Keep running while playing or while anything is still settling. Once
  // fully at rest, stop — wake() restarts on the next relevant change.
  if (anyVisible && (s.playerState === 'Playing' || busy)) {
    rafId = requestAnimationFrame(loop)
  } else {
    rafActive = false
  }
}

// ── registration (called from LyricLines.svelte) ──────────────────────────────
// `els` are the <p class="lyric-fl"> lines in lyric order; `container` is the
// scrollable surface (.lyrics-list / .np-lyrics). Returns the cleanup for the
// caller's $effect — re-run on every lyrics change.
export function registerLyricLines(
  big: boolean, els: HTMLElement[], container: HTMLElement, times: number[],
): () => void {
  const timed: number[] = []
  for (let i = 0; i < times.length; i++) if (times[i] >= 0) timed.push(i)

  const sf: Surface = {
    big, els, container, times, timed,
    centers: [], viewH: 0, maxScroll: 0,
    dispFloat: NaN, holdUntil: 0,
    winLo: 1, winHi: 0, lastFloat: Infinity, activeIdx: -1, activeEl: null,
  }
  measure(sf)
  // Svelte reuses the <p> elements across lyric swaps — reset everything,
  // including a stale karaoke clip (transparent text would be invisible).
  const farScale = (big ? SCALES_BIG : SCALES_SMALL)[4]
  for (const el of els) { deactivate(el); applyFar(el, farScale) }

  const onWheel = () => { sf.holdUntil = performance.now() + HOLD_MS; wake() }
  container.addEventListener('wheel', onWheel, { passive: true })
  const ro = new ResizeObserver(() => { measure(sf); wake() })
  ro.observe(container)

  surfaces.set(big, sf)
  wake()
  return () => {
    container.removeEventListener('wheel', onWheel)
    ro.disconnect()
    if (surfaces.get(big) === sf) surfaces.delete(big)
  }
}

// ── lifecycle ─────────────────────────────────────────────────────────────────
// Must be called during component initialisation (App.svelte) — bridges the
// reactive world into the loop. Under prefers-reduced-motion the engine stays
// inert (LyricLines falls back to the static bucket renderer).
export function initLyricFlow() {
  if (reduceMotion) return

  // Clock: stamp every position event — and re-stamp on play/pause/stop AND on
  // track change so a resume (or a new song) never extrapolates from a stale
  // timestamp. track-change resets s.pos to 0 first, so this stamps the new
  // track at 0 instead of carrying the previous song's position forward.
  $effect(() => {
    void s.playerState
    void s.track
    clockPos = s.pos
    clockStamp = performance.now()
    wake()
  })

  // Seeks (optimistic preview set/moved/cleared) + surface visibility swaps.
  $effect(() => { void s.seekPreview; wake() })
  $effect(() => { void s.showNowPlaying; void s.showLyrics; void s.npLyricsVisible; wake() })

  // Loop lifetime: alive while the app component is mounted.
  $effect(() => {
    disposed = false
    wake()
    return () => {
      disposed = true
      rafActive = false
      cancelAnimationFrame(rafId)
    }
  })
}
