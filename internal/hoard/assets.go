package hoard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// File is one stored file belonging to an asset.
type File struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	Mime      string `json:"mime"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Kind      string `json:"kind"` // preview kind: image, audio, video, model, font, text, none
	CreatedAt string `json:"created_at"`
}

// Ref is a lightweight reference to a collection or project.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"` // collections only
	Note string `json:"note,omitempty"` // projects only: how the asset is used
}

// Asset is the central record of the library.
type Asset struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Tags          []string       `json:"tags"`
	Prompt        string         `json:"prompt"`
	Source        string         `json:"source"`
	SourceURL     string         `json:"source_url"`
	License       string         `json:"license"`
	LicenseURL    string         `json:"license_url"`
	Attribution   string         `json:"attribution"`
	Attrs         map[string]any `json:"attrs"`
	Files         []File         `json:"files"`
	PrimaryFileID string         `json:"primary_file_id"`
	PreviewKind   string         `json:"preview_kind"`
	ThumbVersion  int            `json:"thumb_version"` // 0 = no thumbnail
	TotalSize     int64          `json:"total_size"`
	FileCount     int            `json:"file_count"`
	Collections   []Ref          `json:"collections,omitempty"`
	Projects      []Ref          `json:"projects,omitempty"`
	AIStatus      string         `json:"ai_status,omitempty"` // "", queued, running, done, error
	AIError       string         `json:"ai_error,omitempty"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
	DeletedAt     *string        `json:"deleted_at,omitempty"`

	nameAuto, typeAuto bool
}

// NameIsAuto reports whether the name was derived from a filename and never
// set by a person or agent.
func (a *Asset) NameIsAuto() bool { return a.nameAuto }

// Primary returns the primary file, or nil if the asset has no files yet.
func (a *Asset) Primary() *File {
	for i := range a.Files {
		if a.Files[i].ID == a.PrimaryFileID {
			return &a.Files[i]
		}
	}
	if len(a.Files) > 0 {
		return &a.Files[0]
	}
	return nil
}

// AssetInput carries the mutable fields for create and update. Nil pointers
// mean "leave unchanged".
type AssetInput struct {
	Type        *string        `json:"type,omitempty"`
	Name        *string        `json:"name,omitempty"`
	Description *string        `json:"description,omitempty"`
	Prompt      *string        `json:"prompt,omitempty"`
	Source      *string        `json:"source,omitempty"`
	SourceURL   *string        `json:"source_url,omitempty"`
	License     *string        `json:"license,omitempty"`
	LicenseURL  *string        `json:"license_url,omitempty"`
	Attribution *string        `json:"attribution,omitempty"`
	Tags        *[]string      `json:"tags,omitempty"`        // replaces all tags
	AddTags     []string       `json:"add_tags,omitempty"`    // merged in
	RemoveTags  []string       `json:"remove_tags,omitempty"` // removed
	Attrs       map[string]any `json:"attrs,omitempty"`       // merged; null deletes a key

	AddCollections    []string `json:"add_collections,omitempty"`
	RemoveCollections []string `json:"remove_collections,omitempty"`
	AddProjects       []string `json:"add_projects,omitempty"`
	RemoveProjects    []string `json:"remove_projects,omitempty"`
	ProjectNote       string   `json:"project_note,omitempty"` // usage note for add_projects
}

var reAssetID = regexp.MustCompile(`^ast_[0-9a-z]{12}$`)

// ValidAssetID reports whether id is syntactically an asset id.
func ValidAssetID(id string) bool { return reAssetID.MatchString(id) }

const assetCols = `a.id, a.type, a.type_auto, a.name, a.name_auto, a.description, a.prompt, a.source, a.source_url,
	a.license, a.license_url, a.attribution, a.attrs, a.primary_file_id, a.thumb_version, a.total_size,
	a.file_count, a.ai_status, a.ai_error, a.created_at, a.updated_at, a.deleted_at`

type scanner interface{ Scan(dest ...any) error }

func scanAsset(row scanner) (*Asset, error) {
	var a Asset
	var attrs string
	var deleted sql.NullString
	err := row.Scan(&a.ID, &a.Type, &a.typeAuto, &a.Name, &a.nameAuto, &a.Description, &a.Prompt, &a.Source, &a.SourceURL,
		&a.License, &a.LicenseURL, &a.Attribution, &attrs, &a.PrimaryFileID, &a.ThumbVersion, &a.TotalSize,
		&a.FileCount, &a.AIStatus, &a.AIError, &a.CreatedAt, &a.UpdatedAt, &deleted)
	if err != nil {
		return nil, err
	}
	if deleted.Valid {
		a.DeletedAt = &deleted.String
	}
	a.Attrs = map[string]any{}
	_ = json.Unmarshal([]byte(attrs), &a.Attrs)
	a.Tags = []string{}
	a.Files = []File{}
	a.PreviewKind = KindNone
	return &a, nil
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// hydrate attaches tags and files to the given assets.
func hydrate(ctx context.Context, q queryer, assets []*Asset) error {
	if len(assets) == 0 {
		return nil
	}
	byID := make(map[string]*Asset, len(assets))
	ids := make([]string, len(assets))
	for i, a := range assets {
		byID[a.ID] = a
		ids[i] = a.ID
	}
	in := placeholders(len(ids))

	rows, err := q.QueryContext(ctx, `SELECT asset_id, tag FROM asset_tags WHERE asset_id IN (`+in+`) ORDER BY tag`, anySlice(ids)...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, tag string
		if err := rows.Scan(&id, &tag); err != nil {
			rows.Close()
			return err
		}
		byID[id].Tags = append(byID[id].Tags, tag)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = q.QueryContext(ctx, `SELECT asset_id, id, filename, mime, size, sha256, width, height, created_at
		FROM asset_files WHERE asset_id IN (`+in+`) ORDER BY created_at, filename`, anySlice(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var f File
		if err := rows.Scan(&id, &f.ID, &f.Filename, &f.Mime, &f.Size, &f.SHA256, &f.Width, &f.Height, &f.CreatedAt); err != nil {
			return err
		}
		f.Kind = PreviewKind(f.Filename)
		byID[id].Files = append(byID[id].Files, f)
	}
	for _, a := range assets {
		if p := a.Primary(); p != nil {
			a.PreviewKind = p.Kind
		}
	}
	return rows.Err()
}

// GetAsset returns one asset including its collections and projects.
func (s *Service) GetAsset(ctx context.Context, id string) (*Asset, error) {
	return getAsset(ctx, s.db, id)
}

func getAsset(ctx context.Context, q queryer, id string) (*Asset, error) {
	a, err := scanAsset(q.QueryRowContext(ctx, `SELECT `+assetCols+` FROM assets a WHERE a.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("asset %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if err := hydrate(ctx, q, []*Asset{a}); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT c.id, c.name, c.kind FROM collection_assets ca
		JOIN collections c ON c.id = ca.collection_id WHERE ca.asset_id = ? ORDER BY c.name COLLATE NOCASE`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind); err != nil {
			rows.Close()
			return nil, err
		}
		a.Collections = append(a.Collections, r)
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT p.id, p.name, pa.note FROM project_assets pa
		JOIN projects p ON p.id = pa.project_id WHERE pa.asset_id = ? ORDER BY p.name COLLATE NOCASE`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.ID, &r.Name, &r.Note); err != nil {
			return nil, err
		}
		a.Projects = append(a.Projects, r)
	}
	return a, rows.Err()
}

// ---- listing & search ----

// ListQuery filters, sorts and pages the library.
type ListQuery struct {
	Q          string   // full-text query over name, tags, description, prompt, filenames…
	Types      []string // any of
	Tags       []string // all of
	Collection string   // collection id
	Project    string   // project id, or "none" for assets not used in any project
	License    string   // exact license, or "none" for assets without one
	Untagged   bool
	Trashed    bool
	IDs        []string // restrict to these ids
	Sort       string   // relevance | created | updated | name | size | type
	Desc       *bool    // nil = natural direction for the sort key
	Limit      int
	Offset     int
}

// ListResult is one page of assets.
type ListResult struct {
	Items  []*Asset `json:"items"`
	Total  int      `json:"total"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
}

var reFTSStrip = regexp.MustCompile(`["*():^{}\[\]\x00-\x1f]+`)

// ftsQuery turns free text into a safe FTS5 expression: every term must match
// as a prefix.
func ftsQuery(q string) string {
	var parts []string
	for _, tok := range strings.Fields(reFTSStrip.ReplaceAllString(q, " ")) {
		parts = append(parts, `"`+tok+`"*`)
	}
	return strings.Join(parts, " ")
}

// ListAssets returns a filtered page of assets (without collections/projects).
func (s *Service) ListAssets(ctx context.Context, q ListQuery) (*ListResult, error) {
	if q.Limit <= 0 {
		q.Limit = 60
	}
	if q.Limit > 500 {
		q.Limit = 500
	}
	if q.Offset < 0 {
		q.Offset = 0
	}

	from := `FROM assets a`
	var where []string
	var args []any

	fts := ftsQuery(q.Q)
	if fts != "" {
		from += ` JOIN assets_fts ON assets_fts.rowid = a.seq`
		where = append(where, `assets_fts MATCH ?`)
		args = append(args, fts)
	}
	if q.Trashed {
		where = append(where, `a.deleted_at IS NOT NULL`)
	} else {
		where = append(where, `a.deleted_at IS NULL`)
	}
	if len(q.Types) > 0 {
		where = append(where, `a.type IN (`+placeholders(len(q.Types))+`)`)
		args = append(args, anySlice(q.Types)...)
	}
	for _, t := range NormalizeTags(q.Tags) {
		where = append(where, `EXISTS (SELECT 1 FROM asset_tags t WHERE t.asset_id = a.id AND t.tag = ?)`)
		args = append(args, t)
	}
	if q.Untagged {
		where = append(where, `NOT EXISTS (SELECT 1 FROM asset_tags t WHERE t.asset_id = a.id)`)
	}
	if q.Collection != "" {
		where = append(where, `EXISTS (SELECT 1 FROM collection_assets c WHERE c.asset_id = a.id AND c.collection_id = ?)`)
		args = append(args, q.Collection)
	}
	switch q.Project {
	case "":
	case "none":
		where = append(where, `NOT EXISTS (SELECT 1 FROM project_assets p WHERE p.asset_id = a.id)`)
	default:
		where = append(where, `EXISTS (SELECT 1 FROM project_assets p WHERE p.asset_id = a.id AND p.project_id = ?)`)
		args = append(args, q.Project)
	}
	switch q.License {
	case "":
	case "none":
		where = append(where, `a.license = ''`)
	default:
		where = append(where, `a.license = ?`)
		args = append(args, q.License)
	}
	if len(q.IDs) > 0 {
		where = append(where, `a.id IN (`+placeholders(len(q.IDs))+`)`)
		args = append(args, anySlice(q.IDs)...)
	}
	cond := ` WHERE ` + strings.Join(where, ` AND `)

	sortKey := q.Sort
	if sortKey == "" || (sortKey == "relevance" && fts == "") {
		if fts != "" {
			sortKey = "relevance"
		} else {
			sortKey = "created"
		}
	}
	dir := func(natural string) string {
		if q.Desc == nil {
			return natural
		}
		if *q.Desc {
			return "DESC"
		}
		return "ASC"
	}
	var order string
	switch sortKey {
	case "relevance":
		// bm25 is smaller-is-better; weight name and tags over long text.
		order = `bm25(assets_fts, 10.0, 6.0, 3.0, 1.0, 1.0), a.created_at DESC`
	case "updated":
		order = `a.updated_at ` + dir("DESC") + `, a.id`
	case "name":
		order = `a.name COLLATE NOCASE ` + dir("ASC") + `, a.id`
	case "size":
		order = `a.total_size ` + dir("DESC") + `, a.id`
	case "type":
		order = `a.type ` + dir("ASC") + `, a.name COLLATE NOCASE, a.id`
	case "created":
		order = `a.created_at ` + dir("DESC") + `, a.id`
	default:
		return nil, invalid("unknown sort %q (use relevance, created, updated, name, size or type)", q.Sort)
	}

	res := &ListResult{Items: []*Asset{}, Limit: q.Limit, Offset: q.Offset}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) `+from+cond, args...).Scan(&res.Total); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+assetCols+` `+from+cond+` ORDER BY `+order+` LIMIT ? OFFSET ?`,
		append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		res.Items = append(res.Items, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, hydrate(ctx, s.db, res.Items)
}

// ---- mutations ----

// CreateAsset creates a new asset record. Files are added afterwards.
func (s *Service) CreateAsset(ctx context.Context, in AssetInput) (*Asset, error) {
	id := newID("ast")
	ts := now()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// Until a name/type is given they are derived from the first file.
		if _, err := tx.ExecContext(ctx, `INSERT INTO assets(id, type_auto, name_auto, created_at, updated_at) VALUES(?, 1, 1, ?, ?)`,
			id, ts, ts); err != nil {
			return err
		}
		if err := applyPatch(ctx, tx, id, in); err != nil {
			return err
		}
		return reindex(ctx, tx, id)
	})
	if err != nil {
		return nil, err
	}
	s.syncSidecar(ctx, id)
	return s.GetAsset(ctx, id)
}

// UpdateAsset applies a partial update.
func (s *Service) UpdateAsset(ctx context.Context, id string, in AssetInput) (*Asset, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := assetExists(ctx, tx, id); err != nil {
			return err
		}
		if err := applyPatch(ctx, tx, id, in); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET updated_at = ? WHERE id = ?`, now(), id); err != nil {
			return err
		}
		return reindex(ctx, tx, id)
	})
	if err != nil {
		return nil, err
	}
	s.syncSidecar(ctx, id)
	return s.GetAsset(ctx, id)
}

func assetExists(ctx context.Context, tx *sql.Tx, id string) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM assets WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("asset %q: %w", id, ErrNotFound)
	}
	return err
}

func limitLen(field, v string, max int) (string, error) {
	v = strings.TrimSpace(v)
	if len(v) > max {
		return "", invalid("%s is too long (max %d characters)", field, max)
	}
	return v, nil
}

// applyPatch writes the given fields of one asset inside a transaction.
func applyPatch(ctx context.Context, tx *sql.Tx, id string, in AssetInput) error {
	var sets []string
	var args []any
	set := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if in.Type != nil && *in.Type != "" {
		t := strings.ToLower(strings.TrimSpace(*in.Type))
		if !ValidType(t) {
			return invalid("unknown asset type %q (valid: %s)", *in.Type, strings.Join(TypeIDs(), ", "))
		}
		set("type", t)
		set("type_auto", 0)
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		v, err := limitLen("name", *in.Name, 300)
		if err != nil {
			return err
		}
		set("name", v)
		set("name_auto", 0)
	}
	for _, f := range []struct {
		col string
		val *string
		max int
	}{
		{"description", in.Description, 20000},
		{"prompt", in.Prompt, 50000},
		{"source", in.Source, 500},
		{"source_url", in.SourceURL, 2000},
		{"license", in.License, 200},
		{"license_url", in.LicenseURL, 2000},
		{"attribution", in.Attribution, 1000},
	} {
		if f.val == nil {
			continue
		}
		v, err := limitLen(f.col, *f.val, f.max)
		if err != nil {
			return err
		}
		if strings.HasSuffix(f.col, "_url") && v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			return invalid("%s must be an http(s) URL", f.col)
		}
		set(f.col, v)
	}
	if len(in.Attrs) > 0 {
		var cur string
		if err := tx.QueryRowContext(ctx, `SELECT attrs FROM assets WHERE id = ?`, id).Scan(&cur); err != nil {
			return err
		}
		m := map[string]any{}
		_ = json.Unmarshal([]byte(cur), &m)
		for k, v := range in.Attrs {
			if v == nil {
				delete(m, k)
			} else {
				m[k] = v
			}
		}
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if len(b) > 64<<10 {
			return invalid("attrs is too large (max 64 KiB)")
		}
		set("attrs", string(b))
	}
	if len(sets) > 0 {
		args = append(args, id)
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
			return err
		}
	}

	if in.Tags != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM asset_tags WHERE asset_id = ?`, id); err != nil {
			return err
		}
	}
	add := in.AddTags
	if in.Tags != nil {
		add = append(append([]string{}, *in.Tags...), add...)
	}
	for _, t := range NormalizeTags(add) {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO asset_tags(asset_id, tag) VALUES(?, ?)`, id, t); err != nil {
			return err
		}
	}
	for _, t := range NormalizeTags(in.RemoveTags) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM asset_tags WHERE asset_id = ? AND tag = ?`, id, t); err != nil {
			return err
		}
	}

	ts := now()
	for _, c := range in.AddCollections {
		if err := mustExist(ctx, tx, "collections", c); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collection_assets(collection_id, asset_id, added_at) VALUES(?, ?, ?)`, c, id, ts); err != nil {
			return err
		}
	}
	for _, c := range in.RemoveCollections {
		if _, err := tx.ExecContext(ctx, `DELETE FROM collection_assets WHERE collection_id = ? AND asset_id = ?`, c, id); err != nil {
			return err
		}
	}
	for _, p := range in.AddProjects {
		if err := mustExist(ctx, tx, "projects", p); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_assets(project_id, asset_id, note, added_at) VALUES(?, ?, ?, ?)
			ON CONFLICT(project_id, asset_id) DO UPDATE SET note = CASE WHEN excluded.note = '' THEN note ELSE excluded.note END`,
			p, id, strings.TrimSpace(in.ProjectNote), ts); err != nil {
			return err
		}
	}
	for _, p := range in.RemoveProjects {
		if _, err := tx.ExecContext(ctx, `DELETE FROM project_assets WHERE project_id = ? AND asset_id = ?`, p, id); err != nil {
			return err
		}
	}
	return nil
}

func mustExist(ctx context.Context, tx *sql.Tx, table, id string) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s %q: %w", strings.TrimSuffix(table, "s"), id, ErrNotFound)
	}
	return err
}

// reindex refreshes the full-text row of one asset.
func reindex(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM assets_fts WHERE rowid = (SELECT seq FROM assets WHERE id = ?)`, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO assets_fts(rowid, name, tags, description, prompt, extra)
		SELECT a.seq, a.name,
			COALESCE((SELECT group_concat(tag, ' ') FROM asset_tags WHERE asset_id = a.id), ''),
			a.description, a.prompt,
			a.id || ' ' || a.type || ' ' || a.source || ' ' || a.license || ' ' || a.attribution || ' ' ||
			COALESCE((SELECT group_concat(filename, ' ') FROM asset_files WHERE asset_id = a.id), '')
		FROM assets a WHERE a.id = ?`, id)
	return err
}

// ---- bulk ----

// BulkOp applies one change set to many assets at once.
type BulkOp struct {
	IDs    []string   `json:"ids"`
	Action string     `json:"action,omitempty"` // "" (update) | trash | restore | purge
	Patch  AssetInput `json:"patch"`
}

// Bulk applies op to every listed asset atomically and returns the number of
// assets affected.
func (s *Service) Bulk(ctx context.Context, op BulkOp) (int, error) {
	if len(op.IDs) == 0 {
		return 0, invalid("ids must not be empty")
	}
	if len(op.IDs) > 5000 {
		return 0, invalid("too many ids (max 5000 per call)")
	}
	if op.Patch.Name != nil {
		return 0, invalid("name cannot be set in bulk")
	}
	ts := now()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, id := range op.IDs {
			if err := assetExists(ctx, tx, id); err != nil {
				return err
			}
			var err error
			switch op.Action {
			case "", "update":
				if err = applyPatch(ctx, tx, id, op.Patch); err == nil {
					if _, err = tx.ExecContext(ctx, `UPDATE assets SET updated_at = ? WHERE id = ?`, ts, id); err == nil {
						err = reindex(ctx, tx, id)
					}
				}
			case "trash":
				_, err = tx.ExecContext(ctx, `UPDATE assets SET deleted_at = COALESCE(deleted_at, ?) WHERE id = ?`, ts, id)
			case "restore":
				_, err = tx.ExecContext(ctx, `UPDATE assets SET deleted_at = NULL WHERE id = ?`, id)
			case "purge":
				if _, err = tx.ExecContext(ctx, `DELETE FROM assets_fts WHERE rowid = (SELECT seq FROM assets WHERE id = ?)`, id); err == nil {
					_, err = tx.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, id)
				}
			default:
				return invalid("unknown action %q (use update, trash, restore or purge)", op.Action)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, id := range op.IDs {
		if op.Action == "purge" {
			if ValidAssetID(id) {
				if err := os.RemoveAll(s.assetDir(id)); err != nil {
					s.log.Warn("remove asset dir", "asset", id, "err", err)
				}
			}
		} else {
			s.syncSidecar(ctx, id)
		}
	}
	return len(op.IDs), nil
}

// PurgeExpiredTrash permanently deletes assets trashed before cutoff.
func (s *Service) PurgeExpiredTrash(ctx context.Context, cutoff string) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM assets WHERE deleted_at IS NOT NULL AND deleted_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// In batches: Bulk caps the number of ids per call.
	total := 0
	for len(ids) > 0 {
		n := min(len(ids), 1000)
		done, err := s.Bulk(ctx, BulkOp{IDs: ids[:n], Action: "purge"})
		total += done
		if err != nil {
			return total, err
		}
		ids = ids[n:]
	}
	return total, nil
}

// SetAIStatus records the state of the AI agent for an asset.
func (s *Service) SetAIStatus(ctx context.Context, id, status, errMsg string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE assets SET ai_status = ?, ai_error = ? WHERE id = ?`, status, errMsg, id); err != nil {
		return err
	}
	s.syncSidecar(ctx, id)
	return nil
}

// PendingAI returns ids of assets whose AI analysis was queued or interrupted.
func (s *Service) PendingAI(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM assets WHERE ai_status IN ('queued', 'running') AND deleted_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---- sidecar ----

func (s *Service) assetDir(id string) string { return filepath.Join(s.dataDir, "assets", id) }

// syncSidecar rewrites assets/<id>/asset.json so that the library on disk is
// self-describing even without the database. It runs after every mutation,
// also when the request that caused it has already gone away, and writes for
// one asset are serialised so that the newest state always wins.
func (s *Service) syncSidecar(ctx context.Context, id string) {
	if !ValidAssetID(id) {
		return
	}
	ctx = context.WithoutCancel(ctx)
	mu := s.assetLock(id)
	mu.Lock()
	defer mu.Unlock()

	a, err := s.GetAsset(ctx, id)
	if err != nil {
		return
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return
	}
	dir := s.assetDir(id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		s.log.Warn("sidecar", "asset", id, "err", err)
		return
	}
	if err := writeFileAtomic(filepath.Join(dir, "asset.json"), append(b, '\n')); err != nil {
		s.log.Warn("sidecar", "asset", id, "err", err)
	}
}

// writeFileAtomic replaces path with data through a uniquely named temporary
// file in the same directory, so readers never see a partial file and
// concurrent writers never share a temporary name.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o640)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// assetLock returns one of a fixed set of mutexes for an asset id.
func (s *Service) assetLock(id string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(id))
	return &s.assetMu[h.Sum32()%uint32(len(s.assetMu))]
}

// TypeIsAuto reports whether the type is still the automatic guess.
func (a *Asset) TypeIsAuto() bool { return a.typeAuto }
