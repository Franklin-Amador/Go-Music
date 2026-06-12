package main

// lyricoffsets.go — per-track manual lyric timing corrections.
//
// Some provider LRCs are consistently shifted against the user's release
// (different intro length, radio edit, the "Bad" canary). The user calibrates
// once — nudge buttons or tap-the-line-being-sung — and the offset persists
// here, keyed by artist|title (same identity the lyrics cache uses, so it
// survives file moves/renames). Applied entirely on the frontend: line time +
// offset.
//
// File: %APPDATA%\gomusic\lyric-offsets.json — {"artist|title": seconds}.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	lyricOffMu  sync.Mutex
	lyricOffs   map[string]float64
	lyricOffDir = func() string {
		dir, err := os.UserConfigDir()
		if err != nil {
			return ""
		}
		return filepath.Join(dir, "gomusic")
	}()
)

func lyricOffPath() string {
	if lyricOffDir == "" {
		return ""
	}
	return filepath.Join(lyricOffDir, "lyric-offsets.json")
}

func lyricOffKey(artist, title string) string {
	return strings.ToLower(strings.TrimSpace(artist)) + "|" + strings.ToLower(strings.TrimSpace(title))
}

// loadLyricOffsets is called lazily under lyricOffMu.
func loadLyricOffsetsLocked() {
	if lyricOffs != nil {
		return
	}
	lyricOffs = map[string]float64{}
	p := lyricOffPath()
	if p == "" {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &lyricOffs)
}

// GetLyricOffset returns the saved timing correction (seconds) for a track,
// 0 when none.
func (a *App) GetLyricOffset(artist, title string) float64 {
	lyricOffMu.Lock()
	defer lyricOffMu.Unlock()
	loadLyricOffsetsLocked()
	return lyricOffs[lyricOffKey(artist, title)]
}

// SetLyricOffset stores (or clears, when 0) the timing correction for a track
// and persists the table immediately — it's tiny.
func (a *App) SetLyricOffset(artist, title string, seconds float64) {
	lyricOffMu.Lock()
	defer lyricOffMu.Unlock()
	loadLyricOffsetsLocked()
	key := lyricOffKey(artist, title)
	if seconds == 0 {
		delete(lyricOffs, key)
	} else {
		lyricOffs[key] = seconds
	}
	p := lyricOffPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(lyricOffDir, 0o755)
	data, err := json.MarshalIndent(lyricOffs, "", " ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}
