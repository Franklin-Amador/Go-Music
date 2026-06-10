package main

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"gomusic/audio"
	"gomusic/library"
	"gomusic/playlist"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ── JSON types sent to the frontend ──────────────────────────────────────────

type TrackInfo struct {
	Path         string  `json:"path"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist"`
	Album        string  `json:"album"`
	Format       string  `json:"format"`
	QualityLabel string  `json:"qualityLabel"`
	DSDLabel     string  `json:"dsdLabel"`
	Duration     float64 `json:"duration"`
	IsDSD        bool    `json:"isDSD"`
	HasArt       bool    `json:"hasArt"`
	ArtBase64    string  `json:"artBase64"`  // "data:image/jpeg;base64,..."
	AccentHex    string  `json:"accentHex"`  // "#rrggbb" from dominant color
	OutputMode   string  `json:"outputMode"` // "exclusive" | "shared" | ""
}

type PositionInfo struct {
	Pos float64 `json:"pos"`
	Dur float64 `json:"dur"`
}

// VizData is the visualizer push payload: one event carries both the FFT
// spectrum and the peak-envelope waveform, replacing the old per-frame
// GetSpectrum/GetWaveform IPC polling from the frontend.
type VizData struct {
	Spectrum []float32 `json:"spectrum"`
	Waveform []float32 `json:"waveform"`
}

type PlaylistTrack struct {
	Index   int    `json:"index"`
	Path    string `json:"path"`
	Title   string `json:"title"`
	Current bool   `json:"current"`
}

type LyricLine struct {
	TimeSec float64 `json:"timeSec"` // -1 for plain (unsynced)
	Text    string  `json:"text"`
}

// ── App struct ────────────────────────────────────────────────────────────────

// App is the Wails application struct — all exported methods are callable from JS.
//
// Threading rule (mirrors AGENTS.md §9):
//   All engine callbacks run on internal goroutines. Every UI mutation
//   goes through runtime.EventsEmit, which posts to the WebView's thread.
//   Never call loadAndPlay from inside an engine callback without `go`.
type App struct {
	ctx context.Context
	eng *audio.Engine
	pl  *playlist.Playlist

	// repeatOne: when true, OnFinished replays the current track instead of advancing.
	repeatOne atomic.Bool

	// lastPreloadPath: prevents double-Preload for the same next track.
	lastPreloadPath string
	preloadMu       sync.Mutex

	// mu serialises all calls to loadAndPlay so concurrent next/prev/playAt
	// calls never race the engine's Load/Play sequence.
	mu sync.Mutex

	// library — guarded by libMu.
	lib   *library.Library
	libMu sync.Mutex

	// UI-only preferences mirrored here so saveConfig can persist them.
	// Written from JS via the Set* binds; read on shutdown. The races are
	// benign (last-writer-wins on a short string).
	uiVisualizerMode string
	uiAccentSource   string

	// Named playlists (separate from the live queue). Persisted in
	// playlists-web.json; guarded by namedListsMu.
	namedLists   []SavedPlaylist
	namedListsMu sync.Mutex

	// Visualizer push ticker (~30 fps "viz-data" events while playing).
	// vizStop is non-nil iff the ticker goroutine is running; guarded by vizMu.
	vizStop chan struct{}
	vizMu   sync.Mutex
}

func NewApp() *App {
	return &App{
		eng: audio.NewEngine(),
		pl:  playlist.New(),
	}
}

// startup is called once by Wails after the WebView is ready.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Restore persisted settings before wiring callbacks.
	cfg := loadConfig()
	a.eng.Volume = cfg.Volume
	a.eng.SetExclusiveMode(cfg.Exclusive)
	a.eng.SetCrossfade(cfg.Crossfade)
	a.eng.SetOutputDevice(cfg.OutputDevice)
	if cfg.Shuffle {
		a.pl.ToggleShuffle()
	}
	a.repeatOne.Store(cfg.Repeat)
	a.uiVisualizerMode = cfg.VisualizerMode
	a.uiAccentSource = cfg.AccentSource

	// Restore saved playlists.
	a.namedLists = loadNamedPlaylists()

	// Restore playlist (paths only — no auto-play on startup).
	for _, path := range cfg.Playlist {
		a.pl.Add(path)
	}
	if cfg.Current >= 0 && cfg.Current < len(a.pl.Tracks) {
		a.pl.SetCurrent(cfg.Current)
	}

	// ── engine callbacks → Wails events ──────────────────────────────────────

	a.eng.OnStateChange = func(s audio.State) {
		// Push visualizer data only while actually playing — the ticker is
		// the sole source of viz frames, so idle/paused means zero viz work.
		a.setVizPush(s == audio.StatePlaying)
		runtime.EventsEmit(a.ctx, "state-change", s.String())
	}

	a.eng.OnPositionChange = func(pos, dur float64) {
		runtime.EventsEmit(a.ctx, "position-change", PositionInfo{Pos: pos, Dur: dur})

		// Trigger crossfade preload ~(cfSec+6) seconds before the end.
		cfSec := a.eng.CrossfadeSeconds()
		if cfSec > 0 && dur > 0 && pos > 0 && (dur-pos) < cfSec+6 {
			if next := a.pl.PeekNext(); next != nil {
				a.preloadMu.Lock()
				if next.Path != a.lastPreloadPath {
					a.lastPreloadPath = next.Path
					a.eng.Preload(next.Path)
				}
				a.preloadMu.Unlock()
			}
		}
	}

	a.eng.OnError = func(msg string) {
		runtime.EventsEmit(a.ctx, "audio-error", msg)
	}

	// OnFinished: natural end of the last track (no crossfade).
	a.eng.OnFinished = func() {
		if a.repeatOne.Load() {
			if t := a.pl.CurrentTrack(); t != nil {
				go a.loadAndPlay(t.Path)
			}
			return
		}
		next := a.pl.Next()
		if next == nil {
			a.setVizPush(false)
			runtime.EventsEmit(a.ctx, "state-change", "Stopped")
			return
		}
		go a.loadAndPlay(next.Path)
	}

	// OnTrackTransition: engine just swapped to the preloaded next track via crossfade.
	// We must NOT call Load — the engine has already promoted the track internally.
	// We just update the UI and advance the playlist cursor.
	a.eng.OnTrackTransition = func(info *audio.FileInfo) {
		// Match path in playlist and jump cursor there (AGENTS.md: engine is
		// ignorant of playlist; UI side does the SetCurrent).
		for i, t := range a.pl.Tracks {
			if t.Path == info.Path {
				a.pl.SetCurrent(i)
				break
			}
		}
		a.preloadMu.Lock()
		a.lastPreloadPath = ""
		a.preloadMu.Unlock()

		ti := toTrackInfo(info, a.eng.OutputMode())
		a.setWindowTitle(info)
		runtime.EventsEmit(a.ctx, "track-change", ti)
		runtime.EventsEmit(a.ctx, "playlist-updated", a.playlistSnapshot())
		go a.fetchAndEmitLyrics(info)
	}

	// Emit restored playlist so the frontend tab populates immediately.
	if len(a.pl.Tracks) > 0 {
		runtime.EventsEmit(a.ctx, "playlist-updated", a.playlistSnapshot())
	}

	// Try loading the library gob cache from the previous session.
	go a.tryLoadLibraryCache()
}

func (a *App) shutdown(_ context.Context) {
	a.setVizPush(false)
	a.eng.Stop()
	a.saveConfig()
}

// setVizPush starts/stops the ~30 fps goroutine that pushes "viz-data" events
// (spectrum + waveform in a single payload) to the frontend. Idempotent and
// mutex-guarded: starting while running or stopping while stopped is a no-op,
// so it can be called from every state transition. The goroutine exits on
// stop or when the Wails context is canceled (shutdown) — no leak, no emit
// after teardown. It only READS the engine's Vis snapshots, same as the
// GetSpectrum/GetWaveform binds — no audio hot path involved.
func (a *App) setVizPush(on bool) {
	a.vizMu.Lock()
	defer a.vizMu.Unlock()

	if !on {
		if a.vizStop != nil {
			close(a.vizStop)
			a.vizStop = nil
		}
		return
	}
	if a.vizStop != nil || a.ctx == nil {
		return // already running / not started yet
	}
	stop := make(chan struct{})
	a.vizStop = stop
	ctx := a.ctx
	go func() {
		t := time.NewTicker(33 * time.Millisecond) // ~30 fps
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				runtime.EventsEmit(ctx, "viz-data", VizData{
					Spectrum: a.eng.Vis.Snapshot(),
					Waveform: a.eng.Vis.Waveform(96, 38),
				})
			}
		}
	}()
}

// ── internal helpers ──────────────────────────────────────────────────────────

// loadAndPlay is the single path for loading and starting a track.
// It is mu-guarded so concurrent calls (e.g. user presses Next twice) serialize.
func (a *App) loadAndPlay(path string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.eng.ClearPreload()
	a.preloadMu.Lock()
	a.lastPreloadPath = ""
	a.preloadMu.Unlock()

	info, err := a.eng.Load(path)
	if err != nil {
		runtime.EventsEmit(a.ctx, "audio-error", err.Error())
		return
	}
	if err := a.eng.Play(); err != nil {
		runtime.EventsEmit(a.ctx, "audio-error", err.Error())
		return
	}
	ti := toTrackInfo(info, a.eng.OutputMode())
	a.setWindowTitle(info)
	runtime.EventsEmit(a.ctx, "track-change", ti)
	runtime.EventsEmit(a.ctx, "playlist-updated", a.playlistSnapshot())
	go a.fetchAndEmitLyrics(info)
}

func (a *App) setWindowTitle(info *audio.FileInfo) {
	if info == nil {
		runtime.WindowSetTitle(a.ctx, "Go Music")
		return
	}
	title := info.Title
	if title == "" {
		title = filepath.Base(info.Path)
	}
	if info.Artist != "" {
		runtime.WindowSetTitle(a.ctx, info.Artist+" — "+title+" · Go Music")
	} else {
		runtime.WindowSetTitle(a.ctx, title+" · Go Music")
	}
}

func (a *App) fetchAndEmitLyrics(info *audio.FileInfo) {
	lines := audio.FetchLyrics(info)
	runtime.EventsEmit(a.ctx, "lyrics", toLyricLines(lines))
}

func (a *App) playlistSnapshot() []PlaylistTrack {
	out := make([]PlaylistTrack, len(a.pl.Tracks))
	for i, t := range a.pl.Tracks {
		out[i] = PlaylistTrack{
			Index:   i,
			Path:    t.Path,
			Title:   t.Title,
			Current: i == a.pl.Current,
		}
	}
	return out
}

// ── Bound methods — file / playlist management ────────────────────────────────

// OpenFileDialog opens a native single-file picker.
func (a *App) OpenFileDialog() string {
	path, _ := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Open Audio File",
		Filters: []runtime.FileFilter{
			{DisplayName: "Audio Files (*.flac;*.wav;*.mp3;*.dsf;*.dff)", Pattern: "*.flac;*.wav;*.mp3;*.dsf;*.dff"},
		},
	})
	return path
}

// OpenFolderDialog opens a native folder picker.
func (a *App) OpenFolderDialog() string {
	dir, _ := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Add Music Folder",
	})
	return dir
}

// LoadFile loads a single audio file, adds it to the playlist if not already
// there, and starts playback. Returns the track info.
func (a *App) LoadFile(path string) (*TrackInfo, error) {
	a.pl.Add(path)
	// Find the index we just added and set it as current.
	for i, t := range a.pl.Tracks {
		if t.Path == path {
			a.pl.SetCurrent(i)
			break
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	a.eng.ClearPreload()
	info, err := a.eng.Load(path)
	if err != nil {
		return nil, err
	}
	if err := a.eng.Play(); err != nil {
		return nil, err
	}
	ti := toTrackInfo(info, a.eng.OutputMode())
	runtime.EventsEmit(a.ctx, "playlist-updated", a.playlistSnapshot())
	go a.fetchAndEmitLyrics(info)
	return ti, nil
}

// AddFiles adds one or more audio files to the playlist and returns the updated list.
func (a *App) AddFiles(paths []string) []PlaylistTrack {
	for _, p := range paths {
		a.pl.Add(p)
	}
	snap := a.playlistSnapshot()
	runtime.EventsEmit(a.ctx, "playlist-updated", snap)
	return snap
}

// AddFolder recursively adds all supported audio files in dir to the playlist.
func (a *App) AddFolder(dir string) []PlaylistTrack {
	_ = a.pl.AddDir(dir)
	snap := a.playlistSnapshot()
	runtime.EventsEmit(a.ctx, "playlist-updated", snap)
	return snap
}

// GetPlaylist returns the current playlist.
func (a *App) GetPlaylist() []PlaylistTrack {
	return a.playlistSnapshot()
}

// PlayAt loads and plays track at index in the playlist.
func (a *App) PlayAt(index int) (*TrackInfo, error) {
	if index < 0 || index >= len(a.pl.Tracks) {
		return nil, nil
	}
	a.pl.SetCurrent(index)
	path := a.pl.Tracks[index].Path

	a.mu.Lock()
	defer a.mu.Unlock()
	a.eng.ClearPreload()
	a.preloadMu.Lock()
	a.lastPreloadPath = ""
	a.preloadMu.Unlock()

	info, err := a.eng.Load(path)
	if err != nil {
		return nil, err
	}
	if err := a.eng.Play(); err != nil {
		return nil, err
	}
	ti := toTrackInfo(info, a.eng.OutputMode())
	runtime.EventsEmit(a.ctx, "playlist-updated", a.playlistSnapshot())
	go a.fetchAndEmitLyrics(info)
	return ti, nil
}

// Next advances to the next track.
func (a *App) Next() (*TrackInfo, error) {
	next := a.pl.Next()
	if next == nil {
		return nil, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.eng.ClearPreload()
	a.preloadMu.Lock()
	a.lastPreloadPath = ""
	a.preloadMu.Unlock()

	info, err := a.eng.Load(next.Path)
	if err != nil {
		return nil, err
	}
	if err := a.eng.Play(); err != nil {
		return nil, err
	}
	ti := toTrackInfo(info, a.eng.OutputMode())
	runtime.EventsEmit(a.ctx, "playlist-updated", a.playlistSnapshot())
	go a.fetchAndEmitLyrics(info)
	return ti, nil
}

// Prev moves to the previous track.
func (a *App) Prev() (*TrackInfo, error) {
	prev := a.pl.Prev()
	if prev == nil {
		return nil, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.eng.ClearPreload()
	a.preloadMu.Lock()
	a.lastPreloadPath = ""
	a.preloadMu.Unlock()

	info, err := a.eng.Load(prev.Path)
	if err != nil {
		return nil, err
	}
	if err := a.eng.Play(); err != nil {
		return nil, err
	}
	ti := toTrackInfo(info, a.eng.OutputMode())
	runtime.EventsEmit(a.ctx, "playlist-updated", a.playlistSnapshot())
	go a.fetchAndEmitLyrics(info)
	return ti, nil
}

// UpNext returns the track that Next() would play, respecting shuffle order,
// without advancing the playlist. Returns nil at the end of the queue. Used by
// the immersive view's "Up next" card.
func (a *App) UpNext() *PlaylistTrack {
	t := a.pl.PeekNext()
	if t == nil {
		return nil
	}
	for i, tr := range a.pl.Tracks {
		if tr.Path == t.Path {
			return &PlaylistTrack{Index: i, Path: t.Path, Title: t.Title}
		}
	}
	return nil
}

// MoveTrack reorders the queue, moving the track at `from` to index `to`.
// The current-track highlight follows the move (playlist.Move patches Current).
func (a *App) MoveTrack(from, to int) []PlaylistTrack {
	a.pl.Move(from, to)
	snap := a.playlistSnapshot()
	runtime.EventsEmit(a.ctx, "playlist-updated", snap)
	return snap
}

// Remove removes a track from the playlist by index.
func (a *App) Remove(index int) []PlaylistTrack {
	a.pl.Remove(index)
	snap := a.playlistSnapshot()
	runtime.EventsEmit(a.ctx, "playlist-updated", snap)
	return snap
}

// ClearPlaylist removes all tracks and stops playback.
func (a *App) ClearPlaylist() {
	a.eng.Stop()
	a.pl.Clear()
	runtime.EventsEmit(a.ctx, "playlist-updated", []PlaylistTrack{})
}

// ── Bound methods — transport ─────────────────────────────────────────────────

func (a *App) Play() error  { return a.eng.Play() }
func (a *App) Pause()       { a.eng.Pause() }
func (a *App) Stop()        { a.eng.Stop() }
func (a *App) Seek(s float64) { a.eng.Seek(s) }

func (a *App) SetVolume(v float32) { a.eng.Volume = v }
func (a *App) GetVolume() float32  { return a.eng.Volume }

// ── Bound methods — settings ──────────────────────────────────────────────────

func (a *App) SetShuffle(on bool) {
	if on != a.pl.Shuffle {
		a.pl.ToggleShuffle()
	}
}
func (a *App) IsShuffle() bool { return a.pl.Shuffle }

func (a *App) SetRepeat(on bool) { a.repeatOne.Store(on) }
func (a *App) IsRepeat() bool    { return a.repeatOne.Load() }

func (a *App) SetExclusive(on bool)  { a.eng.SetExclusiveMode(on) }
func (a *App) IsExclusive() bool     { return a.eng.ExclusiveMode() }
func (a *App) GetOutputMode() string { return a.eng.OutputMode() }

// ListOutputDevices enumerates the available playback endpoints (DACs etc.).
// Call it each time the picker opens so hot-plugged devices appear.
func (a *App) ListOutputDevices() []audio.OutputDevice {
	devs, err := audio.ListPlaybackDevices()
	if err != nil {
		return nil
	}
	return devs
}

// SetOutputDevice picks the exclusive-mode endpoint by its hex token ("" =
// system default). Applies on the next track / re-play. Persisted via saveConfig.
func (a *App) SetOutputDevice(id string) { a.eng.SetOutputDevice(id) }
func (a *App) GetOutputDevice() string   { return a.eng.OutputDevice() }

func (a *App) SetCrossfade(sec float64) { a.eng.SetCrossfade(sec) }
func (a *App) GetCrossfade() float64    { return a.eng.CrossfadeSeconds() }

// UI-only preference setters (persisted by saveConfig; no engine effect).
func (a *App) SetVisualizerMode(mode string) { a.uiVisualizerMode = mode }
func (a *App) SetAccentSource(src string)    { a.uiAccentSource = src }

// GetWaveform returns the latest peak-envelope data (96 points, 38 ms window).
// Called from a Wails goroutine — equivalent to the UI-ticker pattern in AGENTS.md §10.
func (a *App) GetWaveform() []float32 {
	return a.eng.Vis.Waveform(96, 38)
}

// GetSpectrum returns 32 log-spaced FFT band magnitudes in [0, 1].
// Changes much faster than peak-envelope — ideal for bar visualizers.
func (a *App) GetSpectrum() []float32 {
	return a.eng.Vis.Snapshot()
}

// RetryLyrics invalidates the LRCLIB cache entry for the current track and
// re-fetches lyrics. Used by the "Retry lookup" button when an automatic
// fetch comes back empty (offline at load time, LRCLIB miss, etc.).
func (a *App) RetryLyrics() {
	if a.eng.Info == nil {
		return
	}
	info := a.eng.Info
	// Clear by artist+title — same key audio/lyrics.go uses internally.
	audio.ClearLyricsCacheFor(info.Artist, info.Title)
	go a.fetchAndEmitLyrics(info)
}

// PlaySongs replaces the queue with the given ordered track list and plays
// from startIndex. Used by list views (Songs library, an expanded saved
// playlist) so the list becomes the playback context — Next/Prev then walk
// the whole list instead of dead-ending on a single track.
func (a *App) PlaySongs(paths []string, startIndex int) (*TrackInfo, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if startIndex < 0 || startIndex >= len(paths) {
		startIndex = 0
	}
	a.eng.Stop()
	a.pl.Clear()
	for _, p := range paths {
		a.pl.Add(p)
	}
	return a.PlayAt(startIndex)
}

// PlaySong loads path, adds it to the playlist if absent, and starts playing.
func (a *App) PlaySong(path string) (*TrackInfo, error) {
	a.pl.Add(path)
	for i, t := range a.pl.Tracks {
		if t.Path == path {
			a.pl.SetCurrent(i)
			return a.PlayAt(i)
		}
	}
	return nil, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func toTrackInfo(info *audio.FileInfo, outputMode string) *TrackInfo {
	if info == nil {
		return nil
	}
	ti := &TrackInfo{
		Path:         info.Path,
		Title:        displayTitle(info),
		Artist:       info.Artist,
		Album:        info.Album,
		Format:       info.Format,
		QualityLabel: info.QualityLabel,
		DSDLabel:     info.DSDLabel,
		Duration:     info.Duration,
		IsDSD:        info.IsDSD,
		HasArt:       len(info.PictureData) > 0,
		OutputMode:   outputMode,
	}
	if len(info.PictureData) > 0 {
		mime := info.PictureMIME
		if mime == "" {
			mime = "image/jpeg"
		}
		ti.ArtBase64 = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(info.PictureData)
		ti.AccentHex = dominantHex(info.PictureData)
	}
	return ti
}

func displayTitle(info *audio.FileInfo) string {
	if info.Title != "" {
		return info.Title
	}
	base := filepath.Base(info.Path)
	ext  := filepath.Ext(base)
	return base[:len(base)-len(ext)]
}

func toLyricLines(lines []audio.LyricLine) []LyricLine {
	if lines == nil {
		return nil
	}
	out := make([]LyricLine, len(lines))
	for i, l := range lines {
		ts := -1.0
		if l.IsSynced() {
			ts = l.Time.Seconds()
		}
		out[i] = LyricLine{TimeSec: ts, Text: l.Text}
	}
	return out
}
