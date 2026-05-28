package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// appConfig holds user preferences that survive between sessions.
// Saved to %APPDATA%\gomusic\config.json on Windows.
type appConfig struct {
	Volume        float32  `json:"volume"`
	PlaylistPaths []string `json:"playlist_paths"`
	CurrentIndex  int      `json:"current_index"`
	ShowLyrics    bool     `json:"show_lyrics"`
	// CrossfadeSec is the PCM crossfade duration in seconds. 0 disables it.
	// Defaults to 4 on first run; users can toggle it from the Edit menu.
	CrossfadeSec float64 `json:"crossfade_sec"`
	// MusicRoot is the folder scanned for the Albums / Artists library
	// view. Empty means no library has been configured yet — the views
	// then show a placeholder prompting the user to pick a folder.
	MusicRoot string `json:"music_root"`
	// ExclusiveMode controls WASAPI exclusive-vs-shared playback. true
	// (default) is bit-perfect direct-to-DAC but blocks other apps from
	// playing through the same device. false lets YouTube / Discord /
	// etc. mix in alongside, at the cost of Windows's mixer/resampler.
	ExclusiveMode bool `json:"exclusive_mode"`
}

func configPath() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.json")
}

// configDir returns the parent directory used for both config.json and
// the library.gob cache — usually %APPDATA%\gomusic on Windows. Returns
// "" only if the OS won't tell us a user config dir.
func configDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "gomusic")
}

// loadConfig reads the saved config. Returns safe defaults if the file is
// missing or corrupt.
func loadConfig() *appConfig {
	// Defaults are pre-set BEFORE unmarshal so a missing field in the JSON
	// (e.g. first run, or upgrading from a version that didn't know about
	// CrossfadeSec) yields the default value — not the zero value.
	cfg := &appConfig{Volume: 1.0, CurrentIndex: -1, CrossfadeSec: 4, ExclusiveMode: true}
	p := configPath()
	if p == "" {
		return cfg
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, cfg)
	// Clamp volume in case the file is corrupted.
	if cfg.Volume < 0 {
		cfg.Volume = 0
	}
	if cfg.Volume > 1 {
		cfg.Volume = 1
	}
	if cfg.CrossfadeSec < 0 {
		cfg.CrossfadeSec = 0
	}
	if cfg.CrossfadeSec > 30 {
		cfg.CrossfadeSec = 30
	}
	return cfg
}

// save writes the current config to disk. Errors are silently ignored —
// persistence is best-effort and should never interrupt playback.
func (cfg *appConfig) save() {
	p := configPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}
