package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// SavedPlaylist is a user-named collection of track paths, persisted across
// sessions independently of the live play queue.
type SavedPlaylist struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
}

// PlaylistMeta is the lightweight summary sent to the Lists tab.
type PlaylistMeta struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func namedPlaylistsPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "gomusic", "playlists-web.json")
}

func loadNamedPlaylists() []SavedPlaylist {
	f, err := os.Open(namedPlaylistsPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var lists []SavedPlaylist
	if err := json.NewDecoder(f).Decode(&lists); err != nil {
		return nil
	}
	return lists
}

// saveNamedPlaylists writes the store to disk. Caller holds namedListsMu.
func (a *App) saveNamedPlaylists() {
	_ = os.MkdirAll(filepath.Dir(namedPlaylistsPath()), 0o755)
	f, err := os.Create(namedPlaylistsPath())
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(a.namedLists)
}

func titleFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// indexOfList returns the index of the named list, or -1. Caller holds the lock.
func (a *App) indexOfList(name string) int {
	for i := range a.namedLists {
		if strings.EqualFold(a.namedLists[i].Name, name) {
			return i
		}
	}
	return -1
}

// getPlaylistsLocked builds the meta list; caller holds namedListsMu.
func (a *App) getPlaylistsLocked() []PlaylistMeta {
	out := make([]PlaylistMeta, len(a.namedLists))
	for i, l := range a.namedLists {
		out[i] = PlaylistMeta{Name: l.Name, Count: len(l.Paths)}
	}
	return out
}

// emitPlaylists notifies the frontend the saved-playlist set changed. Caller
// holds namedListsMu.
func (a *App) emitPlaylists() {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "playlists-updated", a.getPlaylistsLocked())
	}
}

// pathsOf returns a copy of the named list's paths. Takes the lock itself.
func (a *App) pathsOf(name string) []string {
	a.namedListsMu.Lock()
	defer a.namedListsMu.Unlock()
	i := a.indexOfList(name)
	if i < 0 {
		return nil
	}
	out := make([]string, len(a.namedLists[i].Paths))
	copy(out, a.namedLists[i].Paths)
	return out
}

// ── Bound methods (JS → Go) ───────────────────────────────────────────────────

// GetPlaylists returns the saved playlists as name+count summaries.
func (a *App) GetPlaylists() []PlaylistMeta {
	a.namedListsMu.Lock()
	defer a.namedListsMu.Unlock()
	return a.getPlaylistsLocked()
}

// GetPlaylistTracks returns the tracks in a named playlist (for the expanded view).
func (a *App) GetPlaylistTracks(name string) []PlaylistTrack {
	a.namedListsMu.Lock()
	defer a.namedListsMu.Unlock()
	i := a.indexOfList(name)
	if i < 0 {
		return nil
	}
	paths := a.namedLists[i].Paths
	out := make([]PlaylistTrack, len(paths))
	for j, p := range paths {
		out[j] = PlaylistTrack{Index: j, Path: p, Title: titleFromPath(p)}
	}
	return out
}

// CreatePlaylist makes a new empty list. Returns false if the name is blank or
// already taken (case-insensitive).
func (a *App) CreatePlaylist(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	a.namedListsMu.Lock()
	defer a.namedListsMu.Unlock()
	if a.indexOfList(name) >= 0 {
		return false
	}
	a.namedLists = append(a.namedLists, SavedPlaylist{Name: name})
	a.saveNamedPlaylists()
	a.emitPlaylists()
	return true
}

// DeletePlaylist removes a named list.
func (a *App) DeletePlaylist(name string) {
	a.namedListsMu.Lock()
	defer a.namedListsMu.Unlock()
	i := a.indexOfList(name)
	if i < 0 {
		return
	}
	a.namedLists = append(a.namedLists[:i], a.namedLists[i+1:]...)
	a.saveNamedPlaylists()
	a.emitPlaylists()
}

// RenamePlaylist changes a list's name. Returns false on blank/duplicate name.
func (a *App) RenamePlaylist(oldName, newName string) bool {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return false
	}
	a.namedListsMu.Lock()
	defer a.namedListsMu.Unlock()
	i := a.indexOfList(oldName)
	if i < 0 {
		return false
	}
	if j := a.indexOfList(newName); j >= 0 && j != i {
		return false
	}
	a.namedLists[i].Name = newName
	a.saveNamedPlaylists()
	a.emitPlaylists()
	return true
}

// AddToPlaylist appends paths to a list, creating it if it doesn't exist.
// Duplicate paths within the list are skipped.
func (a *App) AddToPlaylist(name string, paths []string) {
	name = strings.TrimSpace(name)
	if name == "" || len(paths) == 0 {
		return
	}
	a.namedListsMu.Lock()
	defer a.namedListsMu.Unlock()
	i := a.indexOfList(name)
	if i < 0 {
		a.namedLists = append(a.namedLists, SavedPlaylist{Name: name})
		i = len(a.namedLists) - 1
	}
	existing := make(map[string]bool, len(a.namedLists[i].Paths))
	for _, p := range a.namedLists[i].Paths {
		existing[p] = true
	}
	for _, p := range paths {
		if !existing[p] {
			a.namedLists[i].Paths = append(a.namedLists[i].Paths, p)
			existing[p] = true
		}
	}
	a.saveNamedPlaylists()
	a.emitPlaylists()
}

// RemoveFromPlaylist drops a single path from a named list.
func (a *App) RemoveFromPlaylist(name, path string) {
	a.namedListsMu.Lock()
	defer a.namedListsMu.Unlock()
	i := a.indexOfList(name)
	if i < 0 {
		return
	}
	paths := a.namedLists[i].Paths
	for j, p := range paths {
		if p == path {
			a.namedLists[i].Paths = append(paths[:j], paths[j+1:]...)
			break
		}
	}
	a.saveNamedPlaylists()
	a.emitPlaylists()
}

// PlayPlaylist replaces the live queue with the named list and plays from the top.
func (a *App) PlayPlaylist(name string) (*TrackInfo, error) {
	paths := a.pathsOf(name)
	if len(paths) == 0 {
		return nil, nil
	}
	a.eng.Stop()
	a.pl.Clear()
	for _, p := range paths {
		a.pl.Add(p)
	}
	return a.PlayAt(0)
}

// QueuePlaylist appends the named list to the end of the current queue.
func (a *App) QueuePlaylist(name string) []PlaylistTrack {
	for _, p := range a.pathsOf(name) {
		a.pl.Add(p)
	}
	snap := a.playlistSnapshot()
	runtime.EventsEmit(a.ctx, "playlist-updated", snap)
	return snap
}
