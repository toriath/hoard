package server

import (
	"encoding/json"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/toriath/hoard/internal/hoard"
)

// routesDelivery registers the REST endpoints for getting assets into a game
// repository: snapshot, audit, sync plan, lock file, atlases, derivatives and
// engine hints. api requires a signed-in user or an API token; pub is only for
// signed, expiring links (the handler verifies the signature).
func (s *Server) routesDelivery(api, pub route) {
	pub("PUT /api/v1/projects/{id}/repo/snapshot", s.handleRepoSnapshot)
	pub("GET /api/v1/projects/{id}/repo/lock-file", s.handleSignedLock)
	api("GET /api/v1/projects/{id}/repo/lock", s.handleLock)
	api("GET /api/v1/projects/{id}/repo/audit", s.handleRepoAudit)
	api("GET /api/v1/projects/{id}/repo/audit-command", s.handleAuditCommand)
	api("GET /api/v1/projects/{id}/repo/sync", s.handleSyncPlan)

	pub("GET /api/v1/atlas", s.handleSignedAtlas)
	api("GET /api/v1/atlas/pack", s.handleAtlas)
	pub("GET /api/v1/derive/{id}/{filename}", s.handleSignedDerive)
	api("GET /api/v1/engine-hints", s.handleEngineHints)
}

// ---- repository ----

func (s *Server) handleRepoSnapshot(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	q := r.URL.Query()
	if !s.svc.Verify("repo-snapshot:"+id, q.Get("exp"), q.Get("sig")) {
		return &apiError{http.StatusForbidden, "forbidden", "snapshot link is invalid or has expired; request a new audit command"}
	}
	// A snapshot is at most 8 MiB; allow a minute to receive it.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(time.Minute))
	defer func() { _ = rc.SetReadDeadline(time.Time{}) }()
	r.Body = http.MaxBytesReader(w, r.Body, 9<<20)
	ctx := hoard.WithActor(r.Context(), hoard.Actor{Username: "signed link", Via: "api"})
	res, err := s.svc.ReplaceRepoSnapshot(ctx, id, r.Body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (s *Server) writeLock(w http.ResponseWriter, r *http.Request, id string) error {
	lock, err := s.svc.BuildRepoLock(r.Context(), id)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "hoard.lock.json"}))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(append(b, '\n'))
	return nil
}

func (s *Server) handleSignedLock(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	q := r.URL.Query()
	if !s.svc.Verify("repo-lock:"+id, q.Get("exp"), q.Get("sig")) {
		return &apiError{http.StatusForbidden, "forbidden", "lock file link is invalid or has expired; request a new one"}
	}
	return s.writeLock(w, r, id)
}

func (s *Server) handleLock(w http.ResponseWriter, r *http.Request) error {
	return s.writeLock(w, r, r.PathValue("id"))
}

func (s *Server) handleRepoAudit(w http.ResponseWriter, r *http.Request) error {
	res, err := s.svc.AuditProject(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (s *Server) handleSyncPlan(w http.ResponseWriter, r *http.Request) error {
	plan, err := s.svc.PlanSync(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, plan)
	return nil
}

// handleAuditCommand returns the audit command for the browser. The command
// contains a signed upload link: treat the response like a credential (it is
// only handed to authenticated callers and is not cached).
func (s *Server) handleAuditCommand(w http.ResponseWriter, r *http.Request) error {
	var exts []string
	if v := r.URL.Query().Get("extensions"); v != "" {
		exts = strings.Split(v, ",")
	}
	cmd, err := s.svc.RepoAuditCommand(r.Context(), s.BaseURL(r), r.PathValue("id"), r.URL.Query().Get("root"), exts)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"command": cmd, "expires_in_seconds": int((2 * time.Hour).Seconds())})
	return nil
}

// ---- atlas ----

func (s *Server) writeAtlas(w http.ResponseWriter, r *http.Request) error {
	ids, opts, err := hoard.ParseAtlasQuery(r.URL.Query())
	if err != nil {
		return err
	}
	return s.sendAtlas(w, r, ids, opts)
}

func (s *Server) sendAtlas(w http.ResponseWriter, r *http.Request, ids []string, opts hoard.AtlasOptions) error {
	atlas, err := s.svc.PackAtlas(r.Context(), ids, opts)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "atlas.zip"}))
	w.Header().Set("Cache-Control", "no-store")
	return atlas.WriteZip(w)
}

// handleSignedAtlas verifies a signature that covers the asset ids and every
// option, as they are after normalisation.
func (s *Server) handleSignedAtlas(w http.ResponseWriter, r *http.Request) error {
	ids, opts, err := hoard.ParseAtlasQuery(r.URL.Query())
	q := r.URL.Query()
	if err != nil || !s.svc.Verify(hoard.AtlasPayload(ids, opts), q.Get("exp"), q.Get("sig")) {
		return &apiError{http.StatusForbidden, "forbidden", "atlas link is invalid or has expired; request a new one"}
	}
	return s.sendAtlas(w, r, ids, opts)
}

func (s *Server) handleAtlas(w http.ResponseWriter, r *http.Request) error { return s.writeAtlas(w, r) }

// ---- derivatives ----

func (s *Server) handleSignedDerive(w http.ResponseWriter, r *http.Request) error {
	id, filename := r.PathValue("id"), r.PathValue("filename")
	q := r.URL.Query()
	opts, err := hoard.ParseDeriveQuery(q)
	if err != nil || !s.svc.Verify("derive:"+id+":"+filename+":"+opts.Payload(), q.Get("exp"), q.Get("sig")) {
		return &apiError{http.StatusForbidden, "forbidden", "derivative link is invalid or has expired; request a new one"}
	}
	df, err := s.svc.Derive(r.Context(), id, filename, opts)
	if err != nil {
		return err
	}
	defer df.Close()
	mimeType := df.Mime
	fh, err := os.Open(df.Path)
	if err != nil {
		return err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", mimeType)
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "private, no-cache")
	clean := hoard.SanitizeFilename(filename)
	stem := strings.TrimSuffix(clean, filepath.Ext(clean))
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": stem + opts.DeriveExt()}))
	http.ServeContent(w, r, "", st.ModTime(), fh)
	return nil
}

// ---- engine hints ----

func (s *Server) handleEngineHints(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	ids, err := hoard.ParseIDList(q.Get("ids"), 100)
	if err != nil {
		return err
	}
	res, err := s.svc.EngineHints(r.Context(), ids, hoard.HintsOptions{Engine: q.Get("engine"), ProjectID: q.Get("project_id"), ResDir: q.Get("res_dir")})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}
