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
	"time"
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

// lostIndex closes the service, removes the database and returns the data dir.
func lostIndex(t *testing.T, s *Service) string {
	t.Helper()
	dir := s.DataDir()
	s.Close()
	for _, f := range []string{"hoard.db", "hoard.db-wal", "hoard.db-shm"} {
		os.Remove(filepath.Join(dir, f))
	}
	return dir
}

func reopen(t *testing.T, dir string) *Service {
	t.Helper()
	s, err := Open(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.WaitRebuild()
	return s
}

func editSidecar(t *testing.T, dir, id string, f func(m map[string]any)) {
	t.Helper()
	p := filepath.Join(dir, "assets", id, "asset.json")
	raw, _ := os.ReadFile(p)
	var m map[string]any
	json.Unmarshal(raw, &m)
	f(m)
	raw, _ = json.Marshal(m)
	os.WriteFile(p, raw, 0o640)
}

func TestRebuildDoesNotFollowSymlinkedFilesOrThumbs(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	a := mustAsset(t, s, AssetInput{Name: ptr("Victim")}, "pic.png", pngBytes(t, 4, 4, color.White))
	dir := lostIndex(t, s)

	secret := t.TempDir()
	os.WriteFile(filepath.Join(secret, "secret.txt"), []byte("top secret"), 0o600)
	os.WriteFile(filepath.Join(secret, "pic.png"), pngBytes(t, 3, 3, color.Black), 0o600)
	// files/ itself is a link out of the data dir.
	files := filepath.Join(dir, "assets", a.ID, "files")
	os.RemoveAll(files)
	if err := os.Symlink(secret, files); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "assets", a.ID, "thumb.png"))
	os.Symlink(filepath.Join(secret, "pic.png"), filepath.Join(dir, "assets", a.ID, "thumb.png"))
	editSidecar(t, dir, a.ID, func(m map[string]any) {
		m["files"] = []any{map[string]any{"id": "fil_aaaaaaaaaaaa", "filename": "secret.txt"}, map[string]any{"id": "fil_bbbbbbbbbbbb", "filename": "pic.png"}}
	})

	s = reopen(t, dir)
	got, err := s.GetAsset(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 0 || got.PrimaryFileID != "" {
		t.Errorf("files behind a symlinked directory were indexed: %+v", got.Files)
	}
	if got.ThumbVersion != 0 {
		t.Error("a symlinked thumbnail counted")
	}
	if _, _, err := s.OpenFile(ctx, a.ID, "secret.txt"); err == nil {
		t.Error("secret.txt can be opened")
	}

	// A single symlinked file inside a real directory is skipped as well.
	s2 := newService(t)
	b := mustAsset(t, s2, AssetInput{Name: ptr("B")}, "ok.png", pngBytes(t, 4, 4, color.White))
	dir2 := lostIndex(t, s2)
	os.Symlink(filepath.Join(secret, "secret.txt"), filepath.Join(dir2, "assets", b.ID, "files", "link.txt"))
	editSidecar(t, dir2, b.ID, func(m map[string]any) {
		m["files"] = append(m["files"].([]any), map[string]any{"id": "fil_cccccccccccc", "filename": "link.txt"})
	})
	s2 = reopen(t, dir2)
	gb, _ := s2.GetAsset(ctx, b.ID)
	if len(gb.Files) != 1 || gb.Files[0].Filename != "ok.png" {
		t.Errorf("files: %+v", gb.Files)
	}
}

func TestRebuildReusedFileIDGetsANewOne(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	a := mustAsset(t, s, AssetInput{Name: ptr("A")}, "a.png", pngBytes(t, 4, 4, color.White))
	b := mustAsset(t, s, AssetInput{Name: ptr("B")}, "b.png", pngBytes(t, 5, 5, color.White))
	dir := lostIndex(t, s)
	shared := a.Files[0].ID
	editSidecar(t, dir, b.ID, func(m map[string]any) {
		m["files"].([]any)[0].(map[string]any)["id"] = shared
		m["primary_file_id"] = shared
	})
	s = reopen(t, dir)
	ga, _ := s.GetAsset(ctx, a.ID)
	gb, _ := s.GetAsset(ctx, b.ID)
	if len(ga.Files) != 1 || len(gb.Files) != 1 {
		t.Fatalf("files: %+v %+v", ga.Files, gb.Files)
	}
	if ga.Files[0].ID == gb.Files[0].ID {
		t.Fatal("two assets share a file id")
	}
	for _, g := range []*Asset{ga, gb} {
		if g.PrimaryFileID != g.Files[0].ID {
			t.Errorf("%s: primary %s does not point at its own file %s", g.ID, g.PrimaryFileID, g.Files[0].ID)
		}
	}
}

func TestRebuildResumesAfterRestart(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	a := mustAsset(t, s, AssetInput{Name: ptr("A")}, "a.png", pngBytes(t, 4, 4, color.White))
	b := mustAsset(t, s, AssetInput{Name: ptr("B")}, "b.png", pngBytes(t, 5, 5, color.White))
	dir := lostIndex(t, s)
	s = reopen(t, dir)
	if v, _ := s.GetSetting(ctx, rebuildSetting); v != "" {
		t.Fatalf("flag still set after a complete import: %q", v)
	}
	// Simulate a restart half way: B is missing and the flag is still set.
	if _, err := s.db.Exec(`DELETE FROM assets WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	s.SetSetting(ctx, rebuildSetting, "1")
	s.Close()
	s = reopen(t, dir)
	if _, err := s.GetAsset(ctx, b.ID); err != nil {
		t.Errorf("rest of the import was not resumed: %v", err)
	}
	if _, err := s.GetAsset(ctx, a.ID); err != nil {
		t.Error(err)
	}
	if v, _ := s.GetSetting(ctx, rebuildSetting); v != "" {
		t.Errorf("flag not cleared: %q", v)
	}
	// With the flag clear and assets present, nothing starts again.
	s.Close()
	s = reopen(t, dir)
	if s.rebuildDone != nil {
		t.Error("import started again")
	}
}

func TestRebuildCancelledKeepsTheFlag(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	mustAsset(t, s, AssetInput{Name: ptr("A")}, "a.png", pngBytes(t, 4, 4, color.White))
	dir := lostIndex(t, s)
	s, err := Open(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.rebuildCancel() // as Close does mid-run
	s.WaitRebuild()
	// Whether the goroutine finished first or not, closing must not lose the flag unless the import completed.
	n, _ := s.ListAssets(ctx, ListQuery{})
	flag, _ := s.GetSetting(ctx, rebuildSetting)
	if n.Total == 0 && flag == "" {
		t.Error("interrupted import cleared its flag with nothing imported")
	}
	s.Close()
}

func TestRebuildDistrustsSidecarValues(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	a := mustAsset(t, s, AssetInput{Name: ptr("A")}, "a.png", pngBytes(t, 4, 4, color.White))
	dir := lostIndex(t, s)
	tags := make([]any, 500)
	for i := range tags {
		tags[i] = "t" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))
	}
	editSidecar(t, dir, a.ID, func(m map[string]any) {
		m["deleted_at"] = "0001-01-01T00:00:00Z"
		m["created_at"] = "2999-01-01T00:00:00Z"
		m["updated_at"] = "2021-03-04T05:06:07+02:00"
		m["description"] = strings.Repeat("x", 30000)
		m["derived_from"] = a.ID
		m["superseded_by"] = a.ID
		m["status"] = "bogus"
		m["source_url"] = "javascript:alert(1)"
		m["tags"] = tags
	})
	s = reopen(t, dir)
	got, _ := s.GetAsset(ctx, a.ID)
	if got.DeletedAt == nil || *got.DeletedAt < "2000" {
		t.Errorf("deleted_at: %v", got.DeletedAt)
	}
	if n, _ := s.PurgeExpiredTrash(ctx, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)); n != 0 {
		t.Error("a forged ancient deleted_at made the asset purgeable at once")
	}
	if got.CreatedAt > time.Now().Add(48*time.Hour).UTC().Format(time.RFC3339) {
		t.Errorf("created_at in the future: %s", got.CreatedAt)
	}
	if got.UpdatedAt != "2021-03-04T03:06:07Z" {
		t.Errorf("updated_at not normalised to UTC: %s", got.UpdatedAt)
	}
	if len(got.Description) != 20000 || got.DerivedFrom != "" || got.SupersededBy != "" || got.Status != "" || got.SourceURL != "" || len(got.Tags) != 200 {
		t.Errorf("limits not applied: desc %d derived %q super %q status %q url %q tags %d",
			len(got.Description), got.DerivedFrom, got.SupersededBy, got.Status, got.SourceURL, len(got.Tags))
	}
}

func TestStatsReportRebuilding(t *testing.T) {
	s := newService(t)
	s.rebuilding.Store(true)
	st, _ := s.GetStats(context.Background())
	if !st.Rebuilding {
		t.Error("rebuilding flag missing")
	}
}
