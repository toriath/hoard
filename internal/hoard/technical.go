package hoard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
)

// technicalAttrs are the attributes a browser measures while it renders a
// preview (see web/src/lib/thumbs.ts). They describe the file, not the
// curation of the asset, so recording them is not an edit.
var technicalAttrs = map[string]bool{
	"duration_s":  true,
	"sample_rate": true,
	"channels":    true,
	"width":       true,
	"height":      true,
	"triangles":   true,
}

// SetTechnicalAttrs merges measured, technical attributes into an asset.
// Only the keys in technicalAttrs with finite numeric values are accepted;
// anything else is an error. Unlike UpdateAsset it does not touch updated_at
// and writes no activity entry, so a preview rendered in the browser does not
// make the asset look changed. The sidecar is kept in sync.
func (s *Service) SetTechnicalAttrs(ctx context.Context, id string, attrs map[string]any) (*Asset, error) {
	if !ValidAssetID(id) {
		return nil, fmt.Errorf("asset %q: %w", id, ErrNotFound)
	}
	for k, v := range attrs {
		if !technicalAttrs[k] {
			return nil, invalid("%q is not a technical attribute (allowed: duration_s, sample_rate, channels, width, height, triangles)", k)
		}
		f, ok := v.(float64)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
			return nil, invalid("technical attribute %q must be a non-negative number", k)
		}
	}
	defer s.lockAssets(id)()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var cur string
		if err := tx.QueryRowContext(ctx, `SELECT attrs FROM assets WHERE id = ?`, id).Scan(&cur); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("asset %q: %w", id, ErrNotFound)
			}
			return err
		}
		m := map[string]any{}
		_ = json.Unmarshal([]byte(cur), &m)
		for k, v := range attrs {
			m[k] = v
		}
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE assets SET attrs = ? WHERE id = ?`, string(b), id)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.syncSidecar(ctx, id)
	return s.GetAsset(ctx, id)
}
