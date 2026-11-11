package hoard

// Delivery into a game repository: the repository snapshot (what files the
// game repo holds, by hash), the audit against the library, and the sync plan
// that decides where each asset's files belong.
//
// The server never reads the repository. An agent runs the audit command in
// the repo; the command lists the asset-like files with hash and size and
// uploads that list through a signed link. Everything else is computed here.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxSnapshotBytes = 8 << 20
	maxSnapshotLines = 50000
	maxRepoPathLen   = 1024
	auditListCap     = 500
	repoLinkTTL      = 2 * time.Hour
)

// ---- snapshot ----

// RepoFile is one line of a repository snapshot.
type RepoFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

var reHexSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

// validRepoPath checks a path reported by a repository: relative, no NUL or
// control characters, no "." or ".." segments, valid UTF-8, at most 1024 bytes.
func validRepoPath(p string) error {
	switch {
	case p == "":
		return errors.New("empty path")
	case len(p) > maxRepoPathLen:
		return fmt.Errorf("path longer than %d bytes", maxRepoPathLen)
	case !utf8.ValidString(p):
		return errors.New("path is not valid UTF-8")
	case strings.HasPrefix(p, "/"):
		return errors.New("path must be relative")
	case len(p) >= 2 && p[1] == ':' && unicode.IsLetter(rune(p[0])):
		return errors.New("path must be relative")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return errors.New("path contains a control character")
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("path has an empty, '.' or '..' segment")
		}
	}
	return nil
}

// ParseRepoSnapshot reads the line format "sha256<TAB>size<TAB>path". The
// whole input is rejected on the first bad line: a snapshot replaces the
// previous one, so a half-understood one would corrupt the audit.
func ParseRepoSnapshot(r io.Reader) ([]RepoFile, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxSnapshotBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	if len(data) > maxSnapshotBytes {
		return nil, invalid("snapshot is larger than %d MiB", maxSnapshotBytes>>20)
	}
	var out []RepoFile
	seen := map[string]bool{}
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if len(out) >= maxSnapshotLines {
			return nil, invalid("snapshot has more than %d files", maxSnapshotLines)
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return nil, invalid("line %d: expected sha256<TAB>size<TAB>path", n+1)
		}
		sum := strings.ToLower(parts[0])
		if !reHexSHA.MatchString(sum) {
			return nil, invalid("line %d: sha256 must be 64 hex digits", n+1)
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || size < 0 || size > 1<<50 {
			return nil, invalid("line %d: size must be a non-negative integer", n+1)
		}
		if err := validRepoPath(parts[2]); err != nil {
			return nil, invalid("line %d: %v", n+1, err)
		}
		if seen[parts[2]] {
			return nil, invalid("line %d: duplicate path", n+1)
		}
		seen[parts[2]] = true
		out = append(out, RepoFile{Path: parts[2], SHA256: sum, Size: size})
	}
	if len(out) == 0 {
		return nil, invalid("snapshot is empty: check the root directory the command ran in")
	}
	return out, nil
}

// RepoSnapshotResult summarises a stored snapshot.
type RepoSnapshotResult struct {
	ProjectID string `json:"project_id"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	Known     int    `json:"known"`   // files whose content matches an asset in the library
	Unknown   int    `json:"unknown"` // files hoard does not track
	SeenAt    string `json:"seen_at"`
}

func (s *Service) projectExists(ctx context.Context, id string) error {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("project %q: %w", id, ErrNotFound)
	}
	return err
}

// ReplaceRepoSnapshot parses a snapshot and replaces the project's stored one
// in a single transaction. Content hashes are matched against the library.
func (s *Service) ReplaceRepoSnapshot(ctx context.Context, projectID string, r io.Reader) (*RepoSnapshotResult, error) {
	if err := s.projectExists(ctx, projectID); err != nil {
		return nil, err
	}
	files, err := ParseRepoSnapshot(r)
	if err != nil {
		return nil, err
	}
	idx, err := s.loadLibIndex(ctx)
	if err != nil {
		return nil, err
	}
	members, err := s.projectMembers(ctx, projectID)
	if err != nil {
		return nil, err
	}
	res := &RepoSnapshotResult{ProjectID: projectID, Files: len(files), SeenAt: now()}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM project_files WHERE project_id = ?`, projectID); err != nil {
			return err
		}
		st, err := tx.PrepareContext(ctx, `INSERT INTO project_files(project_id, path, sha256, size, asset_id, seen_at) VALUES(?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer st.Close()
		for _, f := range files {
			res.Bytes += f.Size
			owner := idx.owner(f.SHA256, members)
			var aid any
			if owner != "" {
				aid = owner
				res.Known++
			} else {
				res.Unknown++
			}
			if _, err := st.ExecContext(ctx, projectID, f.Path, f.SHA256, f.Size, aid, res.SeenAt); err != nil {
				return err
			}
		}
		s.record(ctx, tx, "project.repo_snapshot", "", projectID,
			fmt.Sprintf("%d files (%d known, %d untracked)", res.Files, res.Known, res.Unknown))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) loadSnapshot(ctx context.Context, projectID string) (files []RepoFile, seenAt string, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, sha256, size, seen_at FROM project_files WHERE project_id = ? ORDER BY path`, projectID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var f RepoFile
		var at string
		if err := rows.Scan(&f.Path, &f.SHA256, &f.Size, &at); err != nil {
			return nil, "", err
		}
		if at > seenAt {
			seenAt = at
		}
		files = append(files, f)
	}
	return files, seenAt, rows.Err()
}

func (s *Service) projectMembers(ctx context.Context, projectID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pa.asset_id FROM project_assets pa JOIN assets a ON a.id = pa.asset_id
		WHERE pa.project_id = ? AND a.deleted_at IS NULL`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ---- library index ----

type idxAsset struct {
	ID, Name, Type, Status, SupersededBy, License, Attribution, PrimaryFileID string
	Attrs                                                                     map[string]any
	Files                                                                     []File
}

// libIndex is an in-memory view of the live library, loaded once per audit or
// sync. A self-hosted library is small enough for this to be cheaper than
// per-file queries.
type libIndex struct {
	assets    map[string]*idxAsset
	bySHA     map[string][]string
	newestMem map[string]string
	preds     map[string][]string // newest id -> assets it replaced (transitively)
}

func (s *Service) loadLibIndex(ctx context.Context) (*libIndex, error) {
	idx := &libIndex{assets: map[string]*idxAsset{}, bySHA: map[string][]string{}, newestMem: map[string]string{}}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, status, superseded_by, license, attribution, primary_file_id, attrs
		FROM assets WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		a := &idxAsset{}
		var attrs string
		if err := rows.Scan(&a.ID, &a.Name, &a.Type, &a.Status, &a.SupersededBy, &a.License, &a.Attribution, &a.PrimaryFileID, &attrs); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(attrs), &a.Attrs)
		idx.assets[a.ID] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	frows, err := s.db.QueryContext(ctx, `SELECT f.asset_id, f.id, f.filename, f.size, f.sha256, f.width, f.height
		FROM asset_files f JOIN assets a ON a.id = f.asset_id WHERE a.deleted_at IS NULL ORDER BY f.created_at, f.filename`)
	if err != nil {
		return nil, err
	}
	defer frows.Close()
	for frows.Next() {
		var aid string
		var f File
		if err := frows.Scan(&aid, &f.ID, &f.Filename, &f.Size, &f.SHA256, &f.Width, &f.Height); err != nil {
			return nil, err
		}
		f.Mime, f.Kind = MimeFor(f.Filename), PreviewKind(f.Filename)
		if a := idx.assets[aid]; a != nil {
			a.Files = append(a.Files, f)
			if !slices.Contains(idx.bySHA[f.SHA256], aid) {
				idx.bySHA[f.SHA256] = append(idx.bySHA[f.SHA256], aid)
			}
		}
	}
	return idx, frows.Err()
}

// newest follows superseded_by to the last live asset of the chain.
func (idx *libIndex) newest(id string) string {
	if v, ok := idx.newestMem[id]; ok {
		return v
	}
	cur := id
	seen := map[string]bool{}
	for {
		a := idx.assets[cur]
		if a == nil || a.SupersededBy == "" || seen[cur] || idx.assets[a.SupersededBy] == nil {
			break
		}
		seen[cur] = true
		cur = a.SupersededBy
	}
	idx.newestMem[id] = cur
	return cur
}

// predecessors lists the assets the given newest asset replaced, directly or
// through a chain, in id order.
func (idx *libIndex) predecessors(newest string) []string {
	if idx.preds == nil {
		idx.preds = map[string][]string{}
		ids := make([]string, 0, len(idx.assets))
		for id := range idx.assets {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if n := idx.newest(id); n != id {
				idx.preds[n] = append(idx.preds[n], id)
			}
		}
	}
	return idx.preds[newest]
}

// owner picks the asset a file of the given content belongs to: preferably one
// that is (or was replaced by) a project asset, and the newest of a chain
// rather than a replaced one. "" when no asset holds the content.
func (idx *libIndex) owner(sha string, members map[string]bool) string {
	ids := slices.Clone(idx.bySHA[sha])
	if len(ids) == 0 {
		return ""
	}
	rank := func(id string) int {
		n := idx.newest(id)
		r := 0
		if !members[n] && !members[id] {
			r += 2
		}
		if n != id {
			r++
		}
		return r
	}
	sort.Slice(ids, func(i, j int) bool {
		if ri, rj := rank(ids[i]), rank(ids[j]); ri != rj {
			return ri < rj
		}
		return ids[i] < ids[j]
	})
	return ids[0]
}

func (a *idxAsset) primary() *File {
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

// ---- audit ----

// AuditKnown is a repository file that matches an asset in the library.
type AuditKnown struct {
	Path         string   `json:"path"`
	AssetID      string   `json:"asset_id"`
	Name         string   `json:"name"`
	Status       string   `json:"status,omitempty"`
	License      string   `json:"license,omitempty"`
	Flags        []string `json:"flags,omitempty"` // superseded, rework, rejected, placeholder, not_in_project
	NewerAssetID string   `json:"newer_asset_id,omitempty"`
	NewerName    string   `json:"newer_name,omitempty"`
}

// AuditUnknown is a repository file hoard does not track.
type AuditUnknown struct {
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	SuggestedType string `json:"suggested_type"`
	SuggestedName string `json:"suggested_name"`
}

// AuditMissing is a project asset none of whose files is in the repository.
type AuditMissing struct {
	AssetID string   `json:"asset_id"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Status  string   `json:"status,omitempty"`
	Files   []string `json:"files"`
}

// AuditUnlinked is an asset present in the repository but not linked to the project.
type AuditUnlinked struct {
	AssetID string   `json:"asset_id"`
	Name    string   `json:"name"`
	Paths   []string `json:"paths"`
}

// AuditCounts are the headline numbers.
type AuditCounts struct {
	Files     int `json:"files"`
	InSync    int `json:"in_sync"`   // known files that are the newest version
	Outdated  int `json:"outdated"`  // known files whose asset was superseded
	Attention int `json:"attention"` // known files with status rework, rejected or placeholder
	Untracked int `json:"untracked"`
	Missing   int `json:"missing"`
	Unlinked  int `json:"unlinked"`
}

// RepoAudit is the result of comparing a repository snapshot with a project.
type RepoAudit struct {
	ProjectID   string          `json:"project_id"`
	HasSnapshot bool            `json:"has_snapshot"`
	SnapshotAt  string          `json:"snapshot_at,omitempty"`
	Counts      AuditCounts     `json:"counts"`
	Known       []AuditKnown    `json:"known"`
	Unknown     []AuditUnknown  `json:"unknown"`
	Missing     []AuditMissing  `json:"missing"`
	Unlinked    []AuditUnlinked `json:"unlinked"`
	Truncated   bool            `json:"truncated,omitempty"` // lists are cut at 500 entries; counts are complete
	Hint        string          `json:"hint,omitempty"`
}

func capList[T any](l []T, trunc *bool) []T {
	if len(l) > auditListCap {
		*trunc = true
		return l[:auditListCap]
	}
	return l
}

// AuditProject compares the stored repository snapshot of a project with the
// library.
func (s *Service) AuditProject(ctx context.Context, projectID string) (*RepoAudit, error) {
	if err := s.projectExists(ctx, projectID); err != nil {
		return nil, err
	}
	snap, seenAt, err := s.loadSnapshot(ctx, projectID)
	if err != nil {
		return nil, err
	}
	res := &RepoAudit{ProjectID: projectID, Known: []AuditKnown{}, Unknown: []AuditUnknown{}, Missing: []AuditMissing{}, Unlinked: []AuditUnlinked{}}
	if len(snap) == 0 {
		res.Hint = "No repository snapshot yet: run get_repo_audit_command and execute the command in the game repository."
		return res, nil
	}
	res.HasSnapshot, res.SnapshotAt = true, seenAt
	idx, err := s.loadLibIndex(ctx)
	if err != nil {
		return nil, err
	}
	members, err := s.projectMembers(ctx, projectID)
	if err != nil {
		return nil, err
	}

	present := map[string]bool{} // sha256 -> in the repository
	unlinked := map[string]*AuditUnlinked{}
	for _, f := range snap {
		present[f.SHA256] = true
		res.Counts.Files++
		owner := idx.owner(f.SHA256, members)
		if owner == "" {
			res.Counts.Untracked++
			stem := strings.TrimSuffix(path.Base(f.Path), path.Ext(f.Path))
			res.Unknown = append(res.Unknown, AuditUnknown{Path: f.Path, Size: f.Size,
				SuggestedType: GuessType(path.Base(f.Path), f.Size), SuggestedName: stem})
			continue
		}
		a := idx.assets[owner]
		k := AuditKnown{Path: f.Path, AssetID: owner, Name: a.Name, Status: a.Status, License: a.License}
		newest := idx.newest(owner)
		if newest != owner {
			n := idx.assets[newest]
			k.Flags = append(k.Flags, "superseded")
			k.NewerAssetID, k.NewerName = newest, n.Name
			res.Counts.Outdated++
		} else {
			res.Counts.InSync++
		}
		switch a.Status {
		case "rework", "rejected", "placeholder":
			k.Flags = append(k.Flags, a.Status)
			res.Counts.Attention++
		}
		if !members[owner] && !members[newest] {
			k.Flags = append(k.Flags, "not_in_project")
			u := unlinked[newest]
			if u == nil {
				u = &AuditUnlinked{AssetID: newest, Name: idx.assets[newest].Name}
				unlinked[newest] = u
			}
			u.Paths = append(u.Paths, f.Path)
		}
		res.Known = append(res.Known, k)
	}

	seenMissing := map[string]bool{}
	for m := range members {
		n := idx.newest(m)
		if seenMissing[n] {
			continue
		}
		seenMissing[n] = true
		a := idx.assets[n]
		if a == nil || len(a.Files) == 0 {
			continue
		}
		found := false
		for _, f := range a.Files {
			if present[f.SHA256] {
				found = true
				break
			}
		}
		if !found {
			mm := AuditMissing{AssetID: n, Name: a.Name, Type: a.Type, Status: a.Status}
			for _, f := range a.Files {
				mm.Files = append(mm.Files, f.Filename)
			}
			res.Missing = append(res.Missing, mm)
		}
	}
	sort.Slice(res.Missing, func(i, j int) bool { return res.Missing[i].Name < res.Missing[j].Name })
	res.Counts.Missing = len(res.Missing)
	for _, u := range unlinked {
		res.Unlinked = append(res.Unlinked, *u)
	}
	sort.Slice(res.Unlinked, func(i, j int) bool { return res.Unlinked[i].Name < res.Unlinked[j].Name })
	res.Counts.Unlinked = len(res.Unlinked)

	res.Known = capList(res.Known, &res.Truncated)
	res.Unknown = capList(res.Unknown, &res.Truncated)
	res.Missing = capList(res.Missing, &res.Truncated)
	res.Unlinked = capList(res.Unlinked, &res.Truncated)
	return res, nil
}

// ---- audit command ----

// repoSkipDirs are never descended into by the audit command.
var repoSkipDirs = []string{".git", "node_modules", ".godot", ".import", ".mono", "Library", "Temp", "Logs", "obj", "build", "Build", "dist", ".gradle", ".idea", ".vs", ".venv", "__pycache__", "target"}

// repoCodeExt are extensions of extTable that are code or text, not assets.
var repoCodeExt = map[string]bool{
	".gd": true, ".cs": true, ".lua": true, ".py": true, ".js": true, ".json": true, ".md": true, ".txt": true,
	".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".cfg": true, ".tscn": true, ".tres": true, ".xml": true,
	".csv": true, ".material": true, ".mat": true, ".atlas": true, ".mtl": true,
}

// DefaultAuditExtensions lists the extensions the audit command looks for.
func DefaultAuditExtensions() []string {
	var out []string
	for e := range extTable {
		if !repoCodeExt[e] {
			out = append(out, strings.TrimPrefix(e, "."))
		}
	}
	sort.Strings(out)
	return out
}

var reExt = regexp.MustCompile(`^[a-z0-9]{1,12}$`)

// shellQuoteArg quotes for a POSIX shell, like the MCP layer's shellQuote.
func shellQuoteArg(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// cleanRoot validates a directory argument that ends up in a shell command.
func cleanRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return ".", nil
	}
	for _, r := range root {
		if r < 0x20 || r == 0x7f {
			return "", invalid("root must not contain control characters")
		}
	}
	if len(root) > 1024 || !utf8.ValidString(root) {
		return "", invalid("root is not a usable path")
	}
	return root, nil
}

// RepoSnapshotURL is the signed upload link for a project's snapshot.
func (s *Service) RepoSnapshotURL(base, projectID string) (link string, expires time.Time) {
	exp, sig := s.Sign("repo-snapshot:"+projectID, repoLinkTTL)
	return fmt.Sprintf("%s/api/v1/projects/%s/repo/snapshot?exp=%s&sig=%s", base, url.PathEscape(projectID), exp, sig), time.Now().Add(repoLinkTTL)
}

// RepoLockURL is the signed download link for a project's lock file.
func (s *Service) RepoLockURL(base, projectID string) string {
	exp, sig := s.Sign("repo-lock:"+projectID, repoLinkTTL)
	return fmt.Sprintf("%s/api/v1/projects/%s/repo/lock-file?exp=%s&sig=%s", base, url.PathEscape(projectID), exp, sig)
}

// RepoAuditCommand returns one POSIX shell command that lists the asset-like
// files below root (sha256, size, relative path) and uploads the list through a
// signed link bound to the project.
func (s *Service) RepoAuditCommand(ctx context.Context, base, projectID, root string, extensions []string) (string, error) {
	if err := s.projectExists(ctx, projectID); err != nil {
		return "", err
	}
	root, err := cleanRoot(root)
	if err != nil {
		return "", err
	}
	exts := DefaultAuditExtensions()
	if len(extensions) > 0 {
		exts = nil
		for _, e := range extensions {
			e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
			if !reExt.MatchString(e) {
				return "", invalid("extension %q: use letters and digits only, e.g. png", e)
			}
			if !slices.Contains(exts, e) {
				exts = append(exts, e)
			}
		}
	}
	var prune []string
	for _, d := range repoSkipDirs {
		prune = append(prune, "-name "+shellQuoteArg(d))
	}
	link, _ := s.RepoSnapshotURL(base, projectID)
	script := "( cd " + shellQuoteArg(root) + " || exit 1; " +
		"if command -v sha256sum >/dev/null 2>&1; then H=sha256sum; else H='shasum -a 256'; fi; " +
		"find . \\( " + strings.Join(prune, " -o ") + " \\) -prune -o -type f -print" +
		" | grep -iE " + shellQuoteArg(`\.(`+strings.Join(exts, "|")+`)$`) +
		" | LC_ALL=C sort | while IFS= read -r f; do " +
		`[ -f "$f" ] || continue; p=${f#./}; case "$p" in *"$(printf '\t')"*) continue;; esac; ` +
		`h=$($H < "$f" | cut -d' ' -f1); s=$(wc -c < "$f" | tr -d ' '); ` +
		`printf '%s\t%s\t%s\n' "$h" "$s" "$p"; done )`
	return script + " | curl -fsS --globoff -T - " + shellQuoteArg(link), nil
}

// ---- sync ----

type deliveryBrief struct {
	Engine   string            `json:"engine"`
	PixelArt bool              `json:"pixel_art"`
	TileSize float64           `json:"tile_size"`
	Folders  map[string]string `json:"folders"`
	Naming   string            `json:"naming"`
}

// readBrief loads the brief fields delivery needs; a missing or broken brief
// is an empty one.
func (s *Service) readBrief(ctx context.Context, projectID string) deliveryBrief {
	var b deliveryBrief
	raw, err := s.ProjectBriefJSON(ctx, projectID)
	if err == nil {
		_ = json.Unmarshal(raw, &b)
	}
	return b
}

// cleanFolder validates a repo folder from a brief; "" when unusable.
func cleanFolder(f string) string {
	f = strings.Trim(strings.ReplaceAll(strings.TrimSpace(f), "\\", "/"), "/")
	if f == "" || f == "." {
		return ""
	}
	if validRepoPath(f) != nil {
		return ""
	}
	return f
}

var reWords = regexp.MustCompile(`[\p{L}\p{N}]+`)

// splitWords breaks an identifier into words at separators and camelCase humps.
func splitWords(s string) []string {
	var words []string
	for _, chunk := range reWords.FindAllString(s, -1) {
		rs := []rune(chunk)
		start := 0
		for i := 1; i < len(rs); i++ {
			prev, cur := rs[i-1], rs[i]
			hump := unicode.IsLower(prev) && unicode.IsUpper(cur)
			acronym := i+1 < len(rs) && unicode.IsUpper(prev) && unicode.IsUpper(cur) && unicode.IsLower(rs[i+1])
			if hump || acronym {
				words = append(words, string(rs[start:i]))
				start = i
			}
		}
		words = append(words, string(rs[start:]))
	}
	return words
}

// ApplyNaming converts a file name to the brief's naming convention. The
// extension is kept (lower-cased). "keep" and unknown styles only sanitise.
func ApplyNaming(filename, style string) string {
	filename = SanitizeFilename(filename)
	e := path.Ext(filename)
	stem := strings.TrimSuffix(filename, e)
	e = strings.ToLower(e)
	words := splitWords(stem)
	if len(words) == 0 {
		return filename
	}
	title := func(w string) string {
		rs := []rune(strings.ToLower(w))
		rs[0] = unicode.ToUpper(rs[0])
		return string(rs)
	}
	var out string
	switch style {
	case "snake_case":
		out = strings.ToLower(strings.Join(words, "_"))
	case "kebab-case":
		out = strings.ToLower(strings.Join(words, "-"))
	case "camelCase":
		out = strings.ToLower(words[0])
		for _, w := range words[1:] {
			out += title(w)
		}
	case "PascalCase":
		for _, w := range words {
			out += title(w)
		}
	default:
		return filename
	}
	return out + e
}

// SyncEntry is one file the project should have in the repository.
type SyncEntry struct {
	Path        string `json:"path"`
	Reason      string `json:"reason"` // missing | changed | unchanged | conflict
	AssetID     string `json:"asset_id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status,omitempty"`
	Filename    string `json:"filename"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	License     string `json:"license,omitempty"`
	Attribution string `json:"attribution,omitempty"`
	Note        string `json:"note,omitempty"`
}

// SyncRemoval is a repository file whose asset was superseded and whose
// replacement lives at a different path. hoard never deletes anything.
type SyncRemoval struct {
	Path       string `json:"path"`
	AssetID    string `json:"asset_id"`
	ReplacedBy string `json:"replaced_by"`
	NewPath    string `json:"new_path,omitempty"`
}

// SyncPlan says where every file of the project's assets belongs.
type SyncPlan struct {
	ProjectID   string        `json:"project_id"`
	ProjectName string        `json:"project_name"`
	GeneratedAt string        `json:"generated_at"`
	HasSnapshot bool          `json:"has_snapshot"`
	Entries     []SyncEntry   `json:"entries"`
	Removals    []SyncRemoval `json:"removals"`
	Warnings    []string      `json:"warnings,omitempty"`
}

// PlanSync decides, for each asset used in the project (following superseded_by
// to the newest version), where its files go in the repository and whether
// they are already there.
func (s *Service) PlanSync(ctx context.Context, projectID string) (*SyncPlan, error) {
	p, err := s.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	snap, _, err := s.loadSnapshot(ctx, projectID)
	if err != nil {
		return nil, err
	}
	idx, err := s.loadLibIndex(ctx)
	if err != nil {
		return nil, err
	}
	members, err := s.projectMembers(ctx, projectID)
	if err != nil {
		return nil, err
	}
	brief := s.readBrief(ctx, projectID)

	plan := &SyncPlan{ProjectID: projectID, ProjectName: p.Name, GeneratedAt: now(), HasSnapshot: len(snap) > 0, Entries: []SyncEntry{}, Removals: []SyncRemoval{}}
	if len(snap) == 0 {
		plan.Warnings = append(plan.Warnings, "No repository snapshot: every file is treated as missing. Run the audit command first to avoid overwriting files.")
	}
	byPath := map[string]RepoFile{}
	bySHA := map[string][]string{}
	for _, f := range snap { // snap is sorted by path
		byPath[f.Path] = f
		bySHA[f.SHA256] = append(bySHA[f.SHA256], f.Path)
	}

	newestSet := map[string]bool{}
	var order []*idxAsset
	for m := range members {
		n := idx.newest(m)
		if !newestSet[n] && idx.assets[n] != nil && len(idx.assets[n].Files) > 0 {
			newestSet[n] = true
			order = append(order, idx.assets[n])
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].Name != order[j].Name {
			return order[i].Name < order[j].Name
		}
		return order[i].ID < order[j].ID
	})

	used := map[string]bool{}
	reused := map[string]bool{}
	firstPath := map[string]string{} // newest asset -> path of its primary file
	for _, a := range order {
		switch a.Status {
		case "rework", "rejected":
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s (%s) has status %s", a.Name, a.ID, a.Status))
		}
		preds := idx.predecessors(a.ID)
		prim := a.primary()
		for _, f := range a.Files {
			e := SyncEntry{AssetID: a.ID, Name: a.Name, Type: a.Type, Status: a.Status, Filename: f.Filename,
				SHA256: f.SHA256, Size: f.Size, License: a.License, Attribution: a.Attribution}
			if paths := bySHA[f.SHA256]; len(paths) > 0 {
				e.Path, e.Reason = paths[0], "unchanged"
				used[e.Path] = true
			} else {
				// In place: a file of this asset's predecessors already sits in the repo.
				inPlace := ""
				note := ""
				for _, oid := range preds {
					o := idx.assets[oid]
					for _, of := range o.Files {
						for _, cand := range bySHA[of.SHA256] {
							if used[cand] || inPlace != "" {
								continue
							}
							sameExt := strings.EqualFold(path.Ext(cand), path.Ext(f.Filename))
							okName := len(a.Files) == 1 || strings.EqualFold(path.Base(cand), f.Filename)
							if sameExt && okName {
								inPlace = cand
								note = "updates " + o.Name + " (" + o.ID + ") in place"
							}
						}
					}
				}
				if inPlace != "" {
					e.Path, e.Reason, e.Note = inPlace, "changed", note
					reused[inPlace] = true
				} else {
					e.Path = s.defaultRepoPath(brief, a, f, used)
					if old, ok := byPath[e.Path]; ok && old.SHA256 != f.SHA256 {
						e.Reason, e.Note = "conflict", "a different file already exists at this path"
					} else {
						e.Reason = "missing"
					}
				}
				used[e.Path] = true
			}
			if prim != nil && f.ID == prim.ID {
				firstPath[a.ID] = e.Path
			}
			plan.Entries = append(plan.Entries, e)
		}
	}

	// Files of replaced assets that were not updated in place.
	reportedRemoval := map[string]bool{}
	for _, a := range order {
		for _, oid := range idx.predecessors(a.ID) {
			for _, of := range idx.assets[oid].Files {
				for _, p := range bySHA[of.SHA256] {
					if reused[p] || used[p] || reportedRemoval[p] {
						continue
					}
					reportedRemoval[p] = true
					plan.Removals = append(plan.Removals, SyncRemoval{Path: p, AssetID: oid, ReplacedBy: a.ID, NewPath: firstPath[a.ID]})
				}
			}
		}
	}
	sort.Slice(plan.Removals, func(i, j int) bool { return plan.Removals[i].Path < plan.Removals[j].Path })
	return plan, nil
}

// defaultRepoPath places a file by the brief's folders and naming.
func (s *Service) defaultRepoPath(b deliveryBrief, a *idxAsset, f File, used map[string]bool) string {
	folder := cleanFolder(b.Folders[a.Type])
	if folder == "" {
		folder = cleanFolder(b.Folders["default"])
	}
	if folder == "" {
		folder = "assets/" + a.Type
	}
	name := ApplyNaming(f.Filename, b.Naming)
	p := folder + "/" + name
	if used[p] {
		e := path.Ext(name)
		p = folder + "/" + strings.TrimSuffix(name, e) + "_" + strings.TrimPrefix(a.ID, "ast_") + e
	}
	return p
}

// RepoLock is the content of hoard.lock.json.
type RepoLock struct {
	ProjectID   string          `json:"project_id"`
	ProjectName string          `json:"project_name"`
	GeneratedAt string          `json:"generated_at"`
	Entries     []RepoLockEntry `json:"entries"`
}

// RepoLockEntry pins one repository file to an asset version.
type RepoLockEntry struct {
	Path        string `json:"path"`
	AssetID     string `json:"asset_id"`
	Filename    string `json:"filename"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	License     string `json:"license,omitempty"`
	Attribution string `json:"attribution,omitempty"`
}

// BuildRepoLock generates the lock file: where every asset file belongs after
// a sync, with its content hash, license and attribution.
func (s *Service) BuildRepoLock(ctx context.Context, projectID string) (*RepoLock, error) {
	plan, err := s.PlanSync(ctx, projectID)
	if err != nil {
		return nil, err
	}
	lock := &RepoLock{ProjectID: plan.ProjectID, ProjectName: plan.ProjectName, GeneratedAt: plan.GeneratedAt, Entries: []RepoLockEntry{}}
	for _, e := range plan.Entries {
		if e.Reason == "conflict" {
			continue
		}
		lock.Entries = append(lock.Entries, RepoLockEntry{Path: e.Path, AssetID: e.AssetID, Filename: e.Filename,
			SHA256: e.SHA256, Size: e.Size, License: e.License, Attribution: e.Attribution})
	}
	sort.Slice(lock.Entries, func(i, j int) bool { return lock.Entries[i].Path < lock.Entries[j].Path })
	return lock, nil
}
