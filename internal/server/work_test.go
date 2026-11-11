package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toriath/hoard/internal/hoard"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{uint8(x * 7), uint8(y * 7), 120, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestWorkEndpointsNeedAuthentication: every endpoint of the request queue and
// review steering refuses anonymous callers, and nothing that changes state
// answers to GET.
func TestWorkEndpointsNeedAuthentication(t *testing.T) {
	e := newEnv(t)
	anon := &http.Client{}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/requests"},
		{"POST", "/api/v1/requests"},
		{"GET", "/api/v1/requests/req_000000000000"},
		{"PATCH", "/api/v1/requests/req_000000000000"},
		{"POST", "/api/v1/requests/req_000000000000/cancel"},
		{"POST", "/api/v1/requests/req_000000000000/claim"},
		{"POST", "/api/v1/requests/req_000000000000/renew"},
		{"POST", "/api/v1/requests/req_000000000000/release"},
		{"POST", "/api/v1/requests/req_000000000000/submit"},
		{"POST", "/api/v1/requests/req_000000000000/placeholder"},
		{"POST", "/api/v1/requests/req_000000000000/submissions/ast_000000000000/accept"},
		{"POST", "/api/v1/requests/req_000000000000/submissions/ast_000000000000/reject"},
		{"GET", "/api/v1/assets/ast_000000000000/review"},
		{"POST", "/api/v1/assets/ast_000000000000/review"},
		{"GET", "/api/v1/assets/ast_000000000000/notes"},
		{"POST", "/api/v1/assets/ast_000000000000/notes"},
		{"POST", "/api/v1/notes/not_000000000000/resolve"},
	} {
		e.send(anon, c.method, c.path, `{}`, nil, http.StatusUnauthorized, nil)
	}
	// State changes are not reachable with GET.
	for _, p := range []string{"claim", "cancel", "submit", "placeholder", "release", "renew"} {
		e.do("GET", "/api/v1/requests/req_000000000000/"+p, nil, nil, http.StatusNotFound, nil)
	}
	// Cross-site posts on the session are refused.
	e.do("POST", "/api/v1/requests", `{"title":"x"}`, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden, nil)
}

func TestWorkRESTFlow(t *testing.T) {
	e := newEnv(t)
	var prj hoard.Project
	e.do("POST", "/api/v1/projects", `{"name":"Game"}`, nil, http.StatusOK, &prj)

	var rq hoard.Request
	e.do("POST", "/api/v1/requests", map[string]any{}, nil, http.StatusBadRequest, nil) // no title (non-string body is not sent; checks validation)
	e.do("POST", "/api/v1/requests", `{"title":"Coin","asset_type":"sprite","project_id":"`+prj.ID+`","spec":{"width":32,"height":32},"placeholder":true,"priority":2}`, nil, http.StatusCreated, &rq)
	if rq.Status != "open" || rq.PlaceholderAssetID == "" || rq.Priority != 2 {
		t.Fatalf("request = %+v", rq)
	}
	var list struct {
		Items []hoard.Request `json:"items"`
	}
	e.do("GET", "/api/v1/requests?status=open,claimed&type=sprite&project="+prj.ID, nil, nil, http.StatusOK, &list)
	if len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}
	e.do("GET", "/api/v1/requests?status=bogus", nil, nil, http.StatusBadRequest, nil)
	e.do("POST", "/api/v1/requests/"+rq.ID+"/claim", `{"lease_minutes":10}`, nil, http.StatusOK, &rq)
	e.do("POST", "/api/v1/requests/"+rq.ID+"/claim", ``, nil, http.StatusConflict, nil)

	cand := e.upload("coin.png", testPNG(t, 32, 32), `{"name":"Coin v1"}`)
	e.do("POST", "/api/v1/requests/"+rq.ID+"/submit", `{"asset_ids":["`+cand.ID+`"],"note":"try 1"}`, nil, http.StatusOK, &rq)
	if rq.Status != "submitted" || len(rq.Submissions) != 1 {
		t.Fatalf("submitted = %+v", rq)
	}
	e.do("POST", "/api/v1/requests/"+rq.ID+"/submissions/"+cand.ID+"/reject", `{"feedback":""}`, nil, http.StatusBadRequest, nil)
	e.do("POST", "/api/v1/requests/"+rq.ID+"/submissions/"+cand.ID+"/reject", `{"feedback":"darker"}`, nil, http.StatusOK, &rq)
	if rq.Status != "open" || rq.Submissions[0].Feedback != "darker" {
		t.Fatalf("rejected = %+v", rq)
	}
	e.do("POST", "/api/v1/requests/"+rq.ID+"/submit", `{"asset_ids":["`+cand.ID+`"]}`, nil, http.StatusOK, &rq)
	e.do("POST", "/api/v1/requests/"+rq.ID+"/submissions/"+cand.ID+"/accept", ``, nil, http.StatusOK, &rq)
	if rq.Status != "done" {
		t.Fatalf("accepted = %+v", rq)
	}

	var review struct {
		Status       string          `json:"status"`
		SupersededBy string          `json:"superseded_by"`
		Notes        []hoard.Note    `json:"notes"`
		Requests     []hoard.Request `json:"requests"`
	}
	e.do("GET", "/api/v1/assets/"+rq.PlaceholderAssetID+"/review", nil, nil, http.StatusOK, &review)
	if review.Status != "superseded" || review.SupersededBy != cand.ID || len(review.Requests) != 1 {
		t.Fatalf("placeholder review = %+v", review)
	}

	// Steering and notes.
	var rr hoard.ReviewResult
	e.do("POST", "/api/v1/assets/"+cand.ID+"/review", `{"verdict":"rework","feedback":"more shine","priority":1}`, nil, http.StatusOK, &rr)
	if rr.Asset.Status != "rework" || rr.Request == nil || rr.Request.Kind != "rework" {
		t.Fatalf("rework = %+v", rr)
	}
	e.do("POST", "/api/v1/assets/"+cand.ID+"/review", `{"verdict":"nonsense"}`, nil, http.StatusBadRequest, nil)
	var note hoard.Note
	e.do("POST", "/api/v1/assets/"+cand.ID+"/notes", `{"body":"looks <b>fine</b>"}`, nil, http.StatusCreated, &note)
	var notes struct {
		Items []hoard.Note `json:"items"`
	}
	e.do("GET", "/api/v1/assets/"+cand.ID+"/notes", nil, nil, http.StatusOK, &notes)
	if len(notes.Items) != 3 { // "darker", "more shine" and the comment
		t.Fatalf("notes = %+v", notes)
	}
	e.do("POST", "/api/v1/notes/"+note.ID+"/resolve", ``, nil, http.StatusOK, &note)
	if note.ResolvedAt == nil {
		t.Error("note not resolved")
	}
	e.do("GET", "/api/v1/assets/"+cand.ID+"/notes?open=1", nil, nil, http.StatusOK, &notes)
	if len(notes.Items) != 2 {
		t.Errorf("open notes = %+v", notes)
	}
	e.do("PATCH", "/api/v1/requests/"+rr.Request.ID, `{"priority":3}`, nil, http.StatusOK, &rq)
	e.do("POST", "/api/v1/requests/"+rr.Request.ID+"/cancel", ``, nil, http.StatusOK, &rq)
	if rq.Status != "cancelled" {
		t.Errorf("cancel = %+v", rq)
	}
}

func resultJSON(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %+v", res.Content[0])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("%v in %s", err, text.Text)
	}
	return out
}

var reURL = regexp.MustCompile(`'(http[^']+)'`)

// TestMCPWorkLoop plays both sides over the real MCP endpoint with the
// official SDK client: a coding agent requests an asset (with placeholder), a
// generator agent claims it, creates the asset, uploads over the signed link
// and submits, and the reviewer accepts; then the reviewer sends the asset back
// for rework, which the generator picks up with the feedback.
func TestMCPWorkLoop(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	prj, err := e.svc.SaveProject(ctx, "", hoard.GroupInput{Name: ptrS("Game")})
	if err != nil {
		t.Fatal(err)
	}
	var second struct {
		Secret string `json:"secret"`
	}
	e.do("POST", "/api/v1/tokens", `{"name":"reviewer"}`, nil, http.StatusCreated, &second)
	connect := func(token ...string) *mcp.ClientSession {
		tok := e.token
		if len(token) > 0 {
			tok = token[0]
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
		s, err := client.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: e.ts.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{tok}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	var coder, gen, rev *mcp.ClientSession
	call := func(s *mcp.ClientSession, name string, args map[string]any) map[string]any {
		t.Helper()
		// Both agents share one token; the generator tells itself apart with a label.
		if s == gen && (name == "claim_request" || name == "renew_claim" || name == "release_request" || name == "submit_request") {
			args["agent"] = "gen"
		}
		res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return resultJSON(t, res)
	}
	coder, gen, rev = connect(), connect(), connect(second.Secret)

	// Tools and prompts are published with descriptions.
	tl, _ := coder.ListTools(ctx, nil)
	have := map[string]bool{}
	for _, tool := range tl.Tools {
		have[tool.Name] = true
	}
	for _, n := range []string{"request_asset", "list_requests", "get_request", "claim_request", "renew_claim", "release_request",
		"submit_request", "cancel_request", "review_asset", "review_submission", "add_note", "list_notes", "make_placeholder"} {
		if !have[n] {
			t.Errorf("tool %s missing", n)
		}
	}
	pl, err := coder.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, p := range pl.Prompts {
		names[p.Name] = true
		if p.Description == "" {
			t.Errorf("prompt %s has no description", p.Name)
		}
	}
	for _, n := range []string{"work-request-queue", "request-missing-assets", "review-submissions", "release-check", "tidy-library"} {
		if !names[n] {
			t.Errorf("prompt %s missing", n)
		}
	}
	pr, err := coder.GetPrompt(ctx, &mcp.GetPromptParams{Name: "work-request-queue", Arguments: map[string]string{"tool": "Draw Things", "asset_types": "sprite, ui"}})
	if err != nil || len(pr.Messages) != 1 {
		t.Fatalf("prompt: %v", err)
	}
	if txt := pr.Messages[0].Content.(*mcp.TextContent).Text; !strings.Contains(txt, "Draw Things") || !strings.Contains(txt, `asset_types=["sprite","ui"]`) {
		t.Errorf("prompt text = %q", txt)
	}

	// 1. The coding agent asks for a coin and gets a placeholder.
	out := call(coder, "request_asset", map[string]any{"title": "Gold coin", "description": "spinning coin", "asset_type": "sprite",
		"project_id": prj.ID, "spec": map[string]any{"width": 32, "height": 32, "filename": "coin.png"}, "placeholder": true, "dir": "assets/sprites"})
	rq := out["request"].(map[string]any)
	reqID := rq["id"].(string)
	ph := out["placeholder"].(map[string]any)
	phID := ph["asset"].(map[string]any)["id"].(string)
	cmd := ph["download_commands"].([]any)[0].(map[string]any)["command"].(string)
	if !strings.Contains(cmd, "assets/sprites/coin.png") || !strings.HasPrefix(cmd, "curl -fsS --globoff") {
		t.Errorf("placeholder command = %s", cmd)
	}

	// 2. The generator claims the next sprite request.
	item := call(gen, "claim_request", map[string]any{"asset_types": []string{"sprite"}, "lease_minutes": 30})
	if item["claimed"] != true || item["request"].(map[string]any)["id"] != reqID || item["project_brief"] == nil || len(item["next_steps"].([]any)) < 5 || item["lease"] == nil {
		t.Fatalf("work item = %v", item)
	}
	if again := call(gen, "claim_request", map[string]any{}); again["claimed"] != false {
		t.Errorf("queue should be empty: %v", again)
	}
	// While claimed, a second claim of the same id is refused.
	if res, err := coder.CallTool(ctx, &mcp.CallToolParams{Name: "claim_request", Arguments: map[string]any{"request_id": reqID}}); err != nil || !res.IsError {
		t.Errorf("double claim: %v %+v", err, res)
	}
	call(gen, "renew_claim", map[string]any{"request_id": reqID, "lease_minutes": 45})

	// 3. Create the asset and upload over the signed link.
	created := call(gen, "create_asset", map[string]any{"name": "Gold coin v1", "type": "sprite", "prompt": "a gold coin",
		"generator": map[string]any{"tool": "Draw Things", "seed": 5}, "filenames": []string{"coin.png"}})
	candID := created["asset"].(map[string]any)["id"].(string)
	up := created["upload"].(map[string]any)["commands"].([]any)[0].(map[string]any)["command"].(string)
	m := reURL.FindAllStringSubmatch(up, -1)
	putReq, _ := http.NewRequest("PUT", m[len(m)-1][1], bytes.NewReader(testPNG(t, 32, 32)))
	resp, err := http.DefaultClient.Do(putReq)
	if err != nil || resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %v %v", err, resp)
	}
	resp.Body.Close()

	// 4. Submit; the reviewer accepts.
	sub := call(gen, "submit_request", map[string]any{"request_id": reqID, "asset_ids": []string{candID}, "note": "first try"})
	if sub["request"].(map[string]any)["status"] != "submitted" || sub["warning"] != nil {
		t.Fatalf("submit = %v", sub)
	}
	acc := call(rev, "review_submission", map[string]any{"request_id": reqID, "asset_id": candID, "decision": "accept"})
	if acc["request"].(map[string]any)["status"] != "done" {
		t.Fatalf("accept = %v", acc)
	}
	phAsset, _ := e.svc.GetAsset(ctx, phID)
	cand, _ := e.svc.GetAsset(ctx, candID)
	if phAsset.Status != "superseded" || phAsset.SupersededBy != candID || cand.Status != "approved" || len(cand.Projects) != 1 || cand.Generator["tool"] != "Draw Things" {
		t.Errorf("placeholder %q by %q; candidate %q projects %v", phAsset.Status, phAsset.SupersededBy, cand.Status, cand.Projects)
	}
	done := call(coder, "list_requests", map[string]any{"statuses": []string{"done"}})
	if done["count"] != 1.0 {
		t.Errorf("done list = %v", done)
	}

	// 5. Steering: send it back for rework, the generator receives the feedback.
	rw := call(rev, "review_asset", map[string]any{"asset_id": candID, "verdict": "rework", "feedback": "make it shinier", "priority": 1})
	rwReq := rw["rework_request"].(map[string]any)
	if rw["asset"].(map[string]any)["status"] != "rework" || rwReq["kind"] != "rework" {
		t.Fatalf("rework = %v", rw)
	}
	item = call(gen, "claim_request", map[string]any{"kind": "rework"})
	target := item["target_asset"].(map[string]any)
	feedback := item["rework_feedback"].([]any)
	if item["request"].(map[string]any)["id"] != rwReq["id"] || target["id"] != candID || target["prompt"] != "a gold coin" ||
		len(feedback) != 1 || feedback[0].(map[string]any)["body"] != "make it shinier" || len(target["download_commands"].([]any)) != 1 {
		t.Fatalf("rework work item = %v", item)
	}
	notes := call(coder, "list_notes", map[string]any{"asset_id": candID})
	if notes["count"] != 1.0 {
		t.Errorf("notes = %v", notes)
	}
	// Releasing puts it back; a placeholder for a rework request is refused.
	call(gen, "release_request", map[string]any{"request_id": rwReq["id"]})
	if res, _ := coder.CallTool(ctx, &mcp.CallToolParams{Name: "make_placeholder", Arguments: map[string]any{"request_id": rwReq["id"]}}); !res.IsError {
		t.Error("placeholder for rework accepted")
	}
	// A label is the caller's choice: the generator with another label is still refused.
	own0 := call(coder, "request_asset", map[string]any{"title": "Labelled", "asset_type": "sprite"})["request"].(map[string]any)["id"].(string)
	lab := call(coder, "create_asset", map[string]any{"name": "labelled"})["asset"].(map[string]any)["id"].(string)
	call(gen, "submit_request", map[string]any{"request_id": own0, "asset_ids": []string{lab}})
	genB := map[string]any{"request_id": own0, "asset_id": lab, "decision": "accept"}
	if res, err := gen.CallTool(ctx, &mcp.CallToolParams{Name: "review_submission", Arguments: genB}); err != nil || !res.IsError {
		t.Errorf("review by the submitting token: %v %+v", err, res)
	}
	// Self-review: an agent cannot judge what it submitted itself (same token, no label).
	own := call(coder, "request_asset", map[string]any{"title": "Own work", "asset_type": "sprite"})["request"].(map[string]any)["id"].(string)
	mine := call(coder, "create_asset", map[string]any{"name": "mine"})["asset"].(map[string]any)["id"].(string)
	call(coder, "submit_request", map[string]any{"request_id": own, "asset_ids": []string{mine}})
	if res, err := coder.CallTool(ctx, &mcp.CallToolParams{Name: "review_submission", Arguments: map[string]any{"request_id": own, "asset_id": mine, "decision": "accept"}}); err != nil || !res.IsError ||
		!strings.Contains(res.Content[0].(*mcp.TextContent).Text, "cannot be reviewed by the agent that made it") {
		t.Errorf("self review: %v %+v", err, res)
	}
	if res, _ := coder.CallTool(ctx, &mcp.CallToolParams{Name: "review_asset", Arguments: map[string]any{"asset_id": mine, "verdict": "approve"}}); !res.IsError {
		t.Error("self approve accepted")
	}
	// ...but the generator label makes it another agent, and the user's browser session always may.
	e.do("POST", "/api/v1/requests/"+own+"/submissions/"+mine+"/accept", ``, nil, http.StatusOK, nil)

	// Everything was attributed to the token.
	feed, _ := e.svc.ListActivity(ctx, hoard.ActivityQuery{Action: "request.", Limit: 50})
	for _, a := range feed {
		if a.Via != "mcp" && a.Action != "request.create" && a.Action != "request.accept" {
			t.Errorf("activity %s via %s", a.Action, a.Via)
		}
	}
}

func ptrS(s string) *string { return &s }
