// Package hoard is the core of the application: the data model, the SQLite
// index and the on-disk asset library. The REST API, the MCP server and the AI
// agent are all thin layers over the Service defined here.
package hoard

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Common errors, mapped to HTTP statuses / MCP errors by the outer layers.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// InvalidError reports bad input from a client.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func invalid(format string, a ...any) error {
	return &InvalidError{Msg: fmt.Sprintf(format, a...)}
}

// Service owns the database and the asset library on disk.
//
// On-disk layout below DataDir:
//
//	hoard.db                   SQLite index (WAL mode)
//	assets/<asset-id>/
//	    asset.json             metadata sidecar, always in sync with the DB
//	    thumb.png              preview thumbnail (optional)
//	    files/<filename>       the asset's original files, untouched
//	backups/hoard-<date>.db    daily consistent DB snapshots
//	tmp/                       in-flight uploads
type Service struct {
	db      *sql.DB
	dataDir string
	log     *slog.Logger
	secret  []byte // HMAC key for signed URLs

	thumbSem chan struct{}  // one image is decoded at a time
	assetMu  [64]sync.Mutex // serialises sidecar writes per asset (striped)
	mutateMu [64]sync.Mutex // serialises mutations of one asset (striped); always taken before assetMu
	tokenMu  sync.Mutex
	tokenUse map[string]time.Time // throttles last_used_at writes

	rebuildCancel context.CancelFunc // set while a startup import of the sidecars runs
	rebuildDone   chan struct{}
	rebuilding    atomic.Bool
}

// Open opens (and if necessary creates) the library in dataDir.
func Open(dataDir string, log *slog.Logger) (*Service, error) {
	for _, d := range []string{"assets", "tmp", "backups"} {
		if err := os.MkdirAll(filepath.Join(dataDir, d), 0o750); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	// Leftovers from uploads interrupted by a restart.
	if entries, err := os.ReadDir(filepath.Join(dataDir, "tmp")); err == nil {
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(dataDir, "tmp", e.Name()))
		}
	}

	dsn := "file:" + filepath.Join(dataDir, "hoard.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)

	s := &Service{
		db:       db,
		dataDir:  dataDir,
		log:      log,
		thumbSem: make(chan struct{}, 1),
		tokenUse: map[string]time.Time{},
	}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if s.secret, err = s.loadSecret(); err != nil {
		db.Close()
		return nil, err
	}
	s.protectDB()
	s.startRebuild()
	return s, nil
}

// protectDB makes the index readable by its owner only: it holds password
// hashes, the link-signing key and possibly an AI provider key. Asset files
// stay group-readable so the library can be browsed.
func (s *Service) protectDB() {
	for _, name := range []string{"hoard.db", "hoard.db-wal", "hoard.db-shm"} {
		if err := os.Chmod(filepath.Join(s.dataDir, name), 0o600); err != nil && !os.IsNotExist(err) {
			s.log.Warn("restrict database permissions", "file", name, "err", err)
		}
	}
}

// Close releases the database.
func (s *Service) Close() error {
	if s.rebuildDone != nil {
		s.rebuildCancel()
		<-s.rebuildDone
	}
	return s.db.Close()
}

// DataDir returns the library root.
func (s *Service) DataDir() string { return s.dataDir }

// migrations are applied in order; the index is the schema version.
var migrations = []string{
	`
CREATE TABLE users (
	id            TEXT PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	created_at    TEXT NOT NULL
);
CREATE TABLE sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
CREATE TABLE api_tokens (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name         TEXT NOT NULL,
	token_hash   TEXT NOT NULL UNIQUE,
	prefix       TEXT NOT NULL,
	created_at   TEXT NOT NULL,
	last_used_at TEXT
);
CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE assets (
	seq             INTEGER PRIMARY KEY, -- stable rowid, shared with assets_fts
	id              TEXT NOT NULL UNIQUE,
	type            TEXT NOT NULL DEFAULT 'other',
	type_auto       INTEGER NOT NULL DEFAULT 0,
	name            TEXT NOT NULL DEFAULT '',
	name_auto       INTEGER NOT NULL DEFAULT 0,
	description     TEXT NOT NULL DEFAULT '',
	prompt          TEXT NOT NULL DEFAULT '',
	source          TEXT NOT NULL DEFAULT '',
	source_url      TEXT NOT NULL DEFAULT '',
	license         TEXT NOT NULL DEFAULT '',
	license_url     TEXT NOT NULL DEFAULT '',
	attribution     TEXT NOT NULL DEFAULT '',
	attrs           TEXT NOT NULL DEFAULT '{}',
	primary_file_id TEXT NOT NULL DEFAULT '',
	thumb_version   INTEGER NOT NULL DEFAULT 0,
	total_size      INTEGER NOT NULL DEFAULT 0,
	file_count      INTEGER NOT NULL DEFAULT 0,
	ai_status       TEXT NOT NULL DEFAULT '',
	ai_error        TEXT NOT NULL DEFAULT '',
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL,
	deleted_at      TEXT
);
CREATE INDEX assets_created ON assets(deleted_at, created_at);
CREATE INDEX assets_type ON assets(deleted_at, type);
CREATE TABLE asset_files (
	id         TEXT PRIMARY KEY,
	asset_id   TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
	filename   TEXT NOT NULL,
	mime       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	sha256     TEXT NOT NULL,
	width      INTEGER NOT NULL DEFAULT 0,
	height     INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	UNIQUE (asset_id, filename)
);
CREATE INDEX asset_files_sha ON asset_files(sha256);
CREATE TABLE asset_tags (
	asset_id TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
	tag      TEXT NOT NULL,
	PRIMARY KEY (asset_id, tag)
) WITHOUT ROWID;
CREATE INDEX asset_tags_tag ON asset_tags(tag);
CREATE TABLE collections (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL DEFAULT 'pack',
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);
CREATE TABLE collection_assets (
	collection_id TEXT NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
	asset_id      TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
	added_at      TEXT NOT NULL,
	PRIMARY KEY (collection_id, asset_id)
) WITHOUT ROWID;
CREATE INDEX collection_assets_asset ON collection_assets(asset_id);
CREATE TABLE projects (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	url         TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'active',
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);
CREATE TABLE project_assets (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	asset_id   TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
	note       TEXT NOT NULL DEFAULT '',
	added_at   TEXT NOT NULL,
	PRIMARY KEY (project_id, asset_id)
) WITHOUT ROWID;
CREATE INDEX project_assets_asset ON project_assets(asset_id);
CREATE VIRTUAL TABLE assets_fts USING fts5(
	name, tags, description, prompt, extra,
	tokenize = 'unicode61 remove_diacritics 2', prefix = '2 3'
);
`,
	// 2: hoard as a development companion. Review status and lineage on
	// assets, notes, project briefs, the asset request queue for generator
	// agents, repository snapshots, similarity data and the activity feed.
	`
ALTER TABLE assets ADD COLUMN status        TEXT NOT NULL DEFAULT '';   -- see AssetStatuses
ALTER TABLE assets ADD COLUMN superseded_by TEXT NOT NULL DEFAULT '';   -- asset id that replaced this one
ALTER TABLE assets ADD COLUMN derived_from  TEXT NOT NULL DEFAULT '';   -- asset id this one was made from
ALTER TABLE assets ADD COLUMN generator     TEXT NOT NULL DEFAULT '{}'; -- structured generation recipe (JSON)
CREATE INDEX assets_status ON assets(deleted_at, status);
CREATE INDEX assets_derived ON assets(derived_from);

ALTER TABLE asset_files ADD COLUMN phash TEXT NOT NULL DEFAULT '';      -- perceptual hash of images (hex)

CREATE TABLE asset_colors (
	asset_id TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
	color    TEXT NOT NULL,  -- quantised colour bucket, e.g. "#3060c0"
	weight   REAL NOT NULL,  -- share of the image, 0..1
	PRIMARY KEY (asset_id, color)
) WITHOUT ROWID;
CREATE INDEX asset_colors_color ON asset_colors(color);

CREATE TABLE asset_notes (
	id         TEXT PRIMARY KEY,                  -- not_…
	asset_id   TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
	kind       TEXT NOT NULL DEFAULT 'comment',   -- see NoteKinds
	body       TEXT NOT NULL,
	author     TEXT NOT NULL DEFAULT '',          -- display form of the actor
	created_at TEXT NOT NULL,
	resolved_at TEXT
);
CREATE INDEX asset_notes_asset ON asset_notes(asset_id, created_at);

ALTER TABLE projects ADD COLUMN brief TEXT NOT NULL DEFAULT '{}';       -- ProjectBrief (JSON)

CREATE TABLE asset_requests (
	id                   TEXT PRIMARY KEY,                -- req_…
	project_id           TEXT REFERENCES projects(id) ON DELETE SET NULL,
	kind                 TEXT NOT NULL DEFAULT 'new',     -- new | rework
	title                TEXT NOT NULL,
	description          TEXT NOT NULL DEFAULT '',
	asset_type           TEXT NOT NULL DEFAULT '',
	spec                 TEXT NOT NULL DEFAULT '{}',      -- free-form constraints (JSON): size, duration, palette, format…
	target_asset_id      TEXT REFERENCES assets(id) ON DELETE CASCADE,  -- rework: the asset to redo
	placeholder_asset_id TEXT REFERENCES assets(id) ON DELETE SET NULL,
	accepted_asset_id    TEXT REFERENCES assets(id) ON DELETE SET NULL,
	status               TEXT NOT NULL DEFAULT 'open',    -- see RequestStatuses
	priority             INTEGER NOT NULL DEFAULT 0,      -- higher first
	claimed_by           TEXT NOT NULL DEFAULT '',
	claim_expires_at     TEXT,
	created_by           TEXT NOT NULL DEFAULT '',
	created_at           TEXT NOT NULL,
	updated_at           TEXT NOT NULL,
	closed_at            TEXT
);
CREATE INDEX asset_requests_status ON asset_requests(status, priority DESC, created_at);
CREATE INDEX asset_requests_project ON asset_requests(project_id);
CREATE INDEX asset_requests_target ON asset_requests(target_asset_id);

CREATE TABLE request_submissions (
	request_id   TEXT NOT NULL REFERENCES asset_requests(id) ON DELETE CASCADE,
	asset_id     TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
	note         TEXT NOT NULL DEFAULT '',
	verdict      TEXT NOT NULL DEFAULT '',  -- '' (pending) | accepted | rejected
	submitted_by TEXT NOT NULL DEFAULT '',
	submitted_at TEXT NOT NULL,
	PRIMARY KEY (request_id, asset_id)
) WITHOUT ROWID;
CREATE INDEX request_submissions_asset ON request_submissions(asset_id);

CREATE TABLE project_files (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	path       TEXT NOT NULL,          -- relative path inside the game repository
	sha256     TEXT NOT NULL,
	size       INTEGER NOT NULL DEFAULT 0,
	asset_id   TEXT REFERENCES assets(id) ON DELETE SET NULL,
	seen_at    TEXT NOT NULL,
	PRIMARY KEY (project_id, path)
) WITHOUT ROWID;
CREATE INDEX project_files_sha ON project_files(sha256);

CREATE TABLE activity (
	seq      INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused, even after pruning: clients poll with after_seq
	at       TEXT NOT NULL,
	actor    TEXT NOT NULL DEFAULT '',  -- display form, e.g. "toriath via MCP (claude-code)"
	user_id  TEXT NOT NULL DEFAULT '',
	via      TEXT NOT NULL DEFAULT '',  -- web | api | mcp | system
	action   TEXT NOT NULL,             -- e.g. asset.create, asset.update, request.claim
	asset_id TEXT NOT NULL DEFAULT '',  -- no foreign key: history outlives purged assets
	ref_id   TEXT NOT NULL DEFAULT '',  -- other object involved (request, project, note…)
	summary  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX activity_at ON activity(at);
CREATE INDEX activity_asset ON activity(asset_id, seq);
`,
}

func (s *Service) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// tx runs fn in a transaction.
func (s *Service) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---- settings ----

// GetSetting returns a stored setting, or "" when unset.
func (s *Service) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores a setting.
func (s *Service) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Service) loadSecret() ([]byte, error) {
	ctx := context.Background()
	v, err := s.GetSetting(ctx, "signing_secret")
	if err != nil {
		return nil, err
	}
	if v == "" {
		v = randomString(48)
		if err := s.SetSetting(ctx, "signing_secret", v); err != nil {
			return nil, err
		}
	}
	return []byte(v), nil
}

// ---- backups ----

// Backup writes a consistent snapshot of the database into backups/ and prunes
// old snapshots so that at most keep remain. Asset files are not copied: they
// live right next to the DB and each carries its own asset.json sidecar.
func (s *Service) Backup(ctx context.Context, keep int) error {
	if keep <= 0 {
		return nil
	}
	dir := filepath.Join(s.dataDir, "backups")
	dst := filepath.Join(dir, "hoard-"+time.Now().UTC().Format("2006-01-02")+".db")
	_ = os.Remove(dst)
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "hoard-") && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
	return nil
}

// ---- helpers ----

const idAlphabet = "0123456789abcdefghjkmnpqrstvwxyz" // Crockford base32, lowercase

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	for i := range b {
		b[i] = idAlphabet[int(b[i])%len(idAlphabet)]
	}
	return string(b)
}

// newID returns a short, URL-safe, prefixed identifier such as "ast_7k2m9q4x1c8v".
func newID(prefix string) string { return prefix + "_" + randomString(12) }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func anySlice[T any](in []T) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
