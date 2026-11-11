package hoard

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image/color"
	"strings"
	"testing"
	"time"
)

// pngWithChunks inserts raw chunks right after the IHDR of a valid PNG.
func pngWithChunks(t *testing.T, chunks ...[]byte) []byte {
	t.Helper()
	base := pngBytes(t, 4, 4, color.White)
	at := 8 + 8 + 13 + 4 // signature + IHDR header + data + CRC
	out := append([]byte{}, base[:at]...)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return append(out, base[at:]...)
}

func chunk(typ string, data []byte) []byte {
	b := make([]byte, 4, 12+len(data))
	binary.BigEndian.PutUint32(b, uint32(len(data)))
	b = append(b, typ...)
	b = append(b, data...)
	crc := crc32.ChecksumIEEE(append([]byte(typ), data...))
	return binary.BigEndian.AppendUint32(b, crc)
}

func textChunkBytes(key, text string) []byte {
	return chunk("tEXt", append([]byte(key+"\x00"), text...))
}

func itxtChunk(key, text string, compress bool) []byte {
	d := []byte(key + "\x00")
	if compress {
		var z bytes.Buffer
		w := zlib.NewWriter(&z)
		w.Write([]byte(text))
		w.Close()
		d = append(d, 1, 0, 0, 0)
		return chunk("iTXt", append(d, z.Bytes()...))
	}
	d = append(d, 0, 0, 0, 0)
	return chunk("iTXt", append(d, text...))
}

const a1111Text = "a knight in plate armor, pixel art\nNegative prompt: blurry, lowres\nSteps: 28, Sampler: Euler a, CFG scale: 7.5, Seed: 123456789, Size: 512x768, Model hash: abc123, Model: pixelDiffusion, Lora hashes: \"a: 1, b: 2\""

func TestParsePNGTextChunks(t *testing.T) {
	cases := map[string][]byte{
		"tEXt":       pngWithChunks(t, textChunkBytes("parameters", a1111Text)),
		"iTXt":       pngWithChunks(t, itxtChunk("parameters", a1111Text, false)),
		"iTXt zlib":  pngWithChunks(t, itxtChunk("parameters", a1111Text, true)),
		"after junk": pngWithChunks(t, chunk("tIME", make([]byte, 7)), textChunkBytes("parameters", a1111Text)),
	}
	for name, data := range cases {
		imp := importFromChunks(readPNGText(bytes.NewReader(data)))
		if imp == nil {
			t.Fatalf("%s: nothing imported", name)
		}
		g := imp.gen
		if imp.prompt != "a knight in plate armor, pixel art" || g["negative_prompt"] != "blurry, lowres" ||
			g["steps"] != int64(28) || g["sampler"] != "Euler a" || g["cfg"] != 7.5 || g["seed"] != int64(123456789) ||
			g["width"] != 512 || g["height"] != 768 || g["model"] != "pixelDiffusion" || g["tool"] != "Automatic1111" {
			t.Errorf("%s: parsed %q %v", name, imp.prompt, g)
		}
		if set, _ := g["settings"].(map[string]any); set["Lora hashes"] != "a: 1, b: 2" {
			t.Errorf("%s: extra settings = %v", name, g["settings"])
		}
	}
}

func TestParseComfyAndDrawThings(t *testing.T) {
	graph := `{"3":{"class_type":"KSampler","inputs":{"seed":42,"steps":20,"cfg":6,"sampler_name":"dpmpp_2m","positive":["6",0],"negative":["7",0]}},
		"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"sd15.safetensors"}},
		"5":{"class_type":"EmptyLatentImage","inputs":{"width":512,"height":512}},
		"6":{"class_type":"CLIPTextEncode","inputs":{"text":"a coin"}},
		"7":{"class_type":"CLIPTextEncode","inputs":{"text":"ugly"}}}`
	imp := importFromChunks(readPNGText(bytes.NewReader(pngWithChunks(t,
		textChunkBytes("prompt", graph), textChunkBytes("workflow", `{"nodes":[]}`)))))
	if imp == nil || imp.prompt != "a coin" || imp.gen["tool"] != "ComfyUI" || imp.gen["seed"] != float64(42) ||
		imp.gen["model"] != "sd15.safetensors" || imp.gen["negative_prompt"] != "ugly" || imp.gen["width"] != 512 {
		t.Fatalf("comfy: %+v", imp)
	}
	if set, _ := imp.gen["settings"].(map[string]any); set["comfyui_workflow_embedded"] != true {
		t.Errorf("workflow flag missing: %v", imp.gen)
	}

	xmp := `<x:xmpmeta><rdf:RDF><rdf:Description><exif:UserComment><rdf:Alt><rdf:li xml:lang="x-default">` +
		`{&quot;c&quot;:&quot;a red potion&quot;,&quot;uc&quot;:&quot;blur&quot;,&quot;seed&quot;:99,&quot;steps&quot;:30,&quot;scale&quot;:5.5,&quot;sampler&quot;:&quot;DPM++&quot;,&quot;model&quot;:&quot;sdxl&quot;,&quot;size&quot;:&quot;1024x1024&quot;}` +
		`</rdf:li></rdf:Alt></exif:UserComment></rdf:Description></rdf:RDF></x:xmpmeta>`
	imp = importFromChunks(readPNGText(bytes.NewReader(pngWithChunks(t, itxtChunk("XML:com.adobe.xmp", xmp, false)))))
	if imp == nil || imp.prompt != "a red potion" || imp.gen["tool"] != "Draw Things" || imp.gen["model"] != "sdxl" ||
		imp.gen["negative_prompt"] != "blur" || imp.gen["width"] != 1024 {
		t.Fatalf("draw things xmp: %+v", imp)
	}
	imp = importFromChunks(readPNGText(bytes.NewReader(pngWithChunks(t, textChunkBytes("Comment", `{"c":"a gem","seed":7,"steps":8}`)))))
	if imp == nil || imp.prompt != "a gem" || imp.gen["steps"].(interface{ String() string }).String() != "8" {
		t.Fatalf("draw things text: %+v", imp)
	}

	// Unrelated text and a truncated file import nothing and do not panic.
	if importFromChunks(readPNGText(bytes.NewReader(pngWithChunks(t, textChunkBytes("Software", "gimp"))))) != nil {
		t.Error("unrelated text imported")
	}
	data := pngWithChunks(t, textChunkBytes("parameters", a1111Text))
	_ = readPNGText(bytes.NewReader(data[:60]))
}

func TestImportOnUploadFillsOnlyEmptyFields(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	png := pngWithChunks(t, textChunkBytes("parameters", a1111Text))

	a := mustAsset(t, s, AssetInput{Name: ptr("knight")}, "knight.png", png)
	got, err := s.GetAsset(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "a knight in plate armor, pixel art" || got.Generator["model"] != "pixelDiffusion" {
		t.Fatalf("not imported: %q %v", got.Prompt, got.Generator)
	}
	acts, _ := s.ListActivity(ctx, ActivityQuery{AssetID: a.ID})
	found := false
	for _, e := range acts {
		if e.Action == "asset.update" && e.Summary == "generator metadata imported" {
			found = true
		}
	}
	if !found {
		t.Errorf("no activity entry: %+v", acts)
	}

	// User data wins: an existing prompt and generator keys stay.
	b := mustAsset(t, s, AssetInput{Name: ptr("mine"), Prompt: ptr("my own prompt"), Generator: map[string]any{"seed": 5.0, "tool": "Draw Things"}}, "mine.png", png)
	got, _ = s.GetAsset(ctx, b.ID)
	if got.Prompt != "my own prompt" || got.Generator["seed"] != 5.0 || got.Generator["tool"] != "Draw Things" {
		t.Fatalf("user data overwritten: %q %v", got.Prompt, got.Generator)
	}
	if got.Generator["model"] != "pixelDiffusion" { // missing keys are filled
		t.Errorf("missing key not filled: %v", got.Generator)
	}
	// A file without metadata changes nothing and records nothing.
	c := mustAsset(t, s, AssetInput{Name: ptr("plain")}, "plain.png", pngBytes(t, 4, 4, color.White))
	acts, _ = s.ListActivity(ctx, ActivityQuery{AssetID: c.ID, Action: "asset.update"})
	if len(acts) != 0 {
		t.Errorf("unexpected import entry: %+v", acts)
	}
}

func TestAudioStudioSidecarImport(t *testing.T) {
	sidecar := []byte(`{"prompt":"TrackType: SFX, glass shattering","seed":1234,"audio_type":"sfx","duration":2.5,"settings":{"steps":8,"cfg":1.0}}`)
	for _, jsonFirst := range []bool{true, false} {
		s := newService(t)
		ctx := context.Background()
		a := mustAsset(t, s, AssetInput{Name: ptr("shatter")}, "", nil)
		add := func(name string, data []byte) {
			if _, err := s.AddFile(ctx, a.ID, name, bytes.NewReader(data), 0); err != nil {
				t.Fatal(err)
			}
		}
		if jsonFirst {
			add("shatter.json", sidecar)
			if g, _ := s.GetAsset(ctx, a.ID); g.Prompt != "" {
				t.Fatal("imported without a matching audio file")
			}
			add("shatter.wav", []byte("RIFF....WAVEdata"))
		} else {
			add("shatter.wav", []byte("RIFF....WAVEdata"))
			add("shatter.json", sidecar)
		}
		g, _ := s.GetAsset(ctx, a.ID)
		if g.Prompt != "TrackType: SFX, glass shattering" || g.Generator["tool"] != "audio-studio" ||
			g.Generator["seed"] != 1234.0 || g.Generator["audio_type"] != "sfx" || g.Generator["duration_s"] != 2.5 || g.Generator["steps"] != 8.0 {
			t.Errorf("jsonFirst=%v: %q %v", jsonFirst, g.Prompt, g.Generator)
		}
	}

	// Other json files are left alone, and filled fields are not overwritten.
	s := newService(t)
	ctx := context.Background()
	a := mustAsset(t, s, AssetInput{Name: ptr("x"), Prompt: ptr("keep me")}, "a.wav", []byte("RIFF"))
	if _, err := s.AddFile(ctx, a.ID, "a.json", bytes.NewReader(sidecar), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFile(ctx, a.ID, "other.json", strings.NewReader(`{"hello":"world"}`), 0); err != nil {
		t.Fatal(err)
	}
	g, _ := s.GetAsset(ctx, a.ID)
	if g.Prompt != "keep me" || g.Generator["tool"] != "audio-studio" {
		t.Errorf("prompt overwritten or generator missing: %q %v", g.Prompt, g.Generator)
	}
}

func TestLineageCyclesAndRecipe(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	root := mustAsset(t, s, AssetInput{Name: ptr("root"), Prompt: ptr("a knight"), Generator: map[string]any{"tool": "Draw Things", "seed": 1.0}}, "", nil)
	mid, _ := s.CreateAsset(ctx, AssetInput{Name: ptr("mid"), DerivedFrom: ptr(root.ID)})
	leaf, _ := s.CreateAsset(ctx, AssetInput{Name: ptr("leaf"), DerivedFrom: ptr(mid.ID), Prompt: ptr("a knight, cape")})
	sib, _ := s.CreateAsset(ctx, AssetInput{Name: ptr("sib"), DerivedFrom: ptr(mid.ID)})

	l, err := s.Lineage(ctx, leaf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Ancestors) != 2 || l.Ancestors[0].ID != mid.ID || l.Ancestors[1].ID != root.ID || len(l.Siblings) != 1 || l.Siblings[0].ID != sib.ID {
		t.Fatalf("lineage = %+v", l)
	}
	l, _ = s.Lineage(ctx, mid.ID)
	if len(l.Children) != 2 {
		t.Errorf("children = %+v", l.Children)
	}

	// Make the chain a cycle behind the API's back; the walk must terminate.
	if _, err := s.db.ExecContext(ctx, `UPDATE assets SET derived_from = ? WHERE id = ?`, leaf.ID, root.ID); err != nil {
		t.Fatal(err)
	}
	l, err = s.Lineage(ctx, leaf.ID)
	if err != nil || len(l.Ancestors) != 2 {
		t.Fatalf("cycle: %v %+v", err, l)
	}
	// A long chain stops at the depth limit.
	prev := ""
	var last string
	for i := range 30 {
		a, err := s.CreateAsset(ctx, AssetInput{Name: ptr("n" + string(rune('a'+i%26))), DerivedFrom: &prev})
		if err != nil {
			t.Fatal(err)
		}
		prev, last = a.ID, a.ID
	}
	l, _ = s.Lineage(ctx, last)
	if len(l.Ancestors) != maxLineageDepth {
		t.Errorf("ancestors = %d, want %d", len(l.Ancestors), maxLineageDepth)
	}

	// Superseded chain and supersedes.
	if _, err := s.db.ExecContext(ctx, `UPDATE assets SET superseded_by = ? WHERE id = ?`, sib.ID, leaf.ID); err != nil {
		t.Fatal(err)
	}
	l, _ = s.Lineage(ctx, leaf.ID)
	if len(l.Superseded) != 1 || l.Superseded[0].ID != sib.ID {
		t.Errorf("superseded = %+v", l.Superseded)
	}
	l, _ = s.Lineage(ctx, sib.ID)
	if len(l.Supersedes) != 1 || l.Supersedes[0].ID != leaf.ID {
		t.Errorf("supersedes = %+v", l.Supersedes)
	}
	if _, err := s.Lineage(ctx, "ast_000000000000"); err == nil {
		t.Error("unknown asset accepted")
	}

	r, err := s.GetRecipe(ctx, root.ID)
	if err != nil || !r.HasRecipe || r.Prompt != "a knight" || !strings.Contains(r.Reproduce, `"Draw Things"`) ||
		!strings.Contains(r.Reproduce, "derived_from = "+root.ID) || !strings.Contains(r.Reproduce, "Keep the seed") {
		t.Fatalf("recipe = %+v %v", r, err)
	}
	bare := mustAsset(t, s, AssetInput{Name: ptr("bare")}, "", nil)
	r, _ = s.GetRecipe(ctx, bare.ID)
	if r.HasRecipe || !strings.Contains(r.Reproduce, "cannot be reproduced") {
		t.Errorf("bare recipe = %+v", r)
	}
}

func TestActivityFeedEnrichmentPollingAndPrune(t *testing.T) {
	s := newService(t)
	ctx := WithActor(context.Background(), Actor{Via: "mcp", TokenName: "claude-code"})
	a := mustAsset(t, s, AssetInput{Name: ptr("coin")}, "coin.png", pngBytes(t, 4, 4, color.White))
	feed, err := s.ListActivityFeed(ctx, ActivityQuery{})
	if err != nil || len(feed.Items) == 0 || feed.LatestSeq != feed.Items[0].Seq {
		t.Fatalf("feed = %+v %v", feed, err)
	}
	if e := feed.Items[0]; e.AssetName != "coin" || e.ThumbVersion == 0 {
		t.Errorf("not enriched: %+v", e)
	}
	first := feed.LatestSeq
	for range 3 {
		s.record(ctx, nil, "note.add", a.ID, "", "note")
	}
	// Polling returns the oldest new entries first and honours the limit.
	feed, _ = s.ListActivityFeed(ctx, ActivityQuery{AfterSeq: first, Limit: 2})
	if len(feed.Items) != 2 || feed.Items[0].Seq != first+1 || feed.LatestSeq != first+3 {
		t.Fatalf("polling = %+v", feed)
	}
	feed, _ = s.ListActivityFeed(ctx, ActivityQuery{Action: "asset.,note.", Via: "mcp"})
	if len(feed.Items) != 3 {
		t.Errorf("multi-prefix / via filter: %d", len(feed.Items))
	}
	if feed, _ = s.ListActivityFeed(ctx, ActivityQuery{Via: "web"}); len(feed.Items) != 0 {
		t.Errorf("via=web matched %d", len(feed.Items))
	}
	// Entries of a purged asset stay and are marked.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	feed, _ = s.ListActivityFeed(ctx, ActivityQuery{AssetID: a.ID})
	if len(feed.Items) == 0 || !feed.Items[0].AssetGone {
		t.Errorf("purged asset not marked: %+v", feed.Items)
	}

	// Retention.
	old := time.Now().UTC().AddDate(0, 0, -400).Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, `UPDATE activity SET at = ? WHERE seq = ?`, old, first); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -ActivityRetentionDays).Format(time.RFC3339)
	n, err := s.PruneActivity(ctx, cutoff)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if n, _ = s.PruneActivity(ctx, cutoff); n != 0 {
		t.Errorf("second prune removed %d", n)
	}
}
