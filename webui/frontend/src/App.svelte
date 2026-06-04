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
  interface SongData     { path: string; title: string; artist: string; album: string; trackNum: number }

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
  let isExclPref  = $state(false)  // shared by default — user opts into exclusive

  // ── library state ────────────────────────────────────────────────────────────
  let albums       = $state<AlbumData[]>([])
  let artists      = $state<string[]>([])
  let songs        = $state<SongData[]>([])
  let songsLoaded  = $state(false)
  let artistFilter = $state('')
  let songSearch   = $state('')
  let scanning     = $state(false)
  let scanProgress = $state<{done:number;total:number}|null>(null)

  // ── UI state ─────────────────────────────────────────────────────────────────
  let tab      = $state<'playlist'|'albums'|'artists'|'songs'>('playlist')
  let canvasEl = $state<HTMLCanvasElement|null>(null)

  // ── derived ──────────────────────────────────────────────────────────────────
  const isPlaying      = $derived(playerState === 'Playing')
  const isPaused       = $derived(playerState === 'Paused')
  const posStr         = $derived(fmtTime(pos))
  const durStr         = $derived(fmtTime(dur))
  const accentColor    = $derived(track?.accentHex || '#1db954')
  const progress       = $derived(dur > 0 ? (pos / dur) * 100 : 0)
  const filteredAlbums = $derived(artistFilter ? albums.filter(a => a.artist === artistFilter) : albums)
  const filteredSongs  = $derived(
    songSearch
      ? songs.filter(s =>
          s.title.toLowerCase().includes(songSearch.toLowerCase()) ||
          s.artist.toLowerCase().includes(songSearch.toLowerCase()) ||
          s.album.toLowerCase().includes(songSearch.toLowerCase()))
      : songs
  )
  const activeLyricIdx = $derived((() => {
    if (!lyrics.length) return -1
    let idx = -1
    for (let i = 0; i < lyrics.length; i++) {
      if (lyrics[i].timeSec >= 0 && lyrics[i].timeSec <= pos) idx = i
    }
    return idx
  })())

  // ── CSS variable sync (rgba variants for glow effects) ───────────────────────
  $effect(() => {
    const hex = accentColor.replace('#','')
    const r = parseInt(hex.slice(0,2),16)
    const g = parseInt(hex.slice(2,4),16)
    const b = parseInt(hex.slice(4,6),16)
    const s = document.documentElement.style
    s.setProperty('--accent',     accentColor)
    s.setProperty('--accent-rgb', `${r},${g},${b}`)
    s.setProperty('--accent-08',  `rgba(${r},${g},${b},0.08)`)
    s.setProperty('--accent-15',  `rgba(${r},${g},${b},0.15)`)
    s.setProperty('--accent-30',  `rgba(${r},${g},${b},0.30)`)
    s.setProperty('--accent-50',  `rgba(${r},${g},${b},0.50)`)
  })

  // ── waveform: rAF loop polls GetWaveform() — display-sync'd, no IPC drift ───
  $effect(() => {
    let rafId: number, running = true, pending = false
    function loop() {
      if (!running) return
      rafId = requestAnimationFrame(loop)
      if (pending) return
      pending = true
      Backend.GetWaveform().then((data: number[]) => {
        waveform = data ?? []
        pending = false
      }).catch(() => { pending = false })
    }
    rafId = requestAnimationFrame(loop)
    return () => { running = false; cancelAnimationFrame(rafId) }
  })

  // ── canvas draw ──────────────────────────────────────────────────────────────
  $effect(() => {
    if (!canvasEl || !waveform.length) return
    const ctx = canvasEl.getContext('2d')
    if (!ctx) return
    const W = canvasEl.width, H = canvasEl.height, cy = H / 2
    ctx.clearRect(0, 0, W, H)
    const pts = waveform.length, step = W / pts

    ctx.beginPath()
    for (let i = 0; i < pts; i++) {
      const fade = Math.min(1, Math.min(i, pts-1-i) / (pts * 0.08))
      const y = cy - waveform[i] * fade * cy * 0.88
      i === 0 ? ctx.moveTo(0, y) : ctx.lineTo(i * step, y)
    }
    for (let i = pts-1; i >= 0; i--) {
      const fade = Math.min(1, Math.min(i, pts-1-i) / (pts * 0.08))
      ctx.lineTo(i * step, cy + waveform[i] * fade * cy * 0.88)
    }
    ctx.closePath()

    // Gradient fill
    const grad = ctx.createLinearGradient(0, 0, 0, H)
    grad.addColorStop(0,   `rgba(var(--accent-rgb),0.25)`.replace('var(--accent-rgb)', getAccentRgb()))
    grad.addColorStop(0.5, `rgba(var(--accent-rgb),0.08)`.replace('var(--accent-rgb)', getAccentRgb()))
    grad.addColorStop(1,   `rgba(var(--accent-rgb),0.25)`.replace('var(--accent-rgb)', getAccentRgb()))
    ctx.fillStyle = grad
    ctx.fill()

    // Glow stroke
    ctx.save()
    ctx.filter = 'blur(4px)'
    ctx.strokeStyle = accentColor + '55'
    ctx.lineWidth = 5
    ctx.stroke()
    ctx.restore()

    // Sharp stroke
    ctx.strokeStyle = accentColor + 'cc'
    ctx.lineWidth = 1.5
    ctx.stroke()
  })

  function getAccentRgb() {
    return getComputedStyle(document.documentElement).getPropertyValue('--accent-rgb').trim() || '29,185,84'
  }

  // ── lyric auto-scroll ────────────────────────────────────────────────────────
  $effect(() => {
    if (activeLyricIdx < 0 || !showLyrics) return
    document.querySelector('.lyric-active')?.scrollIntoView({behavior:'smooth',block:'center'})
  })

  // ── lazy load songs when tab opens ──────────────────────────────────────────
  $effect(() => {
    if (tab === 'songs' && !songsLoaded) {
      songsLoaded = true
      Backend.GetLibrarySongs().then(s => { songs = s ?? [] })
    }
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
      EventsOn('audio-error',     (msg:string) => { errorMsg=msg; loading=false; scanning=false }),
      EventsOn('playback-finished',() => { playerState='Stopped'; pos=0 }),
      EventsOn('library-updated', (data:{albums:AlbumData[];artists:string[]}) => {
        albums=data.albums ?? []
        artists=data.artists ?? []
        // Reload songs if tab already visited
        if (songsLoaded) Backend.GetLibrarySongs().then(s => { songs = s ?? [] })
      }),
      EventsOn('scan-progress', (p:{done:number;total:number}) => { scanProgress=p }),
      EventsOn('scan-done',     () => { scanning=false; scanProgress=null }),
      EventsOn('wails:file-drop', (paths:string[]) => {
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
    const pl = await Backend.AddFolder(dir)
    playlist = pl
    tab = 'playlist'
  }

  async function scanLibraryDialog() {
    const dir = await Backend.OpenFolderDialog()
    if (!dir) return
    scanning=true; scanProgress={done:0,total:0}
    Backend.ScanLibrary(dir)
  }

  async function play()  { try { await Backend.Play() } catch(e:any) { errorMsg=String(e) } }
  function pause() { Backend.Pause() }
  function stop()  { Backend.Stop() }

  async function next() {
    loading=true
    try { const t=await Backend.Next(); if(t) track=t }
    catch(e:any) { errorMsg=String(e) }
    finally { loading=false }
  }

  async function prev() {
    loading=true
    try { const t=await Backend.Prev(); if(t) track=t }
    catch(e:any) { errorMsg=String(e) }
    finally { loading=false }
  }

  async function playAt(index:number) {
    loading=true
    try { const t=await Backend.PlayAt(index); if(t) track=t }
    catch(e:any) { errorMsg=String(e) }
    finally { loading=false }
  }

  async function loadAlbum(artist:string, title:string) {
    loading=true; tab='playlist'
    try { const t=await Backend.LoadAlbum(artist, title); if(t) track=t }
    catch(e:any) { errorMsg=String(e) }
    finally { loading=false }
  }

  async function playSong(path:string) {
    loading=true; tab='playlist'
    try { const t=await Backend.PlaySong(path); if(t) track=t }
    catch(e:any) { errorMsg=String(e) }
    finally { loading=false }
  }

  function filterByArtist(artist:string) { artistFilter=artist; tab='albums' }
  function clearArtistFilter() { artistFilter='' }

  function onProgressClick(e:MouseEvent) {
    if (!dur) return
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
    Backend.Seek(((e.clientX - r.left) / r.width) * dur)
  }

  function onVolumeInput(e:Event) {
    volume = parseFloat((e.target as HTMLInputElement).value)
    Backend.SetVolume(volume)
  }

  function toggleShuffle()  { isShuffle=!isShuffle; Backend.SetShuffle(isShuffle) }
  function toggleRepeat()   { isRepeat=!isRepeat;   Backend.SetRepeat(isRepeat) }
  function toggleExclusive(){ isExclPref=!isExclPref; Backend.SetExclusive(isExclPref) }

  function fmtTime(s:number) {
    if (!s||s<0) return '0:00'
    return `${Math.floor(s/60)}:${Math.floor(s%60).toString().padStart(2,'0')}`
  }
</script>

<!-- ═══════════════════════════════════════════════════════════════════════════ -->

<div class="layout" class:lyrics-open={showLyrics && lyrics.length > 0}>

  <!-- ════ LEFT SIDEBAR ════════════════════════════════════════════════════ -->
  <aside class="sidebar">

    <div class="tabs">
      <button class="tab" class:active={tab==='playlist'} onclick={() => tab='playlist'}>Queue</button>
      <button class="tab" class:active={tab==='songs'}    onclick={() => tab='songs'}>Songs</button>
      <button class="tab" class:active={tab==='albums'}   onclick={() => tab='albums'}>Albums</button>
      <button class="tab" class:active={tab==='artists'}  onclick={() => tab='artists'}>Artists</button>
    </div>

    <div class="tab-content">

      <!-- QUEUE -->
      {#if tab === 'playlist'}
        {#if playlist.length === 0}
          <div class="empty-state">
            <span class="empty-icon">♫</span>
            <p>Open a file or folder to start</p>
          </div>
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

      <!-- SONGS -->
      {:else if tab === 'songs'}
        <div class="search-wrap">
          <input class="search-input" type="text" placeholder="Search songs…"
                 bind:value={songSearch} />
        </div>
        {#if songs.length === 0}
          <div class="empty-state">
            <span class="empty-icon">♪</span>
            <p>{songsLoaded ? 'Scan library to populate' : 'Loading…'}</p>
          </div>
        {:else}
          <p class="list-count">{filteredSongs.length} of {songs.length} songs</p>
          <ul class="song-list">
            {#each filteredSongs as s}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
              <li class="song-item" onclick={() => playSong(s.path)}>
                <div class="song-main">
                  <span class="song-title">{s.title}</span>
                  <span class="song-sub">{s.artist}{s.album ? ' · ' + s.album : ''}</span>
                </div>
                <span class="song-play-icon">▶</span>
              </li>
            {/each}
          </ul>
        {/if}

      <!-- ALBUMS -->
      {:else if tab === 'albums'}
        {#if artistFilter}
          <div class="filter-bar">
            <span class="filter-lbl">{artistFilter}</span>
            <button class="filter-clear" onclick={clearArtistFilter}>✕</button>
          </div>
        {/if}
        {#if filteredAlbums.length === 0}
          <div class="empty-state">
            <span class="empty-icon">◎</span>
            <p>{albums.length === 0 ? 'Scan library first' : 'No albums for this artist'}</p>
          </div>
        {:else}
          <div class="album-grid">
            {#each filteredAlbums as al}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <!-- svelte-ignore a11y_no_static_element_interactions -->
              <div class="album-card" onclick={() => loadAlbum(al.artist, al.title)}
                   style={al.accentHex ? `--card-accent:${al.accentHex}` : ''}>
                {#if al.artBase64}
                  <img class="album-art" src={al.artBase64} alt={al.title} />
                {:else}
                  <div class="album-placeholder">♪</div>
                {/if}
                <div class="album-meta">
                  <p class="album-title">{al.title}</p>
                  <p class="album-artist">{al.artist}</p>
                </div>
              </div>
            {/each}
          </div>
        {/if}

      <!-- ARTISTS -->
      {:else}
        {#if artists.length === 0}
          <div class="empty-state">
            <span class="empty-icon">♫</span>
            <p>Scan library first</p>
          </div>
        {:else}
          <p class="list-count">{artists.length} artists</p>
          <ul class="artist-list">
            {#each artists as artist}
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
              <li class="artist-item" class:artist-active={artist===artistFilter}
                  onclick={() => filterByArtist(artist)}>
                <span class="artist-name">{artist}</span>
                <span class="artist-arrow">›</span>
              </li>
            {/each}
          </ul>
        {/if}
      {/if}

    </div><!-- /tab-content -->

    <!-- scan progress -->
    {#if scanning}
      <div class="scan-bar-wrap">
        <span class="scan-label">Scanning…{scanProgress ? ` ${scanProgress.done}/${scanProgress.total}` : ''}</span>
        {#if scanProgress && scanProgress.total > 0}
          <div class="scan-track">
            <div class="scan-fill" style="width:{(scanProgress.done/scanProgress.total)*100}%"></div>
          </div>
        {/if}
      </div>
    {/if}

    <!-- footer -->
    <div class="sidebar-footer">
      <button class="foot-btn" onclick={openFile}   title="Open audio file">+ File</button>
      <button class="foot-btn" onclick={openFolder} title="Add folder to queue">+ Folder</button>
      <button class="foot-btn accent" onclick={scanLibraryDialog} title="Scan music library">⟳ Library</button>
      {#if playlist.length > 0}
        <button class="foot-btn danger" onclick={() => Backend.ClearPlaylist()}>✕</button>
      {/if}
    </div>
  </aside>

  <!-- ════ RIGHT PLAYER ════════════════════════════════════════════════════ -->
  <main class="player">

    <!-- ambient gradient bg driven by accent -->
    <div class="player-bg" aria-hidden="true"></div>

    <!-- album art -->
    <div class="art-section">
      {#if track?.artBase64}
        <div class="art-wrap">
          <img class="art-glow" src={track.artBase64} alt="" aria-hidden="true" />
          <img class="art-img"  src={track.artBase64} alt="Album art" />
        </div>
      {:else}
        <div class="art-wrap art-empty">
          <span class="art-glyph">♫</span>
        </div>
      {/if}
    </div>

    <!-- track info -->
    <div class="track-info">
      {#if loading}
        <div class="loading-dots"><span></span><span></span><span></span></div>
      {:else if track}
        <h1 class="track-title">{track.title}</h1>
        <p class="track-sub">
          {#if track.artist}<span class="track-artist">{track.artist}</span>{/if}
          {#if track.artist && track.album}<span class="dot">·</span>{/if}
          {#if track.album}<span class="track-album">{track.album}</span>{/if}
        </p>
        <div class="badges">
          <span class="badge">{track.qualityLabel || track.format}</span>
          {#if track.isDSD}<span class="badge badge-dsd">{track.dsdLabel}</span>{/if}
          {#if track.outputMode}
            <span class="badge" class:badge-excl={track.outputMode==='exclusive'}>
              {track.outputMode === 'exclusive' ? '● EXCLUSIVE' : '○ SHARED'}
            </span>
          {/if}
        </div>
      {:else}
        <p class="track-empty">Drop files here or use the sidebar</p>
      {/if}
    </div>

    <!-- waveform -->
    <canvas bind:this={canvasEl} class="waveform" width="640" height="56"></canvas>

    <!-- progress -->
    <div class="progress-section">
      <span class="time">{posStr}</span>
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="progress-track" onclick={onProgressClick}>
        <div class="progress-fill" style="width:{progress}%">
          <div class="progress-thumb"></div>
        </div>
      </div>
      <span class="time">{durStr}</span>
    </div>

    <!-- transport -->
    <div class="transport">
      <button class="ctrl-btn" onclick={prev} title="Previous" disabled={!playlist.length}>
        <svg viewBox="0 0 24 24" fill="currentColor"><path d="M6 6h2v12H6zm3.5 6 8.5 6V6z"/></svg>
      </button>
      <button class="play-btn" onclick={isPlaying ? pause : play}
              title={isPlaying ? 'Pause' : 'Play'}
              disabled={!track && !playlist.length}>
        {#if isPlaying}
          <svg viewBox="0 0 24 24" fill="currentColor"><path d="M6 19h4V5H6v14zm8-14v14h4V5h-4z"/></svg>
        {:else}
          <svg viewBox="0 0 24 24" fill="currentColor"><path d="M8 5v14l11-7z"/></svg>
        {/if}
      </button>
      <button class="ctrl-btn" onclick={next} title="Next" disabled={!playlist.length}>
        <svg viewBox="0 0 24 24" fill="currentColor"><path d="M6 18l8.5-6L6 6v12zm2-8.14L11.03 12 8 14.14V9.86zM16 6h2v12h-2z"/></svg>
      </button>
      <button class="ctrl-btn" onclick={stop} title="Stop"
              disabled={!isPlaying && !isPaused}>
        <svg viewBox="0 0 24 24" fill="currentColor"><path d="M6 6h12v12H6z"/></svg>
      </button>
    </div>

    <!-- toggles -->
    <div class="toggles">
      <button class="tog" class:tog-on={isShuffle}  onclick={toggleShuffle}>
        <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M10.59 9.17L5.41 4 4 5.41l5.17 5.17zm4.76-.99l3.65 3.65-3.65 3.65V13h-1.76l-6.4-6.4 1.41-1.41 5.75 5.75V9.18zm.59 10.41v-2.59l-10-10H4V4h1.41l10 10H18V11.41l3.5 3.5z"/></svg>
        Shuffle
      </button>
      <button class="tog" class:tog-on={isRepeat} onclick={toggleRepeat}>
        <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M7 7h10v3l4-4-4-4v3H5v6h2zm10 10H7v-3l-4 4 4 4v-3h12v-6h-2z"/></svg>
        Repeat
      </button>
      <button class="tog" class:tog-on={showLyrics} onclick={() => showLyrics=!showLyrics}>
        <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M12 3v10.55A4 4 0 1 0 14 17V7h4V3z"/></svg>
        Lyrics
      </button>
      <button class="tog excl-tog" class:tog-on={isExclPref} onclick={toggleExclusive}>
        <svg viewBox="0 0 24 24" fill="currentColor" width="14" height="14"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-2 14.5v-9l6 4.5-6 4.5z"/></svg>
        {isExclPref ? 'Exclusive' : 'Shared'}
      </button>
    </div>

    <!-- volume -->
    <div class="volume-row">
      <svg class="vol-icon" viewBox="0 0 24 24" fill="currentColor" width="16" height="16">
        {#if volume === 0}
          <path d="M16.5 12c0-1.77-1.02-3.29-2.5-4.03v2.21l2.45 2.45c.03-.2.05-.41.05-.63zm2.5 0c0 .94-.2 1.82-.54 2.64l1.51 1.51C20.63 14.91 21 13.5 21 12c0-4.28-2.99-7.86-7-8.77v2.06c2.89.86 5 3.54 5 6.71zM4.27 3L3 4.27 7.73 9H3v6h4l5 5v-6.73l4.25 4.25c-.67.52-1.42.93-2.25 1.18v2.06c1.38-.31 2.63-.95 3.69-1.81L19.73 21 21 19.73l-9-9L4.27 3zM12 4L9.91 6.09 12 8.18V4z"/>
        {:else if volume < 0.5}
          <path d="M18.5 12c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02zM5 9v6h4l5 5V4L9 9H5z"/>
        {:else}
          <path d="M3 9v6h4l5 5V4L7 9H3zm13.5 3c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02zM14 3.23v2.06c2.89.86 5 3.54 5 6.71s-2.11 5.85-5 6.71v2.06c4.01-.91 7-4.49 7-8.77s-2.99-7.86-7-8.77z"/>
        {/if}
      </svg>
      <input type="range" min="0" max="1" step="0.01" value={volume}
             oninput={onVolumeInput} class="vol-slider"
             style="--vol:{volume}" />
      <span class="vol-pct">{Math.round(volume*100)}%</span>
    </div>

    {#if errorMsg}
      <div class="error-bar">{errorMsg} <button onclick={() => errorMsg=''}>✕</button></div>
    {/if}

  </main>

  <!-- ════ RIGHT LYRICS PANEL ══════════════════════════════════════════ -->
  {#if showLyrics && lyrics.length > 0}
    <aside class="lyrics-panel">
      <div class="lyrics-header">
        <span>Lyrics</span>
        <button class="lyrics-close" onclick={() => showLyrics=false}>✕</button>
      </div>
      <div class="lyrics-list">
        {#each lyrics as line, i}
          <p class="lyric"
             class:lyric-active={i===activeLyricIdx}
             class:lyric-near={Math.abs(i-activeLyricIdx)===1 && activeLyricIdx>=0}>
            {line.text || '·'}
          </p>
        {/each}
      </div>
    </aside>
  {/if}

</div>

<style>
  /* ── design tokens ──────────────────────────────────────────────────── */
  :root {
    --accent:     #1db954;
    --accent-rgb: 29,185,84;
    --accent-08:  rgba(29,185,84,0.08);
    --accent-15:  rgba(29,185,84,0.15);
    --accent-30:  rgba(29,185,84,0.30);
    --accent-50:  rgba(29,185,84,0.50);

    --bg:      #0a0a0c;
    --sf1:     #111115;
    --sf2:     #16161b;
    --sf3:     #1e1e25;
    --border:  #202028;
    --text:    #ebebf0;
    --muted:   #52525f;
    --muted2:  #3a3a45;
    --dsd:     #f5a623;
    --r:       8px;
  }
  * { box-sizing: border-box; margin: 0; padding: 0 }

  /* ── root layout ────────────────────────────────────────────────────── */
  .layout {
    display: grid;
    grid-template-columns: 260px 1fr;
    height: 100vh;
    background: var(--bg);
    color: var(--text);
    font-family: system-ui, -apple-system, 'Segoe UI', sans-serif;
    -webkit-font-smoothing: antialiased;
    overflow: hidden;
    transition: grid-template-columns 0.25s ease;
  }
  .layout.lyrics-open {
    grid-template-columns: 260px 1fr 300px;
  }

  /* ════════════════ SIDEBAR ═════════════════════════════════════════ */
  .sidebar {
    display: flex; flex-direction: column;
    background: var(--sf1);
    border-right: 1px solid var(--border);
    overflow: hidden;
  }

  /* tabs */
  .tabs {
    display: grid; grid-template-columns: repeat(4, 1fr);
    border-bottom: 1px solid var(--border); flex-shrink: 0
  }
  .tab {
    background: none; border: none; color: var(--muted);
    font-size: 0.65rem; font-weight: 700; letter-spacing: 0.06em;
    text-transform: uppercase; padding: 0.65rem 0; cursor: pointer;
    border-bottom: 2px solid transparent; margin-bottom: -1px;
    transition: color .15s
  }
  .tab:hover  { color: var(--text) }
  .tab.active { color: var(--accent); border-bottom-color: var(--accent) }

  /* content area */
  .tab-content { flex: 1; overflow-y: auto; overflow-x: hidden }

  /* empty state */
  .empty-state {
    display: flex; flex-direction: column; align-items: center;
    padding: 3rem 1rem; gap: 0.5rem; color: var(--muted)
  }
  .empty-icon { font-size: 2rem; opacity: 0.3 }
  .empty-state p { font-size: 0.78rem; text-align: center }

  /* count label */
  .list-count { font-size: 0.65rem; color: var(--muted); padding: 0.5rem 0.75rem }

  /* queue/playlist */
  .pl-list { list-style: none }
  .pl-item {
    display: grid; grid-template-columns: 1.8rem 1fr 1.2rem;
    align-items: center; gap: 0.25rem;
    padding: 0.42rem 0.6rem; cursor: pointer;
    transition: background .1s; border-left: 2px solid transparent
  }
  .pl-item:hover          { background: var(--sf2) }
  .pl-item.pl-current     { background: var(--accent-08); border-left-color: var(--accent) }
  .pl-num  { font-size: 0.62rem; color: var(--muted); text-align: right; font-variant-numeric: tabular-nums }
  .pl-current .pl-num   { color: var(--accent); font-weight: 700 }
  .pl-title { font-size: 0.77rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .pl-current .pl-title { color: var(--accent) }
  .pl-remove { font-size: 0.58rem; color: transparent; cursor: pointer; text-align: center; transition: color .1s }
  .pl-item:hover .pl-remove { color: var(--muted2) }
  .pl-remove:hover          { color: #f08080 !important }

  /* songs */
  .search-wrap { padding: 0.5rem 0.6rem; border-bottom: 1px solid var(--border) }
  .search-input {
    width: 100%; background: var(--sf2); border: 1px solid var(--border);
    color: var(--text); font-size: 0.76rem; padding: 0.35rem 0.6rem;
    border-radius: 6px; outline: none; transition: border-color .15s
  }
  .search-input:focus { border-color: var(--accent) }
  .search-input::placeholder { color: var(--muted) }

  .song-list { list-style: none }
  .song-item {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.45rem 0.7rem; cursor: pointer; border-left: 2px solid transparent;
    transition: background .1s
  }
  .song-item:hover { background: var(--sf2); border-left-color: var(--accent) }
  .song-main  { flex: 1; min-width: 0 }
  .song-title { display: block; font-size: 0.78rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .song-sub   { display: block; font-size: 0.66rem; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; margin-top: 1px }
  .song-play-icon { font-size: 0.6rem; color: transparent; margin-left: 0.4rem; transition: color .1s; flex-shrink: 0 }
  .song-item:hover .song-play-icon { color: var(--accent) }

  /* albums */
  .filter-bar {
    display: flex; align-items: center; gap: 0.5rem;
    padding: 0.45rem 0.6rem; background: var(--accent-08);
    border-bottom: 1px solid var(--border)
  }
  .filter-lbl   { flex: 1; font-size: 0.72rem; color: var(--accent); font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap }
  .filter-clear { background: none; border: none; color: var(--muted); cursor: pointer; font-size: 0.7rem; padding: 2px 4px }
  .filter-clear:hover { color: #f08080 }

  .album-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 5px; padding: 6px }
  .album-card {
    background: var(--sf2); border-radius: var(--r); overflow: hidden;
    cursor: pointer; transition: transform .18s, box-shadow .18s;
    border: 1px solid var(--border)
  }
  .album-card:hover {
    transform: translateY(-3px) scale(1.02);
    box-shadow: 0 8px 24px #00000070, 0 0 0 1px var(--card-accent, var(--accent))44
  }
  .album-art, .album-placeholder {
    width: 100%; aspect-ratio: 1; display: block; object-fit: cover
  }
  .album-placeholder {
    display: flex; align-items: center; justify-content: center;
    background: var(--sf3); color: var(--muted2); font-size: 1.6rem
  }
  .album-meta   { padding: 0.3rem 0.4rem 0.4rem }
  .album-title  { font-size: 0.68rem; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .album-artist { font-size: 0.62rem; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; margin-top: 1px }

  /* artists */
  .artist-list { list-style: none }
  .artist-item {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.52rem 0.75rem; cursor: pointer; border-left: 2px solid transparent;
    transition: background .1s; border-bottom: 1px solid var(--border)
  }
  .artist-item:hover       { background: var(--sf2) }
  .artist-item.artist-active { background: var(--accent-08); border-left-color: var(--accent) }
  .artist-name  { font-size: 0.8rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis }
  .artist-arrow { font-size: 1rem; color: var(--muted2) }
  .artist-item:hover .artist-arrow { color: var(--accent) }

  /* scan progress */
  .scan-bar-wrap {
    padding: 0.4rem 0.6rem; border-top: 1px solid var(--border);
    display: flex; flex-direction: column; gap: 0.25rem; flex-shrink: 0
  }
  .scan-label { font-size: 0.66rem; color: var(--muted) }
  .scan-track { height: 2px; background: var(--border); border-radius: 1px; overflow: hidden }
  .scan-fill  { height: 100%; background: var(--accent); border-radius: 1px; transition: width .3s }

  /* footer */
  .sidebar-footer {
    flex-shrink: 0; display: flex; gap: 3px;
    padding: 5px 6px; border-top: 1px solid var(--border)
  }
  .foot-btn {
    flex: 1; background: var(--sf2); border: 1px solid var(--border);
    color: var(--muted); font-size: 0.66rem; font-weight: 600;
    padding: 0.38rem 0; border-radius: 6px; cursor: pointer;
    transition: background .12s, color .12s
  }
  .foot-btn:hover        { background: var(--sf3); color: var(--text) }
  .foot-btn.accent       { color: var(--accent); border-color: var(--accent-30) }
  .foot-btn.accent:hover { background: var(--accent-15) }
  .foot-btn.danger       { color: #f08080; border-color: #7a202045 }
  .foot-btn.danger:hover { background: #3a1010 }

  /* ════════════════ PLAYER ══════════════════════════════════════════ */
  .player {
    position: relative;
    display: flex; flex-direction: column; align-items: center;
    gap: 0.7rem; padding: 1.6rem 2.4rem 1.2rem;
    overflow-y: auto;
  }

  /* ambient gradient behind player — reacts to accent */
  .player-bg {
    position: absolute; inset: 0; pointer-events: none; z-index: 0;
    background:
      radial-gradient(ellipse 80% 50% at 25% 0%, var(--accent-08) 0%, transparent 70%),
      radial-gradient(ellipse 60% 40% at 75% 100%, var(--accent-08) 0%, transparent 60%),
      var(--bg)
  }

  /* everything above the bg */
  .player > *:not(.player-bg) { position: relative; z-index: 1 }

  /* album art */
  .art-section { flex-shrink: 0 }
  .art-wrap {
    position: relative;
    width: clamp(180px, 32vw, 300px);
    aspect-ratio: 1; border-radius: 16px; overflow: hidden;
    box-shadow:
      0 30px 80px rgba(0,0,0,0.75),
      0 0 0 1px var(--border),
      0 0 50px var(--accent-15)
  }
  .art-wrap.art-empty {
    background: var(--sf1);
    display: flex; align-items: center; justify-content: center
  }
  .art-glyph { font-size: 5rem; color: var(--muted2) }
  .art-img {
    width: 100%; height: 100%; object-fit: cover;
    position: relative; z-index: 1
  }
  .art-glow {
    position: absolute; inset: -25%; width: 150%; height: 150%;
    object-fit: cover;
    filter: blur(40px) saturate(2.5) brightness(0.6);
    opacity: 0.55; z-index: 0
  }

  /* track info */
  .track-info { width: 100%; max-width: 640px; text-align: center }
  .track-title {
    font-size: 1.25rem; font-weight: 700; line-height: 1.3;
    letter-spacing: -0.01em;
    display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
    overflow: hidden
  }
  .track-sub {
    font-size: 0.84rem; color: var(--muted); margin-top: 0.25rem;
    display: flex; gap: 0.4rem; justify-content: center; align-items: center; flex-wrap: wrap
  }
  .track-artist { color: var(--text) }
  .dot          { color: var(--muted2) }
  .track-album  { font-style: italic }
  .badges {
    display: flex; gap: 0.3rem; justify-content: center; flex-wrap: wrap; margin-top: 0.45rem
  }
  .badge {
    font-size: 0.6rem; font-weight: 700; letter-spacing: 0.07em;
    padding: 2px 8px; border-radius: 20px;
    background: var(--sf2); color: var(--muted); border: 1px solid var(--border)
  }
  .badge-dsd  { color: var(--dsd); border-color: var(--dsd)44; background: var(--dsd)12 }
  .badge-excl { color: var(--accent); border-color: var(--accent-30); background: var(--accent-08) }
  .track-empty { color: var(--muted); font-size: 0.85rem }

  /* loading animation */
  .loading-dots { display: flex; gap: 6px; justify-content: center; padding: 1rem 0 }
  .loading-dots span {
    width: 7px; height: 7px; border-radius: 50%;
    background: var(--accent); animation: blink 1s ease-in-out infinite
  }
  .loading-dots span:nth-child(2) { animation-delay: .2s }
  .loading-dots span:nth-child(3) { animation-delay: .4s }
  @keyframes blink { 0%,80%,100%{opacity:.2} 40%{opacity:1} }

  /* waveform */
  .waveform { width: 100%; max-width: 640px; height: 56px; display: block; border-radius: 8px }

  /* progress */
  .progress-section {
    display: flex; align-items: center; gap: 0.6rem;
    width: 100%; max-width: 640px
  }
  .time {
    font-size: 0.68rem; color: var(--muted); min-width: 2.8rem;
    font-variant-numeric: tabular-nums; font-feature-settings: "tnum"
  }
  .progress-track {
    flex: 1; height: 4px; background: var(--sf3); border-radius: 2px;
    cursor: pointer; position: relative; transition: height .15s
  }
  .progress-track:hover { height: 6px }
  .progress-fill {
    height: 100%; background: linear-gradient(90deg, var(--accent) 0%, var(--accent) 100%);
    border-radius: 2px; position: relative; transition: width .25s linear
  }
  .progress-thumb {
    position: absolute; right: -6px; top: 50%;
    width: 12px; height: 12px; border-radius: 50%;
    background: #fff; box-shadow: 0 0 8px var(--accent-50);
    transform: translateY(-50%) scale(0);
    transition: transform .15s; pointer-events: none
  }
  .progress-track:hover .progress-thumb { transform: translateY(-50%) scale(1) }

  /* transport */
  .transport { display: flex; align-items: center; gap: 0.6rem }
  .ctrl-btn {
    background: none; border: none; color: var(--muted); cursor: pointer;
    padding: 0.5rem; border-radius: 50%; transition: color .15s, background .15s;
    display: flex; align-items: center; justify-content: center; width: 40px; height: 40px
  }
  .ctrl-btn svg { width: 20px; height: 20px }
  .ctrl-btn:hover:not(:disabled) { color: var(--text); background: var(--sf2) }
  .ctrl-btn:disabled { opacity: 0.2; cursor: not-allowed }
  .play-btn {
    background: var(--accent); border: none; color: #000; cursor: pointer;
    width: 54px; height: 54px; border-radius: 50%;
    display: flex; align-items: center; justify-content: center;
    box-shadow: 0 0 20px var(--accent-30), 0 4px 12px rgba(0,0,0,0.4);
    transition: transform .15s, box-shadow .15s, filter .15s
  }
  .play-btn svg { width: 24px; height: 24px }
  .play-btn:hover:not(:disabled) {
    transform: scale(1.07);
    box-shadow: 0 0 30px var(--accent-50), 0 6px 18px rgba(0,0,0,0.5);
    filter: brightness(1.12)
  }
  .play-btn:disabled { opacity: 0.3; cursor: not-allowed }

  /* toggles */
  .toggles { display: flex; gap: 0.4rem; flex-wrap: wrap; justify-content: center }
  .tog {
    display: flex; align-items: center; gap: 5px;
    background: none; border: 1px solid var(--border); color: var(--muted);
    font-size: 0.7rem; font-weight: 600; padding: 0.3rem 0.7rem;
    border-radius: 20px; cursor: pointer; transition: all .15s
  }
  .tog:hover   { border-color: var(--muted2); color: var(--text) }
  .tog.tog-on  { border-color: var(--accent); color: var(--accent); background: var(--accent-08) }
  .excl-tog.tog-on { border-color: #4caf50; color: #4caf50; background: #4caf5012 }

  /* volume */
  .volume-row {
    display: flex; align-items: center; gap: 0.6rem;
    width: 100%; max-width: 380px
  }
  .vol-icon { color: var(--muted); flex-shrink: 0 }
  .vol-slider {
    flex: 1;
    -webkit-appearance: none; height: 4px; border-radius: 2px;
    background: linear-gradient(90deg, var(--accent) calc(var(--vol)*100%), var(--sf3) 0%);
    cursor: pointer; outline: none
  }
  .vol-slider::-webkit-slider-thumb {
    -webkit-appearance: none; width: 14px; height: 14px; border-radius: 50%;
    background: #fff; box-shadow: 0 0 6px var(--accent-50); cursor: pointer
  }
  .vol-pct { font-size: 0.68rem; color: var(--muted); min-width: 2.5rem; text-align: right;
             font-variant-numeric: tabular-nums }

  /* ════════════════ LYRICS PANEL (right column) ═════════════════════ */
  .lyrics-panel {
    background: var(--sf1);
    border-left: 1px solid var(--border);
    display: flex; flex-direction: column;
    overflow: hidden;
    animation: slide-in .22s ease-out;
  }
  @keyframes slide-in {
    from { opacity: 0; transform: translateX(20px) }
    to   { opacity: 1; transform: translateX(0) }
  }
  .lyrics-header {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.7rem 1rem; border-bottom: 1px solid var(--border); flex-shrink: 0;
    font-size: 0.65rem; font-weight: 700; letter-spacing: 0.08em;
    text-transform: uppercase; color: var(--muted)
  }
  .lyrics-close {
    background: none; border: none; color: var(--muted); cursor: pointer;
    font-size: 0.75rem; padding: 2px 6px; border-radius: 4px;
    transition: color .12s
  }
  .lyrics-close:hover { color: var(--text) }
  .lyrics-list {
    flex: 1; overflow-y: auto; padding: 1.5rem 0;
    mask-image: linear-gradient(transparent, black 8%, black 92%, transparent);
    -webkit-mask-image: linear-gradient(transparent, black 8%, black 92%, transparent)
  }
  .lyric {
    font-size: 0.82rem; color: var(--muted2); text-align: center;
    padding: 0.28rem 1.2rem; line-height: 1.8; transition: all .22s ease-out;
    cursor: default
  }
  .lyric-near   { font-size: 0.9rem; color: var(--muted) }
  .lyric-active {
    font-size: 1.02rem; font-weight: 700; color: var(--accent);
    padding: 0.38rem 1.2rem
  }

  /* error */
  .error-bar {
    width: 100%; max-width: 640px;
    background: #2a0f0f; border: 1px solid #6b1818; border-radius: var(--r);
    padding: 0.4rem 0.75rem; font-size: 0.75rem; color: #f08080;
    display: flex; justify-content: space-between; align-items: center; gap: 0.5rem
  }
  .error-bar button { background: none; border: none; color: var(--muted); cursor: pointer; font-size: 0.8rem }
</style>
