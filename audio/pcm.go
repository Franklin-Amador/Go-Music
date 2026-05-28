package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/hajimehoshi/go-mp3"
	"github.com/mewkiz/flac"
)

// decodePCM decodes a PCM audio file into interleaved float32 samples.
// Returns (samples, sampleRate, channels, bitDepth, error).
// Supported formats: FLAC, WAV, MP3.
//
// Used by the Preload (crossfade) path, where decode runs in a background
// goroutine and there's no benefit to streaming. The foreground Load path
// uses openPCMStream for chunked decode + streaming resample so Play()
// can start within ~250 ms.
func decodePCM(path string) ([]float32, uint32, uint32, int, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".flac":
		return decodeFLAC(path)
	case ".wav":
		return decodeWAV(path)
	case ".mp3":
		return decodeMP3(path)
	default:
		return nil, 0, 0, 0, fmt.Errorf("unsupported format: %s", ext)
	}
}

// pcmStreamSource is a chunked PCM decoder. The streaming Load path pulls
// chunks of ~8 K source frames at a time, feeds them through
// streamingResampler, and writes the resampled output into the pre-allocated
// pcmData buffer as it's produced.
//
// Implementations must report TotalSourceFrames at construction so the
// engine can pre-allocate the output buffer in one go (no slice growth
// during playback). Formats that can't report total frames cheaply (some
// MP3s without Xing/LAME headers) should NOT implement this interface —
// they fall through to the batch decodePCM path.
type pcmStreamSource interface {
	SampleRate() uint32
	Channels() uint32
	BitDepth() int
	TotalSourceFrames() int64

	// NextChunk returns up to maxFrames worth of decoded source samples
	// (interleaved). Returns (chunk, io.EOF) when the source is exhausted;
	// callers should still process any returned chunk before bailing.
	NextChunk(maxFrames int) ([]float32, error)

	Close() error
}

// openPCMStream returns a streaming decoder for files whose total length is
// cheap to determine. FLAC carries it in STREAMINFO; WAV in the data-chunk
// size. MP3 returns (nil, ErrNoStream) — callers should fall back to the
// batch decoder.
func openPCMStream(path string) (pcmStreamSource, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".flac":
		return openFLACStream(path)
	case ".wav":
		return openWAVStream(path)
	case ".mp3":
		return nil, errNoStream
	}
	return nil, fmt.Errorf("openPCMStream: unsupported format: %s", ext)
}

// errNoStream signals that a format doesn't support cheap streaming — the
// caller should fall back to the batch decodePCM path.
var errNoStream = fmt.Errorf("pcm: streaming not supported for this format")

// ── FLAC streaming ───────────────────────────────────────────────────────────

type flacStream struct {
	stream *flac.Stream
	sr     uint32
	ch     uint32
	bd     int
	total  int64
	scale  float32

	// pending holds any samples decoded from the last frame that didn't
	// fit in the previous NextChunk request. NextChunk returns these
	// before pulling another FLAC frame.
	pending []float32
}

func openFLACStream(path string) (pcmStreamSource, error) {
	stream, err := flac.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open FLAC: %w", err)
	}
	info := stream.Info
	bd := int(info.BitsPerSample)
	return &flacStream{
		stream: stream,
		sr:     info.SampleRate,
		ch:     uint32(info.NChannels),
		bd:     bd,
		total:  int64(info.NSamples),
		scale:  float32(1) / float32(int32(1)<<(bd-1)),
	}, nil
}

func (s *flacStream) SampleRate() uint32       { return s.sr }
func (s *flacStream) Channels() uint32         { return s.ch }
func (s *flacStream) BitDepth() int            { return s.bd }
func (s *flacStream) TotalSourceFrames() int64 { return s.total }

func (s *flacStream) NextChunk(maxFrames int) ([]float32, error) {
	maxSamples := maxFrames * int(s.ch)
	out := make([]float32, 0, maxSamples)

	// Drain pending first.
	if len(s.pending) > 0 {
		take := len(s.pending)
		if take > maxSamples {
			take = maxSamples
		}
		out = append(out, s.pending[:take]...)
		s.pending = s.pending[take:]
		if len(out) >= maxSamples {
			return out, nil
		}
	}

	for len(out) < maxSamples {
		frame, err := s.stream.ParseNext()
		if err == io.EOF {
			if len(out) > 0 {
				return out, io.EOF
			}
			return nil, io.EOF
		}
		if err != nil {
			return out, fmt.Errorf("decode FLAC frame: %w", err)
		}

		nFrames := len(frame.Subframes[0].Samples)
		needed := (maxSamples - len(out)) / int(s.ch)
		toEmit := nFrames
		if toEmit > needed {
			toEmit = needed
		}
		for i := 0; i < toEmit; i++ {
			for c := 0; c < int(s.ch); c++ {
				out = append(out, float32(frame.Subframes[c].Samples[i])*s.scale)
			}
		}
		// Stash whatever didn't fit into pending for next call.
		if toEmit < nFrames {
			s.pending = make([]float32, 0, (nFrames-toEmit)*int(s.ch))
			for i := toEmit; i < nFrames; i++ {
				for c := 0; c < int(s.ch); c++ {
					s.pending = append(s.pending, float32(frame.Subframes[c].Samples[i])*s.scale)
				}
			}
			break
		}
	}
	return out, nil
}

func (s *flacStream) Close() error {
	return s.stream.Close()
}

// ── WAV streaming ────────────────────────────────────────────────────────────

type wavStream struct {
	f         *os.File
	sr        uint32
	ch        uint32
	bd        uint16
	audioFmt  uint16
	dataLeft  int64 // bytes remaining in data chunk
	frameSize int   // bytes per source frame (ch × bd/8)
	total     int64
}

func openWAVStream(path string) (pcmStreamSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	riff := make([]byte, 4)
	if _, err := io.ReadFull(f, riff); err != nil {
		f.Close()
		return nil, err
	}
	if string(riff) != "RIFF" {
		f.Close()
		return nil, fmt.Errorf("WAV: not a RIFF file")
	}
	f.Seek(4, io.SeekCurrent)
	wave := make([]byte, 4)
	if _, err := io.ReadFull(f, wave); err != nil {
		f.Close()
		return nil, err
	}
	if string(wave) != "WAVE" {
		f.Close()
		return nil, fmt.Errorf("WAV: not a WAVE file")
	}

	var (
		sr       uint32
		ch       uint16
		bd       uint16
		audioFmt uint16
	)
	foundFmt := false

	for {
		chunkID := make([]byte, 4)
		if _, err := io.ReadFull(f, chunkID); err != nil {
			f.Close()
			return nil, fmt.Errorf("WAV: chunk header: %w", err)
		}
		var chunkSize uint32
		if err := binary.Read(f, binary.LittleEndian, &chunkSize); err != nil {
			f.Close()
			return nil, err
		}
		pos, _ := f.Seek(0, io.SeekCurrent)

		switch string(chunkID) {
		case "fmt ":
			binary.Read(f, binary.LittleEndian, &audioFmt)
			binary.Read(f, binary.LittleEndian, &ch)
			binary.Read(f, binary.LittleEndian, &sr)
			f.Seek(6, io.SeekCurrent)
			binary.Read(f, binary.LittleEndian, &bd)
			foundFmt = true
			f.Seek(pos+int64(chunkSize), io.SeekStart)

		case "data":
			if !foundFmt {
				f.Close()
				return nil, fmt.Errorf("WAV: data chunk before fmt")
			}
			if audioFmt != 1 && audioFmt != 3 {
				f.Close()
				return nil, fmt.Errorf("WAV: unsupported format %d (PCM=1, float=3)", audioFmt)
			}
			frameSize := int(ch) * int(bd) / 8
			if frameSize == 0 {
				f.Close()
				return nil, fmt.Errorf("WAV: zero frame size")
			}
			total := int64(chunkSize) / int64(frameSize)
			return &wavStream{
				f:         f,
				sr:        sr,
				ch:        uint32(ch),
				bd:        bd,
				audioFmt:  audioFmt,
				dataLeft:  int64(chunkSize),
				frameSize: frameSize,
				total:     total,
			}, nil

		default:
			f.Seek(pos+int64(chunkSize), io.SeekStart)
		}
	}
}

func (s *wavStream) SampleRate() uint32       { return s.sr }
func (s *wavStream) Channels() uint32         { return s.ch }
func (s *wavStream) BitDepth() int            { return int(s.bd) }
func (s *wavStream) TotalSourceFrames() int64 { return s.total }

func (s *wavStream) NextChunk(maxFrames int) ([]float32, error) {
	if s.dataLeft <= 0 {
		return nil, io.EOF
	}
	wantBytes := int64(maxFrames) * int64(s.frameSize)
	if wantBytes > s.dataLeft {
		wantBytes = s.dataLeft
	}
	buf := make([]byte, wantBytes)
	n, err := io.ReadFull(s.f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("WAV: read data: %w", err)
	}
	buf = buf[:n]
	s.dataLeft -= int64(n)

	samples := wavBytesToFloat32(buf, s.audioFmt, s.bd)
	if s.dataLeft <= 0 {
		return samples, io.EOF
	}
	return samples, nil
}

func (s *wavStream) Close() error {
	return s.f.Close()
}

// ── MP3 ──────────────────────────────────────────────────────────────────────

// decodeMP3 decodes an MP3 file into 16-bit PCM via go-mp3, then converts to
// float32 in [-1, +1]. Output is always stereo at the file's native sample rate.
func decodeMP3(path string) ([]float32, uint32, uint32, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	defer f.Close()

	dec, err := mp3.NewDecoder(f)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("decode MP3: %w", err)
	}

	sr := uint32(dec.SampleRate())
	// go-mp3 always outputs interleaved 16-bit stereo little-endian.
	const ch uint32 = 2
	const bytesPerFrame = 4 // 2 ch × 2 bytes

	// Pre-size: dec.Length() returns total bytes of decoded output (or -1).
	var floats []float32
	if total := dec.Length(); total > 0 {
		floats = make([]float32, 0, total/2) // total bytes / 2 = total int16 samples
	}

	buf := make([]byte, 8192*bytesPerFrame)
	for {
		n, err := dec.Read(buf)
		for i := 0; i+1 < n; i += 2 {
			s := int16(binary.LittleEndian.Uint16(buf[i:]))
			floats = append(floats, float32(s)/32768.0)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, 0, 0, fmt.Errorf("MP3 read: %w", err)
		}
	}
	return floats, sr, ch, 16, nil
}

// ── FLAC ─────────────────────────────────────────────────────────────────────

func decodeFLAC(path string) ([]float32, uint32, uint32, int, error) {
	stream, err := flac.Open(path)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("open FLAC: %w", err)
	}
	defer stream.Close()

	info := stream.Info
	sr := info.SampleRate
	ch := uint32(info.NChannels)
	bd := int(info.BitsPerSample)
	scale := float32(1) / float32(int32(1)<<(bd-1))

	// Preallocate the exact output size from FLAC's stream info. Without this
	// `append` doubles the slice capacity on growth, so the transient peak
	// memory during decode is up to 2× the final size — a 4-min 96 kHz / 24-bit
	// stereo file lands at ~184 MB but spikes to ~370 MB just during decode.
	// info.NSamples is the per-channel sample count; total samples = ×channels.
	totalSamples := int64(info.NSamples) * int64(ch)
	var samples []float32
	if totalSamples > 0 {
		samples = make([]float32, 0, totalSamples)
	}

	for {
		frame, err := stream.ParseNext()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, 0, 0, fmt.Errorf("decode FLAC frame: %w", err)
		}

		// frame.Subframes[ch].Samples is []int32
		nFrames := len(frame.Subframes[0].Samples)
		for i := 0; i < nFrames; i++ {
			for c := 0; c < int(ch); c++ {
				samples = append(samples, float32(frame.Subframes[c].Samples[i])*scale)
			}
		}
	}
	return samples, sr, ch, bd, nil
}

// ── WAV ──────────────────────────────────────────────────────────────────────

func decodeWAV(path string) ([]float32, uint32, uint32, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	defer f.Close()

	// RIFF header
	riff := make([]byte, 4)
	if _, err := io.ReadFull(f, riff); err != nil {
		return nil, 0, 0, 0, err
	}
	if string(riff) != "RIFF" {
		return nil, 0, 0, 0, fmt.Errorf("WAV: not a RIFF file")
	}
	f.Seek(4, io.SeekCurrent) // skip file size
	wave := make([]byte, 4)
	if _, err := io.ReadFull(f, wave); err != nil {
		return nil, 0, 0, 0, err
	}
	if string(wave) != "WAVE" {
		return nil, 0, 0, 0, fmt.Errorf("WAV: not a WAVE file")
	}

	var (
		sr        uint32
		ch        uint16
		bd        uint16
		audioFmt  uint16
		dataSize  uint32
	)
	foundFmt := false

	for {
		chunkID := make([]byte, 4)
		if _, err := io.ReadFull(f, chunkID); err != nil {
			break
		}
		var chunkSize uint32
		if err := binary.Read(f, binary.LittleEndian, &chunkSize); err != nil {
			break
		}
		pos, _ := f.Seek(0, io.SeekCurrent)

		switch string(chunkID) {
		case "fmt ":
			binary.Read(f, binary.LittleEndian, &audioFmt)
			binary.Read(f, binary.LittleEndian, &ch)
			binary.Read(f, binary.LittleEndian, &sr)
			f.Seek(6, io.SeekCurrent) // byteRate + blockAlign
			binary.Read(f, binary.LittleEndian, &bd)
			foundFmt = true

		case "data":
			if !foundFmt {
				return nil, 0, 0, 0, fmt.Errorf("WAV: data chunk before fmt")
			}
			dataSize = chunkSize
			if audioFmt != 1 && audioFmt != 3 {
				return nil, 0, 0, 0, fmt.Errorf("WAV: unsupported format %d (PCM=1, float=3)", audioFmt)
			}
			data := make([]byte, dataSize)
			if _, err := io.ReadFull(f, data); err != nil {
				return nil, 0, 0, 0, fmt.Errorf("WAV: read data: %w", err)
			}
			samples := wavBytesToFloat32(data, audioFmt, bd)
			return samples, sr, uint32(ch), int(bd), nil
		}

		f.Seek(pos+int64(chunkSize), io.SeekStart)
	}

	return nil, 0, 0, 0, fmt.Errorf("WAV: data chunk not found")
}

func wavBytesToFloat32(data []byte, audioFmt, bd uint16) []float32 {
	bytesPerSample := int(bd) / 8
	n := len(data) / bytesPerSample
	out := make([]float32, n)

	switch {
	case audioFmt == 3 && bd == 32:
		// IEEE float32
		for i := 0; i < n; i++ {
			bits := binary.LittleEndian.Uint32(data[i*4:])
			out[i] = math.Float32frombits(bits)
		}
	case audioFmt == 1 && bd == 16:
		for i := 0; i < n; i++ {
			s := int16(binary.LittleEndian.Uint16(data[i*2:]))
			out[i] = float32(s) / 32768.0
		}
	case audioFmt == 1 && bd == 24:
		for i := 0; i < n; i++ {
			b := data[i*3 : i*3+3]
			v := int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16
			if v&0x800000 != 0 {
				v |= ^int32(0xFFFFFF)
			}
			out[i] = float32(v) / 8388608.0
		}
	case audioFmt == 1 && bd == 32:
		for i := 0; i < n; i++ {
			s := int32(binary.LittleEndian.Uint32(data[i*4:]))
			out[i] = float32(s) / 2147483648.0
		}
	}
	return out
}
