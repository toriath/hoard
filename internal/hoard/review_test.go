package hoard

import (
	"bytes"
	"context"
	"encoding/binary"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// rawWAV builds a RIFF/WAVE file from explicit fmt and data parts, so that
// tests can lie in the header.
func rawWAV(fmtChunk []byte, dataSize uint32, data []byte, between ...[]byte) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF\x00\x00\x00\x00WAVE")
	if fmtChunk != nil {
		b.WriteString("fmt ")
		_ = binary.Write(&b, binary.LittleEndian, uint32(len(fmtChunk)))
		b.Write(fmtChunk)
		if len(fmtChunk)%2 == 1 {
			b.WriteByte(0)
		}
	}
	for _, x := range between {
		b.Write(x)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, dataSize)
	b.Write(data)
	return b.Bytes()
}

func pcmFmt(tag, ch, rate, block, bits int) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint16(b[0:], uint16(tag))
	binary.LittleEndian.PutUint16(b[2:], uint16(ch))
	binary.LittleEndian.PutUint32(b[4:], uint32(rate))
	binary.LittleEndian.PutUint32(b[8:], uint32(rate*block))
	binary.LittleEndian.PutUint16(b[12:], uint16(block))
	binary.LittleEndian.PutUint16(b[14:], uint16(bits))
	return b
}

func TestAnalyzeAudioMalformed(t *testing.T) {
	dir := t.TempDir()
	run := func(name string, data []byte) (*AudioInfo, error) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		var info *AudioInfo
		var err error
		go func() { defer close(done); info, err = AnalyzeAudio(context.Background(), p) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: analysis did not finish", name)
		}
		return info, err
	}
	megabyte := make([]byte, 1<<20)

	// A huge block alignment must neither allocate nor be trusted.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	info, err := run("bigblock.wav", rawWAV(pcmFmt(1, 1, 44100, 65535, 16), uint32(len(megabyte)), megabyte))
	runtime.ReadMemStats(&after)
	if err != nil || info.Measured || info.SampleRate != 44100 {
		t.Errorf("big block align: %v %+v", err, info)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 16<<20 {
		t.Errorf("a 1 MB file with block align 65535 allocated %d MB", grew>>20)
	}
	// A block align smaller than the samples it must hold is bogus too.
	if info, err = run("smallblock.wav", rawWAV(pcmFmt(1, 2, 44100, 2, 16), 1000, make([]byte, 1000))); err != nil || info.Measured {
		t.Errorf("small block align: %v %+v", err, info)
	}

	for name, data := range map[string][]byte{
		"empty.wav":         {},
		"riff-only.wav":     []byte("RIFF\x00\x00\x00\x00WAVE"),
		"fmt-truncated.wav": append([]byte("RIFF\x00\x00\x00\x00WAVEfmt \x10\x00\x00\x00"), 1, 0, 1),
		"fmt-tiny.wav":      rawWAV([]byte{1, 0}, 0, nil),
		"fmt-huge.wav":      append([]byte("RIFF\x00\x00\x00\x00WAVEfmt \xff\xff\xff\x7f"), make([]byte, 64)...),
		"no-fmt.wav":        rawWAV(nil, 4, []byte{0, 0, 0, 0}),
		"zero-rate.wav":     rawWAV(pcmFmt(1, 1, 0, 2, 16), 4, []byte{0, 0, 0, 0}),
		"zero-channels.wav": rawWAV(pcmFmt(1, 0, 44100, 2, 16), 4, []byte{0, 0, 0, 0}),
	} {
		if info, err := run(name, data); err == nil && info.Measured {
			t.Errorf("%s: measured garbage: %+v", name, info)
		}
	}
	// Chunk skipping is bounded.
	var junk [][]byte
	for range 200 {
		junk = append(junk, []byte("junk\x00\x00\x00\x00"))
	}
	if _, err := run("many-chunks.wav", rawWAV(pcmFmt(1, 1, 44100, 2, 16), 4, []byte{0, 0, 0, 0}, junk...)); err == nil {
		t.Error("200 chunks before the data were accepted")
	}
	// A few zero-size chunks are fine; a chunk that claims more than the file holds is not.
	if info, err = run("zero-chunks.wav", rawWAV(pcmFmt(1, 1, 44100, 2, 16), 4, []byte{0, 0, 0, 0}, []byte("junk\x00\x00\x00\x00"), []byte("JUNK\x00\x00\x00\x00"))); err != nil || !info.Measured {
		t.Errorf("zero-size chunks: %v %+v", err, info)
	}
	if _, err = run("chunk-too-big.wav", rawWAV(pcmFmt(1, 1, 44100, 2, 16), 4, nil, []byte("LIST\xff\xff\xff\x7f"))); err == nil {
		t.Error("chunk larger than the file accepted")
	}
	// 0xFFFFFFFF data size (streaming writers) is clamped to the file; truncated data measures what is there.
	frames := make([]byte, 2*4410)
	for i := range 4410 {
		binary.LittleEndian.PutUint16(frames[i*2:], uint16(int16(16384*math.Sin(float64(i)/10))))
	}
	if info, err = run("streaming.wav", rawWAV(pcmFmt(1, 1, 44100, 2, 16), 0xFFFFFFFF, frames)); err != nil || !info.Measured || info.DurationS > 0.11 {
		t.Errorf("0xFFFFFFFF data size: %v %+v", err, info)
	}
	if _, err = run("truncated-data.wav", rawWAV(pcmFmt(1, 1, 44100, 2, 16), 1<<20, frames[:101])); err != nil {
		t.Errorf("truncated data: %v", err)
	}
	// Odd-sized fmt chunk (padded) and WAVE_FORMAT_EXTENSIBLE.
	odd := append(pcmFmt(1, 1, 44100, 2, 16), 0)
	if info, err = run("odd-fmt.wav", rawWAV(odd, uint32(len(frames)), frames)); err != nil || !info.Measured {
		t.Errorf("odd fmt: %v %+v", err, info)
	}
	ext := append(pcmFmt(0xFFFE, 2, 48000, 6, 24), make([]byte, 24)...) // block 6 = 2 channels x 3 bytes
	binary.LittleEndian.PutUint16(ext[16:], 22)
	binary.LittleEndian.PutUint16(ext[24:], 1) // sub-format PCM
	if info, err = run("extensible.wav", rawWAV(ext, 600, make([]byte, 600))); err != nil || !info.Measured || info.Channels != 2 || info.BitDepth != 24 {
		t.Errorf("extensible: %v %+v", err, info)
	}
	// Short Ogg and FLAC.
	for name, data := range map[string][]byte{
		"short.ogg": []byte("OggS"), "short-vorbis.ogg": append([]byte("OggS\x00\x02"), "\x01vorbis\x00"...),
		"short.opus": append([]byte("OggS"), "OpusHead\x01"...), "short.flac": []byte("fLaC\x00\x00"), "empty.flac": {},
		"notflac.flac": make([]byte, 60),
	} {
		if _, err := run(name, data); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestLoudnessSumsChannels(t *testing.T) {
	dir := t.TempDir()
	measure := func(name string, ch int) float64 {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, fitWavBytes(48000, ch, 16, 48000*2, 0.5), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := AnalyzeAudio(context.Background(), p)
		if err != nil || !info.Measured {
			t.Fatal(err, info)
		}
		return info.LoudnessApprox
	}
	mono, stereo := measure("m.wav", 1), measure("s.wav", 2)
	// A 0.5 sine has a mean square of 0.125: -9.03 dB, minus 0.691.
	if math.Abs(mono-(-9.03-0.691)) > 0.1 {
		t.Errorf("mono loudness = %.2f, want about -9.72", mono)
	}
	// The same level in two channels carries twice the energy: +3.01 dB.
	if math.Abs((stereo-mono)-3.01) > 0.05 {
		t.Errorf("stereo - mono = %.2f dB, want 3.01", stereo-mono)
	}
}

func mustBrief(t *testing.T, s *Service, id string) *ProjectBrief {
	t.Helper()
	b, err := s.GetProjectBrief(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReportNeverMeasuresOrWrites(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	p := newProject(t, s, "Pageview")
	if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"palette":["#000000"],"audio":{"sample_rate":48000}}`)); err != nil {
		t.Fatal(err)
	}
	img := mustAsset(t, s, AssetInput{Name: ptr("img"), Type: ptr("sprite"), License: ptr("CC0-1.0"), AddProjects: []string{p.ID}}, "img.png", paletteImage(t, 8, 8, color.NRGBA{255, 0, 0, 255}))
	snd := mustAsset(t, s, AssetInput{Name: ptr("snd"), Type: ptr("sfx"), License: ptr("CC0-1.0"), AddProjects: []string{p.ID}}, "snd.wav", fitWavBytes(44100, 1, 16, 4410, 0.5))
	forgetMeasurements(t, s, snd.ID) // the upload measured it; this test is about an unmeasured one
	// If anything tried to read the files, it would fail or fill the caches.
	for _, a := range []*Asset{img, snd} {
		if err := os.Remove(filepath.Join(s.assetDir(a.ID), "files", a.Files[0].Filename)); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		measureCache.Lock()
		defer measureCache.Unlock()
		return len(measureCache.palette) + len(measureCache.audio)
	}
	n := count()
	before, _ := s.GetAsset(ctx, snd.ID)

	r, err := s.ProjectReport(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProjectCredits(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	for _, f := range r.FitIssues {
		t.Errorf("report produced a measurement finding: %+v", f)
	}
	if count() != n {
		t.Error("the report filled the measurement caches")
	}
	after, _ := s.GetAsset(ctx, snd.ID)
	if after.UpdatedAt != before.UpdatedAt || len(after.Attrs) != len(before.Attrs) {
		t.Errorf("the report wrote attrs: %+v", after.Attrs)
	}
	// Unmeasured audio is said so, rather than guessed.
	issues, err := s.checkFit(ctx, after, mustBrief(t, s, p.ID), false)
	if err != nil || codes(issues)["audio_unmeasured"].Code == "" {
		t.Errorf("unmeasured audio: %v %+v", err, issues)
	}
	// With stored attrs the report judges them without reading the file.
	if _, err := s.UpdateAsset(ctx, snd.ID, AssetInput{Attrs: map[string]any{"sample_rate": 44100, "channels": 1}}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.ProjectReport(ctx, p.ID)
	if len(r.FitIssues) != 1 || r.FitIssues[0].Asset.ID != snd.ID || !strings.Contains(r.FitIssues[0].Messages[0], "44100 Hz, project wants 48000 Hz") {
		t.Errorf("report from attrs: %+v", r.FitIssues)
	}
}

func TestStoredMeasurementsFollowTheFile(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	p := newProject(t, s, "Remeasure")
	if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"audio":{"sample_rate":48000}}`)); err != nil {
		t.Fatal(err)
	}
	a := mustAsset(t, s, AssetInput{Name: ptr("snd"), Type: ptr("sfx"), License: ptr("CC0-1.0")}, "snd.wav", fitWavBytes(44100, 1, 16, 4410, 0.5))
	if _, err := s.CheckFit(ctx, a.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAsset(ctx, a.ID)
	if got.Attrs["sample_rate"] != 44100.0 || got.Attrs["measured_sha256"] != got.Files[0].SHA256 || got.Attrs["loudness_approx_db"] == nil {
		t.Fatalf("first measurement: %+v", got.Attrs)
	}
	// The file is replaced: the measurement follows it.
	if _, err := s.AddFile(ctx, a.ID, "snd.wav", bytes.NewReader(fitWavBytes(22050, 2, 16, 2205, 0.25)), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckFit(ctx, a.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAsset(ctx, a.ID)
	if got.Attrs["sample_rate"] != 22050.0 || got.Attrs["channels"] != 2.0 || got.Attrs["measured_sha256"] != got.Files[0].SHA256 {
		t.Errorf("re-measurement: %+v", got.Attrs)
	}
	// A trashed asset is not touched.
	b := mustAsset(t, s, AssetInput{Name: ptr("gone"), Type: ptr("sfx"), License: ptr("CC0-1.0")}, "gone.wav", fitWavBytes(44100, 1, 16, 4410, 0.5))
	forgetMeasurements(t, s, b.ID)
	if _, err := s.Bulk(ctx, BulkOp{IDs: []string{b.ID}, Action: "trash"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckFit(ctx, b.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetAsset(ctx, b.ID); got.Attrs["sample_rate"] != nil {
		t.Errorf("trashed asset measured: %+v", got.Attrs)
	}
}

func TestFitSmallThings(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	p := newProject(t, s, "Small")
	// tile_size alone (no pixel_art) is checked.
	if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"tile_size":16,"audio":{"sample_rate":44100}}`)); err != nil {
		t.Fatal(err)
	}
	ts := mustAsset(t, s, AssetInput{Name: ptr("ts"), Type: ptr("tileset"), License: ptr("CC0-1.0")}, "ts.png", pngBytes(t, 20, 20, color.Black))
	is, _ := s.CheckFit(ctx, ts.ID, p.ID)
	if codes(is)["tile_size"].Severity != "warning" {
		t.Errorf("tile size without pixel_art: %+v", is)
	}
	// Opus is always 48 kHz: no sample-rate warning from its header, an info at most.
	ogg := append([]byte("OggS\x00\x02"), make([]byte, 20)...)
	ogg = append(ogg, 0x01)
	ogg = append(ogg, []byte("OpusHead\x01\x02\x00\x00\x44\xac\x00\x00")...)
	ogg = append(ogg, make([]byte, 20)...)
	op := mustAsset(t, s, AssetInput{Name: ptr("op"), Type: ptr("sfx"), License: ptr("CC0-1.0")}, "op.opus", ogg)
	is, _ = s.CheckFit(ctx, op.ID, p.ID)
	if c := codes(is)["sample_rate"]; c.Severity == "warning" || c.Severity == "error" {
		t.Errorf("opus sample rate: %+v", c)
	}
	// Rejected assets do not count towards the size.
	small := pngBytes(t, 8, 8, color.White)
	mustAsset(t, s, AssetInput{Name: ptr("rj"), Type: ptr("sprite"), License: ptr("CC0-1.0"), Status: ptr("rejected"), AddProjects: []string{p.ID}}, "rj.png", pngBytes(t, 8, 8, color.Black))
	mustAsset(t, s, AssetInput{Name: ptr("ok"), Type: ptr("sprite"), License: ptr("CC0-1.0"), AddProjects: []string{p.ID}}, "ok.png", small)
	if r, _ := s.ProjectReport(ctx, p.ID); r.TotalSize != int64(len(small)) {
		t.Errorf("size = %d, want only the accepted asset (%d)", r.TotalSize, len(small))
	}
	// "Unknown" is flagged by the report and the credits alike.
	mustAsset(t, s, AssetInput{Name: ptr("mystery"), Type: ptr("sprite"), License: ptr("Unknown"), AddProjects: []string{p.ID}}, "m.png", small)
	r, _ := s.ProjectReport(ctx, p.ID)
	cr, _ := s.ProjectCredits(ctx, p.ID)
	found, attn := false, false
	for _, l := range r.LicenseProblems {
		found = found || l.Name == "mystery"
	}
	for _, i := range cr.NeedsAttention {
		attn = attn || i.Name == "mystery"
	}
	if !found || !attn || !strings.Contains(cr.Markdown, "## No license recorded") {
		t.Errorf("unknown license: report=%v credits=%v", found, attn)
	}
}

func TestBriefPatchKeysAndNulls(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	p := newProject(t, s, "Keys")
	for _, js := range []string{`{"Engine":"godot"}`, `{"tileSize":16}`, `{"palette ":[]}`, `{"nope":1}`} {
		_, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, js))
		if err == nil || !strings.Contains(err.Error(), "unknown brief field") || !strings.Contains(err.Error(), "tile_size") {
			t.Errorf("%s: %v", js, err)
		}
	}
	if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"formats":{"sprite":["png"],"sfx":["wav"]},"folders":{"sprite":"a","default":"b"},"audio":{"sample_rate":48000,"channels":2}}`)); err != nil {
		t.Fatal(err)
	}
	b, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"formats":{"sprite":null,"sfx":["wav"]},"folders":{"sprite":null,"default":"b"},"audio":{"sample_rate":null,"channels":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Formats["sprite"]; ok || len(b.Formats["sfx"]) != 1 || len(b.Folders) != 1 || b.Audio == nil || b.Audio.SampleRate != nil || *b.Audio.Channels != 2 {
		t.Errorf("nested nulls: %+v", b)
	}
	b, err = s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"audio":{"channels":null},"formats":{"sfx":null},"folders":{"default":null}}`))
	if err != nil || !b.IsEmpty() {
		t.Errorf("all nested nulls should leave an empty brief: %v %+v", err, b)
	}
	for _, dir := range []string{".", "a/./b", ".git", "x/.GIT/hooks", "a/.git"} {
		if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"folders":{"default":"`+dir+`"}}`)); err == nil {
			t.Errorf("folder %q accepted", dir)
		}
	}
	if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"folders":{"default":".github/assets"}}`)); err != nil {
		t.Errorf(".github is fine: %v", err)
	}
}

func TestMdEscapeNeutralisesLinks(t *testing.T) {
	for _, in := range []string{
		"http://evil.example/x", "https://evil.example", "www.evil.example", "WWW.evil.example", "mail me: eve@evil.example",
		"[ref]: http://evil.example", "see &amp; &lt;b&gt;", `back\slash [x](y)`, "javascript:alert(1)",
	} {
		// What remains after removing backslash-escaped characters is what a renderer acts on.
		out := regexp.MustCompile(`\\.`).ReplaceAllString(mdEscape(in), "_")
		for _, bad := range []string{"http:", "https:", "www.", "WWW.", "@", "javascript:", "[ref]", "&amp;", "&lt;"} {
			// An escaped form such as "\:" or "\@" does not contain the bare sequence.
			if strings.Contains(out, bad) {
				t.Errorf("mdEscape(%q) = %q still contains %q", in, out, bad)
			}
		}
	}
	if got := mdEscape(`a\b &amp; c`); got != `a\\b \&amp; c` {
		t.Errorf("backslash and entity: %q", got)
	}
	// Bidi controls and zero-width characters are removed.
	if got := mdEscape("a\u202Eb\u200Bc\u2066d\uFEFFe"); got != "abcde" {
		t.Errorf("invisible characters kept: %q", got)
	}
	if got := mdEscape("see www.example.com"); got != `see www\.example.com` {
		t.Errorf("www: %q", got)
	}
}

// forgetMeasurements drops the attributes an upload measured, so a test can
// start from an asset hoard knows nothing about.
func forgetMeasurements(t *testing.T, s *Service, id string) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE assets SET attrs = '{}' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
}
