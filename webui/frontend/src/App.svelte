<script lang="ts">
  import { EventsOn } from '../wailsjs/runtime/runtime.js'
  import * as Backend from '../wailsjs/go/main/App.js'
  import './lib/tokens.css'
  import { s, d } from './lib/stores.svelte'
  import type { TrackInfo, PlaylistTrack, PlaylistMeta, LyricLine, AlbumData } from './lib/stores.svelte'
  import {
    togglePlay, next, prev, toggleShuffle, toggleRepeat,
    refreshLists, loadDevices, handlePositionChange, applyTrack,
  } from './lib/player'
  import { initViz, setVizData } from './lib/viz.svelte'
  import { initLyricFlow, reduceMotion } from './lib/lyricflow.svelte'
  import Sidebar from './lib/Sidebar.svelte'
  import Player from './lib/Player.svelte'
  import LyricsPanel from './lib/LyricsPanel.svelte'
  import NowPlaying from './lib/NowPlaying.svelte'
  import SettingsModal from './lib/SettingsModal.svelte'
  import AddToPlaylistModal from './lib/AddToPlaylistModal.svelte'

  // Visualizer engine: rAF loop + accent palette tween (effects live for the
  // app's lifetime).
  initViz()

  // Lyric flow engine: interpolated clock + spring scroll + karaoke sweep
  // (inert under prefers-reduced-motion — the fallback effect below scrolls).
  initLyricFlow()

  // ── lyric auto-scroll (reduced-motion fallback only) ─────────────────────────
  // Normally lyricflow.svelte.ts spring-scrolls the active surface. Under
  // prefers-reduced-motion that engine is inert and LyricLines keeps the
  // static bucket renderer, so jump (no smooth crawl) to the line here.
  // Scope the query to whichever lyric surface is on screen so the two copies
  // (side panel + Now Playing) don't fight over the scroll position.
  // NEVER scrollIntoView here: it scrolls EVERY scrollable ancestor — including
  // overflow:hidden boxes like .layout, which are still programmatically
  // scrollable — so near the end of a song (active line can't be centred) the
  // leftover delta shoved the WHOLE UI upward. Container-scoped math instead,
  // clamped so the surface's own scrollTop can never overshoot either.
  const activeLyricIdx = $derived(d.activeLyricIdx)
  $effect(() => {
    if (!reduceMotion) return
    if (activeLyricIdx < 0) return
    const scope = s.showNowPlaying ? '.np-lyrics' : (s.showLyrics ? '.lyrics-list' : null)
    if (!scope) return
    const container = document.querySelector(scope) as HTMLElement | null
    if (!container) return
    const el = container.querySelector('.lyric-active') as HTMLElement | null
    if (!el) return
    // Rect-based offset (container may not be the offsetParent).
    const base = container.getBoundingClientRect().top - container.scrollTop
    const r = el.getBoundingClientRect()
    const center = r.top - base + r.height / 2
    const target = center - container.clientHeight / 2
    container.scrollTop = Math.max(0, Math.min(target, container.scrollHeight - container.clientHeight))
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
    // startup() restored the queue into the backend playlist but emits no
    // playlist-updated (the frontend isn't listening yet at that point) —
    // pull the snapshot once so the Queue tab isn't empty until playback.
    Backend.GetPlaylist().then(pl => { if (pl?.length) s.playlist = pl })
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
      const target = e.target as HTMLElement
      // Ignore when typing in an input
      if (target.tagName === 'INPUT') return
      // When a button-like element has focus, Space/Enter activate THAT
      // control (real <button>s natively, focusable rows via their rowKey
      // handler) — the global play/pause shortcut must not double-fire.
      // Other shortcuts (arrows, S/R/L…) keep working with a button focused.
      if ((e.code === 'Space' || e.code === 'Enter') &&
          (target.tagName === 'BUTTON' || target.getAttribute('role') === 'button')) return

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
          if (s.lyricSyncMode)        { s.lyricSyncMode = false }
          else if (s.addMenuPath !== null) { s.addMenuPath = null }
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
      // applyTrack resets pos/dur/lyrics alongside the track — shared with the
      // button paths (Next/Prev/PlayAt return TrackInfo without this event).
      EventsOn('track-change', (info:TrackInfo) => applyTrack(info)),
      // Online art lookup result (artfetch.go) — only applied if the track is
      // still the one playing; stale results just stay in the disk cache.
      EventsOn('art-found', (a:{path:string;artBase64:string;accentHex:string}) => {
        if (s.track && s.track.path === a.path) {
          s.track.artBase64 = a.artBase64
          s.track.hasArt = true
          s.track.accentHex = a.accentHex
        }
      }),
      // Cover resolved online for a library album without embedded art —
      // patch the matching card in the Albums grid.
      EventsOn('album-art-found', (a:{artist:string;title:string;artBase64:string;accentHex:string}) => {
        const al = s.albums.find(x => x.artist === a.artist && x.title === a.title)
        if (al) { al.artBase64 = a.artBase64; al.accentHex = a.accentHex }
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
