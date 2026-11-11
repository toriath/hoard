package hoard

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dlvProject(t *testing.T, s *Service, name, brief string) *Project {
	t.Helper()
	p, err := s.SaveProject(context.Background(), "", GroupInput{Name: ptr(name)})
	if err != nil {
		t.Fatal(err)
	}
	if brief != "" {
		if _, err := s.db.Exec(`UPDATE projects SET brief = ? WHERE id = ?`, brief, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func dlvSupersede(t *testing.T, s *Service, old, newer string) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE assets SET superseded_by = ?, status = 'superseded' WHERE id = ?`, newer, old); err != nil {
		t.Fatal(err)
	}
}

func dlvSnapshot(t *testing.T, s *Service, projectID string, lines ...string) {
	t.Helper()
	if _, err := s.ReplaceRepoSnapshot(context.Background(), projectID, strings.NewReader(strings.Join(lines, "\n")+"\n")); err != nil {
		t.Fatal(err)
	}
}

func dlvLine(sha string, size int, p string) string { return fmt.Sprintf("%s\t%d\t%s", sha, size, p) }

func TestRepoSnapshotParsing(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	good := dlvLine(sha, 12, "art/hero.png")
	files, err := ParseRepoSnapshot(strings.NewReader(good + "\n" + dlvLine(strings.ToUpper(sha), 0, "a b/ü.ogg") + "\r\n"))
	if err != nil || len(files) != 2 || files[1].SHA256 != sha || files[1].Path != "a b/ü.ogg" {
		t.Fatalf("good snapshot: %v %+v", err, files)
	}
	bad := map[string]string{
		"traversal":     dlvLine(sha, 1, "art/../../etc/passwd"),
		"leading dots":  dlvLine(sha, 1, "../x.png"),
		"absolute":      dlvLine(sha, 1, "/etc/passwd"),
		"drive":         dlvLine(sha, 1, "C:/x.png"),
		"dot segment":   dlvLine(sha, 1, "a/./b.png"),
		"empty segment": dlvLine(sha, 1, "a//b.png"),
		"nul":           dlvLine(sha, 1, "a\x00b.png"),
		"control":       dlvLine(sha, 1, "a\x07b.png"),
		"bad utf8":      dlvLine(sha, 1, "a\xffb.png"),
		"too long":      dlvLine(sha, 1, strings.Repeat("a", 1025)),
		"short sha":     dlvLine("abcd", 1, "a.png"),
		"non-hex sha":   dlvLine(strings.Repeat("zz", 32), 1, "a.png"),
		"bad size":      dlvLine(sha, -1, "a.png"),
		"text size":     sha + "\tbig\ta.png",
		"missing field": sha + "\ta.png",
		"empty":         "",
		"duplicate":     good + "\n" + good,
	}
	for name, in := range bad {
		if _, err := ParseRepoSnapshot(strings.NewReader(in)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Whole upload rejected on one bad line.
	if _, err := ParseRepoSnapshot(strings.NewReader(good + "\n" + dlvLine(sha, 1, "../x"))); err == nil {
		t.Error("snapshot with one bad line accepted")
	}
	// Limits.
	var many strings.Builder
	for i := 0; i <= maxSnapshotLines; i++ {
		many.WriteString(dlvLine(sha, 1, fmt.Sprintf("f/%d.png", i)) + "\n")
	}
	if _, err := ParseRepoSnapshot(strings.NewReader(many.String())); err == nil {
		t.Error("more than 50000 lines accepted")
	}
	if _, err := ParseRepoSnapshot(strings.NewReader(strings.Repeat("x", maxSnapshotBytes+1))); err == nil {
		t.Error("oversize snapshot accepted")
	}
}

func TestSnapshotReplacesAndRecords(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	p := dlvProject(t, s, "game", "")
	a := mustAsset(t, s, AssetInput{Name: ptr("coin")}, "coin.png", pngBytes(t, 4, 4, color.RGBA{255, 0, 0, 255}))
	dlvSnapshot(t, s, p.ID, dlvLine(a.Files[0].SHA256, 10, "art/coin.png"), dlvLine(strings.Repeat("00", 32), 5, "art/other.png"))
	res, err := s.ReplaceRepoSnapshot(ctx, p.ID, strings.NewReader(dlvLine(a.Files[0].SHA256, 10, "moved/coin.png")+"\n"))
	if err != nil || res.Known != 1 || res.Unknown != 0 {
		t.Fatalf("replace: %v %+v", err, res)
	}
	snap, _, _ := s.loadSnapshot(ctx, p.ID)
	if len(snap) != 1 || snap[0].Path != "moved/coin.png" {
		t.Fatalf("snapshot not replaced: %+v", snap)
	}
	act, _ := s.ListActivity(ctx, ActivityQuery{Action: "project.repo_snapshot"})
	if len(act) != 2 {
		t.Errorf("activity entries = %d", len(act))
	}
	if _, err := s.ReplaceRepoSnapshot(ctx, "prj_nope", strings.NewReader(dlvLine(a.Files[0].SHA256, 1, "a.png"))); err == nil {
		t.Error("snapshot for unknown project accepted")
	}
}

func TestAuditClassification(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	p := dlvProject(t, s, "game", "")
	mk := func(name, file string, c color.RGBA, member bool) *Asset {
		in := AssetInput{Name: ptr(name), License: ptr("CC0-1.0")}
		if member {
			in.AddProjects = []string{p.ID}
		}
		return mustAsset(t, s, in, file, pngBytes(t, 4, 4, c))
	}
	current := mk("current", "current.png", color.RGBA{255, 0, 0, 255}, true)
	oldV := mk("hero old", "hero.png", color.RGBA{0, 0, 255, 255}, false)
	newV := mk("hero new", "hero2.png", color.RGBA{0, 255, 0, 255}, true)
	dlvSupersede(t, s, oldV.ID, newV.ID)
	notThere := mk("not there", "missing.png", color.RGBA{255, 255, 0, 255}, true)
	stray := mk("stray", "stray.png", color.RGBA{0, 255, 255, 255}, false)
	if _, err := s.UpdateAsset(ctx, current.ID, AssetInput{Status: ptr("placeholder")}); err != nil {
		t.Fatal(err)
	}

	unknownSHA := strings.Repeat("ee", 32)
	dlvSnapshot(t, s, p.ID,
		dlvLine(current.Files[0].SHA256, 1, "art/current.png"),
		dlvLine(oldV.Files[0].SHA256, 1, "art/hero.png"),
		dlvLine(stray.Files[0].SHA256, 1, "art/stray.png"),
		dlvLine(unknownSHA, 99, "art/Player_Walk.png"),
	)
	r, err := s.AuditProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := r.Counts
	if c.Files != 4 || c.InSync != 2 || c.Outdated != 1 || c.Untracked != 1 || c.Missing != 2 || c.Unlinked != 1 || c.Attention != 1 {
		t.Fatalf("counts = %+v", c)
	}
	byPath := map[string]AuditKnown{}
	for _, k := range r.Known {
		byPath[k.Path] = k
	}
	if k := byPath["art/hero.png"]; k.NewerAssetID != newV.ID || !strings.Contains(strings.Join(k.Flags, ","), "superseded") {
		t.Errorf("outdated file = %+v", k)
	}
	if k := byPath["art/current.png"]; strings.Join(k.Flags, ",") != "placeholder" || k.License != "CC0-1.0" {
		t.Errorf("placeholder file = %+v", k)
	}
	if k := byPath["art/stray.png"]; strings.Join(k.Flags, ",") != "not_in_project" {
		t.Errorf("unlinked file = %+v", k)
	}
	if len(r.Unknown) != 1 || r.Unknown[0].SuggestedName != "Player_Walk" || r.Unknown[0].SuggestedType != "sprite" {
		t.Errorf("unknown = %+v", r.Unknown)
	}
	miss := map[string]bool{}
	for _, m := range r.Missing {
		miss[m.AssetID] = true
	}
	if !miss[notThere.ID] || !miss[newV.ID] || r.Unlinked[0].AssetID != stray.ID {
		t.Errorf("missing = %v unlinked = %+v", miss, r.Unlinked)
	}

	empty := dlvProject(t, s, "empty", "")
	if r, err := s.AuditProject(ctx, empty.ID); err != nil || r.HasSnapshot || r.Hint == "" {
		t.Errorf("audit without snapshot: %v %+v", err, r)
	}
}

func TestApplyNaming(t *testing.T) {
	cases := []struct{ style, in, want string }{
		{"snake_case", "HeroIdle_Walk 2.PNG", "hero_idle_walk_2.png"},
		{"kebab-case", "HeroIdle_Walk 2.PNG", "hero-idle-walk-2.png"},
		{"camelCase", "hero_idle walk.png", "heroIdleWalk.png"},
		{"PascalCase", "hero_idle walk.png", "HeroIdleWalk.png"},
		{"snake_case", "HTTPServer.png", "http_server.png"},
		{"keep", "Hero Idle.PNG", "Hero Idle.PNG"},
		{"", "Hero Idle.png", "Hero Idle.png"},
		{"snake_case", "$$$.png", "$$$.png"},
	}
	for _, c := range cases {
		// SanitizeFilename turns $ into _ in the fallback case.
		want := c.want
		if c.in == "$$$.png" {
			want = "_.png"
		}
		if got := ApplyNaming(c.in, c.style); got != want {
			t.Errorf("ApplyNaming(%q, %q) = %q, want %q", c.in, c.style, got, want)
		}
	}
}

func TestPlanSync(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	brief := `{"engine":"godot","naming":"snake_case","folders":{"sprite":"art/sprites","default":"assets/misc","music":"../escape"}}`
	p := dlvProject(t, s, "game", brief)
	mk := func(name, typ, file string, c color.RGBA, data []byte) *Asset {
		if data == nil {
			data = pngBytes(t, 4, 4, c)
		}
		return mustAsset(t, s, AssetInput{Name: ptr(name), Type: ptr(typ), AddProjects: []string{p.ID}, License: ptr("CC-BY-4.0"), Attribution: ptr("by me")}, file, data)
	}
	fresh := mk("Hero Idle", "sprite", "HeroIdle.png", color.RGBA{1, 2, 3, 255}, nil)
	other := mk("step", "sfx", "Step.wav", color.RGBA{}, []byte("RIFFxxxxWAVE-step"))
	music := mk("theme", "music", "Theme.ogg", color.RGBA{}, []byte("OggS-theme"))
	same := mk("same", "sprite", "same.png", color.RGBA{9, 9, 9, 255}, nil)
	knightOld := mk("knight old", "sprite", "knight.png", color.RGBA{10, 0, 0, 255}, nil)
	knightNew := mk("knight new", "sprite", "knight_v2.png", color.RGBA{20, 0, 0, 255}, nil)
	dlvSupersede(t, s, knightOld.ID, knightNew.ID)
	xOld := mk("x old", "sprite", "x.png", color.RGBA{30, 0, 0, 255}, nil)
	xNew := mk("x new", "sprite", "x.webp", color.RGBA{40, 0, 0, 255}, []byte("RIFFxxxxWEBPVP8 fake"))
	dlvSupersede(t, s, xOld.ID, xNew.ID)
	// A replaced asset still being a member must not produce a second entry.
	if _, err := s.UpdateAsset(ctx, knightOld.ID, AssetInput{AddProjects: []string{p.ID}}); err != nil {
		t.Fatal(err)
	}

	dlvSnapshot(t, s, p.ID,
		dlvLine(same.Files[0].SHA256, 1, "elsewhere/same_copy.png"),
		dlvLine(knightOld.Files[0].SHA256, 1, "game/chars/old_name.png"),
		dlvLine(xOld.Files[0].SHA256, 1, "game/x.png"),
		dlvLine(strings.Repeat("dd", 32), 1, "assets/misc/step.wav"), // different file where step.wav would go
	)
	plan, err := s.PlanSync(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]SyncEntry{}
	for _, e := range plan.Entries {
		by[e.AssetID] = e
	}
	check := func(a *Asset, path, reason string) {
		t.Helper()
		if e := by[a.ID]; e.Path != path || e.Reason != reason {
			t.Errorf("%s: got %s (%s), want %s (%s)", a.Name, e.Path, e.Reason, path, reason)
		}
	}
	check(fresh, "art/sprites/hero_idle.png", "missing") // brief folder + snake_case
	check(other, "assets/misc/step.wav", "conflict")     // folders.default, occupied by another file
	check(music, "assets/misc/theme.ogg", "missing")     // unusable type folder falls back to folders.default
	check(same, "elsewhere/same_copy.png", "unchanged")  // already in the repo: keep its place
	check(knightNew, "game/chars/old_name.png", "changed")
	check(xNew, "art/sprites/x.webp", "missing") // extension differs: not in place
	if _, dup := by[knightOld.ID]; dup {
		t.Error("superseded asset has its own entry")
	}
	if len(plan.Removals) != 1 || plan.Removals[0].Path != "game/x.png" || plan.Removals[0].ReplacedBy != xNew.ID || plan.Removals[0].NewPath != "art/sprites/x.webp" {
		t.Errorf("removals = %+v", plan.Removals)
	}
	if e := by[fresh.ID]; e.License != "CC-BY-4.0" || e.Attribution != "by me" || e.Filename != "HeroIdle.png" {
		t.Errorf("entry metadata = %+v", e)
	}

	lock, err := s.BuildRepoLock(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range lock.Entries {
		if e.AssetID == other.ID {
			t.Error("conflicting entry in lock file")
		}
		if e.SHA256 == "" || e.AssetID == "" {
			t.Errorf("incomplete lock entry %+v", e)
		}
	}
	if len(lock.Entries) != 5 || lock.ProjectID != p.ID {
		t.Errorf("lock = %+v", lock)
	}
}

func TestSyncDefaultsWithoutBrief(t *testing.T) {
	s := newService(t)
	p := dlvProject(t, s, "game", "")
	a := mustAsset(t, s, AssetInput{Name: ptr("hero"), AddProjects: []string{p.ID}}, "Hero Sheet.png", pngBytes(t, 4, 4, color.RGBA{5, 5, 5, 255}))
	plan, err := s.PlanSync(context.Background(), p.ID)
	if err != nil || len(plan.Entries) != 1 || plan.Entries[0].Path != "assets/sprite/Hero Sheet.png" || plan.Entries[0].AssetID != a.ID || len(plan.Warnings) == 0 {
		t.Fatalf("plan = %+v %v", plan, err)
	}
}

func dlvSolid(t *testing.T, w, h int, c color.RGBA) []byte { return pngBytes(t, w, h, c) }

func TestPackAtlas(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	sizes := [][2]int{{32, 16}, {16, 16}, {8, 24}, {20, 20}, {5, 5}, {40, 10}}
	var ids []string
	cols := map[string]color.RGBA{}
	for i, sz := range sizes {
		c := color.RGBA{uint8(40 * (i + 1)), uint8(250 - 30*i), uint8(7 * i), 255}
		a := mustAsset(t, s, AssetInput{Name: ptr(fmt.Sprintf("spr %d", i))}, fmt.Sprintf("s%d.png", i), dlvSolid(t, sz[0], sz[1], c))
		ids = append(ids, a.ID)
		cols[a.ID] = c
	}
	for _, o := range []AtlasOptions{
		{Padding: 2, Extrude: 1, MaxSize: 256},
		{Padding: 0, MaxSize: 128, PowerOfTwo: true},
	} {
		at, err := s.PackAtlas(ctx, ids, o)
		if err != nil {
			t.Fatalf("%+v: %v", o, err)
		}
		o, _ = o.Normalize()
		if at.Width > o.MaxSize || at.Height > o.MaxSize || len(at.Frames) != len(sizes) {
			t.Fatalf("atlas %dx%d frames %d", at.Width, at.Height, len(at.Frames))
		}
		if o.PowerOfTwo && (nextPow2(at.Width) != at.Width || nextPow2(at.Height) != at.Height) {
			t.Errorf("atlas %dx%d is not a power of two", at.Width, at.Height)
		}
		img, err := png.Decode(bytes.NewReader(at.PNG))
		if err != nil || img.Bounds().Dx() != at.Width || img.Bounds().Dy() != at.Height {
			t.Fatalf("atlas image: %v", err)
		}
		for i, f := range at.Frames {
			if f.W != sizes[i][0] || f.H != sizes[i][1] || f.AssetID != ids[i] {
				t.Errorf("frame %d = %+v", i, f)
			}
			if f.X < 0 || f.Y < 0 || f.X+f.W > at.Width || f.Y+f.H > at.Height {
				t.Errorf("frame %+v outside atlas", f)
			}
			for _, pt := range [][2]int{{f.X, f.Y}, {f.X + f.W - 1, f.Y + f.H - 1}, {f.X + f.W/2, f.Y + f.H/2}} {
				want := cols[f.AssetID]
				if r, g, b, _ := img.At(pt[0], pt[1]).RGBA(); uint8(r>>8) != want.R || uint8(g>>8) != want.G || uint8(b>>8) != want.B {
					t.Errorf("frame %s pixel %v has wrong colour", f.Name, pt)
				}
			}
			if o.Extrude > 0 { // the pixel just outside the sprite repeats its edge
				if r, _, _, a := img.At(f.X-1, f.Y-1).RGBA(); uint8(r>>8) != cols[f.AssetID].R || a == 0 {
					t.Errorf("frame %s not extruded", f.Name)
				}
			}
			for j, g := range at.Frames[:i] { // no overlap, padding respected
				pd := o.Padding + 2*o.Extrude
				if f.X < g.X+g.W+pd && g.X < f.X+f.W+pd && f.Y < g.Y+g.H+pd && g.Y < f.Y+f.H+pd {
					// extrude regions of neighbours may touch padding but sprites themselves must not overlap
					if f.X < g.X+g.W && g.X < f.X+f.W && f.Y < g.Y+g.H && g.Y < f.Y+f.H {
						t.Errorf("frames %d and %d overlap: %+v %+v", i, j, f, g)
					}
				}
			}
		}
	}

	// Does not fit.
	if _, err := s.PackAtlas(ctx, ids, AtlasOptions{MaxSize: 32}); err == nil {
		t.Error("packing into a too small atlas succeeded")
	}
	// Option validation.
	for _, o := range []AtlasOptions{{MaxSize: 9000}, {Padding: 40}, {Extrude: 5}, {Format: "xml"}, {Format: "godot", ResPath: "res://../x.png"}, {Format: "godot", ResPath: `res://a"b.png`}} {
		if _, err := o.Normalize(); err == nil {
			t.Errorf("options %+v accepted", o)
		}
	}
	// Not an image.
	snd := mustAsset(t, s, AssetInput{Name: ptr("snd")}, "a.wav", []byte("RIFFxxxxWAVEfmt "))
	if _, err := s.PackAtlas(ctx, []string{ids[0], snd.ID}, AtlasOptions{}); err == nil {
		t.Error("atlas of a sound accepted")
	}
}

func TestAtlasOutputs(t *testing.T) {
	s := newService(t)
	a := mustAsset(t, s, AssetInput{Name: ptr(`evil"../name`)}, "a.png", dlvSolid(t, 8, 8, color.RGBA{1, 1, 1, 255}))
	b := mustAsset(t, s, AssetInput{Name: ptr("evil\"../name")}, "b.png", dlvSolid(t, 8, 8, color.RGBA{2, 2, 2, 255}))
	read := func(format string) map[string]string {
		at, err := s.PackAtlas(context.Background(), []string{a.ID, b.ID}, AtlasOptions{Format: format, ResPath: "res://art/atlas.png"})
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := at.WriteZip(&buf); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, f := range zr.File {
			if strings.Contains(f.Name, "..") || strings.HasPrefix(f.Name, "/") || strings.ContainsAny(f.Name, `"\`) {
				t.Errorf("unsafe zip entry %q", f.Name)
			}
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			out[f.Name] = string(data)
		}
		return out
	}
	g := read("godot")
	var tres string
	n := 0
	for name, data := range g {
		if strings.HasSuffix(name, ".tres") {
			tres = data
			n++
		}
	}
	if n != 2 || !strings.HasPrefix(tres, `[gd_resource type="AtlasTexture" load_steps=2 format=3]`) ||
		!strings.Contains(tres, `path="res://art/atlas.png"`) || !strings.Contains(tres, "region = Rect2(") || g["atlas.png"] == "" {
		t.Errorf("godot output: %d tres, %q", n, tres)
	}
	var ph struct {
		Frames map[string]struct {
			Frame struct{ X, Y, W, H int } `json:"frame"`
		} `json:"frames"`
		Meta struct{ Image string } `json:"meta"`
	}
	if err := json.Unmarshal([]byte(read("phaser")["atlas.json"]), &ph); err != nil || len(ph.Frames) != 2 || ph.Meta.Image != "atlas.png" {
		t.Errorf("phaser json: %v %+v", err, ph)
	}
	var gen struct{ Frames []AtlasFrame }
	if err := json.Unmarshal([]byte(read("json")["atlas.json"]), &gen); err != nil || len(gen.Frames) != 2 || gen.Frames[0].AssetID != a.ID {
		t.Errorf("generic json: %v %+v", err, gen)
	}
}

// hugePNG is a valid PNG header that declares an enormous image and holds no
// pixel data: decoding it would need gigabytes.
func hugePNG(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6
	chunk := append([]byte("IHDR"), ihdr...)
	binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	b.Write(chunk)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func TestAtlasAndDeriveDecodeLimits(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	huge := mustAsset(t, s, AssetInput{Name: ptr("huge")}, "huge.png", hugePNG(30000, 30000))
	if _, err := s.PackAtlas(ctx, []string{huge.ID}, AtlasOptions{}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("huge image packed: %v", err)
	}
	if _, _, err := s.Derive(ctx, huge.ID, "huge.png", DeriveOptions{Scale: 0.5}); err == nil {
		t.Error("huge image derived")
	}

	// The decode budget over all sources: eight images of 36 MB decoded each.
	big := dlvSolid(t, 3000, 3000, color.RGBA{9, 9, 9, 255})
	var ids []string
	for i := range 8 {
		a := mustAsset(t, s, AssetInput{Name: ptr(fmt.Sprintf("big%d", i))}, "big.png", big)
		ids = append(ids, a.ID)
	}
	if _, err := s.PackAtlas(ctx, ids, AtlasOptions{MaxSize: 8192}); err == nil || !strings.Contains(err.Error(), "decode budget") {
		t.Errorf("budget exceeded without the expected error: %v", err)
	}
}

func TestDerive(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for y := range 2 {
		for x := range 4 {
			img.Set(x, y, color.NRGBA{R: 255, A: 255})
			if x >= 2 {
				img.Set(x, y, color.NRGBA{B: 255, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	a := mustAsset(t, s, AssetInput{Name: ptr("two"), Attrs: map[string]any{"pixel_art": true}}, "two.png", buf.Bytes())

	if f := s.DefaultDeriveFilter(ctx, a, 0); f != "nearest" {
		t.Errorf("pixel art filter = %s", f)
	}
	plain := mustAsset(t, s, AssetInput{Name: ptr("photo")}, "p.png", dlvSolid(t, 4, 4, color.RGBA{1, 1, 1, 255}))
	if s.DefaultDeriveFilter(ctx, plain, 1.5) != "linear" || s.DefaultDeriveFilter(ctx, plain, 3) != "nearest" {
		t.Error("default filter rules")
	}

	p, mime, err := s.Derive(ctx, a.ID, "two.png", DeriveOptions{Scale: 3, Filter: "nearest"})
	if err != nil || mime != "image/png" {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, filepath.Join(s.DataDir(), "cache", "derived", a.ID)) {
		t.Errorf("cache path %s", p)
	}
	data, _ := os.ReadFile(p)
	out, err := png.Decode(bytes.NewReader(data))
	if err != nil || out.Bounds().Dx() != 12 || out.Bounds().Dy() != 6 {
		t.Fatalf("derivative: %v %v", err, out.Bounds())
	}
	if r, _, b, _ := out.At(5, 0).RGBA(); r>>8 != 255 || b != 0 { // still pure red: nearest neighbour does not blend
		t.Errorf("nearest blended: %v %v", r, b)
	}
	// Cached: same path, and no re-render needed.
	p2, _, err := s.Derive(ctx, a.ID, "two.png", DeriveOptions{Scale: 3, Filter: "nearest"})
	if err != nil || p2 != p {
		t.Errorf("cache miss: %s vs %s", p2, p)
	}
	// Width only keeps the ratio; jpeg works.
	p3, mime, err := s.Derive(ctx, a.ID, "two.png", DeriveOptions{Width: 8, Format: "jpeg", Filter: "linear"})
	if err != nil || mime != "image/jpeg" || !strings.HasSuffix(p3, ".jpg") {
		t.Fatalf("jpeg: %v %s", err, p3)
	}
	for _, bad := range []DeriveOptions{{}, {Scale: 2, Width: 4}, {Width: 9000}, {Scale: 5000}, {Width: 4, Filter: "cubic"}, {Width: 4, Format: "webp"}, {Width: 4, Format: "jpeg", Quality: 200}} {
		if _, _, err := s.Derive(ctx, a.ID, "two.png", bad); err == nil {
			t.Errorf("options %+v accepted", bad)
		}
	}
	// A result larger than the edge cap is refused before rendering.
	if _, _, err := s.Derive(ctx, a.ID, "two.png", DeriveOptions{Scale: 4000, Filter: "nearest"}); err == nil {
		t.Error("oversize result accepted")
	}
	// File names are resolved through the index.
	if _, _, err := s.Derive(ctx, a.ID, "../../hoard.db", DeriveOptions{Width: 2}); err == nil {
		t.Error("arbitrary path accepted")
	}

	// Purging removes the cache.
	if _, err := s.Bulk(ctx, BulkOp{IDs: []string{a.ID}, Action: "purge"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.DataDir(), "cache", "derived", a.ID)); !os.IsNotExist(err) {
		t.Error("derivative cache survived purge")
	}
	// A cache dir whose asset is gone is swept at startup.
	orphan := filepath.Join(s.DataDir(), "cache", "derived", "ast_aaaaaaaaaaaa")
	_ = os.MkdirAll(orphan, 0o750)
	s.SweepDerived(ctx)
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("orphan cache dir not swept")
	}
}

func TestEngineHints(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	p := dlvProject(t, s, "game", `{"engine":"godot","pixel_art":true,"tile_size":16}`)
	sheet := mustAsset(t, s, AssetInput{Name: ptr("hero run"), AddProjects: []string{p.ID}, Attrs: map[string]any{
		"frame_width": 16.0, "frame_height": 16.0, "frames": 6.0, "columns": 3.0, "fps": 12.0, "loop": true,
		"animations": map[string]any{"run": []any{0.0, 2.0}, `bad"name`: []any{3.0, 5.0}, "oob": []any{4.0, 99.0}},
	}}, "hero_run.png", dlvSolid(t, 48, 32, color.RGBA{3, 3, 3, 255}))
	music := mustAsset(t, s, AssetInput{Name: ptr("theme"), Type: ptr("music")}, "theme.ogg", []byte("OggS"))
	sfx := mustAsset(t, s, AssetInput{Name: ptr("step"), Type: ptr("sfx"), Attrs: map[string]any{"duration_s": 0.4}}, "step.wav", []byte("RIFFxxxxWAVE"))

	res, err := s.EngineHints(ctx, []string{sheet.ID, music.ID, sfx.ID}, HintsOptions{ProjectID: p.ID, ResDir: "res://art/sprites"})
	if err != nil || res.Engine != "godot" || res.EngineSource != "project brief" || len(res.ProjectNotes) == 0 {
		t.Fatalf("hints: %v %+v", err, res)
	}
	h := res.Assets[0]
	if h.Files[0].Settings["Compress > Mode"] != "Lossless" {
		t.Errorf("pixel art import = %+v", h.Files[0])
	}
	want := []string{
		`[gd_resource type="SpriteFrames" load_steps=8 format=3]`,
		`[ext_resource type="Texture2D" path="res://art/sprites/hero_run.png" id="1_sheet"]`,
		`[sub_resource type="AtlasTexture" id="AtlasTexture_5"]`,
		"region = Rect2(32, 16, 16, 16)",
		`"name": &"run"`, `"name": &"bad_name"`, `"speed": 12`, `"loop": true`,
		`"texture": SubResource("AtlasTexture_2")`,
	}
	for _, w := range want {
		if !strings.Contains(h.SpriteFrames, w) {
			t.Errorf("SpriteFrames lacks %q:\n%s", w, h.SpriteFrames)
		}
	}
	if strings.Contains(h.SpriteFrames, "oob") || strings.Contains(h.SpriteFrames, `bad"`) || h.SpriteFramesName != "hero run.tres" {
		t.Errorf("SpriteFrames kept invalid animations or names (%s):\n%s", h.SpriteFramesName, h.SpriteFrames)
	}
	if res.Assets[1].Files[0].ImportParams["loop"] != true || res.Assets[2].Files[0].Settings["Edit > Loop Mode"] != "Disabled" {
		t.Errorf("audio hints: %+v %+v", res.Assets[1].Files[0], res.Assets[2].Files[0])
	}

	u, err := s.EngineHints(ctx, []string{sheet.ID, music.ID, sfx.ID}, HintsOptions{Engine: "unity", ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	us := u.Assets[0].Files[0].Settings
	if us["Filter Mode"] != "Point (no filter)" || us["Pixels Per Unit"] != 16.0 || us["Sprite Mode"] != "Multiple" || u.Assets[0].SpriteFrames != "" {
		t.Errorf("unity sprite = %+v", us)
	}
	if u.Assets[1].Files[0].Settings["Load Type"] != "Streaming" || u.Assets[2].Files[0].Settings["Load Type"] != "Decompress On Load" {
		t.Errorf("unity audio = %+v %+v", u.Assets[1].Files[0], u.Assets[2].Files[0])
	}
	ph, err := s.EngineHints(ctx, []string{sheet.ID}, HintsOptions{Engine: "phaser"})
	if err != nil || !strings.Contains(ph.Assets[0].Files[0].Snippet, "frameWidth: 16") {
		t.Errorf("phaser: %v %+v", err, ph)
	}
	if _, err := s.EngineHints(ctx, []string{sheet.ID}, HintsOptions{}); err == nil {
		t.Error("hints without any engine accepted")
	}
	if _, err := s.EngineHints(ctx, []string{sheet.ID}, HintsOptions{Engine: "godot", ResDir: `res://a"b`}); err == nil {
		t.Error("bad res_dir accepted")
	}
}
