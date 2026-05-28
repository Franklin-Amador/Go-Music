package audio

import (
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/oto/v3"
)

// Output format is fixed per process — oto v3 only allows one context.
//
// 176 400 Hz stereo float32 is chosen for high-quality DSD playback:
//   - Every DSD rate (2.8224, 5.6448, 11.2896, 22.5792 MHz) divides 176 400
//     by an integer (16, 32, 64, 128) — so the DSD path is a single-stage
//     decimation with NO post-decimation resampling. That is what eliminates
//     the high-frequency DSD-noise aliasing ("hormigueo") that any second
//     resampling stage would introduce.
//   - 44 100 / 88 200 PCM upsamples to 176 400 by a clean integer ratio.
//   - 48 000 / 96 000 / 192 000 PCM use the polyphase Kaiser-sinc resampler
//     with ~80 dB stopband — well-behaved.
//   - Windows WASAPI shared mode handles the final conversion to the device's
//     native rate using its own high-quality resampler.
const (
	outputSampleRate uint32 = 176400
	outputChannels   int    = 2
)

// audioPlayer is the common interface implemented by both *oto.Player (WASAPI
// shared mode via oto v3) and *malgoPlayer (WASAPI exclusive mode via malgo).
// The engine holds a single audioPlayer field and never touches the concrete
// type directly — switching backends is transparent to the rest of the code.
type audioPlayer interface {
	Play()
	Pause()
	IsPlaying() bool
	BufferedSize() int
	Close() error
}

// State represents the playback state.
type State int32

const (
	StateStopped State = iota
	StateLoading
	StatePlaying
	StatePaused
	StateBuffering
)

func (s State) String() string {
	return [...]string{"Stopped", "Loading", "Playing", "Paused", "Buffering"}[s]
}

// FileInfo holds metadata about the loaded audio file.
type FileInfo struct {
	Path          string
	Format        string
	SampleRate    uint32
	PCMSampleRate uint32
	BitDepth      int
	Channels      uint32
	Duration      float64
	BitrateKbps   int
	FileSizeMB    float64
	IsDSD         bool
	DSDLabel      string
	QualityLabel  string

	// Tags + embedded album art (best-effort; empty/nil if absent)
	Title       string
	Artist      string
	Album       string
	PictureData []byte
	PictureMIME string
}

// Engine is the main audio playback engine.
//
// Callbacks (called from internal goroutines — use thread-safe UI updates):
//
//	OnStateChange(State)
//	OnPositionChange(posSec, durSec float64)
//	OnError(msg string)
//	OnFinished()                — natural end of the last track (no crossfade)
//	OnTrackTransition(newInfo)  — crossfade just promoted a preloaded track
type Engine struct {
	OnStateChange     func(State)
	OnPositionChange  func(pos, dur float64)
	OnError           func(msg string)
	OnFinished        func()
	OnTrackTransition func(info *FileInfo)

	Info   *FileInfo
	Volume float32

	// Vis collects rolling samples and exposes spectrum data for the UI.
	Vis *Visualizer

	state  atomic.Int32
	stopCh chan struct{}

	// finishedSignaled goes true the moment OnFinished is invoked for the
	// current playback. Cleared on each Play(). Prevents a double auto-advance
	// when the io.EOF path and the watchPlayer poller race to fire.
	finishedSignaled atomic.Bool

	otoCtx *oto.Context
	player audioPlayer // either *malgoPlayer (exclusive) or *oto.Player (shared)

	// PCM path (data is already at outputSampleRate / outputChannels after Load)
	pcmData        []float32
	pcmPos         atomic.Int64 // frame position at output rate
	pcmFrames      int64        // total frames at output rate (full target size)
	pcmFramesReady atomic.Int64 // frames currently decoded + resampled into pcmData

	// pcmStopCh is closed by stopInternal to signal the background streaming
	// decoder to exit. Separate from the playback stopCh because a paused
	// player should NOT cancel the in-flight decode — we still want the
	// background to keep filling so resume / seek-ahead is instant.
	pcmStopCh chan struct{}

	// DSD path
	ring     *ringBuffer
	dsdEOF   atomic.Bool
	dsdReady chan struct{}
	dsdInfo  *DSDInfo
	// dsdDone is closed by the DSD producer when it finishes (whether via
	// stopCh, EOF, or error). stopInternal waits on it so the OLD producer
	// is fully gone before a new one can start — without this, a seek-driven
	// restart races: the old producer's tail end stores `dsdEOF=true` and
	// closes `dsdReady`, both of which already belong to the NEW producer's
	// state, and the stream reader sees a phantom EOF.
	dsdDone chan struct{}

	// DSD seek state: when non-zero, the next playDSD starts the file reader
	// at AudioOffset + dsdSeekBytes (block-aligned) instead of at
	// AudioOffset, and Position() reports dsdSeekTime + framesConsumed/rate.
	// Both reset on Load and on a fresh user-initiated Play from rest.
	dsdSeekBytes int64
	dsdSeekTime  float64

	// exclusiveDisabled lets the user force WASAPI shared mode (so other
	// apps keep playing alongside us at the cost of going through the
	// Windows mixer / resampler). Default false = try exclusive first.
	exclusiveDisabled atomic.Bool

	// ── Crossfade ────────────────────────────────────────────────────────
	// Stored as milliseconds for cheap atomic loads from the audio thread.
	// 0 means crossfading is disabled — pcmReader runs its original fast
	// path with zero mixing overhead.
	crossfadeMs atomic.Int64

	// cfMu guards every crossfade field below. Held briefly by the audio
	// thread on fade-start and fade-end, and by the preload goroutine when
	// publishing a decoded next track.
	cfMu sync.Mutex

	// Preloaded "next" track. Decoded in Preload() and parked here until
	// the pcmReader notices we're within the fade window. Crossfade is
	// PCM-only: Preload no-ops for DSD inputs.
	nextData    []float32
	nextFrames  int64
	nextInfo    *FileInfo
	nextPath    string
	nextLoading atomic.Bool

	// Outgoing (fading-out) track. Populated by tryStartCrossfade as the
	// fade begins, then consumed by pcmReader frame by frame until the
	// remaining-frames counter hits zero. crossfadeFramesTotal stays fixed
	// for the lifetime of one fade so the per-frame gain ramp is stable
	// even if the user changes the crossfade duration mid-fade.
	outgoingData         []float32
	outgoingPos          int64
	outgoingFrames       int64
	outgoingRemaining    int64
	crossfadeFramesTotal int64

	mu sync.Mutex
}

// NewEngine creates a new Engine. Call Load before Play.
func NewEngine() *Engine {
	e := &Engine{
		Volume:            1.0,
		OnStateChange:     func(State) {},
		OnPositionChange:  func(_, _ float64) {},
		OnError:           func(string) {},
		OnFinished:        func() {},
		OnTrackTransition: func(*FileInfo) {},
	}
	e.state.Store(int32(StateStopped))
	// 1024-pt FFT, 32 log-spaced bands. Window covers ~6 ms at 176.4 kHz.
	e.Vis = NewVisualizer(outputSampleRate, outputChannels, 1024, 32)
	return e
}

// ── Public API ───────────────────────────────────────────────────────────────

// Load opens and decodes metadata for the given file.
// PCM files are decoded fully and resampled to the output rate.
// DSD files only have headers parsed here; streaming starts on Play.
//
// Memory hygiene: PCM is held fully resident at 176 400 Hz stereo float32,
// which is ~170 MB per minute of audio. Without explicit help, each new track
// load only marks the previous buffer eligible for GC — the Go runtime is
// lazy about reclaiming the heap and especially lazy about returning memory
// to the OS, so task-manager RSS climbs into the gigabytes after a few track
// changes. We drop our refs, force a GC pass, and ask the runtime to release
// reclaimed pages immediately. The cost is a brief (~tens of ms) pause; the
// payoff is steady-state RAM that stays roughly proportional to the current
// track instead of accumulating.
func (e *Engine) Load(path string) (*FileInfo, error) {
	e.stopInternal()
	e.setState(StateLoading)

	// Drop references to the previous track's heavy buffers BEFORE decoding
	// the new one. Otherwise decode-time peak memory = old buffers + decode
	// scratch + new buffers, which is what tips the process over 1 GB.
	//
	//   - pcmData: up to ~170 MB/minute of audio at 176 400 Hz stereo float32
	//   - ring:    20 s × 176 400 × 2 × 4 = ~28 MB (DSD playback only)
	//   - Info:    metadata + embedded album art (a handful of MB on tracks
	//              with large cover JPEGs)
	e.pcmData = nil
	e.ring = nil
	e.Info = nil
	runtime.GC()
	debug.FreeOSMemory()

	var (
		info *FileInfo
		err  error
	)
	if IsDSDFile(path) {
		info, err = e.loadDSD(path)
	} else {
		info, err = e.loadPCM(path)
	}
	if err != nil {
		e.setState(StateStopped)
		return nil, err
	}
	e.Info = info
	e.dsdSeekBytes, e.dsdSeekTime = 0, 0
	e.setState(StateStopped)

	// After load: the resampler's transient scratch (kernels, parallel-worker
	// buffers, the pre-resample source slice) is now garbage. Force one more
	// pass + OS return so the steady-state RSS matches what we actually hold.
	runtime.GC()
	debug.FreeOSMemory()

	return info, nil
}

// Play starts or resumes playback.
func (e *Engine) Play() error {
	if State(e.state.Load()) == StatePaused {
		return e.resume()
	}
	if e.Info == nil {
		return fmt.Errorf("no file loaded")
	}
	if e.Info.IsDSD {
		return e.playDSD()
	}
	return e.playPCM()
}

// Pause pauses playback without resetting position.
func (e *Engine) Pause() {
	if State(e.state.Load()) != StatePlaying {
		return
	}
	e.mu.Lock()
	if e.player != nil {
		e.player.Pause()
	}
	e.mu.Unlock()
	e.setState(StatePaused)
}

// Stop halts playback and resets position.
func (e *Engine) Stop() {
	e.stopInternal()
}

// Seek moves the playback position in seconds. Works for both PCM and DSD.
//
// For PCM the source data is already fully decoded in memory, so the seek is
// instant: bump pcmPos and Reset the oto player to drop the ~200 ms of stale
// audio it had buffered ahead. Without the Reset, the user clicks the
// progress bar and hears the OLD position for a fraction of a second before
// the new one kicks in — visually that looks like the slider snapped back.
//
// For DSD the source is streamed lazily through a long FIR; there's no
// pre-decoded buffer to index into. We stop the producer goroutine, drain
// the ring, store the byte offset corresponding to the target time, and
// kick off playDSD again — which re-opens the file and seeks to the new
// offset. There is a brief restart (well under one second on typical
// hardware) but the audio resumes cleanly at the new position.
func (e *Engine) Seek(seconds float64) {
	if e.Info == nil {
		return
	}
	if seconds < 0 {
		seconds = 0
	}
	if seconds >= e.Info.Duration {
		seconds = e.Info.Duration - 0.1
	}

	if !e.Info.IsDSD {
		frame := int64(seconds * float64(outputSampleRate))
		if frame >= e.pcmFrames {
			frame = e.pcmFrames - 1
		}
		e.pcmPos.Store(frame)
		// Any in-flight crossfade is mixing leftover audio from the
		// previous track; once the user seeks into the new track that
		// fade no longer makes musical sense. Drop it cleanly.
		e.cfMu.Lock()
		e.outgoingData = nil
		e.outgoingPos = 0
		e.outgoingFrames = 0
		e.outgoingRemaining = 0
		e.crossfadeFramesTotal = 0
		e.cfMu.Unlock()
		// NOTE: oto's internal buffer (~200 ms) still holds samples from the
		// OLD pcmPos and will play those first before the next pcmReader.Read
		// pulls from the new position. Audibly that's a brief "delay" before
		// the jump takes effect. We previously called oto.Player.Reset() to
		// flush that buffer, but Reset both clears the buffer AND silently
		// pauses the player — and the gap between Reset and the follow-up
		// Play() lets watchPlayerDone poll IsPlaying()==false while engine
		// state==Playing, which fires OnFinished and auto-advances. The fix
		// attempts (holding e.mu, two-poll detection) created their own
		// issues. The unflushed-buffer behaviour is benign and predictable.
		return
	}

	// DSD path: full producer restart at the new byte offset.
	wasPlaying := State(e.state.Load()) == StatePlaying || State(e.state.Load()) == StatePaused
	e.stopInternal()
	if !wasPlaying {
		return
	}
	e.dsdSeekBytes, e.dsdSeekTime = computeDSDSeekOffset(e.dsdInfo, seconds)
	_ = e.playDSD()
}

// computeDSDSeekOffset returns (byteOffsetIntoAudio, actualSeekTimeSec) for
// a target seek time. The byte offset is rounded down to the nearest block
// boundary so DSF block unpacking stays aligned; for DFF (no block layout)
// it's just rounded down to the nearest channel-aligned byte. The
// actual-time return reflects that rounding so Position() doesn't lie.
func computeDSDSeekOffset(info *DSDInfo, seconds float64) (int64, float64) {
	if info == nil || info.DSDSampleRate == 0 {
		return 0, 0
	}
	bitsPerChannel := int64(seconds * float64(info.DSDSampleRate))
	if bitsPerChannel < 0 {
		bitsPerChannel = 0
	}
	var byteOffset int64
	if !info.IsDFF {
		bs := int64(info.BlockSize)
		if bs == 0 {
			bs = 1
		}
		bitsPerBlock := bs * 8
		blocks := bitsPerChannel / bitsPerBlock
		byteOffset = blocks * bs * int64(info.Channels)
		bitsPerChannel = blocks * bitsPerBlock
	} else {
		// DFF: bits are interleaved across channels. Round to whole bytes.
		totalBits := bitsPerChannel * int64(info.Channels)
		byteOffset = (totalBits / 8) / int64(info.Channels) * int64(info.Channels)
		bitsPerChannel = (byteOffset * 8) / int64(info.Channels)
	}
	if byteOffset >= info.AudioSize {
		byteOffset = info.AudioSize - 1
	}
	actualSec := float64(bitsPerChannel) / float64(info.DSDSampleRate)
	return byteOffset, actualSec
}

// Position returns the current playback position in seconds.
//
// We subtract oto.Player.BufferedSize() so the value reflects what the DAC is
// actually emitting right now, not what the reader has handed downstream.
// Without that compensation the reported position runs ~200 ms ahead of audible
// playback, which makes synced lyrics flip a line before it's sung — most
// noticeable on DSD because the producer is always staging well ahead of oto.
func (e *Engine) Position() float64 {
	if e.Info == nil {
		return 0
	}
	var pos float64
	if e.Info.IsDSD && e.ring != nil {
		// Ring stores interleaved samples at outputSampleRate × outputChannels.
		// After a seek, the producer started at dsdSeekTime, so frames consumed
		// from the ring measure progress *since the seek*.
		framesConsumed := e.ring.readPos() / int64(outputChannels)
		pos = e.dsdSeekTime + float64(framesConsumed)/float64(outputSampleRate)
	} else {
		pos = float64(e.pcmPos.Load()) / float64(outputSampleRate)
	}

	e.mu.Lock()
	player := e.player
	e.mu.Unlock()
	if player != nil {
		// float32 stereo → 4 bytes per sample × outputChannels per frame.
		const bytesPerFrame = 4 * outputChannels
		bufferedFrames := player.BufferedSize() / bytesPerFrame
		pos -= float64(bufferedFrames) / float64(outputSampleRate)
		if pos < 0 {
			pos = 0
		}
	}
	return pos
}

// CurrentState returns the current playback state.
func (e *Engine) CurrentState() State {
	return State(e.state.Load())
}

// ── Crossfade ────────────────────────────────────────────────────────────────
//
// Crossfade design (PCM-only):
//
// The UI calls Preload(nextPath) a few seconds before the current track is
// expected to end. The engine decodes that track into a parked "next" slot
// without touching playback.
//
// When pcmReader.Read notices the current track has fewer frames left than
// the configured crossfade window AND a preloaded track is sitting in the
// next slot, it calls tryStartCrossfade. That:
//   - moves the current track + its read position into the "outgoing" slot
//   - promotes the preloaded next into the active slot (pcmData / pcmPos=0)
//   - fires OnTrackTransition so the UI can update title / cover / lyrics
//
// From that point on Read mixes the new track and the outgoing one with a
// linear ramp until outgoingRemaining hits zero, then plays the new track
// straight through. The promote happens inside the audio thread without
// stopping the device, so there is no audible gap.
//
// DSD tracks bypass crossfade entirely: the DSD path streams through a
// producer goroutine and a ring buffer, which can't be mixed against
// without substantial extra plumbing.

// SetExclusiveMode toggles WASAPI exclusive mode for FUTURE Play() calls.
// true (default) tries malgo exclusive first, falling back to oto shared
// if the DAC rejects exclusive or another app holds it. false skips the
// malgo attempt entirely so other apps keep playing through the Windows
// mixer alongside us. Takes effect on the next Load()→Play() cycle —
// already-running playback keeps whatever backend it opened with.
func (e *Engine) SetExclusiveMode(enabled bool) {
	e.exclusiveDisabled.Store(!enabled)
}

// ExclusiveMode reports the user's current preference (not what backend
// the current player is actually using).
func (e *Engine) ExclusiveMode() bool {
	return !e.exclusiveDisabled.Load()
}

// SetCrossfade configures the crossfade duration in seconds. Pass 0 to
// disable crossfading entirely (default). Values are clamped to [0, 30].
// Safe to call at any time; the audio thread reads it atomically.
func (e *Engine) SetCrossfade(sec float64) {
	if sec < 0 {
		sec = 0
	}
	if sec > 30 {
		sec = 30
	}
	e.crossfadeMs.Store(int64(sec * 1000))
}

// CrossfadeSeconds returns the currently configured crossfade duration.
func (e *Engine) CrossfadeSeconds() float64 {
	return float64(e.crossfadeMs.Load()) / 1000
}

// Preload decodes path into the engine's "next" slot in a background
// goroutine. When the current track approaches its end the engine fades
// from the current to this preload automatically.
//
// No-op if crossfade is disabled, the same path is already preloaded,
// another preload is in flight, the path is a DSD file, or the current
// track is DSD (mixed crossfade with DSD is not supported).
func (e *Engine) Preload(path string) {
	if e.crossfadeMs.Load() == 0 {
		return
	}
	if IsDSDFile(path) {
		return
	}
	if e.Info != nil && e.Info.IsDSD {
		return
	}

	e.cfMu.Lock()
	if e.nextPath == path && e.nextData != nil {
		e.cfMu.Unlock()
		return
	}
	if e.nextLoading.Load() {
		e.cfMu.Unlock()
		return
	}
	e.nextLoading.Store(true)
	e.nextPath = path // claim the slot so concurrent Preload calls bail
	e.cfMu.Unlock()

	go func() {
		defer e.nextLoading.Store(false)

		data, sr, ch, bd, err := decodePCM(path)
		if err != nil {
			log.Printf("preload decode failed: %v", err)
			e.cfMu.Lock()
			if e.nextPath == path {
				e.nextPath = ""
			}
			e.cfMu.Unlock()
			return
		}
		data = adaptChannels(data, int(ch), outputChannels)
		if sr != outputSampleRate {
			data = resamplePCM(data, outputChannels, sr, outputSampleRate)
		}
		dc := newDCBlocker(outputChannels)
		dc.Process(data, outputChannels)
		softLimit(data)

		frames := int64(len(data)) / int64(outputChannels)
		dur := float64(frames) / float64(outputSampleRate)
		ext := strings.ToUpper(strings.TrimPrefix(filepath.Ext(path), "."))

		stat, _ := os.Stat(path)
		var sizeMB float64
		if stat != nil {
			sizeMB = float64(stat.Size()) / 1_048_576
		}
		info := &FileInfo{
			Path:          path,
			Format:        ext,
			SampleRate:    sr,
			PCMSampleRate: sr,
			BitDepth:      bd,
			Channels:      ch,
			Duration:      dur,
			FileSizeMB:    sizeMB,
			QualityLabel:  fmt.Sprintf("%s • %dbit/%dkHz • %dch", ext, bd, sr/1000, ch),
		}
		if md := ReadMetadata(path, 0); md != nil {
			info.Title = md.Title
			info.Artist = md.Artist
			info.Album = md.Album
			info.PictureData = md.PictureData
			info.PictureMIME = md.PictureMIME
		}

		e.cfMu.Lock()
		// Caller may have invalidated the slot (ClearPreload, Load, Stop)
		// while we were decoding. Don't publish in that case.
		if e.nextPath != path {
			e.cfMu.Unlock()
			return
		}
		e.nextData = data
		e.nextFrames = frames
		e.nextInfo = info
		e.cfMu.Unlock()
	}()
}

// ClearPreload drops any preloaded next track. Called by the UI when the
// user picks a different track manually, so the engine doesn't crossfade
// into a track the user no longer wants to hear.
func (e *Engine) ClearPreload() {
	e.cfMu.Lock()
	e.nextData = nil
	e.nextFrames = 0
	e.nextInfo = nil
	e.nextPath = ""
	e.cfMu.Unlock()
}

// tryStartCrossfade swaps the preloaded track into the active slot and
// parks the current track in the outgoing slot. Called from pcmReader.Read
// the moment we cross into the fade window with a preload available.
// Returns true if a fade was started, false if it couldn't (no preload,
// already fading, etc.). The caller should re-read pcmPos / pcmFrames
// afterwards because the swap resets pcmPos to 0.
func (e *Engine) tryStartCrossfade(remainingCurrentFrames int64) bool {
	e.cfMu.Lock()
	if e.nextData == nil || e.outgoingData != nil {
		e.cfMu.Unlock()
		return false
	}

	// Move current → outgoing
	e.outgoingData = e.pcmData
	e.outgoingPos = e.pcmPos.Load()
	e.outgoingFrames = e.pcmFrames

	fadeFrames := int64(e.crossfadeMs.Load()) * int64(outputSampleRate) / 1000
	// If the current track is shorter than the configured fade window, the
	// fade collapses to whatever remains. Otherwise the gain ramp would
	// overshoot when outgoingRemaining hits 0 before fade completes.
	if fadeFrames > remainingCurrentFrames {
		fadeFrames = remainingCurrentFrames
	}
	if fadeFrames < 1 {
		fadeFrames = 1
	}
	e.outgoingRemaining = fadeFrames
	e.crossfadeFramesTotal = fadeFrames

	// Promote next → current. The Info pointer assignment is a single
	// word on amd64 and Position() / Seek() read it without a lock; that
	// matches how Load() already mutates Info from the UI goroutine while
	// the same reads happen elsewhere. The race detector flags it but in
	// practice the load-acquire / store-release the Go memory model gives
	// us around the surrounding atomic accesses (pcmPos.Store below) is
	// enough to make the new pointer visible by the next Read.
	newInfo := e.nextInfo
	e.pcmData = e.nextData
	e.pcmFrames = e.nextFrames
	e.pcmPos.Store(0)
	e.Info = newInfo

	// Clear next
	e.nextData = nil
	e.nextFrames = 0
	e.nextInfo = nil
	e.nextPath = ""
	e.cfMu.Unlock()

	// Fire the transition callback from the audio thread. The UI handler
	// wraps with fyne.Do; that's where Engine.Info gets updated.
	if e.OnTrackTransition != nil && newInfo != nil {
		e.OnTrackTransition(newInfo)
	}
	return true
}

// ── Load internals ───────────────────────────────────────────────────────────

func (e *Engine) loadPCM(path string) (*FileInfo, error) {
	// Prefer streaming decode for formats that report TotalSourceFrames
	// cheaply (FLAC, WAV). The first ~250 ms of audio is decoded +
	// resampled synchronously so playback can start immediately; the rest
	// is filled in by a background goroutine while the user is already
	// listening. Returns errNoStream for MP3 → batch path below.
	if src, err := openPCMStream(path); err == nil {
		return e.loadPCMStreaming(path, src)
	} else if err != errNoStream {
		log.Printf("openPCMStream failed (%v) — falling back to batch decode", err)
	}
	return e.loadPCMBatch(path)
}

// loadPCMStreaming is the fast path: pre-allocates the full output buffer
// from header-known length, decodes a small prelude synchronously so Play()
// is responsive, then continues filling pcmData in the background.
//
// Invariants:
//   - pcmData is allocated to full target size up front. Reader indexes
//     into it freely; the writer only touches frames in [readyBefore, ready).
//   - pcmFramesReady is the atomic barrier between writer-owned and
//     reader-owned regions. pcmReader.Read clamps its serve count to ready.
//   - pcmStopCh signals the background goroutine to exit on Load/Stop.
//     stopPCMStream() closes it (idempotent) and clears the field.
func (e *Engine) loadPCMStreaming(path string, src pcmStreamSource) (*FileInfo, error) {
	defer func() {
		if r := recover(); r != nil {
			src.Close()
			panic(r)
		}
	}()

	srcRate := src.SampleRate()
	srcCh := src.Channels()
	bd := src.BitDepth()
	totalSrcFrames := src.TotalSourceFrames()

	if totalSrcFrames <= 0 {
		src.Close()
		return e.loadPCMBatch(path)
	}

	// Compute total output frames and pre-allocate the full buffer. This
	// matches the eventual size the batch path would produce (modulo a
	// 1-frame rounding difference at the kernel boundary, which we don't
	// even notice in playback).
	totalOutFrames := totalSrcFrames * int64(outputSampleRate) / int64(srcRate)
	e.pcmData = make([]float32, totalOutFrames*int64(outputChannels))
	e.pcmFrames = totalOutFrames
	e.pcmPos.Store(0)
	e.pcmFramesReady.Store(0)

	dur := float64(totalOutFrames) / float64(outputSampleRate)
	ext := strings.ToUpper(strings.TrimPrefix(filepath.Ext(path), "."))

	stat, _ := os.Stat(path)
	var sizeMB float64
	if stat != nil {
		sizeMB = float64(stat.Size()) / 1_048_576
	}

	info := &FileInfo{
		Path:          path,
		Format:        ext,
		SampleRate:    srcRate,
		PCMSampleRate: srcRate,
		BitDepth:      bd,
		Channels:      srcCh,
		Duration:      dur,
		FileSizeMB:    sizeMB,
		QualityLabel:  fmt.Sprintf("%s • %dbit/%dkHz • %dch", ext, bd, srcRate/1000, srcCh),
	}
	if md := ReadMetadata(path, 0); md != nil {
		info.Title = md.Title
		info.Artist = md.Artist
		info.Album = md.Album
		info.PictureData = md.PictureData
		info.PictureMIME = md.PictureMIME
	}

	// Shared state for prelude + background phases: one streamingResampler
	// (its history carries across chunks), one DC blocker (per-channel
	// state must persist for the whole stream), and one soft-limiter (no
	// state, applied per chunk).
	sr := newStreamingResampler(outputChannels, srcRate, outputSampleRate)
	dc := newDCBlocker(outputChannels)

	// Prelude: decode + resample ~250 ms of source frames synchronously.
	// This is enough to fill oto's 200 ms internal buffer before Play()
	// returns, so the user doesn't hear a pop or initial silence.
	preludeSrcFrames := int(srcRate) / 4
	if preludeSrcFrames < 4096 {
		preludeSrcFrames = 4096
	}
	chunkSrcFrames := 8192
	if int(srcRate) > 96000 {
		chunkSrcFrames = 16384 // bigger chunks for HD: same chunk wall time
	}

	if err := e.pcmStreamFill(src, sr, dc, srcCh, preludeSrcFrames, nil); err != nil {
		src.Close()
		return nil, fmt.Errorf("decode prelude: %w", err)
	}

	// If the file was shorter than the prelude, we're already done.
	if e.pcmFramesReady.Load() >= e.pcmFrames {
		src.Close()
		log.Printf("Loaded PCM (streamed, full): %s | %dkHz/%dbit | %dch | %.1fs",
			filepath.Base(path), srcRate/1000, bd, srcCh, dur)
		return info, nil
	}

	// Background fill: keep decoding and writing until EOF or stopCh.
	stopCh := make(chan struct{})
	e.mu.Lock()
	if e.pcmStopCh != nil {
		// Defensive: a previous decode goroutine should have been stopped
		// by stopInternal already. Close-on-stale is harmless.
		select {
		case <-e.pcmStopCh:
		default:
			close(e.pcmStopCh)
		}
	}
	e.pcmStopCh = stopCh
	e.mu.Unlock()

	go func() {
		defer src.Close()
		for {
			select {
			case <-stopCh:
				return
			default:
			}
			err := e.pcmStreamFill(src, sr, dc, srcCh, chunkSrcFrames, stopCh)
			if err == io.EOF {
				return
			}
			if err != nil {
				log.Printf("PCM streaming decode error: %v", err)
				return
			}
			if e.pcmFramesReady.Load() >= e.pcmFrames {
				return
			}
		}
	}()

	log.Printf("Loaded PCM (streaming): %s | %dkHz/%dbit | %dch | %.1fs",
		filepath.Base(path), srcRate/1000, bd, srcCh, dur)
	return info, nil
}

// pcmStreamFill pulls one chunk from src, runs it through the streaming
// resampler + safety chain, and copies the resulting output into pcmData at
// the current ready cursor. Increments pcmFramesReady atomically when done.
// Returns when the chunk has been processed (or io.EOF reached).
//
// Honours stopCh between operations so an in-flight Load() that's been
// superseded by a new Load() can exit promptly.
func (e *Engine) pcmStreamFill(src pcmStreamSource, sr *streamingResampler, dc *dcBlocker, srcCh uint32, chunkSrcFrames int, stopCh <-chan struct{}) error {
	chunk, err := src.NextChunk(chunkSrcFrames)
	isLast := err == io.EOF
	if err != nil && err != io.EOF {
		return err
	}

	// Adapt channel layout BEFORE resample so the streamingResampler is
	// always running at outputChannels (kernel kept simple).
	if len(chunk) > 0 {
		chunk = adaptChannels(chunk, int(srcCh), outputChannels)
	}
	outChunk := sr.Process(chunk, isLast)

	if stopCh != nil {
		select {
		case <-stopCh:
			return io.EOF
		default:
		}
	}

	if len(outChunk) > 0 {
		dc.Process(outChunk, outputChannels)
		softLimit(outChunk)

		ready := e.pcmFramesReady.Load()
		writeStart := ready * int64(outputChannels)
		writeEnd := writeStart + int64(len(outChunk))
		// Defensive clamp against any rounding mismatch between
		// header-derived total and what the resampler actually emits.
		if writeEnd > int64(len(e.pcmData)) {
			writeEnd = int64(len(e.pcmData))
			outChunk = outChunk[:writeEnd-writeStart]
		}
		copy(e.pcmData[writeStart:writeEnd], outChunk)
		e.pcmFramesReady.Add(int64(len(outChunk) / outputChannels))
	}

	if isLast {
		// Snap ready to total so any tail rounding doesn't leave the
		// reader thinking there's a missing frame at the end.
		e.pcmFramesReady.Store(e.pcmFrames)
		return io.EOF
	}
	return nil
}

// loadPCMBatch is the original full-decode-then-resample path. Kept for MP3
// (no cheap length) and as a safety fallback if streaming init fails.
func (e *Engine) loadPCMBatch(path string) (*FileInfo, error) {
	data, sr, ch, bd, err := decodePCM(path)
	if err != nil {
		return nil, fmt.Errorf("decode PCM: %w", err)
	}

	data = adaptChannels(data, int(ch), outputChannels)
	if sr != outputSampleRate {
		data = resamplePCM(data, outputChannels, sr, outputSampleRate)
	}

	dc := newDCBlocker(outputChannels)
	dc.Process(data, outputChannels)
	softLimit(data)

	e.pcmData = data
	e.pcmFrames = int64(len(data)) / int64(outputChannels)
	e.pcmPos.Store(0)
	e.pcmFramesReady.Store(e.pcmFrames) // batch path is "fully ready" from the start

	dur := float64(e.pcmFrames) / float64(outputSampleRate)
	ext := strings.ToUpper(strings.TrimPrefix(filepath.Ext(path), "."))

	stat, _ := os.Stat(path)
	var sizeMB float64
	if stat != nil {
		sizeMB = float64(stat.Size()) / 1_048_576
	}

	info := &FileInfo{
		Path:          path,
		Format:        ext,
		SampleRate:    sr,
		PCMSampleRate: sr,
		BitDepth:      bd,
		Channels:      ch,
		Duration:      dur,
		FileSizeMB:    sizeMB,
		QualityLabel:  fmt.Sprintf("%s • %dbit/%dkHz • %dch", ext, bd, sr/1000, ch),
	}
	if md := ReadMetadata(path, 0); md != nil {
		info.Title = md.Title
		info.Artist = md.Artist
		info.Album = md.Album
		info.PictureData = md.PictureData
		info.PictureMIME = md.PictureMIME
	}
	log.Printf("Loaded PCM (batch): %s | %dkHz/%dbit | %dch | %.1fs",
		filepath.Base(path), sr/1000, bd, ch, dur)
	return info, nil
}

func (e *Engine) loadDSD(path string) (*FileInfo, error) {
	// Decimate directly to the output rate — no second-stage resampling.
	dsd, err := ParseDSD(path, outputSampleRate)
	if err != nil {
		return nil, fmt.Errorf("parse DSD: %w", err)
	}
	e.dsdInfo = dsd

	stat, _ := os.Stat(path)
	var sizeMB float64
	if stat != nil {
		sizeMB = float64(stat.Size()) / 1_048_576
	}

	info := &FileInfo{
		Path:          path,
		Format:        strings.ToUpper(strings.TrimPrefix(filepath.Ext(path), ".")),
		SampleRate:    dsd.DSDSampleRate,
		PCMSampleRate: dsd.PCMSampleRate,
		BitDepth:      1,
		Channels:      dsd.Channels,
		Duration:      dsd.DurationSeconds(),
		BitrateKbps:   int(dsd.DSDSampleRate * dsd.Channels / 1000),
		FileSizeMB:    sizeMB,
		IsDSD:         true,
		DSDLabel:      dsd.Label,
		QualityLabel:  fmt.Sprintf("%s • %.1f MHz • %dch", dsd.Label, float64(dsd.DSDSampleRate)/1e6, dsd.Channels),
	}
	if md := ReadMetadata(path, dsd.MetadataOffset); md != nil {
		info.Title = md.Title
		info.Artist = md.Artist
		info.Album = md.Album
		info.PictureData = md.PictureData
		info.PictureMIME = md.PictureMIME
	}
	log.Printf("Loaded DSD: %s | %s | %dch | %.1fs",
		filepath.Base(path), dsd.Label, dsd.Channels, dsd.DurationSeconds())
	return info, nil
}

// ── Playback internals ───────────────────────────────────────────────────────

// ensureOtoCtxOnce creates the oto context the first time it's called.
// Subsequent calls are no-ops. oto v3 only supports one context per process.
func (e *Engine) ensureOtoCtxOnce() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.otoCtx != nil {
		return nil
	}
	op := &oto.NewContextOptions{
		SampleRate:   int(outputSampleRate),
		ChannelCount: outputChannels,
		Format:       oto.FormatFloat32LE,
		BufferSize:   200 * time.Millisecond,
	}
	ctx, ready, err := oto.NewContext(op)
	if err != nil {
		return fmt.Errorf("oto context: %w", err)
	}
	<-ready
	e.otoCtx = ctx
	return nil
}

// openPlayer tries to open a WASAPI exclusive-mode device first. If that
// fails (DAC doesn't support the format, or another app holds it), it falls
// back to oto shared mode. The caller must NOT hold e.mu.
func (e *Engine) openPlayer(r io.Reader) (audioPlayer, error) {
	if e.exclusiveDisabled.Load() {
		// User-opted shared mode: skip the malgo attempt entirely so we
		// don't even briefly grab the device. Other apps (YouTube,
		// Discord, system notifications) keep playing alongside.
		if err := e.ensureOtoCtxOnce(); err != nil {
			return nil, err
		}
		log.Printf("audio: WASAPI shared mode (exclusive disabled by user)")
		return e.otoCtx.NewPlayer(r), nil
	}
	if p, err := newExclusivePlayer(r); err == nil {
		log.Printf("audio: WASAPI exclusive mode active")
		return p, nil
	}
	// Exclusive failed — fall back to oto shared mode.
	if err := e.ensureOtoCtxOnce(); err != nil {
		return nil, err
	}
	log.Printf("audio: WASAPI shared mode (exclusive unavailable)")
	return e.otoCtx.NewPlayer(r), nil
}

func (e *Engine) playPCM() error {
	stopCh := make(chan struct{})
	e.mu.Lock()
	e.stopCh = stopCh
	e.mu.Unlock()
	e.finishedSignaled.Store(false)

	reader := &pcmReader{engine: e}
	p, err := e.openPlayer(reader)
	if err != nil {
		return fmt.Errorf("open player: %w", err)
	}
	e.mu.Lock()
	e.player = p
	e.player.Play()
	e.mu.Unlock()

	e.setState(StatePlaying)
	go e.positionTimer(stopCh)
	go e.watchPlayerDone(stopCh)
	return nil
}

func (e *Engine) playDSD() error {
	dsd := e.dsdInfo

	// Ring buffer holds 20 s of OUTPUT-rate stereo samples.
	ringCap := int(outputSampleRate) * 20 * outputChannels
	e.ring = newRingBuffer(ringCap)
	e.dsdEOF.Store(false)
	e.dsdReady = make(chan struct{})

	stopCh := make(chan struct{})
	done := make(chan struct{})
	e.mu.Lock()
	e.stopCh = stopCh
	e.dsdDone = done
	e.mu.Unlock()
	e.finishedSignaled.Store(false)

	go func() {
		defer close(done)
		e.dsdProducer(dsd, stopCh)
	}()

	select {
	case <-e.dsdReady:
	case <-time.After(8 * time.Second):
		log.Println("DSD prebuffer timeout — starting with available data")
	case <-stopCh:
		return nil
	}

	reader := &dsdStreamReader{engine: e}
	p, err := e.openPlayer(reader)
	if err != nil {
		return fmt.Errorf("open player: %w", err)
	}
	e.mu.Lock()
	e.player = p
	e.player.Play()
	e.mu.Unlock()

	e.setState(StatePlaying)
	go e.positionTimer(stopCh)
	go e.watchPlayerDone(stopCh)
	return nil
}

func (e *Engine) resume() error {
	e.mu.Lock()
	if e.player != nil {
		e.player.Play()
	}
	stopCh := e.stopCh
	e.mu.Unlock()
	e.setState(StatePlaying)
	if stopCh != nil {
		go e.positionTimer(stopCh)
	}
	return nil
}

func (e *Engine) stopInternal() {
	e.mu.Lock()
	stopCh := e.stopCh
	e.stopCh = nil
	player := e.player
	e.player = nil
	dsdDone := e.dsdDone
	e.dsdDone = nil
	pcmStopCh := e.pcmStopCh
	e.pcmStopCh = nil
	e.mu.Unlock()

	// Cancel any in-flight streaming PCM decoder. The goroutine polls
	// stopCh between chunks so this exits within a few ms in practice.
	if pcmStopCh != nil {
		select {
		case <-pcmStopCh:
		default:
			close(pcmStopCh)
		}
	}

	if stopCh != nil {
		select {
		case <-stopCh:
		default:
			close(stopCh)
		}
	}
	// Wait for the DSD producer (if any) to finish before we hand control
	// back. Otherwise a fast restart (e.g. seek) starts a new producer while
	// the old one is still in its tail cleanup, where it clobbers the freshly
	// minted dsdReady/dsdEOF/ring with stale "I'm done" writes. The bound
	// is generous; the producer's stopCh check sits inside the ring-write
	// loop and exits within a couple of ms in practice.
	if dsdDone != nil {
		select {
		case <-dsdDone:
		case <-time.After(500 * time.Millisecond):
			log.Println("DSD producer slow to exit — proceeding anyway")
		}
	}
	if player != nil {
		player.Close()
	}

	// Drop any preloaded / outgoing crossfade state. Once the device is
	// closed the audio thread is gone, so there's no reader concurrently
	// reading these — straight assignment is safe. Without this, a Stop
	// followed by a fresh Load would leak the heavy decoded slices until
	// the next crossfade replaced them, and an "outgoing" track still in
	// the slot would also linger past the stop.
	e.cfMu.Lock()
	e.nextData = nil
	e.nextFrames = 0
	e.nextInfo = nil
	e.nextPath = ""
	e.outgoingData = nil
	e.outgoingPos = 0
	e.outgoingFrames = 0
	e.outgoingRemaining = 0
	e.crossfadeFramesTotal = 0
	e.cfMu.Unlock()

	e.setState(StateStopped)
}

// ── DSD producer ─────────────────────────────────────────────────────────────

const dsdPrebufferSec = 2.0

func (e *Engine) dsdProducer(info *DSDInfo, stopCh <-chan struct{}) {
	dec := NewDSDDecoder(info)
	if e.dsdSeekBytes > 0 {
		dec.SeekTo(e.dsdSeekBytes)
	}
	prebufTarget := int(float64(outputSampleRate)*dsdPrebufferSec) * outputChannels
	produced := 0
	signaled := false

	// Per-stream DC blocker (its filter state is per-channel so it must live
	// for the entire playback, not just one chunk).
	dcb := newDCBlocker(outputChannels)

	// Sanity check: with our output rate the decimation should be exact.
	if info.PCMSampleRate != outputSampleRate {
		log.Printf("DSD: PCM rate %d != output %d — clean integer decimation not possible for this file",
			info.PCMSampleRate, outputSampleRate)
	}

	err := dec.IterChunks(func(chunk []float32, _ int) bool {
		select {
		case <-stopCh:
			return false
		default:
		}

		// Channel adapt → safety chain → ring. Volume is applied in
		// dsdStreamReader.Read (consumer side) so that slider changes take
		// effect immediately without waiting for the ring to drain.
		chunk = adaptChannels(chunk, int(info.Channels), outputChannels)
		if len(chunk) == 0 {
			return true
		}

		dcb.Process(chunk, outputChannels)
		softLimit(chunk)
		e.Vis.Push(chunk)

		written := 0
		for written < len(chunk) {
			select {
			case <-stopCh:
				return false
			default:
			}
			n := e.ring.write(chunk[written:])
			if n == 0 {
				time.Sleep(2 * time.Millisecond)
			} else {
				written += n
			}
		}

		produced += len(chunk)
		if !signaled && produced >= prebufTarget {
			signaled = true
			close(e.dsdReady)
		}
		return true
	})

	if !signaled {
		close(e.dsdReady)
	}
	e.dsdEOF.Store(true)
	if err != nil {
		log.Printf("DSD producer error: %v", err)
		e.OnError(fmt.Sprintf("DSD error: %v", err))
	}
}

// ── oto io.Reader implementations ───────────────────────────────────────────

// pcmReader serves PCM frames to oto as interleaved float32 LE bytes.
type pcmReader struct {
	engine *Engine
	visBuf []float32 // reused per call to feed the visualizer without alloc
}

func (r *pcmReader) Read(p []byte) (int, error) {
	e := r.engine
	ch := outputChannels
	bytesPerFrame := ch * 4
	frames := int64(len(p) / bytesPerFrame)
	if frames == 0 {
		return 0, nil
	}

	pos := e.pcmPos.Load()
	remaining := e.pcmFrames - pos

	// ── Crossfade trigger ──────────────────────────────────────────────
	// If we're entering the fade window and have a preload waiting, swap
	// it in. tryStartCrossfade resets pcmPos to 0, so we re-read.
	if remaining > 0 {
		fadeMs := e.crossfadeMs.Load()
		if fadeMs > 0 {
			fadeFrames := int64(fadeMs) * int64(outputSampleRate) / 1000
			if remaining <= fadeFrames {
				// Peek under cfMu to avoid the call overhead when nothing's preloaded.
				e.cfMu.Lock()
				hasNext := e.nextData != nil && e.outgoingData == nil
				e.cfMu.Unlock()
				if hasNext && e.tryStartCrossfade(remaining) {
					pos = e.pcmPos.Load()
					remaining = e.pcmFrames - pos
				}
			}
		}
	}

	// Snapshot outgoing state. After this section we don't take cfMu
	// again until we're updating the consumed counters at the bottom.
	e.cfMu.Lock()
	outActive := e.outgoingData != nil && e.outgoingRemaining > 0
	outData := e.outgoingData
	outPos := e.outgoingPos
	outFrames := e.outgoingFrames
	outRem := e.outgoingRemaining
	fadeTotal := e.crossfadeFramesTotal
	e.cfMu.Unlock()

	if remaining <= 0 && !outActive {
		// True EOF: the new track has also been fully consumed (or we
		// never had a preload and the current track is done).
		return 0, io.EOF
	}

	// Determine how many frames we can produce this call. Limited by
	// whichever of (current remaining, outgoing remaining, outgoing
	// physical frames left, requested) runs out first.
	serve := frames
	if remaining > 0 && serve > remaining {
		serve = remaining
	}
	if outActive {
		if serve > outRem {
			serve = outRem
		}
		if outPos+serve > outFrames {
			serve = outFrames - outPos
		}
	}
	if serve <= 0 {
		return 0, io.EOF
	}

	// ── Streaming-decode barrier ───────────────────────────────────────
	// pcmFramesReady is the high-water mark of decoded+resampled output.
	// If we'd serve beyond it, clamp to ready. If we're already AT ready
	// and the decoder hasn't EOF'd, return a short silence so oto stays
	// fed without us reading uninitialised buffer bytes.
	if remaining > 0 {
		ready := e.pcmFramesReady.Load()
		readyRemaining := ready - pos
		if readyRemaining <= 0 {
			// Decoder hasn't caught up yet (typically only happens after
			// a seek into a far-future position). Brief silence pad.
			pad := bytesPerFrame * 16
			if pad > len(p) {
				pad = len(p)
			}
			return fillSilence(p[:pad]), nil
		}
		if serve > readyRemaining {
			serve = readyRemaining
		}
	}

	// Reuse a per-reader scratch buffer for the visualizer feed so we
	// don't allocate every callback.
	nSamples := int(serve) * ch
	if cap(r.visBuf) < nSamples {
		r.visBuf = make([]float32, nSamples)
	}
	r.visBuf = r.visBuf[:nSamples]

	vol := e.Volume
	out := p[:int(serve)*bytesPerFrame]

	if outActive {
		// ── Mix mode: current * gainCur + outgoing * gainOut, ramped ──
		//
		// gainOut starts at outRem/fadeTotal (close to 1 when fade just
		// began) and ramps down to 0 as outRem is consumed.
		// gainCur = 1 - gainOut.
		var curBase []float32
		if remaining > 0 {
			curBase = e.pcmData[pos*int64(ch) : (pos+serve)*int64(ch)]
		}
		outBase := outData[outPos*int64(ch) : (outPos+serve)*int64(ch)]
		invTotal := float32(0)
		if fadeTotal > 0 {
			invTotal = 1.0 / float32(fadeTotal)
		}

		for i := int64(0); i < serve; i++ {
			// outRem - i frames will remain after consuming this one.
			gainOut := float32(outRem-i) * invTotal
			if gainOut < 0 {
				gainOut = 0
			}
			if gainOut > 1 {
				gainOut = 1
			}
			gainCur := 1 - gainOut

			for c := 0; c < ch; c++ {
				idx := i*int64(ch) + int64(c)
				var s float32
				if curBase != nil {
					s = curBase[idx]*gainCur + outBase[idx]*gainOut
				} else {
					s = outBase[idx] * gainOut
				}
				s *= vol
				r.visBuf[idx] = s
				b := math.Float32bits(s)
				j := idx * 4
				out[j] = byte(b)
				out[j+1] = byte(b >> 8)
				out[j+2] = byte(b >> 16)
				out[j+3] = byte(b >> 24)
			}
		}

		// Update outgoing counters under the lock. If we just drained it,
		// release the slice so the GC can reclaim it.
		e.cfMu.Lock()
		e.outgoingPos += serve
		e.outgoingRemaining -= serve
		if e.outgoingRemaining <= 0 || e.outgoingPos >= e.outgoingFrames {
			e.outgoingData = nil
			e.outgoingFrames = 0
			e.outgoingPos = 0
			e.outgoingRemaining = 0
		}
		e.cfMu.Unlock()
	} else {
		// ── Single-track fast path (zero overhead vs. pre-crossfade) ──
		src := e.pcmData[pos*int64(ch) : (pos+serve)*int64(ch)]
		for i, v := range src {
			s := v * vol
			r.visBuf[i] = s
			b := math.Float32bits(s)
			j := i * 4
			out[j] = byte(b)
			out[j+1] = byte(b >> 8)
			out[j+2] = byte(b >> 16)
			out[j+3] = byte(b >> 24)
		}
	}

	e.Vis.Push(r.visBuf)
	if remaining > 0 {
		e.pcmPos.Store(pos + serve)
	}
	return len(out), nil
}

// dsdStreamReader serves DSD-decoded PCM from the ring buffer to oto.
type dsdStreamReader struct {
	engine *Engine
}

func (r *dsdStreamReader) Read(p []byte) (int, error) {
	e := r.engine
	ch := outputChannels
	maxFrames := len(p) / (ch * 4)
	if maxFrames == 0 {
		return 0, nil
	}
	maxSamples := maxFrames * ch

	avail := e.ring.available()
	if avail == 0 {
		if e.dsdEOF.Load() {
			// EOF — let oto drain; watchPlayerDone fires OnFinished exactly once.
			return 0, io.EOF
		}
		// Underrun: return a tiny silence pad so oto doesn't spin.
		pad := ch * 4 * 16
		if pad > len(p) {
			pad = len(p)
		}
		return fillSilence(p[:pad]), nil
	}

	if avail > maxSamples {
		avail = maxSamples
	}
	floats := make([]float32, avail)
	n := e.ring.readSamples(floats, avail)
	vol := e.Volume

	written := 0
	for i := 0; i < n; i++ {
		b := math.Float32bits(floats[i] * vol)
		p[written] = byte(b)
		p[written+1] = byte(b >> 8)
		p[written+2] = byte(b >> 16)
		p[written+3] = byte(b >> 24)
		written += 4
	}
	return written, nil
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func (e *Engine) positionTimer(stopCh <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			if State(e.state.Load()) == StatePlaying && e.Info != nil {
				e.OnPositionChange(e.Position(), e.Info.Duration)
			}
		}
	}
}

// watchPlayerDone polls the oto player for the moment its buffer is fully
// drained after the source returned EOF. Used for both PCM and DSD playback —
// the unified path means OnFinished fires exactly once, via the same code,
// regardless of source format.
func (e *Engine) watchPlayerDone(stopCh <-chan struct{}) {
	for {
		select {
		case <-stopCh:
			return
		case <-time.After(100 * time.Millisecond):
			e.mu.Lock()
			p := e.player
			e.mu.Unlock()
			if p != nil && !p.IsPlaying() && State(e.state.Load()) == StatePlaying {
				if e.finishedSignaled.CompareAndSwap(false, true) {
					e.stopInternal()
					e.OnFinished()
				}
				return
			}
		}
	}
}

func (e *Engine) setState(s State) {
	e.state.Store(int32(s))
	e.OnStateChange(s)
}

func fillSilence(p []byte) int {
	for i := range p {
		p[i] = 0
	}
	return len(p)
}
