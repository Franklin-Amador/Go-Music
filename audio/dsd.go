// Package audio handles DSD (DSF/DFF) and PCM audio decoding.
package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// DSDRate maps sample rates to human-readable labels.
var DSDRate = map[uint32]string{
	2822400:  "DSD64",
	5644800:  "DSD128",
	11289600: "DSD256",
	22579200: "DSD512",
}

// DSDInfo holds metadata parsed from a DSF or DFF file.
type DSDInfo struct {
	Path           string
	DSDSampleRate  uint32
	PCMSampleRate  uint32
	Channels       uint32
	SampleCount    uint64
	AudioOffset    int64
	AudioSize      int64
	BlockSize      uint32 // DSF: per-channel block size; DFF: 0
	Decimation     uint32
	Label          string // "DSD64", "DSD128", etc.
	IsDFF          bool
	MetadataOffset int64 // DSF: byte offset of ID3v2 stream (0 = none)
}

// DurationSeconds returns the playback duration of the DSD audio.
func (d *DSDInfo) DurationSeconds() float64 {
	if d.DSDSampleRate == 0 {
		return 0
	}
	return float64(d.SampleCount) / float64(d.DSDSampleRate)
}

// ParseDSD reads the header of a DSF or DFF file and returns its DSDInfo.
// targetPCMRate is the desired output PCM sample rate (e.g., 176400). The
// decoder picks an integer decimation factor that lands on this rate when
// possible — this keeps the DSD-to-PCM path single-stage, avoiding the noise
// aliasing that any second resampling stage would cause.
func ParseDSD(path string, targetPCMRate uint32) (*DSDInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return nil, fmt.Errorf("read magic: %w", err)
	}

	var info *DSDInfo
	switch string(magic) {
	case "DSD ":
		info, err = parseDSF(path)
	case "FRM8":
		info, err = parseDFF(path)
	default:
		return nil, fmt.Errorf("unrecognized DSD magic: %q", magic)
	}
	if err != nil {
		return nil, err
	}

	info.Path = path
	info.Label = dsdLabel(info.DSDSampleRate)
	info.Decimation = decimationFor(info.DSDSampleRate, targetPCMRate)
	info.PCMSampleRate = info.DSDSampleRate / info.Decimation

	return info, nil
}

// IsDSDFile returns true if path has a recognized DSD extension.
func IsDSDFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".dsf") || strings.HasSuffix(lower, ".dff")
}

// ────────────────────────────────────────────────────────────────────────────
// DSF parser
// ────────────────────────────────────────────────────────────────────────────

func parseDSF(path string) (*DSDInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// DSD chunk: "DSD " + chunk_size(8) + total_file_size(8) + metadata_offset(8)
	if _, err := f.Seek(4, io.SeekStart); err != nil {
		return nil, err
	}
	var dsdChunkSize, totalFileSize, metadataOffset uint64
	if err := binary.Read(f, binary.LittleEndian, &dsdChunkSize); err != nil {
		return nil, err
	}
	if err := binary.Read(f, binary.LittleEndian, &totalFileSize); err != nil {
		return nil, err
	}
	if err := binary.Read(f, binary.LittleEndian, &metadataOffset); err != nil {
		return nil, err
	}
	_ = totalFileSize

	fmtStart, _ := f.Seek(0, io.SeekCurrent)

	// fmt chunk
	fmtMagic := make([]byte, 4)
	if _, err := io.ReadFull(f, fmtMagic); err != nil {
		return nil, err
	}
	if string(fmtMagic) != "fmt " {
		return nil, fmt.Errorf("DSF: expected fmt chunk, got %q", fmtMagic)
	}
	var fmtSize uint64
	if err := binary.Read(f, binary.LittleEndian, &fmtSize); err != nil {
		return nil, err
	}

	var version, formatID, channelType, channels, dsdSR, bitsPerSample uint32
	for _, p := range []*uint32{&version, &formatID, &channelType, &channels, &dsdSR, &bitsPerSample} {
		if err := binary.Read(f, binary.LittleEndian, p); err != nil {
			return nil, fmt.Errorf("DSF fmt field: %w", err)
		}
	}
	if formatID != 0 {
		return nil, fmt.Errorf("DSF: DST compression not supported (format_id=%d)", formatID)
	}

	var sampleCount uint64
	if err := binary.Read(f, binary.LittleEndian, &sampleCount); err != nil {
		return nil, err
	}
	var blockSize uint32
	if err := binary.Read(f, binary.LittleEndian, &blockSize); err != nil {
		return nil, err
	}
	if blockSize == 0 {
		blockSize = 4096
	}

	// Find data chunk — try known offsets robustly
	dataOffset := int64(-1)
	for _, off := range []int64{fmtStart + int64(fmtSize), fmtStart + 4 + 8 + int64(fmtSize), 80} {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			continue
		}
		tag := make([]byte, 4)
		if _, err := io.ReadFull(f, tag); err != nil {
			continue
		}
		if string(tag) == "data" {
			dataOffset = off
			break
		}
	}
	if dataOffset < 0 {
		return nil, fmt.Errorf("DSF: data chunk not found")
	}
	if _, err := f.Seek(dataOffset+4, io.SeekStart); err != nil {
		return nil, err
	}
	var chunkSize uint64
	if err := binary.Read(f, binary.LittleEndian, &chunkSize); err != nil {
		return nil, err
	}
	audioOffset, _ := f.Seek(0, io.SeekCurrent)

	return &DSDInfo{
		DSDSampleRate:  dsdSR,
		Channels:       channels,
		SampleCount:    sampleCount,
		BlockSize:      blockSize,
		AudioOffset:    audioOffset,
		AudioSize:      int64(chunkSize) - 12,
		MetadataOffset: int64(metadataOffset),
	}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// DFF parser
// ────────────────────────────────────────────────────────────────────────────

func parseDFF(path string) (*DSDInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// FRM8 header: "FRM8" + file_size(8) + "DSD "
	if _, err := f.Seek(12, io.SeekStart); err != nil {
		return nil, err
	}

	var (
		channels    uint32
		dsdSR       uint32
		audioOffset int64
		audioSize   int64
	)

	for {
		chunkID := make([]byte, 4)
		if _, err := io.ReadFull(f, chunkID); err != nil {
			break
		}
		var chunkSize uint64
		if err := binary.Read(f, binary.BigEndian, &chunkSize); err != nil {
			break
		}
		pos, _ := f.Seek(0, io.SeekCurrent)

		switch string(chunkID) {
		case "PROP":
			f.Seek(4, io.SeekCurrent) // prop type "SND "
			end := pos + int64(chunkSize)
			for {
				cur, _ := f.Seek(0, io.SeekCurrent)
				if cur >= end-11 {
					break
				}
				subID := make([]byte, 4)
				if _, err := io.ReadFull(f, subID); err != nil {
					break
				}
				var subSize uint64
				if err := binary.Read(f, binary.BigEndian, &subSize); err != nil {
					break
				}
				subPos, _ := f.Seek(0, io.SeekCurrent)
				switch string(subID) {
				case "FS  ":
					binary.Read(f, binary.BigEndian, &dsdSR)
				case "CHNL":
					var ch uint16
					binary.Read(f, binary.BigEndian, &ch)
					channels = uint32(ch)
				case "CMPR":
					cmpr := make([]byte, 4)
					io.ReadFull(f, cmpr)
					if string(cmpr) != "DSD " {
						return nil, fmt.Errorf("DFF: compression %q not supported", cmpr)
					}
				}
				f.Seek(subPos+int64(subSize), io.SeekStart)
			}

		case "DSD ", "DSTI":
			audioOffset = pos
			audioSize = int64(chunkSize)
		}

		f.Seek(pos+int64(chunkSize), io.SeekStart)
	}

	if channels == 0 || dsdSR == 0 {
		return nil, fmt.Errorf("DFF: incomplete header (channels=%d samplerate=%d)", channels, dsdSR)
	}
	if audioOffset == 0 {
		return nil, fmt.Errorf("DFF: audio chunk not found")
	}

	sampleCount := uint64(audioSize*8) / uint64(channels)

	return &DSDInfo{
		DSDSampleRate: dsdSR,
		Channels:      channels,
		SampleCount:   sampleCount,
		BlockSize:     0, // DFF: bit-interleaved, no block structure
		AudioOffset:   audioOffset,
		AudioSize:     audioSize,
		IsDFF:         true,
	}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// DSD → PCM streaming decoder
// ────────────────────────────────────────────────────────────────────────────

const pcmFramesPerChunk = 4096

// DSDDecoder streams DSD audio as PCM float32 chunks.
//
// The pipeline is a SINGLE filter stage: per-channel bits go straight into
// a long Kaiser-windowed FIR running at the DSD bit rate, which both
// anti-aliases AND decimates to the output PCM rate. There is no separate
// block-mean / post-decimation step — fixing that two-stage approach is what
// removed the audible DSD-noise alias buzz.
type DSDDecoder struct {
	info      *DSDInfo
	decimator *DSDDecimatingFIR
	// seekBytes is added to AudioOffset on IterChunks start, and subtracted
	// from AudioSize for the loop budget. Set by SeekTo before iteration.
	seekBytes int64
}

// NewDSDDecoder creates a DSDDecoder for the given DSDInfo. The DSD info
// must already have its target PCM rate set (via ParseDSD's targetPCMRate
// argument). Call IterChunks to stream PCM data.
func NewDSDDecoder(info *DSDInfo) *DSDDecoder {
	dec := newDSDDecimatingFIR(info.DSDSampleRate, info.PCMSampleRate, int(info.Channels))
	return &DSDDecoder{info: info, decimator: dec}
}

// SeekTo positions the decoder so the next IterChunks call starts streaming
// PCM from `byteOffset` into the audio payload (i.e. AudioOffset+byteOffset
// in the file). Caller is responsible for block alignment — see
// computeDSDSeekOffset.
func (d *DSDDecoder) SeekTo(byteOffset int64) {
	if byteOffset < 0 {
		byteOffset = 0
	}
	if byteOffset >= d.info.AudioSize {
		byteOffset = d.info.AudioSize - 1
	}
	d.seekBytes = byteOffset
}

// IterChunks calls fn with each PCM chunk (frames × channels interleaved float32).
// Stops if fn returns false or an error occurs.
func (d *DSDDecoder) IterChunks(fn func(chunk []float32, frames int) bool) error {
	info := d.info
	f, err := os.Open(info.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Seek(info.AudioOffset+d.seekBytes, io.SeekStart); err != nil {
		return err
	}

	D := int(info.Decimation)
	ch := int(info.Channels)
	bs := int(info.BlockSize)
	if bs == 0 {
		bs = 1
	}

	// Bytes per read: enough to produce pcmFramesPerChunk PCM frames.
	dsdBytesNeeded := (pcmFramesPerChunk * D * ch + 7) / 8
	blockBytes := bs * ch
	dsdBytesNeeded = ((dsdBytesNeeded + blockBytes - 1) / blockBytes) * blockBytes
	if dsdBytesNeeded < blockBytes*4 {
		dsdBytesNeeded = blockBytes * 4
	}

	raw := make([]byte, dsdBytesNeeded)
	remaining := info.AudioSize - d.seekBytes

	for remaining > 0 {
		toRead := int64(dsdBytesNeeded)
		if toRead > remaining {
			toRead = remaining
		}
		n, err := io.ReadFull(f, raw[:toRead])
		if n == 0 || (err != nil && err != io.ErrUnexpectedEOF) {
			break
		}
		remaining -= int64(n)
		chunk := raw[:n]

		var bitsPerCh [][]byte // [ch][nBits], values 0 or 1
		if !info.IsDFF {
			bitsPerCh = unpackDSFBlocks(chunk, ch, bs)
		} else {
			bitsPerCh = unpackDFFInterleaved(chunk, ch)
		}
		if bitsPerCh == nil {
			continue
		}

		// Single-stage decimating FIR — interleaved float32 output, ready
		// for the ring buffer.
		pcm := d.decimator.Process(bitsPerCh)
		if len(pcm) == 0 {
			continue
		}
		frames := len(pcm) / ch

		if !fn(pcm, frames) {
			break
		}
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Bit unpacking
// ────────────────────────────────────────────────────────────────────────────

// unpackDSFBlocks unpacks DSF block-interleaved data into per-channel bit arrays.
// DSF layout: [blk0_ch0 | blk0_ch1 | blk1_ch0 | blk1_ch1 | ...]
// Each block = blockSize bytes per channel.
func unpackDSFBlocks(raw []byte, ch, blockSize int) [][]byte {
	blockBytes := blockSize * ch
	nBlocks := len(raw) / blockBytes
	if nBlocks == 0 {
		return nil
	}
	bitsPerBlock := blockSize * 8
	result := make([][]byte, ch)
	for c := range result {
		result[c] = make([]byte, nBlocks*bitsPerBlock)
	}
	for b := 0; b < nBlocks; b++ {
		for c := 0; c < ch; c++ {
			src := raw[b*blockBytes+c*blockSize : b*blockBytes+c*blockSize+blockSize]
			dst := result[c][b*bitsPerBlock : (b+1)*bitsPerBlock]
			unpackBitsLittle(src, dst)
		}
	}
	return result
}

// unpackDFFInterleaved unpacks DFF bit-interleaved data into per-channel bit arrays.
// DFF layout: bits interleaved channel by channel at the bit level.
func unpackDFFInterleaved(raw []byte, ch int) [][]byte {
	aligned := (len(raw) / ch) * ch
	if aligned == 0 {
		return nil
	}
	// Unpack all bytes
	allBits := make([]byte, aligned*8)
	unpackBitsBig(raw[:aligned], allBits)

	nBitsPerCh := len(allBits) / ch
	result := make([][]byte, ch)
	for c := range result {
		result[c] = make([]byte, nBitsPerCh)
		for i := 0; i < nBitsPerCh; i++ {
			result[c][i] = allBits[i*ch+c]
		}
	}
	return result
}

// unpackBitsLittle unpacks bytes LSB-first (DSF bit order).
func unpackBitsLittle(src, dst []byte) {
	for i, b := range src {
		for j := 0; j < 8; j++ {
			dst[i*8+j] = (b >> j) & 1
		}
	}
}

// unpackBitsBig unpacks bytes MSB-first (DFF bit order).
func unpackBitsBig(src, dst []byte) {
	for i, b := range src {
		for j := 0; j < 8; j++ {
			dst[i*8+j] = (b >> (7 - j)) & 1
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Kaiser FIR design
// ────────────────────────────────────────────────────────────────────────────

// buildKaiserFIR designs a low-pass FIR filter with a Kaiser window using
// scipy's `firwin`-style convention: cutoff is normalized to Nyquist (fs/2),
// so a value of 0.5 means fs/4 in absolute Hz, and 1.0 would mean fs/2.
//
// For an ideal lowpass with cutoff fc (in Hz) at sample rate fs:
//   normalized_cutoff = 2 * fc / fs   (i.e., fraction of Nyquist)
//   h[n] = normalized_cutoff * sinc(normalized_cutoff * (n - M/2))
//
// numTaps: number of filter taps.
// cutoff:  normalized cutoff frequency in [0, 1] where 1.0 = Nyquist (fs/2).
// beta:    Kaiser window parameter (12.0 → ~115 dB stopband attenuation).
func buildKaiserFIR(numTaps int, cutoff, beta float64) []float32 {
	h := make([]float32, numTaps)
	M := numTaps - 1
	I0beta := besselI0(beta)

	for n := 0; n <= M; n++ {
		t := float64(n) - float64(M)/2
		var sinc float64
		if t == 0 {
			sinc = cutoff
		} else {
			sinc = math.Sin(math.Pi*cutoff*t) / (math.Pi * t)
		}
		arg := 2*float64(n)/float64(M) - 1
		w := besselI0(beta*math.Sqrt(1-arg*arg)) / I0beta
		h[n] = float32(sinc * w)
	}
	return h
}

// besselI0 computes the modified Bessel function of the first kind, order 0.
func besselI0(x float64) float64 {
	sum := 1.0
	term := 1.0
	for k := 1; k <= 30; k++ {
		term *= (x / 2) / float64(k)
		term2 := term * term
		sum += term2
		if term2 < 1e-15 {
			break
		}
	}
	return sum
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

func dsdLabel(sr uint32) string {
	if l, ok := DSDRate[sr]; ok {
		return l
	}
	return fmt.Sprintf("DSD%d", sr/44100)
}

// decimationFor returns the integer decimation factor that takes dsdSR down
// to (or as close as possible to) targetSR. Since DSD rates are all integer
// multiples of 44100, picking targetSR as a 44100-multiple guarantees an
// exact match (no second-stage resampling needed).
func decimationFor(dsdSR, targetSR uint32) uint32 {
	if targetSR == 0 {
		targetSR = 176400
	}
	d := dsdSR / targetSR
	if d < 1 {
		d = 1
	}
	return d
}
