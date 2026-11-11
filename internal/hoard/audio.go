package hoard

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/hajimehoshi/go-mp3"
	"github.com/jfreymuth/oggvorbis"
	"github.com/mewkiz/flac"
)

// Server-side audio previews: a waveform thumbnail and the technical
// attributes (duration, sample rate, channels, bit depth, peak level), made
// without ffmpeg or cgo. Decoding is streamed and capped, so a long or hostile
// file cannot use unbounded memory or time.

const (
	// maxPreviewSeconds caps how much of a file is decoded for the waveform.
	maxPreviewSeconds = 10 * 60
	// peakBlockFrames is the number of frames summarised by one peak value.
	peakBlockFrames = 64
	waveBars        = 72 // bars in the waveform, as in the web UI
	maxPreviewTime  = 30 * time.Second
)

// audioPreview lists the extensions decoded on the server.
var audioPreview = map[string]bool{".wav": true, ".mp3": true, ".ogg": true, ".oga": true, ".flac": true}

// audioInfo is the result of one pass over an audio file.
type audioInfo struct {
	Duration   float64 // seconds; 0 when unknown
	SampleRate int
	Channels   int // 0 when the format hides it (MP3 is decoded to stereo)
	BitDepth   int // 0 when not a property of the file (lossy formats)
	Peak       float64
	Complete   bool // the whole file was decoded (Peak is the true peak)
	peaks      []float32
}

// peakCollector summarises a stream of frames into block maxima.
type peakCollector struct {
	maxFrames int64
	frames    int64
	cur       float32
	n         int
	blocks    []float32
	peak      float32
}

var errPreviewCap = errors.New("preview cap reached")

// add records the loudest sample of one frame; it returns errPreviewCap when
// enough has been read.
func (c *peakCollector) add(v float32) error {
	if v < 0 {
		v = -v
	}
	if v > c.cur {
		c.cur = v
	}
	c.frames++
	c.n++
	if c.n == peakBlockFrames {
		c.flush()
	}
	if c.frames >= c.maxFrames {
		c.flush()
		return errPreviewCap
	}
	return nil
}

func (c *peakCollector) flush() {
	if c.n == 0 {
		return
	}
	c.blocks = append(c.blocks, c.cur)
	if c.cur > c.peak {
		c.peak = c.cur
	}
	c.cur, c.n = 0, 0
}

// bars reduces the collected blocks to n bars by taking maxima.
func (c *peakCollector) bars(n int) []float32 {
	out := make([]float32, n)
	b := len(c.blocks)
	if b == 0 {
		return out
	}
	for i := range out {
		lo := i * b / n
		hi := max(lo+1, (i+1)*b/n)
		for j := lo; j < hi && j < b; j++ {
			out[i] = max(out[i], c.blocks[j])
		}
	}
	return out
}

// analyzeAudio decodes the start of an audio file and returns its attributes
// and waveform peaks. It never panics: the decoders are fed untrusted files.
func analyzeAudio(ctx context.Context, path, extension string) (info *audioInfo, err error) {
	defer func() {
		if r := recover(); r != nil {
			info, err = nil, fmt.Errorf("decoder failed: %v", r)
		}
	}()
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	switch extension {
	case ".wav":
		return analyzeWAV(ctx, fh)
	case ".mp3":
		return analyzeMP3(ctx, fh)
	case ".ogg", ".oga":
		return analyzeOgg(ctx, fh)
	case ".flac":
		return analyzeFLAC(ctx, fh)
	}
	return nil, fmt.Errorf("no decoder for %s", extension)
}

func finishInfo(c *peakCollector, info *audioInfo, capped bool) *audioInfo {
	c.flush()
	info.Peak = float64(c.peak)
	info.Complete = !capped
	info.peaks = c.bars(waveBars)
	return info
}

// ---- WAV ----

func analyzeWAV(ctx context.Context, fh *os.File) (*audioInfo, error) {
	st, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(fh, 64<<10)
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	if string(hdr[0:4]) != "RIFF" && string(hdr[0:4]) != "RF64" || string(hdr[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	var (
		tag, channels, bits int
		rate, align         int
		haveFmt             bool
	)
	pos := int64(12)
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return nil, errors.New("no data chunk")
		}
		pos += 8
		id, size := string(ch[0:4]), int64(binary.LittleEndian.Uint32(ch[4:8]))
		switch id {
		case "fmt ":
			if size < 16 || size > 1024 {
				return nil, errors.New("bad fmt chunk")
			}
			buf := make([]byte, size)
			if _, err := io.ReadFull(r, buf); err != nil {
				return nil, err
			}
			tag = int(binary.LittleEndian.Uint16(buf[0:2]))
			channels = int(binary.LittleEndian.Uint16(buf[2:4]))
			rate = int(binary.LittleEndian.Uint32(buf[4:8]))
			align = int(binary.LittleEndian.Uint16(buf[12:14]))
			bits = int(binary.LittleEndian.Uint16(buf[14:16]))
			if tag == 0xFFFE && size >= 26 {
				tag = int(binary.LittleEndian.Uint16(buf[24:26]))
			}
			haveFmt = true
			pos += size
			if size%2 == 1 {
				_, _ = r.Discard(1)
				pos++
			}
		case "data":
			if !haveFmt {
				return nil, errors.New("data before fmt chunk")
			}
			return wavData(ctx, r, size, st.Size()-pos, tag, channels, bits, rate, align)
		default:
			skip := size + size%2
			if _, err := io.CopyN(io.Discard, r, skip); err != nil {
				return nil, errors.New("no data chunk")
			}
			pos += skip
		}
	}
}

func wavData(ctx context.Context, r *bufio.Reader, size, remaining int64, tag, channels, bits, rate, align int) (*audioInfo, error) {
	if channels < 1 || channels > 32 || rate < 1000 || rate > 768000 {
		return nil, errors.New("unsupported WAV parameters")
	}
	float := tag == 3
	switch {
	case tag == 1 && (bits == 8 || bits == 16 || bits == 24 || bits == 32):
	case float && (bits == 32 || bits == 64):
	default:
		return nil, fmt.Errorf("unsupported WAV encoding (format %d, %d bit)", tag, bits)
	}
	frameBytes := channels * bits / 8
	if align != frameBytes {
		return nil, errors.New("inconsistent WAV block alignment")
	}
	// A streamed file may declare 0 or 0xFFFFFFFF; trust the file size then.
	if size <= 0 || size > remaining {
		size = remaining
	}
	total := size / int64(frameBytes)
	info := &audioInfo{SampleRate: rate, Channels: channels, BitDepth: bits, Duration: float64(total) / float64(rate)}
	col := &peakCollector{maxFrames: int64(maxPreviewSeconds) * int64(rate)}
	buf := make([]byte, 4096*frameBytes)
	capped := false
	for left := total; left > 0 && !capped; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := int(min(left, 4096))
		chunk := buf[:n*frameBytes]
		if _, err := io.ReadFull(r, chunk); err != nil {
			info.Duration = float64(total-left) / float64(rate) // truncated file
			break
		}
		for f := 0; f < n && !capped; f++ {
			var m float32
			for c := 0; c < channels; c++ {
				p := chunk[(f*channels+c)*bits/8:]
				var v float32
				switch {
				case float && bits == 32:
					v = math.Float32frombits(binary.LittleEndian.Uint32(p))
				case float:
					v = float32(math.Float64frombits(binary.LittleEndian.Uint64(p)))
				case bits == 8:
					v = (float32(p[0]) - 128) / 128
				case bits == 16:
					v = float32(int16(binary.LittleEndian.Uint16(p))) / 32768
				case bits == 24:
					v = float32(int32(uint32(p[0])<<8|uint32(p[1])<<16|uint32(p[2])<<24)>>8) / 8388608
				default:
					v = float32(int32(binary.LittleEndian.Uint32(p))) / 2147483648
				}
				if v != v { // NaN
					v = 0
				}
				if v < 0 {
					v = -v
				}
				m = max(m, v)
			}
			if err := col.add(m); errors.Is(err, errPreviewCap) {
				capped = true
			}
		}
		left -= int64(n)
	}
	return finishInfo(col, info, capped), nil
}

// ---- MP3 ----

func analyzeMP3(ctx context.Context, fh *os.File) (*audioInfo, error) {
	dec, err := mp3.NewDecoder(fh)
	if err != nil {
		return nil, err
	}
	rate := dec.SampleRate()
	if rate < 8000 || rate > 96000 {
		return nil, errors.New("implausible MP3 sample rate")
	}
	info := &audioInfo{SampleRate: rate}
	if n := dec.Length(); n > 0 {
		info.Duration = float64(n/4) / float64(rate)
	}
	col := &peakCollector{maxFrames: int64(maxPreviewSeconds) * int64(rate)}
	// The decoder always yields 16-bit stereo: 4 bytes per frame.
	buf := make([]byte, 16<<10)
	capped := false
	var decoded int64
	for !capped {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := dec.Read(buf)
		for i := 0; i+4 <= n && !capped; i += 4 {
			l := math.Abs(float64(int16(binary.LittleEndian.Uint16(buf[i:]))))
			rr := math.Abs(float64(int16(binary.LittleEndian.Uint16(buf[i+2:]))))
			decoded++
			if e := col.add(float32(math.Max(l, rr) / 32768)); errors.Is(e, errPreviewCap) {
				capped = true
			}
		}
		if err != nil {
			break
		}
	}
	if info.Duration == 0 && !capped {
		info.Duration = float64(decoded) / float64(rate)
	}
	return finishInfo(col, info, capped), nil
}

// ---- Ogg Vorbis ----

func analyzeOgg(ctx context.Context, fh *os.File) (*audioInfo, error) {
	rd, err := oggvorbis.NewReader(bufio.NewReaderSize(fh, 64<<10))
	if err != nil {
		return nil, err
	}
	rate, ch := rd.SampleRate(), rd.Channels()
	if rate < 1000 || rate > 768000 || ch < 1 || ch > 32 {
		return nil, errors.New("unsupported Ogg Vorbis parameters")
	}
	info := &audioInfo{SampleRate: rate, Channels: ch}
	if n := rd.Length(); n > 0 {
		info.Duration = float64(n) / float64(rate)
	}
	col := &peakCollector{maxFrames: int64(maxPreviewSeconds) * int64(rate)}
	buf := make([]float32, 4096*ch)
	capped := false
	var decoded int64
	for !capped {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := rd.Read(buf)
		for i := 0; i+ch <= n && !capped; i += ch {
			var m float32
			for c := 0; c < ch; c++ {
				v := buf[i+c]
				if v < 0 {
					v = -v
				}
				m = max(m, v)
			}
			decoded++
			if e := col.add(m); errors.Is(e, errPreviewCap) {
				capped = true
			}
		}
		if err != nil {
			break
		}
	}
	if info.Duration == 0 && !capped {
		info.Duration = float64(decoded) / float64(rate)
	}
	return finishInfo(col, info, capped), nil
}

// ---- FLAC ----

func analyzeFLAC(ctx context.Context, fh *os.File) (*audioInfo, error) {
	stream, err := flac.New(bufio.NewReaderSize(fh, 64<<10))
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	si := stream.Info
	rate, ch, depth := int(si.SampleRate), int(si.NChannels), int(si.BitsPerSample)
	if rate < 1000 || ch < 1 || depth < 4 || depth > 32 {
		return nil, errors.New("unsupported FLAC parameters")
	}
	info := &audioInfo{SampleRate: rate, Channels: ch, BitDepth: depth}
	var decoded int64
	if si.NSamples > 0 {
		info.Duration = float64(si.NSamples) / float64(rate)
	}
	scale := float32(math.Ldexp(1, depth-1))
	col := &peakCollector{maxFrames: int64(maxPreviewSeconds) * int64(rate)}
	capped := false
	for !capped {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fr, err := stream.ParseNext()
		if err != nil {
			break
		}
		for i := 0; i < int(fr.BlockSize) && !capped; i++ {
			var m float32
			for _, sf := range fr.Subframes {
				if i < len(sf.Samples) {
					v := float32(sf.Samples[i]) / scale
					if v < 0 {
						v = -v
					}
					m = max(m, v)
				}
			}
			decoded++
			if e := col.add(m); errors.Is(e, errPreviewCap) {
				capped = true
			}
		}
	}
	if info.Duration == 0 && !capped {
		info.Duration = float64(decoded) / float64(rate)
	}
	return finishInfo(col, info, capped), nil
}

// ---- rendering ----

var waveGold = [4]uint8{0xe9, 0xb4, 0x4c, 0xff}

// renderWaveform draws peaks (0..1) the way the web UI does: gold bars,
// mirrored around the middle of a square, on a transparent background.
func renderWaveform(peaks []float32) *image.NRGBA {
	const size = thumbSize
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	h := float64(size) * 0.6
	mid := float64(size) * 0.5
	bar := float64(size) / float64(len(peaks))
	for i, p := range peaks {
		amp := math.Max(0.02, float64(p)) * (h * 0.46)
		if amp > h*0.46 {
			amp = h * 0.46
		}
		x0 := int(math.Round(float64(i)*bar + bar*0.15))
		x1 := x0 + max(1, int(math.Round(bar*0.7)))
		y0, y1 := int(math.Round(mid-amp)), int(math.Round(mid+amp))
		for y := max(y0, 0); y < min(y1, size); y++ {
			for x := max(x0, 0); x < min(x1, size); x++ {
				copy(img.Pix[y*img.Stride+x*4:], waveGold[:])
			}
		}
	}
	return img
}

// attrsFromAudio converts the analysis to asset attributes.
func attrsFromAudio(a *audioInfo) map[string]any {
	m := map[string]any{"sample_rate": a.SampleRate}
	if a.Duration > 0 {
		m["duration_s"] = math.Round(a.Duration*100) / 100
	}
	if a.Channels > 0 {
		m["channels"] = a.Channels
	}
	if a.BitDepth > 0 {
		m["bit_depth"] = a.BitDepth
	}
	if a.Complete {
		db := -120.0
		if a.Peak > 0 {
			db = math.Max(-120, 20*math.Log10(a.Peak))
		}
		m["peak_dbfs"] = math.Round(db*10) / 10
	}
	return m
}

// audioPreviewFile makes the waveform thumbnail (when the asset has none) and
// fills in missing audio attributes of the primary file at path.
func (s *Service) audioPreviewFile(ctx context.Context, assetID, path, extension string) error {
	needThumb, attrs, err := s.previewNeeds(ctx, assetID)
	if err != nil {
		return err
	}
	_, haveDur := attrs["duration_s"]
	_, havePeak := attrs["peak_dbfs"]
	if !needThumb && haveDur && havePeak {
		return nil
	}
	release, err := s.acquireThumb(ctx)
	if err != nil {
		return err
	}
	// Decoding runs inside the upload request: give up rather than keep a
	// client waiting; the browser renders such a file later.
	actx, cancel := context.WithTimeout(ctx, maxPreviewTime)
	info, err := analyzeAudio(actx, path, extension)
	cancel()
	release()
	if err != nil {
		return err
	}
	if needThumb {
		if err := s.putThumb(ctx, assetID, renderWaveform(info.peaks)); err != nil {
			return err
		}
	}
	if err := s.setAbsentAttrs(ctx, assetID, attrsFromAudio(info)); err != nil {
		return err
	}
	s.syncSidecar(ctx, assetID)
	return nil
}

// previewNeeds reports whether the asset still lacks a thumbnail and returns
// its attributes.
func (s *Service) previewNeeds(ctx context.Context, assetID string) (needThumb bool, attrs map[string]any, err error) {
	var version int
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT thumb_version, attrs FROM assets WHERE id = ?`, assetID).Scan(&version, &raw); err != nil {
		return false, nil, err
	}
	attrs = map[string]any{}
	_ = json.Unmarshal([]byte(raw), &attrs)
	return version == 0, attrs, nil
}

// setAbsentAttrs adds the given attributes that the asset does not have yet.
// Values measured from the file are derived data: updated_at and the activity
// feed stay untouched.
func (s *Service) setAbsentAttrs(ctx context.Context, assetID string, add map[string]any) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT attrs FROM assets WHERE id = ?`, assetID).Scan(&raw); err != nil {
			return err
		}
		attrs := map[string]any{}
		_ = json.Unmarshal([]byte(raw), &attrs)
		changed := false
		for k, v := range add {
			if _, ok := attrs[k]; !ok {
				attrs[k] = v
				changed = true
			}
		}
		if !changed {
			return nil
		}
		b, err := json.Marshal(attrs)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET attrs = ? WHERE id = ?`, string(b), assetID); err != nil {
			return err
		}
		return reindex(ctx, tx, assetID)
	})
}

// previewFilePath resolves a stored file for background jobs.
func (s *Service) previewFilePath(assetID, filename string) string {
	return filepath.Join(s.assetDir(assetID), "files", filename)
}
