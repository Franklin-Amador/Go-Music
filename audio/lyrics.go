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
	"sync"
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
// per track (negative results cached as "" so we don't re-query). The chain
// is SYNCED-FIRST in two phases:
//
//	Phase 1 (synced only): LRCLIB exact → LRCLIB sanitized — "(2014 Remaster)"
//	suffixes, "feat. X" credits etc. stripped, only when sanitization changed
//	something — → NetEase exact → NetEase sanitized. The first source that
//	yields TIMESTAMPED lyrics wins. A plain-only hit from an earlier source
//	must NOT short-circuit a later source that has the synced version (real
//	case: LRCLIB had plain romaji for a cover, NetEase had the synced LRC).
//
//	Phase 2 (plain fallback): only when NO source produced synced lyrics, use
//	the best plain text remembered from phase 1 (first source order — no
//	re-querying).
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

	// Phase 1: hunt for synced lyrics across every provider, remembering the
	// first plain candidate encountered along the way for phase 2. Any
	// transient failure (network, rate-limit, 5xx) taints the whole pass:
	// a miss then proves nothing, so it must not become a cached negative.
	var plain string
	var transient bool
	keepPlain := func(p string) {
		if plain == "" {
			plain = p
		}
	}

	synced, p, t := lrclibLookup(artist, title, album, durationSec)
	keepPlain(p)
	transient = transient || t
	if synced == "" && sanitized {
		synced, p, t = lrclibLookup(sanArtist, sanTitle, album, durationSec)
		keepPlain(p)
		transient = transient || t
	}
	if synced == "" {
		var raw string
		raw, t = neteaseFetch(artist, title, durationSec)
		synced, p = splitSyncedPlain(raw)
		keepPlain(p)
		transient = transient || t
	}
	if synced == "" && sanitized {
		var raw string
		raw, t = neteaseFetch(sanArtist, sanTitle, durationSec)
		synced, p = splitSyncedPlain(raw)
		keepPlain(p)
		transient = transient || t
	}

	// Phase 2: no synced anywhere — fall back to the best plain text seen.
	text := synced
	if text == "" {
		text = plain
	}

	// Cache the result — except an empty one tainted by a transient failure:
	// the next playback simply retries. Definitive negatives DO cache, but
	// they expire (see lyricsCacheGet) so newly-uploaded lyrics get found.
	if text != "" || !transient {
		lyricsCachePut(key, text)
	}
	return text
}

// splitSyncedPlain classifies a raw lyrics text into the (synced, plain)
// channels used by the fetchOnline chain. NetEase's lrc.lyric is normally
// timestamped, but never assume — untimed text is a plain candidate only.
func splitSyncedPlain(text string) (synced, plain string) {
	if text == "" {
		return "", ""
	}
	if lrcTimeRe.MatchString(text) {
		return text, ""
	}
	return "", text
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

// lrclibLookup returns the best synced and best plain lyrics for a track as
// SEPARATE channels — the fetchOnline chain must be able to keep hunting for
// a synced version elsewhere even when LRCLIB only has plain text. Strategy
// (matching logic unchanged from the single-string version):
//
//  1. /api/get for exact (artist + title [+ album + duration]). If it returns
//     a record whose duration matches ours within tolerance, use it.
//  2. Otherwise /api/search, then pick the record with the smallest duration
//     delta that actually carries syncedLyrics — even when /api/get returned
//     something, because that something may have been the wrong release.
//  3. Last resort per channel: whatever /api/get returned, even if duration
//     mismatched (synced) or it was plain-only (plain).
//
// Caching is handled by the fetchOnline orchestrator, not here, so the
// sanitized retry and the NetEase fallback all share one cache entry.
func lrclibLookup(artist, title, album string, durationSec float64) (synced, plain string, transient bool) {
	get, t1 := lrclibGet(artist, title, album, int(durationSec))
	transient = t1
	if get == nil && album != "" {
		// Album name in the file tag often doesn't match LRCLIB (wrong edition,
		// year suffix, compilation name). Retry without album for a broader match.
		var t2 bool
		get, t2 = lrclibGet(artist, title, "", int(durationSec))
		transient = transient || t2
	}

	if get != nil && get.SyncedLyrics != "" && durationSec > 0 &&
		math.Abs(get.Duration-durationSec) <= lrclibDurationToleranceSec {
		return get.SyncedLyrics, "", transient
	}

	// Fall back to search: prefer synced, prefer closest-duration.
	searchSynced, searchPlain, t3 := lrclibSearchBest(artist, title, album, durationSec)
	transient = transient || t3

	synced = searchSynced
	if synced == "" && get != nil {
		// Duration-mismatched /api/get synced — same last resort as before.
		synced = get.SyncedLyrics
	}
	plain = searchPlain
	if plain == "" && get != nil {
		plain = get.PlainLyrics
	}
	return synced, plain, transient
}

func lrclibGet(artist, title, album string, duration int) (*lrclibResp, bool) {
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

	data, ok, transient := lrclibDoJSON(u.String(), &lrclibResp{})
	if !ok {
		return nil, transient
	}
	r := data.(*lrclibResp)
	if r.Instrumental {
		return nil, false
	}
	return r, false
}

// lrclibSearchBest queries /api/search and returns, per channel, the lyrics
// of the record whose duration is closest to ours: the closest record that
// carries syncedLyrics, and the closest plain-only record. Album is
// intentionally omitted: tag album names often differ from LRCLIB's
// (different editions, punctuation, language) and would filter out valid hits.
// Either channel may be "".
func lrclibSearchBest(artist, title, album string, durationSec float64) (string, string, bool) {
	u := url.URL{Scheme: "https", Host: "lrclib.net", Path: "/api/search"}
	q := u.Query()
	if title != "" {
		q.Set("track_name", title)
	}
	if artist != "" {
		q.Set("artist_name", artist)
	}
	u.RawQuery = q.Encode()

	data, ok, transient := lrclibDoJSON(u.String(), &[]lrclibResp{})
	if !ok {
		return "", "", transient
	}
	results := *(data.(*[]lrclibResp))
	if len(results) == 0 {
		return "", "", false
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
	syncedText, plainText := "", ""
	if bestSynced >= 0 {
		syncedText = results[bestSynced].SyncedLyrics
	}
	if bestPlain >= 0 {
		plainText = results[bestPlain].PlainLyrics
	}
	return syncedText, plainText, false
}

// ── transient-failure classification + request pacing ───────────────────────
//
// A "transient" failure (network down, timeout, 429 rate-limit, 5xx, or a
// decode error from a changed API layout) means THE TRACK MIGHT EXIST — the
// orchestrator must not write a permanent negative cache entry for it. Only a
// definitive "the API understood us and has nothing" (404 / clean empty
// result) may cache a negative. This distinction is the fix for whole-album
// loads where a burst of lookups got rate-limited: some tracks succeeded,
// the throttled ones were negative-cached forever ("found some, not others").
func transientStatus(code int) bool {
	return code == 429 || code == 403 || code >= 500
}

// pacer spaces requests to one provider so album-sized bursts (8 tracks ×
// several calls each) don't trip rate limits in the first place. FetchLyrics
// always runs on a background goroutine, so sleeping here is safe.
type pacer struct {
	mu   sync.Mutex
	last time.Time
	gap  time.Duration
}

func (p *pacer) wait() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d := p.gap - time.Since(p.last); d > 0 {
		time.Sleep(d)
	}
	p.last = time.Now()
}

var (
	lrclibPace  = &pacer{gap: 600 * time.Millisecond}
	neteasePace = &pacer{gap: 800 * time.Millisecond}
)

// lrclibDoJSON issues a GET and decodes into out (a pointer). Returns the
// pointer back and ok on success; transient reports whether a failure was
// retryable (network/429/5xx/decode) as opposed to a definitive not-found.
func lrclibDoJSON(rawURL string, out interface{}) (data interface{}, ok, transient bool) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, false, false
	}
	req.Header.Set("User-Agent", lrclibUserAgent)
	lrclibPace.wait()
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, true
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, false, transientStatus(resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, false, true
	}
	return out, true, false
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
// on any miss/failure (transient reports whether the miss was retryable).
func neteaseFetch(artist, title string, durationSec float64) (string, bool) {
	id, transient := neteaseSearchBest(artist, title, durationSec)
	if id == 0 {
		return "", transient
	}
	return neteaseLyric(id)
}

// neteaseSearchBest returns the song id of the best candidate, or 0.
// Preference order: candidates whose artist matches ours (loose, case- and
// space-insensitive substring either way) beat non-matching ones; within a
// class, smallest |duration delta| wins. When our duration is known, a best
// delta beyond the tolerance rejects the whole result — wrong-version lyrics
// are worse than none (the "Bad" track canary philosophy).
func neteaseSearchBest(artist, title string, durationSec float64) (int64, bool) {
	query := strings.TrimSpace(artist + " " + title)
	if query == "" {
		return 0, false
	}
	form := url.Values{}
	form.Set("s", query)
	form.Set("type", "1")
	form.Set("limit", "10")
	req, err := http.NewRequest("POST", neteaseSearchURL, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var out neteaseSearchResp
	if ok, transient := neteaseDoJSON(req, &out); !ok {
		return 0, transient
	}
	songs := out.Result.Songs
	if len(songs) == 0 {
		return 0, false
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
		return 0, false
	}
	return bestID, false
}

// neteaseLyric fetches the LRC text for a song id. Empty or
// instrumental-marker responses are treated as a (definitive) miss.
func neteaseLyric(id int64) (string, bool) {
	req, err := http.NewRequest("GET", fmt.Sprintf(neteaseLyricURL, id), nil)
	if err != nil {
		return "", false
	}
	var out neteaseLyricResp
	if ok, transient := neteaseDoJSON(req, &out); !ok {
		return "", transient
	}
	text := strings.TrimSpace(out.Lrc.Lyric)
	// "纯音乐" ("pure music") is NetEase's instrumental placeholder.
	if text == "" || strings.Contains(text, "纯音乐") {
		return "", false
	}
	return stripNeteaseCredits(text), false
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
// headers and decodes the JSON body. transient follows the same rules as
// lrclibDoJSON — a retryable failure must not become a permanent negative.
func neteaseDoJSON(req *http.Request, out interface{}) (ok, transient bool) {
	req.Header.Set("Referer", neteaseReferer)
	req.Header.Set("User-Agent", neteaseUserAgent)
	neteasePace.wait()
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, true
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, transientStatus(resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, true
	}
	return true, false
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
// v7: synced-first two-phase chain — tracks cached as plain retry every
// provider for a synced version before settling for plain again.
// v8: transient failures (rate-limit/network) no longer cache negatives, and
// definitive negatives expire after lyricsNegativeTTL — purges the bogus
// permanent negatives v7 wrote for tracks LRCLIB actually has (the "found
// some album tracks but not others" report).
const lyricsCacheVersion = "v8"

// lyricsNegativeTTL bounds how long a definitive "no lyrics anywhere" result
// is trusted. Lyrics databases grow daily (community uploads), so a not-found
// from last week says little about today. Positive entries never expire.
const lyricsNegativeTTL = 24 * time.Hour

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
	path := filepath.Join(dir, key+".txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if len(data) == 0 {
		// Negative entry: honour it only while fresh. An expired negative is
		// treated as a miss so the chain re-queries (and rewrites the entry,
		// resetting the clock, whatever the outcome).
		if st, err := os.Stat(path); err != nil || time.Since(st.ModTime()) > lyricsNegativeTTL {
			return "", false
		}
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
