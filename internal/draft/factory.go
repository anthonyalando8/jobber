package draft

import (
	"fmt"
	"os"
	"strings"
)

// NewProvider creates an AI provider based on environment configuration
// Priority: ANTHROPIC_API_KEY > GEMINI_API_KEY
// Falls back to configured model or defaults
func NewProvider() (AIProvider, error) {
	claudeKey := os.Getenv("ANTHROPIC_API_KEY")
	claudeModel := os.Getenv("ANTHROPIC_MODEL")
	geminiKeys := splitKeys(os.Getenv("GEMINI_API_KEY"))
	geminiModel := os.Getenv("GEMINI_MODEL")

	// Prefer Claude if available
	if claudeKey != "" {
		return NewClaudeProvider(claudeKey, claudeModel), nil
	}

	// Fall back to Gemini — GEMINI_API_KEY may hold one key or a comma-separated pool of
	// several, since Google's free-tier quota is per key and pooling multiplies throughput.
	if len(geminiKeys) > 0 {
		return NewGeminiProvider(geminiKeys, geminiModel), nil
	}

	return nil, fmt.Errorf("no AI provider configured: set ANTHROPIC_API_KEY or GEMINI_API_KEY")
}

// splitKeys parses a comma-separated key list, trimming whitespace and dropping empties.
func splitKeys(raw string) []string {
	var keys []string
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}
