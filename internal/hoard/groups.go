package hoard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Collection is a logical group of assets: a pack, a visual style, the asset
// set planned for a project, or anything else.
type Collection struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Kind        string   `json:"kind"` // pack | style | project | other
	AssetCount  int      `json:"asset_count"`
	Covers      []string `json:"covers"` // up to four "<asset-id>:<thumb-version>" for mosaics
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// Project is a game (or other production) that assets were used in.
type Project struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
	Status      string   `json:"status"` // active | released | archived
	AssetCount  int      `json:"asset_count"`
	Covers      []string `json:"covers"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`

	Brief json.RawMessage `json:"brief"` // the project brief, see ProjectBrief ({} when none)
}

// GroupInput is the mutable part of a collection or project.
type GroupInput struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Kind        *string `json:"kind,omitempty"`   // collections
	URL         *string `json:"url,omitempty"`    // projects
	Status      *string `json:"status,omitempty"` // projects
}

// ProjectStatuses are the allowed project states.
var ProjectStatuses = []string{"active", "released", "archived"}

func (s *Service) covers(ctx context.Context, joinTable, col, id string) []string {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.thumb_version FROM `+joinTable+` j JOIN assets a ON a.id = j.asset_id
		WHERE j.`+col+` = ? AND a.deleted_at IS NULL AND a.thumb_version > 0 ORDER BY j.added_at DESC LIMIT 4`, id)
	if err != nil {
		return []string{}
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var aid string
		var v int
		if rows.Scan(&aid, &v) == nil {
			out = append(out, fmt.Sprintf("%s:%d", aid, v))
		}
	}
	return out
}

// ---- collections ----

// ListCollections returns all collections with asset counts.
func (s *Service) ListCollections(ctx context.Context) ([]Collection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.name, c.description, c.kind, c.created_at, c.updated_at,
		(SELECT COUNT(*) FROM collection_assets ca JOIN assets a ON a.id = ca.asset_id
			WHERE ca.collection_id = c.id AND a.deleted_at IS NULL)
		FROM collections c ORDER BY c.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	out := []Collection{}
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.Kind, &c.CreatedAt, &c.UpdatedAt, &c.AssetCount); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	for i := range out {
		out[i].Covers = s.covers(ctx, "collection_assets", "collection_id", out[i].ID)
	}
	return out, rows.Err()
}

// GetCollection returns one collection.
func (s *Service) GetCollection(ctx context.Context, id string) (*Collection, error) {
	all, err := s.ListCollections(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("collection %q: %w", id, ErrNotFound)
}

// SaveCollection creates (id == "") or updates a collection.
func (s *Service) SaveCollection(ctx context.Context, id string, in GroupInput) (*Collection, error) {
	if in.Kind != nil && !slices.Contains(CollectionKinds, *in.Kind) {
		return nil, invalid("unknown collection kind %q (valid: %s)", *in.Kind, strings.Join(CollectionKinds, ", "))
	}
	ts := now()
	if id == "" {
		name, err := groupName(in.Name)
		if err != nil {
			return nil, err
		}
		id = newID("col")
		kind := "pack"
		if in.Kind != nil {
			kind = *in.Kind
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO collections(id, name, description, kind, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?)`,
			id, name, strings.TrimSpace(deref(in.Description)), kind, ts, ts); err != nil {
			return nil, err
		}
		return s.GetCollection(ctx, id)
	}
	if _, err := s.GetCollection(ctx, id); err != nil {
		return nil, err
	}
	if in.Name != nil {
		name, err := groupName(in.Name)
		if err != nil {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE collections SET name = ? WHERE id = ?`, name, id); err != nil {
			return nil, err
		}
	}
	if in.Description != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE collections SET description = ? WHERE id = ?`, strings.TrimSpace(*in.Description), id); err != nil {
			return nil, err
		}
	}
	if in.Kind != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE collections SET kind = ? WHERE id = ?`, *in.Kind, id); err != nil {
			return nil, err
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE collections SET updated_at = ? WHERE id = ?`, ts, id); err != nil {
		return nil, err
	}
	s.syncMembers(ctx, "collection_assets", "collection_id", id)
	return s.GetCollection(ctx, id)
}

// DeleteCollection removes a collection; its assets are kept.
func (s *Service) DeleteCollection(ctx context.Context, id string) error {
	return s.deleteGroup(ctx, "collections", "collection_assets", "collection_id", id)
}

// ---- projects ----

// ListProjects returns all projects with usage counts.
func (s *Service) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.name, p.description, p.url, p.status, p.created_at, p.updated_at, p.brief,
		(SELECT COUNT(*) FROM project_assets pa JOIN assets a ON a.id = pa.asset_id
			WHERE pa.project_id = p.id AND a.deleted_at IS NULL)
		FROM projects p ORDER BY p.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	out := []Project{}
	for rows.Next() {
		var p Project
		var brief string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.URL, &p.Status, &p.CreatedAt, &p.UpdatedAt, &brief, &p.AssetCount); err != nil {
			rows.Close()
			return nil, err
		}
		p.Brief = json.RawMessage(brief)
		out = append(out, p)
	}
	rows.Close()
	for i := range out {
		out[i].Covers = s.covers(ctx, "project_assets", "project_id", out[i].ID)
	}
	return out, rows.Err()
}

// GetProject returns one project.
func (s *Service) GetProject(ctx context.Context, id string) (*Project, error) {
	all, err := s.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("project %q: %w", id, ErrNotFound)
}

// SaveProject creates (id == "") or updates a project.
func (s *Service) SaveProject(ctx context.Context, id string, in GroupInput) (*Project, error) {
	if in.Status != nil && !slices.Contains(ProjectStatuses, *in.Status) {
		return nil, invalid("unknown project status %q (valid: %s)", *in.Status, strings.Join(ProjectStatuses, ", "))
	}
	if in.URL != nil {
		u := strings.TrimSpace(*in.URL)
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return nil, invalid("url must be an http(s) URL")
		}
		in.URL = &u
	}
	ts := now()
	if id == "" {
		name, err := groupName(in.Name)
		if err != nil {
			return nil, err
		}
		id = newID("prj")
		status := "active"
		if in.Status != nil {
			status = *in.Status
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO projects(id, name, description, url, status, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			id, name, strings.TrimSpace(deref(in.Description)), deref(in.URL), status, ts, ts); err != nil {
			return nil, err
		}
		return s.GetProject(ctx, id)
	}
	if _, err := s.GetProject(ctx, id); err != nil {
		return nil, err
	}
	if in.Name != nil {
		name, err := groupName(in.Name)
		if err != nil {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE projects SET name = ? WHERE id = ?`, name, id); err != nil {
			return nil, err
		}
	}
	for col, v := range map[string]*string{"description": in.Description, "url": in.URL, "status": in.Status} {
		if v == nil {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE projects SET `+col+` = ? WHERE id = ?`, strings.TrimSpace(*v), id); err != nil {
			return nil, err
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE projects SET updated_at = ? WHERE id = ?`, ts, id); err != nil {
		return nil, err
	}
	s.syncMembers(ctx, "project_assets", "project_id", id)
	return s.GetProject(ctx, id)
}

// DeleteProject removes a project; its assets are kept.
func (s *Service) DeleteProject(ctx context.Context, id string) error {
	return s.deleteGroup(ctx, "projects", "project_assets", "project_id", id)
}

// ---- shared ----

func groupName(name *string) (string, error) {
	n := strings.TrimSpace(deref(name))
	if n == "" || len(n) > 200 {
		return "", invalid("name must be 1-200 characters")
	}
	return n, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *Service) memberIDs(ctx context.Context, joinTable, col, id string) []string {
	rows, err := s.db.QueryContext(ctx, `SELECT asset_id FROM `+joinTable+` WHERE `+col+` = ?`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var a string
		if rows.Scan(&a) == nil {
			ids = append(ids, a)
		}
	}
	return ids
}

// syncMembers refreshes the sidecars of all assets in a group (its name is
// part of each sidecar).
func (s *Service) syncMembers(ctx context.Context, joinTable, col, id string) {
	for _, a := range s.memberIDs(ctx, joinTable, col, id) {
		s.syncSidecar(ctx, a)
	}
}

func (s *Service) deleteGroup(ctx context.Context, table, joinTable, col, id string) error {
	members := s.memberIDs(ctx, joinTable, col, id)
	r, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return fmt.Errorf("%s %q: %w", strings.TrimSuffix(table, "s"), id, ErrNotFound)
	}
	for _, a := range members {
		s.syncSidecar(ctx, a)
	}
	return nil
}

// ---- tags & stats ----

// TagCount is a tag with the number of (non-trashed) assets carrying it.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// ListTags returns tags by popularity, optionally filtered by prefix/substring.
func (s *Service) ListTags(ctx context.Context, q string, limit int) ([]TagCount, error) {
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.tag, COUNT(*) AS n FROM asset_tags t JOIN assets a ON a.id = t.asset_id
		WHERE a.deleted_at IS NULL AND t.tag LIKE ? ESCAPE '\' GROUP BY t.tag ORDER BY n DESC, t.tag LIMIT ?`,
		"%"+escapeLike(NormalizeTag(q))+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TagCount{}
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Tag, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// RenameTag renames a tag everywhere (merging if the target exists); an empty
// target deletes the tag.
func (s *Service) RenameTag(ctx context.Context, from, to string) (int, error) {
	from, to = NormalizeTag(from), NormalizeTag(to)
	if from == "" {
		return 0, invalid("tag must not be empty")
	}
	var ids []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT asset_id FROM asset_tags WHERE tag = ?`, from)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if to != "" && to != from {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO asset_tags(asset_id, tag) SELECT asset_id, ? FROM asset_tags WHERE tag = ?`, to, from); err != nil {
				return err
			}
		}
		if to != from {
			if _, err := tx.ExecContext(ctx, `DELETE FROM asset_tags WHERE tag = ?`, from); err != nil {
				return err
			}
		}
		for _, id := range ids {
			if err := reindex(ctx, tx, id); err != nil {
				return err
			}
		}
		if len(ids) > 0 && to != from {
			target := to
			if target == "" {
				target = "(deleted)"
			}
			s.record(ctx, tx, "tag.rename", "", "", fmt.Sprintf("%s → %s (%d assets)", from, target, len(ids)))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		s.syncSidecar(ctx, id)
	}
	return len(ids), nil
}

// Stats summarises the library.
type Stats struct {
	Assets      int            `json:"assets"`
	Files       int            `json:"files"`
	TotalSize   int64          `json:"total_size"`
	Trashed     int            `json:"trashed"`
	Untagged    int            `json:"untagged"`
	ByType      map[string]int `json:"by_type"`
	Licenses    []TagCount     `json:"licenses"`
	Tags        int            `json:"tags"`
	Collections int            `json:"collections"`
	Projects    int            `json:"projects"`
}

// GetStats computes library statistics.
func (s *Service) GetStats(ctx context.Context) (*Stats, error) {
	st := &Stats{ByType: map[string]int{}, Licenses: []TagCount{}}
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM assets WHERE deleted_at IS NULL),
		(SELECT COALESCE(SUM(file_count), 0) FROM assets WHERE deleted_at IS NULL),
		(SELECT COALESCE(SUM(total_size), 0) FROM assets),
		(SELECT COUNT(*) FROM assets WHERE deleted_at IS NOT NULL),
		(SELECT COUNT(*) FROM assets a WHERE deleted_at IS NULL AND NOT EXISTS (SELECT 1 FROM asset_tags t WHERE t.asset_id = a.id)),
		(SELECT COUNT(DISTINCT t.tag) FROM asset_tags t JOIN assets a ON a.id = t.asset_id WHERE a.deleted_at IS NULL),
		(SELECT COUNT(*) FROM collections),
		(SELECT COUNT(*) FROM projects)`).
		Scan(&st.Assets, &st.Files, &st.TotalSize, &st.Trashed, &st.Untagged, &st.Tags, &st.Collections, &st.Projects)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT type, COUNT(*) FROM assets WHERE deleted_at IS NULL GROUP BY type`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			rows.Close()
			return nil, err
		}
		st.ByType[t] = n
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT license, COUNT(*) AS n FROM assets WHERE deleted_at IS NULL AND license != ''
		GROUP BY license ORDER BY n DESC, license`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l TagCount
		if err := rows.Scan(&l.Tag, &l.Count); err != nil {
			return nil, err
		}
		st.Licenses = append(st.Licenses, l)
	}
	if errors.Is(rows.Err(), sql.ErrNoRows) {
		return st, nil
	}
	return st, rows.Err()
}

// ProjectBriefJSON returns the stored brief of a project as raw JSON ({} when
// none was saved). The brief's schema is documented with ProjectBrief.
func (s *Service) ProjectBriefJSON(ctx context.Context, projectID string) (json.RawMessage, error) {
	var b string
	err := s.db.QueryRowContext(ctx, `SELECT brief FROM projects WHERE id = ?`, projectID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("project %q: %w", projectID, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}
