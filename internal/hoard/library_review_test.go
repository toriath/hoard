package hoard

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- hostile and oversized audio ----

func TestHostileOggReturnsQuickly(t *testing.T) {
	dir := t.TempDir()
	// A valid identification header followed by junk; the real attack file
	// sent a Vorbis decoder into an endless loop, so none may be involved.
	var b bytes.Buffer
	b.WriteString("OggS\x00\x02")
	b.Write(make([]byte, 8)) // granule
	b.Write([]byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	id := append([]byte("\x01vorbis"), make([]byte, 23)...)
	binary.LittleEndian.PutUint32(id[7:], 0)
	id[11] = 2
	binary.LittleEndian.PutUint32(id[12:], 44100)
	b.WriteByte(1)
	b.WriteByte(byte(len(id)))
	b.Write(id)
	b.Write(bytes.Repeat([]byte{0xAA, 0x55, 0x01}, 40))
	p := filepath.Join(dir, "evil.ogg")
	os.WriteFile(p, b.Bytes(), 0o600)

	done := make(chan struct{})
	var info *audioInfo
	var err error
	go func() {
		defer close(done)
		info, err = analyzeAudio(context.Background(), p, ".ogg")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hostile Ogg file hangs the analysis")
	}
	if err != nil || info.SampleRate != 44100 || info.Channels != 2 || !info.noWaveform {
		t.Errorf("info = %+v err = %v", info, err)
	}
	// header only, truncated, wrong codec
	for _, data := range [][]byte{[]byte("OggS"), b.Bytes()[:30], append([]byte("OggS\x00\x02"), bytes.Repeat([]byte{0}, 60)...)} {
		os.WriteFile(p, data, 0o600)
		if _, err := analyzeAudio(context.Background(), p, ".ogg"); err == nil {
			t.Errorf("accepted %d bytes of junk as Ogg Vorbis", len(data))
		}
	}
}

func TestOggUploadHasNoWaveformButAttributes(t *testing.T) {
	s := newService(t)
	data, err := os.ReadFile("testdata/tone.ogg")
	if err != nil {
		t.Fatal(err)
	}
	a := mustAsset(t, s, AssetInput{Name: ptr("tone")}, "tone.ogg", data)
	if a.ThumbVersion != 0 {
		t.Error("a waveform was made from an Ogg file")
	}
	if a.Attrs["sample_rate"] != 44100.0 || a.Attrs["duration_s"] == nil {
		t.Errorf("attrs = %v", a.Attrs)
	}
}

func TestLargeAudioIsRefusedQuickly(t *testing.T) {
	s := newService(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "big.mp3")
	f, _ := os.Create(p)
	f.Truncate(maxPreviewFileBytes + 1) // sparse
	f.Close()
	start := time.Now()
	for _, e := range []string{".mp3", ".wav", ".flac", ".ogg"} {
		if _, err := analyzeAudio(context.Background(), p, e); err == nil {
			t.Errorf("%s of %d bytes analysed", e, maxPreviewFileBytes+1)
		}
	}
	if time.Since(start) > time.Second {
		t.Errorf("refusal took %v", time.Since(start))
	}

	// through an upload: no thumbnail, the file is stored, the failure is remembered
	a, _ := s.CreateAsset(context.Background(), AssetInput{Name: ptr("big")})
	fh, _ := os.Open(p)
	defer fh.Close()
	start = time.Now()
	if _, err := s.AddFile(context.Background(), a.ID, "big.mp3", fh, 0); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("upload took %v", time.Since(start))
	}
	a, _ = s.GetAsset(context.Background(), a.ID)
	if a.ThumbVersion != 0 || a.Attrs[previewFailedAttr] == nil {
		t.Errorf("asset = %+v", a)
	}
}

func TestAnalysisStopsWithItsContext(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.wav")
	os.WriteFile(p, wavBytes(1, 16, 1, 8000, 80000, sine(0.5, 100, 8000)), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := analyzeAudio(ctx, p, ".wav"); err == nil {
		t.Error("analysis ran on a cancelled context")
	}
	// the wrapper itself
	f, _ := os.Open(p)
	defer f.Close()
	cf := &ctxFile{ctx: ctx, f: f}
	if _, err := cf.Read(make([]byte, 4)); err == nil {
		t.Error("read after cancellation")
	}
	if _, err := cf.Seek(0, 0); err == nil {
		t.Error("seek after cancellation")
	}
}

func TestMediaWorkDoesNotUseTheImageSlot(t *testing.T) {
	s := newService(t)
	s.thumbSem <- struct{}{} // an image is being decoded
	defer func() { <-s.thumbSem }()
	done := make(chan *Asset, 1)
	go func() {
		done <- mustAsset(t, s, AssetInput{Name: ptr("beep")}, "beep.wav", wavBytes(1, 16, 1, 8000, 4000, sine(0.5, 100, 8000)))
	}()
	select {
	case a := <-done:
		if a.ThumbVersion == 0 {
			t.Error("no waveform")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("audio analysis waits for the image decoder slot")
	}
}

// ---- stale derived data ----

func TestPrimaryChangeRefreshesDerivedData(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	red := encodePNG(t, solid(64, 64, color.NRGBA{230, 40, 40, 255}))
	blue := encodePNG(t, solid(64, 64, color.NRGBA{40, 40, 230, 255}))
	// give the solid pictures some structure so that they get hashes too
	a := mustAsset(t, s, AssetInput{Name: ptr("multi")}, "red.png", red)
	if _, err := s.AddFile(ctx, a.ID, "blue.png", bytes.NewReader(blue), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFile(ctx, a.ID, "tone.wav", bytes.NewReader(wavBytes(1, 16, 1, 8000, 4000, sine(0.5, 100, 8000))), 0); err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetAsset(ctx, a.ID)
	ids := map[string]string{}
	for _, f := range a.Files {
		ids[f.Filename] = f.ID
	}
	colorOf := func() string {
		c, _ := s.AssetColors(ctx, a.ID)
		if len(c) == 0 {
			return ""
		}
		return c[0].Color
	}
	red0 := colorOf()
	if red0 == "" {
		t.Fatal("no colours for the first picture")
	}

	// to another picture: its colours
	if err := s.SetPrimaryFile(ctx, a.ID, ids["blue.png"]); err != nil {
		t.Fatal(err)
	}
	if blue0 := colorOf(); blue0 == "" || blue0 == red0 {
		t.Errorf("colours after switching to the blue picture: %q (was %q)", blue0, red0)
	}
	// to a sound: the picture colours no longer describe the primary file
	if err := s.SetPrimaryFile(ctx, a.ID, ids["tone.wav"]); err != nil {
		t.Fatal(err)
	}
	if c := colorOf(); c != "" {
		t.Errorf("colours of a former primary file survive: %q", c)
	}
	// removing the primary re-points it and recomputes from the new one
	if err := s.SetPrimaryFile(ctx, a.ID, ids["red.png"]); err != nil {
		t.Fatal(err)
	}
	if colorOf() != red0 {
		t.Errorf("colours = %q, want %q", colorOf(), red0)
	}
	if err := s.DeleteFile(ctx, a.ID, ids["red.png"]); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetAsset(ctx, a.ID)
	if p := after.Primary(); p == nil {
		t.Fatal("no primary after deleting")
	}
	c := colorOf()
	if p := after.Primary(); decodableImage[ext(p.Filename)] {
		if c == "" || c == red0 {
			t.Errorf("colours after deleting the primary: %q", c)
		}
	} else if c != "" {
		t.Errorf("colours %q for a non-image primary %s", c, p.Filename)
	}
	// deleting the last picture leaves no colours
	for _, f := range after.Files {
		if decodableImage[ext(f.Filename)] {
			_ = s.DeleteFile(ctx, a.ID, f.ID)
		}
	}
	if colorOf() != "" {
		t.Error("colours remain without any picture")
	}
}

func TestAudioAttrsFollowThePrimaryFile(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	a := mustAsset(t, s, AssetInput{Name: ptr("two")}, "short.wav", wavBytes(1, 16, 1, 8000, 4000, sine(0.5, 100, 8000)))
	if a.Attrs["duration_s"] != 0.5 {
		t.Fatalf("attrs = %v", a.Attrs)
	}
	if _, err := s.AddFile(ctx, a.ID, "long.wav", bytes.NewReader(wavBytes(1, 16, 2, 16000, 32000, sine(0.25, 100, 16000))), 0); err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetAsset(ctx, a.ID)
	var long string
	for _, f := range a.Files {
		if f.Filename == "long.wav" {
			long = f.ID
		}
	}
	if err := s.SetPrimaryFile(ctx, a.ID, long); err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetAsset(ctx, a.ID)
	if a.Attrs["duration_s"] != 2.0 || a.Attrs["sample_rate"] != 16000.0 || a.Attrs["channels"] != 2.0 {
		t.Errorf("attrs of the new primary file = %v", a.Attrs)
	}
}

// ---- backfill ----

func TestBackfillRemembersFailuresAndHasABudget(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	bad := mustAsset(t, s, AssetInput{Name: ptr("bad")}, "bad.wav", []byte("RIFFfakewave"))
	if bad.Attrs[previewFailedAttr] == nil {
		t.Fatalf("failure not remembered: %v", bad.Attrs)
	}
	// The file is made valid behind the index's back: the marker is bound to
	// the recorded content hash, so no retry happens...
	path := filepath.Join(s.DataDir(), "assets", bad.ID, "files", "bad.wav")
	os.WriteFile(path, wavBytes(1, 16, 1, 8000, 800, sine(0.5, 100, 8000)), 0o600)
	s.RunBackfill(ctx)
	if a, _ := s.GetAsset(ctx, bad.ID); a.ThumbVersion != 0 {
		t.Error("a file marked as failed was analysed again")
	}
	// ...until the file really changes (a new upload under the same name).
	if _, err := s.AddFile(ctx, bad.ID, "bad.wav", bytes.NewReader(wavBytes(1, 16, 1, 8000, 800, sine(0.5, 100, 8000))), 0); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.GetAsset(ctx, bad.ID); a.ThumbVersion == 0 || a.Attrs[previewFailedAttr] != nil {
		t.Errorf("changed file not analysed: %+v", a)
	}

	// budget: one file per run
	old := backfillFiles
	backfillFiles = 1
	defer func() { backfillFiles = old }()
	for i := 0; i < 3; i++ {
		mustAsset(t, s, AssetInput{Name: ptr(fmt.Sprintf("p%d", i))}, fmt.Sprintf("p%d.png", i), encodePNG(t, plasma(uint64(i+20), 64)))
	}
	s.db.Exec(`UPDATE asset_files SET phash = ''`)
	s.RunBackfill(ctx)
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM asset_files WHERE length(phash) = 16`).Scan(&n)
	if n != 1 {
		t.Errorf("%d files processed in one run, budget is 1", n)
	}
}

func TestStatsOfAFormerPrimaryAreDropped(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	a := mustAsset(t, s, AssetInput{Name: ptr("x")}, "a.png", encodePNG(t, plasma(40, 64)))
	if _, err := s.AddFile(ctx, a.ID, "b.png", bytes.NewReader(encodePNG(t, plasma(41, 64))), 0); err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetAsset(ctx, a.ID)
	var other string // a file that is not the primary one
	for _, f := range a.Files {
		if f.ID != a.PrimaryFileID {
			other = f.ID
		}
	}
	before, _ := s.AssetColors(ctx, a.ID)
	if err := s.storeImageStats(ctx, a.ID, other, &imageStats{PHash: "0123456789abcdef", Colors: []ColorWeight{{"#101010", 1}}}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.AssetColors(ctx, a.ID)
	var ph string
	s.db.QueryRow(`SELECT phash FROM asset_files WHERE id = ?`, other).Scan(&ph)
	if len(after) != len(before) || ph == "0123456789abcdef" {
		t.Errorf("stale statistics stored: %v phash %q", after, ph)
	}
}

// ---- licence detection ----

func TestDetectLicense(t *testing.T) {
	boiler := "\n\nCreative Commons is not a party to its public licenses.\nThe text of the Creative Commons public licenses is dedicated to the public domain under the CC0 Public Domain Dedication.\n"
	cases := map[string]string{
		// the official legal codes carry the CC0 sentence and must not read as CC0
		"Attribution 4.0 International\n\n=====\nCreative Commons Corporation (\"Creative Commons\") is not a law firm." + boiler: "CC-BY-4.0",
		"Attribution-ShareAlike 4.0 International" + boiler:                                                                       "CC-BY-SA-4.0",
		"Creative Commons Attribution-NonCommercial 4.0 International Public License" + boiler:                                    "CC-BY-NC-4.0",
		"Creative Commons Attribution-NonCommercial-ShareAlike 3.0 Unported" + boiler:                                             "CC-BY-NC-SA-3.0",
		"License: CC BY-NC 4.0": "CC-BY-NC-4.0",
		"https://creativecommons.org/licenses/by-sa/4.0/legalcode" + boiler: "CC-BY-SA-4.0",
		"CC BY 3.0":                   "CC-BY-3.0",
		"licensed under CC-BY-SA 4.0": "CC-BY-SA-4.0",
		"Creative Commons Attribution 4.0 International License":                                  "CC-BY-4.0",
		"CC0 1.0 Universal\n\nStatement of Purpose\n\nCC0 1.0 Universal Public Domain Dedication": "CC0-1.0",
		"https://creativecommons.org/publicdomain/zero/1.0/":                                      "CC0-1.0",
		"License: Creative Commons Zero, CC0":                                                     "CC0-1.0",
		"License: CC0":                                                                            "CC0-1.0",
		"https://creativecommons.org/publicdomain/mark/1.0/":                                      "Public Domain",
		"This work is in the public domain.":                                                      "Public Domain",
		"This is not public domain, ask first.":                                                   "",
		"NOT CC0 1.0: you must credit the artist.":                                                "",
		"Not public domain. License: CC BY 3.0":                                                   "CC-BY-3.0",
		"This font is licensed under the SIL Open Font License, Version 1.1.":                     "OFL-1.1",
		"MIT License\n\nPermission is hereby granted, free of charge, to any person":              "MIT",
		"Licensed under the Apache License, Version 2.0":                                          "Apache-2.0",
		"All rights reserved.":                                                                    "",
		// two different licences: unknown
		"Code: MIT License. Art: CC BY 4.0":                        "",
		"CC0 1.0 Universal, also available as CC BY 4.0":           "",
		"CC BY 4.0 (https://creativecommons.org/licenses/by/4.0/)": "CC-BY-4.0",
		"CC BY (https://creativecommons.org/licenses/by/4.0/)":     "CC-BY-4.0",
	}
	for text, want := range cases {
		if got := DetectLicense(text); got != want {
			t.Errorf("DetectLicense(%.70q) = %q, want %q (all: %v)", text, got, want, DetectLicenses(text))
		}
	}
	if got := DetectLicenses("Code: MIT License. Art: CC BY 4.0"); len(got) != 2 {
		t.Errorf("licences = %v", got)
	}
}

func TestDetectAttribution(t *testing.T) {
	for text, want := range map[string]string{
		"# Pack\n\nCredits:\nCreated by Jane Artist (jane.example)\n": "Created by Jane",
		"© 2024 Jane Artist":                     "© 2024 Jane Artist",
		"(c) 2020 John Doe, all rights reserved": "(c) 2020 John Doe",
		"(C) Studio Name":                        "(C) Studio Name",
		"Copyright 2021 Someone":                 "Copyright 2021 Someone",
		"Credits:\nenjoy\n":                      "",
		"by Jane Artist":                         "by Jane Artist",
	} {
		if got := DetectAttribution(text); !strings.HasPrefix(got, want) || (want == "" && got != "") {
			t.Errorf("DetectAttribution(%q) = %q, want prefix %q", text, got, want)
		}
	}
}

// ---- import: documents, conflicts, central directory ----

func TestImportDocumentsAndConflicts(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	res, err := s.ImportZip(ctx, makeZip(t,
		zf{"LICENSE.txt", "CC BY 4.0"}, zf{"README", "Created by Jane Artist"}, zf{"readme.html", "<p>hi</p>"},
		zf{"credits.png", "png"}, zf{"license.wav", "wav"}, zf{"a.png", "a"}), "p.zip", ImportOptions{Mode: ImportSingle}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// documents with document extensions are never assets, in any mode; other files named like them are
	got := strings.Join(res.Docs, ",")
	if !strings.Contains(got, "LICENSE.txt") || !strings.Contains(got, "README") || !strings.Contains(got, "readme.html") || strings.Contains(got, "credits.png") || strings.Contains(got, "license.wav") {
		t.Errorf("docs = %v", res.Docs)
	}
	if res.Files != 3 || !strings.Contains(res.Summary, "not imported as assets") {
		t.Errorf("files = %d summary = %s", res.Files, res.Summary)
	}
	if res.License != "CC-BY-4.0" {
		t.Errorf("license = %q", res.License)
	}

	// conflicting licences: none applied, and the summary says so
	res, err = s.ImportZip(ctx, makeZip(t, zf{"LICENSE", "Art: CC BY 4.0, code: MIT License"}, zf{"a.png", "a"}), "q.zip", ImportOptions{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.License != "" || len(res.LicenseConflict) != 2 || !strings.Contains(res.Summary, "no license applied") {
		t.Errorf("result = %+v", res)
	}
	a, _ := s.GetAsset(ctx, res.Assets[0].ID)
	if a.License != "" {
		t.Errorf("asset license = %q", a.License)
	}
	// the official CC BY text with its CC0 sentence must not turn into CC0
	res, _ = s.ImportZip(ctx, makeZip(t, zf{"LICENSE.txt", "Attribution 4.0 International\n...The text of the Creative Commons public licenses is dedicated to the public domain under the CC0 Public Domain Dedication."}, zf{"b.png", "b"}), "r.zip", ImportOptions{}, 0)
	if res.License != "CC-BY-4.0" {
		t.Errorf("license = %q", res.License)
	}
}

func TestImportEntryCountIsReadBeforeTheDirectory(t *testing.T) {
	s := newService(t)
	dir := t.TempDir()

	// zip64: more entries than a 16-bit count holds
	big := filepath.Join(dir, "big.zip")
	fh, _ := os.Create(big)
	zw := zip.NewWriter(fh)
	for i := 0; i < 70000; i++ {
		zw.Create(fmt.Sprintf("f%d", i))
	}
	zw.Close()
	fh.Close()
	if n, err := zipEntryCount(big); err != nil || n != 70000 {
		t.Fatalf("zip64 count = %d %v", n, err)
	}
	_, err := s.ImportZip(context.Background(), big, "big.zip", ImportOptions{}, 0)
	if err == nil || !strings.Contains(err.Error(), "entries") {
		t.Errorf("err = %v", err)
	}

	// a forged count in the end record of a tiny archive
	data, _ := os.ReadFile(makeZip(t, zf{"a.png", "a"}))
	i := bytes.LastIndex(data, []byte("PK\x05\x06"))
	binary.LittleEndian.PutUint16(data[i+8:], 60000)
	binary.LittleEndian.PutUint16(data[i+10:], 60000)
	forged := filepath.Join(dir, "forged.zip")
	os.WriteFile(forged, data, 0o600)
	if n, _ := zipEntryCount(forged); n != 60000 {
		t.Errorf("forged count = %d", n)
	}
	_, err = s.ImportZip(context.Background(), forged, "forged.zip", ImportOptions{}, 0)
	if err == nil || !strings.Contains(err.Error(), "60000 entries") {
		t.Errorf("err = %v", err)
	}
	// a normal archive passes
	if n, err := zipEntryCount(makeZip(t, zf{"a", "x"}, zf{"b", "y"})); err != nil || n != 2 {
		t.Errorf("count = %d %v", n, err)
	}
}

func TestColorFilterIsCapped(t *testing.T) {
	s := newService(t)
	var cols []string
	for i := 0; i <= MaxColorFilters; i++ {
		cols = append(cols, "#ff0000")
	}
	if _, err := s.ListAssets(context.Background(), ListQuery{Colors: cols}); err == nil {
		t.Error("more colours than allowed accepted")
	}
	if _, err := s.ListAssets(context.Background(), ListQuery{Colors: cols[:MaxColorFilters]}); err != nil {
		t.Errorf("ten colours refused: %v", err)
	}
}
