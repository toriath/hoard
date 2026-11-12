package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toriath/hoard/internal/hoard"
)

func TestActivityLineageRecipeREST(t *testing.T) {
	e := newEnv(t)
	root := e.upload("root.bin", []byte("x"), `{"name":"root","prompt":"a knight","generator":{"tool":"Draw Things","seed":3}}`)
	child := e.upload("child.bin", []byte("y"), `{"name":"child","derived_from":"`+root.ID+`"}`)

	var feed hoard.ActivityFeed
	e.do("GET", "/api/v1/activity?asset="+child.ID, nil, nil, http.StatusOK, &feed)
	if len(feed.Items) == 0 || feed.Items[0].AssetName != "child" || feed.LatestSeq < feed.Items[0].Seq {
		t.Fatalf("feed = %+v", feed)
	}
	for _, it := range feed.Items {
		if it.AssetID != child.ID {
			t.Errorf("asset filter leaked %+v", it)
		}
	}
	var polled hoard.ActivityFeed
	e.do("GET", "/api/v1/activity?after="+strconv.FormatInt(feed.LatestSeq, 10), nil, nil, http.StatusOK, &polled)
	if len(polled.Items) != 0 || polled.LatestSeq != feed.LatestSeq {
		t.Errorf("polling past the end = %+v", polled)
	}
	var web hoard.ActivityFeed
	e.do("GET", "/api/v1/activity?via=web&action=asset.&limit=1", nil, nil, http.StatusOK, &web)
	if len(web.Items) > 1 {
		t.Errorf("limit ignored: %d", len(web.Items))
	}

	var l hoard.Lineage
	e.do("GET", "/api/v1/assets/"+child.ID+"/lineage", nil, nil, http.StatusOK, &l)
	if len(l.Ancestors) != 1 || l.Ancestors[0].ID != root.ID {
		t.Errorf("lineage = %+v", l)
	}
	var r hoard.Recipe
	e.do("GET", "/api/v1/assets/"+root.ID+"/recipe", nil, nil, http.StatusOK, &r)
	if !r.HasRecipe || r.Prompt != "a knight" || len(r.Variations) != 1 {
		t.Errorf("recipe = %+v", r)
	}
	e.do("GET", "/api/v1/assets/ast_000000000000/recipe", nil, nil, http.StatusNotFound, nil)
	e.do("GET", "/api/v1/activity?since=yesterday", nil, nil, http.StatusBadRequest, nil)
	e.do("GET", "/api/v1/activity?since=2026-01-01T00:00:00%2B02:00", nil, nil, http.StatusOK, nil)
	// Needs authentication like the rest of the API.
	e.send(&http.Client{}, "GET", "/api/v1/activity", nil, nil, http.StatusUnauthorized, nil)
}

func TestMCPRecentChangesAndRecipe(t *testing.T) {
	e := newEnv(t)
	root := e.upload("root.bin", []byte("x"), `{"name":"root","prompt":"a knight","generator":{"tool":"Draw Things","seed":3}}`)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	first := call("recent_changes", map[string]any{})
	latest := first["latest_seq"].(float64)
	if latest == 0 || first["count"].(float64) == 0 {
		t.Fatalf("recent_changes = %v", first)
	}
	if call("recent_changes", map[string]any{"after_seq": latest})["count"].(float64) != 0 {
		t.Error("entries after latest_seq")
	}
	// An agent creating an asset over MCP shows up for the next poll, with its name.
	call("create_asset", map[string]any{"name": "from agent", "derived_from": root.ID})
	next := call("recent_changes", map[string]any{"after_seq": latest, "action_prefix": "asset."})
	entries := next["entries"].([]any)
	if len(entries) == 0 || entries[0].(map[string]any)["asset_name"] != "from agent" || entries[0].(map[string]any)["via"] != "mcp" {
		t.Fatalf("poll = %v", next)
	}
	if next["next_seq"].(float64) <= latest {
		t.Errorf("next_seq = %v", next["next_seq"])
	}

	rec := call("get_recipe", map[string]any{"id": root.ID})
	if rec["prompt"] != "a knight" || rec["reproduce"] == "" || len(rec["variations"].([]any)) != 1 {
		t.Errorf("recipe = %v", rec)
	}
}
