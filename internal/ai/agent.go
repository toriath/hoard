// Package ai is hoard's built-in metadata agent. It looks at an asset (its
// preview image, source text and existing metadata) and proposes a name, a
// description, tags and a type, using either the Claude API or any
// OpenAI-compatible local server such as LM Studio.
package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/toriath/hoard/internal/config"
	"github.com/toriath/hoard/internal/hoard"
)

// Provider ids.
const (
	ProviderNone      = ""
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai" // OpenAI-compatible: LM Studio, Ollama, llama.cpp, vLLM…

	DefaultAnthropicModel = "claude-opus-5-5"
	DefaultOpenAIBaseURL  = "http://localhost:1234/v1" // LM Studio's default
)

// Settings configure the agent. They are stored in the database; environment
// variables only provide defaults.
type Settings struct {
	Provider     string `json:"provider"`
	BaseURL      string `json:"base_url"`
	Model        string `json:"model"`
	APIKey       string `json:"api_key,omitempty"`       // write-only over the API
	ClearAPIKey  bool   `json:"clear_api_key,omitempty"` // request only: forget the stored key
	HasAPIKey    bool   `json:"has_api_key"`
	AutoAnalyze  bool   `json:"auto_analyze"` // analyze new uploads automatically
	Vision       bool   `json:"vision"`       // send preview images to the model
	Instructions string `json:"instructions"` // house rules, e.g. tagging conventions
}

// Suggestion is the agent's proposed metadata for one asset.
type Suggestion struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Type        string   `json:"type"`
	Model       string   `json:"model,omitempty"` // model that produced the suggestion
}

// ErrNotConfigured is returned when no AI provider has been set up.
var ErrNotConfigured = errors.New("no AI provider configured (Settings → AI agent)")

type job struct {
	id        string
	overwrite bool
}

// Agent runs analyses, directly or through a background queue.
type Agent struct {
	svc *hoard.Service
	cfg config.Config
	log *slog.Logger

	queue   chan job
	mu      sync.Mutex
	pending map[string]bool // asset ids scheduled or queued
}

// New creates the agent. Call Run to start processing queued work.
func New(svc *hoard.Service, cfg config.Config, log *slog.Logger) *Agent {
	return &Agent{svc: svc, cfg: cfg, log: log, queue: make(chan job, 20000), pending: map[string]bool{}}
}

// Settings returns the effective settings (database over environment). The
// API key is included; callers must mask it before sending it anywhere.
func (a *Agent) Settings(ctx context.Context) (Settings, error) {
	s := Settings{Provider: a.cfg.AIProvider, BaseURL: a.cfg.AIBaseURL, Model: a.cfg.AIModel, Vision: true}
	raw, err := a.svc.GetSetting(ctx, "ai")
	if err != nil {
		return s, err
	}
	if raw != "" {
		var stored Settings
		if err := json.Unmarshal([]byte(raw), &stored); err == nil {
			s = stored
		}
	}
	s.BaseURL = effectiveBaseURL(s.Provider, s.BaseURL)
	if s.Provider == ProviderAnthropic && s.Model == "" {
		s.Model = DefaultAnthropicModel
	}
	// A key from the environment belongs to the endpoint the environment
	// names. It is never sent to a provider or URL chosen in the UI.
	if s.APIKey == "" && a.cfg.AIKey != "" && a.envKeyApplies(s) {
		s.APIKey = a.cfg.AIKey
	}
	s.HasAPIKey = s.APIKey != ""
	return s, nil
}

// effectiveBaseURL normalises a base URL and fills in the provider's default.
func effectiveBaseURL(provider, base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if provider == ProviderOpenAI && base == "" {
		return DefaultOpenAIBaseURL
	}
	return base
}

func (a *Agent) envKeyApplies(s Settings) bool {
	provider := a.cfg.AIProvider
	if provider == "" {
		provider = ProviderAnthropic // a bare ANTHROPIC_API_KEY
	}
	return s.Provider == provider && s.BaseURL == effectiveBaseURL(provider, a.cfg.AIBaseURL)
}

// CheckBaseURL validates the address of a model server. The server makes
// requests to it, so it is restricted to plain http(s) URLs without embedded
// credentials, and link-local addresses are refused: nothing a model server
// would live at, but where cloud metadata services do. Private LAN addresses
// must stay allowed, since that is where a local model runs.
func CheckBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return &hoard.InvalidError{Msg: "base_url must be an http(s) URL"}
	}
	if u.User != nil {
		return &hoard.InvalidError{Msg: "base_url must not contain credentials"}
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()) {
		return &hoard.InvalidError{Msg: "base_url must not be a link-local address"}
	}
	return nil
}

// SaveSettings validates and stores settings. An empty APIKey keeps the
// previously stored key, but only while provider and base URL stay the same:
// a key must never follow a changed endpoint, or anyone able to edit the
// settings could have it sent to a host of their choosing.
func (a *Agent) SaveSettings(ctx context.Context, in Settings) error {
	if !slices.Contains([]string{ProviderNone, ProviderAnthropic, ProviderOpenAI}, in.Provider) {
		return &hoard.InvalidError{Msg: `provider must be "", "anthropic" or "openai"`}
	}
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if err := CheckBaseURL(in.BaseURL); err != nil {
		return err
	}
	if len(in.Instructions) > 4000 {
		return &hoard.InvalidError{Msg: "instructions are too long (max 4000 characters)"}
	}
	if in.APIKey == "" && !in.ClearAPIKey {
		if raw, _ := a.svc.GetSetting(ctx, "ai"); raw != "" {
			var prev Settings
			if json.Unmarshal([]byte(raw), &prev) == nil && prev.Provider == in.Provider &&
				effectiveBaseURL(prev.Provider, prev.BaseURL) == effectiveBaseURL(in.Provider, in.BaseURL) {
				in.APIKey = prev.APIKey
			}
		}
	}
	in.Model = strings.TrimSpace(in.Model)
	in.HasAPIKey = false
	in.ClearAPIKey = false
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return a.svc.SetSetting(ctx, "ai", string(b))
}

// ---- prompt ----

const systemPrompt = `You catalogue assets in a game developer's personal asset library. For the asset you are shown, write the metadata that will let its owner (and their AI coding agents) find it again later.

Return:
- name: a short, human-readable title in Title Case, 2-6 words, describing what the asset is. No file extensions, no version numbers, no underscores.
- description: one to three plain sentences on what the asset shows or contains, its visual or sonic style, and what it would be good for in a game. Describe only what you can actually observe or reasonably infer from the provided information; do not invent details.
- tags: 5 to 12 lowercase kebab-case tags, most specific first. Cover subject (e.g. "sword", "forest"), style (e.g. "pixel-art", "low-poly", "hand-painted"), mood or genre (e.g. "sci-fi", "horror"), technical traits when visible (e.g. "tileable", "transparent", "looping", "animated", "top-down") and dominant colours when relevant. Reuse tags from the library's existing vocabulary whenever one fits instead of coining a near-duplicate.
- type: the single best category for the asset from the allowed list.

Audio, video and 3D files cannot be attached, so for those you may only receive file names, technical attributes and possibly a rendered preview image; rely on the file name and any existing metadata there and stay generic rather than guessing specifics.`

var outputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name":        map[string]any{"type": "string"},
		"description": map[string]any{"type": "string"},
		"tags":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"type":        map[string]any{"type": "string", "enum": hoard.TypeIDs()},
	},
	"required":             []string{"name", "description", "tags", "type"},
	"additionalProperties": false,
}

// request is the provider-neutral input for one analysis.
type request struct {
	system    string
	text      string
	image     []byte // optional preview
	imageMime string
}

const maxSourceChars = 24000

func (a *Agent) buildRequest(ctx context.Context, set Settings, as *hoard.Asset) (*request, error) {
	var b strings.Builder
	b.WriteString("Asset to catalogue:\n")
	fmt.Fprintf(&b, "- current name: %q", as.Name)
	if as.NameIsAuto() {
		b.WriteString(" (derived from the file name)")
	}
	fmt.Fprintf(&b, "\n- current type: %s\n", as.Type)
	if as.Description != "" {
		fmt.Fprintf(&b, "- current description: %s\n", as.Description)
	}
	if len(as.Tags) > 0 {
		fmt.Fprintf(&b, "- current tags: %s\n", strings.Join(as.Tags, ", "))
	}
	if as.Prompt != "" {
		fmt.Fprintf(&b, "- prompt it was generated with: %s\n", as.Prompt)
	}
	if as.Source != "" {
		fmt.Fprintf(&b, "- source: %s\n", as.Source)
	}
	b.WriteString("- files:\n")
	for _, f := range as.Files {
		fmt.Fprintf(&b, "  - %s (%s, %d bytes", f.Filename, f.Mime, f.Size)
		if f.Width > 0 {
			fmt.Fprintf(&b, ", %dx%d px", f.Width, f.Height)
		}
		b.WriteString(")\n")
	}
	if len(as.Attrs) > 0 {
		if j, err := json.Marshal(as.Attrs); err == nil {
			fmt.Fprintf(&b, "- technical attributes: %s\n", j)
		}
	}
	for _, c := range as.Collections {
		fmt.Fprintf(&b, "- in collection: %s (%s)\n", c.Name, c.Kind)
	}

	fmt.Fprintf(&b, "\nAllowed types: %s\n", describeTypes())
	if tags, err := a.svc.ListTags(ctx, "", 150); err == nil && len(tags) > 0 {
		names := make([]string, len(tags))
		for i, t := range tags {
			names[i] = t.Tag
		}
		fmt.Fprintf(&b, "\nExisting tag vocabulary of this library (reuse where fitting): %s\n", strings.Join(names, ", "))
	}

	req := &request{system: systemPrompt}
	if s := strings.TrimSpace(set.Instructions); s != "" {
		req.system += "\n\nHouse rules from the library owner:\n" + s
	}

	// Text assets (shaders, data files): include the source itself.
	if p := as.Primary(); p != nil && p.Kind == hoard.KindText {
		if fh, _, err := a.svc.OpenFile(ctx, as.ID, p.Filename); err == nil {
			src, _ := io.ReadAll(io.LimitReader(fh, maxSourceChars+1))
			fh.Close()
			note := ""
			if len(src) > maxSourceChars {
				src = src[:maxSourceChars]
				note = fmt.Sprintf(" (first %d bytes of %d)", maxSourceChars, p.Size)
			}
			fmt.Fprintf(&b, "\nContents of %s%s:\n```\n%s\n```\n", p.Filename, note, strings.ToValidUTF8(string(src), "?"))
		}
	}

	if set.Vision {
		if path, mime := a.svc.ThumbPath(as.ID); path != "" {
			if img, err := os.ReadFile(path); err == nil {
				req.image, req.imageMime = img, mime
				b.WriteString("\nThe attached image is a preview of the asset (for 3D models a render, for video a frame, for audio a waveform plot).\n")
			}
		}
	}
	req.text = b.String()
	return req, nil
}

func describeTypes() string {
	parts := make([]string, len(hoard.AssetTypes))
	for i, t := range hoard.AssetTypes {
		parts[i] = fmt.Sprintf("%s (%s)", t.ID, t.Description)
	}
	return strings.Join(parts, "; ")
}

// ---- analysis ----

// Suggest asks the configured model for metadata without changing anything.
func (a *Agent) Suggest(ctx context.Context, assetID string) (*Suggestion, error) {
	set, err := a.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if set.Provider == ProviderNone {
		return nil, ErrNotConfigured
	}
	as, err := a.svc.GetAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	req, err := a.buildRequest(ctx, set, as)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	sug, err := a.call(ctx, set, req)
	if err != nil {
		return nil, err
	}
	if !hoard.ValidType(sug.Type) {
		sug.Type = as.Type
	}
	return sug, nil
}

// call sends one request to the configured provider and parses the answer.
func (a *Agent) call(ctx context.Context, set Settings, req *request) (*Suggestion, error) {
	var raw string
	var err error
	model := set.Model
	switch set.Provider {
	case ProviderAnthropic:
		raw, err = callAnthropic(ctx, set, req)
	case ProviderOpenAI:
		raw, model, err = callOpenAI(ctx, set, req)
	default:
		return nil, ErrNotConfigured
	}
	if err != nil {
		return nil, err
	}
	sug, err := parseSuggestion(raw)
	if err != nil {
		return nil, err
	}
	sug.Model = model
	return sug, nil
}

// parseSuggestion extracts the JSON object from a model reply. Local models
// sometimes wrap it in prose, code fences or reasoning tags.
func parseSuggestion(raw string) (*Suggestion, error) {
	s := raw
	if i := strings.LastIndex(s, "</think>"); i >= 0 {
		s = s[i+len("</think>"):]
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("model did not return JSON: %.200s", strings.TrimSpace(raw))
	}
	var sug Suggestion
	if err := json.Unmarshal([]byte(s[start:end+1]), &sug); err != nil {
		return nil, fmt.Errorf("model returned invalid JSON: %w", err)
	}
	sug.Name = strings.TrimSpace(sug.Name)
	sug.Description = strings.TrimSpace(sug.Description)
	sug.Tags = hoard.NormalizeTags(sug.Tags)
	if len(sug.Tags) > 16 {
		sug.Tags = sug.Tags[:16]
	}
	sug.Type = strings.ToLower(strings.TrimSpace(sug.Type))
	return &sug, nil
}

// Apply writes a suggestion to the asset. Without overwrite only gaps are
// filled: tags are merged, the description is set if empty, and name/type are
// replaced only while they are still the automatic guesses.
func (a *Agent) Apply(ctx context.Context, assetID string, sug *Suggestion, overwrite bool) (*hoard.Asset, error) {
	as, err := a.svc.GetAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	in := hoard.AssetInput{AddTags: sug.Tags}
	if sug.Description != "" && (overwrite || as.Description == "") {
		in.Description = &sug.Description
	}
	if sug.Name != "" && (overwrite || as.NameIsAuto()) {
		in.Name = &sug.Name
	}
	if sug.Type != "" && sug.Type != as.Type && (overwrite || as.TypeIsAuto()) {
		in.Type = &sug.Type
	}
	if sug.Model != "" {
		in.Attrs = map[string]any{"ai_enriched_by": sug.Model}
	}
	return a.svc.UpdateAsset(ctx, assetID, in)
}

// Test checks that the configured provider answers.
func (a *Agent) Test(ctx context.Context) (string, error) {
	set, err := a.Settings(ctx)
	if err != nil {
		return "", err
	}
	if set.Provider == ProviderNone {
		return "", ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req := &request{
		system: systemPrompt,
		text:   "Asset to catalogue:\n- current name: \"sword_slash_01\" (derived from the file name)\n- current type: sfx\n- files:\n  - sword_slash_01.wav (audio/wav, 48213 bytes)\n\nAllowed types: " + describeTypes(),
	}
	sug, err := a.call(ctx, set, req)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s answered: %q, tags: %s", sug.Model, sug.Name, strings.Join(sug.Tags, ", ")), nil
}

// ---- queue ----

// Enqueue schedules background analysis for the given assets.
func (a *Agent) Enqueue(ctx context.Context, overwrite bool, delay time.Duration, ids ...string) int {
	n := 0
	for _, id := range ids {
		a.mu.Lock()
		dup := a.pending[id]
		a.pending[id] = true
		a.mu.Unlock()
		if dup {
			continue
		}
		_ = a.svc.SetAIStatus(ctx, id, "queued", "")
		j := job{id: id, overwrite: overwrite}
		push := func() {
			select {
			case a.queue <- j:
			default:
				a.done(j.id)
				_ = a.svc.SetAIStatus(context.Background(), j.id, "error", "analysis queue is full")
			}
		}
		if delay > 0 {
			time.AfterFunc(delay, push)
		} else {
			push()
		}
		n++
	}
	return n
}

// AutoEnqueue schedules analysis of a freshly uploaded asset if the user
// enabled automatic analysis. The short delay lets all files of a multi-file
// asset and the browser-rendered preview arrive first.
func (a *Agent) AutoEnqueue(ctx context.Context, id string) {
	set, err := a.Settings(ctx)
	if err != nil || set.Provider == ProviderNone || !set.AutoAnalyze {
		return
	}
	a.Enqueue(ctx, false, 12*time.Second, id)
}

func (a *Agent) done(id string) {
	a.mu.Lock()
	delete(a.pending, id)
	a.mu.Unlock()
}

// Run processes the queue until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) {
	// Resume work that was queued when the process last stopped.
	if ids, err := a.svc.PendingAI(ctx); err == nil && len(ids) > 0 {
		a.Enqueue(ctx, false, 0, ids...)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-a.queue:
					a.process(ctx, j)
				}
			}
		}()
	}
	wg.Wait()
}

func (a *Agent) process(ctx context.Context, j job) {
	defer a.done(j.id)
	as, err := a.svc.GetAsset(ctx, j.id)
	if err != nil || as.DeletedAt != nil {
		return
	}
	_ = a.svc.SetAIStatus(ctx, j.id, "running", "")
	sug, err := a.Suggest(ctx, j.id)
	if err == nil {
		_, err = a.Apply(ctx, j.id, sug, j.overwrite)
	}
	if ctx.Err() != nil {
		_ = a.svc.SetAIStatus(context.Background(), j.id, "queued", "") // resume after restart
		return
	}
	if err != nil {
		a.log.Warn("ai analysis failed", "asset", j.id, "err", err)
		msg := err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		_ = a.svc.SetAIStatus(ctx, j.id, "error", msg)
		return
	}
	_ = a.svc.SetAIStatus(ctx, j.id, "done", "")
}

func dataURL(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}
