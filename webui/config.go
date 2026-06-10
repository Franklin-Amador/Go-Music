package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// webConfig mirrors the settings the user cares about between sessions.
// Using a separate file (config-web.json) so it doesn't conflict with the
// Fyne UI's config.json if both binaries share the same %APPDATA%\gomusic\.
type webConfig struct {
	Volume    float32  `json:"volume"`
	Playlist  []string `json:"playlist"`
	Current   int      `json:"current"`
	Exclusive bool     `json:"exclusive"`
	Crossfade float64  `json:"crossfade"`
	Shuffle   bool     `json:"shuffle"`
	Repeat    bool     `json:"repeat"`
	MusicRoot string   `json:"musicRoot"`

	// UI-only preferences (no engine effect). Persisted so the look survives
	// restarts. VisualizerMode: "bars" | "wave" | "radial". AccentSource:
	// "auto" (dominant colour from album art) | "fixed" (default green).
	VisualizerMode string `json:"visualizerMode"`
	AccentSource   string `json:"accentSource"`

	// OutputDevice is the hex token of the chosen exclusive-mode DAC ("" =
	// system default).
	OutputDevice string `json:"outputDevice"`
}

func webConfigPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "gomusic", "config-web.json")
}

// loadConfig reads the saved config. Returns safe defaults on any error.
func loadConfig() webConfig {
	f, err := os.Open(webConfigPath())
	if err != nil {
		return webConfig{Volume: 1.0}
	}
	defer f.Close()
	var c webConfig
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		return webConfig{Volume: 1.0}
	}
	if c.Volume <= 0 {
		c.Volume = 1.0
	}
	if c.VisualizerMode == "" {
		c.VisualizerMode = "bars"
	}
	if c.AccentSource == "" {
		c.AccentSource = "auto"
	}
	return c
}

// saveConfig persists the current engine + playlist state.
func (a *App) saveConfig() {
	c := webConfig{
		Volume:         a.eng.Volume,
		Exclusive:      a.eng.ExclusiveMode(),
		Crossfade:      a.eng.CrossfadeSeconds(),
		Shuffle:        a.pl.Shuffle,
		Repeat:         a.repeatOne.Load(),
		Current:        a.pl.Current,
		VisualizerMode: a.uiVisualizerMode,
		AccentSource:   a.uiAccentSource,
		OutputDevice:   a.eng.OutputDevice(),
	}
	for _, t := range a.pl.Tracks {
		c.Playlist = append(c.Playlist, t.Path)
	}
	if a.lib != nil {
		c.MusicRoot = a.lib.Root
	}

	_ = os.MkdirAll(filepath.Dir(webConfigPath()), 0o755)
	f, err := os.Create(webConfigPath())
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(&c)
}

// ── Bound methods (called from JS) ────────────────────────────────────────────

// GetInitialState returns the persisted config so the frontend can restore UI
// toggles (volume, shuffle, repeat, exclusive) without an extra round-trip.
func (a *App) GetInitialState() webConfig {
	return loadConfig()
}

// SaveConfig is called by the frontend on window close / pagehide.
func (a *App) SaveConfig() {
	a.saveConfig()
}
