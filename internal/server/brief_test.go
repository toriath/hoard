package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toriath/hoard/internal/hoard"
)

func testPNG(t *testing.T, w, h int, c color.Color) []byte {
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

// briefEnv creates a project with a brief and two assets in it.
func briefEnv(t *testing.T) (e *env, prj *hoard.Project, good, bad *hoard.Asset) {
	t.Helper()
	e = newEnv(t)
	e.do("POST", "/api/v1/projects", `{"name":"Dungeon <b>crawler</b>"}`, nil, http.StatusOK, &prj)
	if prj == nil || prj.ID == "" {
		t.Fatal("no project")
	}
	e.do("PATCH", "/api/v1/projects/"+prj.ID+"/brief", `{"engine":"godot","pixel_art":true,"palette":["#000000","#ffffff"],"max_texture_size":64,"licenses":["CC0-1.0","CC-BY-4.0"]}`, nil, http.StatusOK, nil)
	good = e.upload("tile_a.png", testPNG(t, 16, 16, color.Black), `{"license":"CC0-1.0","projects":["`+prj.ID+`"]}`)
	bad = e.upload("tile_b.png", testPNG(t, 128, 128, color.NRGBA{200, 30, 30, 255}), `{"license":"CC-BY-4.0","attribution":"","projects":["`+prj.ID+`"]}`)
	for _, a := range []*hoard.Asset{good, bad} {
		if _, err := e.svc.UpdateAsset(context.Background(), a.ID, hoard.AssetInput{AddProjects: []string{prj.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestBriefREST(t *testing.T) {
	e, prj, good, bad := briefEnv(t)
	base := "/api/v1/projects/" + prj.ID

	// Validation errors are 400 with the reason; nothing is stored.
	var apiErr struct {
		Error struct{ Message string } `json:"error"`
	}
	e.do("PATCH", base+"/brief", `{"palette":["red"]}`, nil, http.StatusBadRequest, &apiErr)
	if !strings.Contains(apiErr.Error.Message, "hex colour") {
		t.Errorf("message: %q", apiErr.Error.Message)
	}
	e.do("PATCH", base+"/brief", `{"folders":{"sprite":"../x"}}`, nil, http.StatusBadRequest, nil)
	e.do("PATCH", base+"/brief", `[1]`, nil, http.StatusBadRequest, nil)
	e.do("PATCH", "/api/v1/projects/prj_missing/brief", `{}`, nil, http.StatusNotFound, nil)

	// Merge: null clears, others stay; the project carries the brief.
	var b hoard.ProjectBrief
	e.do("PATCH", base+"/brief", `{"notes":"hi","engine":null}`, nil, http.StatusOK, &b)
	if b.Engine != "" || b.Notes != "hi" || !b.PixelArt {
		t.Errorf("merge: %+v", b)
	}
	var got hoard.Project
	e.do("GET", base, nil, nil, http.StatusOK, &got)
	if !strings.Contains(string(got.Brief), `"notes":"hi"`) {
		t.Errorf("project.brief = %s", got.Brief)
	}
	e.do("GET", base+"/brief", nil, nil, http.StatusOK, &b)
	if b.Notes != "hi" {
		t.Errorf("GET brief: %+v", b)
	}

	// Fit: POST only, against one project or all.
	e.do("GET", "/api/v1/assets/"+bad.ID+"/fit", nil, nil, http.StatusNotFound, nil) // no GET: a fit check stores measurements
	var fit struct {
		Reports []hoard.FitReport `json:"reports"`
	}
	e.do("POST", "/api/v1/assets/"+bad.ID+"/fit", nil, nil, http.StatusOK, &fit)
	if len(fit.Reports) != 1 || fit.Reports[0].ProjectID != prj.ID || !fit.Reports[0].HasBrief || fit.Reports[0].Errors < 2 {
		t.Fatalf("fit: %+v", fit)
	}
	codes := map[string]bool{}
	for _, i := range fit.Reports[0].Issues {
		codes[i.Code] = true
	}
	if !codes["max_texture_size"] || !codes["palette"] || !codes["attribution_missing"] {
		t.Errorf("fit codes: %v", codes)
	}
	// A chunked, empty body is fine too.
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/assets/"+good.ID+"/fit", io.NopCloser(strings.NewReader("")))
	req.Header.Set("Authorization", "Bearer "+e.token)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("empty chunked fit body: %v %v", err, resp)
	}
	e.do("POST", "/api/v1/assets/"+good.ID+"/fit", `{"project_id":"`+prj.ID+`"}`, nil, http.StatusOK, &fit)
	if fit.Reports[0].Errors != 0 || fit.Reports[0].Warnings != 0 {
		t.Errorf("good asset: %+v", fit.Reports[0])
	}
	e.do("POST", "/api/v1/assets/"+good.ID+"/fit", `{"project_id":"prj_missing"}`, nil, http.StatusNotFound, nil)

	// Report.
	var rep hoard.ProjectReport
	e.do("GET", base+"/report", nil, nil, http.StatusOK, &rep)
	if rep.Verdict != "problems" || rep.AssetCount != 2 || len(rep.FitIssues) != 1 || len(rep.MissingAttribution) != 1 {
		t.Errorf("report: %+v", rep)
	}

	// Credits as JSON and as a Markdown download; the project name is escaped.
	var cr hoard.Credits
	e.do("GET", base+"/credits", nil, nil, http.StatusOK, &cr)
	if cr.AssetCount != 2 || len(cr.NeedsAttention) != 1 || !strings.Contains(cr.Markdown, "## CC-BY-4.0") || strings.Contains(cr.Markdown, "<b>") {
		t.Errorf("credits: %+v", cr)
	}
	res := e.do("GET", base+"/credits.md", nil, nil, http.StatusOK, nil)
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") || !strings.Contains(res.Header.Get("Content-Disposition"), "CREDITS.md") {
		t.Errorf("credits.md headers: %v", res.Header)
	}
	if body, _ := io.ReadAll(res.Body); !strings.HasPrefix(string(body), "# Credits") {
		t.Errorf("credits.md body: %.60s", body)
	}
}

func zipNames(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestProjectBundle(t *testing.T) {
	e, prj, good, bad := briefEnv(t)
	other, _ := e.svc.SaveProject(context.Background(), "", hoard.GroupInput{Name: ptrS("Other")})

	// The signed-in variant: one folder per asset, plus CREDITS.md.
	res := e.do("GET", "/api/v1/projects/"+prj.ID+"/bundle", nil, nil, http.StatusOK, nil)
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("bundle Cache-Control = %q", res.Header.Get("Cache-Control"))
	}
	body, _ := io.ReadAll(res.Body)
	files := zipNames(t, body)
	if _, ok := files["CREDITS.md"]; !ok || files["tile_a/tile_a.png"] == "" || files["tile_b/tile_b.png"] == "" || files["tile_a/asset.json"] == "" {
		var names []string
		for n := range files {
			names = append(names, n)
		}
		t.Fatalf("bundle contents: %v", names)
	}
	if !strings.Contains(files["CREDITS.md"], "tile\\_a") {
		t.Errorf("CREDITS.md in bundle: %s", files["CREDITS.md"])
	}
	// A rejected asset is left out.
	if _, err := e.svc.UpdateAsset(context.Background(), bad.ID, hoard.AssetInput{Status: ptrS("rejected")}); err != nil {
		t.Fatal(err)
	}
	res = e.do("GET", "/api/v1/projects/"+prj.ID+"/bundle", nil, nil, http.StatusOK, nil)
	body, _ = io.ReadAll(res.Body)
	if files = zipNames(t, body); files["tile_b/tile_b.png"] != "" || files["tile_a/tile_a.png"] == "" {
		t.Errorf("rejected asset in bundle")
	}
	_ = good

	// The signed variant for agents needs no session, is bound to the project
	// and refuses tampering and expiry.
	anon := &http.Client{}
	exp, sig := e.svc.Sign("bundle:"+prj.ID, time.Hour)
	link := func(id, exp, sig string) string {
		return "/api/v1/dl-bundle/" + id + "?exp=" + url.QueryEscape(exp) + "&sig=" + url.QueryEscape(sig)
	}
	e.send(anon, "GET", "/api/v1/projects/"+prj.ID+"/bundle", nil, nil, http.StatusUnauthorized, nil)
	res = e.send(anon, "GET", link(prj.ID, exp, sig), nil, nil, http.StatusOK, nil)
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("signed bundle Cache-Control = %q", res.Header.Get("Cache-Control"))
	}
	body, _ = io.ReadAll(res.Body)
	if zipNames(t, body)["CREDITS.md"] == "" {
		t.Error("signed bundle lacks CREDITS.md")
	}
	flip := []byte(sig)
	flip[0] ^= 1
	e.send(anon, "GET", link(prj.ID, exp, string(flip)), nil, nil, http.StatusForbidden, nil)
	e.send(anon, "GET", link(other.ID, exp, sig), nil, nil, http.StatusForbidden, nil) // bound to the project
	e.send(anon, "GET", link(prj.ID, "99999999999", sig), nil, nil, http.StatusForbidden, nil)
	e.send(anon, "GET", link(prj.ID, "", ""), nil, nil, http.StatusForbidden, nil)
	pastExp, pastSig := e.svc.Sign("bundle:"+prj.ID, -time.Minute)
	e.send(anon, "GET", link(prj.ID, pastExp, pastSig), nil, nil, http.StatusForbidden, nil)
	// A link for something else (a zip of ids) does not open a bundle.
	zExp, zSig := e.svc.Sign("zip:"+prj.ID, time.Hour)
	e.send(anon, "GET", link(prj.ID, zExp, zSig), nil, nil, http.StatusForbidden, nil)
}

func ptrS(s string) *string { return &s }

func TestMCPBrief(t *testing.T) {
	e, prj, good, bad := briefEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   e.ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{e.token}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args map[string]any) (map[string]any, *mcp.CallToolResult) {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var out map[string]any
		if txt, ok := res.Content[0].(*mcp.TextContent); ok {
			_ = json.Unmarshal([]byte(txt.Text), &out)
			if out == nil {
				out = map[string]any{"text": txt.Text}
			}
		}
		return out, res
	}

	out, _ := call("get_project_brief", map[string]any{"project_id": prj.ID})
	if out["empty"] != false || out["brief"].(map[string]any)["engine"] != "godot" {
		t.Errorf("get_project_brief: %v", out)
	}
	// Set with a null that clears, a replaced list, and a rejected value.
	out, res := call("set_project_brief", map[string]any{"project_id": prj.ID, "brief": map[string]any{
		"engine": nil, "tile_size": 16, "palette": []string{"#ABCDEF"}, "audio": map[string]any{"sample_rate": 48000}}})
	br := out["brief"].(map[string]any)
	if res.IsError || br["engine"] != nil || br["tile_size"] != 16.0 || br["palette"].([]any)[0] != "#abcdef" {
		t.Errorf("set_project_brief: %v %v", res.IsError, out)
	}
	if _, res = call("set_project_brief", map[string]any{"project_id": prj.ID, "brief": map[string]any{"tile_size": -1}}); !res.IsError {
		t.Error("invalid brief accepted over MCP")
	}
	if _, res = call("get_project_brief", map[string]any{"project_id": "prj_nope"}); !res.IsError {
		t.Error("unknown project accepted")
	}
	// The activity feed names the actor.
	feed, _ := e.svc.ListActivity(ctx, hoard.ActivityQuery{Action: "project.brief", Limit: 1})
	if len(feed) != 1 || feed[0].Via != "mcp" {
		t.Errorf("activity: %+v", feed)
	}

	// Fit: the project id selects, omitting it checks every project of the asset.
	out, _ = call("check_asset_fit", map[string]any{"asset_ids": []string{bad.ID, good.ID}})
	results := out["results"].([]any)
	if len(results) != 2 || len(results[0].(map[string]any)["reports"].([]any)) != 1 {
		t.Fatalf("check_asset_fit: %v", out)
	}
	first := results[0].(map[string]any)["reports"].([]any)[0].(map[string]any)
	if first["errors"].(float64) < 1 || first["project_id"] != prj.ID {
		t.Errorf("fit report: %v", first)
	}
	solo := e.upload("solo.png", testPNG(t, 8, 8, color.Black), "")
	out, _ = call("check_asset_fit", map[string]any{"asset_ids": []string{solo.ID}, "project_id": prj.ID})
	if r := out["results"].([]any)[0].(map[string]any)["reports"].([]any); len(r) != 1 {
		t.Errorf("explicit project: %v", out)
	}
	out, _ = call("check_asset_fit", map[string]any{"asset_ids": []string{solo.ID}})
	if n, _ := out["results"].([]any)[0].(map[string]any)["note"].(string); !strings.Contains(n, "project_id") {
		t.Errorf("asset without project: %v", out)
	}
	// One unknown id does not abort the others.
	out, res = call("check_asset_fit", map[string]any{"asset_ids": []string{"ast_000000000000", good.ID}})
	rs := out["results"].([]any)
	if res.IsError || len(rs) != 2 || rs[0].(map[string]any)["error"] == nil || rs[1].(map[string]any)["error"] != nil || len(rs[1].(map[string]any)["reports"].([]any)) != 1 {
		t.Errorf("partial results: %v", out)
	}
	if _, res = call("check_asset_fit", map[string]any{"asset_ids": []string{good.ID}, "project_id": "prj_nope"}); !res.IsError {
		t.Error("unknown project accepted")
	}
	// Unknown brief fields are refused with a hint.
	if _, res = call("set_project_brief", map[string]any{"project_id": prj.ID, "brief": map[string]any{"Engine": "godot"}}); !res.IsError {
		t.Error("miscased field accepted")
	}
	many := make([]string, 51)
	for i := range many {
		many[i] = good.ID
	}
	if _, res = call("check_asset_fit", map[string]any{"asset_ids": many}); !res.IsError {
		t.Error("51 assets accepted")
	}

	out, _ = call("project_report", map[string]any{"project_id": prj.ID})
	if out["verdict"] != "problems" || out["asset_count"] != 2.0 {
		t.Errorf("project_report: %v", out)
	}

	out, _ = call("get_project_credits", map[string]any{"project_id": prj.ID})
	cmd, _ := out["bundle_command"].(string)
	if !strings.HasPrefix(cmd, "curl -fsS --globoff -o ") || !strings.Contains(cmd, "/api/v1/dl-bundle/"+prj.ID+"?exp=") || !strings.Contains(out["markdown"].(string), "# Credits") {
		t.Errorf("get_project_credits: %v", out)
	}
	// The link in the command works without credentials.
	if m := regexp.MustCompile(`'([^']+)'`).FindStringSubmatch(cmd); m != nil {
		u := strings.Replace(m[1], "http://localhost:8080", e.ts.URL, 1)
		resp, err := http.Get(u)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Errorf("bundle link: %v %v", err, resp)
		}
	}

	// The brief as a resource template.
	tpls, err := session.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tpl := range tpls.ResourceTemplates {
		found = found || tpl.URITemplate == "hoard://project/{id}/brief"
	}
	if !found {
		t.Fatalf("resource template missing: %+v", tpls.ResourceTemplates)
	}
	rr, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "hoard://project/" + prj.ID + "/brief"})
	if err != nil || len(rr.Contents) != 1 || rr.Contents[0].MIMEType != "application/json" || !strings.Contains(rr.Contents[0].Text, `"tile_size":16`) {
		t.Errorf("read resource: %v %+v", err, rr)
	}
	if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "hoard://project/prj_nope/brief"}); err == nil {
		t.Error("resource for an unknown project")
	}
	if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "hoard://project/a/b/brief"}); err == nil {
		t.Error("resource with a slash in the id")
	}
}
