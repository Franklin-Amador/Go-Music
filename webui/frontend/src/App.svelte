<script lang="ts">
  import { EventsOn } from '../wailsjs/runtime/runtime.js'
  import * as Backend from '../wailsjs/go/main/App.js'

  // ── types ───────────────────────────────────────────────────────────────────
  interface TrackInfo {
    path: string; title: string; artist: string; album: string
    format: string; qualityLabel: string; dsdLabel: string
    duration: number; isDSD: boolean; hasArt: boolean
    artBase64: string; accentHex: string; outputMode: string
  }
  interface PlaylistTrack { index: number; path: string; title: string; current: boolean }
  interface LyricLine    { timeSec: number; text: string }

  // ── reactive state ───────────────────────────────────────────────────────────
  let track      = $state<TrackInfo | null>(null)
  let playerState = $state('Stopped')
  let pos        = $state(0)
  let dur        = $state(0)
  let volume     = $state(1.0)
  let playlist   = $state<PlaylistTrack[]>([])
  let lyrics     = $state<LyricLine[]>([])
  let waveform   = $state<number[]>([])
  let errorMsg   = $state('')
  let loading    = $state(false)
  let showLyrics = $state(false)
  let isShuffle  = $state(false)
  let isRepeat   = $state(false)

  let canvasEl   = $state<HTMLCanvasElement | null>(null)
  let playlistEl = $state<HTMLElement | null>(null)

  // ── derived ─────────────────────────────────────────────────────────────────
  const isPlaying   = $derived(playerState === 'Playing')
  const isPaused    = $derived(playerState === 'Paused')
  const posStr      = $derived(fmtTime(pos))
  const durStr      = $derived(fmtTime(dur))
  const accentColor = $derived(track?.accentHex || '#1db954')

  const activeLyricIdx = $derived((() => {
    if (!lyrics.length) return -1
    let idx = -1
    for (let i = 0; i < lyrics.length; i++) {
      if (lyrics[i].timeSec >= 0 && lyrics[i].timeSec <= pos) idx = i
    }
    return idx
  })())

  // ── CSS accent variable sync ─────────────────────────────────────────────────
  $effect(() => {
    document.documentElement.style.setProperty('--accent', accentColor)
    document.documentElement.style.setProperty('--accent-15', accentColor + '26')
    document.documentElement.style.setProperty('--accent-40', accentColor + '66')
  })

  // ── waveform canvas draw ─────────────────────────────────────────────────────
  $effect(() => {
    if (!canvasEl || !waveform.length) return
    const ctx = canvasEl.getContext('2d')
    if (!ctx) return
    const W = canvasEl.width
    const H = canvasEl.height
    const cy = H / 2
    ctx.clearRect(0, 0, W, H)

    const pts = waveform.length
    const stepX = W / pts

    // Build path
    ctx.beginPath()
    for (let i = 0; i < pts; i++) {
      const x = i * stepX
      // Edge fade: alpha at edges
      const edge = Math.min(i, pts - 1 - i) / (pts * 0.1)
      const amp = waveform[i] * Math.min(1, edge) * cy * 0.85
      if (i === 0) ctx.moveTo(x, cy - amp)
      else ctx.lineTo(x, cy - amp)
    }
    for (let i = pts - 1; i >= 0; i--) {
      const x = i * stepX
      const edge = Math.min(i, pts - 1 - i) / (pts * 0.1)
      const amp = waveform[i] * Math.min(1, edge) * cy * 0.85
      ctx.lineTo(x, cy + amp)
    }
    ctx.closePath()

    // Glow fill
    ctx.fillStyle = accentColor + '20'
    ctx.fill()

    // Glow stroke
    ctx.save()
    ctx.filter = 'blur(3px)'
    ctx.strokeStyle = accentColor + '60'
    ctx.lineWidth = 3
    ctx.stroke()
    ctx.restore()

    // Sharp stroke
    ctx.strokeStyle = accentColor + 'cc'
    ctx.lineWidth = 1.5
    ctx.stroke()
  })

  // ── auto-scroll active lyric ─────────────────────────────────────────────────
  $effect(() => {
    if (activeLyricIdx < 0 || !showLyrics) return
    const el = document.querySelector<HTMLElement>('.lyric-active')
    el?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  })

  // ── Wails event subscriptions ────────────────────────────────────────────────
  $effect(() => {
    const off: Array<() => void> = [
      EventsOn('state-change', (s: string) => {
        playerState = s
        if (s === 'Stopped') { pos = 0; loading = false }
        if (s === 'Loading')  loading = true
        if (s === 'Playing')  loading = false
      }),
      EventsOn('position-change', (p: { pos: number; dur: number }) => {
        pos = p.pos; dur = p.dur
      }),
      EventsOn('track-change', (info: TrackInfo) => {
        track = info; loading = false; errorMsg = ''
      }),
      EventsOn('playlist-updated', (pl: PlaylistTrack[]) => {
        playlist = pl
      }),
      EventsOn('lyrics', (lines: LyricLine[] | null) => {
        lyrics = lines ?? []
      }),
      EventsOn('waveform', (data: number[]) => {
        waveform = data
      }),
      EventsOn('audio-error', (msg: string) => {
        errorMsg = msg; loading = false
      }),
      EventsOn('playback-finished', () => {
        playerState = 'Stopped'; pos = 0
      }),
      EventsOn('wails:file-drop', (paths: string[]) => {
        Backend.AddFiles(paths).then(pl => { playlist = pl })
      }),
    ]
    return () => off.forEach(f => f())
  })

  // ── handlers ─────────────────────────────────────────────────────────────────
  async function openFile() {
    const path = await Backend.OpenFileDialog()
    if (!path) return
    errorMsg = ''; loading = true
    try { track = await Backend.LoadFile(path) }
    catch (e: any) { errorMsg = String(e); loading = false }
  }

  async function openFolder() {
    const dir = await Backend.OpenFolderDialog()
    if (!dir) return
    playlist = await Backend.AddFolder(dir)
  }

  async function play()  { try { await Backend.Play() } catch (e: any) { errorMsg = String(e) } }
  function pause()  { Backend.Pause() }
  function stop()   { Backend.Stop() }

  async function next() {
    loading = true
    try { const t = await Backend.Next(); if (t) track = t }
    catch (e: any) { errorMsg = String(e) }
    finally { loading = false }
  }

  async function prev() {
    loading = true
    try { const t = await Backend.Prev(); if (t) track = t }
    catch (e: any) { errorMsg = String(e) }
    finally { loading = false }
  }

  async function playAt(index: number) {
    loading = true
    try { const t = await Backend.PlayAt(index); if (t) track = t }
    catch (e: any) { errorMsg = String(e) }
    finally { loading = false }
  }

  function onProgressClick(e: MouseEvent) {
    if (!dur) return
    const bar = e.currentTarget as HTMLElement
    const rect = bar.getBoundingClientRect()
    Backend.Seek(((e.clientX - rect.left) / rect.width) * dur)
  }

  function onVolumeInput(e: Event) {
    volume = parseFloat((e.target as HTMLInputElement).value)
    Backend.SetVolume(volume)
  }

  async function toggleShuffle() {
    isShuffle = !isShuffle
    Backend.SetShuffle(isShuffle)
  }

  async function toggleRepeat() {
    isRepeat = !isRepeat
    Backend.SetRepeat(isRepeat)
  }

  function fmtTime(s: number): string {
    if (!s || s < 0) return '0:00'
    const m = Math.floor(s / 60)
    return `${m}:${Math.floor(s % 60).toString().padStart(2, '0')}`
  }
</script>

<!-- ── markup ──────────────────────────────────────────────────────────────── -->

<div class="layout" class:has-lyrics={showLyrics}>

  <!-- ── left column: player ─────────────────────────────────────────────── -->
  <section class="player">

    <!-- art -->
    <div class="art-wrap">
      {#if track?.artBase64}
        <img class="art-img" src={track.artBase64} alt="Album art" />
      {:else}
        <div class="art-placeholder">♪</div>
      {/if}
      <!-- ambient glow behind art -->
      {#if track?.artBase64}
        <img class="art-glow" src={track.artBase64} alt="" aria-hidden="true" />
      {/if}
    </div>

    <!-- track info -->
    <div class="track-info">
      {#if loading}
        <p class="loading-msg">Loading…</p>
      {:else if track}
        <p class="title">{track.title}</p>
        {#if track.artist}<p class="artist">{track.artist}</p>{/if}
        {#if track.album}<p class="album">{track.album}</p>{/if}
        <p class="meta">
          {track.qualityLabel || track.format}
          {#if track.isDSD}<span class="dsd-badge">{track.dsdLabel}</span>{/if}
          {#if track.outputMode}
            <span class="mode-badge" class:excl={track.outputMode === 'exclusive'}>
              {track.outputMode.toUpperCase()}
            </span>
          {/if}
        </p>
      {:else}
        <p class="empty-hint">Drop files here or use Open File</p>
      {/if}
    </div>

    <!-- waveform canvas -->
    <div class="waveform-wrap" role="none">
      <canvas bind:this={canvasEl} class="waveform" width="400" height="56"></canvas>
    </div>

    <!-- progress bar (clickable) -->
    <div class="progress-row">
      <span class="time">{posStr}</span>
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="progress-bar" onclick={onProgressClick}>
        <div class="progress-fill" style="width:{dur ? (pos/dur)*100 : 0}%"></div>
      </div>
      <span class="time">{durStr}</span>
    </div>

    <!-- transport -->
    <div class="transport">
      <button class="icon-btn" onclick={prev}     title="Previous"   disabled={!playlist.length}>⏮</button>
      {#if isPlaying}
        <button class="icon-btn play-btn" onclick={pause} title="Pause">⏸</button>
      {:else}
        <button class="icon-btn play-btn" onclick={play}  title="Play" disabled={!track && !playlist.length}>▶</button>
      {/if}
      <button class="icon-btn" onclick={next}     title="Next"       disabled={!playlist.length}>⏭</button>
      <button class="icon-btn" onclick={stop}     title="Stop"       disabled={!isPlaying && !isPaused}>⏹</button>
    </div>

    <!-- shuffle / repeat / lyrics toggle -->
    <div class="toggles">
      <button class="tog-btn" class:active={isShuffle} onclick={toggleShuffle} title="Shuffle">⇌</button>
      <button class="tog-btn" class:active={isRepeat}  onclick={toggleRepeat}  title="Repeat one">↺</button>
      <button class="tog-btn" class:active={showLyrics}
              onclick={() => showLyrics = !showLyrics} title="Lyrics">♪</button>
    </div>

    <!-- volume -->
    <div class="volume-row">
      <span class="vol-icon">🔊</span>
      <input type="range" min="0" max="1" step="0.01" value={volume}
             oninput={onVolumeInput} class="vol-slider" />
      <span class="vol-pct">{Math.round(volume * 100)}%</span>
    </div>

    <!-- open buttons -->
    <div class="open-row">
      <button class="open-btn" onclick={openFile}>Open File</button>
      <button class="open-btn" onclick={openFolder}>Open Folder</button>
    </div>

    {#if errorMsg}
      <div class="error">{errorMsg}</div>
    {/if}
  </section>

  <!-- ── right column: playlist ──────────────────────────────────────────── -->
  <section class="sidebar" bind:this={playlistEl}>

    {#if showLyrics && lyrics.length}
      <!-- lyrics panel -->
      <div class="lyrics-panel">
        <h3 class="panel-title">Lyrics</h3>
        <div class="lyric-list">
          {#each lyrics as line, i}
            <p class="lyric-line"
               class:lyric-active={i === activeLyricIdx}
               class:lyric-near={Math.abs(i - activeLyricIdx) === 1}
               class:lyric-plain={line.timeSec < 0}>
              {line.text || '·'}
            </p>
          {/each}
        </div>
      </div>

    {:else}
      <!-- playlist panel -->
      <div class="playlist-panel">
        <h3 class="panel-title">
          Playlist
          <span class="count">{playlist.length} tracks</span>
          {#if playlist.length}
            <button class="clear-btn" onclick={() => Backend.ClearPlaylist()}>✕ Clear</button>
          {/if}
        </h3>
        {#if playlist.length === 0}
          <p class="pl-empty">No tracks — open a file or folder to start</p>
        {:else}
          <ul class="pl-list">
            {#each playlist as t}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
              <li class="pl-item" class:pl-current={t.current}
                  onclick={() => playAt(t.index)}>
                <span class="pl-num">{t.current ? '▶' : t.index + 1}</span>
                <span class="pl-title">{t.title}</span>
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <!-- svelte-ignore a11y_no_static_element_interactions -->
                <span class="pl-remove"
                      onclick={(e) => { e.stopPropagation(); Backend.Remove(t.index) }}
                      title="Remove">✕</span>
              </li>
            {/each}
          </ul>
        {/if}
      </div>
    {/if}

  </section>
</div>

<style>
  /* ── CSS variables (accent overridden dynamically from Go) ──────────────── */
  :root {
    --accent:    #1db954;
    --accent-15: #1db95426;
    --accent-40: #1db95466;
    --bg:        #0c0c0e;
    --surface:   #151518;
    --surface2:  #1c1c20;
    --border:    #2a2a32;
    --text:      #e8e8ec;
    --muted:     #6b6b7a;
    --dsd:       #f5a623;
    --radius:    10px;
  }

  /* ── global resets ──────────────────────────────────────────────────────── */
  * { box-sizing: border-box; margin: 0; padding: 0 }

  /* ── layout ─────────────────────────────────────────────────────────────── */
  .layout {
    display: grid;
    grid-template-columns: 340px 1fr;
    height: 100vh;
    background: var(--bg);
    color: var(--text);
    font-family: system-ui, -apple-system, 'Segoe UI', sans-serif;
    -webkit-font-smoothing: antialiased;
    overflow: hidden;
  }

  /* ── player (left) ──────────────────────────────────────────────────────── */
  .player {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.7rem;
    padding: 1.2rem 1rem 1rem;
    background: var(--surface);
    border-right: 1px solid var(--border);
    overflow-y: auto;
  }

  /* album art */
  .art-wrap {
    position: relative;
    width: 220px;
    height: 220px;
    border-radius: 12px;
    overflow: hidden;
    flex-shrink: 0;
  }

  .art-img {
    width: 100%; height: 100%;
    object-fit: cover;
    border-radius: 12px;
    position: relative;
    z-index: 1;
  }

  .art-glow {
    position: absolute;
    inset: -20px;
    width: calc(100% + 40px);
    height: calc(100% + 40px);
    object-fit: cover;
    filter: blur(30px) saturate(2);
    opacity: 0.4;
    z-index: 0;
    transform: scale(1.1);
  }

  .art-placeholder {
    width: 100%; height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 4rem;
    background: var(--surface2);
    color: var(--muted);
    border-radius: 12px;
  }

  /* track info */
  .track-info {
    width: 100%;
    text-align: center;
  }

  .title  { font-size: 1rem; font-weight: 700; line-height: 1.3; color: var(--text) }
  .artist { font-size: 0.82rem; color: var(--muted); margin-top: 0.2rem }
  .album  { font-size: 0.78rem; color: var(--muted) }
  .meta   { font-size: 0.72rem; color: var(--muted); margin-top: 0.3rem;
            display: flex; gap: 0.4rem; justify-content: center; align-items: center; flex-wrap: wrap }
  .dsd-badge {
    color: var(--dsd); font-weight: 700; font-size: 0.68rem;
    background: #f5a62322; padding: 1px 5px; border-radius: 3px
  }
  .mode-badge {
    font-size: 0.62rem; font-weight: 700; letter-spacing: 0.06em;
    padding: 1px 5px; border-radius: 3px;
    background: var(--surface2); color: var(--muted); border: 1px solid var(--border)
  }
  .mode-badge.excl { color: var(--accent); border-color: var(--accent) }

  .empty-hint  { color: var(--muted); font-size: 0.8rem; text-align: center }
  .loading-msg { color: var(--accent); font-size: 0.85rem; text-align: center }

  /* waveform */
  .waveform-wrap { width: 100% }
  .waveform { width: 100%; height: 56px; display: block; border-radius: 6px }

  /* progress */
  .progress-row {
    display: flex; align-items: center; gap: 0.5rem; width: 100%
  }
  .time { font-size: 0.7rem; color: var(--muted); min-width: 2.6rem; font-variant-numeric: tabular-nums }
  .progress-bar {
    flex: 1; height: 4px; background: var(--border); border-radius: 2px;
    cursor: pointer; position: relative; overflow: visible
  }
  .progress-bar:hover { height: 6px }
  .progress-fill {
    height: 100%; background: var(--accent); border-radius: 2px; pointer-events: none;
    transition: width 0.25s linear
  }

  /* transport */
  .transport {
    display: flex; gap: 0.5rem; align-items: center
  }
  .icon-btn {
    background: none; border: none; color: var(--text); font-size: 1.1rem;
    cursor: pointer; padding: 0.4rem 0.55rem; border-radius: 8px;
    transition: background 0.12s, color 0.12s
  }
  .icon-btn:hover:not(:disabled) { background: var(--surface2) }
  .icon-btn:disabled { opacity: 0.3; cursor: not-allowed }
  .play-btn {
    font-size: 1.3rem; padding: 0.4rem 0.75rem;
    background: var(--accent); color: #000; border-radius: 50%
  }
  .play-btn:hover:not(:disabled) { filter: brightness(1.12) }

  /* toggles */
  .toggles { display: flex; gap: 0.4rem }
  .tog-btn {
    background: none; border: 1px solid var(--border); color: var(--muted);
    font-size: 0.9rem; padding: 0.3rem 0.6rem; border-radius: 6px;
    cursor: pointer; transition: all 0.12s
  }
  .tog-btn:hover  { border-color: var(--accent); color: var(--text) }
  .tog-btn.active { border-color: var(--accent); color: var(--accent); background: var(--accent-15) }

  /* volume */
  .volume-row {
    display: flex; align-items: center; gap: 0.5rem; width: 100%
  }
  .vol-icon { font-size: 0.85rem }
  .vol-slider { flex: 1; accent-color: var(--accent); cursor: pointer; height: 4px }
  .vol-pct { font-size: 0.7rem; color: var(--muted); min-width: 2.4rem; text-align: right;
             font-variant-numeric: tabular-nums }

  /* open buttons */
  .open-row { display: flex; gap: 0.5rem; width: 100% }
  .open-btn {
    flex: 1; background: var(--surface2); border: 1px solid var(--border);
    color: var(--text); font-size: 0.78rem; font-weight: 600;
    padding: 0.45rem 0; border-radius: 7px; cursor: pointer; transition: background 0.12s
  }
  .open-btn:hover { background: var(--border) }

  /* error */
  .error {
    width: 100%; background: #3a1010; border: 1px solid #7a2020;
    border-radius: 6px; padding: 0.4rem 0.7rem; font-size: 0.75rem; color: #f08080
  }

  /* ── sidebar (right) ─────────────────────────────────────────────────────── */
  .sidebar {
    display: flex; flex-direction: column;
    background: var(--bg); overflow: hidden
  }

  .panel-title {
    font-size: 0.75rem; font-weight: 700; letter-spacing: 0.07em;
    text-transform: uppercase; color: var(--muted);
    padding: 0.9rem 1rem 0.5rem;
    display: flex; align-items: center; gap: 0.6rem;
    border-bottom: 1px solid var(--border)
  }
  .count { font-weight: 400; font-size: 0.7rem }
  .clear-btn {
    margin-left: auto; background: none; border: none;
    color: var(--muted); font-size: 0.7rem; cursor: pointer; padding: 0.15rem 0.4rem;
    border-radius: 4px
  }
  .clear-btn:hover { color: #f08080 }

  /* playlist */
  .playlist-panel { display: flex; flex-direction: column; flex: 1; overflow: hidden }
  .pl-empty { color: var(--muted); font-size: 0.82rem; padding: 1.5rem 1rem }
  .pl-list { list-style: none; overflow-y: auto; flex: 1 }
  .pl-item {
    display: grid; grid-template-columns: 2rem 1fr 1.5rem;
    align-items: center; gap: 0.4rem;
    padding: 0.5rem 1rem; cursor: pointer;
    transition: background 0.1s; border-bottom: 1px solid transparent
  }
  .pl-item:hover { background: var(--surface) }
  .pl-item.pl-current {
    background: var(--accent-15);
    border-left: 2px solid var(--accent)
  }
  .pl-num { font-size: 0.7rem; color: var(--muted); text-align: right; font-variant-numeric: tabular-nums }
  .pl-current .pl-num { color: var(--accent); font-weight: 700 }
  .pl-title { font-size: 0.82rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .pl-current .pl-title { color: var(--accent) }
  .pl-remove {
    font-size: 0.65rem; color: transparent; cursor: pointer;
    transition: color 0.1s; text-align: center
  }
  .pl-item:hover .pl-remove { color: var(--muted) }
  .pl-remove:hover { color: #f08080 !important }

  /* lyrics */
  .lyrics-panel { display: flex; flex-direction: column; flex: 1; overflow: hidden }
  .lyric-list { overflow-y: auto; flex: 1; padding: 1rem 0 }
  .lyric-line {
    font-size: 0.85rem; color: var(--muted); text-align: center;
    padding: 0.3rem 1.5rem; line-height: 1.6;
    transition: all 0.22s ease-out
  }
  .lyric-near  { font-size: 0.92rem; color: var(--text) }
  .lyric-active {
    font-size: 1.05rem; font-weight: 700; color: var(--accent);
    padding: 0.4rem 1.5rem
  }
  .lyric-plain { font-size: 0.8rem }
</style>
