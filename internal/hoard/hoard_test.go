package hoard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func newService(t *testing.T) *Service {
	t.Helper()
	s, err := Open(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func pngBytes(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func ptr[T any](v T) *T { return &v }

func mustAsset(t *testing.T, s *Service, in AssetInput, filename string, data []byte) *Asset {
	t.Helper()
	ctx := context.Background()
	a, err := s.CreateAsset(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if filename != "" {
		if _, err := s.AddFile(ctx, a.ID, filename, bytes.NewReader(data), 0); err != nil {
			t.Fatal(err)
		}
	}
	a, err = s.GetAsset(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAssetLifecycle(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	a := mustAsset(t, s, AssetInput{AddTags: []string{"Pixel Art", "weapon,  Sword "}, Prompt: ptr("a tiny sword")},
		"../../etc/Knight Sword.png", pngBytes(t, 600, 300, color.NRGBA{200, 30, 30, 255}))

	if a.Name != "Knight Sword" || a.Type != "sprite" {
		t.Errorf("name/type derived from file = %q/%q", a.Name, a.Type)
	}
	if got := strings.Join(a.Tags, ","); got != "pixel-art,sword,weapon" {
		t.Errorf("tags = %s", got)
	}
	if len(a.Files) != 1 || a.Files[0].Filename != "Knight Sword.png" || a.Files[0].Width != 600 {
		t.Fatalf("file = %+v", a.Files)
	}
	if a.ThumbVersion != 1 || a.PreviewKind != KindImage {
		t.Errorf("thumb=%d kind=%s", a.ThumbVersion, a.PreviewKind)
	}
	// Nothing may escape the asset directory, whatever the upload was called.
	want := filepath.Join(s.DataDir(), "assets", a.ID, "files", "Knight Sword.png")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("file not at %s: %v", want, err)
	}
	if p, mime := s.ThumbPath(a.ID); p == "" || mime != "image/jpeg" {
		t.Errorf("opaque thumbnail should be JPEG, got %q %q", p, mime)
	}

	// The sidecar mirrors the record.
	raw, err := os.ReadFile(filepath.Join(s.DataDir(), "assets", a.ID, "asset.json"))
	if err != nil {
		t.Fatal(err)
	}
	var side Asset
	if err := json.Unmarshal(raw, &side); err != nil || side.Prompt != "a tiny sword" || len(side.Files) != 1 {
		t.Errorf("sidecar out of sync: %v %+v", err, side)
	}

	// Explicit edits stop the automatic guesses.
	a, err = s.UpdateAsset(ctx, a.ID, AssetInput{Name: ptr("Hero Blade"), Type: ptr("ui"), License: ptr("CC0-1.0"), Attrs: map[string]any{"frames": 4}})
	if err != nil {
		t.Fatal(err)
	}
	if a.NameIsAuto() || a.TypeIsAuto() || a.Attrs["frames"] != float64(4) {
		t.Errorf("update not applied: %+v", a)
	}
	if _, err := s.UpdateAsset(ctx, a.ID, AssetInput{Type: ptr("nonsense")}); err == nil {
		t.Error("invalid type accepted")
	}
	if _, err := s.UpdateAsset(ctx, a.ID, AssetInput{SourceURL: ptr("javascript:alert(1)")}); err == nil {
		t.Error("non-http URL accepted")
	}

	// Trash hides, restore brings back, purge removes files.
	if _, err := s.Bulk(ctx, BulkOp{IDs: []string{a.ID}, Action: "trash"}); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.ListAssets(ctx, ListQuery{}); res.Total != 0 {
		t.Error("trashed asset still listed")
	}
	if res, _ := s.ListAssets(ctx, ListQuery{Trashed: true}); res.Total != 1 {
		t.Error("trashed asset not in trash")
	}
	if _, err := s.Bulk(ctx, BulkOp{IDs: []string{a.ID}, Action: "restore"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bulk(ctx, BulkOp{IDs: []string{a.ID}, Action: "purge"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.DataDir(), "assets", a.ID)); !os.IsNotExist(err) {
		t.Error("purge left files behind")
	}
	if _, err := s.GetAsset(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("purged asset still there: %v", err)
	}
	if res, _ := s.ListAssets(ctx, ListQuery{Q: "blade"}); res.Total != 0 {
		t.Error("purged asset still in the search index")
	}
}

func TestSearchAndFilters(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	sword := mustAsset(t, s, AssetInput{Name: ptr("Flaming Sword"), AddTags: []string{"weapon", "fire"}, License: ptr("CC0-1.0")}, "sword.png", pngBytes(t, 16, 16, color.Transparent))
	mustAsset(t, s, AssetInput{Description: ptr("Looping forest ambience with birds")}, "forest_theme.ogg", []byte("OggS-not-really"))
	shader := mustAsset(t, s, AssetInput{Prompt: ptr("a dissolve effect with burning edges")}, "dissolve.gdshader", []byte("shader_type canvas_item;"))

	names := func(q ListQuery) string {
		res, err := s.ListAssets(ctx, q)
		if err != nil {
			t.Fatalf("%+v: %v", q, err)
		}
		var out []string
		for _, a := range res.Items {
			out = append(out, a.Name)
		}
		return strings.Join(out, "|")
	}
	for _, tc := range []struct {
		q    ListQuery
		want string
	}{
		{ListQuery{Q: "flam swo"}, "Flaming Sword"},    // prefix match on every term
		{ListQuery{Q: "birds"}, "forest_theme"},        // description
		{ListQuery{Q: "burning"}, "dissolve"},          // prompt
		{ListQuery{Q: "gdshader"}, "dissolve"},         // file name
		{ListQuery{Q: `"; DROP TABLE assets; --`}, ""}, // hostile input is just text
		{ListQuery{Q: "sword OR (", Sort: "name"}, ""}, // FTS syntax is neutralised
		{ListQuery{Types: []string{"music", "shader"}, Sort: "name"}, "dissolve|forest_theme"},
		{ListQuery{Tags: []string{"weapon", "fire"}}, "Flaming Sword"},
		{ListQuery{Tags: []string{"weapon", "ice"}}, ""},
		{ListQuery{Untagged: true, Sort: "name"}, "dissolve|forest_theme"},
		{ListQuery{License: "none", Sort: "name"}, "dissolve|forest_theme"},
		{ListQuery{License: "CC0-1.0"}, "Flaming Sword"},
		{ListQuery{Sort: "size", Desc: ptr(false)}, "forest_theme|dissolve|Flaming Sword"},
	} {
		if got := names(tc.q); got != tc.want {
			t.Errorf("%+v = %q, want %q", tc.q, got, tc.want)
		}
	}
	if _, err := s.ListAssets(ctx, ListQuery{Sort: "bogus"}); err == nil {
		t.Error("unknown sort accepted")
	}

	// Groups, usage tracking and bulk edits.
	col, err := s.SaveCollection(ctx, "", GroupInput{Name: ptr("Fire Pack"), Kind: ptr("pack")})
	if err != nil {
		t.Fatal(err)
	}
	prj, err := s.SaveProject(ctx, "", GroupInput{Name: ptr("Emberfall")})
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.Bulk(ctx, BulkOp{IDs: []string{sword.ID, shader.ID}, Patch: AssetInput{
		AddTags: []string{"fire-pack"}, AddCollections: []string{col.ID}, AddProjects: []string{prj.ID}, ProjectNote: "boss fight",
	}})
	if err != nil || n != 2 {
		t.Fatalf("bulk = %d, %v", n, err)
	}
	if got := names(ListQuery{Collection: col.ID, Sort: "name"}); got != "dissolve|Flaming Sword" {
		t.Errorf("collection members = %q", got)
	}
	if got := names(ListQuery{Project: "none"}); got != "forest_theme" {
		t.Errorf("unused assets = %q", got)
	}
	full, _ := s.GetAsset(ctx, sword.ID)
	if len(full.Projects) != 1 || full.Projects[0].Note != "boss fight" || len(full.Collections) != 1 {
		t.Errorf("memberships = %+v %+v", full.Collections, full.Projects)
	}
	if _, err := s.Bulk(ctx, BulkOp{IDs: []string{sword.ID}, Patch: AssetInput{AddCollections: []string{"col_missing"}}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown collection: %v", err)
	}
	if c, _ := s.GetCollection(ctx, col.ID); c.AssetCount != 2 || len(c.Covers) != 1 {
		t.Errorf("collection = %+v", c)
	}

	// Renaming a tag merges and re-indexes.
	if n, err := s.RenameTag(ctx, "fire-pack", "fire"); err != nil || n != 2 {
		t.Fatalf("rename = %d, %v", n, err)
	}
	if got := names(ListQuery{Q: "fire", Sort: "name"}); got != "dissolve|Flaming Sword" {
		t.Errorf("after rename = %q", got)
	}
	st, err := s.GetStats(ctx)
	if err != nil || st.Assets != 3 || st.ByType["shader"] != 1 || st.Untagged != 1 {
		t.Errorf("stats = %+v, %v", st, err)
	}
}

func TestFiles(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	a := mustAsset(t, s, AssetInput{}, "scene.bin", []byte("binary blob"))
	if a.PreviewKind != KindNone {
		t.Fatalf("kind = %s", a.PreviewKind)
	}
	// A previewable file added later takes over as primary and sets the type.
	res, err := s.AddFile(ctx, a.ID, "scene.gltf", strings.NewReader(`{"asset":{"version":"2.0"}}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetAsset(ctx, a.ID)
	if a.PrimaryFileID != res.File.ID || a.Type != "model" || a.FileCount != 2 {
		t.Errorf("primary=%s type=%s files=%d", a.PrimaryFileID, a.Type, a.FileCount)
	}
	// Same name replaces; identical content elsewhere is reported.
	if res, err = s.AddFile(ctx, a.ID, "scene.bin", strings.NewReader("new blob"), 0); err != nil || !res.Replaced {
		t.Errorf("replace: %+v %v", res, err)
	}
	b := mustAsset(t, s, AssetInput{}, "", nil)
	if res, err = s.AddFile(ctx, b.ID, "copy.bin", strings.NewReader("new blob"), 0); err != nil || len(res.DuplicateOf) != 1 || res.DuplicateOf[0] != a.ID {
		t.Errorf("duplicate detection: %+v %v", res, err)
	}
	// Limits.
	if _, err := s.AddFile(ctx, a.ID, "big.bin", strings.NewReader("0123456789"), 5); !errors.Is(err, ErrTooLarge) {
		t.Errorf("size limit: %v", err)
	}
	if _, err := s.AddFile(ctx, a.ID, "empty.bin", strings.NewReader(""), 0); err == nil {
		t.Error("empty file accepted")
	}
	// Reads go through the index only.
	for _, name := range []string{"../asset.json", "../../hoard.db", "nope.bin"} {
		if _, _, err := s.OpenFile(ctx, a.ID, name); !errors.Is(err, ErrNotFound) {
			t.Errorf("OpenFile(%q) = %v", name, err)
		}
	}
	if _, _, err := s.OpenFile(ctx, "../..", "hoard.db"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad asset id: %v", err)
	}
	// Deleting the primary re-points it.
	if err := s.DeleteFile(ctx, a.ID, a.PrimaryFileID); err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetAsset(ctx, a.ID)
	if a.FileCount != 1 || a.Primary() == nil || a.Primary().Filename != "scene.bin" {
		t.Errorf("after delete: %+v", a.Files)
	}
	// Client thumbnails are decoded and re-encoded; junk is refused.
	if err := s.SetThumbnail(ctx, a.ID, []byte("<svg onload=alert(1)>")); err == nil {
		t.Error("non-image thumbnail accepted")
	}
	if err := s.SetThumbnail(ctx, a.ID, pngBytes(t, 900, 100, color.Transparent)); err != nil {
		t.Fatal(err)
	}
	if p, mime := s.ThumbPath(a.ID); p == "" || mime != "image/png" {
		t.Errorf("transparent thumbnail should be PNG: %q %q", p, mime)
	}
}

func TestAuth(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if _, err := s.SetupFirstUser(ctx, "alice", "short"); err == nil {
		t.Error("weak password accepted")
	}
	u, err := s.SetupFirstUser(ctx, "alice", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetupFirstUser(ctx, "eve", "another long password"); !errors.Is(err, ErrConflict) {
		t.Errorf("second setup: %v", err)
	}
	if _, _, err := s.Login(ctx, "ALICE", "wrong password!"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if _, _, err := s.Login(ctx, "nobody", "correct horse battery"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("unknown user: %v", err)
	}
	session, _, err := s.Login(ctx, "ALICE", "correct horse battery") // usernames are case-insensitive
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.SessionUser(ctx, session); err != nil || got.ID != u.ID {
		t.Errorf("session: %v", err)
	}
	if _, err := s.SessionUser(ctx, "forged"); err == nil {
		t.Error("forged session accepted")
	}

	tok, secret, err := s.CreateToken(ctx, u.ID, "claude")
	if err != nil || !strings.HasPrefix(secret, "hoard_") {
		t.Fatal(err)
	}
	if got, err := s.TokenUser(ctx, secret); err != nil || got.ID != u.ID {
		t.Errorf("token: %v", err)
	}
	// Secrets are stored hashed only.
	var stored string
	if err := s.db.QueryRow(`SELECT token_hash || prefix FROM api_tokens`).Scan(&stored); err != nil || strings.Contains(stored, secret) {
		t.Error("token secret stored in clear")
	}
	if err := s.DeleteToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TokenUser(ctx, secret); err == nil {
		t.Error("revoked token accepted")
	}

	// Changing the password ends the other sessions.
	other, _, _ := s.Login(ctx, "alice", "correct horse battery")
	if err := s.ChangePassword(ctx, u.ID, "correct horse battery", "an even better passphrase", session); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, other); err == nil {
		t.Error("other session survived the password change")
	}
	if _, err := s.SessionUser(ctx, session); err != nil {
		t.Error("current session was dropped")
	}
	if err := s.DeleteUser(ctx, u.ID); err == nil {
		t.Error("last user deleted")
	}
}

func TestSignedLinks(t *testing.T) {
	s := newService(t)
	exp, sig := s.Sign("download:ast_x", time.Minute)
	if !s.Verify("download:ast_x", exp, sig) {
		t.Error("valid signature rejected")
	}
	if s.Verify("download:ast_y", exp, sig) || s.Verify("upload:ast_x", exp, sig) {
		t.Error("signature valid for another payload")
	}
	if s.Verify("download:ast_x", "99999999999", sig) {
		t.Error("expiry is not covered by the signature")
	}
	exp, sig = s.Sign("download:ast_x", -time.Second)
	if s.Verify("download:ast_x", exp, sig) {
		t.Error("expired link accepted")
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd": "passwd", `C:\Users\x\model.FBX`: "model.FBX", ".htaccess": "htaccess", "a<b>:c?.png": "a_b_c_.png", "": "file", "...": "file",
		"Knight's $word`x`.png": "Knight_s _word_x_.png", "A';calc;echo '.png": "A_;calc;echo _.png", "tile[0] (1) & co.png": "tile[0] (1) & co.png",
	} {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{" Pixel  Art ": "pixel-art", "Sci-Fi": "sci-fi", "C#": "c#", "--x--": "x", "héros/épée": "héros-épée"} {
		if got := NormalizeTag(in); got != want {
			t.Errorf("NormalizeTag(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"hero.png": "sprite", "rock_normal.png": "texture", "dungeon_tileset.png": "tileset", "btn_play.png": "ui", "boss_theme.ogg": "music",
		"jump.wav": "sfx", "vo_intro_line.mp3": "voice", "crate.glb": "model", "water.gdshader": "shader", "Inter.ttf": "font",
		"intro.mp4": "video", "readme.unknownext": "other",
	} {
		if got := GuessType(in, 1000); got != want {
			t.Errorf("GuessType(%q) = %q, want %q", in, got, want)
		}
	}
	if MimeFor("evil.html") != "application/octet-stream" || MimeFor("x.svg") != "image/svg+xml" {
		t.Error("unexpected MIME mapping")
	}
}

func TestHardening(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	// Long names are cut on a character boundary and stay addressable.
	long := strings.Repeat("ä", 200) + ".png"
	name := SanitizeFilename(long)
	if len(name) > 180 || !utf8.ValidString(name) || !strings.HasSuffix(name, ".png") {
		t.Errorf("truncated name invalid: %d bytes, valid=%v", len(name), utf8.ValidString(name))
	}
	a := mustAsset(t, s, AssetInput{}, long, []byte("x"))
	if fh, _, err := s.OpenFile(ctx, a.ID, a.Files[0].Filename); err != nil {
		t.Errorf("file with truncated name cannot be opened: %v", err)
	} else {
		fh.Close()
	}

	// Names differing only in case are one file.
	b := mustAsset(t, s, AssetInput{}, "Sprite.PNG", []byte("first"))
	res, err := s.AddFile(ctx, b.ID, "sprite.png", strings.NewReader("second"), 0)
	if err != nil || !res.Replaced {
		t.Fatalf("case-variant upload: %+v %v", res, err)
	}
	b, _ = s.GetAsset(ctx, b.ID)
	if b.FileCount != 1 || b.Files[0].Filename != "sprite.png" || b.PrimaryFileID != res.File.ID {
		t.Errorf("after case-variant upload: %+v primary=%s", b.Files, b.PrimaryFileID)
	}
	entries, _ := os.ReadDir(filepath.Join(s.DataDir(), "assets", b.ID, "files"))
	if len(entries) != 1 {
		t.Errorf("%d files on disk, want 1", len(entries))
	}

	// Trashed assets are read-only.
	if _, err := s.Bulk(ctx, BulkOp{IDs: []string{b.ID}, Action: "trash"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFile(ctx, b.ID, "late.bin", strings.NewReader("x"), 0); err == nil {
		t.Error("file added to a trashed asset")
	}

	// Control characters in a query are text, not an error.
	if _, err := s.ListAssets(ctx, ListQuery{Q: "a\x00b\x01"}); err != nil {
		t.Errorf("control characters in query: %v", err)
	}

	// The decoded-size budget, not the file size, bounds image decoding.
	if decodedSize(image.Config{Width: 11200, Height: 11200, ColorModel: color.NRGBA64Model}) <= maxDecodedBytes {
		t.Error("a 16-bit 125 MP image is within the decode budget")
	}
	if decodedSize(image.Config{Width: 4096, Height: 4096, ColorModel: color.NRGBAModel}) > maxDecodedBytes {
		t.Error("an ordinary 4K texture exceeds the decode budget")
	}

	// The sidecar follows changes that are not metadata edits.
	if err := s.SetAIStatus(ctx, a.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.DataDir(), "assets", a.ID, "asset.json"))
	if !strings.Contains(string(raw), `"ai_status": "done"`) {
		t.Error("sidecar not updated after an AI status change")
	}

	// The index is private to its owner.
	if st, err := os.Stat(filepath.Join(s.DataDir(), "hoard.db")); err != nil || st.Mode().Perm()&0o077 != 0 {
		t.Errorf("hoard.db mode = %v, %v", st.Mode().Perm(), err)
	}

	// Expired trash is purged in batches, however much there is.
	var ids []string
	for range 1200 {
		c, err := s.CreateAsset(ctx, AssetInput{Name: ptr("bulk")})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	if _, err := s.Bulk(ctx, BulkOp{IDs: ids, Action: "trash"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeExpiredTrash(ctx, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if err != nil || n != 1201 { // the 1200 plus the asset trashed above
		t.Errorf("purged %d, %v", n, err)
	}
}

func TestSetupIsAtomic(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var created atomic.Int32
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.SetupFirstUser(ctx, fmt.Sprintf("user%d", i), "a long enough password"); err == nil {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if n, _ := s.UserCount(ctx); created.Load() != 1 || n != 1 {
		t.Errorf("%d setups succeeded, %d users exist; want exactly one", created.Load(), n)
	}
}
