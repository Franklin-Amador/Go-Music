package main

// artfetch.go — online album-art lookup for tracks with no embedded picture.
//
// Provider chain: iTunes Search API → Deezer (both keyless). Results are
// validated as decodable images and cached on disk under
// %LocalAppData%\gomusic\art\<sha1(artist|album)>.jpg so each album is
// queried at most once. Misses are negative-cached as an empty "<key>.miss"
// marker file; a future "Retry art" path just needs to delete the marker
// (and the .jpg, if any) before calling fetchArtAsync again.
//
// Everything here runs on background goroutines — never on the bind/UI path.
// UI updates flow exclusively through the "art-found" Wails event.

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ArtFound is the payload of the "art-found" event. Emitted only on success —
// the frontend never hears about misses.
type ArtFound struct {
	Path      string `json:"path"`      // track path the art belongs to
	ArtBase64 string `json:"artBase64"` // "data:image/...;base64,..."
	AccentHex string `json:"accentHex"` // dominant color of the fetched art
}

// artFetchMu serializes ONLINE lookups app-wide: rapid track skipping queues
// goroutines here instead of stacking concurrent HTTP requests against the
// providers. Disk-cache hits never touch it.
var artFetchMu sync.Mutex

var artHTTP = &http.Client{Timeout: 6 * time.Second}

const artUserAgent = "GoMusic/1.0 (album-art lookup)"

// ── cache ─────────────────────────────────────────────────────────────────────

// artCacheDir returns %LocalAppData%\gomusic\art ("" on failure).
func artCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "gomusic", "art")
}

// artCacheKey keys the cache on artist+album (artist+title when the track has
// no album tag), lowercased and trimmed. Returns "" when there is nothing
// usable to search for.
func artCacheKey(artist, album, title string) string {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	a, b := norm(artist), norm(album)
	if b == "" {
		b = norm(title)
	}
	if a == "" && b == "" {
		return ""
	}
	sum := sha1.Sum([]byte(a + "|" + b))
	return hex.EncodeToString(sum[:])
}

// ── per-track entry point ─────────────────────────────────────────────────────

// fetchArtAsync is launched (in a goroutine) for every track that loads with
// no embedded art: cache first, then the online provider chain. On success it
// emits "art-found"; on a miss it writes the negative marker and stays silent.
// If the user already skipped to another track, the cache is still written —
// the frontend's path check turns the late emit into a UI no-op.
func (a *App) fetchArtAsync(path, artist, album, title string) {
	key := artCacheKey(artist, album, title)
	dir := artCacheDir()
	if key == "" || dir == "" {
		return
	}
	jpg := filepath.Join(dir, key+".jpg")
	miss := filepath.Join(dir, key+".miss")

	if b, err := os.ReadFile(jpg); err == nil && validArt(b) {
		a.emitArtFound(path, b)
		return
	}
	if _, err := os.Stat(miss); err == nil {
		return // negative-cached — don't re-query on every play
	}

	artFetchMu.Lock()
	defer artFetchMu.Unlock()

	// Re-check after waiting: a queued fetch for the same album may have
	// filled the cache while we held the line.
	if b, err := os.ReadFile(jpg); err == nil && validArt(b) {
		a.emitArtFound(path, b)
		return
	}
	if _, err := os.Stat(miss); err == nil {
		return
	}

	b := fetchOnlineArt(artist, album, title)
	_ = os.MkdirAll(dir, 0o755)
	if b == nil {
		_ = os.WriteFile(miss, nil, 0o644) // silent failure, negative-cache it
		return
	}
	_ = os.WriteFile(jpg, b, 0o644)
	a.emitArtFound(path, b)
}

func (a *App) emitArtFound(path string, b []byte) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "art-found", ArtFound{
		Path:      path,
		ArtBase64: "data:" + artMime(b) + ";base64," + base64.StdEncoding.EncodeToString(b),
		AccentHex: dominantHex(b),
	})
}

// ── library albums sweep ──────────────────────────────────────────────────────

// AlbumArtFound is the payload of "album-art-found" — art resolved online for
// a library album that has no embedded cover.
type AlbumArtFound struct {
	Artist    string `json:"artist"`
	Title     string `json:"title"`
	ArtBase64 string `json:"artBase64"`
	AccentHex string `json:"accentHex"`
}

// albumArtSweeping prevents overlapping sweeps (startup emit + scan-done emit).
var albumArtSweeping atomic.Bool

// sweepMissingAlbumArt walks the albums that came out of the library without
// embedded covers and resolves each through the same disk cache + provider
// chain as the now-playing flow. Same cache key (artist|album), so a cover
// fetched for the playing track instantly serves its album card and vice
// versa. One goroutine, sequential — artFetchMu plus the providers' goodwill
// are respected; misses are negative-cached so re-sweeps cost nothing.
func (a *App) sweepMissingAlbumArt(albums []AlbumData) {
	var missing []AlbumData
	for _, al := range albums {
		if al.ArtBase64 == "" && al.Artist != "" {
			missing = append(missing, al)
		}
	}
	if len(missing) == 0 || !albumArtSweeping.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer albumArtSweeping.Store(false)
		dir := artCacheDir()
		if dir == "" {
			return
		}
		for _, al := range missing {
			key := artCacheKey(al.Artist, al.Title, "")
			if key == "" {
				continue
			}
			jpg := filepath.Join(dir, key+".jpg")
			miss := filepath.Join(dir, key+".miss")

			b, err := os.ReadFile(jpg)
			if err != nil || !validArt(b) {
				if _, err := os.Stat(miss); err == nil {
					continue // negative-cached
				}
				artFetchMu.Lock()
				b = fetchOnlineArt(al.Artist, al.Title, "")
				_ = os.MkdirAll(dir, 0o755)
				if b == nil {
					_ = os.WriteFile(miss, nil, 0o644)
					artFetchMu.Unlock()
					continue
				}
				_ = os.WriteFile(jpg, b, 0o644)
				artFetchMu.Unlock()
			}
			if a.ctx == nil {
				return
			}
			runtime.EventsEmit(a.ctx, "album-art-found", AlbumArtFound{
				Artist:    al.Artist,
				Title:     al.Title,
				ArtBase64: "data:" + artMime(b) + ";base64," + base64.StdEncoding.EncodeToString(b),
				AccentHex: dominantHex(b),
			})
		}
	}()
}

// ── provider chain ────────────────────────────────────────────────────────────

// fetchOnlineArt tries iTunes, then Deezer. Returns nil on any failure —
// callers treat nil as a cacheable miss. Never panics, never logs loudly.
func fetchOnlineArt(artist, album, title string) []byte {
	if b := fetchITunesArt(artist, album, title); b != nil {
		return b
	}
	return fetchDeezerArt(artist, album, title)
}

func fetchITunesArt(artist, album, title string) []byte {
	sub, entity := album, "album"
	if sub == "" {
		sub, entity = title, "song"
	}
	term := strings.TrimSpace(strings.TrimSpace(artist) + " " + strings.TrimSpace(sub))
	if term == "" {
		return nil
	}
	q := url.Values{
		"term":   {term},
		"media":  {"music"},
		"entity": {entity},
		"limit":  {"5"},
	}
	body := artGet("https://itunes.apple.com/search?" + q.Encode())
	if body == nil {
		return nil
	}
	var res struct {
		Results []struct {
			ArtistName     string `json:"artistName"`
			CollectionName string `json:"collectionName"`
			TrackName      string `json:"trackName"`
			ArtworkURL100  string `json:"artworkUrl100"`
		} `json:"results"`
	}
	if json.Unmarshal(body, &res) != nil {
		return nil
	}

	// Loose matching: case-insensitive substring in either direction. Prefer a
	// result where both artist and album/title line up; fall back to the first
	// artist-only match.
	bestURL := ""
	for _, r := range res.Results {
		if r.ArtworkURL100 == "" {
			continue
		}
		artistOK := artist == "" || looseMatch(r.ArtistName, artist)
		name := r.CollectionName
		if entity == "song" {
			name = r.TrackName
		}
		subOK := sub == "" || looseMatch(name, sub) || looseMatch(r.CollectionName, sub)
		if artistOK && subOK {
			bestURL = r.ArtworkURL100
			break
		}
		if artistOK && bestURL == "" {
			bestURL = r.ArtworkURL100
		}
	}
	if bestURL == "" {
		return nil
	}
	img := artGet(strings.Replace(bestURL, "100x100", "600x600", 1))
	if !validArt(img) {
		return nil
	}
	return img
}

func fetchDeezerArt(artist, album, title string) []byte {
	var parts []string
	if strings.TrimSpace(artist) != "" {
		parts = append(parts, `artist:"`+strings.TrimSpace(artist)+`"`)
	}
	switch {
	case strings.TrimSpace(album) != "":
		parts = append(parts, `album:"`+strings.TrimSpace(album)+`"`)
	case strings.TrimSpace(title) != "":
		parts = append(parts, `track:"`+strings.TrimSpace(title)+`"`)
	}
	if len(parts) == 0 {
		return nil
	}
	q := url.Values{"q": {strings.Join(parts, " ")}}
	body := artGet("https://api.deezer.com/search?" + q.Encode())
	if body == nil {
		return nil
	}
	var res struct {
		Data []struct {
			Album struct {
				CoverXL  string `json:"cover_xl"`  // 1000×1000
				CoverBig string `json:"cover_big"` // 500×500
			} `json:"album"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &res) != nil || len(res.Data) == 0 {
		return nil
	}
	u := res.Data[0].Album.CoverXL
	if u == "" {
		u = res.Data[0].Album.CoverBig
	}
	if u == "" {
		return nil
	}
	img := artGet(u)
	if !validArt(img) {
		return nil
	}
	return img
}

// ── plumbing ──────────────────────────────────────────────────────────────────

// artGet GETs a URL with the proper User-Agent and a hard 6 s timeout.
// Returns nil on any error or non-200 — silent failure by design.
func artGet(u string) []byte {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", artUserAgent)
	resp, err := artHTTP.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20)) // hard 6 MB ceiling
	if err != nil {
		return nil
	}
	return b
}

func looseMatch(have, want string) bool {
	h := strings.ToLower(strings.TrimSpace(have))
	w := strings.ToLower(strings.TrimSpace(want))
	if h == "" || w == "" {
		return false
	}
	return strings.Contains(h, w) || strings.Contains(w, h)
}

// validArt accepts only decodable jpeg/png of sane size (> 1 KB, < 5 MB).
func validArt(b []byte) bool {
	if len(b) < 1024 || len(b) > 5<<20 {
		return false
	}
	_, _, err := image.DecodeConfig(bytes.NewReader(b))
	return err == nil
}

func artMime(b []byte) string {
	_, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err == nil && format == "png" {
		return "image/png"
	}
	return "image/jpeg"
}
