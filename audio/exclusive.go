package audio

// exclusive.go — WASAPI exclusive-mode audio output via miniaudio (malgo).
//
// In exclusive mode the app talks directly to the DAC at the file's native
// rate with no Windows mixer in the path: bit-perfect, zero resampling.
// The hardware buffer is one device period (~2–10 ms) vs. oto's ~200 ms
// shared-mode buffer, which also improves lyric-sync accuracy.
//
// If the DAC doesn't support 176 400 Hz exclusive (or another app holds it),
// InitDevice fails and Engine.openPlayer() falls back to oto shared mode.
// The user never sees a difference in UX — quality degrades gracefully.

import (
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
)

// OutputDevice is a WASAPI playback endpoint reported to the UI. ID is the
// hex-encoded malgo device id (opaque token); pass it back to SetOutputDevice.
type OutputDevice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
}

// ListPlaybackDevices enumerates the active playback endpoints (DACs, speakers,
// HDMI…). Hot-plugged devices appear on the next call since malgo re-queries
// the OS each time.
func ListPlaybackDevices() ([]OutputDevice, error) {
	mctx, err := ensureMalgoCtx()
	if err != nil {
		return nil, err
	}
	infos, err := mctx.Devices(malgo.Playback)
	if err != nil {
		return nil, err
	}
	out := make([]OutputDevice, 0, len(infos))
	for i := range infos {
		d := &infos[i]
		out = append(out, OutputDevice{
			ID:        hex.EncodeToString(d.ID[:]),
			Name:      d.Name(),
			IsDefault: d.IsDefault != 0,
		})
	}
	return out, nil
}

// parseDeviceID decodes a hex token from ListPlaybackDevices back into a
// malgo.DeviceID. Returns (id, true) only on an exact-length match.
func parseDeviceID(deviceHex string) (malgo.DeviceID, bool) {
	var id malgo.DeviceID
	if deviceHex == "" {
		return id, false
	}
	raw, err := hex.DecodeString(deviceHex)
	if err != nil || len(raw) != len(id) {
		return id, false
	}
	copy(id[:], raw)
	return id, true
}

// ── Singleton malgo context ──────────────────────────────────────────────────
// malgo.InitContext scans audio backends (expensive). One context per process
// is enough — it can open multiple devices sequentially.

var (
	malgoOnce sync.Once
	malgoCtx  *malgo.AllocatedContext
	malgoErr  error
)

func ensureMalgoCtx() (*malgo.AllocatedContext, error) {
	malgoOnce.Do(func() {
		ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(msg string) {
			log.Printf("malgo: %s", msg)
		})
		if err != nil {
			malgoErr = fmt.Errorf("malgo context: %w", err)
			return
		}
		malgoCtx = ctx
	})
	return malgoCtx, malgoErr
}

// ── malgoPlayer ──────────────────────────────────────────────────────────────

// malgoPlayer drives WASAPI exclusive-mode output. It implements audioPlayer
// so the engine can treat it identically to an oto player.
type malgoPlayer struct {
	device   *malgo.Device
	reader   io.Reader
	doneCh   chan struct{} // closed ~50ms after the reader signals io.EOF
	doneOnce sync.Once
	paused   atomic.Bool
}

// newExclusivePlayer opens a WASAPI exclusive device at 176 400 Hz / stereo /
// float32. When deviceHex is a valid token from ListPlaybackDevices the device
// is targeted explicitly; otherwise the system default playback device is used.
// Returns an error if the DAC rejects the format or the device is already held
// in exclusive mode by another application.
func newExclusivePlayer(r io.Reader, deviceHex string) (*malgoPlayer, error) {
	mctx, err := ensureMalgoCtx()
	if err != nil {
		return nil, err
	}

	p := &malgoPlayer{
		reader: r,
		doneCh: make(chan struct{}),
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.SampleRate = outputSampleRate
	cfg.Playback.Format = malgo.FormatF32
	cfg.Playback.Channels = uint32(outputChannels)
	cfg.Playback.ShareMode = malgo.Exclusive
	cfg.Wasapi.NoAutoConvertSRC = 1
	// Target a specific endpoint when one was chosen in the UI. devID must
	// outlive the InitDevice call below — keep it in the function scope (the
	// unsafe.Pointer is GC-tracked, but a function-scoped var is unambiguous).
	var devID malgo.DeviceID
	if parsed, ok := parseDeviceID(deviceHex); ok {
		devID = parsed
		cfg.Playback.DeviceID = devID.Pointer()
	}

	device, err := malgo.InitDevice(mctx.Context, cfg, malgo.DeviceCallbacks{
		Data: p.dataCallback,
	})
	if err != nil {
		return nil, fmt.Errorf("exclusive device: %w", err)
	}

	p.device = device
	return p, nil
}

// dataCallback is called by miniaudio's audio thread whenever the device
// needs more samples. It must be fast and non-blocking.
//
// It delegates to the same pcmReader / dsdStreamReader io.Reader used by the
// oto path — those readers are already lock-free (atomic + ring-buffer reads)
// and safe to call from a real-time audio thread.
func (p *malgoPlayer) dataCallback(output, _ []byte, frameCount uint32) {
	needed := int(frameCount) * outputChannels * 4 // float32 = 4 bytes/sample
	if needed > len(output) {
		needed = len(output)
	}

	n, err := p.reader.Read(output[:needed])

	// Zero-fill any samples the reader didn't provide (underrun / tail).
	for i := n; i < needed; i++ {
		output[i] = 0
	}

	if err == io.EOF {
		// Give the hardware buffer one final period to play out before we
		// signal that the track is done. 50 ms >> worst-case period size.
		p.doneOnce.Do(func() {
			go func() {
				time.Sleep(50 * time.Millisecond)
				close(p.doneCh)
			}()
		})
	}
}

// ── audioPlayer interface ─────────────────────────────────────────────────────

func (p *malgoPlayer) Play() {
	p.paused.Store(false)
	if err := p.device.Start(); err != nil {
		log.Printf("malgo: device start failed: %v", err)
	}
}

func (p *malgoPlayer) Pause() {
	p.paused.Store(true)
	_ = p.device.Stop()
}

func (p *malgoPlayer) IsPlaying() bool {
	if p.paused.Load() {
		return false
	}
	select {
	case <-p.doneCh:
		return false
	default:
		return p.device.IsStarted()
	}
}

// BufferedSize returns 0: in exclusive mode the DAC latency is one hardware
// period (~2–10 ms), negligible for position tracking. Returning 0 gives the
// engine a more accurate playback position than oto's ~200 ms shared buffer.
func (p *malgoPlayer) BufferedSize() int { return 0 }

func (p *malgoPlayer) Close() error {
	_ = p.device.Stop()
	p.device.Uninit()
	return nil
}
