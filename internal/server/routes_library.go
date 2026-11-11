package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/toriath/hoard/internal/hoard"
)

// routesLibrary registers the library maintenance endpoints: similar and
// duplicate assets, and importing a zip archive as a pack. api requires a
// signed-in user or an API token; pub is only for signed, expiring links
// (verify the signature in the handler).
func (s *Server) routesLibrary(api, pub route) {
	api("GET /api/v1/assets/{id}/similar", s.handleSimilar)
	api("GET /api/v1/duplicates", s.handleDuplicates)
	api("POST /api/v1/import", s.handleImport)
	pub("PUT /api/v1/import/{filename}", s.handleSignedImport)
}

func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request) error {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.svc.FindSimilar(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (s *Server) handleDuplicates(w http.ResponseWriter, r *http.Request) error {
	groups, err := s.svc.FindDuplicates(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
	return nil
}

// handleImport takes multipart/form-data with an optional "options" part
// (JSON, must come first) and one "file" part holding the zip archive.
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) error {
	mr, err := r.MultipartReader()
	if err != nil {
		return &apiError{http.StatusBadRequest, "invalid", "expected multipart/form-data with a zip file part"}
	}
	var opts hoard.ImportOptions
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return &apiError{http.StatusBadRequest, "invalid", "upload was interrupted"}
		}
		switch {
		case part.FormName() == "options":
			dec := json.NewDecoder(io.LimitReader(part, 64<<10))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&opts); err != nil {
				return &apiError{http.StatusBadRequest, "invalid", "invalid options JSON: " + err.Error()}
			}
		case part.FileName() != "":
			return s.importArchive(w, r, part, part.FileName(), opts)
		}
	}
	return &apiError{http.StatusBadRequest, "invalid", `no "file" part in upload`}
}

// handleSignedImport is the target of the link get_import_command hands out:
// `curl -T pack.zip URL`. The signature covers the file name and the options.
func (s *Server) handleSignedImport(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	filename := r.PathValue("filename")
	tok := q.Get("o")
	if !s.svc.Verify("import:"+hoard.SanitizeFilename(filename)+":"+tok, q.Get("exp"), q.Get("sig")) {
		return &apiError{http.StatusForbidden, "forbidden", "import link is invalid or has expired; request a new one"}
	}
	opts, err := hoard.DecodeImportOptions(tok)
	if err != nil {
		return err
	}
	return s.importArchive(w, r, r.Body, filename, opts)
}

func (s *Server) importArchive(w http.ResponseWriter, r *http.Request, body io.Reader, name string, opts hoard.ImportOptions) error {
	res, err := s.svc.ImportZipReader(r.Context(), body, name, opts, s.cfg.MaxUploadBytes)
	if err != nil {
		return err
	}
	for _, a := range res.Assets {
		s.agent.AutoEnqueue(r.Context(), a.ID)
	}
	writeJSON(w, http.StatusCreated, res)
	return nil
}
