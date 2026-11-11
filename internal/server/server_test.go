package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/toriath/hoard/internal/ai"
	"github.com/toriath/hoard/internal/config"
	"github.com/toriath/hoard/internal/hoard"
)

type env struct {
	svc    *hoard.Service
	t      *testing.T
	ts     *httptest.Server
	client *http.Client // has a cookie jar: behaves like a browser session
	token  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := hoard.Open(t.TempDir(), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	cfg := config.Config{MaxUploadBytes: 1 << 20}
	web := fstest.MapFS{"index.html": {Data: []byte("<html>hoard</html>")}, "static/app.js": {Data: []byte("//js")}}
	ts := httptest.NewServer(New(svc, ai.New(svc, cfg, log), cfg, log, "test", web))
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	e := &env{t: t, ts: ts, client: &http.Client{Jar: jar}, svc: svc}

	var out struct {
		Secret string `json:"secret"`
	}
	e.do("POST", "/api/v1/auth/setup", `{"username":"alice","password":"correct horse battery"}`, nil, http.StatusCreated, nil)
	e.do("POST", "/api/v1/tokens", `{"name":"test"}`, nil, http.StatusCreated, &out)
	e.token = out.Secret
	return e
}

// do sends a request with the session cookie and checks the status.
func (e *env) do(method, path string, body any, hdr map[string]string, want int, out any) *http.Response {
	e.t.Helper()
	return e.send(e.client, method, path, body, hdr, want, out)
}

func (e *env) send(c *http.Client, method, path string, body any, hdr map[string]string, want int, out any) *http.Response {
	e.t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case string:
		r = strings.NewReader(b)
	case []byte:
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, r)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, ok := body.(string); ok {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != want {
		e.t.Fatalf("%s %s = %d, want %d: %s", method, path, res.StatusCode, want, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			e.t.Fatalf("%s %s: %v in %s", method, path, err, data)
		}
	}
	res.Body = io.NopCloser(bytes.NewReader(data))
	return res
}

func (e *env) upload(name string, content []byte, meta string) *hoard.Asset {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if meta != "" {
		_ = mw.WriteField("meta", meta)
	}
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(content)
	mw.Close()
	var out struct {
		Asset *hoard.Asset `json:"asset"`
	}
	e.do("POST", "/api/v1/assets", buf.Bytes(), map[string]string{"Content-Type": mw.FormDataContentType()}, http.StatusCreated, &out)
	return out.Asset
}

func TestAuthentication(t *testing.T) {
	e := newEnv(t)
	anon := &http.Client{}
	bearer := map[string]string{"Authorization": "Bearer " + e.token}

	e.send(anon, "GET", "/healthz", nil, nil, http.StatusOK, nil)
	e.send(anon, "GET", "/api/v1/assets", nil, nil, http.StatusUnauthorized, nil)
	e.send(anon, "GET", "/api/v1/assets", nil, map[string]string{"Authorization": "Bearer hoard_wrong"}, http.StatusUnauthorized, nil)
	e.send(anon, "GET", "/api/v1/assets", nil, bearer, http.StatusOK, nil)
	e.do("GET", "/api/v1/assets", nil, nil, http.StatusOK, nil)

	// Setup is a one-time operation.
	e.send(anon, "POST", "/api/v1/auth/setup", `{"username":"eve","password":"another long password"}`, nil, http.StatusConflict, nil)

	// A cross-site request must not be able to ride on the session cookie.
	e.do("POST", "/api/v1/assets/bulk", `{"ids":["x"],"action":"purge"}`, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden, nil)

	// The API key must never be echoed back.
	e.do("PUT", "/api/v1/settings/ai", `{"provider":"anthropic","api_key":"sk-ant-secret-value","model":"claude-opus-5-5"}`, nil, http.StatusOK, nil)
	res := e.do("GET", "/api/v1/settings/ai", nil, nil, http.StatusOK, nil)
	if body, _ := io.ReadAll(res.Body); bytes.Contains(body, []byte("sk-ant-secret-value")) || !bytes.Contains(body, []byte(`"has_api_key":true`)) {
		t.Errorf("AI settings leak or lose the key: %s", body)
	}

	// Brute force is throttled.
	for i := range 9 {
		want := http.StatusUnauthorized
		if i == 8 {
			want = http.StatusTooManyRequests
		}
		e.send(anon, "POST", "/api/v1/auth/login", `{"username":"alice","password":"guess-number-xx"}`, nil, want, nil)
	}

	// Logging out invalidates the session.
	e.do("POST", "/api/v1/auth/logout", `{}`, nil, http.StatusOK, nil)
	e.do("GET", "/api/v1/assets", nil, nil, http.StatusUnauthorized, nil)
}

func TestUploadAndDelivery(t *testing.T) {
	e := newEnv(t)
	a := e.upload("payload.html", []byte("<script>alert(document.cookie)</script>"), `{"tags":["Test"],"license":"MIT"}`)
	if a.Name != "payload" || len(a.Tags) != 1 || a.Tags[0] != "test" || a.License != "MIT" {
		t.Fatalf("asset = %+v", a)
	}

	// User files are inert: opaque type, no sniffing, sandboxed.
	res := e.do("GET", "/api/v1/assets/"+a.ID+"/raw/payload.html", nil, nil, http.StatusOK, nil)
	if ct := res.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("content type = %q", ct)
	}
	if res.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("missing hardening headers: %v", res.Header)
	}
	e.do("GET", "/api/v1/assets/"+a.ID+"/raw/..%2Fasset.json", nil, nil, http.StatusNotFound, nil)
	res = e.do("GET", "/api/v1/assets/"+a.ID+"/raw/payload.html", nil, map[string]string{"Range": "bytes=0-7"}, http.StatusPartialContent, nil)
	if b, _ := io.ReadAll(res.Body); string(b) != "<script>" {
		t.Errorf("range = %q", b)
	}

	// Per-file limit (1 MiB in this test) removes the half-created asset.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "huge.bin")
	fw.Write(make([]byte, 1<<20+1))
	mw.Close()
	e.do("POST", "/api/v1/assets", buf.Bytes(), map[string]string{"Content-Type": mw.FormDataContentType()}, http.StatusRequestEntityTooLarge, nil)
	var list hoard.ListResult
	e.do("GET", "/api/v1/assets", nil, nil, http.StatusOK, &list)
	if list.Total != 1 {
		t.Errorf("failed upload left an asset behind: total=%d", list.Total)
	}

	// Zip download of several assets.
	b := e.upload("b.txt", []byte("hello"), "")
	res = e.do("GET", "/api/v1/download?ids="+a.ID+","+b.ID, nil, nil, http.StatusOK, nil)
	if z, _ := io.ReadAll(res.Body); !bytes.HasPrefix(z, []byte("PK")) || !bytes.Contains(z, []byte("asset.json")) {
		t.Error("not a zip with metadata")
	}

	// Trash via DELETE, then it is gone from the list.
	e.do("DELETE", "/api/v1/assets/"+b.ID, nil, nil, http.StatusOK, nil)
	e.do("GET", "/api/v1/assets", nil, nil, http.StatusOK, &list)
	if list.Total != 1 {
		t.Errorf("total after trash = %d", list.Total)
	}

	// The SPA is served for unknown paths, with a CSP; missing bundles 404.
	res = e.do("GET", "/collections/col_x", nil, nil, http.StatusOK, nil)
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Error("SPA served without CSP")
	}
	e.do("GET", "/static/missing.js", nil, nil, http.StatusNotFound, nil)
	e.do("GET", "/static/app.js", nil, nil, http.StatusOK, nil)
	// A deep link to an asset is a page of the app, not a bundle file.
	e.do("GET", "/assets/"+a.ID, nil, nil, http.StatusOK, nil)
	e.do("GET", "/api/v1/nope", nil, nil, http.StatusNotFound, nil)
}

func mcpCall(e *env, method string, params any, out any) {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	hdr := map[string]string{"Authorization": "Bearer " + e.token, "Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	e.send(&http.Client{}, "POST", "/mcp", body, hdr, http.StatusOK, &env)
	if env.Error != nil {
		e.t.Fatalf("%s: %s", method, env.Error)
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		e.t.Fatal(err)
	}
}

// toolText calls an MCP tool and returns its JSON text payload decoded.
func toolText(e *env, name string, args any, out any) (isError bool, text string) {
	e.t.Helper()
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	mcpCall(e, "tools/call", map[string]any{"name": name, "arguments": args}, &res)
	if len(res.Content) == 0 {
		e.t.Fatalf("%s: empty result", name)
	}
	if out != nil && !res.IsError {
		if err := json.Unmarshal([]byte(res.Content[0].Text), out); err != nil {
			e.t.Fatalf("%s: %v in %s", name, err, res.Content[0].Text)
		}
	}
	return res.IsError, res.Content[0].Text
}

func TestMCP(t *testing.T) {
	e := newEnv(t)

	// Tokens only: neither anonymous callers nor browser sessions get in.
	e.send(&http.Client{}, "POST", "/mcp", `{}`, nil, http.StatusUnauthorized, nil)
	e.do("POST", "/mcp", `{}`, nil, http.StatusUnauthorized, nil)

	var init struct {
		ServerInfo   struct{ Name string } `json:"serverInfo"`
		Instructions string                `json:"instructions"`
	}
	mcpCall(e, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "0"}}, &init)
	if init.ServerInfo.Name != "hoard" || !strings.Contains(init.Instructions, "create_asset") {
		t.Errorf("initialize = %+v", init)
	}
	var tools struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	mcpCall(e, "tools/list", map[string]any{}, &tools)
	if len(tools.Tools) < 22 { // feature areas add their own; each has its own test
		t.Errorf("%d tools", len(tools.Tools))
	}

	// The agent workflow: create with metadata, upload over the signed link,
	// find it again, record usage, download, trash.
	var created struct {
		Asset  hoard.Asset `json:"asset"`
		Upload struct {
			Commands []struct{ Filename, Command string } `json:"commands"`
		} `json:"upload"`
	}
	toolText(e, "create_asset", map[string]any{
		"name": "Laser Shot", "tags": []string{"Sci-Fi", "weapon"}, "prompt": "short laser zap", "license": "CC0-1.0",
		"filenames": []string{"out/laser shot.wav"},
	}, &created)
	if created.Asset.Prompt != "short laser zap" || len(created.Upload.Commands) != 1 {
		t.Fatalf("create = %+v", created)
	}
	cmd := created.Upload.Commands[0].Command
	if !strings.HasPrefix(cmd, "curl -fsS --globoff -T 'out/laser shot.wav' '"+e.ts.URL+"/api/v1/upload/"+created.Asset.ID+"/laser%20shot.wav?exp=") {
		t.Errorf("upload command = %s", cmd)
	}
	if strings.Contains(cmd, e.token) {
		t.Error("API token leaked into a command")
	}
	signed := cmd[strings.LastIndex(cmd, " '")+2 : len(cmd)-1]
	path := strings.TrimPrefix(signed, e.ts.URL)
	e.send(&http.Client{}, "PUT", path, []byte("RIFF....WAVE"), nil, http.StatusCreated, nil)

	// A signature is bound to its asset and file name and cannot be replayed elsewhere.
	other := e.upload("x.txt", []byte("x"), "")
	e.send(&http.Client{}, "PUT", strings.Replace(path, created.Asset.ID, other.ID, 1), []byte("nope"), nil, http.StatusForbidden, nil)
	e.send(&http.Client{}, "PUT", strings.Replace(path, "laser%20shot.wav", "something-else.bin", 1), []byte("nope"), nil, http.StatusForbidden, nil)
	u, _ := url.Parse(path)
	e.send(&http.Client{}, "PUT", u.Path+"?exp=9999999999&sig=deadbeef", []byte("nope"), nil, http.StatusForbidden, nil)

	var found struct {
		Total  int `json:"total"`
		Assets []struct {
			ID, Type string
			Files    []string
		} `json:"assets"`
	}
	toolText(e, "search_assets", map[string]any{"query": "laser sci", "types": []string{"sfx"}}, &found)
	if found.Total != 1 || found.Assets[0].ID != created.Asset.ID || found.Assets[0].Files[0] != "laser shot.wav" {
		t.Fatalf("search = %+v", found)
	}

	var prj struct {
		Project hoard.Project `json:"project"`
	}
	toolText(e, "save_project", map[string]any{"name": "Starfall"}, &prj)
	toolText(e, "bulk_update_assets", map[string]any{"ids": []string{created.Asset.ID}, "add_project_ids": []string{prj.Project.ID}, "project_note": "player weapon"}, nil)

	var got struct {
		Asset    hoard.Asset                `json:"asset"`
		Download []struct{ Command string } `json:"download_commands"`
	}
	toolText(e, "get_asset", map[string]any{"id": created.Asset.ID}, &got)
	if len(got.Asset.Projects) != 1 || got.Asset.Projects[0].Note != "player weapon" || len(got.Download) != 1 {
		t.Fatalf("get = %+v", got)
	}
	dl := got.Download[0].Command
	dlPath := strings.TrimPrefix(dl[strings.LastIndex(dl, " '")+2:len(dl)-1], e.ts.URL)
	res := e.send(&http.Client{}, "GET", dlPath, nil, nil, http.StatusOK, nil)
	if b, _ := io.ReadAll(res.Body); string(b) != "RIFF....WAVE" {
		t.Errorf("downloaded %q", b)
	}

	// Inline text files and reading them back.
	var shader struct {
		Asset hoard.Asset `json:"asset"`
	}
	toolText(e, "create_asset", map[string]any{"text_files": []map[string]string{{"filename": "glow.gdshader", "content": "shader_type canvas_item;"}}}, &shader)
	if shader.Asset.Type != "shader" || shader.Asset.Name != "glow" {
		t.Errorf("inline asset = %+v", shader.Asset)
	}
	if _, text := toolText(e, "view_asset", map[string]any{"id": shader.Asset.ID, "filename": "glow.gdshader"}, nil); text != "shader_type canvas_item;" {
		t.Errorf("view = %q", text)
	}

	// Errors come back as tool errors the model can read and act on.
	if isErr, text := toolText(e, "get_asset", map[string]any{"id": "ast_missing"}, nil); !isErr || !strings.Contains(text, "not found") {
		t.Errorf("missing asset: %v %q", isErr, text)
	}
	if isErr, text := toolText(e, "update_asset", map[string]any{"id": shader.Asset.ID, "type": "banana"}, nil); !isErr || !strings.Contains(text, "valid:") {
		t.Errorf("bad type: %v %q", isErr, text)
	}
	if isErr, _ := toolText(e, "analyze_assets", map[string]any{"ids": []string{shader.Asset.ID}}, nil); !isErr {
		t.Error("analyze without a provider should fail clearly")
	}

	// Deleting is recoverable by default, and a link issued earlier cannot
	// write into a trashed asset.
	toolText(e, "delete_assets", map[string]any{"ids": []string{created.Asset.ID}}, nil)
	e.send(&http.Client{}, "PUT", path, []byte("RIFF....LATE"), nil, http.StatusBadRequest, nil)
	toolText(e, "restore_assets", map[string]any{"ids": []string{created.Asset.ID}}, nil)
	toolText(e, "delete_assets", map[string]any{"ids": []string{shader.Asset.ID}}, nil)
	toolText(e, "search_assets", map[string]any{"trashed": true}, &found)
	if found.Total != 1 {
		t.Errorf("trash holds %d assets, want 1", found.Total)
	}
	toolText(e, "restore_assets", map[string]any{"ids": []string{shader.Asset.ID}}, nil)
	toolText(e, "search_assets", map[string]any{"query": "glow"}, &found)
	if found.Total != 1 {
		t.Errorf("restore failed: %+v", found)
	}
}

// API tokens are for agents: they must not be able to create accounts or
// further tokens, or to repoint the AI provider.
func TestTokensCannotAdminister(t *testing.T) {
	e := newEnv(t)
	bearer := map[string]string{"Authorization": "Bearer " + e.token}
	anon := &http.Client{}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/users", `{"username":"mallory","password":"a long enough password"}`},
		{"POST", "/api/v1/tokens", `{"name":"persist"}`},
		{"POST", "/api/v1/auth/password", `{"current":"x","new":"another long password"}`},
		{"PUT", "/api/v1/settings/ai", `{"provider":"openai","base_url":"http://evil.example"}`},
		{"POST", "/api/v1/settings/ai/test", `{}`},
		{"POST", "/api/v1/settings/ai/models", `{"base_url":"http://evil.example"}`},
	} {
		e.send(anon, c.method, c.path, c.body, bearer, http.StatusForbidden, nil)
	}
	e.send(anon, "DELETE", "/api/v1/tokens/tok_x", nil, bearer, http.StatusForbidden, nil)
	// Reading and all library work stays available to tokens.
	e.send(anon, "GET", "/api/v1/settings/ai", nil, bearer, http.StatusOK, nil)
	e.send(anon, "GET", "/api/v1/tokens", nil, bearer, http.StatusOK, nil)
}

// The stored AI key must only ever travel to the endpoint it was saved for.
func TestAIKeyStaysWithItsEndpoint(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var seen []string
	capture := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.Host+" "+r.Header.Get("Authorization"))
			mu.Unlock()
			io.WriteString(w, `{"data":[{"id":"m"}],"choices":[{"message":{"content":"{}"}}]}`)
		}))
	}
	trusted, evil := capture(), capture()
	defer trusted.Close()
	defer evil.Close()
	leaked := func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range seen {
			if strings.HasPrefix(s, strings.TrimPrefix(evil.URL, "http://")) && strings.Contains(s, "sk-local-secret") {
				return true
			}
		}
		return false
	}

	e.do("PUT", "/api/v1/settings/ai", `{"provider":"openai","base_url":"`+trusted.URL+`/v1","api_key":"sk-local-secret"}`, nil, http.StatusOK, nil)

	// The former GET form is gone, so a cross-site navigation cannot trigger it.
	e.do("GET", "/api/v1/settings/ai/models?base_url="+evil.URL, nil, nil, http.StatusNotFound, nil)
	// Asking for another server's models works, but without the key.
	e.do("POST", "/api/v1/settings/ai/models", `{"base_url":"`+evil.URL+`/v1"}`, nil, http.StatusOK, nil)
	// The configured server still gets it.
	e.do("POST", "/api/v1/settings/ai/models", `{}`, nil, http.StatusOK, nil)
	// Repointing the provider without re-entering the key drops the key.
	var saved struct {
		HasKey bool `json:"has_api_key"`
	}
	e.do("PUT", "/api/v1/settings/ai", `{"provider":"openai","base_url":"`+evil.URL+`/v1"}`, nil, http.StatusOK, &saved)
	e.do("POST", "/api/v1/settings/ai/test", `{}`, nil, http.StatusOK, nil)

	if saved.HasKey {
		t.Error("key survived a change of base URL")
	}
	if leaked() {
		t.Errorf("key was sent to the other host: %v", seen)
	}
	mu.Lock()
	defer mu.Unlock()
	sentToTrusted := false
	for _, s := range seen {
		if strings.HasPrefix(s, strings.TrimPrefix(trusted.URL, "http://")) && strings.Contains(s, "sk-local-secret") {
			sentToTrusted = true
		}
	}
	if !sentToTrusted {
		t.Errorf("configured endpoint did not receive the key: %v", seen)
	}
}

// A parallel burst of guesses must be throttled like sequential ones.
func TestLoginThrottleUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	const n = 40
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/auth/login", strings.NewReader(`{"username":"alice","password":"wrong-guess-number"}`))
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			res.Body.Close()
			codes[i] = res.StatusCode
		}()
	}
	wg.Wait()
	checked := 0
	for _, c := range codes {
		if c == http.StatusUnauthorized {
			checked++
		}
	}
	if checked > 8 {
		t.Errorf("%d of %d parallel guesses reached the password check, limit is 8", checked, n)
	}
	// Garbage never reaches the password hash and is not an error either.
	e.send(&http.Client{}, "POST", "/api/v1/auth/login", `{"username":"`+strings.Repeat("x", 5000)+`","password":"p"}`, nil, http.StatusUnauthorized, nil)
	e.send(&http.Client{}, "POST", "/api/v1/auth/login", `{"username":"alice","password":"`+strings.Repeat("x", 9000)+`"}`, nil, http.StatusRequestEntityTooLarge, nil)
	// Anonymous callers do not learn the version.
	res := e.send(&http.Client{}, "GET", "/healthz", nil, nil, http.StatusOK, nil)
	if b, _ := io.ReadAll(res.Body); bytes.Contains(b, []byte("version")) {
		t.Errorf("healthz discloses the version: %s", b)
	}
}

// Only wrong guesses of the current password count against the throttle.
func TestPasswordChangeThrottle(t *testing.T) {
	e := newEnv(t)
	for range 12 { // a too-short new password, many times: never throttled
		e.do("POST", "/api/v1/auth/password", `{"current":"correct horse battery","new":"short"}`, nil, http.StatusBadRequest, nil)
	}
	for i := range 9 {
		want := http.StatusBadRequest
		if i == 8 {
			want = http.StatusTooManyRequests
		}
		e.do("POST", "/api/v1/auth/password", `{"current":"a wrong guess","new":"another long password"}`, nil, want, nil)
	}
}
