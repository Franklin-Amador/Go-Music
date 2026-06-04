<script lang="ts">
  import { EventsOn } from '../wailsjs/runtime/runtime.js'
  import * as Backend from '../wailsjs/go/main/App.js'

  interface TrackInfo {
    path: string; title: string; artist: string; album: string
    format: string; qualityLabel: string; dsdLabel: string
    duration: number; isDSD: boolean; hasArt: boolean
    pictureMIME: string; outputMode: string
  }

  // ── reactive state (Svelte 5 runes) ────────────────────────────────────────
  let track    = $state<TrackInfo | null>(null)
  let state    = $state('Stopped')
  let pos      = $state(0)
  let dur      = $state(0)
  let volume   = $state(1.0)
  let errorMsg = $state('')
  let loading  = $state(false)

  const posStr = $derived(fmtTime(pos))
  const durStr = $derived(fmtTime(dur))
  const isPlaying = $derived(state === 'Playing')
  const isPaused  = $derived(state === 'Paused')

  // ── Wails event subscriptions ───────────────────────────────────────────────
  $effect(() => {
    const off1 = EventsOn('state-change',    (s: string) => { state = s })
    const off2 = EventsOn('position-change', (p: { pos: number; dur: number }) => {
      pos = p.pos
      dur = p.dur
    })
    const off3 = EventsOn('audio-error',     (msg: string) => { errorMsg = msg; loading = false })
    const off4 = EventsOn('playback-finished', () => { state = 'Stopped'; pos = 0 })
    const off5 = EventsOn('track-change',    (info: TrackInfo) => { track = info; loading = false })

    return () => { off1(); off2(); off3(); off4(); off5() }
  })

  // ── handlers ────────────────────────────────────────────────────────────────
  async function openAndLoad() {
    const path = await Backend.OpenFileDialog()
    if (!path) return
    errorMsg = ''
    loading = true
    try {
      track = await Backend.LoadFile(path)
    } catch (e: any) {
      errorMsg = String(e)
    } finally {
      loading = false
    }
  }

  async function play() {
    try { await Backend.Play() } catch (e: any) { errorMsg = String(e) }
  }

  function pause()  { Backend.Pause() }
  function stop()   { Backend.Stop() }

  function onVolumeChange(e: Event) {
    volume = parseFloat((e.target as HTMLInputElement).value)
    Backend.SetVolume(volume)
  }

  function fmtTime(s: number): string {
    const m   = Math.floor(s / 60)
    const sec = Math.floor(s % 60)
    return `${m}:${sec.toString().padStart(2, '0')}`
  }
</script>

<main>
  <!-- header -->
  <div class="header">
    <span class="app-name">Go Music</span>
    {#if track?.outputMode}
      <span class="output-mode" class:exclusive={track.outputMode === 'EXCLUSIVE'}>
        {track.outputMode}
      </span>
    {/if}
  </div>

  <!-- track info -->
  <div class="track-info">
    {#if loading}
      <p class="loading">Loading…</p>
    {:else if track}
      <p class="title">{track.title || track.path.split('\\').at(-1)}</p>
      {#if track.artist}<p class="artist">{track.artist}</p>{/if}
      {#if track.album}<p class="album">{track.album}</p>{/if}
      <p class="meta">
        {track.qualityLabel || track.format}
        {#if track.isDSD && track.dsdLabel}<span class="dsd">{track.dsdLabel}</span>{/if}
      </p>
    {:else}
      <p class="empty">No track loaded — click Open File to start</p>
    {/if}
  </div>

  <!-- position -->
  {#if dur > 0}
    <div class="progress-row">
      <span class="time">{posStr}</span>
      <div class="progress-bar">
        <div class="fill" style="width: {(pos / dur) * 100}%"></div>
      </div>
      <span class="time">{durStr}</span>
    </div>
  {/if}

  <!-- transport -->
  <div class="transport">
    <button onclick={openAndLoad} class="btn btn-open">Open File</button>
    {#if isPlaying}
      <button onclick={pause} class="btn btn-icon">⏸ Pause</button>
    {:else}
      <button onclick={play}  class="btn btn-icon" disabled={!track}>▶ Play</button>
    {/if}
    <button onclick={stop} class="btn btn-icon" disabled={!track && !isPlaying && !isPaused}>⏹ Stop</button>
  </div>

  <!-- volume -->
  <div class="volume-row">
    <span class="vol-label">Vol</span>
    <input
      type="range"
      min="0" max="1" step="0.01"
      value={volume}
      oninput={onVolumeChange}
      class="volume-slider"
    />
    <span class="vol-pct">{Math.round(volume * 100)}%</span>
  </div>

  <!-- error -->
  {#if errorMsg}
    <div class="error">{errorMsg}</div>
  {/if}
</main>

<style>
  :root {
    --bg:     #0c0c0e;
    --surface: #17171a;
    --border:  #2a2a30;
    --accent:  #1db954;
    --text:    #e8e8ec;
    --muted:   #7a7a8a;
    --dsd:     #f5a623;
    --excl:    #1db954;
    --radius:  8px;
  }

  main {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1.2rem;
    min-height: 100vh;
    padding: 2rem 1.5rem;
    box-sizing: border-box;
    background: var(--bg);
    color: var(--text);
    font-family: system-ui, -apple-system, "Segoe UI", sans-serif;
    -webkit-font-smoothing: antialiased;
  }

  .header {
    display: flex;
    align-items: center;
    gap: 1rem;
    width: 100%;
    max-width: 600px;
  }

  .app-name {
    font-size: 1.1rem;
    font-weight: 700;
    letter-spacing: 0.08em;
    color: var(--accent);
    text-transform: uppercase;
  }

  .output-mode {
    font-size: 0.65rem;
    font-weight: 700;
    letter-spacing: 0.1em;
    padding: 2px 7px;
    border-radius: 4px;
    background: var(--surface);
    color: var(--muted);
    border: 1px solid var(--border);
  }

  .output-mode.exclusive {
    color: var(--excl);
    border-color: var(--excl);
  }

  .track-info {
    width: 100%;
    max-width: 600px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 1.25rem 1.5rem;
    min-height: 7rem;
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 0.25rem;
  }

  .title   { font-size: 1.15rem; font-weight: 600; margin: 0; }
  .artist  { font-size: 0.9rem; color: var(--muted); margin: 0; }
  .album   { font-size: 0.85rem; color: var(--muted); margin: 0; }
  .meta    { font-size: 0.75rem; color: var(--muted); margin: 0.25rem 0 0; }
  .empty   { color: var(--muted); font-size: 0.9rem; margin: 0; }
  .loading { color: var(--accent); font-size: 0.9rem; margin: 0; }

  .dsd {
    margin-left: 0.5rem;
    color: var(--dsd);
    font-weight: 600;
  }

  /* progress */
  .progress-row {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    width: 100%;
    max-width: 600px;
  }

  .time {
    font-size: 0.75rem;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
    min-width: 2.8rem;
  }

  .progress-bar {
    flex: 1;
    height: 4px;
    background: var(--border);
    border-radius: 2px;
    overflow: hidden;
  }

  .fill {
    height: 100%;
    background: var(--accent);
    border-radius: 2px;
    transition: width 0.25s linear;
  }

  /* transport */
  .transport {
    display: flex;
    gap: 0.75rem;
    align-items: center;
  }

  .btn {
    border: none;
    border-radius: var(--radius);
    padding: 0.55rem 1.1rem;
    font-size: 0.85rem;
    font-weight: 600;
    cursor: pointer;
    transition: background 0.15s, opacity 0.15s;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
  }

  .btn:hover:not(:disabled) {
    background: var(--border);
  }

  .btn:disabled {
    opacity: 0.35;
    cursor: not-allowed;
  }

  .btn-open {
    background: var(--accent);
    color: #000;
    border-color: var(--accent);
  }

  .btn-open:hover {
    background: #17a347;
    border-color: #17a347;
  }

  /* volume */
  .volume-row {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    width: 100%;
    max-width: 600px;
  }

  .vol-label {
    font-size: 0.75rem;
    color: var(--muted);
    min-width: 1.8rem;
  }

  .vol-pct {
    font-size: 0.75rem;
    color: var(--muted);
    min-width: 2.5rem;
    text-align: right;
    font-variant-numeric: tabular-nums;
  }

  .volume-slider {
    flex: 1;
    accent-color: var(--accent);
    cursor: pointer;
  }

  /* error */
  .error {
    width: 100%;
    max-width: 600px;
    background: #3a1010;
    border: 1px solid #7a2020;
    border-radius: var(--radius);
    padding: 0.6rem 1rem;
    font-size: 0.8rem;
    color: #f08080;
  }
</style>
