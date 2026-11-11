package server

import (
	"encoding/json"
	"net/http"

	"github.com/toriath/hoard/internal/hoard"
)

// routesBrief registers the REST endpoints for project briefs, fit checks,
// project reports and credits. api requires a signed-in user or an API token;
// pub is only for signed, expiring links (the handler verifies the signature).
func (s *Server) routesBrief(api, pub route) {
	api("GET /api/v1/projects/{id}/brief", s.handleGetBrief)
	api("PATCH /api/v1/projects/{id}/brief", s.handleSaveBrief)
	api("GET /api/v1/projects/{id}/report", s.handleProjectReport)
	api("GET /api/v1/projects/{id}/credits", s.handleCredits)
	api("GET /api/v1/projects/{id}/credits.md", s.handleCreditsMD)
	api("GET /api/v1/projects/{id}/bundle", s.handleBundle)
	// A fit check stores measured audio attributes, so it is a POST.
	api("POST /api/v1/assets/{id}/fit", s.handleFit)
	pub("GET /api/v1/dl-bundle/{id}", s.handleSignedBundle)
}

func (s *Server) handleGetBrief(w http.ResponseWriter, r *http.Request) error {
	b, err := s.svc.GetProjectBrief(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, b)
	return nil
}

// handleSaveBrief merges the JSON object in the body into the brief: fields
// present replace, null clears, absent fields stay.
func (s *Server) handleSaveBrief(w http.ResponseWriter, r *http.Request) error {
	var patch map[string]json.RawMessage
	if err := readJSONLimit(w, r, &patch, 64<<10); err != nil {
		return err
	}
	if patch == nil {
		return &apiError{http.StatusBadRequest, "invalid", "the body must be a JSON object"}
	}
	b, err := s.svc.SaveProjectBrief(r.Context(), r.PathValue("id"), patch)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, b)
	return nil
}

func (s *Server) handleProjectReport(w http.ResponseWriter, r *http.Request) error {
	rep, err := s.svc.ProjectReport(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rep)
	return nil
}

func (s *Server) handleCredits(w http.ResponseWriter, r *http.Request) error {
	c, err := s.svc.ProjectCredits(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}

func (s *Server) handleCreditsMD(w http.ResponseWriter, r *http.Request) error {
	c, err := s.svc.ProjectCredits(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="CREDITS.md"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(c.Markdown))
	return nil
}

// bundle streams a zip with one folder per asset of the project (rejected and
// superseded assets left out, like in CREDITS.md) plus CREDITS.md at the root.
func (s *Server) bundle(w http.ResponseWriter, r *http.Request, projectID string) error {
	p, err := s.svc.GetProject(r.Context(), projectID)
	if err != nil {
		return err
	}
	credits, err := s.svc.ProjectCredits(r.Context(), projectID)
	if err != nil {
		return err
	}
	all, err := s.svc.ProjectAssets(r.Context(), projectID)
	if err != nil {
		return err
	}
	assets := make([]*hoard.Asset, 0, len(all))
	for _, a := range all {
		if a.Status != "rejected" && a.Status != "superseded" {
			assets = append(assets, a)
		}
	}
	if len(assets) > 2000 {
		return &apiError{http.StatusBadRequest, "invalid", "too many assets for one archive (max 2000)"}
	}
	return s.writeZip(w, r, slug(p.Name)+"-assets.zip", assets, true, zipFile{Name: "CREDITS.md", Data: []byte(credits.Markdown)})
}

func (s *Server) handleBundle(w http.ResponseWriter, r *http.Request) error {
	return s.bundle(w, r, r.PathValue("id"))
}

func (s *Server) handleSignedBundle(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	q := r.URL.Query()
	if !s.svc.Verify("bundle:"+id, q.Get("exp"), q.Get("sig")) {
		return &apiError{http.StatusForbidden, "forbidden", "download link is invalid or has expired; request a new one"}
	}
	return s.bundle(w, r, id)
}

// handleFit checks an asset against one project (body {"project_id": "..."})
// or, without a body, against every project it is used in.
func (s *Server) handleFit(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		ProjectID string `json:"project_id"`
	}
	if r.ContentLength != 0 {
		if err := readJSONLimit(w, r, &in, 4<<10); err != nil {
			return err
		}
	}
	reports, err := s.svc.FitReports(r.Context(), r.PathValue("id"), in.ProjectID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": reports})
	return nil
}
