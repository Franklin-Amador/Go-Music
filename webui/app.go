package main

import (
	"context"

	"gomusic/audio"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// TrackInfo is the JSON-serialisable view of audio.FileInfo sent to the frontend.
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
	PictureMIME  string  `json:"pictureMIME"`
	OutputMode   string  `json:"outputMode"`
}

// PositionInfo is sent via the "position-change" event.
type PositionInfo struct {
	Pos float64 `json:"pos"`
	Dur float64 `json:"dur"`
}

// App is the Wails application struct. Its exported methods are callable from JS.
//
// Threading rule (mirrors AGENTS.md §9 for Wails):
//   Engine callbacks run on internal goroutines — they must never touch the
//   WebView directly. All UI mutations go through runtime.EventsEmit, which
//   posts to the WebView's message queue on the correct thread.
type App struct {
	ctx context.Context
	eng *audio.Engine
}

func NewApp() *App {
	return &App{
		eng: audio.NewEngine(),
	}
}

// startup is called once by Wails after the WebView is ready.
// We wire all engine callbacks here so ctx is valid before any event fires.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	a.eng.OnStateChange = func(s audio.State) {
		runtime.EventsEmit(a.ctx, "state-change", s.String())
	}

	a.eng.OnPositionChange = func(pos, dur float64) {
		runtime.EventsEmit(a.ctx, "position-change", PositionInfo{Pos: pos, Dur: dur})
	}

	a.eng.OnError = func(msg string) {
		runtime.EventsEmit(a.ctx, "audio-error", msg)
	}

	// OnFinished fires at the natural end of the last track.
	// For Fase 1 we just notify the frontend; auto-advance comes in Fase 2
	// when the playlist is wired up.
	a.eng.OnFinished = func() {
		runtime.EventsEmit(a.ctx, "playback-finished")
	}

	// OnTrackTransition fires from the audio thread during a PCM crossfade.
	// Fase 1 has no crossfade, so this is wired but rarely fires.
	a.eng.OnTrackTransition = func(info *audio.FileInfo) {
		runtime.EventsEmit(a.ctx, "track-change", toTrackInfo(info, a.eng.OutputMode()))
	}
}

func (a *App) shutdown(_ context.Context) {
	a.eng.Stop()
}

// ── Bound methods ─────────────────────────────────────────────────────────────

// OpenFileDialog opens a native file-picker and returns the chosen path,
// or "" if the user cancelled.
func (a *App) OpenFileDialog() string {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Open Audio File",
		Filters: []runtime.FileFilter{
			{DisplayName: "Audio Files (*.flac;*.wav;*.mp3;*.dsf;*.dff)", Pattern: "*.flac;*.wav;*.mp3;*.dsf;*.dff"},
		},
	})
	if err != nil {
		return ""
	}
	return path
}

// LoadFile loads an audio file and starts playback. Returns the track info or
// an error string so the frontend can show a message without a catch() branch.
//
// Load is blocking: PCM files are fully decoded + resampled here (3-6 s for
// large FLACs). Wails calls this off the main thread so the UI stays
// responsive, but position updates won't arrive until Load returns and Play
// fires.
func (a *App) LoadFile(path string) (*TrackInfo, error) {
	info, err := a.eng.Load(path)
	if err != nil {
		return nil, err
	}
	if err := a.eng.Play(); err != nil {
		return nil, err
	}
	return toTrackInfo(info, a.eng.OutputMode()), nil
}

// Play starts or resumes playback.
func (a *App) Play() error {
	return a.eng.Play()
}

// Pause pauses without resetting position.
func (a *App) Pause() {
	a.eng.Pause()
}

// Stop halts playback and resets position.
func (a *App) Stop() {
	a.eng.Stop()
}

// Seek moves the playback position. seconds is clamped by the engine to [0, duration].
func (a *App) Seek(seconds float64) {
	a.eng.Seek(seconds)
}

// SetVolume sets the linear volume in [0, 1].
func (a *App) SetVolume(v float32) {
	a.eng.Volume = v
}

// GetVolume returns the current linear volume.
func (a *App) GetVolume() float32 {
	return a.eng.Volume
}

// GetOutputMode returns the current output mode string ("EXCLUSIVE", "SHARED", or "").
func (a *App) GetOutputMode() string {
	return a.eng.OutputMode()
}

// ── helpers ───────────────────────────────────────────────────────────────────

func toTrackInfo(info *audio.FileInfo, outputMode string) *TrackInfo {
	if info == nil {
		return nil
	}
	return &TrackInfo{
		Path:         info.Path,
		Title:        info.Title,
		Artist:       info.Artist,
		Album:        info.Album,
		Format:       info.Format,
		QualityLabel: info.QualityLabel,
		DSDLabel:     info.DSDLabel,
		Duration:     info.Duration,
		IsDSD:        info.IsDSD,
		HasArt:       len(info.PictureData) > 0,
		PictureMIME:  info.PictureMIME,
		OutputMode:   outputMode,
	}
}
