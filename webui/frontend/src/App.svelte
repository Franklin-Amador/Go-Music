<script lang="ts">
  import { EventsOn } from '../wailsjs/runtime/runtime.js'
  import * as Backend from '../wailsjs/go/main/App.js'
  import './lib/tokens.css'
  import { s, d } from './lib/stores.svelte'
  import type { TrackInfo, PlaylistTrack, PlaylistMeta, LyricLine, AlbumData } from './lib/stores.svelte'
  import {
    togglePlay, next, prev, toggleShuffle, toggleRepeat,
    refreshLists, loadDevices, handlePositionChange,
  } from './lib/player'
  import { initViz, setVizData } from './lib/viz.svelte'
  import Sidebar from './lib/Sidebar.svelte'
  import Player from './lib/Player.svelte'
  import LyricsPanel from './lib/LyricsPanel.svelte'
  import NowPlaying from './lib/NowPlaying.svelte'
  import SettingsModal from './lib/SettingsModal.svelte'
  import AddToPlaylistModal from './lib/AddToPlaylistModal.svelte'

  // Visualizer engine: rAF loop + accent palette tween (effects live for the
  // app's lifetime).
  initViz()

  // ── lyric auto-scroll ────────────────────────────────────────────────────────
  // Scope the query to whichever lyric surface is on screen so the two copies
  // (side panel + Now Playing) don't fight over scrollIntoView.
  const activeLyricIdx = $derived(d.activeLyricIdx)
  $effect(() => {
    if (activeLyricIdx < 0) return
    const scope = s.showNowPlaying ? '.np-lyrics' : (s.showLyrics ? '.lyrics-list' : null)
    if (!scope) return
    document.querySelector(`${scope} .lyric-active`)?.scrollIntoView({behavior:'smooth',block:'center'})
  })

  // ── lazy load songs when tab opens ──────────────────────────────────────────
  $effect(() => {
    if (s.tab === 'songs' && !s.songsLoaded) {
      s.songsLoaded = true
      Backend.GetLibrarySongs().then(sg => { s.songs = sg ?? [] })
    }
  })

  // ── reload output devices each time Settings opens (catches hot-plug) ────────
  $effect(() => {
    if (s.showSettings) loadDevices()
  })

  // ── immersive: keep "Up next" fresh ──────────────────────────────────────────
  // Re-query whenever the track or queue changes (covers shuffle order too,
  // since PeekNext lives in Go). Touch the deps so the effect tracks them.
  $effect(() => {
    void s.track; void s.playlist; void s.isShuffle
    Backend.UpNext().then(t => { s.upNext = t }).catch(() => { s.upNext = null })
  })

  // ── immersive: auto-hide controls after 3s of mouse inactivity ───────────────
  $effect(() => {
    if (!s.showNowPlaying) { s.npIdle = false; return }
    let timer: number
    const reset = () => {
      s.npIdle = false
      clearTimeout(timer)
      timer = setTimeout(() => { s.npIdle = true }, 3000) as unknown as number
    }
    reset()
    window.addEventListener('mousemove', reset)
    return () => { window.removeEventListener('mousemove', reset); clearTimeout(timer) }
  })

  // ── Restore persisted state on startup ───────────────────────────────────────
  $effect(() => {
    Backend.GetInitialState().then(cfg => {
      if (!cfg) return
      s.volume         = cfg.volume    ?? 1.0
      s.isExclPref     = cfg.exclusive ?? false
      s.isShuffle      = cfg.shuffle   ?? false
      s.isRepeat       = cfg.repeat    ?? false
      s.crossfade      = cfg.crossfade ?? 0
      s.visualizerMode = (cfg.visualizerMode as 'bars'|'wave'|'radial') || 'bars'
      s.accentSource   = (cfg.accentSource as 'auto'|'fixed') || 'auto'
      s.selectedDevice = cfg.outputDevice ?? ''
      // Engine already applied these in startup(); just sync the UI toggles.
    })
    refreshLists()
  })

  // ── Save on window close (pagehide fires before WebView tears down) ──────────
  $effect(() => {
    const save = () => Backend.SaveConfig()
    window.addEventListener('pagehide', save)
    return () => window.removeEventListener('pagehide', save)
  })

  // ── Keyboard shortcuts ────────────────────────────────────────────────────────
  $effect(() => {
    function onKey(e: KeyboardEvent) {
      // Ignore when typing in an input
      if ((e.target as HTMLElement).tagName === 'INPUT') return

      switch (e.code) {
        case 'Space':
          e.preventDefault()
          togglePlay()
          break
        case 'ArrowRight':
          if (e.ctrlKey) { e.preventDefault(); next() }
          else           { e.preventDefault(); Backend.Seek(Math.min(s.pos + 5, s.dur)) }
          break
        case 'ArrowLeft':
          if (e.ctrlKey) { e.preventDefault(); prev() }
          else           { e.preventDefault(); Backend.Seek(Math.max(s.pos - 5, 0)) }
          break
        case 'ArrowUp':
          e.preventDefault()
          s.volume = Math.min(1, s.volume + 0.05)
          Backend.SetVolume(s.volume)
          break
        case 'ArrowDown':
          e.preventDefault()
          s.volume = Math.max(0, s.volume - 0.05)
          Backend.SetVolume(s.volume)
          break
        case 'KeyS': toggleShuffle(); break
        case 'KeyR': toggleRepeat();  break
        case 'KeyL': s.showLyrics = !s.showLyrics; break
        case 'Escape':
          if (s.addMenuPath !== null) { s.addMenuPath = null }
          else if (s.showSettings)    { s.showSettings = false }
          else if (s.showNowPlaying)  { s.showNowPlaying = false }
          else                        { s.songSearch = ''; s.artistFilter = '' }
          break
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  // ── Wails events ─────────────────────────────────────────────────────────────
  $effect(() => {
    const off = [
      EventsOn('state-change', (st:string) => {
        s.playerState = st
        if (st === 'Stopped') { s.pos = 0; s.loading = false; s.seekPreview = null }
        if (st === 'Loading')  s.loading = true
        if (st === 'Playing')  s.loading = false
      }),
      EventsOn('position-change', handlePositionChange),
      // Visualizer push (~30 fps from Go, only while playing) — replaces the
      // old per-frame GetSpectrum/GetWaveform IPC polling.
      EventsOn('viz-data', setVizData),
      EventsOn('track-change',    (info:TrackInfo) => {
        s.track=info; s.loading=false; s.errorMsg=''
        s.seekPreview = null
        s.lyrics=[]; s.lyricsFetched=false; s.lyricsRetrying=false
      }),
      EventsOn('playlist-updated',(pl:PlaylistTrack[]) => { s.playlist=pl }),
      EventsOn('playlists-updated',(p:PlaylistMeta[]) => {
        s.playlists = p ?? []
        if (s.openListName) Backend.GetPlaylistTracks(s.openListName).then(t => { s.openListTracks = t ?? [] })
      }),
      EventsOn('lyrics', (lines:LyricLine[]|null) => {
        s.lyrics = lines ?? []
        s.lyricsFetched = true
        s.lyricsRetrying = false
      }),
      EventsOn('audio-error',     (msg:string) => { s.errorMsg=msg; s.loading=false; s.scanning=false }),
      EventsOn('playback-finished',() => { s.playerState='Stopped'; s.pos=0 }),
      EventsOn('library-updated', (data:{albums:AlbumData[];artists:string[]}) => {
        s.albums=data.albums ?? []
        s.artists=data.artists ?? []
        // Reload songs if tab already visited
        if (s.songsLoaded) Backend.GetLibrarySongs().then(sg => { s.songs = sg ?? [] })
      }),
      EventsOn('scan-progress', (p:{done:number;total:number}) => { s.scanProgress=p }),
      EventsOn('scan-done',     () => { s.scanning=false; s.scanProgress=null }),
      EventsOn('wails:file-drop', (paths:string[]) => {
        Backend.AddFiles(paths).then(pl => { s.playlist=pl })
      }),
    ]
    return () => off.forEach(f => f())
  })
</script>

<!-- ═══════════════════════════════════════════════════════════════════════════ -->

<div class="layout" class:lyrics-open={s.showLyrics}>
  <Sidebar />
  <Player />
  <LyricsPanel />
  <NowPlaying />
  <SettingsModal />
  <AddToPlaylistModal />
</div>

<style>
  /* ── root layout — proporciones fluidas ─────────────────────────────── */
  .layout {
    display: grid;
    grid-template-columns: clamp(200px, 23vw, 300px) 1fr;
    height: 100vh;
    color: var(--text);
    font-family: system-ui, -apple-system, 'Segoe UI', sans-serif;
    -webkit-font-smoothing: antialiased;
    overflow: hidden;
    transition: grid-template-columns 0.28s cubic-bezier(0.4, 0, 0.2, 1);
    /* Subtle ambient tint across the whole window */
    background:
      radial-gradient(ellipse 120% 120% at 50% 50%,
        rgba(var(--accent-rgb), 0.025) 0%, transparent 65%),
      var(--bg);
  }
  .layout.lyrics-open {
    grid-template-columns: clamp(200px, 23vw, 300px) 1fr clamp(200px, 22vw, 310px);
  }
</style>
