package composition

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

//go:embed prompts/*.txt
var prompts embed.FS

type JSONProvider struct {
	Provider, Model string
	// Keys is a pool. GEMINI_API_KEY is documented in .env.example as a comma-separated pool
	// because Google's free-tier quota is per key, and the legacy provider already rotates it.
	// Passing the whole string as one credential authenticates against nothing.
	Keys   []string
	Client *http.Client

	mu   sync.Mutex
	next int
}

// key rotates through the pool so a multi-key quota is actually usable.
func (p *JSONProvider) key() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.Keys) == 0 {
		return ""
	}
	value := p.Keys[p.next%len(p.Keys)]
	p.next++
	return value
}

// splitKeys accepts either a single credential or a comma-separated pool.
func splitKeys(raw string) []string {
	keys := []string{}
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			keys = append(keys, trimmed)
		}
	}
	return keys
}

// NewConfigured initializes a v2 stage route without making a remote request.
// The web runtime shares these instances between its dashboard and explicit manual
// composition/review. Legacy drafting and the scheduler do not use these providers.
func NewConfigured(stage string) (*JSONProvider, error) {
	if stage != "COMPOSE" && stage != "REVIEW" {
		return nil, fmt.Errorf("unknown composition stage")
	}
	provider := strings.ToLower(os.Getenv("EMAIL_" + stage + "_PROVIDER"))
	if provider == "" {
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
			provider = "claude"
		} else {
			provider = "gemini"
		}
	}
	model := os.Getenv("EMAIL_" + stage + "_MODEL")
	keys := []string{}
	switch provider {
	case "claude":
		keys = splitKeys(os.Getenv("ANTHROPIC_API_KEY"))
		if model == "" {
			model = os.Getenv("ANTHROPIC_MODEL")
		}
	case "gemini":
		keys = splitKeys(os.Getenv("GEMINI_API_KEY"))
		if model == "" {
			model = os.Getenv("GEMINI_MODEL")
		}
	default:
		return nil, fmt.Errorf("unsupported %s provider", stage)
	}
	if len(keys) == 0 || model == "" {
		return nil, fmt.Errorf("configure %s provider credentials and an explicit model", stage)
	}
	return &JSONProvider{Provider: provider, Model: model, Keys: keys, Client: &http.Client{Timeout: 65 * time.Second}}, nil
}

func CompositionPrompt(intent string) (string, error) {
	common, err := prompts.ReadFile("prompts/common.txt")
	if err != nil {
		return "", err
	}
	switch intent {
	case "specific_job_application", "speculative_job_enquiry", "service_outreach":
	default:
		return "", fmt.Errorf("invalid composition intent")
	}
	specific, err := prompts.ReadFile("prompts/" + intent + ".txt")
	return string(common) + "\n\n" + string(specific), err
}
func (p *JSONProvider) Compose(ctx context.Context, request WriteRequest) (Email, Metadata, error) {
	var email Email
	if err := request.Snapshot.Validate(); err != nil {
		return email, Metadata{}, err
	}
	system, err := CompositionPrompt(string(request.Snapshot.Intent))
	if err != nil {
		return email, Metadata{}, err
	}
	raw, meta, err := p.call(ctx, system, request, PolicyVersion)
	if err == nil {
		err = Decode(raw, &email)
	}
	if err != nil && meta.ErrorCategory == "" {
		meta.ErrorCategory = "invalid_output"
	}
	return email, meta, err
}
func (p *JSONProvider) Review(ctx context.Context, s Snapshot, email Email) (Review, Metadata, error) {
	var review Review
	system, err := prompts.ReadFile("prompts/review.txt")
	if err != nil {
		return review, Metadata{}, err
	}
	// Only final text, immutable envelope and reference assertions, not composer reasoning.
	request := struct {
		Snapshot Snapshot `json:"snapshot"`
		Email    Email    `json:"email"`
	}{s, email}
	raw, meta, err := p.call(ctx, string(system), request, ReviewVersion)
	if err == nil {
		err = Decode(raw, &review)
	}
	if err == nil {
		err = ValidateReview(review)
	}
	if err != nil && meta.ErrorCategory == "" {
		meta.ErrorCategory = "invalid_review"
	}
	return review, meta, err
}

func (p *JSONProvider) call(ctx context.Context, system string, input any, version string) (raw []byte, meta Metadata, err error) {
	started := time.Now()
	meta = Metadata{Provider: p.Provider, Model: p.Model, PromptVersion: version, AttemptedAt: started.UTC()}
	meta.InputHash, meta.PromptHash = Hash(input), Hash(system)
	defer func() { meta.DurationMS = time.Since(started).Milliseconds() }()
	user, err := json.Marshal(input)
	if err != nil {
		return nil, meta, err
	}
	var payload any
	endpoint := ""
	switch p.Provider {
	case "claude":
		endpoint = "https://api.anthropic.com/v1/messages"
		payload = map[string]any{"model": p.Model, "max_tokens": 4096, "system": system, "messages": []any{map[string]any{"role": "user", "content": string(user)}}}
	case "gemini":
		endpoint = "https://generativelanguage.googleapis.com/v1beta/models/" + url.PathEscape(p.Model) + ":generateContent"
		payload = map[string]any{"systemInstruction": map[string]any{"parts": []any{map[string]string{"text": system}}}, "contents": []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": string(user)}}}}, "generationConfig": map[string]any{"responseMimeType": "application/json", "maxOutputTokens": 4096}}
	default:
		return nil, meta, fmt.Errorf("unsupported provider")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, meta, err
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 65 * time.Second}
	}
	// A rate-limited key is rotated past once, at most one full pass over the pool. Quota is
	// per key, so a pool with one exhausted key must not fail every other call. This is
	// rotation, not retry: a rejected request is not reissued against the same credential and
	// there is no sleeping loop.
	var envelope []byte
	attempts := len(p.Keys)
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, meta, err
		}
		req.Header.Set("Content-Type", "application/json")
		credential := p.key()
		if p.Provider == "claude" {
			req.Header.Set("x-api-key", credential)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else {
			req.Header.Set("x-goog-api-key", credential)
		}
		response, err := client.Do(req)
		if err != nil {
			meta.ErrorCategory = "transport"
			return nil, meta, fmt.Errorf("composition provider transport failed")
		}
		if response.StatusCode == http.StatusTooManyRequests && attempt+1 < attempts {
			response.Body.Close()
			meta.ErrorCategory = "rate_limited_rotated"
			continue
		}
		meta.RequestID = response.Header.Get("request-id")
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			meta.ErrorCategory = fmt.Sprintf("http_%d", response.StatusCode)
			return nil, meta, fmt.Errorf("composition provider returned HTTP %d", response.StatusCode)
		}
		envelope, err = io.ReadAll(io.LimitReader(response.Body, 256*1024+1))
		response.Body.Close()
		if err != nil || len(envelope) > 256*1024 {
			meta.ErrorCategory = "oversized_or_unreadable"
			return nil, meta, fmt.Errorf("provider envelope unreadable or too large")
		}
		meta.ErrorCategory = ""
		break
	}
	if len(envelope) == 0 {
		meta.ErrorCategory = "rate_limited"
		return nil, meta, fmt.Errorf("every provider credential is rate limited")
	}
	text := ""
	if p.Provider == "claude" {
		var parsed struct {
			ID         string          `json:"id"`
			Usage      json.RawMessage `json:"usage"`
			StopReason string          `json:"stop_reason"`
			Content    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err = json.Unmarshal(envelope, &parsed); err != nil {
			return nil, meta, fmt.Errorf("invalid provider envelope")
		}
		meta.RequestID = parsed.ID
		meta.Usage = parsed.Usage
		if parsed.StopReason != "end_turn" || len(parsed.Content) != 1 || parsed.Content[0].Type != "text" {
			meta.ErrorCategory = "incomplete"
			return nil, meta, fmt.Errorf("provider output incomplete or unexpected")
		}
		text = parsed.Content[0].Text
	} else {
		var parsed struct {
			Usage      json.RawMessage `json:"usageMetadata"`
			Candidates []struct {
				FinishReason string `json:"finishReason"`
				Content      struct {
					Parts []struct {
						Text    string `json:"text"`
						Thought bool   `json:"thought"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err = json.Unmarshal(envelope, &parsed); err != nil {
			return nil, meta, fmt.Errorf("invalid provider envelope")
		}
		if len(parsed.Candidates) != 1 || parsed.Candidates[0].FinishReason != "STOP" {
			meta.ErrorCategory = "incomplete"
			return nil, meta, fmt.Errorf("provider output incomplete")
		}
		meta.Usage = parsed.Usage
		for _, part := range parsed.Candidates[0].Content.Parts {
			if !part.Thought {
				text += part.Text
			}
		}
	}
	if text == "" || len(text) > MaxResponseBytes {
		meta.ErrorCategory = "invalid_output"
		return nil, meta, fmt.Errorf("provider text absent or exceeds limit")
	}
	meta.OutputText = text
	return []byte(text), meta, nil
}

// Prompt fingerprints make text-only prompt edits invalidate earlier reviews
// even if a developer forgets to increment the human-readable version.
func CurrentPromptHash(stage, intent string) string {
	if stage == "compose" {
		p, err := CompositionPrompt(intent)
		if err != nil {
			return ""
		}
		return Hash(p)
	}
	p, err := prompts.ReadFile("prompts/review.txt")
	if err != nil {
		return ""
	}
	return Hash(string(p))
}
