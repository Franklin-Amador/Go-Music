package audio

import (
	"io"
	"os"

	"github.com/dhowden/tag"
)

// Metadata is a per-file bundle of tag info + embedded album art (if any).
// Fields are best-effort: missing tags map to empty strings, missing art to
// a nil PictureData slice.
type Metadata struct {
	Title       string
	Artist      string
	AlbumArtist string // "album artist" tag — the catalog-grouping artist
	Album       string
	Year        int    // year tag, 0 if absent
	TrackNum    int    // track number within the album, 0 if absent
	Lyrics      string // raw lyrics text — may be plain or LRC-format
	PictureData []byte
	PictureMIME string // "image/jpeg", "image/png", etc.
}

// ReadFileMetadata is the public, format-aware entry point used by the
// library scanner: it figures out the right offset for DSF files (where
// the ID3v2 stream sits past the audio data) and delegates to ReadMetadata.
// Returns nil if metadata can't be extracted — never an error.
func ReadFileMetadata(path string) *Metadata {
	if IsDSDFile(path) {
		// DSF stores ID3v2 at a known offset; ParseDSD reads the headers
		// (cheap, no audio decode) and returns it. DFF has no standard
		// metadata location — ParseDSD's MetadataOffset is 0 for DFF and
		// ReadMetadata then just tries the file from byte 0, which usually
		// finds nothing for DFF. Either way: best-effort, won't crash.
		dsd, err := ParseDSD(path, outputSampleRate)
		if err != nil {
			return nil
		}
		return ReadMetadata(path, dsd.MetadataOffset)
	}
	return ReadMetadata(path, 0)
}

// ReadMetadata extracts tags + embedded picture from a music file.
//
// dsdMetadataOffset > 0 selects the DSF code path: the file is wrapped with a
// SectionReader starting at the offset of the embedded ID3v2 stream so the
// tag library auto-detects ID3v2 instead of the DSF magic at byte 0.
//
// For FLAC / MP3 / WAV the offset is 0 and tag.ReadFrom auto-detects directly
// from the file start.
//
// Returns nil if no metadata can be read — never an error. Tag parsing is
// best-effort UI sugar; bad tags should not break playback.
func ReadMetadata(path string, dsdMetadataOffset int64) *Metadata {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var reader io.ReadSeeker = f
	if dsdMetadataOffset > 0 {
		fi, err := f.Stat()
		if err != nil {
			return nil
		}
		size := fi.Size() - dsdMetadataOffset
		if size <= 0 {
			return nil
		}
		reader = io.NewSectionReader(f, dsdMetadataOffset, size)
	}

	m, err := tag.ReadFrom(reader)
	if err != nil {
		return nil
	}

	trackNum, _ := m.Track()
	out := &Metadata{
		Title:       m.Title(),
		Artist:      m.Artist(),
		AlbumArtist: m.AlbumArtist(),
		Album:       m.Album(),
		Year:        m.Year(),
		TrackNum:    trackNum,
		Lyrics:      m.Lyrics(),
	}
	if pic := m.Picture(); pic != nil {
		out.PictureData = pic.Data
		out.PictureMIME = pic.MIMEType
	}
	return out
}
