package hoard

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
	"golang.org/x/image/draw"
	"golang.org/x/image/font/gofont/goregular"
)

// plasma draws a smooth, structured picture; different seeds give unrelated pictures.
func plasma(seed uint64, size int) *image.NRGBA {
	r := rand.New(rand.NewPCG(seed, seed*7+1))
	var f, p [6]float64
	for i := range f {
		f[i] = 1 + r.Float64()*5
		p[i] = r.Float64() * 2 * math.Pi
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			u, v := float64(x)/float64(size)*2*math.Pi, float64(y)/float64(size)*2*math.Pi
			val := func(a, b, c int) uint8 {
				t := math.Sin(u*f[a]+p[a]) + math.Sin(v*f[b]+p[b]) + math.Sin((u+v)*f[c]+p[c])
				return uint8(128 + 42*t)
			}
			img.SetNRGBA(x, y, color.NRGBA{val(0, 1, 2), val(3, 4, 5), val(1, 3, 5), 255})
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, img image.Image, q int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func scaled(src image.Image, size int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst
}

func TestPHashIsStableAcrossReencoding(t *testing.T) {
	base := plasma(1, 256)
	h := dHash(base)
	if h == "" {
		t.Fatal("no hash for a structured picture")
	}
	jpg, _ := jpeg.Decode(bytes.NewReader(encodeJPEG(t, base, 60)))
	small := scaled(base, 100)
	for name, img := range map[string]*image.NRGBA{
		"jpeg q60": toNRGBA(jpg),
		"rescaled": small,
		"png":      base,
	} {
		if d := HashDistance(h, dHash(img)); d > NearIdenticalDistance {
			t.Errorf("%s: distance %d from the original, want <= %d", name, d, NearIdenticalDistance)
		}
	}
	// Unrelated pictures are far apart.
	for seed := uint64(2); seed < 8; seed++ {
		if d := HashDistance(h, dHash(plasma(seed, 256))); d <= SimilarDistance {
			t.Errorf("picture %d is only %d bits from picture 1", seed, d)
		}
	}
	// A flat picture has no hash.
	flat := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	if dHash(flat) != "" {
		t.Error("flat picture got a hash")
	}
}

func toNRGBA(src image.Image) *image.NRGBA {
	dst := image.NewNRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	return dst
}

func TestSimilarityAndDuplicates(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	base := plasma(11, 256)
	orig := encodePNG(t, base)

	a := mustAsset(t, s, AssetInput{Name: ptr("orig")}, "orig.png", orig)
	// the same picture, re-encoded as a smaller JPEG
	b := mustAsset(t, s, AssetInput{Name: ptr("reencoded")}, "re.jpg", encodeJPEG(t, scaled(base, 180), 70))
	// identical bytes
	c := mustAsset(t, s, AssetInput{Name: ptr("copy")}, "copy.png", orig)
	// something else
	d := mustAsset(t, s, AssetInput{Name: ptr("other")}, "other.png", encodePNG(t, plasma(12, 256)))

	// The upload result reports both kinds of look-alikes.
	res, err := s.AddFile(ctx, mustAsset(t, s, AssetInput{Name: ptr("fresh")}, "", nil).ID, "fresh.png", bytes.NewReader(orig), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DuplicateOf) != 2 {
		t.Errorf("duplicate_of = %v, want the original and its copy", res.DuplicateOf)
	}
	res, err = s.AddFile(ctx, mustAsset(t, s, AssetInput{Name: ptr("fresh2")}, "", nil).ID, "x.jpg", bytes.NewReader(encodeJPEG(t, scaled(base, 150), 65)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DuplicateOf) != 0 || len(res.SimilarTo) < 3 {
		t.Errorf("similar_to = %v duplicate_of = %v", res.SimilarTo, res.DuplicateOf)
	}

	hits, err := s.FindSimilar(ctx, a.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 {
		t.Fatalf("hits = %+v", hits)
	}
	// ranking: identical bytes first, then the visually near-identical re-encode; the unrelated picture is absent
	if hits[0].Score != 1 || !contains(hits[0].Reasons, "identical file") || hits[1].Score != 1 || hits[2].Score >= 1 {
		t.Errorf("hits = %+v %+v %+v", hits[0], hits[1], hits[2])
	}
	if ids := []string{hits[0].Asset.ID, hits[1].Asset.ID}; !contains(ids, c.ID) {
		t.Errorf("the copy is not among the identical hits: %v", ids)
	}
	var gotB bool
	for _, h := range hits {
		if h.Asset.ID == d.ID {
			t.Errorf("unrelated picture listed: %+v", h)
		}
		if h.Asset.ID == b.ID {
			gotB = true
			if h.Distance == nil || !strings.HasPrefix(h.Reasons[0], "visually near-identical (distance ") || h.Score >= 1 {
				t.Errorf("re-encoded hit = %+v", h)
			}
		}
	}
	if !gotB {
		t.Error("re-encoded picture not found")
	}

	groups, err := s.FindDuplicates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var identical, near *DuplicateGroup
	for i := range groups {
		switch groups[i].Kind {
		case "identical":
			identical = &groups[i]
		case "near-identical":
			near = &groups[i]
		}
	}
	if identical == nil || !hasAssets(identical, a.ID, c.ID) {
		t.Errorf("identical group missing: %+v", groups)
	}
	if near == nil || !hasAssets(near, a.ID, b.ID, c.ID) {
		t.Errorf("near-identical group missing: %+v", groups)
	}
	for _, g := range groups {
		if hasAssets(&g, d.ID) {
			t.Errorf("unrelated picture in a duplicate group: %+v", g)
		}
	}

	// trashed assets drop out
	if _, err := s.Bulk(ctx, BulkOp{IDs: []string{c.ID}, Action: "trash"}); err != nil {
		t.Fatal(err)
	}
	hits, _ = s.FindSimilar(ctx, a.ID, 10)
	for _, h := range hits {
		if h.Asset.ID == c.ID {
			t.Error("trashed asset listed as similar")
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func hasAssets(g *DuplicateGroup, ids ...string) bool {
	for _, id := range ids {
		found := false
		for _, a := range g.Assets {
			found = found || a.ID == id
		}
		if !found {
			return false
		}
	}
	return true
}

func TestSimilarByTags(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	tags := func(t ...string) AssetInput { return AssetInput{AddTags: t, Type: ptr("sfx")} }
	a := mustAsset(t, s, tags("sword", "metal", "hit", "impact"), "a.wav", []byte("aaaa"))
	b := mustAsset(t, s, tags("sword", "metal", "hit", "swing"), "b.wav", []byte("bbbb"))
	mustAsset(t, s, tags("sword", "wood"), "c.wav", []byte("cccc"))
	mustAsset(t, s, AssetInput{AddTags: []string{"sword", "metal", "hit"}, Type: ptr("music")}, "d.wav", []byte("dddd")) // other type
	hits, err := s.FindSimilar(ctx, a.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Asset.ID != b.ID || hits[0].Reasons[0] != "shares 3 tags" || hits[0].Distance != nil {
		t.Fatalf("hits = %+v", hits)
	}
}

// ---- colours ----

func solid(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

func TestColorExtractionAndSearch(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	// left half red, right half blue, with a fully transparent green strip that must not count
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			switch {
			case y >= 90:
				img.SetNRGBA(x, y, color.NRGBA{0, 255, 0, 0})
			case x < 50:
				img.SetNRGBA(x, y, color.NRGBA{210, 30, 30, 255})
			default:
				img.SetNRGBA(x, y, color.NRGBA{30, 40, 220, 255})
			}
		}
	}
	redBlue := mustAsset(t, s, AssetInput{Name: ptr("redblue")}, "rb.png", encodePNG(t, img))
	green := mustAsset(t, s, AssetInput{Name: ptr("green")}, "g.png", encodePNG(t, solid(64, 64, color.NRGBA{40, 200, 60, 255})))
	mustAsset(t, s, AssetInput{Name: ptr("sound")}, "s.wav", []byte("RIFFxxxxWAVE"))

	cols, err := s.AssetColors(ctx, redBlue.ID)
	if err != nil || len(cols) != 2 {
		t.Fatalf("colours = %+v %v", cols, err)
	}
	for _, c := range cols {
		if c.Weight < 0.4 || c.Weight > 0.6 {
			t.Errorf("weight of %s = %v, want about 0.5", c.Color, c.Weight)
		}
	}

	find := func(colors ...string) []string {
		t.Helper()
		res, err := s.ListAssets(ctx, ListQuery{Colors: colors})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, a := range res.Items {
			ids = append(ids, a.Name)
		}
		return ids
	}
	if got := find("#ff0000"); len(got) != 1 || got[0] != "redblue" {
		t.Errorf("red search = %v", got)
	}
	if got := find("#0000ff", "#e02020"); len(got) != 1 || got[0] != "redblue" { // both must be present
		t.Errorf("blue and red search = %v", got)
	}
	if got := find("00ff00"); len(got) != 1 || got[0] != "green" {
		t.Errorf("green search = %v (the transparent strip must not count for redblue)", got)
	}
	if got := find("#ffff00"); len(got) != 0 {
		t.Errorf("yellow search = %v", got)
	}
	if _, err := s.ListAssets(ctx, ListQuery{Colors: []string{"red"}}); err == nil {
		t.Error("invalid colour accepted")
	}
	_ = green

	// nearby values share a bucket; far ones do not
	a, _ := QuantizeColor("#d94c4c")
	b, _ := QuantizeColor("#dd5555")
	c, _ := QuantizeColor("#4c4cd9")
	if a != b || a == c {
		t.Errorf("buckets %s %s %s", a, b, c)
	}
	if g, _ := QuantizeColor("#777"); g != "#808080" {
		t.Errorf("grey bucket = %s", g)
	}
}

// ---- audio ----

func wavBytes(tag uint16, bits, channels, rate int, frames int, sample func(i, ch int) float64) []byte {
	frameBytes := channels * bits / 8
	var data bytes.Buffer
	for i := 0; i < frames; i++ {
		for ch := 0; ch < channels; ch++ {
			v := sample(i, ch)
			switch {
			case tag == 3 && bits == 32:
				binary.Write(&data, binary.LittleEndian, float32(v))
			case bits == 8:
				data.WriteByte(uint8(128 + v*127))
			case bits == 16:
				binary.Write(&data, binary.LittleEndian, int16(v*32767))
			case bits == 24:
				x := int32(v * 8388607)
				data.Write([]byte{byte(x), byte(x >> 8), byte(x >> 16)})
			case bits == 32:
				binary.Write(&data, binary.LittleEndian, int32(v*2147483647))
			}
		}
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+data.Len()))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, tag)
	binary.Write(&b, binary.LittleEndian, uint16(channels))
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*frameBytes))
	binary.Write(&b, binary.LittleEndian, uint16(frameBytes))
	binary.Write(&b, binary.LittleEndian, uint16(bits))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(data.Len()))
	b.Write(data.Bytes())
	return b.Bytes()
}

func sine(amp, hz float64, rate int) func(i, ch int) float64 {
	return func(i, _ int) float64 { return amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)) }
}

func TestAnalyzeWAVFormats(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name     string
		tag      uint16
		bits, ch int
	}{
		{"u8", 1, 8, 1}, {"s16", 1, 16, 2}, {"s24", 1, 24, 1}, {"s32", 1, 32, 1}, {"f32", 3, 32, 2},
	}
	for _, c := range cases {
		data := wavBytes(c.tag, c.bits, c.ch, 22050, 22050, sine(0.5, 440, 22050))
		path := filepath.Join(dir, c.name+".wav")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := analyzeAudio(context.Background(), path, ".wav")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if info.SampleRate != 22050 || info.Channels != c.ch || info.BitDepth != c.bits || math.Abs(info.Duration-1) > 0.001 {
			t.Errorf("%s: %+v", c.name, info)
		}
		attrs := attrsFromAudio(info)
		if db := attrs["peak_dbfs"].(float64); math.Abs(db-(-6.0)) > 0.4 {
			t.Errorf("%s: peak %v dBFS, want about -6", c.name, db)
		}
		if len(info.peaks) != waveBars {
			t.Errorf("%s: %d bars", c.name, len(info.peaks))
		}
	}
	// not WAV, or an unsupported encoding
	bad := filepath.Join(dir, "bad.wav")
	os.WriteFile(bad, []byte("RIFFfakewave"), 0o600)
	if _, err := analyzeAudio(context.Background(), bad, ".wav"); err == nil {
		t.Error("garbage accepted as WAV")
	}
	os.WriteFile(bad, wavBytes(1, 16, 1, 8000, 100, sine(0.5, 100, 8000))[:60], 0o600) // truncated data chunk
	if info, err := analyzeAudio(context.Background(), bad, ".wav"); err != nil || info.Duration <= 0 {
		t.Errorf("truncated WAV: %+v %v", info, err)
	}
}

func TestAnalyzeCap(t *testing.T) {
	c := &peakCollector{maxFrames: 1000}
	var err error
	for i := 0; i < 5000 && err == nil; i++ {
		err = c.add(0.5)
	}
	if err != errPreviewCap || c.frames != 1000 {
		t.Fatalf("cap not applied: %v after %d frames", err, c.frames)
	}
}

func TestAnalyzeFLACOggMP3(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// FLAC, encoded with the library's own encoder
	const rate, n = 16000, 16000
	var buf bytes.Buffer
	info := &meta.StreamInfo{BlockSizeMin: 4096, BlockSizeMax: 4096, SampleRate: rate, NChannels: 1, BitsPerSample: 16, NSamples: n}
	enc, err := flac.NewEncoder(&buf, info)
	if err != nil {
		t.Fatal(err)
	}
	for off := 0; off < n; off += 4096 {
		size := min(4096, n-off)
		samples := make([]int32, size)
		for i := range samples {
			samples[i] = int32(16384 * math.Sin(2*math.Pi*300*float64(off+i)/rate))
		}
		f := &frame.Frame{
			Header:    frame.Header{HasFixedBlockSize: true, BlockSize: uint16(size), SampleRate: rate, Channels: frame.ChannelsMono, BitsPerSample: 16},
			Subframes: []*frame.Subframe{{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, Samples: samples, NSamples: size}},
		}
		if err := enc.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "t.flac")
	os.WriteFile(p, buf.Bytes(), 0o600)
	got, err := analyzeAudio(ctx, p, ".flac")
	if err != nil {
		t.Fatal(err)
	}
	if got.SampleRate != rate || got.Channels != 1 || got.BitDepth != 16 || math.Abs(got.Duration-1) > 0.01 || got.Peak < 0.45 || got.Peak > 0.55 {
		t.Errorf("flac: %+v", got)
	}

	// Ogg Vorbis from the decoder library's test data
	ogg, err := os.ReadFile("testdata/tone.ogg")
	if err != nil {
		t.Fatal(err)
	}
	p = filepath.Join(dir, "t.ogg")
	os.WriteFile(p, ogg, 0o600)
	got, err = analyzeAudio(ctx, p, ".ogg")
	if err != nil {
		t.Fatal(err)
	}
	if got.SampleRate != 44100 || got.Channels != 1 || got.Duration <= 0 || !got.noWaveform {
		t.Errorf("ogg: %+v", got)
	}

	// MP3: silent MPEG-1 layer III frames (128 kbit/s, 44.1 kHz, 417 bytes each)
	var mp3 bytes.Buffer
	for i := 0; i < 40; i++ {
		frame := make([]byte, 417)
		copy(frame, []byte{0xFF, 0xFB, 0x90, 0x00})
		mp3.Write(frame)
	}
	p = filepath.Join(dir, "t.mp3")
	os.WriteFile(p, mp3.Bytes(), 0o600)
	got, err = analyzeAudio(ctx, p, ".mp3")
	if err != nil {
		t.Fatal(err)
	}
	if got.SampleRate != 44100 || math.Abs(got.Duration-40*1152.0/44100) > 0.05 {
		t.Errorf("mp3: %+v", got)
	}

	// truncated or corrupt files fail cleanly
	for _, ext := range []string{".mp3", ".ogg", ".flac"} {
		p = filepath.Join(dir, "junk"+ext)
		os.WriteFile(p, bytes.Repeat([]byte{0x13, 0x37}, 3000), 0o600)
		if _, err := analyzeAudio(ctx, p, ext); err == nil {
			t.Errorf("junk accepted as %s", ext)
		}
	}
}

func TestAudioPreviewOnUpload(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	wav := wavBytes(1, 16, 1, 22050, 11025, sine(0.5, 440, 22050))

	a := mustAsset(t, s, AssetInput{Name: ptr("beep")}, "beep.wav", wav)
	p, mime := s.ThumbPath(a.ID)
	if p == "" || mime != "image/png" || a.ThumbVersion == 0 {
		t.Fatalf("no waveform thumbnail: %q %s v%d", p, mime, a.ThumbVersion)
	}
	fh, _ := os.Open(p)
	img, err := png.Decode(fh)
	fh.Close()
	if err != nil || img.Bounds().Dx() != thumbSize {
		t.Fatalf("thumbnail: %v %v", img, err)
	}
	// gold bars on a transparent background, symmetric around the middle
	var mid, top int
	for x := 0; x < thumbSize; x++ {
		if _, _, _, a := img.At(x, thumbSize/2).RGBA(); a != 0 {
			mid++
		}
		if _, _, _, a := img.At(x, 5).RGBA(); a != 0 {
			top++
		}
	}
	if mid < thumbSize/2 || top != 0 {
		t.Errorf("waveform not drawn as expected: %d opaque pixels in the middle row, %d in the top row", mid, top)
	}
	if a.Attrs["sample_rate"] != 22050.0 || a.Attrs["channels"] != 1.0 || a.Attrs["bit_depth"] != 16.0 || a.Attrs["duration_s"] != 0.5 {
		t.Errorf("attrs = %v", a.Attrs)
	}
	if db, _ := a.Attrs["peak_dbfs"].(float64); math.Abs(db+6) > 0.4 {
		t.Errorf("peak_dbfs = %v", a.Attrs["peak_dbfs"])
	}

	// The sidecar carries the attributes too.
	side, _ := os.ReadFile(filepath.Join(s.DataDir(), "assets", a.ID, "asset.json"))
	if !strings.Contains(string(side), "peak_dbfs") {
		t.Error("sidecar lacks the measured attributes")
	}

	// Existing attributes are kept; measuring changes neither updated_at nor the activity feed.
	b := mustAsset(t, s, AssetInput{Name: ptr("keep"), Attrs: map[string]any{"duration_s": 99.0}}, "", nil)
	before, _ := s.GetAsset(ctx, b.ID)
	feed, _ := s.ListActivity(ctx, ActivityQuery{Limit: 500})
	if err := s.setAbsentAttrs(ctx, b.ID, map[string]any{"duration_s": 1.0, "sample_rate": 44100}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetAsset(ctx, b.ID)
	feed2, _ := s.ListActivity(ctx, ActivityQuery{Limit: 500})
	if after.Attrs["duration_s"] != 99.0 || after.Attrs["sample_rate"] != 44100.0 || after.UpdatedAt != before.UpdatedAt || len(feed2) != len(feed) {
		t.Errorf("attrs %v updated %s -> %s feed %d -> %d", after.Attrs, before.UpdatedAt, after.UpdatedAt, len(feed), len(feed2))
	}

	// A thumbnail made by someone else (browser, user) is never replaced.
	c := mustAsset(t, s, AssetInput{Name: ptr("custom")}, "", nil)
	if err := s.SetThumbnail(ctx, c.ID, encodePNG(t, solid(32, 32, color.NRGBA{200, 10, 10, 255}))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFile(ctx, c.ID, "c.wav", bytes.NewReader(wav), 0); err != nil {
		t.Fatal(err)
	}
	c, _ = s.GetAsset(ctx, c.ID)
	if c.ThumbVersion != 1 {
		t.Errorf("custom thumbnail replaced: version %d", c.ThumbVersion)
	}
	if c.Attrs["duration_s"] != 0.5 { // but the attributes are still measured
		t.Errorf("attrs of asset with custom thumbnail = %v", c.Attrs)
	}

	// Files that merely carry a media extension are fine without preview.
	d := mustAsset(t, s, AssetInput{Name: ptr("fake")}, "fake.wav", []byte("RIFFfakewave"))
	if d.ThumbVersion != 0 || len(d.Files) != 1 {
		t.Errorf("fake wav: %+v", d)
	}
}

func TestFontSpecimen(t *testing.T) {
	s := newService(t)
	a := mustAsset(t, s, AssetInput{Name: ptr("go")}, "Go-Regular.ttf", goregular.TTF)
	p, mime := s.ThumbPath(a.ID)
	if p == "" || a.ThumbVersion == 0 {
		t.Fatal("no specimen made")
	}
	if mime != "image/jpeg" && mime != "image/png" {
		t.Errorf("mime %s", mime)
	}
	fh, _ := os.Open(p)
	img, _, err := image.Decode(fh)
	fh.Close()
	if err != nil || img.Bounds().Dx() != thumbSize {
		t.Fatalf("specimen: %v", err)
	}
	// text was drawn on all three lines
	rows := map[int]bool{}
	bg := img.At(2, 2)
	for y := 0; y < thumbSize; y++ {
		for x := 0; x < thumbSize; x += 3 {
			if img.At(x, y) != bg {
				rows[y/64] = true
				break
			}
		}
	}
	if len(rows) < 3 {
		t.Errorf("text found in %d horizontal bands only", len(rows))
	}
	// a broken font is not a problem
	b := mustAsset(t, s, AssetInput{Name: ptr("broken")}, "broken.ttf", append([]byte{0, 1, 0, 0}, bytes.Repeat([]byte{7}, 500)...))
	if b.ThumbVersion != 0 {
		t.Error("thumbnail from garbage")
	}
}

func TestBackfill(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	img := encodePNG(t, plasma(5, 128))
	a := mustAsset(t, s, AssetInput{Name: ptr("img")}, "i.png", img)
	w := mustAsset(t, s, AssetInput{Name: ptr("wav")}, "w.wav", wavBytes(1, 16, 1, 8000, 800, sine(0.5, 100, 8000)))
	// simulate an asset stored before the feature: no hash, colours, thumbnail
	if _, err := s.db.Exec(`UPDATE asset_files SET phash = ''`); err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`DELETE FROM asset_colors`)
	s.db.Exec(`UPDATE assets SET thumb_version = 0, attrs = '{}' WHERE id = ?`, w.ID)
	os.Remove(filepath.Join(s.DataDir(), "assets", w.ID, "thumb.png"))

	s.RunBackfill(ctx)
	var ph string
	s.db.QueryRow(`SELECT phash FROM asset_files WHERE asset_id = ?`, a.ID).Scan(&ph)
	if len(ph) != 16 {
		t.Errorf("phash after backfill = %q", ph)
	}
	if cols, _ := s.AssetColors(ctx, a.ID); len(cols) == 0 {
		t.Error("no colours after backfill")
	}
	w2, _ := s.GetAsset(ctx, w.ID)
	if w2.ThumbVersion == 0 || w2.Attrs["duration_s"] == nil {
		t.Errorf("audio not backfilled: %+v", w2)
	}
	// a cancelled context stops the work
	ctx2, cancel := context.WithCancel(ctx)
	cancel()
	s.RunBackfill(ctx2)
}

// ---- zip import ----

type zf struct {
	name string
	data string
}

func makeZip(t *testing.T, files ...zf) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pack.zip")
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(fh)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f.data))
	}
	zw.Close()
	fh.Close()
	return path
}

func TestImportModes(t *testing.T) {
	ctx := context.Background()
	files := []zf{
		{"LICENSE.txt", "Creative Commons Zero v1.0 Universal\nCC0 1.0\n"},
		{"readme.md", "# Nice Pack\nCreated by Jane Artist\n"},
		{"coin.png", string(encodePNG(t, plasma(1, 32)))},
		{"sprites/hero/idle.png", "idle"},
		{"sprites/hero/run.png", "run"},
		{"sprites/enemy.png", "enemy"},
		{"sounds/hit.wav", "hit"},
		{"__MACOSX/._coin.png", "junk"},
		{".DS_Store", "junk"},
		{"sprites/.hidden/x.png", "junk"},
		{"sprites/", ""},
	}

	t.Run("per_file", func(t *testing.T) {
		s := newService(t)
		res, err := s.ImportZip(ctx, makeZip(t, files...), "Kenney Pack.zip", ImportOptions{Tags: []string{"Imported"}}, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Assets) != 5 || res.Files != 5 || res.License != "CC0-1.0" || res.LicenseSource != "LICENSE.txt" || !strings.Contains(res.Attribution, "Jane Artist") {
			t.Fatalf("result = %+v", res)
		}
		if res.Skipped < 3 {
			t.Errorf("skipped %d, want the macOS junk, the dotfiles and the licence text", res.Skipped)
		}
		col, err := s.GetCollection(ctx, res.CollectionID)
		if err != nil || col.Kind != "pack" || col.Name != "Kenney Pack" || col.AssetCount != 5 {
			t.Fatalf("collection = %+v %v", col, err)
		}
		a, _ := s.GetAsset(ctx, res.Assets[0].ID)
		if a.License != "CC0-1.0" || !strings.Contains(a.Attribution, "Jane") || len(a.Tags) != 1 || a.Tags[0] != "imported" || a.Source == "" {
			t.Errorf("asset = %+v", a)
		}
	})

	t.Run("per_folder", func(t *testing.T) {
		s := newService(t)
		res, err := s.ImportZip(ctx, makeZip(t, files...), "pack.zip", ImportOptions{Mode: ImportPerFolder, CollectionName: "My UI", License: "MIT"}, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		// coin.png at the root (LICENSE.txt and readme.md are documents, not assets) + folders sprites and sounds
		if res.CollectionName != "My UI" || res.License != "MIT" || res.LicenseSource != "options" {
			t.Fatalf("result = %+v", res)
		}
		var sprites *ImportedAsset
		for i, a := range res.Assets {
			if a.Name == "sprites" {
				sprites = &res.Assets[i]
			}
		}
		if len(res.Assets) != 3 || sprites == nil || sprites.Files != 3 {
			t.Fatalf("assets = %+v", res.Assets)
		}
		a, _ := s.GetAsset(ctx, sprites.ID)
		names := map[string]bool{}
		for _, f := range a.Files {
			names[f.Filename] = true
		}
		if !names["hero_idle.png"] || !names["hero_run.png"] || !names["enemy.png"] {
			t.Errorf("files = %v", names)
		}
	})

	t.Run("single", func(t *testing.T) {
		s := newService(t)
		res, err := s.ImportZip(ctx, makeZip(t, files...), "pack.zip", ImportOptions{Mode: ImportSingle}, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Assets) != 1 || res.Assets[0].Name != "pack" || res.Files != 5 {
			t.Fatalf("result = %+v", res)
		}
		a, _ := s.GetAsset(ctx, res.Assets[0].ID)
		var got []string
		for _, f := range a.Files {
			got = append(got, f.Filename)
		}
		if !contains(got, "sprites_hero_idle.png") || contains(got, "LICENSE.txt") {
			t.Errorf("files = %v", got)
		}
	})

	t.Run("project and bad options", func(t *testing.T) {
		s := newService(t)
		if _, err := s.ImportZip(ctx, makeZip(t, files...), "p.zip", ImportOptions{Mode: "bogus"}, 0); err == nil {
			t.Error("unknown mode accepted")
		}
		if _, err := s.ImportZip(ctx, makeZip(t, files...), "p.zip", ImportOptions{ProjectID: "prj_nope"}, 0); err == nil {
			t.Error("unknown project accepted")
		}
		proj, err := s.SaveProject(ctx, "", GroupInput{Name: ptr("Game")})
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.ImportZip(ctx, makeZip(t, zf{"a.png", "x"}), "p.zip", ImportOptions{ProjectID: proj.ID}, 0)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := s.GetAsset(ctx, res.Assets[0].ID)
		if len(a.Projects) != 1 || a.Projects[0].ID != proj.ID {
			t.Errorf("projects = %+v", a.Projects)
		}
	})
}

func TestImportSafety(t *testing.T) {
	ctx := context.Background()

	t.Run("traversal names stay harmless", func(t *testing.T) {
		s := newService(t)
		outside := filepath.Join(filepath.Dir(s.DataDir()), "evil.png")
		res, err := s.ImportZip(ctx, makeZip(t,
			zf{"../../evil.png", "x"}, zf{"/etc/passwd", "root"}, zf{"..\\..\\win.dll", "w"}, zf{"a/../../b.txt", "b"}), "z.zip", ImportOptions{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(outside); err == nil {
			t.Error("file written outside the library")
		}
		for _, a := range res.Assets {
			full, _ := s.GetAsset(ctx, a.ID)
			for _, f := range full.Files {
				if strings.ContainsAny(f.Filename, `/\`) || strings.HasPrefix(f.Filename, ".") {
					t.Errorf("unsafe file name %q", f.Filename)
				}
				if _, err := os.Stat(filepath.Join(s.DataDir(), "assets", full.ID, "files", f.Filename)); err != nil {
					t.Errorf("file not inside the asset directory: %v", err)
				}
			}
		}
		if res.Files != 4 {
			t.Errorf("files = %d", res.Files)
		}
	})

	t.Run("zip bomb", func(t *testing.T) {
		s := newService(t)
		bomb := makeZip(t, zf{"zeros.bin", strings.Repeat("\x00", 40<<20)})
		_, err := s.ImportZip(ctx, bomb, "bomb.zip", ImportOptions{}, 1<<30)
		var inv *InvalidError
		if err == nil || !asInvalid(err, &inv) || !strings.Contains(err.Error(), "zip bomb") {
			t.Fatalf("bomb accepted or wrong error: %v", err)
		}
		if cols, _ := s.ListCollections(ctx); len(cols) != 0 {
			t.Error("collection created for a refused archive")
		}
		if res, _ := s.ListAssets(ctx, ListQuery{}); res.Total != 0 {
			t.Error("assets created for a refused archive")
		}
	})

	t.Run("size limit", func(t *testing.T) {
		s := newService(t)
		p := makeZip(t, zf{"a.bin", strings.Repeat("abcdefgh", 100)}, zf{"b.bin", strings.Repeat("ijklmnop", 100)})
		if _, err := s.ImportZip(ctx, p, "p.zip", ImportOptions{}, 1000); err == nil {
			t.Error("archive above the size limit accepted")
		}
		if _, err := s.ImportZip(ctx, p, "p.zip", ImportOptions{}, 5000); err != nil {
			t.Errorf("archive within the limit refused: %v", err)
		}
	})

	t.Run("too many entries", func(t *testing.T) {
		s := newService(t)
		path := filepath.Join(t.TempDir(), "many.zip")
		fh, _ := os.Create(path)
		zw := zip.NewWriter(fh)
		for i := 0; i <= ImportMaxEntries; i++ {
			w, _ := zw.Create(fmt.Sprintf("d%d/f%d.txt", i%50, i))
			w.Write([]byte("x"))
		}
		zw.Close()
		fh.Close()
		if _, err := s.ImportZip(ctx, path, "many.zip", ImportOptions{}, 0); err == nil {
			t.Error("archive with too many entries accepted")
		}
	})

	t.Run("not a zip", func(t *testing.T) {
		s := newService(t)
		p := filepath.Join(t.TempDir(), "x.zip")
		os.WriteFile(p, []byte("this is not a zip"), 0o600)
		if _, err := s.ImportZip(ctx, p, "x.zip", ImportOptions{}, 0); err == nil {
			t.Error("garbage accepted")
		}
		if _, err := s.ImportZip(ctx, makeZip(t, zf{".DS_Store", "x"}, zf{"d/", ""}), "x.zip", ImportOptions{}, 0); err == nil {
			t.Error("archive without files accepted")
		}
	})
}

func asInvalid(err error, target **InvalidError) bool {
	e, ok := err.(*InvalidError)
	if ok {
		*target = e
	}
	return ok
}
