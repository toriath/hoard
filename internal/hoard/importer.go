package hoard

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
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
	Summary        string          `json:"summary"`
	Mode           string          `json:"mode"`
	CollectionID   string          `json:"collection_id,omitempty"`
	CollectionName string          `json:"collection_name,omitempty"`
	License        string          `json:"license,omitempty"`
	LicenseSource  string          `json:"license_source,omitempty"` // the file the licence was read from, or "options"
	Attribution    string          `json:"attribution,omitempty"`
	Assets         []ImportedAsset `json:"assets"`
	Files          int             `json:"files"`
	Skipped        int             `json:"skipped"`
	SkippedSample  []string        `json:"skipped_sample,omitempty"`
	Failed         []ImportFailure `json:"failed,omitempty"`
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
	limited := io.Reader(r)
	if maxBytes > 0 {
		limited = io.LimitReader(r, maxBytes+1)
	}
	n, err := io.Copy(tmp, limited)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("store upload: %w", err)
	}
	if maxBytes > 0 && n > maxBytes {
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
	lic, attr, src := s.detectPackLicense(entries)
	if res.License == "" && lic != "" {
		res.License, res.LicenseSource = lic, src
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
		if opts.Mode == ImportPerFile && root && reDocName.MatchString(strings.TrimSuffix(e.base(), filepath.Ext(e.base()))) {
			skip(strings.Join(e.parts, "/")) // describes the pack; its text goes into the collection
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

// ---- licence detection ----

// detectPackLicense reads the descriptive files in the archive's root and
// looks for a licence and an attribution line. LICENSE and COPYING files come
// first, then READMEs and credits, then any other .txt or .md file.
func (s *Service) detectPackLicense(entries []zipEntry) (license, attribution, source string) {
	type doc struct {
		e    zipEntry
		rank int
	}
	var docs []doc
	for _, e := range entries {
		if len(e.parts) != 1 {
			continue
		}
		stem := strings.TrimSuffix(e.base(), filepath.Ext(e.base()))
		low := strings.ToLower(e.base())
		switch {
		case reLicenseName.MatchString(stem):
			docs = append(docs, doc{e, 0})
		case reDocName.MatchString(stem):
			docs = append(docs, doc{e, 1})
		case strings.HasSuffix(low, ".txt") || strings.HasSuffix(low, ".md"):
			docs = append(docs, doc{e, 2})
		}
	}
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].rank < docs[j].rank })
	if len(docs) > 8 {
		docs = docs[:8]
	}
	for _, d := range docs {
		if d.e.f.UncompressedSize64 > 2<<20 {
			continue
		}
		rc, err := d.e.f.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(rc, importDocLimit))
		rc.Close()
		text := strings.ToValidUTF8(string(b), "")
		if license == "" {
			if l := DetectLicense(text); l != "" {
				license, source = l, d.e.base()
			}
		}
		if attribution == "" {
			attribution = DetectAttribution(text)
		}
		if license != "" && attribution != "" {
			break
		}
	}
	return license, attribution, source
}

var reMIT = regexp.MustCompile(`\bmit licen[sc]e\b`)

var (
	reCCURL   = regexp.MustCompile(`(?i)creativecommons\.org/(?:licenses|publicdomain)/([a-z\-]+)/(\d\.\d)`)
	reCCLong  = regexp.MustCompile(`(?i)creative\s+commons\s+attribution((?:[\s\-]*(?:non-?commercial|share-?alike|no-?deriv(?:ative)?s?))*)[\s\-]*(?:(?:license|licence)?[\s,]*)?(?:(?:version|v)?\s*)?(\d\.\d)?`)
	reCCShort = regexp.MustCompile(`(?i)\bcc[\s\-_]?by((?:[\s\-_]?(?:nc|sa|nd))*)[\s\-_]*v?(\d\.\d)?`)
)

// DetectLicense recognises common licences by their wording in a licence or
// readme text and returns an SPDX-style identifier, or "" when unsure.
func DetectLicense(text string) string {
	low := strings.ToLower(text)
	ccID := func(mods, ver string) string {
		id := "CC-BY"
		m := strings.ToLower(mods)
		for _, k := range []struct{ word, tag string }{{"noncommercial", "NC"}, {"non-commercial", "NC"}, {"nc", "NC"}, {"sharealike", "SA"}, {"share-alike", "SA"}, {"sa", "SA"}, {"noderiv", "ND"}, {"no-deriv", "ND"}, {"nd", "ND"}} {
			if containsWord(m, k.word) && !strings.Contains(id, "-"+k.tag) {
				id += "-" + k.tag
			}
		}
		if ver != "" {
			id += "-" + ver
		}
		return id
	}
	switch {
	case strings.Contains(low, "cc0") || strings.Contains(low, "creative commons zero") || strings.Contains(low, "cc-zero") ||
		strings.Contains(low, "creativecommons.org/publicdomain/zero"):
		return "CC0-1.0"
	}
	if m := reCCURL.FindStringSubmatch(text); m != nil {
		kind := strings.ToUpper(m[1])
		if kind == "ZERO" {
			return "CC0-1.0"
		}
		return "CC-" + kind + "-" + m[2]
	}
	if m := reCCLong.FindStringSubmatch(text); m != nil {
		return ccID(m[1], m[2])
	}
	if m := reCCShort.FindStringSubmatch(text); m != nil {
		return ccID(m[1], m[2])
	}
	switch {
	case strings.Contains(low, "sil open font license") || strings.Contains(low, "open font license"):
		return "OFL-1.1"
	case strings.Contains(low, "permission is hereby granted, free of charge") || reMIT.MatchString(low):
		return "MIT"
	case strings.Contains(low, "apache license") && strings.Contains(low, "2.0"):
		return "Apache-2.0"
	case strings.Contains(low, "public domain"):
		return "Public Domain"
	}
	return ""
}

func containsWord(hay, word string) bool {
	for i := 0; ; {
		j := strings.Index(hay[i:], word)
		if j < 0 {
			return false
		}
		a, b := i+j, i+j+len(word)
		isLetter := func(c byte) bool { return c >= 'a' && c <= 'z' }
		if (a == 0 || !isLetter(hay[a-1])) && (b == len(hay) || !isLetter(hay[b])) {
			return true
		}
		i = a + 1
	}
}

var reAttribution = regexp.MustCompile(`(?im)^[\s\-*#>]*((?:attribution|credits?|author|artist|created by|made by|copyright|\(c\)|©)\b.{0,200}|by\s+\S.{0,160})$`)

// DetectAttribution returns the first line of a text that reads like an
// attribution ("Created by …", "Author: …", "Copyright …", "by …").
func DetectAttribution(text string) string {
	for _, m := range reAttribution.FindAllStringSubmatch(text, 20) {
		line := strings.TrimSpace(m[1])
		// A bare label such as "Credits:" or "Attribution" says nothing.
		if i := strings.IndexAny(line, ":"); i >= 0 && strings.TrimSpace(line[i+1:]) == "" {
			continue
		}
		if len(strings.Fields(line)) < 2 {
			continue
		}
		return truncateRunes(line, 200)
	}
	return ""
}

// Validate normalises the options (default mode, tag names) and rejects
// unknown modes and oversized values.
func (o *ImportOptions) Validate() error { return validateImportOptions(o) }
