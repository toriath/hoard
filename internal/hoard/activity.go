package hoard

import (
	"context"
	"database/sql"
	"strings"
)

// Activity is one entry of the change feed.
type Activity struct {
	Seq     int64  `json:"seq"`
	At      string `json:"at"`
	Actor   string `json:"actor"`
	Via     string `json:"via"`
	Action  string `json:"action"`
	AssetID string `json:"asset_id,omitempty"`
	RefID   string `json:"ref_id,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// record appends an entry to the activity feed. Pass the transaction of the
// change so that the entry exists exactly when the change does. Failures are
// logged, never returned: the feed must not break a mutation.
func (s *Service) record(ctx context.Context, q execer, action, assetID, refID, summary string) {
	if q == nil {
		q = s.db
	}
	a := ActorFrom(ctx)
	if len(summary) > 500 {
		summary = summary[:500] + "…"
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO activity(at, actor, user_id, via, action, asset_id, ref_id, summary)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, now(), a.String(), a.UserID, a.Via, action, assetID, refID, strings.ToValidUTF8(summary, "")); err != nil {
		s.log.Warn("record activity", "action", action, "err", err)
	}
}

// ActivityQuery filters the feed. Entries are returned newest first.
type ActivityQuery struct {
	AssetID  string // only this asset
	Since    string // RFC 3339; only entries after this time
	AfterSeq int64  // only entries with a larger seq (for polling)
	Action   string // prefix, e.g. "request." or "asset.update"; several prefixes separated by commas
	Via      string // only these channels, separated by commas: web, api, mcp, system
	Limit    int    // default 50, max 500
}

// ListActivity returns feed entries, newest first (oldest first when AfterSeq is set).
func (s *Service) ListActivity(ctx context.Context, q ActivityQuery) ([]Activity, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 500 {
		q.Limit = 500
	}
	where := []string{"1 = 1"}
	var args []any
	if q.AssetID != "" {
		where = append(where, "asset_id = ?")
		args = append(args, q.AssetID)
	}
	if q.Since != "" {
		where = append(where, "at > ?")
		args = append(args, q.Since)
	}
	if q.AfterSeq > 0 {
		where = append(where, "seq > ?")
		args = append(args, q.AfterSeq)
	}
	if prefixes := splitList(q.Action); len(prefixes) > 0 {
		var ors []string
		for _, p := range prefixes {
			ors = append(ors, "action LIKE ? ESCAPE '\\'")
			args = append(args, strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(p)+"%")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	if vias := splitList(q.Via); len(vias) > 0 {
		where = append(where, "via IN ("+strings.TrimSuffix(strings.Repeat("?,", len(vias)), ",")+")")
		for _, v := range vias {
			args = append(args, v)
		}
	}
	// Polling (AfterSeq) returns the oldest new entries first, so a client
	// that follows the last seq it saw never skips entries when more than
	// Limit arrived in between. Everything else is newest first.
	order := "DESC"
	if q.AfterSeq > 0 {
		order = "ASC"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq, at, actor, via, action, asset_id, ref_id, summary FROM activity
		WHERE `+strings.Join(where, " AND ")+` ORDER BY seq `+order+` LIMIT ?`, append(args, q.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.Seq, &a.At, &a.Actor, &a.Via, &a.Action, &a.AssetID, &a.RefID, &a.Summary); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
