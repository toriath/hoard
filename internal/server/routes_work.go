package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/toriath/hoard/internal/hoard"
)

// routesWork registers the REST endpoints of the asset request queue, review
// steering and notes. All of them need a signed-in user or an API token;
// everything that changes state is POST or PATCH.
func (s *Server) routesWork(api, pub route) {
	_ = pub
	api("GET /api/v1/requests", s.handleListRequests)
	api("POST /api/v1/requests", s.handleCreateRequest)
	api("GET /api/v1/requests/{id}", s.handleGetRequest)
	api("PATCH /api/v1/requests/{id}", s.handlePatchRequest)
	api("POST /api/v1/requests/{id}/cancel", s.handleCancelRequest)
	api("POST /api/v1/requests/{id}/claim", s.handleClaimRequest)
	api("POST /api/v1/requests/{id}/renew", s.handleRenewRequest)
	api("POST /api/v1/requests/{id}/release", s.handleReleaseRequest)
	api("POST /api/v1/requests/{id}/submit", s.handleSubmitRequest)
	api("POST /api/v1/requests/{id}/placeholder", s.handleRequestPlaceholder)
	api("POST /api/v1/requests/{id}/submissions/{asset}/accept", s.handleAcceptSubmission)
	api("POST /api/v1/requests/{id}/submissions/{asset}/reject", s.handleRejectSubmission)

	api("GET /api/v1/assets/{id}/review", s.handleGetReview)
	api("POST /api/v1/assets/{id}/review", s.handleReviewAsset)
	api("GET /api/v1/assets/{id}/notes", s.handleListNotes)
	api("POST /api/v1/assets/{id}/notes", s.handleAddNote)
	api("POST /api/v1/notes/{id}/resolve", s.handleResolveNote)
}

// listParam reads a repeated or comma-separated query parameter.
func listParam(r *http.Request, name string) []string {
	var out []string
	for _, v := range r.URL.Query()[name] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	reqs, err := s.svc.ListRequests(r.Context(), hoard.RequestQuery{
		Statuses: listParam(r, "status"), Project: q.Get("project"), Kind: q.Get("kind"), AssetTypes: listParam(r, "type"),
		ClaimedByMe: q.Get("mine") == "1", Target: q.Get("target"), Sort: q.Get("sort"), Limit: limit,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": reqs})
	return nil
}

func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) error {
	var in hoard.RequestInput
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	req, err := s.svc.CreateRequest(r.Context(), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, req)
	return nil
}

func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request) error {
	req, err := s.svc.GetRequest(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, req)
	return nil
}

func (s *Server) handlePatchRequest(w http.ResponseWriter, r *http.Request) error {
	var in hoard.RequestPatch
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	req, err := s.svc.UpdateRequest(r.Context(), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, req)
	return nil
}

func (s *Server) handleCancelRequest(w http.ResponseWriter, r *http.Request) error {
	req, err := s.svc.CancelRequest(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, req)
	return nil
}

type leaseBody struct {
	LeaseMinutes int `json:"lease_minutes"`
}

// optionalJSON reads an optional small JSON body.
func optionalJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return readJSONLimit(w, r, v, 64<<10)
}

func (s *Server) handleClaimRequest(w http.ResponseWriter, r *http.Request) error {
	var b leaseBody
	if err := optionalJSON(w, r, &b); err != nil {
		return err
	}
	req, err := s.svc.ClaimRequest(r.Context(), r.PathValue("id"), b.LeaseMinutes)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, req)
	return nil
}

func (s *Server) handleRenewRequest(w http.ResponseWriter, r *http.Request) error {
	var b leaseBody
	if err := optionalJSON(w, r, &b); err != nil {
		return err
	}
	req, err := s.svc.RenewClaim(r.Context(), r.PathValue("id"), b.LeaseMinutes)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, req)
	return nil
}

func (s *Server) handleReleaseRequest(w http.ResponseWriter, r *http.Request) error {
	req, err := s.svc.ReleaseClaim(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, req)
	return nil
}

func (s *Server) handleSubmitRequest(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		AssetIDs []string `json:"asset_ids"`
		Note     string   `json:"note"`
	}
	if err := readJSONLimit(w, r, &b, 64<<10); err != nil {
		return err
	}
	req, err := s.svc.SubmitRequest(r.Context(), r.PathValue("id"), b.AssetIDs, b.Note)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, req)
	return nil
}

func (s *Server) handleRequestPlaceholder(w http.ResponseWriter, r *http.Request) error {
	a, err := s.svc.MakePlaceholder(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a)
	return nil
}

func (s *Server) handleAcceptSubmission(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		Supersede *bool `json:"supersede"`
	}
	if err := optionalJSON(w, r, &b); err != nil {
		return err
	}
	req, err := s.svc.AcceptSubmission(r.Context(), r.PathValue("id"), r.PathValue("asset"), b.Supersede == nil || *b.Supersede)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, req)
	return nil
}

func (s *Server) handleRejectSubmission(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		Feedback string `json:"feedback"`
	}
	if err := readJSONLimit(w, r, &b, 64<<10); err != nil {
		return err
	}
	req, err := s.svc.RejectSubmission(r.Context(), r.PathValue("id"), r.PathValue("asset"), b.Feedback)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, req)
	return nil
}

// handleGetReview bundles what the asset drawer shows: status, notes and the
// requests the asset takes part in.
func (s *Server) handleGetReview(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	a, err := s.svc.GetAsset(r.Context(), id)
	if err != nil {
		return err
	}
	notes, err := s.svc.ListNotes(r.Context(), id, true)
	if err != nil {
		return err
	}
	reqs, err := s.svc.RequestsForAsset(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": a.Status, "superseded_by": a.SupersededBy, "notes": notes, "requests": reqs})
	return nil
}

func (s *Server) handleReviewAsset(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		Verdict   string `json:"verdict"`
		Feedback  string `json:"feedback"`
		Priority  int    `json:"priority"`
		ProjectID string `json:"project_id"`
	}
	if err := readJSONLimit(w, r, &b, 64<<10); err != nil {
		return err
	}
	res, err := s.svc.ReviewAsset(r.Context(), r.PathValue("id"), b.Verdict, b.Feedback, b.Priority, b.ProjectID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (s *Server) handleListNotes(w http.ResponseWriter, r *http.Request) error {
	notes, err := s.svc.ListNotes(r.Context(), r.PathValue("id"), r.URL.Query().Get("open") != "1")
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": notes})
	return nil
}

func (s *Server) handleAddNote(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		Kind string `json:"kind"`
		Body string `json:"body"`
	}
	if err := readJSONLimit(w, r, &b, 64<<10); err != nil {
		return err
	}
	n, err := s.svc.AddNote(r.Context(), r.PathValue("id"), b.Kind, b.Body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, n)
	return nil
}

func (s *Server) handleResolveNote(w http.ResponseWriter, r *http.Request) error {
	n, err := s.svc.ResolveNote(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, n)
	return nil
}
