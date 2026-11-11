package hoard

import (
	"context"
	"encoding/json"
	"errors"
	"image/color"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRebuildFromSidecars(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := Open(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	col, _ := s.SaveCollection(ctx, "", GroupInput{Name: ptr("Dungeon pack"), Kind: ptr("style")})
	prj, _ := s.SaveProject(ctx, "", GroupInput{Name: ptr("Rogue")})
	a := mustAsset(t, s, AssetInput{
		Name: ptr("Torch"), Type: ptr("sprite"), Tags: &[]string{"fire", "pixel"}, Description: ptr("a lit torch"),
		Attrs: map[string]any{"frame_width": 16.0}, Status: ptr("approved"),
		Generator:      map[string]any{"tool": "Draw Things", "seed": 7.0},
		AddCollections: []string{col.ID}, AddProjects: []string{prj.ID}, ProjectNote: "wall light",
	}, "torch.png", pngBytes(t, 8, 4, color.White))
	b := mustAsset(t, s, AssetInput{Name: ptr("Gone"), DerivedFrom: &a.ID}, "gone.png", pngBytes(t, 2, 2, color.Black))
	if _, err := s.Bulk(ctx, BulkOp{IDs: []string{b.ID}, Action: "trash"}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetAsset(ctx, a.ID)
	s.Close()

	// Lose the index, and plant things the import must refuse.
	for _, f := range []string{"hoard.db", "hoard.db-wal", "hoard.db-shm"} {
		os.Remove(filepath.Join(dir, f))
	}
	os.MkdirAll(filepath.Join(dir, "assets", "ast_bad"), 0o750)
	os.WriteFile(filepath.Join(dir, "assets", "ast_bad", "asset.json"), []byte("{}"), 0o640)
	broken := filepath.Join(dir, "assets", "ast_aaaaaaaaaaaa")
	os.MkdirAll(broken, 0o750)
	os.WriteFile(filepath.Join(broken, "asset.json"), []byte("{not json"), 0o640)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "asset.json"), []byte(`{"id":"ast_bbbbbbbbbbbb","name":"escape"}`), 0o640)
	if err := os.Symlink(outside, filepath.Join(dir, "assets", "ast_bbbbbbbbbbbb")); err != nil {
		t.Fatal(err)
	}
	// A tampered sidecar: wrong hash and size, a file name that climbs out,
	// and a file that is not there.
	sc := filepath.Join(dir, "assets", a.ID, "asset.json")
	raw, _ := os.ReadFile(sc)
	var m map[string]any
	json.Unmarshal(raw, &m)
	files := m["files"].([]any)
	files[0].(map[string]any)["sha256"] = "deadbeef"
	files[0].(map[string]any)["size"] = 999999.0
	m["files"] = append(files,
		map[string]any{"id": "fil_zzzzzzzzzzzz", "filename": "../../hoard.db"},
		map[string]any{"id": "fil_yyyyyyyyyyyy", "filename": "missing.png"})
	raw, _ = json.Marshal(m)
	os.WriteFile(sc, raw, 0o640)

	s, err = Open(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.WaitRebuild()

	got, err := s.GetAsset(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Torch" || got.Type != "sprite" || got.Status != "approved" || got.Description != "a lit torch" ||
		got.CreatedAt != before.CreatedAt || got.UpdatedAt != before.UpdatedAt || got.Generator["tool"] != "Draw Things" ||
		got.Attrs["frame_width"] != 16.0 {
		t.Errorf("metadata not restored: %+v", got)
	}
	if strings.Join(got.Tags, ",") != "fire,pixel" {
		t.Errorf("tags: %v", got.Tags)
	}
	if len(got.Files) != 1 || got.Files[0].ID != before.Files[0].ID || got.Files[0].SHA256 != before.Files[0].SHA256 ||
		got.Files[0].Size != before.Files[0].Size || got.Files[0].Width != 8 || got.PrimaryFileID != before.PrimaryFileID {
		t.Errorf("files not recomputed from disk: %+v", got.Files)
	}
	if got.ThumbVersion == 0 {
		t.Error("thumbnail on disk not noticed")
	}
	if len(got.Collections) != 1 || got.Collections[0].ID != col.ID || got.Collections[0].Name != "Dungeon pack" || got.Collections[0].Kind != "style" {
		t.Errorf("collections: %+v", got.Collections)
	}
	if len(got.Projects) != 1 || got.Projects[0].ID != prj.ID || got.Projects[0].Note != "wall light" {
		t.Errorf("projects: %+v", got.Projects)
	}
	gone, err := s.GetAsset(ctx, b.ID)
	if err != nil || gone.DeletedAt == nil || gone.DerivedFrom != a.ID {
		t.Errorf("trashed asset: %+v %v", gone, err)
	}
	res, err := s.ListAssets(ctx, ListQuery{Q: "torch"})
	if err != nil || res.Total != 1 {
		t.Errorf("full-text search after rebuild: %v %v", res, err)
	}
	if _, err := s.GetAsset(ctx, "ast_bbbbbbbbbbbb"); !errors.Is(err, ErrNotFound) {
		t.Error("asset behind a symlink was imported")
	}
	if all, _ := s.ListAssets(ctx, ListQuery{Trashed: true}); all.Total != 1 {
		t.Errorf("trash: %+v", all)
	}
	if st, _ := s.GetStats(ctx); st.Assets != 1 {
		t.Errorf("stats: %+v", st)
	}
}

func TestNoRebuildWhenIndexHasAssets(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := Open(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	a := mustAsset(t, s, AssetInput{Name: ptr("Keep")}, "k.png", pngBytes(t, 2, 2, color.White))
	s.Close()
	s, err = Open(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.rebuildDone != nil {
		t.Fatal("import started although the index has assets")
	}
	if _, err := s.GetAsset(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
}
