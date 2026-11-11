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

// Limits applied to what a sidecar claims.
const (
	maxImportTags  = 200
	maxImportFiles = 2000
	rebuildSetting = "rebuild_in_progress"
)

var (
	// The alphabet of newID (Crockford base32 without i, l, o, u).
	reFileID     = regexp.MustCompile(`^fil_[0-9a-hjkmnp-tv-z]{12}$`)
	reCollection = regexp.MustCompile(`^col_[0-9a-hjkmnp-tv-z]{12}$`)
	reProject    = regexp.MustCompile(`^prj_[0-9a-hjkmnp-tv-z]{12}$`)
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
	resume, _ := s.GetSetting(context.Background(), rebuildSetting)
	if resume == "" {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM assets`).Scan(&n); err != nil || n > 0 {
			return
		}
		entries, err := os.ReadDir(filepath.Join(s.dataDir, "assets"))
		if err != nil || len(entries) == 0 {
			return
		}
		// Remembered, so that a restart in the middle continues the import
		// (which skips what is already there) instead of leaving the rest out.
		if err := s.SetSetting(context.Background(), rebuildSetting, "1"); err != nil {
			s.log.Warn("rebuild: cannot record progress", "err", err)
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.rebuildCancel = cancel
	s.rebuildDone = make(chan struct{})
	s.rebuilding.Store(true)
	go func() {
		defer close(s.rebuildDone)
		defer s.rebuilding.Store(false)
		res := s.RebuildFromSidecars(ctx)
		if ctx.Err() != nil {
			s.log.Info("index rebuild interrupted; it continues at the next start", "assets", res.Assets)
			return
		}
		if err := s.SetSetting(context.Background(), rebuildSetting, ""); err != nil {
			s.log.Warn("rebuild: cannot clear progress", "err", err)
		}
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
		if !ValidAssetID(id) || !isRealDir(filepath.Join(root, id)) {
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

// isRealDir is true for a directory that is not a symbolic link.
func isRealDir(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.IsDir()
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

// validTime normalises a recorded time to UTC RFC 3339. Values that do not
// parse or lie outside 2000-01-01 .. tomorrow become the current time, so a
// forged ancient deleted_at cannot make the trash purge an asset at once.
func validTime(v string) string {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil || t.Year() < 2000 || t.After(time.Now().Add(24*time.Hour)) {
		return now()
	}
	return t.UTC().Format(time.RFC3339)
}

// clip shortens v to at most max bytes without cutting a character.
func clip(v string, max int) string {
	v = strings.TrimSpace(strings.ToValidUTF8(v, ""))
	if len(v) <= max {
		return v
	}
	return strings.ToValidUTF8(v[:max], "")
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
	if !isRealDir(dir) {
		return 0, 0, 0, os.ErrInvalid
	}

	typ := strings.ToLower(sc.Type)
	if !ValidType(typ) {
		typ = "other"
	}
	if !ValidStatus(sc.Status) {
		sc.Status = ""
	}
	for _, v := range []*string{&sc.SupersededBy, &sc.DerivedFrom} {
		if !ValidAssetID(*v) || *v == sc.ID {
			*v = ""
		}
	}
	srcURL, licURL := clip(sc.SourceURL, 2000), clip(sc.LicenseURL, 2000)
	for _, u := range []*string{&srcURL, &licURL} {
		if *u != "" && !strings.HasPrefix(*u, "http://") && !strings.HasPrefix(*u, "https://") {
			*u = ""
		}
	}
	attrs := []byte("{}")
	if sc.Attrs != nil {
		if b, e := json.Marshal(sc.Attrs); e == nil && len(b) <= 64<<10 {
			attrs = b
		}
	}
	gen := []byte("{}")
	if len(sc.Generator) > 0 {
		if b, e := json.Marshal(sc.Generator); e == nil && len(b) <= 32<<10 {
			gen = b
		}
	}
	created := validTime(sc.CreatedAt)
	updated := validTime(sc.UpdatedAt)
	var deleted any
	if sc.DeletedAt != nil && *sc.DeletedAt != "" {
		deleted = validTime(*sc.DeletedAt)
	}
	thumb := 0
	for _, n := range []string{"thumb.png", "thumb.jpg"} {
		if fi, e := os.Lstat(filepath.Join(dir, n)); e == nil && fi.Mode().IsRegular() {
			thumb = 1
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO assets(id, type, type_auto, name, name_auto, description, prompt, source, source_url,
		license, license_url, attribution, attrs, thumb_version, created_at, updated_at, deleted_at, status, superseded_by, derived_from, generator)
		VALUES(?, ?, 0, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sc.ID, typ, clip(sc.Name, 300), clip(sc.Description, 20000), clip(sc.Prompt, 50000), clip(sc.Source, 500), srcURL,
		clip(sc.License, 200), licURL, clip(sc.Attribution, 1000), string(attrs), thumb, created, updated, deleted,
		sc.Status, sc.SupersededBy, sc.DerivedFrom, string(gen)); err != nil {
		return
	}

	tags := NormalizeTags(sc.Tags)
	if len(tags) > maxImportTags {
		tags = tags[:maxImportTags]
	}
	for _, t := range tags {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO asset_tags(asset_id, tag) VALUES(?, ?)`, sc.ID, t); err != nil {
			return
		}
	}

	// Files: only real files of a real files/ directory, with size and hash
	// taken from disk. Nothing is followed through a symbolic link.
	filesDir := filepath.Join(dir, "files")
	idMap := map[string]string{} // id in the sidecar -> id in the index
	seenNames := map[string]bool{}
	var firstKept string
	if isRealDir(filesDir) {
		for i, f := range sc.Files {
			if i >= maxImportFiles {
				break
			}
			name := f.Filename
			if name == "" || name != SanitizeFilename(name) || strings.ContainsAny(name, `/\`) || seenNames[strings.ToLower(name)] {
				s.log.Warn("rebuild: skipping file", "asset", sc.ID, "file", name)
				continue
			}
			size, sum, cfg, hasCfg, ferr := hashRegular(filepath.Join(filesDir, name))
			if ferr != nil || size == 0 {
				s.log.Warn("rebuild: file missing or not a regular file", "asset", sc.ID, "file", name)
				continue
			}
			var w, h int
			if hasCfg {
				w, h = cfg.Width, cfg.Height
			}
			// A file id that another asset already uses gets a fresh one.
			fid := f.ID
			if !reFileID.MatchString(fid) {
				fid = newID("fil")
			}
			inserted := false
			for try := 0; try < 3 && !inserted; try++ {
				var r sql.Result
				if r, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO asset_files(id, asset_id, filename, mime, size, sha256, width, height, created_at)
					VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, fid, sc.ID, name, MimeFor(name), size, sum, w, h, validTime(f.CreatedAt)); err != nil {
					return
				}
				if n, _ := r.RowsAffected(); n > 0 {
					inserted = true
				} else {
					fid = newID("fil")
				}
			}
			if !inserted {
				continue
			}
			seenNames[strings.ToLower(name)] = true
			if f.ID != "" {
				idMap[f.ID] = fid
			}
			if firstKept == "" {
				firstKept = fid
			}
			nFiles++
		}
	}
	primary := idMap[sc.PrimaryFileID]
	if primary == "" {
		primary = firstKept
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
		name := clip(c.Name, 200)
		if name == "" {
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
		name := clip(p.Name, 200)
		if name == "" {
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
			p.ID, sc.ID, clip(p.Note, 1000), ts); err != nil {
			return
		}
	}
	err = reindex(ctx, tx, sc.ID)
	return
}
