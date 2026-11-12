package hoard

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Importing an asset pack from a zip archive. The archive is an untrusted
// upload: entry paths never reach the file system (every file goes through
// AddFile, which keeps only a sanitised base name), the number of entries,
// their sizes and their compression ratio are bounded before anything is
// created, and the bytes actually read are bounded again while extracting.

const (
	// ImportMaxEntries is the most entries (files and folders) a zip may hold.
	ImportMaxEntries = 10000
	// ImportMaxZipBytes caps the size of the uploaded archive itself.
	ImportMaxZipBytes = 2 << 30
	// importMaxRatio is the compression ratio above which a large entry is
	// taken for a zip bomb. Deflate cannot exceed about 1000:1.
	importMaxRatio = 200
	// importRatioFloor: entries below this size are never judged by ratio.
	importRatioFloor = 4 << 20
	importDocLimit   = 64 << 10 // bytes read from a licence or readme file

	ImportPerFile   = "per_file"
	ImportPerFolder = "per_folder"
	ImportSingle    = "single"
)

// ImportOptions control how a pack is imported.
type ImportOptions struct {
	Mode           string   `json:"mode,omitempty"`            // per_file (default) | per_folder | single
	CollectionName string   `json:"collection_name,omitempty"` // default: the zip's name
	License        string   `json:"license,omitempty"`         // overrides the licence found in the pack
	Tags           []string `json:"tags,omitempty"`            // added to every asset
	ProjectID      string   `json:"project_id,omitempty"`      // record usage in this project
}

// EncodeImportOptions packs options into one URL-safe token, so that a signed
// link can bind them.
func EncodeImportOptions(o ImportOptions) string {
	b, _ := json.Marshal(o)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeImportOptions reverses EncodeImportOptions ("" means defaults).
func DecodeImportOptions(tok string) (ImportOptions, error) {
	var o ImportOptions
	if tok == "" {
		return o, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(b) > 16<<10 {
		return o, invalid("import options are malformed")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return o, invalid("import options are malformed")
	}
	return o, nil
}

// ImportedAsset is one asset created by an import.
type ImportedAsset struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Files int    `json:"files"`
}

// ImportFailure names a file or group that could not be imported.
type ImportFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// ImportResult summarises an import.
type ImportResult struct {
	Summary         string          `json:"summary"`
	Mode            string          `json:"mode"`
	CollectionID    string          `json:"collection_id,omitempty"`
	CollectionName  string          `json:"collection_name,omitempty"`
	License         string          `json:"license,omitempty"`
	LicenseSource   string          `json:"license_source,omitempty"` // the file the licence was read from, or "options"
	Attribution     string          `json:"attribution,omitempty"`
	Assets          []ImportedAsset `json:"assets"`
	Files           int             `json:"files"`
	Docs            []string        `json:"docs,omitempty"`             // licence and readme files of the pack; not imported as assets
	LicenseConflict []string        `json:"license_conflict,omitempty"` // licences found that disagree; none was applied
	AINotQueued     int             `json:"ai_not_queued,omitempty"`    // assets the AI agent was not asked to describe
	Skipped         int             `json:"skipped"`
	SkippedSample   []string        `json:"skipped_sample,omitempty"`
	Failed          []ImportFailure `json:"failed,omitempty"`
}

// ImportZipReader spools r into tmp/ (at most maxBytes) and imports it.
func (s *Service) ImportZipReader(ctx context.Context, r io.Reader, zipName string, opts ImportOptions, maxBytes int64) (*ImportResult, error) {
	if err := validateImportOptions(&opts); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Join(s.dataDir, "tmp"), "import-*.zip")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	// The archive itself is capped too: the configured upload size, at most 2 GiB.
	if maxBytes <= 0 || maxBytes > ImportMaxZipBytes {
		maxBytes = ImportMaxZipBytes
	}
	limited := io.LimitReader(r, maxBytes+1)
	n, err := io.Copy(tmp, limited)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("store upload: %w", err)
	}
	if n > maxBytes {
		return nil, ErrTooLarge
	}
	return s.ImportZip(ctx, tmp.Name(), zipName, opts, maxBytes)
}

func validateImportOptions(o *ImportOptions) error {
	switch o.Mode {
	case "":
		o.Mode = ImportPerFile
	case ImportPerFile, ImportPerFolder, ImportSingle:
	default:
		return invalid("unknown import mode %q (use per_file, per_folder or single)", o.Mode)
	}
	if len(o.CollectionName) > 200 {
		return invalid("collection_name must be at most 200 characters")
	}
	if len(o.License) > 200 {
		return invalid("license must be at most 200 characters")
	}
	o.Tags = NormalizeTags(o.Tags)
	if len(o.Tags) > 50 {
		return invalid("at most 50 tags")
	}
	return nil
}

type zipEntry struct {
	f     *zip.File
	parts []string // sanitised path components, never used as a path
}

func (e zipEntry) base() string { return e.parts[len(e.parts)-1] }

// reDocName matches the files in a pack's root that describe the pack.
var reLicenseName = regexp.MustCompile(`(?i)^(licen[sc]e|copying)([-_. ].*)?$`)

var reDocName = regexp.MustCompile(`(?i)^(licen[sc]e|copying|readme|credits?|attribution|notice|authors|terms)([-_. ].*)?$`)

// docExts are the extensions of files that document a pack (none counts too).
var docExts = map[string]bool{"": true, ".txt": true, ".md": true, ".rst": true, ".html": true, ".htm": true, ".pdf": true}

// isPackDoc reports whether a root file is the pack's licence, readme or
// credits: a documentation name with a text or document extension.
func isPackDoc(name string) bool {
	e := filepath.Ext(name)
	return docExts[strings.ToLower(e)] && reDocName.MatchString(strings.TrimSuffix(name, e))
}

// zipEntryCount reads the number of entries declared by the end-of-central-
// directory record (and the zip64 one) without parsing the directory.
func zipEntryCount(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := st.Size()
	tail := min(size, 64<<10+22)
	buf := make([]byte, tail)
	if _, err := f.ReadAt(buf, size-tail); err != nil && err != io.EOF {
		return 0, err
	}
	i := bytes.LastIndex(buf, []byte("PK\x05\x06"))
	if i < 0 || i+22 > len(buf) {
		return 0, errors.New("no end of central directory")
	}
	n := uint64(binary.LittleEndian.Uint16(buf[i+10:]))
	if n != 0xFFFF {
		return n, nil
	}
	// zip64: the locator sits right before the end record
	if i >= 20 && string(buf[i-20:i-16]) == "PK\x06\x07" {
		off := int64(binary.LittleEndian.Uint64(buf[i-12:]))
		var rec [56]byte
		if off >= 0 && off+56 <= size {
			if _, err := f.ReadAt(rec[:], off); err == nil && string(rec[:4]) == "PK\x06\x06" {
				return binary.LittleEndian.Uint64(rec[32:]), nil
			}
		}
	}
	return n, nil
}

// ImportZip imports the archive at path as a pack. zipName is the name the
// archive was uploaded under. maxBytes bounds the total uncompressed size (0 =
// unlimited).
func (s *Service) ImportZip(ctx context.Context, zipPath, zipName string, opts ImportOptions, maxBytes int64) (*ImportResult, error) {
	if err := validateImportOptions(&opts); err != nil {
		return nil, err
	}
	if opts.ProjectID != "" {
		if _, err := s.GetProject(ctx, opts.ProjectID); err != nil {
			return nil, err
		}
	}
	// The central directory is read in full by zip.OpenReader: look at the
	// declared entry count first.
	if n, err := zipEntryCount(zipPath); err == nil && n > ImportMaxEntries {
		return nil, invalid("the archive has %d entries; at most %d are imported at once", n, ImportMaxEntries)
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, invalid("not a readable zip archive: %v", err)
	}
	defer zr.Close()
	if len(zr.File) > ImportMaxEntries {
		return nil, invalid("the archive has %d entries; at most %d are imported at once", len(zr.File), ImportMaxEntries)
	}

	// 1. Select entries and check the limits before anything is created.
	res := &ImportResult{Mode: opts.Mode, Assets: []ImportedAsset{}}
	skip := func(name string) {
		res.Skipped++
		if len(res.SkippedSample) < 10 {
			res.SkippedSample = append(res.SkippedSample, truncateRunes(name, 80))
		}
	}
	perEntry := maxBytes
	var entries []zipEntry
	var declared int64
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			continue
		}
		parts, hidden := cleanZipPath(name)
		switch {
		case hidden || len(parts) == 0:
			skip(name)
			continue
		case f.Mode()&os.ModeSymlink != 0 || f.Flags&0x1 != 0 || f.UncompressedSize64 == 0:
			skip(name) // links, encrypted and empty files cannot be stored
			continue
		}
		size := int64(f.UncompressedSize64)
		if f.UncompressedSize64 > 1<<62 || (perEntry > 0 && size > perEntry) {
			return nil, invalid("%q is larger than the upload size limit", truncateRunes(name, 80))
		}
		if size >= importRatioFloor && f.CompressedSize64 > 0 && size/int64(f.CompressedSize64) > importMaxRatio {
			return nil, invalid("%q has a compression ratio above %d:1; the archive is refused as a possible zip bomb", truncateRunes(name, 80), importMaxRatio)
		}
		declared += size
		if maxBytes > 0 && declared > maxBytes {
			return nil, invalid("the archive unpacks to more than the upload size limit (%d MiB)", maxBytes>>20)
		}
		entries = append(entries, zipEntry{f: f, parts: parts})
	}
	if len(entries) == 0 {
		return nil, invalid("the archive contains no files to import")
	}

	// 2. The licence of the pack, from the files describing it.
	if opts.License != "" {
		res.License, res.LicenseSource = opts.License, "options"
	}
	lic, attr, src, conflict := s.detectPackLicense(entries)
	if res.License == "" && lic != "" {
		res.License, res.LicenseSource = lic, src
	}
	if res.License == "" {
		res.LicenseConflict = conflict
	}
	res.Attribution = attr

	// 3. Group the files into assets.
	packName := strings.TrimSpace(opts.CollectionName)
	if packName == "" {
		packName = strings.TrimSpace(strings.TrimSuffix(path.Base(strings.ReplaceAll(zipName, "\\", "/")), filepath.Ext(zipName)))
	}
	if packName == "" || packName == "." {
		packName = "Imported pack"
	}
	type member struct {
		e    zipEntry
		name string
	}
	type group struct {
		name    string
		members []member
	}
	var groups []*group
	byKey := map[string]*group{}
	for _, e := range entries {
		root := len(e.parts) == 1
		if root && isPackDoc(e.base()) { // describes the pack; never an asset, whatever the mode
			res.Docs = append(res.Docs, e.base())
			continue
		}
		var key, gname, fname string
		switch opts.Mode {
		case ImportSingle:
			key, gname, fname = "single", packName, strings.Join(e.parts, "_")
		case ImportPerFolder:
			if root {
				key, gname, fname = "file:"+e.base(), "", e.base()
			} else {
				key, gname, fname = "dir:"+e.parts[0], e.parts[0], strings.Join(e.parts[1:], "_")
			}
		default:
			key, gname, fname = fmt.Sprintf("file:%d", len(groups)), "", e.base()
		}
		g := byKey[key]
		if g == nil {
			g = &group{name: gname}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.members = append(g.members, member{e, fname})
	}
	if len(groups) == 0 {
		return nil, invalid("the archive contains no importable files")
	}

	// 4. Collection, then assets.
	desc := "Imported from " + truncateRunes(zipName, 120) + "."
	if res.License != "" {
		desc += " License: " + res.License + "."
	}
	if res.Attribution != "" {
		desc += " " + res.Attribution
	}
	kind := "pack"
	col, err := s.SaveCollection(ctx, "", GroupInput{Name: &packName, Kind: &kind, Description: &desc})
	if err != nil {
		return nil, err
	}
	res.CollectionID, res.CollectionName = col.ID, col.Name
	source := "Imported from " + truncateRunes(zipName, 120)

	for _, g := range groups {
		if err := ctx.Err(); err != nil {
			break
		}
		in := AssetInput{Source: &source, AddTags: opts.Tags, AddCollections: []string{col.ID}}
		if g.name != "" {
			in.Name = &g.name
		}
		if res.License != "" {
			in.License = &res.License
		}
		if res.Attribution != "" {
			in.Attribution = &res.Attribution
		}
		if opts.ProjectID != "" {
			in.AddProjects = []string{opts.ProjectID}
			in.ProjectNote = "imported with " + truncateRunes(zipName, 80)
		}
		a, err := s.CreateAsset(ctx, in)
		if err != nil {
			res.Failed = append(res.Failed, ImportFailure{Name: firstNonEmpty(g.name, g.members[0].name), Error: err.Error()})
			continue
		}
		taken := map[string]bool{}
		stored := 0
		for _, m := range g.members {
			fname := uniqueName(SanitizeFilename(m.name), taken)
			rc, err := m.e.f.Open()
			if err != nil {
				res.Failed = append(res.Failed, ImportFailure{Name: fname, Error: err.Error()})
				continue
			}
			// The declared size was checked; the bytes read are bounded by it.
			_, err = s.AddFile(ctx, a.ID, fname, rc, int64(m.e.f.UncompressedSize64))
			rc.Close()
			if err != nil {
				res.Failed = append(res.Failed, ImportFailure{Name: fname, Error: err.Error()})
				continue
			}
			stored++
		}
		if stored == 0 { // nothing usable: do not leave an empty asset behind
			_, _ = s.Bulk(context.WithoutCancel(ctx), BulkOp{IDs: []string{a.ID}, Action: "purge"})
			continue
		}
		res.Files += stored
		res.Assets = append(res.Assets, ImportedAsset{ID: a.ID, Name: firstNonEmpty(g.name, a.Name), Files: stored})
	}
	if len(res.Assets) == 0 {
		_ = s.DeleteCollection(context.WithoutCancel(ctx), col.ID)
		res.CollectionID, res.CollectionName = "", ""
	}
	// refresh names that came from the first file
	for i := range res.Assets {
		if res.Assets[i].Name == "" {
			if a, err := s.GetAsset(ctx, res.Assets[i].ID); err == nil {
				res.Assets[i].Name = a.Name
			}
		}
	}
	res.Summary = fmt.Sprintf("Imported %d file(s) as %d asset(s)", res.Files, len(res.Assets))
	if res.CollectionName != "" {
		res.Summary += " into the pack collection " + `"` + res.CollectionName + `"`
	}
	if res.License != "" {
		res.Summary += "; license " + res.License
	}
	if len(res.Docs) > 0 {
		res.Summary += "; not imported as assets: " + strings.Join(res.Docs, ", ")
	}
	if len(res.LicenseConflict) > 0 {
		res.Summary += "; no license applied, the pack names several (" + strings.Join(res.LicenseConflict, ", ") + ")"
	} else if res.License == "" {
		res.Summary += "; no license found in the pack"
	}
	if res.Skipped > 0 {
		res.Summary += fmt.Sprintf("; skipped %d entr%s", res.Skipped, map[bool]string{true: "y", false: "ies"}[res.Skipped == 1])
	}
	if len(res.Failed) > 0 {
		res.Summary += fmt.Sprintf("; %d failed", len(res.Failed))
	}
	res.Summary += "."
	s.record(ctx, nil, "pack.import", "", res.CollectionID, res.Summary)
	return res, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// uniqueName makes name unique (ignoring case) among the taken ones.
func uniqueName(name string, taken map[string]bool) string {
	cand := name
	e := filepath.Ext(name)
	stem := strings.TrimSuffix(name, e)
	for i := 2; taken[strings.ToLower(cand)]; i++ {
		cand = fmt.Sprintf("%s_%d%s", stem, i, e)
	}
	taken[strings.ToLower(cand)] = true
	return cand
}

// cleanZipPath splits an entry path into harmless components. Components
// that climb ("..") or stay ("."), and empty ones, are dropped; hidden is
// true when the entry belongs to metadata that is not worth importing
// (dotfiles, hidden folders, __MACOSX, Thumbs.db, desktop.ini). The result is
// only used to build asset and file names, never to address the disk.
func cleanZipPath(name string) (parts []string, hidden bool) {
	for _, p := range strings.Split(name, "/") {
		if p == "" || p == "." || p == ".." {
			continue
		}
		low := strings.ToLower(p)
		if strings.HasPrefix(p, ".") || low == "__macosx" || low == "thumbs.db" || low == "desktop.ini" {
			return nil, true
		}
		parts = append(parts, p)
	}
	return parts, false
}

// Validate normalises the options (default mode, tag names) and rejects
// unknown modes and oversized values.
func (o *ImportOptions) Validate() error { return validateImportOptions(o) }
