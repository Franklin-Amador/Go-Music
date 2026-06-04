package audio

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LyricLine is a single line of lyrics. Time is the playback offset at which
// the line should be highlighted; -1 means "no timing" (plain lyrics).
type LyricLine struct {
	Time time.Duration
	Text string
}

// IsSynced reports whether the line carries a real timestamp.
func (l LyricLine) IsSynced() bool { return l.Time >= 0 }

// FetchLyrics looks up lyrics for the given file using a three-step fallback:
//
//	1. <song>.lrc next to the audio file (synced karaoke format)
//	2. Embedded tags inside the file (USLT for MP3 / DSF, LYRICS for FLAC)
//	3. lrclib.net online API (synced if available, else plain text)
//
// Returns nil if all three fail. Network errors are non-fatal and silent.
// A negative result from LRCLIB is cached so we don't keep hitting the API.
func FetchLyrics(info *FileInfo) []LyricLine {
	// 1. Local .lrc file next to the audio
	if lines := tryLRCFile(info.Path); lines != nil {
		return lines
	}

	// 2. Embedded tags inside the file
	if info.IsDSD {
		if md := ReadMetadata(info.Path, dsdOffsetForLyrics(info.Path)); md != nil && md.Lyrics != "" {
			return parseAnyLyrics(md.Lyrics)
		}
	} else {
		if md := ReadMetadata(info.Path, 0); md != nil && md.Lyrics != "" {
			return parseAnyLyrics(md.Lyrics)
		}
	}

	// 3. LRCLIB online (with cache).
	// Strip track-number prefixes from both the tag title and the filename
	// fallback. Tag titles are sometimes written as "A1.Take On Me" or
	// "01 - Title", which LRCLIB won't match.
	searchTitle := info.Title
	if m := trackPrefixRe.FindStringIndex(searchTitle); m != nil {
		searchTitle = strings.TrimSpace(searchTitle[m[1]:])
	}
	if searchTitle == "" {
		base := strings.TrimSuffix(filepath.Base(info.Path), filepath.Ext(info.Path))
		if m := trackPrefixRe.FindStringIndex(base); m != nil {
			base = strings.TrimSpace(base[m[1]:])
		}
		searchTitle = base
	}
	if info.Artist != "" || searchTitle != "" {
		if text := fetchLRCLib(info.Artist, searchTitle, info.Album, info.Duration); text != "" {
			return parseAnyLyrics(text)
		}
	}

	return nil
}

// dsdOffsetForLyrics re-parses the DSD header just to extract the metadata
// offset. Cheap (header is < 100 bytes) and avoids plumbing the offset
// through FileInfo just for this case.
func dsdOffsetForLyrics(path string) int64 {
	info, err := ParseDSD(path, outputSampleRate)
	if err != nil {
		return 0
	}
	return info.MetadataOffset
}

// ── Source 1: local .lrc file ────────────────────────────────────────────────

func tryLRCFile(audioPath string) []LyricLine {
	// "song.flac" → "song.lrc"; also try "song.LRC" and the path verbatim
	// with extension swapped to lower case.
	base := strings.TrimSuffix(audioPath, filepath.Ext(audioPath))
	for _, ext := range []string{".lrc", ".LRC", ".Lrc"} {
		candidate := base + ext
		if data, err := os.ReadFile(candidate); err == nil {
			if lines := parseLRC(string(data)); len(lines) > 0 {
				return lines
			}
		}
	}
	return nil
}

// ── Source 3: LRCLIB API ─────────────────────────────────────────────────────

type lrclibResp struct {
	ID           int     `json:"id"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
	Instrumental bool    `json:"instrumental"`
	Duration     float64 `json:"duration"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
}

const (
	lrclibUserAgent = "GoMusic/1.0 (https://github.com/Franklin-Amador)"
	// If /api/get returns a record whose duration deviates more than this from
	// ours, we suspect a different release (remaster, single, live) and fall
	// back to /api/search to pick a closer match. Synced timing is the entire
	// reason the user enables the lyrics panel — accepting a near-miss leaves
	// every line consistently off, which is the "Bad" symptom.
	lrclibDurationToleranceSec = 2.5
)

// fetchLRCLib returns the best synced (or plain, as a last resort) lyrics for
// a track. Strategy:
//
//  1. /api/get for exact (artist + title [+ album + duration]). If it returns
//     a record whose duration matches ours within tolerance, use it.
//  2. Otherwise /api/search, then pick the record with the smallest duration
//     delta that actually carries syncedLyrics — even when /api/get returned
//     something, because that something may have been the wrong release.
//
// All paths cache the final chosen text (or "" for negative) under the same
// key so we don't re-query for the same track on the next playback.
func fetchLRCLib(artist, title, album string, durationSec float64) string {
	key := lyricsCacheKey(artist, title)
	if cached, ok := lyricsCacheGet(key); ok {
		return cached
	}

	get := lrclibGet(artist, title, album, int(durationSec))
	if get == nil && album != "" {
		// Album name in the file tag often doesn't match LRCLIB (wrong edition,
		// year suffix, compilation name). Retry without album for a broader match.
		get = lrclibGet(artist, title, "", int(durationSec))
	}

	if get != nil && get.SyncedLyrics != "" && durationSec > 0 &&
		math.Abs(get.Duration-durationSec) <= lrclibDurationToleranceSec {
		lyricsCachePut(key, get.SyncedLyrics)
		return get.SyncedLyrics
	}

	// Fall back to search: prefer synced, prefer closest-duration.
	if best := lrclibSearchBest(artist, title, album, durationSec); best != "" {
		lyricsCachePut(key, best)
		return best
	}

	// Last resort: whatever /api/get returned, even if duration mismatched or
	// it was plain-only. Better something on screen than nothing.
	if get != nil {
		switch {
		case get.SyncedLyrics != "":
			lyricsCachePut(key, get.SyncedLyrics)
			return get.SyncedLyrics
		case get.PlainLyrics != "":
			lyricsCachePut(key, get.PlainLyrics)
			return get.PlainLyrics
		}
	}

	return ""
}

func lrclibGet(artist, title, album string, duration int) *lrclibResp {
	u := url.URL{Scheme: "https", Host: "lrclib.net", Path: "/api/get"}
	q := u.Query()
	q.Set("artist_name", artist)
	q.Set("track_name", title)
	if album != "" {
		q.Set("album_name", album)
	}
	if duration > 0 {
		q.Set("duration", strconv.Itoa(duration))
	}
	u.RawQuery = q.Encode()

	data, ok := lrclibDoJSON(u.String(), &lrclibResp{})
	if !ok {
		return nil
	}
	r := data.(*lrclibResp)
	if r.Instrumental {
		return nil
	}
	return r
}

// lrclibSearchBest queries /api/search and returns the synced lyrics of the
// record whose duration is closest to ours. Records without syncedLyrics are
// ignored — we already would have gotten a plain-only result from /api/get.
// Album is intentionally omitted: tag album names often differ from LRCLIB's
// (different editions, punctuation, language) and would filter out valid hits.
// If nothing has syncedLyrics, returns "".
func lrclibSearchBest(artist, title, album string, durationSec float64) string {
	u := url.URL{Scheme: "https", Host: "lrclib.net", Path: "/api/search"}
	q := u.Query()
	if title != "" {
		q.Set("track_name", title)
	}
	if artist != "" {
		q.Set("artist_name", artist)
	}
	u.RawQuery = q.Encode()

	data, ok := lrclibDoJSON(u.String(), &[]lrclibResp{})
	if !ok {
		return ""
	}
	results := *(data.(*[]lrclibResp))
	if len(results) == 0 {
		return ""
	}

	bestSynced, bestPlain := -1, -1
	bestSyncedDelta, bestPlainDelta := math.MaxFloat64, math.MaxFloat64
	for i, r := range results {
		if r.Instrumental {
			continue
		}
		if durationSec <= 0 {
			// No duration to compare — first synced wins, else first plain.
			if r.SyncedLyrics != "" && bestSynced < 0 {
				bestSynced = i
			} else if r.PlainLyrics != "" && bestPlain < 0 {
				bestPlain = i
			}
			continue
		}
		d := math.Abs(r.Duration - durationSec)
		if r.SyncedLyrics != "" && d < bestSyncedDelta {
			bestSyncedDelta = d
			bestSynced = i
		} else if r.PlainLyrics != "" && d < bestPlainDelta {
			bestPlainDelta = d
			bestPlain = i
		}
	}
	if bestSynced >= 0 {
		return results[bestSynced].SyncedLyrics
	}
	if bestPlain >= 0 {
		return results[bestPlain].PlainLyrics
	}
	return ""
}

// lrclibDoJSON issues a GET and decodes into out (a pointer). Returns the
// pointer back and true on success, nil and false on any error or non-200.
func lrclibDoJSON(rawURL string, out interface{}) (interface{}, bool) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", lrclibUserAgent)
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, false
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, false
	}
	return out, true
}

// ── Cache (file-backed under user cache dir) ─────────────────────────────────

// lyricsCacheKey is namespaced with a version tag. Bumping it invalidates
// every existing cached entry — used when the matching logic changes (e.g.
// added duration-aware /api/search fallback) so users automatically get
// fresh, better-matched lyrics instead of the stale wrong-version cache.
const lyricsCacheVersion = "v5"

func lyricsCacheKey(artist, title string) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s|%s|%s",
		lyricsCacheVersion,
		strings.ToLower(strings.TrimSpace(artist)),
		strings.ToLower(strings.TrimSpace(title)),
	)
	return hex.EncodeToString(h.Sum(nil))
}

func lyricsCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "gomusic", "lyrics")
}

func lyricsCacheGet(key string) (string, bool) {
	dir := lyricsCacheDir()
	if dir == "" {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(dir, key+".txt"))
	if err != nil {
		return "", false
	}
	return string(data), true
}

func lyricsCachePut(key, data string) {
	dir := lyricsCacheDir()
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, key+".txt"), []byte(data), 0o644)
}

// ClearLyricsCacheFor drops the cached lyrics entry for the given artist+title
// pair so the next FetchLyrics call re-queries LRCLIB from scratch. Used to
// implement a manual "retry" button in the UI: without this, a previously
// cached empty result would short-circuit every retry attempt.
//
// Silent no-op when no cache entry exists or the cache dir is unavailable —
// callers don't need to react.
func ClearLyricsCacheFor(artist, title string) {
	dir := lyricsCacheDir()
	if dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(dir, lyricsCacheKey(artist, title)+".txt"))
}

// ── Parsing ──────────────────────────────────────────────────────────────────

var (
	lrcTimeRe    = regexp.MustCompile(`\[(\d+):(\d+(?:\.\d+)?)\]`)
	lrcOffsetRe  = regexp.MustCompile(`(?i)\[offset:\s*([+-]?\d+)\s*\]`)
	// Matches common track-number prefixes: "09 - ", "1. ", "A1.", "B2 ", "C 08 " etc.
	trackPrefixRe = regexp.MustCompile(`^[A-Za-z]{0,2}\d+[\s.\-]+`)
)

// parseAnyLyrics auto-detects format: a string with timestamps becomes a
// synced LRC parse; anything else falls back to one line per text line.
func parseAnyLyrics(text string) []LyricLine {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if lrcTimeRe.MatchString(text) {
		return parseLRC(text)
	}
	return parsePlain(text)
}

// parseLRC parses standard LRC format. Multi-timestamp lines are supported
// (the same lyric text appears for each timestamp). Honors the global
// `[offset:±N]` tag (milliseconds): a positive offset shifts every line
// EARLIER (subtracted from the timestamp), matching the most widely deployed
// interpretation (LRC Editor, Walaoke, lrclib-style files). Without this many
// re-mastered tracks drift consistently in one direction.
func parseLRC(text string) []LyricLine {
	var offset time.Duration
	if m := lrcOffsetRe.FindStringSubmatch(text); len(m) == 2 {
		if ms, err := strconv.Atoi(m[1]); err == nil {
			offset = time.Duration(ms) * time.Millisecond
		}
	}

	var out []LyricLine
	for _, raw := range strings.Split(text, "\n") {
		matches := lrcTimeRe.FindAllStringSubmatchIndex(raw, -1)
		if len(matches) == 0 {
			continue
		}
		lastEnd := matches[len(matches)-1][1]
		lyric := strings.TrimSpace(raw[lastEnd:])

		for _, m := range matches {
			mm, _ := strconv.Atoi(raw[m[2]:m[3]])
			ss, _ := strconv.ParseFloat(raw[m[4]:m[5]], 64)
			t := time.Duration(mm)*time.Minute + time.Duration(ss*float64(time.Second)) - offset
			if t < 0 {
				t = 0
			}
			out = append(out, LyricLine{Time: t, Text: lyric})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}

func parsePlain(text string) []LyricLine {
	var out []LyricLine
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		out = append(out, LyricLine{Time: -1, Text: line})
	}
	return out
}
