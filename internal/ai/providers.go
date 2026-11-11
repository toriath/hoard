package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// ---- Claude API ----

// supportsRefusalFallback lists models for which the API can transparently
// re-serve a safety-classifier refusal on another model.
func supportsRefusalFallback(model string) bool {
	for _, p := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5-1", "claude-fable-5-2"} {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}

// supportsEffort is false only for older models that reject output_config.effort.
func supportsEffort(model string) bool {
	return !strings.Contains(model, "-4-5") && !strings.HasPrefix(model, "claude-3")
}

func callAnthropic(ctx context.Context, set Settings, req *request) (string, error) {
	if set.APIKey == "" {
		return "", errors.New("no Claude API key configured")
	}
	opts := []option.RequestOption{option.WithAPIKey(set.APIKey), option.WithMaxRetries(2)}
	if set.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(set.BaseURL))
	}
	client := anthropic.NewClient(opts...)

	blocks := []anthropic.BetaContentBlockParamUnion{}
	if len(req.image) > 0 {
		blocks = append(blocks, anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
			Data:      base64.StdEncoding.EncodeToString(req.image),
			MediaType: anthropic.BetaBase64ImageSourceMediaType(req.imageMime),
		}))
	}
	blocks = append(blocks, anthropic.NewBetaTextBlock(req.text))

	params := anthropic.BetaMessageNewParams{
		Model:     set.Model,
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: req.system}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(blocks...)},
		// Structured output: the reply is guaranteed to match the schema.
		OutputConfig: anthropic.BetaOutputConfigParam{
			Format: anthropic.BetaJSONOutputFormatParam{Schema: outputSchema},
		},
	}
	if supportsEffort(set.Model) {
		// Cataloguing one asset is a small extraction task.
		params.OutputConfig.Effort = anthropic.BetaOutputConfigEffortLow
	}
	if supportsRefusalFallback(set.Model) {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}

	msg, err := client.Beta.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return "", fmt.Errorf("Claude API rejected the API key (HTTP %d)", apiErr.StatusCode)
			case http.StatusNotFound:
				return "", fmt.Errorf("Claude API does not know model %q", set.Model)
			case http.StatusTooManyRequests:
				return "", errors.New("Claude API rate limit reached, try again later")
			default:
				return "", fmt.Errorf("Claude API error (HTTP %d): %s", apiErr.StatusCode, apiErr.Error())
			}
		}
		return "", fmt.Errorf("Claude API unreachable: %w", err)
	}
	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return "", fmt.Errorf("the model declined to analyze this asset (%s)", msg.StopDetails.Category)
	case anthropic.BetaStopReasonMaxTokens:
		return "", errors.New("the model's answer was cut off")
	}
	var out strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			out.WriteString(t.Text)
		}
	}
	if out.Len() == 0 {
		return "", errors.New("the model returned no text")
	}
	return out.String(), nil
}

// ---- OpenAI-compatible servers (LM Studio, Ollama, llama.cpp, …) ----

var httpClient = &http.Client{Timeout: 5 * time.Minute}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature"`
	Stream         bool          `json:"stream"`
	ResponseFormat any           `json:"response_format,omitempty"`
}

// callOpenAI returns the reply and the model that produced it (which is
// discovered from the server when none is configured).
func callOpenAI(ctx context.Context, set Settings, req *request) (string, string, error) {
	model := set.Model
	if model == "" {
		var err error
		if model, err = firstModel(ctx, set); err != nil {
			return "", "", err
		}
	}
	var content any = req.text
	if len(req.image) > 0 {
		content = []map[string]any{
			{"type": "text", "text": req.text},
			{"type": "image_url", "image_url": map[string]any{"url": dataURL(req.imageMime, req.image)}},
		}
	}
	body := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: req.system + "\n\nAnswer with a single JSON object with the keys name, description, tags and type, and nothing else."},
			{Role: "user", Content: content},
		},
		Temperature: 0.2,
		ResponseFormat: map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "asset_metadata", "strict": true, "schema": outputSchema},
		},
	}
	out, status, err := postChat(ctx, set, body)
	if err != nil && status == http.StatusBadRequest {
		// Server without structured-output support: rely on the instruction.
		body.ResponseFormat = nil
		out, status, err = postChat(ctx, set, body)
	}
	if err != nil && status == http.StatusBadRequest && len(req.image) > 0 {
		// Text-only model: retry without the preview image.
		body.Messages[1].Content = req.text
		out, _, err = postChat(ctx, set, body)
	}
	return out, model, err
}

func postChat(ctx context.Context, set Settings, body chatRequest) (string, int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return "", 0, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, set.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", 0, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if set.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+set.APIKey)
	}
	resp, err := httpClient.Do(hreq)
	if err != nil {
		return "", 0, fmt.Errorf("cannot reach the model server at %s: %w", set.BaseURL, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("model server error (HTTP %d): %.300s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed.Choices) == 0 {
		return "", resp.StatusCode, fmt.Errorf("unexpected reply from model server: %.300s", strings.TrimSpace(string(data)))
	}
	return parsed.Choices[0].Message.Content, resp.StatusCode, nil
}

// firstModel asks the server which models it has and picks the first, so a
// local setup works without naming a model.
func firstModel(ctx context.Context, set Settings) (string, error) {
	models, err := ListModels(ctx, set)
	if err != nil {
		return "", err
	}
	if len(models) == 0 {
		return "", errors.New("the model server reports no loaded models")
	}
	return models[0], nil
}

// ListModels returns the model ids offered by an OpenAI-compatible server.
func ListModels(ctx context.Context, set Settings) ([]string, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, set.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if set.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+set.APIKey)
	}
	resp, err := httpClient.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the model server at %s: %w", set.BaseURL, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model server error (HTTP %d): %.300s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("unexpected reply from model server: %.300s", strings.TrimSpace(string(data)))
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		// Embedding models cannot chat.
		if !strings.Contains(strings.ToLower(m.ID), "embed") {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}
