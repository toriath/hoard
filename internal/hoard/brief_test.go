package hoard

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rawPatch(t *testing.T, js string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func newProject(t *testing.T, s *Service, name string) *Project {
	t.Helper()
	p, err := s.SaveProject(context.Background(), "", GroupInput{Name: ptr(name)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBriefValidationAndMerge(t *testing.T) {
	s := newService(t)
	ctx := WithActor(context.Background(), Actor{Username: "alice", Via: "mcp", TokenName: "t"})
	p := newProject(t, s, "Game")

	bad := []string{
		`{"engine":"cryengine"}`,
		`{"palette":["red"]}`,
		`{"palette":["#12345"]}`,
		`{"tile_size":0}`,
		`{"tile_size":-4}`,
		`{"max_texture_size":99999999}`,
		`{"base_resolution":"wide"}`,
		`{"formats":{"sprite":[".png"]}}`,
		`{"formats":{"sprite":["PNG"]}}`,
		`{"formats":{"gizmo":["png"]}}`,
		`{"folders":{"sprite":"/etc"}}`,
		`{"folders":{"sprite":"../outside"}}`,
		`{"folders":{"sprite":"a/../../b"}}`,
		`{"folders":{"sprite":"C:\\x"}}`,
		`{"folders":{"nonsense":"assets"}}`,
		`{"naming":"Title Case"}`,
		`{"size_budget_mb":0}`,
		`{"audio":{"sample_rate":5}}`,
		`{"audio":{"channels":0}}`,
		`{"audio":{"max_peak_dbfs":3}}`,
		`{"unknown_field":1}`,
		`{"tile_size":"16"}`,
	}
	for _, js := range bad {
		if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, js)); err == nil {
			t.Errorf("accepted %s", js)
		} else if _, ok := err.(*InvalidError); !ok {
			t.Errorf("%s: want InvalidError, got %T %v", js, err, err)
		}
	}
	if got, _ := s.GetProjectBrief(ctx, p.ID); !got.IsEmpty() {
		t.Fatalf("a rejected patch changed the brief: %+v", got)
	}
	if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"art_direction":"`+strings.Repeat("x", 9000)+`"}`)); err == nil {
		t.Error("huge art_direction accepted")
	}
	if _, err := s.SaveProjectBrief(ctx, "prj_missing", rawPatch(t, `{}`)); err == nil {
		t.Error("unknown project accepted")
	}

	b, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"engine":"godot","pixel_art":true,"palette":["#ABC","#1A1C2C"],"tile_size":16,
		"formats":{"sprite":["png"]},"folders":{"sprite":"assets/sprites"},"audio":{"sample_rate":48000,"channels":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if b.Palette[0] != "#aabbcc" || b.Palette[1] != "#1a1c2c" {
		t.Errorf("palette not normalised: %v", b.Palette)
	}
	// Fields given replace; others stay; null clears.
	b, err = s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"notes":"hello","palette":["#000000"],"tile_size":null,"audio":{"channels":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if b.Engine != "godot" || !b.PixelArt || b.Notes != "hello" || len(b.Palette) != 1 || b.TileSize != nil {
		t.Errorf("merge result: %+v", b)
	}
	if b.Audio == nil || b.Audio.SampleRate != nil || *b.Audio.Channels != 1 {
		t.Errorf("nested object is replaced as a whole: %+v", b.Audio)
	}
	if b.Formats["sprite"][0] != "png" || b.Folders["sprite"] != "assets/sprites" {
		t.Errorf("untouched fields lost: %+v", b)
	}
	// Clearing everything yields an empty brief.
	b, err = s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"engine":null,"pixel_art":null,"palette":null,"notes":null,"audio":null,"formats":null,"folders":null}`))
	if err != nil || !b.IsEmpty() {
		t.Fatalf("clear: %v %+v", err, b)
	}

	// The brief is on the project, and changes are recorded.
	if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"engine":"unity"}`)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProject(ctx, p.ID)
	if err != nil || !strings.Contains(string(got.Brief), `"engine":"unity"`) {
		t.Fatalf("project brief: %v %s", err, got.Brief)
	}
	feed, _ := s.ListActivity(ctx, ActivityQuery{Action: "project.brief"})
	if len(feed) == 0 || feed[0].RefID != p.ID || feed[0].Via != "mcp" || !strings.Contains(feed[0].Summary, "engine") {
		t.Errorf("activity: %+v", feed)
	}
	n := len(feed)
	if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{}`)); err != nil {
		t.Fatal(err)
	}
	if feed, _ = s.ListActivity(ctx, ActivityQuery{Action: "project.brief"}); len(feed) != n {
		t.Error("a no-op patch was recorded")
	}

}

// wavBytes builds a PCM WAV file with a constant-amplitude sine.
func wavBytes(rate, channels, bits, frames int, amp float64) []byte {
	block := channels * bits / 8
	var data bytes.Buffer
	for i := range frames {
		v := amp * math.Sin(2*math.Pi*440*float64(i)/float64(rate))
		for range channels {
			switch bits {
			case 8:
				data.WriteByte(byte(128 + v*127))
			case 16:
				_ = binary.Write(&data, binary.LittleEndian, int16(v*32767))
			case 24:
				x := int32(v * 8388607)
				data.Write([]byte{byte(x), byte(x >> 8), byte(x >> 16)})
			}
		}
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+data.Len()))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*block))
	_ = binary.Write(&b, binary.LittleEndian, uint16(block))
	_ = binary.Write(&b, binary.LittleEndian, uint16(bits))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(data.Len()))
	b.Write(data.Bytes())
	return b.Bytes()
}

func paletteImage(t *testing.T, w, h int, cols ...color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, cols[(x+y)%len(cols)])
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func codes(is []FitIssue) map[string]FitIssue {
	m := map[string]FitIssue{}
	for _, i := range is {
		m[i.Code] = i
	}
	return m
}

func TestFitImage(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	p := newProject(t, s, "Pixel game")

	empty, err := s.CheckFit(ctx, mustAsset(t, s, AssetInput{Name: ptr("x")}, "", nil).ID, p.ID)
	if err != nil || len(empty) != 1 || empty[0].Code != "no_brief" || empty[0].Severity != "info" {
		t.Fatalf("empty brief: %v %+v", err, empty)
	}

	if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"pixel_art":true,"palette":["#000000","#ffffff"],"tile_size":16,"max_texture_size":64,
		"power_of_two":true,"formats":{"sprite":["png"],"tileset":["png"]},"licenses":["CC0-1.0","CC-BY-4.0"],"size_budget_mb":1,"naming":"snake_case"}`)); err != nil {
		t.Fatal(err)
	}
	black, white := color.NRGBA{0, 0, 0, 255}, color.NRGBA{255, 255, 255, 255}

	// Fits: on palette, power of two, small.
	good := mustAsset(t, s, AssetInput{Name: ptr("hero"), Type: ptr("tileset"), License: ptr("CC0-1.0")}, "hero_sheet.png", paletteImage(t, 32, 32, black, white))
	is, err := s.CheckFit(ctx, good.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if e, w := countIssues(is); e != 0 || w != 0 {
		t.Fatalf("clean asset has findings: %+v", is)
	}

	// Doesn't fit: 100x100 > 64, not pow2, not a multiple of 16, off palette, CC-BY w/o attribution, wrong licence.
	bad := mustAsset(t, s, AssetInput{Name: ptr("Bad Tiles"), Type: ptr("tileset"), License: ptr("CC-BY-NC-4.0")},
		"Bad Tiles.png", paletteImage(t, 100, 100, color.NRGBA{200, 30, 30, 255}, white))
	is, _ = s.CheckFit(ctx, bad.ID, p.ID)
	c := codes(is)
	for _, want := range []string{"max_texture_size", "power_of_two", "tile_size", "palette", "license_not_allowed", "attribution_missing", "naming"} {
		if _, ok := c[want]; !ok {
			t.Errorf("missing finding %s in %+v", want, is)
		}
	}
	if !strings.Contains(c["max_texture_size"].Message, "100x100 px") || !strings.Contains(c["max_texture_size"].Message, "64") {
		t.Errorf("message not concrete: %q", c["max_texture_size"].Message)
	}
	if c["palette"].Severity != "error" || !strings.Contains(c["palette"].Message, "50.0%") {
		t.Errorf("palette finding: %+v", c["palette"])
	}
	if is[0].Severity != "error" {
		t.Errorf("errors must come first: %+v", is)
	}

	// Wrong format and status.
	jpg := mustAsset(t, s, AssetInput{Name: ptr("photo"), Type: ptr("sprite"), License: ptr("CC0-1.0"), Status: ptr("placeholder")}, "photo.bmp", pngBytes(t, 8, 8, black))
	is, _ = s.CheckFit(ctx, jpg.ID, p.ID)
	c = codes(is)
	if !strings.Contains(c["format"].Message, ".bmp") || !strings.Contains(c["format"].Message, "png") {
		t.Errorf("format: %+v", c["format"])
	}
	if c["status_placeholder"].Severity != "warning" {
		t.Errorf("placeholder: %+v", is)
	}

	// An image that is mostly on the palette but for a few pixels: a warning with the share.
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			img.SetNRGBA(x, y, black)
		}
	}
	for x := range 40 {
		img.SetNRGBA(x, 0, color.NRGBA{255, 0, 0, 255})
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	near := mustAsset(t, s, AssetInput{Name: ptr("near"), Type: ptr("sprite"), License: ptr("CC0-1.0")}, "near.png", buf.Bytes())
	is, _ = s.CheckFit(ctx, near.ID, p.ID)
	if c = codes(is); c["palette"].Severity != "warning" || !strings.Contains(c["palette"].Message, "1.0%") {
		t.Errorf("small palette deviation: %+v", is)
	}

	// Transparent pixels are not counted.
	tr := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	tr.SetNRGBA(0, 0, black)
	buf.Reset()
	_ = png.Encode(&buf, tr)
	t1 := mustAsset(t, s, AssetInput{Name: ptr("t1"), Type: ptr("sprite"), License: ptr("CC0-1.0")}, "t1.png", buf.Bytes())
	if is, _ = s.CheckFit(ctx, t1.ID, p.ID); codes(is)["palette"].Code != "" {
		t.Errorf("transparent pixels counted: %+v", is)
	}

	// The report reads the same findings, without double counting licence/status.
	for _, a := range []*Asset{good, bad, jpg} {
		if _, err := s.UpdateAsset(ctx, a.ID, AssetInput{AddProjects: []string{p.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.ProjectReport(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "problems" || r.AssetCount != 3 || len(r.LicenseProblems) != 1 || len(r.Placeholders) != 1 || len(r.MissingAttribution) != 1 {
		t.Errorf("report: %+v", r)
	}
	if len(r.FitIssues) != 2 || r.FitIssues[0].Asset.ID != bad.ID || r.FitIssues[0].Errors < 1 {
		t.Errorf("report fit issues: %+v", r.FitIssues)
	}
	if r.RepoDrift != nil {
		t.Errorf("drift without snapshot: %+v", r.RepoDrift)
	}
	if !strings.Contains(r.Summary, "license") {
		t.Errorf("summary: %s", r.Summary)
	}
}

func TestFitAudio(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	p := newProject(t, s, "Audio game")
	if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"audio":{"sample_rate":48000,"channels":2,"loudness_lufs":-16,"max_peak_dbfs":-1},"formats":{"sfx":["wav","ogg"]}}`)); err != nil {
		t.Fatal(err)
	}
	// 1 s of a half-scale sine at 44.1 kHz mono: peak about -6 dBFS, RMS about -9 dBFS.
	snd := mustAsset(t, s, AssetInput{Name: ptr("beep"), Type: ptr("sfx"), License: ptr("CC0-1.0")}, "beep.wav", wavBytes(44100, 1, 16, 44100, 0.5))
	updated := snd.UpdatedAt
	is, err := s.CheckFit(ctx, snd.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := codes(is)
	if c["sample_rate"].Message != "44100 Hz, project wants 48000 Hz." {
		t.Errorf("sample rate message: %q", c["sample_rate"].Message)
	}
	if c["channels"].Severity != "info" {
		t.Errorf("mono in a stereo project should be info: %+v", c["channels"])
	}
	if _, ok := c["peak"]; ok {
		t.Errorf("-6 dBFS peak is within -1: %+v", c["peak"])
	}
	if c["loudness"].Severity != "info" || !strings.Contains(c["loudness"].Message, "not true LUFS") {
		t.Errorf("loudness (sfx): %+v", c["loudness"])
	}

	// Measured values are stored once, without touching updated_at or the feed.
	after, _ := s.GetAsset(ctx, snd.ID)
	if after.UpdatedAt != updated {
		t.Error("measuring bumped updated_at")
	}
	if after.Attrs["sample_rate"] != 44100.0 || after.Attrs["channels"] != 1.0 || after.Attrs["bit_depth"] != 16.0 {
		t.Errorf("attrs: %+v", after.Attrs)
	}
	if d, _ := after.Attrs["duration_s"].(float64); math.Abs(d-1) > 0.001 {
		t.Errorf("duration: %v", after.Attrs["duration_s"])
	}
	if pk, _ := after.Attrs["peak_dbfs"].(float64); math.Abs(pk+6) > 0.3 {
		t.Errorf("peak: %v", after.Attrs["peak_dbfs"])
	}
	feed, _ := s.ListActivity(ctx, ActivityQuery{AssetID: snd.ID})
	for _, e := range feed {
		if e.Action == "asset.update" {
			t.Errorf("measuring recorded an update: %+v", e)
		}
	}
	side, _ := os.ReadFile(filepath.Join(s.DataDir(), "assets", snd.ID, "asset.json"))
	if !strings.Contains(string(side), "peak_dbfs") {
		t.Error("sidecar lacks the measured attrs")
	}
	// Existing values are never overwritten.
	if _, err := s.UpdateAsset(ctx, snd.ID, AssetInput{Attrs: map[string]any{"sample_rate": 22050}}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.CheckFit(ctx, snd.ID, p.ID)
	after, _ = s.GetAsset(ctx, snd.ID)
	if after.Attrs["sample_rate"] != 22050.0 {
		t.Errorf("attrs overwritten: %+v", after.Attrs)
	}

	// Loud and clipping music at the right sample rate: peak and loudness warnings.
	loud := mustAsset(t, s, AssetInput{Name: ptr("loud"), Type: ptr("music"), License: ptr("CC0-1.0")}, "loud.wav", wavBytes(48000, 2, 16, 48000, 1.0))
	is, _ = s.CheckFit(ctx, loud.ID, p.ID)
	c = codes(is)
	if c["peak"].Severity != "warning" || !strings.Contains(c["peak"].Message, "clips") {
		t.Errorf("peak: %+v", c["peak"])
	}
	if c["loudness"].Severity != "warning" || !strings.Contains(c["loudness"].Message, "above") {
		t.Errorf("loudness: %+v", c["loudness"])
	}
	if _, ok := c["sample_rate"]; ok {
		t.Errorf("sample rate matches: %+v", c["sample_rate"])
	}
	// Format restriction only lists wav and ogg for sfx; music has none.
	if _, ok := c["format"]; ok {
		t.Errorf("unexpected format finding: %+v", c["format"])
	}

	// A silent file has no loudness to compare; a truncated one does not crash.
	silent := mustAsset(t, s, AssetInput{Name: ptr("silence"), Type: ptr("music"), License: ptr("CC0-1.0")}, "silence.wav", wavBytes(48000, 2, 16, 48000, 0))
	is, _ = s.CheckFit(ctx, silent.ID, p.ID)
	if _, ok := codes(is)["loudness"]; ok {
		t.Errorf("loudness of silence: %+v", is)
	}
	broken := mustAsset(t, s, AssetInput{Name: ptr("broken"), Type: ptr("sfx"), License: ptr("CC0-1.0")}, "broken.wav", []byte("RIFF....WAVEfmt "))
	if is, _ = s.CheckFit(ctx, broken.ID, p.ID); codes(is)["audio_unchecked"].Severity != "info" {
		t.Errorf("broken wav: %+v", is)
	}
	mp3 := mustAsset(t, s, AssetInput{Name: ptr("song"), Type: ptr("music"), License: ptr("CC0-1.0")}, "song.mp3", []byte("ID3 not really"))
	if is, _ = s.CheckFit(ctx, mp3.ID, p.ID); codes(is)["audio_unchecked"].Code == "" {
		t.Errorf("mp3 should be reported as not checked: %+v", is)
	}
}

func TestAudioHeaders(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// 24-bit stereo with an odd-sized chunk before data.
	w := wavBytes(96000, 2, 24, 960, 0.25)
	w = append(append([]byte{}, w[:36]...), append([]byte("LIST\x03\x00\x00\x00abc\x00"), w[36:]...)...)
	info, err := AnalyzeAudio(context.Background(), write("a.wav", w))
	if err != nil || info.SampleRate != 96000 || info.Channels != 2 || info.BitDepth != 24 || !info.Measured || math.Abs(info.PeakDBFS+12) > 0.2 {
		t.Errorf("wav24: %v %+v", err, info)
	}
	// Vorbis identification header inside an Ogg page.
	ogg := append([]byte("OggS\x00\x02"), make([]byte, 20)...)
	ogg = append(ogg, 0x01)
	ogg = append(ogg, []byte("\x01vorbis")...)
	ogg = append(ogg, 0, 0, 0, 0, 2, 0x44, 0xAC, 0, 0) // version 0, 2 channels, 44100 Hz
	ogg = append(ogg, make([]byte, 20)...)
	info, err = AnalyzeAudio(context.Background(), write("a.ogg", ogg))
	if err != nil || info.SampleRate != 44100 || info.Channels != 2 {
		t.Errorf("ogg: %v %+v", err, info)
	}
	// FLAC STREAMINFO: 48 kHz, 2 channels, 16 bit, 96000 samples.
	fl := []byte("fLaC\x00\x00\x00\x22")
	si := make([]byte, 34)
	rate := uint32(48000)
	si[10], si[11], si[12] = byte(rate>>12), byte(rate>>4), byte(rate<<4)|1<<1 // channels-1 = 1
	si[12] |= 0                                                                // bit 4 of bps-1 (15) = 0
	si[13] = 15 << 4
	binary.BigEndian.PutUint32(si[14:], 96000)
	info, err = AnalyzeAudio(context.Background(), write("a.flac", append(fl, si...)))
	if err != nil || info.SampleRate != 48000 || info.Channels != 2 || info.BitDepth != 16 || math.Abs(info.DurationS-2) > 1e-6 {
		t.Errorf("flac: %v %+v", err, info)
	}
	if _, err := AnalyzeAudio(context.Background(), write("x.wav", []byte("nope"))); err == nil {
		t.Error("garbage accepted as wav")
	}
}

func TestReportCreditsAndDrift(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	p := newProject(t, s, "Report *game*")
	if _, err := s.SaveProjectBrief(ctx, p.ID, rawPatch(t, `{"licenses":["CC0-1.0","CC-BY-4.0"],"size_budget_mb":0.0001}`)); err != nil {
		t.Fatal(err)
	}
	evil := "<script>alert(1)</script> [x](javascript:alert(1)) **bold** `code`\n# heading | pipe"
	a1 := mustAsset(t, s, AssetInput{Name: ptr(evil), Type: ptr("sprite"), License: ptr("CC-BY-4.0"), Attribution: ptr("by <b>Eve</b> [me](http://evil)"),
		Source: ptr("Kenney *pack*"), SourceURL: ptr("https://example.com/a)[b](c"), AddProjects: []string{p.ID}}, "a.png", pngBytes(t, 16, 16, color.Black))
	a2 := mustAsset(t, s, AssetInput{Name: ptr("plain"), Type: ptr("sprite"), License: ptr("CC-BY-4.0"), Source: ptr("itch"),
		SourceURL: ptr("https://example.com/a?b=c"), AddProjects: []string{p.ID}}, "b.png", pngBytes(t, 16, 16, color.White))
	a3 := mustAsset(t, s, AssetInput{Name: ptr("nolic"), Type: ptr("sprite"), AddProjects: []string{p.ID}, Status: ptr("rework")}, "c.png", pngBytes(t, 8, 8, color.White))
	gone := mustAsset(t, s, AssetInput{Name: ptr("old"), Type: ptr("sprite"), License: ptr("MIT"), AddProjects: []string{p.ID}, Status: ptr("superseded")}, "d.png", pngBytes(t, 8, 8, color.White))
	_ = gone

	cr, err := s.ProjectCredits(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	md := cr.Markdown
	if cr.AssetCount != 3 || len(cr.NeedsAttention) != 2 {
		t.Errorf("credits counts: %d %+v", cr.AssetCount, cr.NeedsAttention)
	}
	for _, bad := range []string{"<script>", "<b>", "**bold**", "[x](", "[me](", "\n# heading", "<javascript"} {
		if strings.Contains(md, bad) {
			t.Errorf("unescaped %q in:\n%s", bad, md)
		}
	}
	for _, want := range []string{"## CC-BY-4.0", "<https://spdx.org/licenses/CC-BY-4.0.html>", "<https://example.com/a?b=c>", "## No license recorded", "## Needs attention", "Report \\*game\\*"} {
		if !strings.Contains(md, want) {
			t.Errorf("credits lack %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "**old**") {
		t.Error("superseded asset credited")
	}
	_ = a2

	// Report: licence problems (no licence), missing attribution, rework, budget.
	r, err := s.ProjectReport(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "problems" || !r.OverBudget || len(r.LicenseProblems) != 1 || r.LicenseProblems[0].ID != a3.ID ||
		len(r.MissingAttribution) != 1 || len(r.NeedsWork) != 2 || r.SizeBudgetBytes != 100 {
		t.Errorf("report: %+v", r)
	}

	// Open requests are counted read-only.
	if _, err := s.db.Exec(`INSERT INTO asset_requests(id, project_id, title, status, created_at, updated_at) VALUES
		('req_1', ?, 'coin', 'open', 'x', 'x'), ('req_2', ?, 'gem', 'submitted', 'x', 'x'), ('req_3', ?, 'old', 'done', 'x', 'x')`, p.ID, p.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = s.ProjectReport(ctx, p.ID)
	if r.Requests.Open != 1 || r.Requests.Submitted != 1 || r.Requests.Total != 2 {
		t.Errorf("requests: %+v", r.Requests)
	}

	// Repo drift only exists with a snapshot; a1 is in it (by hash), a2 is not.
	a1full, _ := s.GetAsset(ctx, a1.ID)
	if _, err := s.db.Exec(`INSERT INTO project_files(project_id, path, sha256, size, asset_id, seen_at) VALUES(?, 'assets/a.png', ?, 1, NULL, '2026-01-02T00:00:00Z')`,
		p.ID, a1full.Files[0].SHA256); err != nil {
		t.Fatal(err)
	}
	r, _ = s.ProjectReport(ctx, p.ID)
	if r.RepoDrift == nil || r.RepoDrift.Files != 1 || r.RepoDrift.SnapshotAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("drift: %+v", r.RepoDrift)
	}
	names := map[string]bool{}
	for _, m := range r.RepoDrift.MissingAssets {
		names[m.Name] = true
	}
	if names[evil] || !names["plain"] || !names["nolic"] {
		t.Errorf("missing assets: %+v", r.RepoDrift.MissingAssets)
	}

	// A project with only good assets is ok.
	q := newProject(t, s, "Clean")
	mustAsset(t, s, AssetInput{Name: ptr("fine"), Type: ptr("sprite"), License: ptr("CC0-1.0"), AddProjects: []string{q.ID}}, "f.png", pngBytes(t, 8, 8, color.White))
	if r, _ = s.ProjectReport(ctx, q.ID); r.Verdict != "ok" {
		t.Errorf("clean project: %+v", r)
	}
	if _, err := s.ProjectReport(ctx, "prj_nope"); err == nil {
		t.Error("report for unknown project")
	}
}

func TestMdEscapeAndLinks(t *testing.T) {
	if got := mdEscape("a\nb*c_d[e]<f>"); got != `a b\*c\_d\[e\]\<f\>` {
		t.Errorf("mdEscape = %q", got)
	}
	for _, u := range []string{"javascript:alert(1)", "data:text/html,x", "https://a b", "https://x/<y>", "ftp://x"} {
		if strings.HasPrefix(mdLink(u), "<") {
			t.Errorf("%q rendered as a link", u)
		}
	}
	if mdLink("https://example.com/x?y=1") != "<https://example.com/x?y=1>" {
		t.Error("plain https URL is not linked")
	}
}
