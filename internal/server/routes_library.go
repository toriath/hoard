package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

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

// maxImportAI is the most assets of one import the AI agent is queued for.
const maxImportAI = 50

// importLinks remembers the signatures of import links that were used, until
// they would have expired anyway.
var importLinks = &usedLinks{used: map[string]int64{}}

type usedLinks struct {
	mu   sync.Mutex
	used map[string]int64 // signature -> expiry (unix seconds)
}

// use records sig and reports false if it was already recorded.
func (u *usedLinks) use(sig string, exp int64) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now().Unix()
	for k, e := range u.used {
		if e < now {
			delete(u.used, k)
		}
	}
	if _, ok := u.used[sig]; ok {
		return false
	}
	u.used[sig] = exp
	return true
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
	sawOptions := false
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
			sawOptions = true
		case part.FileName() != "":
			// The file is processed as it streams in: options that follow it
			// could not be applied any more, so they must come first.
			if !sawOptions {
				return &apiError{http.StatusBadRequest, "invalid", `the "options" part (JSON, {} for the defaults) must come before the file`}
			}
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
	// One import per link: a replay would import the same pack again.
	exp, _ := strconv.ParseInt(q.Get("exp"), 10, 64)
	if !importLinks.use(q.Get("sig"), exp) {
		return &apiError{http.StatusConflict, "conflict", "this import link was already used; request a new one"}
	}
	return s.importArchive(w, r, r.Body, hoard.SanitizeFilename(filename), opts)
}

func (s *Server) importArchive(w http.ResponseWriter, r *http.Request, body io.Reader, name string, opts hoard.ImportOptions) error {
	res, err := s.svc.ImportZipReader(r.Context(), body, name, opts, s.cfg.MaxUploadBytes)
	if err != nil {
		return err
	}
	// A large pack would queue the AI agent for thousands of assets.
	if len(res.Assets) <= maxImportAI {
		for _, a := range res.Assets {
			s.agent.AutoEnqueue(r.Context(), a.ID)
		}
	} else {
		res.AINotQueued = len(res.Assets)
		res.Summary += " AI analysis was not queued for " + strconv.Itoa(res.AINotQueued) + " assets; use analyze_assets."
	}
	writeJSON(w, http.StatusCreated, res)
	return nil
}
