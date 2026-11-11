package server

import (
	"net/http"
	"strconv"

	"github.com/toriath/hoard/internal/hoard"
)

// routesHistory registers the activity feed, lineage and recipe endpoints.
func (s *Server) routesHistory(api, pub route) {
	_ = pub
	api("GET /api/v1/activity", s.handleActivity)
	api("GET /api/v1/assets/{id}/lineage", s.handleLineage)
	api("GET /api/v1/assets/{id}/recipe", s.handleRecipe)
}

// handleActivity: ?asset=&since=&after=&action=&via=&limit=. action and via
// take comma-separated lists; with after the oldest new entries come first.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	aq := hoard.ActivityQuery{
		AssetID: q.Get("asset"),
		Since:   q.Get("since"),
		Action:  q.Get("action"),
		Via:     q.Get("via"),
	}
	aq.AfterSeq, _ = strconv.ParseInt(q.Get("after"), 10, 64)
	aq.Limit, _ = strconv.Atoi(q.Get("limit"))
	feed, err := s.svc.ListActivityFeed(r.Context(), aq)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, feed)
	return nil
}

func (s *Server) handleLineage(w http.ResponseWriter, r *http.Request) error {
	l, err := s.svc.Lineage(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, l)
	return nil
}

func (s *Server) handleRecipe(w http.ResponseWriter, r *http.Request) error {
	rec, err := s.svc.GetRecipe(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rec)
	return nil
}
