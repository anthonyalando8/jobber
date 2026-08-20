package draft

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ClaudeProvider implements AIProvider using Anthropic's Claude API
type ClaudeProvider struct {
	apiKey string
	model  string
}

// NewClaudeProvider creates a new Claude provider
func NewClaudeProvider(apiKey, model string) *ClaudeProvider {
	if model == "" {
		model = "claude-sonnet-5"
	}
	return &ClaudeProvider{
		apiKey: apiKey,
		model:  model,
	}
}

// MinInterval — Claude's rate limits are generous enough for this workload's volume that no
// client-side pacing is needed.
func (c *ClaudeProvider) MinInterval() time.Duration { return 0 }

// claudeRequest is the request payload for Claude API
type claudeRequest struct {
	Model     string               `json:"model"`
	MaxTokens int                  `json:"max_tokens"`
	Messages  []claudeMessageInput `json:"messages"`
	System    string               `json:"system"`
}

type claudeMessageInput struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// claudeResponse is the response from Claude API
type claudeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Observe asks Claude for the section 7 observation/hypothesis JSON, grounded in req.Signals.
func (c *ClaudeProvider) Observe(ctx context.Context, req ObserveRequest) (*ObserveResult, error) {
	reqBody := claudeRequest{
		Model:     c.model,
		MaxTokens: 300,
		System:    systemPrompt,
		Messages: []claudeMessageInput{
			{Role: "user", Content: buildUserPrompt(req)},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var claudeResp claudeResponse
	if err := json.Unmarshal(body, &claudeResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if claudeResp.Error != nil {
		return nil, fmt.Errorf("Claude error: %s", claudeResp.Error.Message)
	}

	if len(claudeResp.Content) == 0 {
		return nil, fmt.Errorf("no content in response")
	}

	return parseObserveResponse(claudeResp.Content[0].Text)
}
