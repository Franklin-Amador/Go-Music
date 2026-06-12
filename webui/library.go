package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gomusic/library"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// SongData is a flat track entry for the Songs tab.
type SongData struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	TrackNum int    `json:"trackNum"`
}

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
	albums := toAlbumDataSlice(lib.Albums)
	runtime.EventsEmit(a.ctx, "library-updated", map[string]interface{}{
		"albums":  albums,
		"artists": artistNames(lib.Artists),
	})
	// Resolve covers for albums with no embedded art (online, cached on disk);
	// each hit arrives as an "album-art-found" event.
	a.sweepMissingAlbumArt(albums)
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

// GetLibrarySongs returns all library tracks as a flat list sorted by
// artist → album → track number. Used by the Songs tab.
func (a *App) GetLibrarySongs() []SongData {
	a.libMu.Lock()
	lib := a.lib
	a.libMu.Unlock()
	if lib == nil {
		return nil
	}
	var out []SongData
	for _, al := range lib.Albums {
		for _, t := range al.Tracks {
			title := t.Title
			if title == "" {
				title = strings.TrimSuffix(filepath.Base(t.Path), filepath.Ext(t.Path))
			}
			out = append(out, SongData{
				Path:     t.Path,
				Title:    title,
				Artist:   t.Artist,
				Album:    al.Title,
				TrackNum: t.TrackNum,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Artist != out[j].Artist {
			return out[i].Artist < out[j].Artist
		}
		if out[i].Album != out[j].Album {
			return out[i].Album < out[j].Album
		}
		return out[i].TrackNum < out[j].TrackNum
	})
	return out
}

func artistNames(artists []*library.Artist) []string {
	names := make([]string, len(artists))
	for i, ar := range artists {
		names[i] = ar.Name
	}
	return names
}
