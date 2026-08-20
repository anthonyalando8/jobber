package draft

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// GeminiProvider implements AIProvider using Google's Gemini API, pooling one or more API keys.
// Google's free tier quota is per key (per Cloud project), so rotating across several keys
// multiplies real throughput rather than working around anything — it's just parallel accounts.
type GeminiProvider struct {
	apiKeys []string
	model   string
	next    atomic.Uint64
}

// geminiPerKeyInterval is a safe cadence for one key against the observed free-tier limit of 5
// requests/minute (one call every 12s = 5/min exactly; 13s leaves a small margin).
const geminiPerKeyInterval = 13 * time.Second

// NewGeminiProvider creates a new Gemini provider from a pool of one or more API keys.
// gemini-1.5-flash (the old default) has been retired by Google; gemini-3.5-flash is the current
// lightweight default as of August 2026 — set GEMINI_MODEL yourself if that's changed again by
// the time you read this.
func NewGeminiProvider(apiKeys []string, model string) *GeminiProvider {
	if model == "" {
		model = "gemini-3.5-flash"
	}
	return &GeminiProvider{
		apiKeys: apiKeys,
		model:   model,
	}
}

// nextKey rotates through the pool round-robin.
func (g *GeminiProvider) nextKey() string {
	i := g.next.Add(1) - 1
	return g.apiKeys[i%uint64(len(g.apiKeys))]
}

// MinInterval divides the safe per-key cadence across the pool — 4 keys means calls can go out
// roughly 4x as fast, since each one lands on a different key's independent quota.
func (g *GeminiProvider) MinInterval() time.Duration {
	n := len(g.apiKeys)
	if n <= 1 {
		return geminiPerKeyInterval
	}
	return geminiPerKeyInterval / time.Duration(n)
}

// geminiRequest is the request payload for Gemini API
type geminiRequest struct {
	Contents          []geminiContent `json:"contents"`
	SystemInstruction *struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"systemInstruction,omitempty"`
	GenerationConfig struct {
		Temperature     float32 `json:"temperature"`
		TopK            int     `json:"topK"`
		TopP            float32 `json:"topP"`
		MaxOutputTokens int     `json:"maxOutputTokens"`
		ThinkingConfig  struct {
			ThinkingBudget int `json:"thinkingBudget"`
		} `json:"thinkingConfig"`
	} `json:"generationConfig"`
}

type geminiContent struct {
	Role  string `json:"role"`
	Parts []struct {
		Text string `json:"text"`
	} `json:"parts"`
}

// geminiResponse is the response from Gemini API
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type       string `json:"@type"`
			RetryDelay string `json:"retryDelay"`
		} `json:"details"`
	} `json:"error,omitempty"`
}

// geminiMaxRetries bounds the sleeping retries after every key in the pool has been tried once
// and all came back rate-limited.
const geminiMaxRetries = 2

// Observe asks Gemini for the section 7 observation/hypothesis JSON, grounded in req.Signals.
// On a rate-limit response it first rotates to the next key in the pool with no delay — one
// key's quota being exhausted says nothing about the others' — and only falls back to sleeping
// once the whole pool has been tried.
func (g *GeminiProvider) Observe(ctx context.Context, req ObserveRequest) (*ObserveResult, error) {
	var lastErr error

	for i := 0; i < len(g.apiKeys); i++ {
		result, retryAfter, err := g.doObserve(ctx, req, g.nextKey())
		if err == nil {
			return result, nil
		}
		lastErr = err
		if retryAfter <= 0 {
			return nil, err // not a rate-limit error — no point trying another key
		}
	}

	// Every key in the pool was rate-limited — wait it out rather than fail the draft.
	for attempt := 0; attempt < geminiMaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(geminiPerKeyInterval):
		}
		result, retryAfter, err := g.doObserve(ctx, req, g.nextKey())
		if err == nil {
			return result, nil
		}
		lastErr = err
		if retryAfter <= 0 {
			return nil, err
		}
	}
	return nil, lastErr
}

// doObserve makes one attempt against a specific key. retryAfter > 0 means the caller should
// treat this as a rate-limit failure (try another key, or wait); it's 0 for any other error.
func (g *GeminiProvider) doObserve(ctx context.Context, req ObserveRequest, apiKey string) (*ObserveResult, time.Duration, error) {
	reqBody := geminiRequest{
		Contents: []geminiContent{
			{
				Role: "user",
				Parts: []struct {
					Text string `json:"text"`
				}{
					{Text: buildUserPrompt(req)},
				},
			},
		},
		SystemInstruction: &struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		}{
			Parts: []struct {
				Text string `json:"text"`
			}{
				{Text: systemPrompt},
			},
		},
	}

	reqBody.GenerationConfig.Temperature = 0.2
	reqBody.GenerationConfig.TopK = 40
	reqBody.GenerationConfig.TopP = 0.95
	// Generous relative to the <=25/<=20 word output this actually needs — leaves headroom so a
	// response never gets cut off mid-JSON, which showed up as a real parse failure in testing.
	reqBody.GenerationConfig.MaxOutputTokens = 800
	// Gemini 2.5+/3.x models spend part of maxOutputTokens on internal "thinking" before writing
	// the visible answer — that was silently eating the budget and truncating responses (and
	// plausibly causing the off-format reply seen in testing too). This task needs no reasoning,
	// just constrained extraction, so thinking is switched off entirely.
	reqBody.GenerationConfig.ThinkingConfig.ThinkingBudget = 0

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		g.model, apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests {
			var errResp geminiResponse
			if json.Unmarshal(body, &errResp) == nil && errResp.Error != nil {
				for _, d := range errResp.Error.Details {
					if delay, err := time.ParseDuration(d.RetryDelay); err == nil && delay > 0 {
						return nil, delay, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
					}
				}
			}
			// Rate-limited but no parseable delay — still worth trying the next key.
			return nil, 15 * time.Second, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
		}
		return nil, 0, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		return nil, 0, fmt.Errorf("unmarshal response: %w", err)
	}

	if geminiResp.Error != nil {
		return nil, 0, fmt.Errorf("Gemini error: %s", geminiResp.Error.Message)
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return nil, 0, fmt.Errorf("no content in response")
	}

	result, err := parseObserveResponse(geminiResp.Candidates[0].Content.Parts[0].Text)
	return result, 0, err
}
