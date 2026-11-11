package hoard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"image"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// maxSidecarBytes bounds what the import reads from one asset.json.
const maxSidecarBytes = 8 << 20

var (
	reFileID     = regexp.MustCompile(`^fil_[0-9a-z]{12}$`)
	reCollection = regexp.MustCompile(`^col_[0-9a-z]{12}$`)
	reProject    = regexp.MustCompile(`^prj_[0-9a-z]{12}$`)
)

// RebuildResult summarises an import of the asset sidecars.
type RebuildResult struct {
	Assets      int // asset rows recreated
	Files       int // file rows recreated
	Collections int // collections created because they were missing
	Projects    int // projects created because they were missing
	Skipped     int // sidecars that could not be imported
}

// startRebuild imports the sidecars in the background when the index holds no
// assets at all but the library directory has some.
func (s *Service) startRebuild() {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM assets`).Scan(&n); err != nil || n > 0 {
		return
	}
	entries, err := os.ReadDir(filepath.Join(s.dataDir, "assets"))
	if err != nil || len(entries) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.rebuildCancel = cancel
	s.rebuildDone = make(chan struct{})
	go func() {
		defer close(s.rebuildDone)
		res := s.RebuildFromSidecars(ctx)
		s.log.Info("index rebuilt from asset sidecars",
			"assets", res.Assets, "files", res.Files, "collections", res.Collections,
			"projects", res.Projects, "skipped", res.Skipped)
	}()
}

// WaitRebuild blocks until a startup import of the sidecars has finished. It
// returns at once when none is running.
func (s *Service) WaitRebuild() {
	if s.rebuildDone != nil {
		<-s.rebuildDone
	}
}

// sidecar is the part of asset.json the import reads. File contents on disk
// are authoritative: sizes and hashes in the sidecar are never trusted.
type sidecar struct {
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
	Collections   []Ref          `json:"collections"`
	Projects      []Ref          `json:"projects"`
	Status        string         `json:"status"`
	SupersededBy  string         `json:"superseded_by"`
	DerivedFrom   string         `json:"derived_from"`
	Generator     map[string]any `json:"generator"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
	DeletedAt     *string        `json:"deleted_at"`
}

// RebuildFromSidecars recreates the index rows of every assets/<id>/asset.json
// whose id is not in the index yet. Users, API tokens and settings are not
// stored in sidecars and are not restored. Directories and files that are
// symbolic links are skipped, so the import never reads outside the data dir.
func (s *Service) RebuildFromSidecars(ctx context.Context) RebuildResult {
	var res RebuildResult
	root := filepath.Join(s.dataDir, "assets")
	entries, err := os.ReadDir(root)
	if err != nil {
		s.log.Warn("rebuild: read assets directory", "err", err)
		return res
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return res
		}
		id := e.Name()
		if !ValidAssetID(id) || !e.IsDir() { // IsDir is false for symlinks
			s.log.Warn("rebuild: skipping entry", "name", e.Name())
			res.Skipped++
			continue
		}
		sc, err := readSidecar(filepath.Join(root, id), id)
		if err != nil {
			s.log.Warn("rebuild: skipping asset", "asset", id, "err", err)
			res.Skipped++
			continue
		}
		var nf, nc, np int
		err = s.tx(ctx, func(tx *sql.Tx) error {
			var e error
			nf, nc, np, e = s.importSidecar(ctx, tx, sc)
			return e
		})
		if err != nil {
			s.log.Warn("rebuild: skipping asset", "asset", id, "err", err)
			res.Skipped++
			continue
		}
		res.Assets++
		res.Files += nf
		res.Collections += nc
		res.Projects += np
	}
	return res
}

func readSidecar(dir, id string) (*sidecar, error) {
	path := filepath.Join(dir, "asset.json")
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, maxSidecarBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSidecarBytes {
		return nil, os.ErrInvalid
	}
	var sc sidecar
	if err := json.Unmarshal(b, &sc); err != nil {
		return nil, err
	}
	if sc.ID != id {
		return nil, os.ErrInvalid
	}
	return &sc, nil
}

func validTime(v string) string {
	if _, err := time.Parse(time.RFC3339, v); err != nil {
		return now()
	}
	return v
}

// hashRegular hashes a regular file (never a symlink) and, for images, reads
// the dimensions.
func hashRegular(path string) (size int64, sum string, cfg image.Config, hasCfg bool, err error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, "", cfg, false, err
	}
	if !fi.Mode().IsRegular() {
		return 0, "", cfg, false, os.ErrInvalid
	}
	fh, err := os.Open(path)
	if err != nil {
		return 0, "", cfg, false, err
	}
	defer fh.Close()
	h := sha256.New()
	size, err = io.Copy(h, fh)
	if err != nil {
		return 0, "", cfg, false, err
	}
	sum = hex.EncodeToString(h.Sum(nil))
	if decodableImage[ext(path)] {
		if _, err := fh.Seek(0, io.SeekStart); err == nil {
			if c, _, err := image.DecodeConfig(fh); err == nil {
				cfg, hasCfg = c, true
			}
		}
	}
	return size, sum, cfg, hasCfg, nil
}

func (s *Service) importSidecar(ctx context.Context, tx *sql.Tx, sc *sidecar) (nFiles, nCols, nProjs int, err error) {
	var exists int
	if e := tx.QueryRowContext(ctx, `SELECT 1 FROM assets WHERE id = ?`, sc.ID).Scan(&exists); e == nil {
		return 0, 0, 0, nil // already in the index
	}
	dir := s.assetDir(sc.ID)

	typ := strings.ToLower(sc.Type)
	if !ValidType(typ) {
		typ = "other"
	}
	if !ValidStatus(sc.Status) {
		sc.Status = ""
	}
	for _, v := range []*string{&sc.SupersededBy, &sc.DerivedFrom} {
		if !ValidAssetID(*v) {
			*v = ""
		}
	}
	attrs := []byte("{}")
	if sc.Attrs != nil {
		attrs, _ = json.Marshal(sc.Attrs)
	}
	gen := []byte("{}")
	if len(sc.Generator) > 0 {
		gen, _ = json.Marshal(sc.Generator)
	}
	created := validTime(sc.CreatedAt)
	updated := validTime(sc.UpdatedAt)
	var deleted any
	if sc.DeletedAt != nil && *sc.DeletedAt != "" {
		deleted = validTime(*sc.DeletedAt)
	}
	thumb := 0
	if p, _ := s.ThumbPath(sc.ID); p != "" {
		thumb = 1
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO assets(id, type, type_auto, name, name_auto, description, prompt, source, source_url,
		license, license_url, attribution, attrs, thumb_version, created_at, updated_at, deleted_at, status, superseded_by, derived_from, generator)
		VALUES(?, ?, 0, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sc.ID, typ, strings.TrimSpace(sc.Name), sc.Description, sc.Prompt, sc.Source, sc.SourceURL,
		sc.License, sc.LicenseURL, sc.Attribution, string(attrs), thumb, created, updated, deleted,
		sc.Status, sc.SupersededBy, sc.DerivedFrom, string(gen)); err != nil {
		return
	}

	for _, t := range NormalizeTags(sc.Tags) {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO asset_tags(asset_id, tag) VALUES(?, ?)`, sc.ID, t); err != nil {
			return
		}
	}

	// Files: only those that exist on disk, with size and hash taken from disk.
	seenNames := map[string]bool{}
	var kept []string
	for _, f := range sc.Files {
		name := f.Filename
		if name == "" || name != SanitizeFilename(name) || strings.ContainsAny(name, `/\`) ||
			seenNames[strings.ToLower(name)] {
			s.log.Warn("rebuild: skipping file", "asset", sc.ID, "file", name)
			continue
		}
		size, sum, cfg, hasCfg, ferr := hashRegular(filepath.Join(dir, "files", name))
		if ferr != nil || size == 0 {
			s.log.Warn("rebuild: file missing or unreadable", "asset", sc.ID, "file", name)
			continue
		}
		seenNames[strings.ToLower(name)] = true
		fid := f.ID
		if !reFileID.MatchString(fid) {
			fid = newID("fil")
		}
		var w, h int
		if hasCfg {
			w, h = cfg.Width, cfg.Height
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO asset_files(id, asset_id, filename, mime, size, sha256, width, height, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, fid, sc.ID, name, MimeFor(name), size, sum, w, h, validTime(f.CreatedAt)); err != nil {
			return
		}
		kept = append(kept, fid)
		nFiles++
	}
	primary := sc.PrimaryFileID
	if !slices.Contains(kept, primary) {
		primary = ""
		if len(kept) > 0 {
			if err = tx.QueryRowContext(ctx, `SELECT id FROM asset_files WHERE asset_id = ? ORDER BY created_at, filename LIMIT 1`, sc.ID).Scan(&primary); err != nil {
				return
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE assets SET primary_file_id = ? WHERE id = ?`, primary, sc.ID); err != nil {
		return
	}
	if err = refreshAggregates(ctx, tx, sc.ID); err != nil {
		return
	}
	// refreshAggregates stamps updated_at; keep the recorded time.
	if _, err = tx.ExecContext(ctx, `UPDATE assets SET updated_at = ? WHERE id = ?`, updated, sc.ID); err != nil {
		return
	}

	ts := now()
	for _, c := range sc.Collections {
		if !reCollection.MatchString(c.ID) {
			continue
		}
		name := strings.TrimSpace(c.Name)
		if name == "" || len(name) > 200 {
			name = c.ID
		}
		kind := c.Kind
		if !slices.Contains(CollectionKinds, kind) {
			kind = "pack"
		}
		var r sql.Result
		if r, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO collections(id, name, description, kind, created_at, updated_at) VALUES(?, ?, '', ?, ?, ?)`,
			c.ID, name, kind, ts, ts); err != nil {
			return
		}
		if n, _ := r.RowsAffected(); n > 0 {
			nCols++
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO collection_assets(collection_id, asset_id, added_at) VALUES(?, ?, ?)`, c.ID, sc.ID, ts); err != nil {
			return
		}
	}
	for _, p := range sc.Projects {
		if !reProject.MatchString(p.ID) {
			continue
		}
		name := strings.TrimSpace(p.Name)
		if name == "" || len(name) > 200 {
			name = p.ID
		}
		var r sql.Result
		if r, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO projects(id, name, created_at, updated_at) VALUES(?, ?, ?, ?)`,
			p.ID, name, ts, ts); err != nil {
			return
		}
		if n, _ := r.RowsAffected(); n > 0 {
			nProjs++
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_assets(project_id, asset_id, note, added_at) VALUES(?, ?, ?, ?)`,
			p.ID, sc.ID, p.Note, ts); err != nil {
			return
		}
	}
	err = reindex(ctx, tx, sc.ID)
	return
}
