// Package server exposes hoard over HTTP: the JSON REST API under /api/v1,
// the MCP endpoint at /mcp and the embedded web UI everywhere else.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/toriath/hoard/internal/ai"
	"github.com/toriath/hoard/internal/config"
	"github.com/toriath/hoard/internal/hoard"
	"github.com/toriath/hoard/internal/mcpserver"
)

const sessionCookie = "hoard_session"

// Server wires the core service into HTTP handlers.
type Server struct {
	svc        *hoard.Service
	agent      *ai.Agent
	cfg        config.Config
	log        *slog.Logger
	version    string
	webFS      fs.FS
	logins     *limiter // failed sign-ins per client address + username
	loginsByIP *limiter // failed sign-ins per client address
	handler    http.Handler
}

// New builds the HTTP handler. webFS holds the built web UI.
func New(svc *hoard.Service, agent *ai.Agent, cfg config.Config, log *slog.Logger, version string, webFS fs.FS) *Server {
	s := &Server{svc: svc, agent: agent, cfg: cfg, log: log, version: version, webFS: webFS,
		logins: newLimiter(8, 10*time.Minute), loginsByIP: newLimiter(40, 10*time.Minute)}

	mux := http.NewServeMux()
	pub := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(h)) }
	api := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(s.requireUser(h))) }
	// Managing accounts, tokens and the AI provider needs a browser session:
	// a leaked API token must not be able to make itself permanent.
	session := func(pattern string, h handlerFunc) { mux.Handle(pattern, s.wrap(s.requireUser(requireSession(h)))) }

	pub("GET /healthz", s.handleHealth)
	pub("GET /api/v1/auth/status", s.handleAuthStatus)
	pub("POST /api/v1/auth/setup", s.handleSetup)
	pub("POST /api/v1/auth/login", s.handleLogin)
	api("POST /api/v1/auth/logout", s.handleLogout)
	session("POST /api/v1/auth/password", s.handlePassword)

	api("GET /api/v1/users", s.handleListUsers)
	session("POST /api/v1/users", s.handleCreateUser)
	session("DELETE /api/v1/users/{id}", s.handleDeleteUser)
	api("GET /api/v1/tokens", s.handleListTokens)
	session("POST /api/v1/tokens", s.handleCreateToken)
	session("DELETE /api/v1/tokens/{id}", s.handleDeleteToken)

	api("GET /api/v1/meta", s.handleMeta)
	api("GET /api/v1/stats", s.handleStats)
	api("GET /api/v1/tags", s.handleTags)
	api("POST /api/v1/tags/rename", s.handleRenameTag)

	api("GET /api/v1/assets", s.handleListAssets)
	api("POST /api/v1/assets", s.handleCreateAsset)
	api("POST /api/v1/assets/bulk", s.handleBulk)
	api("POST /api/v1/assets/analyze", s.handleAnalyze)
	api("GET /api/v1/assets/{id}", s.handleGetAsset)
	api("PATCH /api/v1/assets/{id}", s.handleUpdateAsset)
	api("DELETE /api/v1/assets/{id}", s.handleDeleteAsset)
	api("POST /api/v1/assets/{id}/restore", s.handleRestoreAsset)
	api("POST /api/v1/assets/{id}/suggest", s.handleSuggest)
	api("POST /api/v1/assets/{id}/files", s.handleAddFiles)
	api("PUT /api/v1/assets/{id}/files/{filename}", s.handlePutFile)
	api("DELETE /api/v1/assets/{id}/files/{fid}", s.handleDeleteFile)
	api("POST /api/v1/assets/{id}/files/{fid}/primary", s.handleSetPrimary)
	api("GET /api/v1/assets/{id}/raw/{filename}", s.handleRaw)
	api("GET /api/v1/assets/{id}/download", s.handleDownloadAsset)
	api("GET /api/v1/assets/{id}/thumbnail", s.handleGetThumb)
	api("PUT /api/v1/assets/{id}/thumbnail", s.handlePutThumb)
	api("DELETE /api/v1/assets/{id}/thumbnail", s.handleDeleteThumb)
	api("GET /api/v1/download", s.handleDownloadMany)

	api("GET /api/v1/collections", s.handleListCollections)
	api("POST /api/v1/collections", s.handleSaveCollection)
	api("GET /api/v1/collections/{id}", s.handleGetCollection)
	api("PATCH /api/v1/collections/{id}", s.handleSaveCollection)
	api("DELETE /api/v1/collections/{id}", s.handleDeleteCollection)
	api("GET /api/v1/projects", s.handleListProjects)
	api("POST /api/v1/projects", s.handleSaveProject)
	api("GET /api/v1/projects/{id}", s.handleGetProject)
	api("PATCH /api/v1/projects/{id}", s.handleSaveProject)
	api("DELETE /api/v1/projects/{id}", s.handleDeleteProject)

	api("GET /api/v1/settings/ai", s.handleGetAI)
	session("PUT /api/v1/settings/ai", s.handlePutAI)
	session("POST /api/v1/settings/ai/test", s.handleTestAI)
	session("POST /api/v1/settings/ai/models", s.handleAIModels)

	// Feature areas live in their own files; each registers its routes here.
	s.routesBrief(api)         // routes_brief.go
	s.routesWork(api)          // routes_work.go
	s.routesDelivery(api, pub) // routes_delivery.go (pub: signed links only)
	s.routesLibrary(api)       // routes_library.go
	s.routesHistory(api)       // routes_history.go

	// Signed, expiring links handed out to agents (no session or token needed).
	pub("GET /api/v1/dl/{id}/{filename}", s.handleSignedDownload)
	pub("GET /api/v1/dl-zip", s.handleSignedZip)
	pub("PUT /api/v1/upload/{id}/{filename}", s.handleSignedUpload)

	mux.Handle("/api/", s.wrap(func(w http.ResponseWriter, r *http.Request) error {
		return &apiError{http.StatusNotFound, "not_found", "no such API endpoint"}
	}))
	mux.Handle("/mcp", s.requireBearer(mcpserver.NewHandler(svc, agent, version, cfg.MaxUploadBytes)))
	mux.Handle("/", http.HandlerFunc(s.handleWeb))

	// Reject cross-site state-changing requests (CSRF) for cookie sessions.
	// Non-browser clients send neither Origin nor Sec-Fetch-Site and pass.
	s.handler = securityHeaders(http.NewCrossOriginProtection().Handler(mux))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// ---- plumbing ----

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// route registers a handler for a pattern such as "GET /api/v1/requests".
type route func(pattern string, h handlerFunc)

type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Message }

func (s *Server) wrap(h handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			s.writeError(w, r, err)
		}
	})
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	var inv *hoard.InvalidError
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &ae):
	case errors.As(err, &inv):
		ae = &apiError{http.StatusBadRequest, "invalid", inv.Msg}
	case errors.Is(err, hoard.ErrNotFound):
		ae = &apiError{http.StatusNotFound, "not_found", err.Error()}
	case errors.Is(err, hoard.ErrConflict):
		ae = &apiError{http.StatusConflict, "conflict", err.Error()}
	case errors.Is(err, hoard.ErrTooLarge), errors.As(err, &maxErr):
		ae = &apiError{http.StatusRequestEntityTooLarge, "too_large",
			fmt.Sprintf("upload exceeds the limit of %d MB per file", s.cfg.MaxUploadBytes>>20)}
	case errors.Is(err, ai.ErrNotConfigured):
		ae = &apiError{http.StatusBadRequest, "ai_not_configured", err.Error()}
	case errors.Is(err, context.Canceled):
		return // client went away
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		ae = &apiError{http.StatusInternalServerError, "internal", "internal server error"}
	}
	writeJSON(w, ae.Status, map[string]any{"error": ae})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// readJSON decodes a JSON body of at most 2 MiB.
func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return readJSONLimit(w, r, v, 2<<20)
}

// readJSONLimit decodes a JSON body of bounded size within a deadline, so that
// a client trickling in a half-finished body cannot pin memory indefinitely.
// (The server has no global read timeout because uploads may take long.)
func readJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer func() { _ = rc.SetReadDeadline(time.Time{}) }()
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return &apiError{http.StatusBadRequest, "invalid", "request body is empty"}
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &apiError{http.StatusRequestEntityTooLarge, "too_large", "request body is too large"}
		}
		return &apiError{http.StatusBadRequest, "invalid", "invalid JSON: " + err.Error()}
	}
	return nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// BaseURL returns the externally visible origin of this request, used to
// build links for agents. It follows the Host the client used, so the same
// instance works over LAN and tailnet alike.
func (s *Server) BaseURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	return scheme(r) + "://" + r.Host
}

func scheme(r *http.Request) string {
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return "https"
	}
	return "http"
}

// ---- authentication ----

type ctxKey int

const userKey ctxKey = 0

func userFrom(ctx context.Context) *hoard.User {
	u, _ := ctx.Value(userKey).(*hoard.User)
	return u
}

func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// authenticate resolves the caller from an API token or a session cookie.
func (s *Server) authenticate(r *http.Request) *hoard.User {
	if tok := bearer(r); tok != "" {
		if u, err := s.svc.TokenUser(r.Context(), tok); err == nil {
			return u
		}
		return nil
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if u, err := s.svc.SessionUser(r.Context(), c.Value); err == nil {
			return u
		}
	}
	return nil
}

// requireSession refuses API tokens on routes reserved for signed-in people.
func requireSession(h handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if bearer(r) != "" {
			return &apiError{http.StatusForbidden, "session_required",
				"this needs a signed-in browser session: API tokens cannot manage accounts, tokens or the AI provider"}
		}
		return h(w, r)
	}
}

func (s *Server) requireUser(h handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		u := s.authenticate(r)
		if u == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="hoard"`)
			return &apiError{http.StatusUnauthorized, "unauthorized", "authentication required: sign in, or send 'Authorization: Bearer <api token>'"}
		}
		via := "web"
		if bearer(r) != "" {
			via = "api"
		}
		return h(w, r.WithContext(withUser(r.Context(), u, via)))
	}
}

// withUser attaches the authenticated user and the matching activity actor.
func withUser(ctx context.Context, u *hoard.User, via string) context.Context {
	ctx = context.WithValue(ctx, userKey, u)
	return hoard.WithActor(ctx, hoard.Actor{UserID: u.ID, Username: u.Username, Via: via, TokenName: u.TokenName})
}

// requireBearer guards the MCP endpoint: API tokens only, never cookies.
func (s *Server) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		var u *hoard.User
		if tok != "" {
			u, _ = s.svc.TokenUser(r.Context(), tok)
		}
		if u == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="hoard"`)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": &apiError{Code: "unauthorized",
				Message: "MCP requires an API token: create one in the hoard web UI under Settings → API tokens and send it as 'Authorization: Bearer <token>'"}})
			return
		}
		// Tool handlers build links from this; never trust a client-sent value.
		r.Header.Set(mcpserver.BaseURLHeader, s.BaseURL(r))
		next.ServeHTTP(w, r.WithContext(withUser(r.Context(), u, "mcp")))
	})
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: scheme(r) == "https", MaxAge: int(hoard.SessionTTL.Seconds()),
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// credentialBytes caps the body of sign-in, setup and password requests.
const credentialBytes = 8 << 10

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) error {
	if _, err := s.svc.UserCount(r.Context()); err != nil {
		return err
	}
	// No version here: anonymous callers should not learn what to look up.
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	return nil
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) error {
	n, err := s.svc.UserCount(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"setup_required": n == 0, "user": s.authenticate(r)})
	return nil
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) error {
	var c credentials
	if err := readJSONLimit(w, r, &c, credentialBytes); err != nil {
		return err
	}
	u, err := s.svc.SetupFirstUser(r.Context(), c.Username, c.Password)
	if err != nil {
		return err
	}
	token, err := s.svc.NewSession(r.Context(), u.ID)
	if err != nil {
		return err
	}
	s.setSession(w, r, token)
	writeJSON(w, http.StatusCreated, map[string]any{"user": u})
	return nil
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) error {
	var c credentials
	if err := readJSONLimit(w, r, &c, credentialBytes); err != nil {
		return err
	}
	badCredentials := &apiError{http.StatusUnauthorized, "bad_credentials", "invalid username or password"}
	c.Username = strings.TrimSpace(c.Username)
	// Input that cannot belong to an account is refused before it reaches the
	// throttle, the log or the password hash.
	if !hoard.ValidUsername(c.Username) || len(c.Password) > hoard.MaxPasswordLen {
		return badCredentials
	}
	// Throttle per account and, more loosely, per client address (which also
	// covers attempts that spray many usernames). The attempt is counted
	// before the password is checked, so a parallel burst cannot slip through.
	ip := clientIP(r)
	key := ip + "|" + strings.ToLower(c.Username)
	if !s.loginsByIP.take(ip) || !s.logins.take(key) {
		return &apiError{http.StatusTooManyRequests, "rate_limited", "too many sign-in attempts, try again in a few minutes"}
	}
	token, u, err := s.svc.Login(r.Context(), c.Username, c.Password)
	if errors.Is(err, hoard.ErrBadCredentials) {
		s.log.Warn("failed sign-in", "username", c.Username, "ip", ip)
		return badCredentials
	}
	if err != nil {
		return err
	}
	s.logins.reset(key)
	s.loginsByIP.refund(ip)
	s.setSession(w, r, token)
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
	return nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.svc.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: scheme(r) == "https"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSONLimit(w, r, &in, credentialBytes); err != nil {
		return err
	}
	keep := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		keep = c.Value
	}
	// Guessing the current password through a stolen session is throttled too.
	key := "password|" + userFrom(r.Context()).ID
	if !s.logins.take(key) {
		return &apiError{http.StatusTooManyRequests, "rate_limited", "too many attempts, try again in a few minutes"}
	}
	err := s.svc.ChangePassword(r.Context(), userFrom(r.Context()).ID, in.Current, in.New, keep)
	if errors.Is(err, hoard.ErrBadCredentials) {
		return &apiError{http.StatusBadRequest, "invalid", "current password is incorrect"}
	}
	if err != nil {
		s.logins.refund(key) // only wrong guesses count, not a rejected new password
		return err
	}
	s.logins.reset(key)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) error {
	users, err := s.svc.ListUsers(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": users})
	return nil
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) error {
	var c credentials
	if err := readJSON(w, r, &c); err != nil {
		return err
	}
	u, err := s.svc.CreateUser(r.Context(), c.Username, c.Password)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, u)
	return nil
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteUser(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) error {
	tokens, err := s.svc.ListTokens(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tokens})
	return nil
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name string `json:"name"`
	}
	if err := readJSON(w, r, &in); err != nil {
		return err
	}
	t, secret, err := s.svc.CreateToken(r.Context(), userFrom(r.Context()).ID, in.Name)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": t, "secret": secret})
	return nil
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteToken(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

// ---- login rate limiting ----

type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*hit
}

type hit struct {
	n     int
	reset time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, hits: map[string]*hit{}}
}

// take counts one attempt and reports whether it is still within the limit.
// Counting on entry, rather than after a failure, means concurrent attempts
// cannot all pass the check before any of them is recorded.
func (l *limiter) take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	nowT := time.Now()
	if len(l.hits) > 10000 { // bound memory under a spray of keys
		for k, h := range l.hits {
			if nowT.After(h.reset) {
				delete(l.hits, k)
			}
		}
	}
	h := l.hits[key]
	if h == nil || nowT.After(h.reset) {
		h = &hit{reset: nowT.Add(l.window)}
		l.hits[key] = h
	}
	if h.n >= l.max {
		return false
	}
	h.n++
	return true
}

// refund gives one attempt back after a success.
func (l *limiter) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if h := l.hits[key]; h != nil && h.n > 0 {
		h.n--
	}
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}

// ---- web UI ----

// The app needs nothing beyond its own origin, except data: and blob: URLs
// for images and fetches, which the 3D loaders use for textures and buffers
// embedded in model files. Inline style attributes are set by the UI library.
const contentSecurityPolicy = "default-src 'self'; img-src 'self' data: blob:; connect-src 'self' data: blob:; " +
	"style-src 'self' 'unsafe-inline'; script-src 'self'; object-src 'none'; " +
	"frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// handleWeb serves the embedded single-page app; unknown paths fall back to
// index.html so that client-side routes survive a reload.
func (s *Server) handleWeb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if st, err := fs.Stat(s.webFS, name); err != nil || st.IsDir() {
		if strings.HasPrefix(name, "static/") { // a missing bundle file is a 404, not the app
			http.NotFound(w, r)
			return
		}
		name = "index.html"
		if _, err := fs.Stat(s.webFS, name); err != nil {
			http.Error(w, "The web UI is not part of this build. Run `make build` (or `npm run build` in web/) and restart.\nThe REST API and MCP endpoint work regardless.", http.StatusNotFound)
			return
		}
	}
	if name == "index.html" {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.HasPrefix(name, "static/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // content-hashed by the bundler
	}
	http.ServeFileFS(w, r, s.webFS, name)
}
