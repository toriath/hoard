package hoard

import (
	"context"
	"strings"
)

// ActivityEntry is a feed entry enriched for display: the name and thumbnail
// version of the asset it concerns, when that asset still exists.
type ActivityEntry struct {
	Activity
	AssetName    string `json:"asset_name,omitempty"`
	ThumbVersion int    `json:"thumb_version,omitempty"`
	AssetGone    bool   `json:"asset_gone,omitempty"` // purged; the entry outlives it
}

// ActivityFeed is a page of the feed plus the newest seq in the whole log, so
// that a client can poll with AfterSeq even when its filter matched nothing.
type ActivityFeed struct {
	Items     []ActivityEntry `json:"items"`
	LatestSeq int64           `json:"latest_seq"`
}

// ActivityRetentionDays is how long entries are kept by the daily maintenance.
const ActivityRetentionDays = 365

// ListActivityFeed is ListActivity with asset names and thumbnail versions
// added in one batched query.
func (s *Service) ListActivityFeed(ctx context.Context, q ActivityQuery) (*ActivityFeed, error) {
	items, err := s.ListActivity(ctx, q)
	if err != nil {
		return nil, err
	}
	feed := &ActivityFeed{Items: make([]ActivityEntry, len(items))}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM activity`).Scan(&feed.LatestSeq); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var ids []any
	for _, a := range items {
		if a.AssetID != "" && !seen[a.AssetID] {
			seen[a.AssetID] = true
			ids = append(ids, a.AssetID)
		}
	}
	type info struct {
		name  string
		thumb int
	}
	known := map[string]info{}
	// At most 500 distinct ids, well below SQLite's variable limit.
	if len(ids) > 0 {
		rows, err := s.db.QueryContext(ctx, `SELECT id, name, thumb_version FROM assets WHERE id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, ids...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var in info
			if err := rows.Scan(&id, &in.name, &in.thumb); err != nil {
				return nil, err
			}
			known[id] = in
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for i, a := range items {
		e := ActivityEntry{Activity: a}
		if a.AssetID != "" {
			if in, ok := known[a.AssetID]; ok {
				e.AssetName, e.ThumbVersion = in.name, in.thumb
			} else {
				e.AssetGone = true
			}
		}
		feed.Items[i] = e
	}
	return feed, nil
}

// PruneActivity deletes entries older than cutoff (RFC 3339) and returns how
// many were removed.
func (s *Service) PruneActivity(ctx context.Context, cutoff string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM activity WHERE at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
