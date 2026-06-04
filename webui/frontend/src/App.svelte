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
  interface AlbumData    { artist: string; title: string; year: number; artBase64: string; accentHex: string; trackCount: number }

  // ── playback state ───────────────────────────────────────────────────────────
  let track       = $state<TrackInfo | null>(null)
  let playerState = $state('Stopped')
  let pos         = $state(0)
  let dur         = $state(0)
  let volume      = $state(1.0)
  let playlist    = $state<PlaylistTrack[]>([])
  let lyrics      = $state<LyricLine[]>([])
  let waveform    = $state<number[]>([])
  let errorMsg    = $state('')
  let loading     = $state(false)
  let showLyrics  = $state(false)
  let isShuffle   = $state(false)
  let isRepeat    = $state(false)
  let isExclPref  = $state(true)   // user preference; actual mode = track.outputMode

  // ── library state ────────────────────────────────────────────────────────────
  let albums         = $state<AlbumData[]>([])
  let artists        = $state<string[]>([])
  let artistFilter   = $state('')
  let scanProgress   = $state<{done:number;total:number}|null>(null)
  let scanning       = $state(false)

  // ── sidebar tab ──────────────────────────────────────────────────────────────
  let tab = $state<'playlist'|'albums'|'artists'>('playlist')

  // ── canvas ref ───────────────────────────────────────────────────────────────
  let canvasEl = $state<HTMLCanvasElement|null>(null)

  // ── derived ──────────────────────────────────────────────────────────────────
  const isPlaying      = $derived(playerState === 'Playing')
  const isPaused       = $derived(playerState === 'Paused')
  const posStr         = $derived(fmtTime(pos))
  const durStr         = $derived(fmtTime(dur))
  const accentColor    = $derived(track?.accentHex || '#1db954')
  const filteredAlbums = $derived(artistFilter ? albums.filter(a => a.artist === artistFilter) : albums)

  const activeLyricIdx = $derived((() => {
    if (!lyrics.length) return -1
    let idx = -1
    for (let i = 0; i < lyrics.length; i++) {
      if (lyrics[i].timeSec >= 0 && lyrics[i].timeSec <= pos) idx = i
    }
    return idx
  })())

  // ── CSS accent sync ──────────────────────────────────────────────────────────
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
    const W = canvasEl.width, H = canvasEl.height, cy = H / 2
    ctx.clearRect(0, 0, W, H)
    const pts = waveform.length, stepX = W / pts
    ctx.beginPath()
    for (let i = 0; i < pts; i++) {
      const edge = Math.min(1, Math.min(i, pts-1-i) / (pts * 0.1))
      const amp  = waveform[i] * edge * cy * 0.85
      i === 0 ? ctx.moveTo(0, cy - amp) : ctx.lineTo(i * stepX, cy - amp)
    }
    for (let i = pts-1; i >= 0; i--) {
      const edge = Math.min(1, Math.min(i, pts-1-i) / (pts * 0.1))
      ctx.lineTo(i * stepX, cy + waveform[i] * edge * cy * 0.85)
    }
    ctx.closePath()
    ctx.fillStyle = accentColor + '18'; ctx.fill()
    ctx.save(); ctx.filter = 'blur(3px)'
    ctx.strokeStyle = accentColor + '50'; ctx.lineWidth = 4; ctx.stroke()
    ctx.restore()
    ctx.strokeStyle = accentColor + 'bb'; ctx.lineWidth = 1.5; ctx.stroke()
  })

  // ── lyric auto-scroll ────────────────────────────────────────────────────────
  $effect(() => {
    if (activeLyricIdx < 0 || !showLyrics) return
    document.querySelector('.lyric-active')?.scrollIntoView({behavior:'smooth',block:'center'})
  })

  // ── Wails events ─────────────────────────────────────────────────────────────
  $effect(() => {
    const off = [
      EventsOn('state-change', (s:string) => {
        playerState = s
        if (s === 'Stopped') { pos = 0; loading = false }
        if (s === 'Loading')  loading = true
        if (s === 'Playing')  loading = false
      }),
      EventsOn('position-change', (p:{pos:number;dur:number}) => { pos=p.pos; dur=p.dur }),
      EventsOn('track-change',    (info:TrackInfo) => { track=info; loading=false; errorMsg='' }),
      EventsOn('playlist-updated',(pl:PlaylistTrack[]) => { playlist=pl }),
      EventsOn('lyrics',          (lines:LyricLine[]|null) => { lyrics=lines??[] }),
      EventsOn('waveform',        (data:number[]) => { waveform=data }),
      EventsOn('audio-error',     (msg:string) => { errorMsg=msg; loading=false; scanning=false }),
      EventsOn('playback-finished',() => { playerState='Stopped'; pos=0 }),
      EventsOn('library-updated', (data:{albums:AlbumData[];artists:string[]}) => {
        albums=data.albums; artists=data.artists
      }),
      EventsOn('scan-progress', (p:{done:number;total:number}) => { scanProgress=p }),
      EventsOn('scan-done',     () => { scanning=false; scanProgress=null }),
      EventsOn('wails:file-drop',(paths:string[]) => {
        Backend.AddFiles(paths).then(pl => { playlist=pl })
      }),
    ]
    return () => off.forEach(f => f())
  })

  // ── handlers ─────────────────────────────────────────────────────────────────
  async function openFile() {
    const path = await Backend.OpenFileDialog()
    if (!path) return
    errorMsg=''; loading=true
    try { track = await Backend.LoadFile(path) }
    catch(e:any) { errorMsg=String(e); loading=false }
  }

  async function openFolder() {
    const dir = await Backend.OpenFolderDialog()
    if (!dir) return
    playlist = await Backend.AddFolder(dir)
    if (tab !== 'playlist') tab = 'playlist'
  }

  async function scanLibraryDialog() {
    const dir = await Backend.OpenFolderDialog()
    if (!dir) return
    scanning = true; scanProgress = {done:0,total:0}
    Backend.ScanLibrary(dir)
  }

  async function play()  { try { await Backend.Play() } catch(e:any) { errorMsg=String(e) } }
  function pause() { Backend.Pause() }
  function stop()  { Backend.Stop() }

  async function next() {
    loading=true
    try { const t=await Backend.Next(); if(t) track=t } catch(e:any) { errorMsg=String(e) }
    finally { loading=false }
  }

  async function prev() {
    loading=true
    try { const t=await Backend.Prev(); if(t) track=t } catch(e:any) { errorMsg=String(e) }
    finally { loading=false }
  }

  async function playAt(index:number) {
    loading=true
    try { const t=await Backend.PlayAt(index); if(t) track=t } catch(e:any) { errorMsg=String(e) }
    finally { loading=false }
  }

  async function loadAlbum(artist:string, title:string) {
    loading=true; tab='playlist'
    try { const t=await Backend.LoadAlbum(artist, title); if(t) track=t }
    catch(e:any) { errorMsg=String(e) }
    finally { loading=false }
  }

  function filterByArtist(artist:string) {
    artistFilter = artist; tab = 'albums'
  }

  function clearArtistFilter() { artistFilter = '' }

  function onProgressClick(e:MouseEvent) {
    if (!dur) return
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
    Backend.Seek(((e.clientX - r.left) / r.width) * dur)
  }

  function onVolumeInput(e:Event) {
    volume = parseFloat((e.target as HTMLInputElement).value)
    Backend.SetVolume(volume)
  }

  function toggleShuffle() { isShuffle=!isShuffle; Backend.SetShuffle(isShuffle) }
  function toggleRepeat()  { isRepeat=!isRepeat;   Backend.SetRepeat(isRepeat)   }
  function toggleExclusive() {
    isExclPref = !isExclPref
    Backend.SetExclusive(isExclPref)
  }

  function fmtTime(s:number) {
    if (!s||s<0) return '0:00'
    return `${Math.floor(s/60)}:${Math.floor(s%60).toString().padStart(2,'0')}`
  }
</script>

<!-- ── markup ──────────────────────────────────────────────────────────────── -->

<div class="layout">

  <!-- ══ LEFT SIDEBAR ══════════════════════════════════════════════════════ -->
  <aside class="sidebar">

    <!-- tabs -->
    <div class="tabs">
      <button class="tab" class:active={tab==='playlist'} onclick={() => tab='playlist'}>Playlist</button>
      <button class="tab" class:active={tab==='albums'}   onclick={() => tab='albums'}>Albums</button>
      <button class="tab" class:active={tab==='artists'}  onclick={() => tab='artists'}>Artists</button>
    </div>

    <!-- tab content -->
    <div class="tab-content">

      <!-- ── PLAYLIST TAB ── -->
      {#if tab === 'playlist'}
        {#if playlist.length === 0}
          <p class="empty-hint">Open a file or folder to get started</p>
        {:else}
          <ul class="pl-list">
            {#each playlist as t}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
              <li class="pl-item" class:pl-current={t.current} onclick={() => playAt(t.index)}>
                <span class="pl-num">{t.current ? '▶' : t.index+1}</span>
                <span class="pl-title">{t.title}</span>
                <span class="pl-remove"
                      onclick={(e) => { e.stopPropagation(); Backend.Remove(t.index) }}
                      title="Remove">✕</span>
              </li>
            {/each}
          </ul>
        {/if}

      <!-- ── ALBUMS TAB ── -->
      {:else if tab === 'albums'}
        {#if artistFilter}
          <div class="filter-bar">
            <span class="filter-label">{artistFilter}</span>
            <button class="clear-filter" onclick={clearArtistFilter}>✕</button>
          </div>
        {/if}
        {#if filteredAlbums.length === 0}
          {#if albums.length === 0}
            <p class="empty-hint">Scan a music folder to build the library</p>
          {:else}
            <p class="empty-hint">No albums for this artist</p>
          {/if}
        {:else}
          <div class="album-grid">
            {#each filteredAlbums as al}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <!-- svelte-ignore a11y_no_static_element_interactions -->
              <div class="album-card" onclick={() => loadAlbum(al.artist, al.title)}>
                {#if al.artBase64}
                  <img class="album-art" src={al.artBase64} alt={al.title} />
                {:else}
                  <div class="album-placeholder">♪</div>
                {/if}
                <p class="album-title">{al.title}</p>
                <p class="album-artist">{al.artist}</p>
                <p class="album-meta">{al.year || ''} · {al.trackCount} tracks</p>
              </div>
            {/each}
          </div>
        {/if}

      <!-- ── ARTISTS TAB ── -->
      {:else}
        {#if artists.length === 0}
          <p class="empty-hint">Scan a music folder to build the library</p>
        {:else}
          <ul class="artist-list">
            {#each artists as artist}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
              <li class="artist-item"
                  class:artist-active={artist === artistFilter}
                  onclick={() => filterByArtist(artist)}>
                {artist}
              </li>
            {/each}
          </ul>
        {/if}
      {/if}

    </div><!-- /tab-content -->

    <!-- scan progress overlay -->
    {#if scanning}
      <div class="scan-overlay">
        <span class="scan-label">Scanning…</span>
        {#if scanProgress && scanProgress.total > 0}
          <div class="scan-bar">
            <div class="scan-fill"
                 style="width:{(scanProgress.done/scanProgress.total)*100}%"></div>
          </div>
          <span class="scan-count">{scanProgress.done} / {scanProgress.total}</span>
        {/if}
      </div>
    {/if}

    <!-- sidebar footer -->
    <div class="sidebar-footer">
      {#if playlist.length > 0}
        <button class="foot-btn danger" onclick={() => Backend.ClearPlaylist()}>✕ Clear</button>
      {/if}
      <button class="foot-btn" onclick={openFile}>+ File</button>
      <button class="foot-btn" onclick={openFolder}>+ Folder</button>
      <button class="foot-btn lib" onclick={scanLibraryDialog}>⟳ Library</button>
    </div>

  </aside>

  <!-- ══ RIGHT PLAYER ═══════════════════════════════════════════════════════ -->
  <main class="player">

    <!-- album art with ambient glow -->
    <div class="art-wrap">
      {#if track?.artBase64}
        <img class="art-glow" src={track.artBase64} alt="" aria-hidden="true" />
        <img class="art-img"  src={track.artBase64} alt="Album art" />
      {:else}
        <div class="art-placeholder">♪</div>
      {/if}
    </div>

    <!-- track info -->
    <div class="track-info">
      {#if loading}
        <p class="loading-msg">Loading…</p>
      {:else if track}
        <p class="title">{track.title}</p>
        <p class="sub">
          {#if track.artist}{track.artist}{/if}
          {#if track.artist && track.album} · {/if}
          {#if track.album}<span class="album-name">{track.album}</span>{/if}
        </p>
        <div class="badges">
          <span class="badge quality">{track.qualityLabel || track.format}</span>
          {#if track.isDSD}
            <span class="badge dsd">{track.dsdLabel}</span>
          {/if}
          {#if track.outputMode}
            <span class="badge mode" class:excl={track.outputMode==='exclusive'}>
              {track.outputMode.toUpperCase()}
            </span>
          {/if}
        </div>
      {:else}
        <p class="empty-hint">Drop files here or use the sidebar</p>
      {/if}
    </div>

    <!-- waveform -->
    <canvas bind:this={canvasEl} class="waveform" width="560" height="52"></canvas>

    <!-- progress bar -->
    <div class="progress-row">
      <span class="time">{posStr}</span>
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="progress-bar" onclick={onProgressClick}>
        <div class="progress-fill" style="width:{dur?(pos/dur)*100:0}%"></div>
      </div>
      <span class="time">{durStr}</span>
    </div>

    <!-- transport -->
    <div class="transport">
      <button class="icon-btn" onclick={prev} title="Previous" disabled={!playlist.length}>⏮</button>
      {#if isPlaying}
        <button class="icon-btn play-btn" onclick={pause} title="Pause">⏸</button>
      {:else}
        <button class="icon-btn play-btn" onclick={play}  title="Play"
                disabled={!track && !playlist.length}>▶</button>
      {/if}
      <button class="icon-btn" onclick={next} title="Next" disabled={!playlist.length}>⏭</button>
      <button class="icon-btn" onclick={stop} title="Stop" disabled={!isPlaying && !isPaused}>⏹</button>
    </div>

    <!-- toggles row: shuffle / repeat / lyrics / exclusive -->
    <div class="toggles">
      <button class="tog-btn" class:active={isShuffle}  onclick={toggleShuffle}  title="Shuffle">⇌ Shuffle</button>
      <button class="tog-btn" class:active={isRepeat}   onclick={toggleRepeat}   title="Repeat one">↺ Repeat</button>
      <button class="tog-btn" class:active={showLyrics} onclick={() => showLyrics=!showLyrics} title="Lyrics">♪ Lyrics</button>
      <button class="tog-btn excl-tog" class:active={isExclPref}
              onclick={toggleExclusive} title="Toggle WASAPI exclusive mode">
        {isExclPref ? '⊗ EXCL' : '◯ EXCL'}
      </button>
    </div>

    <!-- volume -->
    <div class="volume-row">
      <span class="vol-icon">🔊</span>
      <input type="range" min="0" max="1" step="0.01" value={volume}
             oninput={onVolumeInput} class="vol-slider" />
      <span class="vol-pct">{Math.round(volume*100)}%</span>
    </div>

    <!-- lyrics panel (collapsible) -->
    {#if showLyrics && lyrics.length}
      <div class="lyrics-panel">
        {#each lyrics as line, i}
          <p class="lyric-line"
             class:lyric-active={i===activeLyricIdx}
             class:lyric-near={Math.abs(i-activeLyricIdx)===1}>
            {line.text || '·'}
          </p>
        {/each}
      </div>
    {/if}

    {#if errorMsg}
      <div class="error">{errorMsg}</div>
    {/if}

  </main>
</div>

<style>
  :root {
    --accent:    #1db954;
    --accent-15: #1db95426;
    --accent-40: #1db95466;
    --bg:        #0c0c0e;
    --surface:   #111114;
    --surface2:  #1a1a1e;
    --border:    #252528;
    --text:      #e8e8ec;
    --muted:     #5c5c6a;
    --dsd:       #f5a623;
    --radius:    8px;
  }
  * { box-sizing: border-box; margin: 0; padding: 0 }

  /* ── layout: sidebar LEFT, player RIGHT ─────────────────────────── */
  .layout {
    display: grid;
    grid-template-columns: 270px 1fr;
    height: 100vh;
    background: var(--bg);
    color: var(--text);
    font-family: system-ui, -apple-system, 'Segoe UI', sans-serif;
    -webkit-font-smoothing: antialiased;
    overflow: hidden;
  }

  /* ══ SIDEBAR ═══════════════════════════════════════════════════════ */
  .sidebar {
    display: flex; flex-direction: column;
    background: var(--surface);
    border-right: 1px solid var(--border);
    overflow: hidden;
    position: relative;
  }

  /* tabs */
  .tabs {
    display: flex; flex-shrink: 0;
    border-bottom: 1px solid var(--border);
  }
  .tab {
    flex: 1; background: none; border: none;
    color: var(--muted); font-size: 0.72rem; font-weight: 600;
    letter-spacing: 0.05em; text-transform: uppercase;
    padding: 0.65rem 0; cursor: pointer;
    transition: color 0.15s;
    border-bottom: 2px solid transparent; margin-bottom: -1px;
  }
  .tab:hover  { color: var(--text) }
  .tab.active { color: var(--accent); border-bottom-color: var(--accent) }

  /* tab content */
  .tab-content { flex: 1; overflow-y: auto; overflow-x: hidden }

  /* playlist */
  .pl-list { list-style: none }
  .pl-item {
    display: grid; grid-template-columns: 1.8rem 1fr 1.2rem;
    align-items: center; gap: 0.3rem;
    padding: 0.45rem 0.75rem; cursor: pointer;
    transition: background 0.1s;
  }
  .pl-item:hover { background: var(--surface2) }
  .pl-item.pl-current {
    background: var(--accent-15); border-left: 2px solid var(--accent)
  }
  .pl-num   { font-size: 0.65rem; color: var(--muted); text-align: right;
              font-variant-numeric: tabular-nums }
  .pl-current .pl-num { color: var(--accent); font-weight: 700 }
  .pl-title { font-size: 0.78rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .pl-current .pl-title { color: var(--accent) }
  .pl-remove { font-size: 0.6rem; color: transparent; cursor: pointer; text-align: center }
  .pl-item:hover .pl-remove { color: var(--muted) }
  .pl-remove:hover { color: #f08080 !important }

  /* albums */
  .filter-bar {
    display: flex; align-items: center; gap: 0.5rem;
    padding: 0.5rem 0.75rem; background: var(--accent-15);
    border-bottom: 1px solid var(--border); font-size: 0.75rem
  }
  .filter-label { flex: 1; color: var(--accent); font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap }
  .clear-filter { background: none; border: none; color: var(--muted); cursor: pointer; font-size: 0.7rem }
  .clear-filter:hover { color: #f08080 }

  .album-grid {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 6px; padding: 8px;
  }
  .album-card {
    background: var(--surface2); border-radius: var(--radius);
    overflow: hidden; cursor: pointer;
    transition: transform 0.15s, box-shadow 0.15s;
  }
  .album-card:hover {
    transform: translateY(-2px);
    box-shadow: 0 6px 20px #00000060;
  }
  .album-art, .album-placeholder {
    width: 100%; aspect-ratio: 1; object-fit: cover; display: block
  }
  .album-placeholder {
    display: flex; align-items: center; justify-content: center;
    background: var(--border); color: var(--muted); font-size: 1.5rem
  }
  .album-title  { font-size: 0.7rem; font-weight: 600; padding: 0.3rem 0.4rem 0;
                  white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .album-artist { font-size: 0.65rem; color: var(--muted); padding: 0 0.4rem;
                  white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .album-meta   { font-size: 0.6rem; color: var(--muted); padding: 0.1rem 0.4rem 0.4rem }

  /* artists */
  .artist-list { list-style: none }
  .artist-item {
    padding: 0.55rem 0.75rem; cursor: pointer; font-size: 0.82rem;
    border-bottom: 1px solid var(--border); transition: background 0.1s;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis
  }
  .artist-item:hover      { background: var(--surface2) }
  .artist-item.artist-active { background: var(--accent-15); color: var(--accent) }

  /* scan overlay */
  .scan-overlay {
    position: absolute; bottom: 40px; left: 0; right: 0;
    background: var(--surface2); border-top: 1px solid var(--border);
    padding: 0.5rem 0.75rem; display: flex; flex-direction: column; gap: 0.3rem
  }
  .scan-label { font-size: 0.7rem; color: var(--muted) }
  .scan-bar   { height: 3px; background: var(--border); border-radius: 2px; overflow: hidden }
  .scan-fill  { height: 100%; background: var(--accent); border-radius: 2px; transition: width 0.3s }
  .scan-count { font-size: 0.65rem; color: var(--muted); text-align: right }

  /* sidebar footer */
  .sidebar-footer {
    flex-shrink: 0; display: flex; gap: 4px;
    padding: 6px 8px; border-top: 1px solid var(--border)
  }
  .foot-btn {
    flex: 1; background: var(--surface2); border: 1px solid var(--border);
    color: var(--text); font-size: 0.68rem; font-weight: 600;
    padding: 0.35rem 0; border-radius: 6px; cursor: pointer; transition: background 0.12s
  }
  .foot-btn:hover  { background: var(--border) }
  .foot-btn.lib    { color: var(--accent); border-color: var(--accent-40) }
  .foot-btn.danger { color: #f08080; border-color: #7a202050 }
  .foot-btn.danger:hover { background: #3a1010 }

  .empty-hint { color: var(--muted); font-size: 0.78rem; padding: 1.2rem 0.75rem }

  /* ══ PLAYER ════════════════════════════════════════════════════════ */
  .player {
    display: flex; flex-direction: column; align-items: center;
    gap: 0.65rem; padding: 1.4rem 2rem 1rem;
    overflow-y: auto; background: var(--bg);
  }

  /* art */
  .art-wrap {
    position: relative; flex-shrink: 0;
    width: clamp(180px, 35vw, 280px);
    aspect-ratio: 1; border-radius: 14px; overflow: hidden;
  }
  .art-img {
    width: 100%; height: 100%; object-fit: cover;
    border-radius: 14px; position: relative; z-index: 1
  }
  .art-glow {
    position: absolute; inset: -30%; width: 160%; height: 160%;
    object-fit: cover; filter: blur(40px) saturate(2);
    opacity: 0.35; z-index: 0
  }
  .art-placeholder {
    width: 100%; height: 100%; display: flex;
    align-items: center; justify-content: center;
    font-size: 5rem; background: var(--surface); color: var(--muted); border-radius: 14px
  }

  /* track info */
  .track-info { width: 100%; text-align: center; max-width: 560px }
  .title  { font-size: 1.1rem; font-weight: 700; line-height: 1.3 }
  .sub    { font-size: 0.82rem; color: var(--muted); margin-top: 0.2rem }
  .album-name { color: var(--text) }
  .badges {
    display: flex; gap: 0.35rem; justify-content: center;
    flex-wrap: wrap; margin-top: 0.4rem
  }
  .badge {
    font-size: 0.62rem; font-weight: 700; letter-spacing: 0.06em;
    padding: 2px 7px; border-radius: 4px;
  }
  .badge.quality { background: var(--surface2); color: var(--muted); border: 1px solid var(--border) }
  .badge.dsd     { background: #f5a62320; color: var(--dsd); border: 1px solid var(--dsd) }
  .badge.mode    { background: var(--surface2); color: var(--muted); border: 1px solid var(--border) }
  .badge.mode.excl { color: var(--accent); border-color: var(--accent); background: var(--accent-15) }

  .loading-msg { color: var(--accent); font-size: 0.85rem }

  /* waveform */
  .waveform { width: 100%; max-width: 560px; height: 52px; display: block; border-radius: 6px }

  /* progress */
  .progress-row {
    display: flex; align-items: center; gap: 0.5rem; width: 100%; max-width: 560px
  }
  .time { font-size: 0.68rem; color: var(--muted); min-width: 2.5rem;
          font-variant-numeric: tabular-nums }
  .progress-bar {
    flex: 1; height: 4px; background: var(--border); border-radius: 2px;
    cursor: pointer; transition: height 0.1s
  }
  .progress-bar:hover { height: 7px }
  .progress-fill { height: 100%; background: var(--accent); border-radius: 2px; transition: width .25s linear }

  /* transport */
  .transport { display: flex; gap: 0.5rem; align-items: center }
  .icon-btn {
    background: none; border: none; color: var(--text); font-size: 1rem;
    cursor: pointer; padding: 0.4rem 0.6rem; border-radius: 8px; transition: background 0.12s
  }
  .icon-btn:hover:not(:disabled) { background: var(--surface) }
  .icon-btn:disabled { opacity: 0.28; cursor: not-allowed }
  .play-btn {
    font-size: 1.2rem; padding: 0.45rem 0.85rem;
    background: var(--accent); color: #000; border-radius: 50%;
  }
  .play-btn:hover:not(:disabled) { filter: brightness(1.15) }

  /* toggles */
  .toggles { display: flex; gap: 0.4rem; flex-wrap: wrap; justify-content: center }
  .tog-btn {
    background: none; border: 1px solid var(--border); color: var(--muted);
    font-size: 0.72rem; font-weight: 600; padding: 0.3rem 0.65rem;
    border-radius: 6px; cursor: pointer; transition: all 0.12s
  }
  .tog-btn:hover  { border-color: var(--accent); color: var(--text) }
  .tog-btn.active { border-color: var(--accent); color: var(--accent); background: var(--accent-15) }
  .excl-tog.active { border-color: #4caf50; color: #4caf50; background: #4caf5015 }

  /* volume */
  .volume-row {
    display: flex; align-items: center; gap: 0.5rem; width: 100%; max-width: 400px
  }
  .vol-icon   { font-size: 0.85rem }
  .vol-slider { flex: 1; accent-color: var(--accent); cursor: pointer }
  .vol-pct    { font-size: 0.68rem; color: var(--muted); min-width: 2.4rem; text-align: right;
                font-variant-numeric: tabular-nums }

  /* lyrics */
  .lyrics-panel {
    width: 100%; max-width: 560px; max-height: 200px;
    overflow-y: auto; padding: 0.5rem 0;
  }
  .lyric-line {
    font-size: 0.82rem; color: var(--muted); text-align: center;
    padding: 0.25rem 1rem; line-height: 1.7; transition: all 0.22s ease-out
  }
  .lyric-near   { font-size: 0.9rem; color: var(--text) }
  .lyric-active { font-size: 1.05rem; font-weight: 700; color: var(--accent); padding: 0.35rem 1rem }

  /* error */
  .error {
    width: 100%; max-width: 560px;
    background: #3a1010; border: 1px solid #7a2020;
    border-radius: var(--radius); padding: 0.4rem 0.75rem;
    font-size: 0.75rem; color: #f08080
  }
</style>
