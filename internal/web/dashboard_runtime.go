package web

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/anthonyalando8/prospector/internal/composition"
)

// This is the initialized v2 route used by both manual composition and dashboard.
// Credentials remain inside providers; only allowlisted descriptive fields reach the UI.
type compositionRuntime struct {
	Compose, Review       *composition.JSONProvider
	ComposeErr, ReviewErr error
	Hour, Day             int
	Enabled               bool
}

func loadCompositionRuntime() *compositionRuntime {
	c := &compositionRuntime{Enabled: os.Getenv("EMAIL_COMPOSITION_ENABLED") == "true"}
	c.Hour, _ = strconv.Atoi(os.Getenv("AI_REQUESTS_PER_HOUR"))
	c.Day, _ = strconv.Atoi(os.Getenv("AI_REQUESTS_PER_DAY"))
	c.Compose, c.ComposeErr = composition.NewConfigured("COMPOSE")
	c.Review, c.ReviewErr = composition.NewConfigured("REVIEW")
	return c
}

var runtimeMu sync.Mutex

func (h *Handler) compositionConfig() *compositionRuntime {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if h.compositionRuntime == nil {
		h.compositionRuntime = loadCompositionRuntime()
	}
	return h.compositionRuntime
}
func (c *compositionRuntime) limits() (int, int) { return c.Hour, c.Day }
func (c *compositionRuntime) composer() (*composition.JSONProvider, error) {
	if c.Compose == nil {
		return nil, fmt.Errorf("composition provider unavailable")
	}
	return c.Compose, nil
}
func (c *compositionRuntime) reviewer() (*composition.JSONProvider, error) {
	if c.Review == nil {
		return nil, fmt.Errorf("review provider unavailable")
	}
	return c.Review, nil
}
func providerName(p *composition.JSONProvider) string {
	if p == nil {
		return "Not configured"
	}
	switch p.Provider {
	case "gemini":
		return "Google Gemini"
	case "claude":
		return "Anthropic Claude"
	}
	return p.Provider
}
