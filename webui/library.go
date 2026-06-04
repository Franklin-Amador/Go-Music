package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"

	"gomusic/library"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// AlbumData is the JSON view of a library.Album sent to the frontend.
type AlbumData struct {
	Artist     string `json:"artist"`
	Title      string `json:"title"`
	Year       int    `json:"year"`
	ArtBase64  string `json:"artBase64"`
	AccentHex  string `json:"accentHex"`
	TrackCount int    `json:"trackCount"`
}

func libCachePath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "gomusic", "library.gob")
}

// tryLoadLibraryCache is called from startup. Non-fatal if the cache is absent.
func (a *App) tryLoadLibraryCache() {
	lib, err := library.Load(libCachePath())
	if err != nil {
		return
	}
	a.libMu.Lock()
	a.lib = lib
	a.libMu.Unlock()
	a.emitLibrary()
}

func (a *App) emitLibrary() {
	a.libMu.Lock()
	lib := a.lib
	a.libMu.Unlock()
	if lib == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "library-updated", map[string]interface{}{
		"albums":  toAlbumDataSlice(lib.Albums),
		"artists": artistNames(lib.Artists),
	})
}

// ScanLibrary scans root in a background goroutine, emitting progress events.
func (a *App) ScanLibrary(root string) {
	if root == "" {
		return
	}
	go func() {
		lib := library.New()
		err := lib.Scan(root, func(done, total int) {
			runtime.EventsEmit(a.ctx, "scan-progress", map[string]int{
				"done": done, "total": total,
			})
		})
		if err != nil {
			runtime.EventsEmit(a.ctx, "audio-error", "Scan failed: "+err.Error())
			return
		}
		_ = lib.Save(libCachePath())
		a.libMu.Lock()
		a.lib = lib
		a.libMu.Unlock()
		runtime.EventsEmit(a.ctx, "scan-done")
		a.emitLibrary()
	}()
}

// GetLibraryAlbums returns all albums with embedded art as base64 data URIs.
func (a *App) GetLibraryAlbums() []AlbumData {
	a.libMu.Lock()
	lib := a.lib
	a.libMu.Unlock()
	if lib == nil {
		return nil
	}
	return toAlbumDataSlice(lib.Albums)
}

// GetLibraryArtists returns sorted artist names.
func (a *App) GetLibraryArtists() []string {
	a.libMu.Lock()
	lib := a.lib
	a.libMu.Unlock()
	if lib == nil {
		return nil
	}
	return artistNames(lib.Artists)
}

// LoadAlbum replaces the playlist with the album's tracks (sorted by track
// number) and starts playing from track 1.
func (a *App) LoadAlbum(artist, title string) (*TrackInfo, error) {
	a.libMu.Lock()
	lib := a.lib
	a.libMu.Unlock()
	if lib == nil {
		return nil, nil
	}

	var found *library.Album
	for _, al := range lib.Albums {
		if strings.EqualFold(al.Artist, artist) && strings.EqualFold(al.Title, title) {
			found = al
			break
		}
	}
	if found == nil || len(found.Tracks) == 0 {
		return nil, nil
	}

	// Replace playlist with album tracks (library already sorts by track num).
	a.pl.Clear()
	for _, t := range found.Tracks {
		a.pl.Add(t.Path)
	}
	a.pl.SetCurrent(0)
	runtime.EventsEmit(a.ctx, "playlist-updated", a.playlistSnapshot())

	return a.PlayAt(0)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func toAlbumDataSlice(albums []*library.Album) []AlbumData {
	out := make([]AlbumData, 0, len(albums))
	for _, al := range albums {
		d := AlbumData{
			Artist:     al.Artist,
			Title:      al.Title,
			Year:       al.Year,
			TrackCount: len(al.Tracks),
		}
		if len(al.CoverData) > 0 {
			mime := al.CoverMIME
			if mime == "" {
				mime = "image/jpeg"
			}
			d.ArtBase64 = "data:" + mime + ";base64," +
				base64.StdEncoding.EncodeToString(al.CoverData)
			d.AccentHex = dominantHex(al.CoverData)
		}
		out = append(out, d)
	}
	return out
}

func artistNames(artists []*library.Artist) []string {
	names := make([]string, len(artists))
	for i, ar := range artists {
		names[i] = ar.Name
	}
	return names
}
