package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toriath/hoard/internal/hoard"
)

type dlvMCP struct {
	t   *testing.T
	ctx context.Context
	s   *mcp.ClientSession
}

func newDlvMCP(t *testing.T, e *env) *dlvMCP {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   e.ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{e.token}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return &dlvMCP{t: t, ctx: ctx, s: session}
}

// call runs a tool and decodes its JSON text result.
func (m *dlvMCP) call(name string, args map[string]any) map[string]any {
	m.t.Helper()
	res, err := m.s.CallTool(m.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		m.t.Fatalf("%s: %v", name, err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError {
		m.t.Fatalf("%s failed: %s", name, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		m.t.Fatalf("%s: %v in %s", name, err, text)
	}
	return out
}

func (m *dlvMCP) fails(name string, args map[string]any) string {
	m.t.Helper()
	res, err := m.s.CallTool(m.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error()
	}
	if !res.IsError {
		m.t.Fatalf("%s unexpectedly succeeded", name)
	}
	return res.Content[0].(*mcp.TextContent).Text
}

// lastQuoted returns the last single-quoted argument of a shell command.
func lastQuoted(t *testing.T, cmd string) string {
	t.Helper()
	end := strings.LastIndex(cmd, "'")
	start := strings.LastIndex(cmd[:end], "'")
	if start < 0 || end < 0 {
		t.Fatalf("no quoted url in %q", cmd)
	}
	return cmd[start+1 : end]
}

func dlvPNG(w, h int, c color.RGBA) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func (e *env) raw(method, rawURL string, body []byte) (*http.Response, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(method, rawURL, bytes.NewReader(body))
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func withParam(t *testing.T, raw, key, val string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set(key, val)
	u.RawQuery = q.Encode()
	return u.String()
}

func TestRepoSnapshotLinks(t *testing.T) {
	e := newEnv(t)
	m := newDlvMCP(t, e)
	ctx := context.Background()
	p1, _ := e.svc.SaveProject(ctx, "", hoard.GroupInput{Name: dlvPtr("one")})
	p2, _ := e.svc.SaveProject(ctx, "", hoard.GroupInput{Name: dlvPtr("two")})

	out := m.call("get_repo_audit_command", map[string]any{"project_id": p1.ID})
	cmd := out["command"].(string)
	link := lastQuoted(t, cmd)
	if !strings.Contains(cmd, "curl -fsS --globoff -T - ") || !strings.Contains(link, "/projects/"+p1.ID+"/repo/snapshot?") {
		t.Fatalf("command = %s", cmd)
	}
	sha := strings.Repeat("ab", 32)
	good := []byte(sha + "\t10\tart/coin.png\n")

	// Wrong method, tampered signature, link of another project: all refused.
	resp, _ := e.raw("GET", link, nil)
	if resp.StatusCode == http.StatusOK {
		t.Error("GET stored a snapshot")
	}
	if resp, _ := e.raw("PUT", withParam(t, link, "sig", strings.Repeat("0", 40)), good); resp.StatusCode != http.StatusForbidden {
		t.Errorf("tampered signature: %d", resp.StatusCode)
	}
	other := strings.Replace(link, "/projects/"+p1.ID+"/", "/projects/"+p2.ID+"/", 1)
	if resp, _ := e.raw("PUT", other, good); resp.StatusCode != http.StatusForbidden {
		t.Errorf("link reused for another project: %d", resp.StatusCode)
	}
	if resp, _ := e.raw("PUT", withParam(t, link, "exp", "1"), good); resp.StatusCode != http.StatusForbidden {
		t.Errorf("expired link: %d", resp.StatusCode)
	}
	// Bad content.
	for name, body := range map[string][]byte{
		"traversal": []byte(sha + "\t1\t../../etc/passwd\n"),
		"garbage":   []byte("hello\n"),
		"empty":     nil,
	} {
		if resp, _ := e.raw("PUT", link, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, resp.StatusCode)
		}
	}
	if resp, body := e.raw("PUT", link, bytes.Repeat([]byte("x"), 9<<20+10)); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "larger") {
		t.Errorf("oversize: status %d", resp.StatusCode)
	}
	au, err := e.svc.AuditProject(ctx, p1.ID)
	if err != nil || au.HasSnapshot {
		t.Fatalf("rejected uploads left a snapshot: %v %+v", err, au)
	}
	// The real thing.
	resp, body := e.raw("PUT", link, good)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"files":1`) {
		t.Fatalf("snapshot: %d %s", resp.StatusCode, body)
	}

	// Lock file link: bound to the project, served as a download.
	plan := m.call("sync_project", map[string]any{"project_id": p1.ID})
	lock := lastQuoted(t, plan["lock_command"].(string))
	resp, body = e.raw("GET", lock, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Disposition"), "hoard.lock.json") || !strings.Contains(string(body), p1.ID) {
		t.Errorf("lock file: %d %s", resp.StatusCode, body)
	}
	if resp, _ := e.raw("GET", strings.Replace(lock, "/projects/"+p1.ID+"/", "/projects/"+p2.ID+"/", 1), nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("lock link reused: %d", resp.StatusCode)
	}
	if resp, _ := e.raw("GET", withParam(t, lock, "sig", "bad"), nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("tampered lock link: %d", resp.StatusCode)
	}
	// Session endpoints.
	e.do("GET", "/api/v1/projects/"+p1.ID+"/repo/audit", nil, nil, http.StatusOK, nil)
	var ac struct {
		Command string `json:"command"`
	}
	e.do("GET", "/api/v1/projects/"+p1.ID+"/repo/audit-command?root=%2Ftmp%2Fmy%20game", nil, nil, http.StatusOK, &ac)
	if !strings.Contains(ac.Command, "cd '/tmp/my game'") {
		t.Errorf("audit-command root: %s", ac.Command)
	}
	e.do("GET", "/api/v1/projects/prj_missing/repo/audit", nil, nil, http.StatusNotFound, nil)
}

// TestAuditCommandRuns executes the generated command in a real shell against
// the test server and audits the result.
func TestAuditCommandRuns(t *testing.T) {
	for _, bin := range []string{"sh", "curl", "find"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
	e := newEnv(t)
	m := newDlvMCP(t, e)
	ctx := context.Background()
	p, _ := e.svc.SaveProject(ctx, "", hoard.GroupInput{Name: dlvPtr("game")})

	coin := dlvPNG(6, 6, color.RGBA{255, 0, 0, 255})
	a := e.upload("coin.png", coin, `{"name":"coin"}`)
	if _, err := e.svc.UpdateAsset(ctx, a.ID, hoard.AssetInput{AddProjects: []string{p.ID}}); err != nil {
		t.Fatal(err)
	}
	e.upload("lost.png", dlvPNG(6, 6, color.RGBA{1, 2, 3, 255}), `{"name":"lost"}`)
	lost, _ := e.svc.ListAssets(ctx, hoard.ListQuery{Q: "lost"})
	if _, err := e.svc.UpdateAsset(ctx, lost.Items[0].ID, hoard.AssetInput{AddProjects: []string{p.ID}}); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	write := func(rel string, data []byte) {
		t.Helper()
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("art/coin.png", coin)
	write("art/my hero's sprite.png", dlvPNG(3, 3, color.RGBA{9, 8, 7, 255}))
	write("art/tab\tname.png", []byte("skipped: a tab cannot be reported"))
	write("sounds/step.OGG", []byte("OggS fake"))
	write("src/main.gd", []byte("extends Node"))
	write("notes.txt", []byte("text"))
	write("node_modules/pkg/logo.png", []byte("npm"))
	write(".git/objects/x.png", []byte("git"))
	write("Library/cache.png", []byte("unity"))

	cmd := m.call("get_repo_audit_command", map[string]any{"project_id": p.ID, "root": repo})["command"].(string)
	sh := exec.Command("sh", "-c", cmd)
	if outb, err := sh.CombinedOutput(); err != nil {
		t.Fatalf("command failed: %v\n%s\n%s", err, outb, cmd)
	}

	audit := m.call("audit_project", map[string]any{"project_id": p.ID})["audit"].(map[string]any)
	counts := audit["counts"].(map[string]any)
	if counts["files"] != 3.0 || counts["in_sync"] != 1.0 || counts["untracked"] != 2.0 || counts["missing"] != 1.0 {
		t.Fatalf("counts = %v\n%v", counts, audit)
	}
	var paths []string
	for _, u := range audit["unknown"].([]any) {
		paths = append(paths, u.(map[string]any)["path"].(string))
	}
	if strings.Join(paths, "|") != "art/my hero's sprite.png|sounds/step.OGG" {
		t.Errorf("unknown = %v", paths)
	}

	// Sync: the missing asset is fetched with the command we are given.
	plan := m.call("sync_project", map[string]any{"project_id": p.ID, "root": repo})
	dls := plan["downloads"].([]any)
	if len(dls) != 1 {
		t.Fatalf("plan = %v", plan)
	}
	d := dls[0].(map[string]any)
	if d["reason"] != "missing" || d["path"] != "assets/sprite/lost.png" {
		t.Errorf("download = %v", d)
	}
	for _, c := range []string{d["command"].(string), plan["lock_command"].(string)} {
		if outb, err := exec.Command("sh", "-c", c).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, outb)
		}
	}
	if b, err := os.ReadFile(filepath.Join(repo, "assets/sprite/lost.png")); err != nil || !bytes.Equal(b, dlvPNG(6, 6, color.RGBA{1, 2, 3, 255})) {
		t.Errorf("downloaded file: %v", err)
	}
	var lock hoard.RepoLock
	lb, err := os.ReadFile(filepath.Join(repo, "hoard.lock.json"))
	if err != nil || json.Unmarshal(lb, &lock) != nil || len(lock.Entries) != 2 {
		t.Errorf("lock file: %v %s", err, lb)
	}
	// Run the audit again: now in sync.
	if outb, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, outb)
	}
	plan = m.call("sync_project", map[string]any{"project_id": p.ID, "root": repo})
	if len(plan["downloads"].([]any)) != 0 || plan["summary"].(map[string]any)["unchanged"] != 2.0 {
		t.Errorf("second sync = %v", plan)
	}
}

func TestDerivativeLinkCoversParameters(t *testing.T) {
	e := newEnv(t)
	m := newDlvMCP(t, e)
	a := e.upload("tile.png", dlvPNG(8, 8, color.RGBA{200, 10, 10, 255}), `{"name":"tile"}`)

	out := m.call("get_derivative_command", map[string]any{"asset_id": a.ID, "filename": "tile.png", "scale": 2})
	if out["filter"] != "nearest" || out["width"] != 16.0 {
		t.Errorf("default filter / size: %v", out)
	}
	link := lastQuoted(t, out["command"].(string))
	resp, body := e.raw("GET", link, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("derivative: %d %s", resp.StatusCode, body)
	}
	if img, err := png.Decode(bytes.NewReader(body)); err != nil || img.Bounds().Dx() != 16 {
		t.Errorf("derivative image: %v", err)
	}
	// Every parameter is covered by the signature.
	for _, tamper := range [][2]string{{"scale", "3"}, {"filter", "linear"}, {"format", "jpeg"}, {"width", "64"}, {"height", "5"}} {
		if resp, _ := e.raw("GET", withParam(t, link, tamper[0], tamper[1]), nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("changing %s: status %d, want 403", tamper[0], resp.StatusCode)
		}
	}
	// And so is the file: the link of one asset does not render another.
	b := e.upload("other.png", dlvPNG(8, 8, color.RGBA{1, 1, 1, 255}), `{"name":"other"}`)
	if resp, _ := e.raw("GET", strings.Replace(link, "/derive/"+a.ID+"/", "/derive/"+b.ID+"/", 1), nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("link used for another asset: %d", resp.StatusCode)
	}
	if resp, _ := e.raw("GET", withParam(t, link, "exp", "1"), nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("expired: %d", resp.StatusCode)
	}
	// Tool-level validation.
	m.fails("get_derivative_command", map[string]any{"asset_id": a.ID, "filename": "tile.png"})
	m.fails("get_derivative_command", map[string]any{"asset_id": a.ID, "filename": "nope.png", "width": 4})
	m.fails("get_derivative_command", map[string]any{"asset_id": a.ID, "filename": "tile.png", "width": 9000})
	// Explicit width with jpeg.
	out = m.call("get_derivative_command", map[string]any{"asset_id": a.ID, "filename": "tile.png", "width": 4, "format": "jpeg", "quality": 70, "filter": "linear", "output": "x.jpg"})
	if !strings.Contains(out["command"].(string), "-o 'x.jpg'") {
		t.Errorf("command = %v", out["command"])
	}
	jl := lastQuoted(t, out["command"].(string))
	if resp, _ := e.raw("GET", withParam(t, jl, "quality", "99"), nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("changing jpeg quality: status %d", resp.StatusCode)
	}
	resp, _ = e.raw("GET", jl, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("jpeg derivative: %d", resp.StatusCode)
	}
}

func TestAtlasLinkCoversOptions(t *testing.T) {
	e := newEnv(t)
	m := newDlvMCP(t, e)
	var ids []any
	for i, c := range []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}} {
		a := e.upload(fmt.Sprintf("s%d.png", i), dlvPNG(10+i, 12, c), fmt.Sprintf(`{"name":"sprite %d"}`, i))
		ids = append(ids, a.ID)
	}
	out := m.call("pack_atlas", map[string]any{"asset_ids": ids, "padding": 2, "format": "godot", "res_path": "res://art/atlas.png", "power_of_two": true})
	frames := out["frames"].([]any)
	if len(frames) != 3 || out["width"].(float64) != 32 && out["width"].(float64) != 64 && out["width"].(float64) != 16 {
		t.Errorf("pack_atlas = %v", out)
	}
	link := lastQuoted(t, out["command"].(string))
	resp, body := e.raw("GET", link, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("atlas: %d %s", resp.StatusCode, body)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if len(names) != 4 || names[0] != "atlas.png" {
		t.Errorf("zip = %v", names)
	}
	for _, tamper := range [][2]string{{"padding", "0"}, {"max_size", "512"}, {"extrude", "1"}, {"format", "json"}, {"power_of_two", "false"}, {"res_path", "res://other.png"}, {"ids", ids[0].(string)}} {
		if resp, _ := e.raw("GET", withParam(t, link, tamper[0], tamper[1]), nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("changing %s: status %d, want 403", tamper[0], resp.StatusCode)
		}
	}
	// Session variant for the web UI.
	q := hoard.AtlasQuery([]string{ids[0].(string), ids[1].(string)}, hoard.AtlasOptions{MaxSize: 256, Format: "phaser", ResPath: "res://atlas.png"})
	r := e.do("GET", "/api/v1/atlas/pack?"+q.Encode(), nil, nil, http.StatusOK, nil)
	if r.Header.Get("Content-Type") != "application/zip" {
		t.Errorf("atlas/pack content type %s", r.Header.Get("Content-Type"))
	}
	m.fails("pack_atlas", map[string]any{"asset_ids": ids, "max_size": 20})
	m.fails("pack_atlas", map[string]any{"asset_ids": []string{"../x"}})
}

func TestDeliveryToolsListed(t *testing.T) {
	e := newEnv(t)
	m := newDlvMCP(t, e)
	list, err := m.s.ListTools(m.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range list.Tools {
		have[tl.Name] = true
	}
	for _, n := range []string{"get_repo_audit_command", "audit_project", "sync_project", "pack_atlas", "get_derivative_command", "get_engine_hints"} {
		if !have[n] {
			t.Errorf("tool %s missing", n)
		}
	}
	m.fails("get_engine_hints", map[string]any{"asset_ids": []string{"ast_000000000000"}, "engine": "godot"})
	m.fails("get_repo_audit_command", map[string]any{"project_id": "prj_missing"})
	p, _ := e.svc.SaveProject(context.Background(), "", hoard.GroupInput{Name: dlvPtr("inj")})
	m.fails("get_repo_audit_command", map[string]any{"project_id": p.ID, "extensions": []string{"png'; rm -rf /"}})
}

func dlvPtr[T any](v T) *T { return &v }
