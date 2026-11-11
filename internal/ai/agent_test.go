package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/toriath/hoard/internal/config"
	"github.com/toriath/hoard/internal/hoard"
)

const answer = `{"name":"Gold Coin","description":"A shiny coin.","tags":["Coin","pixel art","pickup"],"type":"sprite"}`

func setup(t *testing.T) (*Agent, *hoard.Service, *hoard.Asset) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := hoard.Open(t.TempDir(), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	ctx := context.Background()
	a, err := svc.CreateAsset(ctx, hoard.AssetInput{AddTags: []string{"existing"}})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 8, 8)))
	if _, err := svc.AddFile(ctx, a.ID, "coin_gold.png", &buf, 0); err != nil {
		t.Fatal(err)
	}
	a, _ = svc.GetAsset(ctx, a.ID)
	return New(svc, config.Config{}, log), svc, a
}

func TestParseSuggestion(t *testing.T) {
	for _, raw := range []string{
		answer,
		"```json\n" + answer + "\n```",
		"<think>{\"not\": \"this\"}</think>\nSure! Here it is: " + answer + " Hope that helps.",
	} {
		s, err := parseSuggestion(raw)
		if err != nil || s.Name != "Gold Coin" || strings.Join(s.Tags, ",") != "coin,pixel-art,pickup" || s.Type != "sprite" {
			t.Errorf("parse(%q) = %+v, %v", raw, s, err)
		}
	}
	if _, err := parseSuggestion("I cannot help with that."); err == nil {
		t.Error("prose accepted as a suggestion")
	}
}

func TestOpenAICompatible(t *testing.T) {
	agent, svc, asset := setup(t)
	ctx := context.Background()
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"nomic-embed-text"},{"id":"qwen-vl"}]}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, body)
		// Behave like an older server: no structured output, no vision.
		if body["response_format"] != nil {
			http.Error(w, `{"error":"response_format unsupported"}`, http.StatusBadRequest)
			return
		}
		if _, multimodal := body["messages"].([]any)[1].(map[string]any)["content"].([]any); multimodal {
			http.Error(w, `{"error":"model does not support images"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "```json\n" + answer + "\n```"}}}})
	}))
	defer srv.Close()

	if _, err := agent.Suggest(ctx, asset.ID); err != ErrNotConfigured {
		t.Fatalf("unconfigured: %v", err)
	}
	if err := agent.SaveSettings(ctx, Settings{Provider: ProviderOpenAI, BaseURL: srv.URL + "/v1/", Vision: true}); err != nil {
		t.Fatal(err)
	}
	sug, err := agent.Suggest(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sug.Name != "Gold Coin" || sug.Model != "qwen-vl" {
		t.Errorf("suggestion = %+v", sug)
	}
	if len(calls) != 3 {
		t.Fatalf("expected graceful degradation over 3 calls, got %d", len(calls))
	}
	user := calls[0]["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if !strings.Contains(user[0].(map[string]any)["text"].(string), "coin_gold.png") || user[1].(map[string]any)["type"] != "image_url" {
		t.Errorf("first request lacks file context or preview: %v", user)
	}

	// Filling gaps keeps what a human set; overwrite replaces it.
	if _, err := svc.UpdateAsset(ctx, asset.ID, hoard.AssetInput{Description: ptr("Hand-written.")}); err != nil {
		t.Fatal(err)
	}
	got, err := agent.Apply(ctx, asset.ID, sug, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Gold Coin" || got.Description != "Hand-written." || strings.Join(got.Tags, ",") != "coin,existing,pickup,pixel-art" || got.Attrs["ai_enriched_by"] != "qwen-vl" {
		t.Errorf("fill = %+v", got)
	}
	got, _ = agent.Apply(ctx, asset.ID, sug, true)
	if got.Description != "A shiny coin." {
		t.Errorf("overwrite = %q", got.Description)
	}
}

func TestAnthropicRequest(t *testing.T) {
	agent, _, asset := setup(t)
	ctx := context.Background()
	var body map[string]any
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "stop_reason": "end_turn",
			"content": []any{map[string]any{"type": "thinking", "thinking": "", "signature": "x"}, map[string]any{"type": "text", "text": answer}},
			"usage":   map[string]any{"input_tokens": 10, "output_tokens": 10},
		})
	}))
	defer srv.Close()

	if err := agent.SaveSettings(ctx, Settings{Provider: ProviderAnthropic, BaseURL: srv.URL, APIKey: "sk-ant-test", Vision: true}); err != nil {
		t.Fatal(err)
	}
	sug, err := agent.Suggest(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sug.Name != "Gold Coin" || sug.Model != DefaultAnthropicModel {
		t.Errorf("suggestion = %+v", sug)
	}
	// The SDK decides how the key travels (x-api-key or bearer, by API version).
	sentKey := header.Get("X-Api-Key") == "sk-ant-test" || header.Get("Authorization") == "Bearer sk-ant-test"
	if !sentKey || !strings.Contains(header.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("headers = %v", header)
	}
	if body["model"] != "claude-opus-5-5" || body["fallbacks"] != "default" || body["thinking"] != nil || body["temperature"] != nil {
		t.Errorf("request = %v", body)
	}
	oc := body["output_config"].(map[string]any)
	if oc["effort"] != "low" || oc["format"].(map[string]any)["type"] != "json_schema" {
		t.Errorf("output_config = %v", oc)
	}
	content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["type"] != "image" || content[1].(map[string]any)["type"] != "text" {
		t.Errorf("content = %v", content)
	}

	// A refusal is reported, not parsed.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_2","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber"},"content":[],"usage":{"input_tokens":1,"output_tokens":0}}`)
	})
	if _, err := agent.Suggest(ctx, asset.ID); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Errorf("refusal: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

// A key from the environment is only used with the endpoint the environment
// configured, never with one entered in the UI.
func TestEnvKeyScope(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := hoard.Open(t.TempDir(), log)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ctx := context.Background()
	agent := New(svc, config.Config{AIProvider: ProviderAnthropic, AIKey: "sk-ant-from-env"}, log)

	if set, _ := agent.Settings(ctx); set.APIKey != "sk-ant-from-env" {
		t.Errorf("env key not used for the env provider: %+v", set)
	}
	for _, in := range []Settings{
		{Provider: ProviderAnthropic, BaseURL: "https://evil.example"},
		{Provider: ProviderOpenAI, BaseURL: "https://evil.example/v1"},
		{Provider: ProviderOpenAI},
	} {
		if err := agent.SaveSettings(ctx, in); err != nil {
			t.Fatal(err)
		}
		if set, _ := agent.Settings(ctx); set.APIKey != "" || set.HasAPIKey {
			t.Errorf("env key attached to %+v", in)
		}
	}
	// A key saved in the UI stays while the endpoint stays, and goes when it changes.
	_ = agent.SaveSettings(ctx, Settings{Provider: ProviderOpenAI, BaseURL: "http://lan:1234/v1", APIKey: "local-key"})
	_ = agent.SaveSettings(ctx, Settings{Provider: ProviderOpenAI, BaseURL: "http://lan:1234/v1/", Model: "m"})
	if set, _ := agent.Settings(ctx); set.APIKey != "local-key" {
		t.Error("key lost although the endpoint did not change")
	}
	_ = agent.SaveSettings(ctx, Settings{Provider: ProviderOpenAI, BaseURL: "http://other:1234/v1"})
	if set, _ := agent.Settings(ctx); set.APIKey != "" {
		t.Error("key followed a changed base URL")
	}
}
