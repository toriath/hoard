package hoard

import (
	"context"
	"testing"
)

func TestTechnicalAttrsAreNotAnEdit(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	a := mustAsset(t, s, AssetInput{Name: ptr("Boom"), Attrs: map[string]any{"fps": 12.0}}, "", nil)
	old := "2020-01-02T03:04:05Z"
	if _, err := s.db.Exec(`UPDATE assets SET updated_at = ? WHERE id = ?`, old, a.ID); err != nil {
		t.Fatal(err)
	}
	var before int
	s.db.QueryRow(`SELECT COUNT(*) FROM activity WHERE asset_id = ?`, a.ID).Scan(&before)

	got, err := s.SetTechnicalAttrs(ctx, a.ID, map[string]any{"duration_s": 1.5, "channels": 2.0})
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdatedAt != old {
		t.Errorf("updated_at moved to %s", got.UpdatedAt)
	}
	var after int
	s.db.QueryRow(`SELECT COUNT(*) FROM activity WHERE asset_id = ?`, a.ID).Scan(&after)
	if after != before {
		t.Errorf("%d activity entries were written", after-before)
	}
	if got.Attrs["duration_s"] != 1.5 || got.Attrs["channels"] != 2.0 || got.Attrs["fps"] != 12.0 {
		t.Errorf("attrs not merged: %v", got.Attrs)
	}
	sidecarMatchesDB(t, s, a.ID)

	// Only measured numbers are accepted: this is no back door for edits.
	for name, attrs := range map[string]map[string]any{
		"other key":    {"frame_width": 16.0},
		"string value": {"duration_s": "long"},
		"negative":     {"duration_s": -1.0},
		"nested":       {"width": map[string]any{"a": 1}},
	} {
		if _, err := s.SetTechnicalAttrs(ctx, a.ID, attrs); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := s.SetTechnicalAttrs(ctx, "ast_aaaaaaaaaaaa", map[string]any{"width": 1.0}); err == nil {
		t.Error("unknown asset accepted")
	}
}
