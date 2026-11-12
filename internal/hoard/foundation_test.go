package hoard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusAndLineage(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	src := mustAsset(t, s, AssetInput{Name: ptr("knight")}, "", nil)
	gen := map[string]any{"tool": "Draw Things", "seed": 42.0}
	v, err := s.CreateAsset(ctx, AssetInput{Name: ptr("knight v2"), DerivedFrom: ptr(src.ID), Generator: gen, Status: ptr("draft")})
	if err != nil {
		t.Fatal(err)
	}
	if v.DerivedFrom != src.ID || v.Status != "draft" || v.Generator["tool"] != "Draw Things" {
		t.Fatalf("lineage not stored: %+v", v)
	}
	if _, err := s.UpdateAsset(ctx, v.ID, AssetInput{Status: ptr("bogus")}); err == nil {
		t.Fatal("invalid status accepted")
	}
	if _, err := s.UpdateAsset(ctx, v.ID, AssetInput{DerivedFrom: ptr(v.ID)}); err == nil {
		t.Fatal("self-derivation accepted")
	}
	if _, err := s.UpdateAsset(ctx, v.ID, AssetInput{DerivedFrom: ptr("ast_000000000000")}); err == nil {
		t.Fatal("unknown derived_from accepted")
	}
	if _, err := s.UpdateAsset(ctx, v.ID, AssetInput{Status: ptr("approved")}); err != nil {
		t.Fatal(err)
	}

	res, err := s.ListAssets(ctx, ListQuery{Statuses: []string{"approved"}})
	if err != nil || res.Total != 1 || res.Items[0].ID != v.ID {
		t.Fatalf("status filter: %v %+v", err, res)
	}
	res, err = s.ListAssets(ctx, ListQuery{Statuses: []string{"none"}})
	if err != nil || res.Total != 1 || res.Items[0].ID != src.ID {
		t.Fatalf("status none filter: %v %+v", err, res)
	}

	// The sidecar carries the new fields, so the library stays self-describing.
	b, err := os.ReadFile(filepath.Join(s.DataDir(), "assets", v.ID, "asset.json"))
	if err != nil {
		t.Fatal(err)
	}
	var side Asset
	if err := json.Unmarshal(b, &side); err != nil || side.Status != "approved" || side.DerivedFrom != src.ID || side.Generator["seed"] != 42.0 {
		t.Fatalf("sidecar: %v %s", err, b)
	}
}

func TestActivityRecordsActor(t *testing.T) {
	s := newService(t)
	ctx := WithActor(context.Background(), Actor{UserID: "usr_x", Username: "toriath", Via: "mcp", TokenName: "claude-code"})
	a := mustAsset(t, s, AssetInput{Name: ptr("coin")}, "", nil)
	if _, err := s.UpdateAsset(ctx, a.ID, AssetInput{AddTags: []string{"pickup"}, Status: ptr("rework")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFile(ctx, a.ID, "coin.png", strings.NewReader("not really a png"), 0); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListActivity(ctx, ActivityQuery{AssetID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %+v", got)
	}
	if got[0].Action != "asset.file" || got[1].Action != "asset.update" || got[2].Action != "asset.create" {
		t.Fatalf("order/actions: %+v", got)
	}
	if got[1].Actor != "toriath via MCP (claude-code)" || got[1].Via != "mcp" || !strings.Contains(got[1].Summary, "status → rework") {
		t.Fatalf("actor/summary: %+v", got[1])
	}
	if got[2].Via != "system" {
		t.Fatalf("background actor: %+v", got[2])
	}
	if only, _ := s.ListActivity(ctx, ActivityQuery{Action: "asset.upd"}); len(only) != 1 {
		t.Fatalf("action prefix filter: %+v", only)
	}
	if after, _ := s.ListActivity(ctx, ActivityQuery{AfterSeq: got[1].Seq}); len(after) != 1 || after[0].Seq != got[0].Seq {
		t.Fatalf("after_seq filter: %+v", after)
	}
}
