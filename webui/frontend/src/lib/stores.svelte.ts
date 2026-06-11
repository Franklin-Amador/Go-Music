// ── Shared reactive state (Svelte 5 runes in a module) ───────────────────────
// Every piece of cross-component state lives on the single `s` object below.
// Components mutate it directly (`s.volume = …`); reactivity is preserved
// because `$state` proxies are deeply reactive across module boundaries.

// ── types ───────────────────────────────────────────────────────────────────
export interface TrackInfo {
  path: string; title: string; artist: string; album: string
  format: string; qualityLabel: string; dsdLabel: string
  duration: number; isDSD: boolean; hasArt: boolean
  artBase64: string; accentHex: string; outputMode: string
}
export interface PlaylistTrack { index: number; path: string; title: string; current: boolean; duration: number }
export interface PlaylistMeta  { name: string; count: number }
export interface OutputDevice  { id: string; name: string; isDefault: boolean }
export interface LyricLine    { timeSec: number; text: string }
export interface AlbumData    { artist: string; title: string; year: number; artBase64: string; accentHex: string; trackCount: number }
export interface SongData     { path: string; title: string; artist: string; album: string; trackNum: number }

export const s = $state({
  // ── playback state ─────────────────────────────────────────────────────────
  track:       null as TrackInfo | null,
  playerState: 'Stopped',
  pos:         0,
  dur:         0,
  dragging:    false,               // user is scrubbing the progress bar
  seekPreview: null as number | null, // optimistic position (s) while scrubbing / until the seek is confirmed
  volume:      1.0,
  playlist:    [] as PlaylistTrack[],
  lyrics:        [] as LyricLine[],
  lyricsFetched: false,  // true once the first fetch has returned (empty or not)
  lyricsRetrying: false,
  waveform:    [] as number[],
  errorMsg:    '',
  loading:     false,
  showLyrics:  false,
  isShuffle:   false,
  isRepeat:    false,
  isExclPref:  false,  // shared by default — user opts into exclusive

  // ── library state ──────────────────────────────────────────────────────────
  albums:       [] as AlbumData[],
  artists:      [] as string[],
  songs:        [] as SongData[],
  songsLoaded:  false,
  artistFilter: '',
  songSearch:   '',
  scanning:     false,
  scanProgress: null as {done:number;total:number}|null,

  // ── queue (Queue tab) state ────────────────────────────────────────────────
  queueSearch:   '',
  dragIndex:     null as number|null,   // item being dragged
  dragOverIndex: null as number|null,   // item currently hovered as drop target

  // ── named playlists (Lists tab) state ──────────────────────────────────────
  playlists:      [] as PlaylistMeta[],
  openListName:   null as string|null,        // expanded list showing its tracks
  openListTracks: [] as PlaylistTrack[],
  creatingList:   false,
  newListName:    '',
  // "Add to playlist" menu — set to the song path whose menu is open
  addMenuPath:    null as string|null,
  addMenuTitle:   '',
  addMenuNew:     '',

  // ── UI state ───────────────────────────────────────────────────────────────
  tab:            'playlist' as 'playlist'|'lists'|'albums'|'artists'|'songs',
  canvasEl:       null as HTMLCanvasElement|null,
  npCanvasEl:     null as HTMLCanvasElement|null,   // Now Playing visualizer
  showSettings:   false,
  showNowPlaying: false,
  npIdle:         false,               // controls hidden after inactivity
  npLyricsVisible: true,               // lyrics column toggle inside immersive
  upNext:         null as PlaylistTrack | null,
  crossfade:      0,
  visualizerMode: 'bars' as 'bars'|'wave'|'radial',
  accentSource:   'auto' as 'auto'|'fixed',
  outputDevices:  [] as OutputDevice[],
  selectedDevice: '',                  // hex id, '' = system default
  devicesLoading: false,
})

// ── derived (computed on access — reads stay reactive in templates/effects) ──
export const d = {
  get isPlaying() { return s.playerState === 'Playing' },
  get isPaused()  { return s.playerState === 'Paused' },
  // displayPos drives the progress bar + time label. While scrubbing (or in the
  // brief window after release, before the engine's reported position catches
  // up) it reflects the optimistic seek target so the bar never lags the cursor.
  get displayPos() { return s.seekPreview !== null ? s.seekPreview : s.pos },
  get accentColor() { return s.accentSource === 'fixed' ? '#1db954' : (s.track?.accentHex || '#1db954') },
  get progress() { return s.dur > 0 ? (this.displayPos / s.dur) * 100 : 0 },
  get filteredPlaylist() {
    return s.queueSearch
      ? s.playlist.filter(t => t.title.toLowerCase().includes(s.queueSearch.toLowerCase()))
      : s.playlist
  },
  get filteredAlbums() { return s.artistFilter ? s.albums.filter(a => a.artist === s.artistFilter) : s.albums },
  get filteredSongs() {
    return s.songSearch
      ? s.songs.filter(x =>
          x.title.toLowerCase().includes(s.songSearch.toLowerCase()) ||
          x.artist.toLowerCase().includes(s.songSearch.toLowerCase()) ||
          x.album.toLowerCase().includes(s.songSearch.toLowerCase()))
      : s.songs
  },
  // Plain (unsynced) lyrics have every timeSec = -1; the DoF renderer and
  // auto-scroll only make sense when at least one line carries a timestamp.
  get lyricsSynced() { return s.lyrics.some(l => l.timeSec >= 0) },
  get activeLyricIdx() {
    if (!s.lyrics.length) return -1
    let idx = -1
    for (let i = 0; i < s.lyrics.length; i++) {
      if (s.lyrics[i].timeSec >= 0 && s.lyrics[i].timeSec <= s.pos) idx = i
    }
    return idx
  },
}
