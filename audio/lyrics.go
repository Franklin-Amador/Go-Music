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

// FetchLyrics looks up lyrics for the given file using a multi-step fallback:
//
//	1. <song>.lrc next to the audio file (synced karaoke format)
//	2. Embedded tags inside the file (USLT for MP3 / DSF, LYRICS for FLAC)
//	3. Online: lrclib.net (exact, then sanitized title/artist), then the
//	   NetEase Cloud Music API as a last resort (synced LRC, huge catalog).
//
// Returns nil if all steps fail. Network errors are non-fatal and silent.
// A negative result from the online chain is cached so we don't keep
// hitting the APIs.
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

	// 3. Online lookup (with cache).
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
		if text := fetchOnline(info.Artist, searchTitle, info.Album, info.Duration); text != "" {
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

// ── Source 3: online providers (LRCLIB → NetEase) ───────────────────────────

// fetchOnline orchestrates the online lookup chain with a single cache entry
// per track (negative results cached as "" so we don't re-query):
//
//  1. LRCLIB with the exact tag artist/title.
//  2. LRCLIB again with a sanitized artist/title — "(2014 Remaster)" suffixes,
//     "feat. X" credits etc. stripped — only when sanitization changed something.
//  3. NetEase Cloud Music, same exact-then-sanitized order. No API key; serves
//     real synced LRC for a huge catalog including Western music.
//
// Duration tolerance applies on every pass so a wrong release (remaster,
// live, single edit) never wins just because the name matched.
func fetchOnline(artist, title, album string, durationSec float64) string {
	key := lyricsCacheKey(artist, title)
	if cached, ok := lyricsCacheGet(key); ok {
		return cached
	}

	sanArtist, sanTitle := sanitizeForSearch(artist, title)
	sanitized := sanArtist != artist || sanTitle != title

	text := lrclibLookup(artist, title, album, durationSec)
	if text == "" && sanitized {
		text = lrclibLookup(sanArtist, sanTitle, album, durationSec)
	}
	if text == "" {
		text = neteaseFetch(artist, title, durationSec)
	}
	if text == "" && sanitized {
		text = neteaseFetch(sanArtist, sanTitle, durationSec)
	}

	lyricsCachePut(key, text) // "" caches the negative result
	return text
}

// sanitizeForSearch strips noise that breaks online matching: trailing
// parenthetical / bracketed qualifiers on the title ("(2014 Remaster)",
// "[Live]", "(Deluxe Edition)", "(feat. X)") and "feat./ft. X" credits from
// both title and artist ("Avicii feat. Aloe Blacc" → "Avicii"). If stripping
// would leave an empty string, the original value is kept.
func sanitizeForSearch(artist, title string) (string, string) {
	t := title
	for {
		next := strings.TrimSpace(parenSuffixRe.ReplaceAllString(t, ""))
		if next == t {
			break
		}
		t = next
	}
	t = strings.TrimSpace(featCreditRe.ReplaceAllString(t, ""))
	a := strings.TrimSpace(featCreditRe.ReplaceAllString(artist, ""))
	if t == "" {
		t = title
	}
	if a == "" {
		a = artist
	}
	return a, t
}

// ── LRCLIB API ───────────────────────────────────────────────────────────────

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

// lrclibLookup returns the best synced (or plain, as a last resort) lyrics
// for a track. Strategy:
//
//  1. /api/get for exact (artist + title [+ album + duration]). If it returns
//     a record whose duration matches ours within tolerance, use it.
//  2. Otherwise /api/search, then pick the record with the smallest duration
//     delta that actually carries syncedLyrics — even when /api/get returned
//     something, because that something may have been the wrong release.
//
// Caching is handled by the fetchOnline orchestrator, not here, so the
// sanitized retry and the NetEase fallback all share one cache entry.
func lrclibLookup(artist, title, album string, durationSec float64) string {
	get := lrclibGet(artist, title, album, int(durationSec))
	if get == nil && album != "" {
		// Album name in the file tag often doesn't match LRCLIB (wrong edition,
		// year suffix, compilation name). Retry without album for a broader match.
		get = lrclibGet(artist, title, "", int(durationSec))
	}

	if get != nil && get.SyncedLyrics != "" && durationSec > 0 &&
		math.Abs(get.Duration-durationSec) <= lrclibDurationToleranceSec {
		return get.SyncedLyrics
	}

	// Fall back to search: prefer synced, prefer closest-duration.
	if best := lrclibSearchBest(artist, title, album, durationSec); best != "" {
		return best
	}

	// Last resort: whatever /api/get returned, even if duration mismatched or
	// it was plain-only. Better something on screen than nothing.
	if get != nil {
		switch {
		case get.SyncedLyrics != "":
			return get.SyncedLyrics
		case get.PlainLyrics != "":
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

// ── NetEase Cloud Music API ──────────────────────────────────────────────────
// Keyless public endpoints. Carries real synced LRC for a huge catalog
// (including Western music), which makes it a strong fallback when LRCLIB
// has no match. All failures (offline, geo-block, layout change) are silent
// misses — they must never break playback.

const (
	// cloudsearch/pc still returns plain JSON; the older /api/search/get/web
	// endpoint started returning an encrypted "result" blob (verified live
	// 2026-06) and is useless without the eapi crypto dance.
	neteaseSearchURL = "http://music.163.com/api/cloudsearch/pc"
	neteaseLyricURL  = "http://music.163.com/api/song/lyric?id=%d&lv=1&kv=1&tv=-1"
	neteaseReferer   = "http://music.163.com"
	neteaseUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	// Slightly looser than LRCLIB's tolerance because NetEase search is fuzzy
	// and we already gate on artist match; still tight enough to reject a
	// different release whose timing would drift on every line.
	neteaseDurationToleranceSec = 4.0
)

// neteaseSong tolerates both JSON shapes NetEase has shipped: cloudsearch
// uses {dt, ar[]} while the legacy search API used {duration, artists[]}.
type neteaseSong struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Dt        int64  `json:"dt"`       // duration, milliseconds (cloudsearch)
	DurationL int64  `json:"duration"` // duration, milliseconds (legacy)
	Ar        []struct {
		Name string `json:"name"`
	} `json:"ar"`
	ArtistsL []struct {
		Name string `json:"name"`
	} `json:"artists"`
}

func (s *neteaseSong) durationMs() int64 {
	if s.Dt > 0 {
		return s.Dt
	}
	return s.DurationL
}

func (s *neteaseSong) artistNames() []string {
	var out []string
	for _, a := range s.Ar {
		out = append(out, a.Name)
	}
	for _, a := range s.ArtistsL {
		out = append(out, a.Name)
	}
	return out
}

type neteaseSearchResp struct {
	Result struct {
		Songs []neteaseSong `json:"songs"`
	} `json:"result"`
}

type neteaseLyricResp struct {
	Lrc struct {
		Lyric string `json:"lyric"`
	} `json:"lrc"`
}

// neteaseFetch searches NetEase for the track and returns its LRC text, or ""
// on any miss/failure.
func neteaseFetch(artist, title string, durationSec float64) string {
	id := neteaseSearchBest(artist, title, durationSec)
	if id == 0 {
		return ""
	}
	return neteaseLyric(id)
}

// neteaseSearchBest returns the song id of the best candidate, or 0.
// Preference order: candidates whose artist matches ours (loose, case- and
// space-insensitive substring either way) beat non-matching ones; within a
// class, smallest |duration delta| wins. When our duration is known, a best
// delta beyond the tolerance rejects the whole result — wrong-version lyrics
// are worse than none (the "Bad" track canary philosophy).
func neteaseSearchBest(artist, title string, durationSec float64) int64 {
	query := strings.TrimSpace(artist + " " + title)
	if query == "" {
		return 0
	}
	form := url.Values{}
	form.Set("s", query)
	form.Set("type", "1")
	form.Set("limit", "10")
	req, err := http.NewRequest("POST", neteaseSearchURL, strings.NewReader(form.Encode()))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var out neteaseSearchResp
	if !neteaseDoJSON(req, &out) {
		return 0
	}
	songs := out.Result.Songs
	if len(songs) == 0 {
		return 0
	}

	ourArtist := normalizeLoose(artist)
	bestID := int64(0)
	bestDelta := math.MaxFloat64
	bestArtistMatch := false
	for i := range songs {
		sng := &songs[i]
		match := false
		if ourArtist != "" {
			for _, name := range sng.artistNames() {
				na := normalizeLoose(name)
				if na != "" && (strings.Contains(na, ourArtist) || strings.Contains(ourArtist, na)) {
					match = true
					break
				}
			}
		}
		delta := 0.0
		if durationSec > 0 {
			delta = math.Abs(float64(sng.durationMs())/1000.0 - durationSec)
		}
		if (match && !bestArtistMatch) || (match == bestArtistMatch && delta < bestDelta) {
			bestArtistMatch = match
			bestDelta = delta
			bestID = sng.ID
		}
	}
	if durationSec > 0 && bestDelta > neteaseDurationToleranceSec {
		return 0
	}
	return bestID
}

// neteaseLyric fetches the LRC text for a song id. Empty or
// instrumental-marker responses are treated as a miss.
func neteaseLyric(id int64) string {
	req, err := http.NewRequest("GET", fmt.Sprintf(neteaseLyricURL, id), nil)
	if err != nil {
		return ""
	}
	var out neteaseLyricResp
	if !neteaseDoJSON(req, &out) {
		return ""
	}
	text := strings.TrimSpace(out.Lrc.Lyric)
	// "纯音乐" ("pure music") is NetEase's instrumental placeholder.
	if text == "" || strings.Contains(text, "纯音乐") {
		return ""
	}
	return stripNeteaseCredits(text)
}

// stripNeteaseCredits drops the timestamped credit lines NetEase prepends to
// every lyric ("作词 : X", "制作人 : Ken Nelson"). Matching requires a Han
// character in the label before the colon, so Western lyric lines that happen
// to contain a colon survive. Returns "" if nothing but credits remained.
func stripNeteaseCredits(text string) string {
	var kept []string
	any := false
	for _, line := range strings.Split(text, "\n") {
		body := strings.TrimSpace(lrcTimeRe.ReplaceAllString(line, ""))
		if neteaseCreditRe.MatchString(body) {
			continue
		}
		if body != "" {
			any = true
		}
		kept = append(kept, line)
	}
	if !any {
		return ""
	}
	return strings.Join(kept, "\n")
}

// neteaseDoJSON sends the request with the required Referer + desktop UA
// headers and decodes the JSON body. False on any error or non-200.
func neteaseDoJSON(req *http.Request, out interface{}) bool {
	req.Header.Set("Referer", neteaseReferer)
	req.Header.Set("User-Agent", neteaseUserAgent)
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(out) == nil
}

// normalizeLoose lowercases and removes all whitespace, for forgiving
// artist-name comparison ("Aloe  Blacc" vs "aloe blacc").
func normalizeLoose(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}

// ── Cache (file-backed under user cache dir) ─────────────────────────────────

// lyricsCacheKey is namespaced with a version tag. Bumping it invalidates
// every existing cached entry — used when the matching logic changes (e.g.
// added duration-aware /api/search fallback) so users automatically get
// fresh, better-matched lyrics instead of the stale wrong-version cache.
// v6: sanitized-title retry + NetEase fallback — retries old negatives.
const lyricsCacheVersion = "v6"

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
	// Trailing "(2014 Remaster)" / "[Live]" / "(feat. X)" qualifiers — applied
	// repeatedly so "Title (feat. X) [Remastered 2011]" sheds both groups.
	parenSuffixRe = regexp.MustCompile(`\s*[(\[][^()\[\]]*[)\]]\s*$`)
	// Bare "feat. X" / "ft. X" / "featuring X" credits outside parentheses.
	featCreditRe = regexp.MustCompile(`(?i)\s+(?:feat\.?|ft\.?|featuring)\s+.*$`)
	// NetEase credit lines: a short label containing a Han character, then a
	// colon (ASCII or full-width) — e.g. "作词 : Guy Berryman".
	neteaseCreditRe = regexp.MustCompile(`^[^:：]{0,16}\p{Han}[^:：]{0,16}[:：]`)
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
