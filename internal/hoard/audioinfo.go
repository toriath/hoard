package hoard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
)

// AudioInfo is what hoard can tell about an audio file from its header and,
// for PCM WAV, from its samples.
type AudioInfo struct {
	Format     string  // wav, ogg, opus, flac
	SampleRate int     // Hz
	Channels   int     //
	BitDepth   int     // 0 when unknown or not meaningful (Vorbis, Opus)
	DurationS  float64 // 0 when unknown

	// Measured only for PCM and float WAV.
	Measured bool
	PeakDBFS float64
	// LoudnessApprox estimates loudness in LUFS-like dB: the energy of all
	// channels summed (as in BS.1770), averaged over the 100 ms blocks above
	// -70 dB, minus 0.691. There is no K-weighting, so it is NOT true LUFS.
	LoudnessApprox float64
}

const (
	maxAnalyzeBytes = 256 << 20 // of sample data per file
	silenceFloor    = 1e-7      // mean square of a 100 ms block below which it counts as silence (-70 dBFS)
	dbfsFloor       = -120.0
)

// AnalyzeAudio inspects a file. The extension selects the parser; formats
// that are not understood return an error.
func AnalyzeAudio(ctx context.Context, path string) (*AudioInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	switch ext(path) {
	case ".wav":
		return analyzeWAV(ctx, f, st.Size())
	case ".ogg", ".oga", ".opus":
		return analyzeOgg(f)
	case ".flac":
		return analyzeFLAC(f)
	}
	return nil, errors.New("format not supported by the audio checker")
}

func toDB(v float64) float64 {
	if v <= 0 {
		return dbfsFloor
	}
	return math.Max(dbfsFloor, 20*math.Log10(v))
}

func analyzeWAV(ctx context.Context, f *os.File, size int64) (*AudioInfo, error) {
	br := bufio.NewReaderSize(f, 64<<10)
	var head [12]byte
	if _, err := io.ReadFull(br, head[:]); err != nil || string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	var (
		tag, bits, block int
		info             = &AudioInfo{Format: "wav"}
		haveFmt          bool
	)
	for chunks := 0; ; chunks++ {
		if chunks > 64 {
			return nil, errors.New("WAV file has too many chunks before its data")
		}
		var ch [8]byte
		if _, err := io.ReadFull(br, ch[:]); err != nil {
			return nil, errors.New("WAV file has no data chunk")
		}
		id, n := string(ch[0:4]), int64(binary.LittleEndian.Uint32(ch[4:8]))
		pad := n & 1
		switch id {
		case "fmt ":
			if n < 16 || n > 4096 {
				return nil, errors.New("WAV fmt chunk is malformed")
			}
			buf := make([]byte, n)
			if _, err := io.ReadFull(br, buf); err != nil {
				return nil, errors.New("WAV fmt chunk is truncated")
			}
			tag = int(binary.LittleEndian.Uint16(buf[0:2]))
			info.Channels = int(binary.LittleEndian.Uint16(buf[2:4]))
			info.SampleRate = int(binary.LittleEndian.Uint32(buf[4:8]))
			block = int(binary.LittleEndian.Uint16(buf[12:14]))
			bits = int(binary.LittleEndian.Uint16(buf[14:16]))
			if tag == 0xFFFE && n >= 26 { // WAVE_FORMAT_EXTENSIBLE: the real tag opens the sub-format GUID
				tag = int(binary.LittleEndian.Uint16(buf[24:26]))
			}
			info.BitDepth = bits
			haveFmt = true
			if pad == 1 {
				_, _ = br.Discard(1)
			}
		case "data":
			if !haveFmt {
				return nil, errors.New("WAV data chunk precedes the fmt chunk")
			}
			if info.Channels < 1 || info.SampleRate < 1 || block < 1 {
				return nil, errors.New("WAV header is inconsistent")
			}
			if n > size { // streaming writers leave 0xFFFFFFFF or a stale size
				n = size
			}
			info.DurationS = float64(n) / float64(block) / float64(info.SampleRate)
			if (tag == 1 && (bits == 8 || bits == 16 || bits == 24 || bits == 32)) || (tag == 3 && (bits == 32 || bits == 64)) {
				// block must hold one sample per channel; a header that claims
				// more is bogus (and an untrusted size must never size a buffer).
				if bps := bits / 8; block >= info.Channels*bps && block <= 64 {
					measureWAV(ctx, br, n, info, tag, bits, block)
				}
			}
			return info, nil
		default:
			if n+pad > 1<<30 {
				return nil, errors.New("WAV chunk too large")
			}
			if _, err := br.Discard(int(n + pad)); err != nil {
				return nil, errors.New("WAV file has no data chunk")
			}
		}
	}
}

// measureWAV streams the samples once, in a fixed 64 KiB buffer of whole
// blocks, for the peak and the block-gated energy.
func measureWAV(ctx context.Context, r io.Reader, n int64, info *AudioInfo, tag, bits, block int) {
	if n > maxAnalyzeBytes {
		n = maxAnalyzeBytes
	}
	bps := bits / 8
	frames := int(n) / block
	if frames == 0 {
		return
	}
	blockFrames := max(1, info.SampleRate/10)
	perRead := (64 << 10) / block
	buf := make([]byte, perRead*block)
	var (
		peak, blockSum, gatedSum float64
		bFrames, gatedN, total   int
	)
	flush := func() {
		if bFrames == 0 {
			return
		}
		// Sum of the per-channel mean squares.
		if ms := blockSum / float64(bFrames); ms > silenceFloor {
			gatedSum += ms
			gatedN++
		}
		blockSum, bFrames = 0, 0
	}
	remaining := frames
	for remaining > 0 && ctx.Err() == nil {
		want := min(remaining, perRead)
		got, err := io.ReadFull(r, buf[:want*block])
		have := got / block
		for i := range have {
			frame := buf[i*block : i*block+block]
			for c := 0; c < info.Channels; c++ {
				var v float64
				b := frame[c*bps : (c+1)*bps]
				switch {
				case tag == 3 && bps == 4:
					v = float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
				case tag == 3:
					v = math.Float64frombits(binary.LittleEndian.Uint64(b))
				case bps == 1:
					v = (float64(b[0]) - 128) / 128
				case bps == 2:
					v = float64(int16(binary.LittleEndian.Uint16(b))) / 32768
				case bps == 3:
					x := int32(b[0]) | int32(b[1])<<8 | int32(int8(b[2]))<<16
					v = float64(x) / 8388608
				default:
					v = float64(int32(binary.LittleEndian.Uint32(b))) / 2147483648
				}
				if math.IsNaN(v) || math.IsInf(v, 0) {
					continue
				}
				if a := math.Abs(v); a > peak {
					peak = a
				}
				blockSum += v * v
			}
			bFrames++
			total++
			if bFrames >= blockFrames {
				flush()
			}
		}
		remaining -= have
		if err != nil || have == 0 {
			break
		}
	}
	if total == 0 || ctx.Err() != nil {
		return
	}
	// A trailing partial block shorter than half of a full one is ignored.
	if bFrames*2 >= blockFrames {
		flush()
	}
	info.Measured = true
	info.PeakDBFS = toDB(peak)
	info.LoudnessApprox = dbfsFloor // digital silence
	if gatedN > 0 {
		info.LoudnessApprox = math.Max(dbfsFloor, 10*math.Log10(gatedSum/float64(gatedN))-0.691)
	}
}

func analyzeOgg(f *os.File) (*AudioInfo, error) {
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if !bytes.HasPrefix(head, []byte("OggS")) {
		return nil, errors.New("not an Ogg file")
	}
	if i := bytes.Index(head, []byte("\x01vorbis")); i >= 0 && len(head) >= i+16 {
		p := head[i+7:] // version u32, channels u8, rate u32
		return &AudioInfo{Format: "ogg", Channels: int(p[4]), SampleRate: int(binary.LittleEndian.Uint32(p[5:9]))}, nil
	}
	if i := bytes.Index(head, []byte("OpusHead")); i >= 0 && len(head) >= i+19 {
		// Opus always decodes at 48 kHz; the header only records the original rate.
		return &AudioInfo{Format: "opus", Channels: int(head[i+9]), SampleRate: 48000}, nil
	}
	return nil, errors.New("Ogg stream is neither Vorbis nor Opus")
}

func analyzeFLAC(f *os.File) (*AudioInfo, error) {
	var b [4 + 4 + 34]byte
	if _, err := io.ReadFull(f, b[:]); err != nil || string(b[0:4]) != "fLaC" || b[4]&0x7f != 0 {
		return nil, errors.New("FLAC STREAMINFO block not found")
	}
	si := b[8:]
	rate := int(si[10])<<12 | int(si[11])<<4 | int(si[12])>>4
	ch := int(si[12]>>1&7) + 1
	bps := int(si[12]&1)<<4 | int(si[13]>>4) + 1
	total := uint64(si[13]&0x0f)<<32 | uint64(binary.BigEndian.Uint32(si[14:18]))
	info := &AudioInfo{Format: "flac", SampleRate: rate, Channels: ch, BitDepth: bps}
	if rate > 0 {
		info.DurationS = float64(total) / float64(rate)
	}
	return info, nil
}
