package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toriath/hoard/internal/hoard"
)

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// TestMCPWithSDKClient drives the endpoint with a real MCP client (the
// official SDK's Streamable HTTP transport) rather than hand-written
// JSON-RPC, covering the full handshake a client such as Claude Code performs.
func TestMCPWithSDKClient(t *testing.T) {
	e := newEnv(t)
	e.upload("coin_gold.png", []byte("not really a png"), `{"tags":["coin"]}`)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   e.ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{e.token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	if init := session.InitializeResult(); init == nil || init.ServerInfo.Name != "hoard" || init.Instructions == "" {
		t.Errorf("initialize result = %+v", init)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("tool %s lacks description or schema", tool.Name)
		}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_assets", Arguments: map[string]any{"query": "coin"}})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if res.IsError || !ok || !strings.Contains(text.Text, `"coin_gold"`) {
		t.Errorf("search result = %+v", res.Content[0])
	}
	// Changes over MCP are attributed to the token's user and name.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "create_asset", Arguments: map[string]any{"name": "attributed"}})
	if err != nil || res.IsError {
		t.Fatalf("create_asset: %v %+v", err, res)
	}
	feed, err := e.svc.ListActivity(ctx, hoard.ActivityQuery{Action: "asset.create", Limit: 1})
	if err != nil || len(feed) != 1 || feed[0].Via != "mcp" || feed[0].Actor != "alice via MCP (test)" {
		t.Errorf("activity actor = %+v %v", feed, err)
	}
	// A file uploaded over a signed link is attributed to the link, not to the system.
	var made struct {
		Asset struct {
			ID string `json:"id"`
		} `json:"asset"`
		Upload struct {
			Commands []struct {
				Command string `json:"command"`
			} `json:"commands"`
		} `json:"upload"`
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "create_asset", Arguments: map[string]any{"name": "via link", "filenames": []string{"a.txt"}}})
	if err != nil || res.IsError {
		t.Fatalf("create_asset with file: %v %+v", err, res)
	}
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &made); err != nil || len(made.Upload.Commands) != 1 {
		t.Fatalf("upload commands: %v %+v", err, made)
	}
	link := made.Upload.Commands[0].Command
	link = link[strings.LastIndex(link, " '")+2 : len(link)-1]
	req, _ := http.NewRequest("PUT", link, strings.NewReader("hello"))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("signed upload: %v %v", err, resp)
	}
	feed, err = e.svc.ListActivity(ctx, hoard.ActivityQuery{AssetID: made.Asset.ID, Action: "asset.file"})
	if err != nil || len(feed) != 1 || feed[0].Actor != "signed link via API" {
		t.Errorf("signed upload actor = %+v %v", feed, err)
	}
	// Schema validation happens before the handler: a wrong argument type is a readable tool error.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "search_assets", Arguments: map[string]any{"limit": "many"}})
	if err == nil && !res.IsError {
		t.Error("invalid arguments accepted")
	}
}

// TestMCPGallery covers the interactive gallery (an MCP App) the way a client
// that supports the extension uses it: the tool points at a view, the view is
// read as a resource, and the view's own tool calls return media inline.
func TestMCPGallery(t *testing.T) {
	e := newEnv(t)
	coin := e.upload("coin_gold.png", []byte("not really a png"), `{"tags":["coin"]}`)
	step := e.upload("step_stone.wav", []byte("RIFFfakewave"), `{"tags":["footstep"]}`)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   e.ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{e.token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	const uri = "ui://hoard/gallery"
	const mime = "text/html;profile=mcp-app"

	// The tool links to the view under both spellings of the key; the helper
	// tool is marked as callable by the view only.
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range tools.Tools {
		ui, _ := tool.Meta["ui"].(map[string]any)
		switch tool.Name {
		case "show_assets":
			seen[tool.Name] = true
			if ui["resourceUri"] != uri || tool.Meta["ui/resourceUri"] != uri {
				t.Errorf("show_assets _meta = %v", tool.Meta)
			}
		case "asset_media":
			seen[tool.Name] = true
			if vis, _ := ui["visibility"].([]any); len(vis) != 1 || vis[0] != "app" {
				t.Errorf("asset_media _meta = %v", tool.Meta)
			}
		default:
			if tool.Meta["ui"] != nil {
				t.Errorf("%s unexpectedly opens a view", tool.Name)
			}
		}
	}
	if !seen["show_assets"] || !seen["asset_media"] {
		t.Fatalf("gallery tools missing: %v", seen)
	}

	// The view itself.
	list, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Resources) != 1 || list.Resources[0].URI != uri || list.Resources[0].MIMEType != mime {
		t.Errorf("resources = %+v", list.Resources)
	}
	view, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Contents) != 1 {
		t.Fatalf("view contents = %d", len(view.Contents))
	}
	c := view.Contents[0]
	if c.URI != uri || c.MIMEType != mime || !strings.HasPrefix(c.Text, "<!doctype html>") || !strings.Contains(c.Text, "ui/initialize") {
		t.Errorf("view = %s %s %.40q", c.URI, c.MIMEType, c.Text)
	}
	if ui, _ := c.Meta["ui"].(map[string]any); ui["prefersBorder"] != true || ui["csp"] != nil {
		t.Errorf("view _meta = %v", c.Meta)
	}

	// show_assets: the same payload as text (for the model and for clients
	// without views) and as structured content (for the view).
	show := func(args map[string]any) (*mcp.CallToolResult, []map[string]any) {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "show_assets", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			return res, nil
		}
		sc, _ := res.StructuredContent.(map[string]any)
		raw, _ := sc["assets"].([]any)
		var assets []map[string]any
		for _, a := range raw {
			assets = append(assets, a.(map[string]any))
		}
		text, _ := res.Content[0].(*mcp.TextContent)
		for _, a := range assets {
			if text == nil || !strings.Contains(text.Text, a["id"].(string)) {
				t.Errorf("text result lacks %v", a["id"])
			}
		}
		return res, assets
	}

	res, assets := show(map[string]any{"title": "Which one?", "ids": []string{step.ID, coin.ID, step.ID}})
	if len(assets) != 2 || assets[0]["id"] != step.ID || assets[1]["id"] != coin.ID {
		t.Fatalf("show_assets by id = %v", assets)
	}
	if sc := res.StructuredContent.(map[string]any); sc["title"] != "Which one?" {
		t.Errorf("title = %v", sc["title"])
	}
	if assets[0]["audio_file"] != "step_stone.wav" || assets[0]["web_url"] != e.ts.URL+"/assets/"+step.ID {
		t.Errorf("audio asset = %v", assets[0])
	}
	if _, has := assets[1]["audio_file"]; has {
		t.Errorf("image asset offers audio: %v", assets[1])
	}
	if _, assets = show(map[string]any{"query": "coin"}); len(assets) != 1 || assets[0]["id"] != coin.ID {
		t.Errorf("show_assets by query = %v", assets)
	}
	if res, _ = show(map[string]any{"ids": []string{"../../etc/passwd"}}); !res.IsError {
		t.Error("invalid id accepted")
	}
	many := make([]string, 61)
	for i := range many {
		many[i] = coin.ID
	}
	if res, _ = show(map[string]any{"ids": many}); !res.IsError {
		t.Error("more than 60 ids accepted")
	}

	// asset_media: audio comes back inline, anything else is refused.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "asset_media", Arguments: map[string]any{"id": step.ID, "filename": "step_stone.wav"}})
	if err != nil {
		t.Fatal(err)
	}
	if audio, ok := res.Content[0].(*mcp.AudioContent); res.IsError || !ok || string(audio.Data) != "RIFFfakewave" || audio.MIMEType != "audio/wav" {
		t.Errorf("asset_media = %+v", res.Content[0])
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "asset_media", Arguments: map[string]any{"id": coin.ID, "filename": "coin_gold.png"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("asset_media returned a file that is not audio")
	}
}
