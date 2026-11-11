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
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func picture(t *testing.T, seed float64, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 96, 96))
	for y := 0; y < 96; y++ {
		for x := 0; x < 96; x++ {
			v := math.Sin(float64(x)*0.13*seed+seed) + math.Sin(float64(y)*0.17+seed*2) + math.Sin(float64(x+y)*0.05*seed)
			k := uint8(128 + 40*v)
			img.SetNRGBA(x, y, color.NRGBA{uint8(int(c.R) * int(k) / 255), uint8(int(c.G) * int(k) / 255), uint8(int(c.B) * int(k) / 255), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(data))
	}
	zw.Close()
	return buf.Bytes()
}

func (e *env) uploadResult(name string, content []byte) (assetID string, dup, similar []string) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(content)
	mw.Close()
	var out struct {
		Asset struct {
			ID string `json:"id"`
		} `json:"asset"`
		Duplicates []string `json:"duplicate_of"`
		SimilarTo  []string `json:"similar_to"`
	}
	e.do("POST", "/api/v1/assets", buf.Bytes(), map[string]string{"Content-Type": mw.FormDataContentType()}, http.StatusCreated, &out)
	return out.Asset.ID, out.Duplicates, out.SimilarTo
}

func TestSimilarDuplicatesAndColorsOverREST(t *testing.T) {
	e := newEnv(t)
	red := color.NRGBA{255, 60, 60, 255}
	a, _, _ := e.uploadResult("a.png", picture(t, 3, red))
	b, dup, _ := e.uploadResult("b.png", picture(t, 3, red))
	c, _, _ := e.uploadResult("c.png", picture(t, 9, color.NRGBA{40, 60, 255, 255}))
	if len(dup) != 1 || dup[0] != a {
		t.Errorf("duplicate_of = %v", dup)
	}

	var sim struct {
		Items []struct {
			Asset   struct{ ID string }
			Score   float64
			Reasons []string
		} `json:"items"`
	}
	e.do("GET", "/api/v1/assets/"+b+"/similar", nil, nil, http.StatusOK, &sim)
	if len(sim.Items) != 1 || sim.Items[0].Asset.ID != a || sim.Items[0].Reasons[0] != "identical file" {
		t.Errorf("similar = %+v", sim)
	}
	e.do("GET", "/api/v1/assets/ast_000000000000/similar", nil, nil, http.StatusNotFound, nil)

	var dups struct {
		Groups []struct {
			Kind   string
			Assets []struct{ ID string }
		} `json:"groups"`
	}
	e.do("GET", "/api/v1/duplicates", nil, nil, http.StatusOK, &dups)
	if len(dups.Groups) != 1 || dups.Groups[0].Kind != "identical" || len(dups.Groups[0].Assets) != 2 {
		t.Errorf("duplicates = %+v", dups)
	}

	// colour search
	var list struct {
		Items []struct{ ID string } `json:"items"`
	}
	e.do("GET", "/api/v1/assets?color="+url.QueryEscape("#ff3c3c"), nil, nil, http.StatusOK, &list)
	if len(list.Items) != 2 {
		t.Errorf("red search = %+v", list)
	}
	e.do("GET", "/api/v1/assets?color=3c3cff", nil, nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].ID != c {
		t.Errorf("blue search = %+v", list)
	}
	e.do("GET", "/api/v1/assets?color=notacolor", nil, nil, http.StatusBadRequest, nil)

	// anonymous callers get nothing
	anon := &http.Client{}
	e.send(anon, "GET", "/api/v1/duplicates", nil, nil, http.StatusUnauthorized, nil)
	e.send(anon, "GET", "/api/v1/assets/"+a+"/similar", nil, nil, http.StatusUnauthorized, nil)
	e.send(anon, "POST", "/api/v1/import", nil, nil, http.StatusUnauthorized, nil)
}

func importForm(t *testing.T, options string, name string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if options != "" {
		mw.WriteField("options", options)
	}
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	return &buf, mw.FormDataContentType()
}

type importOut struct {
	Mode           string `json:"mode"`
	CollectionID   string `json:"collection_id"`
	CollectionName string `json:"collection_name"`
	License        string `json:"license"`
	Files          int    `json:"files"`
	Assets         []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"assets"`
}

func TestImportOverREST(t *testing.T) {
	e := newEnv(t)
	pack := zipOf(t, map[string]string{"LICENSE": "CC-BY 4.0\nby Someone Famous", "a.png": "x", "b/c.png": "y"})
	body, ct := importForm(t, `{"mode":"per_file","collection_name":"Test pack","tags":["ui"]}`, "pack.zip", pack)
	var out importOut
	e.do("POST", "/api/v1/import", body.Bytes(), map[string]string{"Content-Type": ct}, http.StatusCreated, &out)
	if out.CollectionName != "Test pack" || out.License != "CC-BY-4.0" || len(out.Assets) != 2 || out.Files != 2 {
		t.Fatalf("import = %+v", out)
	}
	var cols struct {
		Items []struct {
			ID         string `json:"id"`
			Kind       string `json:"kind"`
			AssetCount int    `json:"asset_count"`
		} `json:"items"`
	}
	e.do("GET", "/api/v1/collections", nil, nil, http.StatusOK, &cols)
	if len(cols.Items) != 1 || cols.Items[0].Kind != "pack" || cols.Items[0].AssetCount != 2 {
		t.Errorf("collections = %+v", cols)
	}

	// refusals: unknown field, unknown mode, not a zip, no file
	for _, c := range []struct{ opts, name string }{{`{"nope":1}`, "p.zip"}, {`{"mode":"x"}`, "p.zip"}} {
		b, ct := importForm(t, c.opts, c.name, pack)
		e.do("POST", "/api/v1/import", b.Bytes(), map[string]string{"Content-Type": ct}, http.StatusBadRequest, nil)
	}
	b, ct := importForm(t, "{}", "p.zip", []byte("not a zip"))
	e.do("POST", "/api/v1/import", b.Bytes(), map[string]string{"Content-Type": ct}, http.StatusBadRequest, nil)
	// over the upload size limit (1 MiB in the test environment)
	big := zipOf(t, map[string]string{"big.bin": strings.Repeat("random-ish text ", 1)})
	b, ct = importForm(t, "{}", "p.zip", append(big, bytes.Repeat([]byte{1}, 2<<20)...))
	e.do("POST", "/api/v1/import", b.Bytes(), map[string]string{"Content-Type": ct}, http.StatusRequestEntityTooLarge, nil)
}

func connectMCP(t *testing.T, e *env) (context.Context, *mcp.ClientSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   e.ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{e.token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return ctx, session
}

func callJSON(t *testing.T, ctx context.Context, s *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil && !res.IsError {
		if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), out); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return res
}

func TestLibraryMCPTools(t *testing.T) {
	e := newEnv(t)
	red := color.NRGBA{255, 60, 60, 255}
	a, _, _ := e.uploadResult("a.png", picture(t, 3, red))
	b, _, _ := e.uploadResult("b.png", picture(t, 3, red))
	ctx, session := connectMCP(t, e)

	var sim struct {
		Count   int `json:"count"`
		Similar []struct {
			Asset   struct{ ID string } `json:"asset"`
			Reasons []string            `json:"reasons"`
		} `json:"similar"`
	}
	callJSON(t, ctx, session, "find_similar", map[string]any{"id": b}, &sim)
	if sim.Count != 1 || sim.Similar[0].Asset.ID != a || sim.Similar[0].Reasons[0] != "identical file" {
		t.Errorf("find_similar = %+v", sim)
	}
	if res := callJSON(t, ctx, session, "find_similar", map[string]any{"id": "ast_000000000000"}, nil); !res.IsError {
		t.Error("unknown asset accepted")
	}
	var dups struct {
		Groups int `json:"groups"`
	}
	callJSON(t, ctx, session, "find_duplicates", map[string]any{}, &dups)
	if dups.Groups != 1 {
		t.Errorf("find_duplicates = %+v", dups)
	}
	var found struct {
		Total int `json:"total"`
	}
	callJSON(t, ctx, session, "search_assets", map[string]any{"colors": []string{"#ff3c3c"}}, &found)
	if found.Total != 2 {
		t.Errorf("colour search = %+v", found)
	}
	callJSON(t, ctx, session, "search_assets", map[string]any{"colors": []string{"#3c3cff"}}, &found)
	if found.Total != 0 {
		t.Errorf("blue search = %+v", found)
	}
}

func TestSignedImportBindsItsOptions(t *testing.T) {
	e := newEnv(t)
	ctx, session := connectMCP(t, e)
	proj := e.svcProject("Game")

	var cmd struct {
		Command string `json:"command"`
	}
	callJSON(t, ctx, session, "get_import_command", map[string]any{
		"filename": "dl/My Pack's.zip", "mode": "per_folder", "collection_name": "Via MCP", "license": "CC0-1.0", "project_id": proj,
	}, &cmd)
	// the command is quoted for the shell and uploads with curl -T
	if !strings.HasPrefix(cmd.Command, "curl -fsS --globoff -T 'dl/My Pack'\\''s.zip' '") {
		t.Fatalf("command = %s", cmd.Command)
	}
	link := cmd.Command[strings.LastIndex(cmd.Command, "' '")+3 : len(cmd.Command)-1]
	u, err := url.Parse(link)
	if err != nil || !strings.HasPrefix(u.Path, "/api/v1/import/") {
		t.Fatalf("link = %s %v", link, err)
	}
	if res := callJSON(t, ctx, session, "get_import_command", map[string]any{"filename": "a.zip", "mode": "bogus"}, nil); !res.IsError {
		t.Error("bad mode accepted")
	}
	if res := callJSON(t, ctx, session, "get_import_command", map[string]any{"filename": "a.zip", "project_id": "prj_nope"}, nil); !res.IsError {
		t.Error("unknown project accepted")
	}

	pack := zipOf(t, map[string]string{"f1/a.png": "a", "f1/sub/b.png": "b", "root.png": "r"})
	put := func(link string, want int, out any) {
		t.Helper()
		e.send(&http.Client{}, "PUT", strings.TrimPrefix(link, e.ts.URL), pack, nil, want, out)
	}

	// Tampering with anything the signature binds is refused.
	for name, mutate := range map[string]func(q url.Values, u *url.URL){
		"options":   func(q url.Values, _ *url.URL) { q.Set("o", "e30") }, // {} instead of the signed options
		"signature": func(q url.Values, _ *url.URL) { q.Set("sig", strings.Repeat("0", 40)) },
		"expiry":    func(q url.Values, _ *url.URL) { q.Set("exp", "99999999999") },
		"file name": func(_ url.Values, u *url.URL) { u.Path = "/api/v1/import/other.zip" },
	} {
		t2 := *u
		q := t2.Query()
		mutate(q, &t2)
		t2.RawQuery = q.Encode()
		put(t2.String(), http.StatusForbidden, nil)
		_ = name
	}
	e.send(&http.Client{}, "PUT", "/api/v1/import/My%20Pack_s.zip", pack, nil, http.StatusForbidden, nil) // no signature at all

	var out importOut
	put(u.String(), http.StatusCreated, &out)
	if out.Mode != "per_folder" || out.CollectionName != "Via MCP" || out.License != "CC0-1.0" || len(out.Assets) != 2 {
		t.Fatalf("import = %+v", out)
	}
	// A link imports once: replaying it would import the pack again.
	put(u.String(), http.StatusConflict, nil)
	// the project option was honoured
	var list struct {
		Items []struct{ ID string } `json:"items"`
	}
	e.do("GET", "/api/v1/assets?project="+proj, nil, nil, http.StatusOK, &list)
	if len(list.Items) != 2 {
		t.Errorf("project usage = %+v", list)
	}
}

func (e *env) svcProject(name string) string {
	e.t.Helper()
	var out struct {
		ID string `json:"id"`
	}
	e.do("POST", "/api/v1/projects", `{"name":"`+name+`"}`, nil, http.StatusOK, &out)
	return out.ID
}

func TestImportOptionsComeFirstAndLargePacksSkipTheAI(t *testing.T) {
	e := newEnv(t)
	pack := zipOf(t, map[string]string{"a.png": "x"})

	// the file before the options part: refused rather than imported with defaults
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "p.zip")
	fw.Write(pack)
	mw.WriteField("options", `{"collection_name":"late"}`)
	mw.Close()
	e.do("POST", "/api/v1/import", buf.Bytes(), map[string]string{"Content-Type": mw.FormDataContentType()}, http.StatusBadRequest, nil)
	var cols struct {
		Items []struct{ ID string } `json:"items"`
	}
	e.do("GET", "/api/v1/collections", nil, nil, http.StatusOK, &cols)
	if len(cols.Items) != 0 {
		t.Error("something was imported")
	}

	// more assets than the AI agent is queued for
	files := map[string]string{}
	for i := 0; i < maxImportAI+1; i++ {
		files[fmt.Sprintf("f%d.png", i)] = fmt.Sprintf("content %d", i)
	}
	body, ct := importForm(t, `{}`, "big.zip", zipOf(t, files))
	var out struct {
		Summary     string `json:"summary"`
		AINotQueued int    `json:"ai_not_queued"`
		Files       int    `json:"files"`
	}
	e.do("POST", "/api/v1/import", body.Bytes(), map[string]string{"Content-Type": ct}, http.StatusCreated, &out)
	if out.Files != maxImportAI+1 || out.AINotQueued != maxImportAI+1 || !strings.Contains(out.Summary, "AI analysis was not queued for 51 assets; use analyze_assets") {
		t.Errorf("import = %+v", out)
	}
	var small struct {
		AINotQueued int `json:"ai_not_queued"`
	}
	body, ct = importForm(t, `{}`, "small.zip", pack)
	e.do("POST", "/api/v1/import", body.Bytes(), map[string]string{"Content-Type": ct}, http.StatusCreated, &small)
	if small.AINotQueued != 0 {
		t.Errorf("small import = %+v", small)
	}

	// at most ten colours per search
	q := strings.Repeat("ff0000,", 11)
	e.do("GET", "/api/v1/assets?color="+q, nil, nil, http.StatusBadRequest, nil)
}
