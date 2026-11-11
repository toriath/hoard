package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/toriath/hoard/internal/ai"
	"github.com/toriath/hoard/internal/hoard"
)

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) error {
	set, err := s.agent.Settings(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":          s.version,
		"types":            hoard.AssetTypes,
		"licenses":         hoard.CommonLicenses,
		"collection_kinds": hoard.CollectionKinds,
		"project_statuses": hoard.ProjectStatuses,
		"max_upload_bytes": s.cfg.MaxUploadBytes,
		"trash_days":       s.cfg.TrashDays,
		"ai_enabled":       set.Provider != ai.ProviderNone,
		"user":             userFrom(r.Context()),
	})
	return nil
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) error {
	st, err := s.svc.GetStats(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, st)
	return nil
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) error {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tags, err := s.svc.ListTags(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tags})
	return nil
}

func (s *Server) handleRenameTag(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	n, err := s.svc.RenameTag(r.Context(), in.From, in.To)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"affected": n})
	return nil
}

// ---- list / read ----

func multi(r *http.Request, key string) []string {
	var out []string
	for _, v := range r.URL.Query()[key] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func parseListQuery(r *http.Request) hoard.ListQuery {
	q := r.URL.Query()
	lq := hoard.ListQuery{
		Q:          q.Get("q"),
		Types:      multi(r, "type"),
		Tags:       multi(r, "tag"),
		Collection: q.Get("collection"),
		Project:    q.Get("project"),
		License:    q.Get("license"),
		Statuses:   multi(r, "status"),
		Colors:     multi(r, "color"),
		Untagged:   q.Get("untagged") == "1" || q.Get("untagged") == "true",
		Trashed:    q.Get("trashed") == "1" || q.Get("trashed") == "true",
		IDs:        multi(r, "ids"),
		Sort:       q.Get("sort"),
	}
	lq.Limit, _ = strconv.Atoi(q.Get("limit"))
	lq.Offset, _ = strconv.Atoi(q.Get("offset"))
	switch strings.ToLower(q.Get("order")) {
	case "asc":
		f := false
		lq.Desc = &f
	case "desc":
		t := true
		lq.Desc = &t
	}
	return lq
}

func (s *Server) handleListAssets(w http.ResponseWriter, r *http.Request) error {
	res, err := s.svc.ListAssets(r.Context(), parseListQuery(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (s *Server) handleGetAsset(w http.ResponseWriter, r *http.Request) error {
	a, err := s.svc.GetAsset(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a)
	return nil
}

// ---- create / upload ----

type uploadResult struct {
	Asset      *hoard.Asset `json:"asset"`
	Duplicates []string     `json:"duplicate_of,omitempty"`
	SimilarTo  []string     `json:"similar_to,omitempty"` // near-identical pictures (perceptual hash)
}

// handleCreateAsset accepts either a JSON body (metadata only) or
// multipart/form-data with an optional "meta" JSON part followed by one or
// more "file" parts. Files are streamed straight to disk.
func (s *Server) handleCreateAsset(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "multipart/form-data" {
		var in hoard.AssetInput
		if err := readJSON(w, r, &in); err != nil {
			return err
		}
		a, err := s.svc.CreateAsset(ctx, in)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusCreated, uploadResult{Asset: a})
		return nil
	}

	mr, err := r.MultipartReader()
	if err != nil {
		return &apiError{http.StatusBadRequest, "invalid", "invalid multipart body"}
	}
	var in hoard.AssetInput
	var assetID string
	var dups, similar []string
	fail := func(err error) error {
		if assetID != "" { // do not leave a half-uploaded asset behind, even if the client is gone
			_, _ = s.svc.Bulk(context.WithoutCancel(ctx), hoard.BulkOp{IDs: []string{assetID}, Action: "purge"})
		}
		return err
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(&apiError{http.StatusBadRequest, "invalid", "upload was interrupted"})
		}
		switch {
		case part.FormName() == "meta":
			if assetID != "" {
				return fail(&apiError{http.StatusBadRequest, "invalid", `the "meta" part must come before the files`})
			}
			if err := json.NewDecoder(io.LimitReader(part, 2<<20)).Decode(&in); err != nil {
				return &apiError{http.StatusBadRequest, "invalid", "invalid meta JSON: " + err.Error()}
			}
		case part.FileName() != "":
			if assetID == "" {
				a, err := s.svc.CreateAsset(ctx, in)
				if err != nil {
					return err
				}
				assetID = a.ID
			}
			res, err := s.svc.AddFile(ctx, assetID, part.FileName(), part, s.cfg.MaxUploadBytes)
			if err != nil {
				return fail(err)
			}
			dups = append(dups, res.DuplicateOf...)
			similar = append(similar, res.SimilarTo...)
		}
	}
	if assetID == "" {
		return &apiError{http.StatusBadRequest, "invalid", `no "file" part in upload`}
	}
	a, err := s.svc.GetAsset(ctx, assetID)
	if err != nil {
		return err
	}
	s.agent.AutoEnqueue(ctx, assetID)
	writeJSON(w, http.StatusCreated, uploadResult{Asset: a, Duplicates: dups, SimilarTo: similar})
	return nil
}

func (s *Server) handleAddFiles(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id := r.PathValue("id")
	mr, err := r.MultipartReader()
	if err != nil {
		return &apiError{http.StatusBadRequest, "invalid", "expected multipart/form-data with file parts"}
	}
	n := 0
	var dups, similar []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return &apiError{http.StatusBadRequest, "invalid", "upload was interrupted"}
		}
		if part.FileName() == "" {
			continue
		}
		res, err := s.svc.AddFile(ctx, id, part.FileName(), part, s.cfg.MaxUploadBytes)
		if err != nil {
			return err
		}
		dups = append(dups, res.DuplicateOf...)
		similar = append(similar, res.SimilarTo...)
		n++
	}
	if n == 0 {
		return &apiError{http.StatusBadRequest, "invalid", `no "file" part in upload`}
	}
	a, err := s.svc.GetAsset(ctx, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, uploadResult{Asset: a, Duplicates: dups, SimilarTo: similar})
	return nil
}

// handlePutFile stores the raw request body as a file: `curl -T file URL`.
func (s *Server) handlePutFile(w http.ResponseWriter, r *http.Request) error {
	return s.putFile(w, r, r.PathValue("id"), r.PathValue("filename"))
}

func (s *Server) putFile(w http.ResponseWriter, r *http.Request, id, filename string) error {
	res, err := s.svc.AddFile(r.Context(), id, filename, r.Body, s.cfg.MaxUploadBytes)
	if err != nil {
		return err
	}
	s.agent.AutoEnqueue(r.Context(), id)
	writeJSON(w, http.StatusCreated, res)
	return nil
}

func (s *Server) handleSignedUpload(w http.ResponseWriter, r *http.Request) error {
	id, filename := r.PathValue("id"), r.PathValue("filename")
	q := r.URL.Query()
	// The signature covers the asset and the (sanitised) file name: a link is
	// good for writing exactly one named file, not for adding arbitrary ones.
	if !s.svc.Verify("upload:"+id+":"+hoard.SanitizeFilename(filename), q.Get("exp"), q.Get("sig")) {
		return &apiError{http.StatusForbidden, "forbidden", "upload link is invalid or has expired; request a new one"}
	}
	return s.putFile(w, r, id, filename)
}

// ---- update / delete ----

func (s *Server) handleUpdateAsset(w http.ResponseWriter, r *http.Request) error {
	var in hoard.AssetInput
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	a, err := s.svc.UpdateAsset(r.Context(), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a)
	return nil
}

func (s *Server) handleDeleteAsset(w http.ResponseWriter, r *http.Request) error {
	action := "trash"
	if r.URL.Query().Get("permanent") == "1" {
		action = "purge"
	}
	if _, err := s.svc.Bulk(r.Context(), hoard.BulkOp{IDs: []string{r.PathValue("id")}, Action: action}); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": action})
	return nil
}

func (s *Server) handleRestoreAsset(w http.ResponseWriter, r *http.Request) error {
	if _, err := s.svc.Bulk(r.Context(), hoard.BulkOp{IDs: []string{r.PathValue("id")}, Action: "restore"}); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

func (s *Server) handleBulk(w http.ResponseWriter, r *http.Request) error {
	var op hoard.BulkOp
	if err := readJSON(w, r, &op); err != nil {
		return err
	}
	n, err := s.svc.Bulk(r.Context(), op)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"affected": n})
	return nil
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteFile(r.Context(), r.PathValue("id"), r.PathValue("fid")); err != nil {
		return err
	}
	return s.handleGetAsset(w, r)
}

func (s *Server) handleSetPrimary(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.SetPrimaryFile(r.Context(), r.PathValue("id"), r.PathValue("fid")); err != nil {
		return err
	}
	return s.handleGetAsset(w, r)
}

// ---- file delivery ----

// serveFile streams a stored file with range support. User files are never
// allowed to act as documents of this origin: fixed content type, no
// sniffing, and a sandboxing CSP in case one is opened directly.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, id, filename string, attachment bool) error {
	fh, f, err := s.svc.OpenFile(r.Context(), id, filename)
	if err != nil {
		return err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", f.Mime)
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("ETag", `"`+f.SHA256[:32]+`"`)
	h.Set("Cache-Control", "private, no-cache")
	disp := "inline"
	if attachment {
		disp = "attachment"
	}
	h.Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": f.Filename}))
	http.ServeContent(w, r, "", st.ModTime(), fh)
	return nil
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) error {
	return s.serveFile(w, r, r.PathValue("id"), r.PathValue("filename"), r.URL.Query().Get("download") == "1")
}

func (s *Server) handleSignedDownload(w http.ResponseWriter, r *http.Request) error {
	id, filename := r.PathValue("id"), r.PathValue("filename")
	q := r.URL.Query()
	if !s.svc.Verify("download:"+id+":"+filename, q.Get("exp"), q.Get("sig")) {
		return &apiError{http.StatusForbidden, "forbidden", "download link is invalid or has expired; request a new one"}
	}
	return s.serveFile(w, r, id, filename, true)
}

func (s *Server) handleDownloadAsset(w http.ResponseWriter, r *http.Request) error {
	a, err := s.svc.GetAsset(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if len(a.Files) == 0 {
		return &apiError{http.StatusNotFound, "not_found", "asset has no files"}
	}
	if len(a.Files) == 1 {
		return s.serveFile(w, r, a.ID, a.Files[0].Filename, true)
	}
	return s.writeZip(w, r, slug(a.Name)+".zip", []*hoard.Asset{a}, false)
}

func (s *Server) handleDownloadMany(w http.ResponseWriter, r *http.Request) error {
	return s.zipByIDs(w, r, multi(r, "ids"))
}

func (s *Server) handleSignedZip(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	ids := multi(r, "ids")
	if !s.svc.Verify("zip:"+strings.Join(ids, ","), q.Get("exp"), q.Get("sig")) {
		return &apiError{http.StatusForbidden, "forbidden", "download link is invalid or has expired; request a new one"}
	}
	return s.zipByIDs(w, r, ids)
}

func (s *Server) zipByIDs(w http.ResponseWriter, r *http.Request, ids []string) error {
	if len(ids) == 0 {
		return &apiError{http.StatusBadRequest, "invalid", "ids is required"}
	}
	if len(ids) > 2000 {
		return &apiError{http.StatusBadRequest, "invalid", "too many assets for one archive (max 2000)"}
	}
	assets := make([]*hoard.Asset, 0, len(ids))
	for _, id := range ids {
		a, err := s.svc.GetAsset(r.Context(), id)
		if err != nil {
			return err
		}
		assets = append(assets, a)
	}
	name := "hoard-" + time.Now().Format("20060102-150405") + ".zip"
	return s.writeZip(w, r, name, assets, true)
}

var compressedExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".avif": true, ".mp3": true, ".ogg": true,
	".opus": true, ".flac": true, ".m4a": true, ".aac": true, ".mp4": true, ".webm": true, ".mov": true, ".mkv": true,
	".zip": true, ".glb": true, ".woff": true, ".woff2": true, ".ktx2": true, ".usdz": true,
}

// writeZip streams an archive. With folders, every asset gets its own
// directory containing its files and its asset.json metadata.
func (s *Server) writeZip(w http.ResponseWriter, r *http.Request, name string, assets []*hoard.Asset, folders bool) error {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	zw := zip.NewWriter(w)
	defer zw.Close()
	used := map[string]bool{}
	for _, a := range assets {
		prefix := ""
		if folders {
			dir := slug(a.Name)
			if used[dir] {
				dir += "-" + a.ID[len(a.ID)-6:]
			}
			used[dir] = true
			prefix = dir + "/"
			meta, _ := json.MarshalIndent(a, "", "  ")
			// The metadata must not collide with a user file of the same name.
			metaName := "asset.json"
			for _, f := range a.Files {
				if strings.EqualFold(f.Filename, metaName) {
					metaName = "asset.hoard.json"
				}
			}
			mw, err := zw.CreateHeader(&zip.FileHeader{Name: prefix + metaName, Method: zip.Deflate, Modified: time.Now()})
			if err != nil {
				return nil // headers are sent; nothing sensible left to report
			}
			_, _ = mw.Write(meta)
		}
		for _, f := range a.Files {
			if r.Context().Err() != nil {
				return nil
			}
			fh, err := os.Open(filepath.Join(s.svc.DataDir(), "assets", a.ID, "files", f.Filename))
			if err != nil {
				s.log.Warn("zip: missing file", "asset", a.ID, "file", f.Filename)
				continue
			}
			method := zip.Deflate
			if compressedExt[strings.ToLower(filepath.Ext(f.Filename))] {
				method = zip.Store
			}
			st, _ := fh.Stat()
			fw, err := zw.CreateHeader(&zip.FileHeader{Name: prefix + f.Filename, Method: method, Modified: st.ModTime()})
			if err == nil {
				_, err = io.Copy(fw, fh)
			}
			fh.Close()
			if err != nil {
				return nil
			}
		}
	}
	return nil
}

var reSlug = regexp.MustCompile(`[^\p{L}\p{N}._-]+`)

func slug(name string) string {
	s := strings.Trim(reSlug.ReplaceAllString(name, "-"), "-.")
	if len(s) > 80 {
		s = s[:80]
	}
	if s == "" {
		s = "asset"
	}
	return s
}

// ---- thumbnails ----

func (s *Server) handleGetThumb(w http.ResponseWriter, r *http.Request) error {
	path, ct := s.svc.ThumbPath(r.PathValue("id"))
	if path == "" {
		return &apiError{http.StatusNotFound, "not_found", "asset has no thumbnail"}
	}
	w.Header().Set("Content-Type", ct)
	if r.URL.Query().Has("v") {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	http.ServeFile(w, r, path)
	return nil
}

func (s *Server) handlePutThumb(w http.ResponseWriter, r *http.Request) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		return &apiError{http.StatusRequestEntityTooLarge, "too_large", "thumbnail must be smaller than 16 MB"}
	}
	if err := s.svc.SetThumbnail(r.Context(), r.PathValue("id"), data); err != nil {
		return err
	}
	return s.handleGetAsset(w, r)
}

func (s *Server) handleDeleteThumb(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteThumbnail(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	return s.handleGetAsset(w, r)
}

// ---- collections & projects ----

func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request) error {
	items, err := s.svc.ListCollections(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (s *Server) handleGetCollection(w http.ResponseWriter, r *http.Request) error {
	c, err := s.svc.GetCollection(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}

func (s *Server) handleSaveCollection(w http.ResponseWriter, r *http.Request) error {
	var in hoard.GroupInput
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	c, err := s.svc.SaveCollection(r.Context(), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}

func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteCollection(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) error {
	items, err := s.svc.ListProjects(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) error {
	p, err := s.svc.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, p)
	return nil
}

func (s *Server) handleSaveProject(w http.ResponseWriter, r *http.Request) error {
	var in hoard.GroupInput
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	p, err := s.svc.SaveProject(r.Context(), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, p)
	return nil
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteProject(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

// ---- AI agent ----

func (s *Server) handleGetAI(w http.ResponseWriter, r *http.Request) error {
	set, err := s.agent.Settings(r.Context())
	if err != nil {
		return err
	}
	set.APIKey = "" // never leaves the server
	writeJSON(w, http.StatusOK, set)
	return nil
}

func (s *Server) handlePutAI(w http.ResponseWriter, r *http.Request) error {
	var in ai.Settings
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	if err := s.agent.SaveSettings(r.Context(), in); err != nil {
		return err
	}
	return s.handleGetAI(w, r)
}

func (s *Server) handleTestAI(w http.ResponseWriter, r *http.Request) error {
	msg, err := s.agent.Test(r.Context())
	if err != nil {
		if errors.Is(err, ai.ErrNotConfigured) {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
	return nil
}

// handleAIModels lists the models of an OpenAI-compatible server, by default
// the configured one. It is a POST so that no cross-site navigation can make
// the server issue requests, and the stored key is only ever sent to the
// stored endpoint, never to a URL supplied with the request.
func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"` // a key just typed for this address, not yet saved
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	set, err := s.agent.Settings(r.Context())
	if err != nil {
		return err
	}
	if u := strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"); u != "" {
		if err := ai.CheckBaseURL(u); err != nil {
			return err
		}
		if u != set.BaseURL {
			set.APIKey = ""
		}
		set.BaseURL = u
	}
	if set.BaseURL == "" {
		set.BaseURL = ai.DefaultOpenAIBaseURL
		set.APIKey = ""
	}
	if in.APIKey != "" {
		set.APIKey = in.APIKey
	}
	models, err := ai.ListModels(r.Context(), set)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []string{}, "error": err.Error()})
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": models})
	return nil
}

func (s *Server) handleSuggest(w http.ResponseWriter, r *http.Request) error {
	sug, err := s.agent.Suggest(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ai.ErrNotConfigured) || isCoreError(err) {
			return err
		}
		return &apiError{http.StatusBadGateway, "ai_failed", err.Error()}
	}
	writeJSON(w, http.StatusOK, sug)
	return nil
}

func isCoreError(err error) bool {
	var inv *hoard.InvalidError
	return errors.Is(err, hoard.ErrNotFound) || errors.As(err, &inv)
}

func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		IDs       []string `json:"ids"`
		Overwrite bool     `json:"overwrite"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	set, err := s.agent.Settings(r.Context())
	if err != nil {
		return err
	}
	if set.Provider == ai.ProviderNone {
		return ai.ErrNotConfigured
	}
	for _, id := range in.IDs {
		if _, err := s.svc.GetAsset(r.Context(), id); err != nil {
			return err
		}
	}
	n := s.agent.Enqueue(r.Context(), in.Overwrite, 0, in.IDs...)
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": n})
	return nil
}
