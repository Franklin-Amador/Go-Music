// Package playlist manages an ordered list of audio tracks.
package playlist

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
)

var supportedExts = map[string]bool{
	".dsf":  true,
	".dff":  true,
	".flac": true,
	".wav":  true,
	".mp3":  true,
}

// Track represents a single entry in the playlist.
type Track struct {
	Path  string
	Title string // filename without extension
}

// Playlist holds an ordered list of tracks and a current index.
//
// Shuffle mode plays the tracks in a randomized order, each track exactly
// once per cycle. When enabled, the current track stays at the head of the
// shuffled order so playback continues uninterrupted; Next/Prev then walk
// the permutation instead of the linear list. Disabling shuffle returns to
// the natural order at the current track.
type Playlist struct {
	Tracks  []Track
	Current int

	Shuffle      bool
	shuffleOrder []int            // permutation of indices; only valid while Shuffle is true
	shufflePos   int              // index into shuffleOrder pointing at Current
	paths        map[string]bool  // dedup set; prevents adding the same file twice
}

// New creates an empty playlist.
func New() *Playlist {
	return &Playlist{Current: -1, paths: make(map[string]bool)}
}

// Add appends a file to the playlist if its extension is supported.
// Duplicate paths are silently ignored.
func (p *Playlist) Add(path string) {
	ext := strings.ToLower(filepath.Ext(path))
	if !supportedExts[ext] {
		return
	}
	if p.paths == nil {
		p.paths = make(map[string]bool)
	}
	if p.paths[path] {
		return
	}
	p.paths[path] = true
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	p.Tracks = append(p.Tracks, Track{Path: path, Title: title})
	if p.Current < 0 {
		p.Current = 0
	}
	// New tracks invalidate any in-flight shuffle order. Rebuild lazily on
	// the next Next/Prev so we don't pay for it on every Add during a bulk
	// AddDir.
	p.shuffleOrder = nil
}

// AddDir scans a directory recursively and adds every supported file it
// finds. Subdirectories like "Deezer/", "Spotify/", album folders, etc. all
// get walked. Symlinks and inaccessible entries are skipped silently.
func (p *Playlist) AddDir(dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip permission errors, missing entries, etc.
		}
		if d.IsDir() {
			return nil
		}
		p.Add(path)
		return nil
	})
}

// Remove deletes the track at index i.
func (p *Playlist) Remove(i int) {
	if i < 0 || i >= len(p.Tracks) {
		return
	}
	delete(p.paths, p.Tracks[i].Path)
	p.Tracks = append(p.Tracks[:i], p.Tracks[i+1:]...)
	if p.Current >= len(p.Tracks) {
		p.Current = len(p.Tracks) - 1
	}
	p.shuffleOrder = nil
}

// Move relocates the track at index `from` to index `to`. Out-of-range
// arguments or no-op moves are ignored. Current is updated so it still
// points at the same track after the move — i.e. the highlighted row
// follows the reorder. Shuffle order is invalidated since the natural-order
// indices it stored no longer point at the same tracks.
func (p *Playlist) Move(from, to int) {
	n := len(p.Tracks)
	if from < 0 || from >= n || to < 0 || to >= n || from == to {
		return
	}
	t := p.Tracks[from]
	// Remove from old position, then insert at new position.
	p.Tracks = append(p.Tracks[:from], p.Tracks[from+1:]...)
	p.Tracks = append(p.Tracks[:to], append([]Track{t}, p.Tracks[to:]...)...)
	// Patch Current so the same track stays "current".
	switch {
	case p.Current == from:
		p.Current = to
	case from < p.Current && to >= p.Current:
		p.Current--
	case from > p.Current && to <= p.Current:
		p.Current++
	}
	p.shuffleOrder = nil
}

// Clear removes all tracks.
func (p *Playlist) Clear() {
	p.Tracks = p.Tracks[:0]
	p.Current = -1
	p.shuffleOrder = nil
	p.paths = make(map[string]bool)
}

// CurrentTrack returns the current track, or nil if the playlist is empty.
func (p *Playlist) CurrentTrack() *Track {
	if p.Current < 0 || p.Current >= len(p.Tracks) {
		return nil
	}
	return &p.Tracks[p.Current]
}

// Next advances to the next track and returns it, or nil if at the end.
// In shuffle mode, "next" follows the shuffled permutation; the order ends
// after every track has been visited once.
func (p *Playlist) Next() *Track {
	if p.Len() == 0 {
		return nil
	}
	if p.Shuffle {
		p.ensureShuffleOrder()
		if p.shufflePos+1 >= len(p.shuffleOrder) {
			return nil
		}
		p.shufflePos++
		p.Current = p.shuffleOrder[p.shufflePos]
		return &p.Tracks[p.Current]
	}
	if p.Current+1 >= len(p.Tracks) {
		return nil
	}
	p.Current++
	return &p.Tracks[p.Current]
}

// PeekNext returns what Next() WOULD return without actually advancing the
// playlist. Used by the crossfade preloader: the UI wants to know which
// track to decode ahead of time, but should not change Current until the
// engine actually promotes the preload.
func (p *Playlist) PeekNext() *Track {
	if p.Len() == 0 {
		return nil
	}
	if p.Shuffle {
		// Same condition as Next() — if the next slot exists in the
		// shuffled permutation, peek at it.
		if len(p.shuffleOrder) != len(p.Tracks) {
			// Lazily build so we can peek even if Next hasn't been called yet.
			p.rebuildShuffleOrder()
		}
		if p.shufflePos+1 >= len(p.shuffleOrder) {
			return nil
		}
		return &p.Tracks[p.shuffleOrder[p.shufflePos+1]]
	}
	if p.Current+1 >= len(p.Tracks) {
		return nil
	}
	return &p.Tracks[p.Current+1]
}

// Prev moves to the previous track and returns it, or nil if at the start.
func (p *Playlist) Prev() *Track {
	if p.Len() == 0 {
		return nil
	}
	if p.Shuffle {
		p.ensureShuffleOrder()
		if p.shufflePos <= 0 {
			return nil
		}
		p.shufflePos--
		p.Current = p.shuffleOrder[p.shufflePos]
		return &p.Tracks[p.Current]
	}
	if p.Current <= 0 {
		return nil
	}
	p.Current--
	return &p.Tracks[p.Current]
}

// Len returns the number of tracks.
func (p *Playlist) Len() int {
	return len(p.Tracks)
}

// SetCurrent jumps directly to track i. In shuffle mode it splices the
// chosen track into the head of the remaining order, so subsequent Next
// calls don't replay tracks we've already visited.
func (p *Playlist) SetCurrent(i int) {
	if i < 0 || i >= len(p.Tracks) {
		return
	}
	p.Current = i
	if p.Shuffle {
		p.rebuildShuffleOrder()
	}
}

// ToggleShuffle flips shuffle mode. Enabling rebuilds the permutation so the
// current track is the head; disabling drops the permutation.
func (p *Playlist) ToggleShuffle() {
	p.Shuffle = !p.Shuffle
	if p.Shuffle {
		p.rebuildShuffleOrder()
	} else {
		p.shuffleOrder = nil
	}
}

// ensureShuffleOrder builds a permutation if one isn't cached.
func (p *Playlist) ensureShuffleOrder() {
	if len(p.shuffleOrder) == len(p.Tracks) {
		return
	}
	p.rebuildShuffleOrder()
}

// rebuildShuffleOrder produces a fresh Fisher–Yates permutation of every
// track index, with the current track moved to position 0 so playback
// continues from where the user is right now.
func (p *Playlist) rebuildShuffleOrder() {
	n := len(p.Tracks)
	p.shuffleOrder = make([]int, n)
	for i := range p.shuffleOrder {
		p.shuffleOrder[i] = i
	}
	rand.Shuffle(n, func(i, j int) {
		p.shuffleOrder[i], p.shuffleOrder[j] = p.shuffleOrder[j], p.shuffleOrder[i]
	})
	// Place the current track at position 0.
	for i, idx := range p.shuffleOrder {
		if idx == p.Current {
			p.shuffleOrder[0], p.shuffleOrder[i] = p.shuffleOrder[i], p.shuffleOrder[0]
			break
		}
	}
	p.shufflePos = 0
}

// IsSupported returns true if the file extension is playable.
func IsSupported(path string) bool {
	return supportedExts[strings.ToLower(filepath.Ext(path))]
}
