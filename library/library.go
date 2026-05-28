// Package library indexes a music collection on disk: it walks a root
// folder, reads tags via the audio package, groups tracks into albums
// and artists, and persists the result in a gob cache so subsequent
// launches don't rescan the whole tree.
//
// The library model is deliberately read-only outside of Scan(): the UI
// holds a *Library pointer, displays from its sorted slices, and replaces
// the whole pointer when a rescan finishes. That keeps the rendering side
// free of locks and the scan side free of UI churn.
package library

import (
	"encoding/gob"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gomusic/audio"
)

// Track is one playable file inside an album.
type Track struct {
	Path     string
	Title    string
	Artist   string // per-track artist; may differ from Album.Artist on compilations
	TrackNum int    // disc-relative track number, 0 if absent in tags
}

// Album bundles every Track that shares an (album-artist, album-title) pair.
// Cover bytes are held inline so the UI can render thumbnails without
// touching the filesystem; we keep only ONE picture per album (the first
// track's) to avoid carrying 200 KB of duplicated JPEG per song.
type Album struct {
	Artist    string
	Title     string
	Year      int
	CoverData []byte // raw bytes from tag picture frame
	CoverMIME string // "image/jpeg", "image/png", etc.
	Tracks    []*Track
}

// Artist groups every album whose AlbumArtist (falling back to per-track
// Artist) matches. Albums are sorted year-asc then title-asc.
type Artist struct {
	Name   string
	Albums []*Album
}

// Library is the rendered, sorted index. After Scan() returns, all slices
// are stable and ready to feed Fyne widgets directly.
type Library struct {
	Root    string
	Albums  []*Album
	Artists []*Artist

	// Index maps used during scan and for click-to-find lookups. Not
	// serialized — they get rebuilt from the slices on Load().
	albumByKey   map[string]*Album  `gob:"-"`
	artistByName map[string]*Artist `gob:"-"`
}

// New returns an empty Library ready to Scan a root.
func New() *Library {
	return &Library{
		albumByKey:   make(map[string]*Album),
		artistByName: make(map[string]*Artist),
	}
}

// Progress is the callback shape used by Scan. done counts files processed
// so far; total is the total to process. UIs typically render this as a
// progress bar / percentage. Called from the scan goroutine.
type Progress func(done, total int)

// Scan walks root, reads tags from every supported audio file, and rebuilds
// the Library's Albums + Artists. The previous content is discarded.
//
// Pure stat/read I/O work — no decoding, no GPU, just tag extraction. On
// SSD with ~1000 FLAC files this completes in 1–3 s; spinning disks or
// large covers slow it down proportionally.
//
// Returns the FIRST error encountered if the WalkDir itself fails (e.g.
// the root doesn't exist). Per-file tag-read failures are silently skipped
// — those files just get a fallback title from the filename.
func (l *Library) Scan(root string, progress Progress) error {
	if root == "" {
		return errors.New("library: empty root")
	}
	if fi, err := os.Stat(root); err != nil {
		return err
	} else if !fi.IsDir() {
		return errors.New("library: root is not a directory")
	}

	l.Root = root
	l.albumByKey = make(map[string]*Album)
	l.artistByName = make(map[string]*Artist)
	l.Albums = nil
	l.Artists = nil

	// Two-pass walk: first count total files (so the progress callback
	// can show a meaningful percentage), then index each one.
	var allPaths []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if isSupportedAudio(path) {
			allPaths = append(allPaths, path)
		}
		return nil
	})

	total := len(allPaths)
	if progress != nil {
		progress(0, total)
	}

	for i, path := range allPaths {
		md := audio.ReadFileMetadata(path)
		l.addFile(path, md)
		if progress != nil && (i+1)%16 == 0 {
			progress(i+1, total)
		}
	}
	if progress != nil {
		progress(total, total)
	}

	// Second pass: for every album whose tag-embedded cover came up
	// empty, look for "cover.jpg" / "folder.jpg" / etc. next to its
	// audio files. This is the standard Foobar/MusicBee/Roon layout —
	// no embedded art in the FLAC, separate JPG in the album folder.
	l.fillFolderCovers()

	l.finalize()
	return nil
}

// fillFolderCovers walks each album, picks the directory of its first
// track, and tries a handful of well-known filenames for the cover. It's
// best-effort: if nothing matches we just leave CoverData empty and the
// UI renders the placeholder glyph.
func (l *Library) fillFolderCovers() {
	candidates := []string{
		"cover.jpg", "cover.jpeg", "cover.png",
		"folder.jpg", "folder.jpeg", "folder.png",
		"album.jpg", "album.jpeg", "album.png",
		"front.jpg", "front.jpeg", "front.png",
		"Cover.jpg", "Folder.jpg", "Album.jpg", "Front.jpg",
	}
	// Two albums whose tracks live in the same folder share the cover.
	// Memoise so we don't re-read the same JPG twice.
	seenDir := make(map[string][]byte)
	mimeDir := make(map[string]string)

	for _, album := range l.albumByKey {
		if len(album.CoverData) > 0 || len(album.Tracks) == 0 {
			continue
		}
		dir := filepath.Dir(album.Tracks[0].Path)
		if data, ok := seenDir[dir]; ok {
			if len(data) > 0 {
				album.CoverData = data
				album.CoverMIME = mimeDir[dir]
			}
			continue
		}
		var (
			found []byte
			mime  string
		)
		for _, name := range candidates {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil && len(data) > 0 {
				found = data
				mime = mimeForExt(filepath.Ext(name))
				break
			}
		}
		seenDir[dir] = found
		mimeDir[dir] = mime
		if len(found) > 0 {
			album.CoverData = found
			album.CoverMIME = mime
		}
	}
}

func mimeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	}
	return "image/jpeg"
}

// addFile slots a single file into the in-progress index. Picks album-
// artist (with per-track artist as fallback) and album title (with a
// dedicated bucket for missing tags) so the result is never lossy: every
// scanned file ends up somewhere reachable from both the Albums and
// Artists views.
func (l *Library) addFile(path string, md *audio.Metadata) {
	if md == nil {
		md = &audio.Metadata{}
	}

	albumArtist := md.AlbumArtist
	if albumArtist == "" {
		albumArtist = md.Artist
	}
	if albumArtist == "" {
		albumArtist = "Unknown Artist"
	}
	albumTitle := md.Album
	if albumTitle == "" {
		albumTitle = "Unknown Album"
	}
	trackTitle := md.Title
	if trackTitle == "" {
		// Use the filename without extension as a fallback title.
		base := filepath.Base(path)
		trackTitle = strings.TrimSuffix(base, filepath.Ext(base))
	}
	perTrackArtist := md.Artist
	if perTrackArtist == "" {
		perTrackArtist = albumArtist
	}

	key := strings.ToLower(albumArtist) + " " + strings.ToLower(albumTitle)
	album, ok := l.albumByKey[key]
	if !ok {
		album = &Album{
			Artist: albumArtist,
			Title:  albumTitle,
			Year:   md.Year,
		}
		l.albumByKey[key] = album

		artistKey := strings.ToLower(albumArtist)
		artist, ok := l.artistByName[artistKey]
		if !ok {
			artist = &Artist{Name: albumArtist}
			l.artistByName[artistKey] = artist
		}
		artist.Albums = append(artist.Albums, album)
	}

	// Take the first non-empty cover we see — usually that's track 1.
	if len(album.CoverData) == 0 && len(md.PictureData) > 0 {
		album.CoverData = md.PictureData
		album.CoverMIME = md.PictureMIME
	}
	// If a later track carries a non-zero year and the album didn't, fill
	// it in. (Some libraries only tag the year on track 1; some on all.)
	if album.Year == 0 && md.Year > 0 {
		album.Year = md.Year
	}

	album.Tracks = append(album.Tracks, &Track{
		Path:     path,
		Title:    trackTitle,
		Artist:   perTrackArtist,
		TrackNum: md.TrackNum,
	})
}

// finalize sorts every internal slice and snapshots the maps into the
// Albums/Artists slices the UI consumes. Called once at the end of Scan
// (and again after Load deserializes from the cache).
func (l *Library) finalize() {
	for _, a := range l.albumByKey {
		sort.SliceStable(a.Tracks, func(i, j int) bool {
			ti, tj := a.Tracks[i], a.Tracks[j]
			if ti.TrackNum != tj.TrackNum {
				// Treat 0 (missing) as larger than any real number so
				// unnumbered tracks fall to the end instead of jumping
				// to the top of the album.
				if ti.TrackNum == 0 {
					return false
				}
				if tj.TrackNum == 0 {
					return true
				}
				return ti.TrackNum < tj.TrackNum
			}
			return strings.ToLower(ti.Title) < strings.ToLower(tj.Title)
		})
	}

	l.Albums = l.Albums[:0]
	for _, a := range l.albumByKey {
		l.Albums = append(l.Albums, a)
	}
	sort.Slice(l.Albums, func(i, j int) bool {
		ai, aj := l.Albums[i], l.Albums[j]
		if !strings.EqualFold(ai.Artist, aj.Artist) {
			return strings.ToLower(ai.Artist) < strings.ToLower(aj.Artist)
		}
		if ai.Year != aj.Year && ai.Year != 0 && aj.Year != 0 {
			return ai.Year < aj.Year
		}
		return strings.ToLower(ai.Title) < strings.ToLower(aj.Title)
	})

	l.Artists = l.Artists[:0]
	for _, a := range l.artistByName {
		sort.Slice(a.Albums, func(i, j int) bool {
			ai, aj := a.Albums[i], a.Albums[j]
			if ai.Year != aj.Year && ai.Year != 0 && aj.Year != 0 {
				return ai.Year < aj.Year
			}
			return strings.ToLower(ai.Title) < strings.ToLower(aj.Title)
		})
		l.Artists = append(l.Artists, a)
	}
	sort.Slice(l.Artists, func(i, j int) bool {
		return strings.ToLower(l.Artists[i].Name) < strings.ToLower(l.Artists[j].Name)
	})
}

// Save serializes the library to a gob file. Only the slices are encoded;
// the lookup maps are rebuilt on Load (encoding them would double the file
// size and they're trivial to reconstruct).
func (l *Library) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// Encode a small wrapper struct so future format bumps can add fields
	// without breaking older caches (we'd just gob-skip unknown fields).
	w := wireFormat{
		Version: 1,
		Root:    l.Root,
		Albums:  l.Albums,
		Artists: l.Artists,
	}
	return gob.NewEncoder(f).Encode(&w)
}

// Load reads a previously saved library. Returns (nil, err) on missing
// file or decode failure — the caller falls back to rescanning.
func Load(path string) (*Library, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var w wireFormat
	if err := gob.NewDecoder(f).Decode(&w); err != nil {
		return nil, err
	}
	if w.Version != 1 {
		return nil, errors.New("library: unknown cache version")
	}

	l := New()
	l.Root = w.Root
	l.Albums = w.Albums
	l.Artists = w.Artists

	// Rebuild lookup maps so subsequent calls (e.g. find-album-by-key)
	// keep their cheap O(1) semantics.
	for _, a := range l.Albums {
		key := strings.ToLower(a.Artist) + " " + strings.ToLower(a.Title)
		l.albumByKey[key] = a
	}
	for _, ar := range l.Artists {
		l.artistByName[strings.ToLower(ar.Name)] = ar
	}
	return l, nil
}

// wireFormat is the on-disk representation. Versioned so we can evolve.
type wireFormat struct {
	Version int
	Root    string
	Albums  []*Album
	Artists []*Artist
}

// CachePath is the conventional location for the gob index next to the
// config file. UIs pass this to Save() and Load().
func CachePath(configDir string) string {
	return filepath.Join(configDir, "library.gob")
}

// isSupportedAudio mirrors playlist.IsSupported but lives here to avoid
// importing the playlist package (which would create a cycle in the
// reverse direction once the UI starts using both).
func isSupportedAudio(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".dsf", ".dff", ".flac", ".wav", ".mp3":
		return true
	}
	return false
}
