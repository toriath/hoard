package server

import "net/http"

// handlePutTechnical records attributes measured by the browser while it
// renders a preview. It is not an edit: no updated_at bump, no activity entry.
func (s *Server) handlePutTechnical(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Attrs map[string]any `json:"attrs"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	a, err := s.svc.SetTechnicalAttrs(r.Context(), r.PathValue("id"), in.Attrs)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a)
	return nil
}
