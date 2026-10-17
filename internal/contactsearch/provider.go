package contactsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SearchResult is one public search hit, in the shape every provider must produce. It is
// deliberately the raw hit and nothing more: a title, a link, whatever summary the engine chose
// to show, who said so and when. Interpreting it is the pipeline's job, not the provider's.
type SearchResult struct {
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	Snippet    string    `json:"snippet"`
	Provider   string    `json:"provider"`
	ObservedAt time.Time `json:"observed_at"`
	Rank       int       `json:"rank"`
}

// SearchProvider is the whole surface the pipeline uses to reach a search engine. Nothing above
// this interface knows which engine answered, so replacing the engine — or running without one —
// changes one constructor call and no logic.
//
// Enabled reporting false is an ordinary state, not an error: the highest-trust evidence in this
// pipeline comes from the job posting and the employer's own careers page, both of which are
// reached by direct fetch. A missing search engine narrows what can be discovered; it does not
// stop discovery.
type SearchProvider interface {
	Name() string
	Enabled() bool
	// Search returns at most limit results, or an error. An error is a transient condition the
	// caller records and retries under a bounded schedule; it is never a reason to fabricate a
	// result.
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
}

// NullProvider is the provider used when no search engine is configured. It is not an error
// type: it answers honestly that it is not enabled, and every caller already handles that.
type NullProvider struct{}

func (NullProvider) Name() string  { return "none" }
func (NullProvider) Enabled() bool { return false }
func (NullProvider) Search(context.Context, string, int) ([]SearchResult, error) {
	return nil, fmt.Errorf("contactsearch: no search provider configured")
}

// GoogleProvider queries Google's Custom Search JSON API. It is one implementation of
// SearchProvider chosen because the project already holds credentials for it (see
// internal/domainsearch, which uses the same API for a different question); nothing in this
// package depends on it being the one in use.
//
// It does not enforce a budget itself. Budget is a property of the contact-discovery pipeline as
// a whole — pages fetched and companies examined count too — so it lives in Budget rather than
// being scattered across providers that would each keep their own idea of it.
type GoogleProvider struct {
	apiKey, cx string
	client     *http.Client
}

// NewGoogleProvider builds a Google-backed provider. Empty credentials produce a provider that
// reports Enabled() false, which is the same graceful no-op the rest of this codebase uses for
// an unconfigured AI provider.
func NewGoogleProvider(apiKey, cx string) *GoogleProvider {
	return &GoogleProvider{
		apiKey: strings.TrimSpace(apiKey),
		cx:     strings.TrimSpace(cx),
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (g *GoogleProvider) Name() string  { return "google_cse" }
func (g *GoogleProvider) Enabled() bool { return g != nil && g.apiKey != "" && g.cx != "" }

type googleResponse struct {
	Items []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
	} `json:"items"`
}

// Search runs one query. Results are returned exactly as the engine ranked them; this method
// filters nothing, because deciding that a hit is noise requires knowing which company is being
// researched, which is the pipeline's knowledge rather than the provider's.
func (g *GoogleProvider) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if !g.Enabled() {
		return nil, fmt.Errorf("contactsearch: google provider not configured")
	}
	if limit < 1 || limit > 10 {
		limit = 5
	}
	endpoint := "https://www.googleapis.com/customsearch/v1?key=" + url.QueryEscape(g.apiKey) +
		"&cx=" + url.QueryEscape(g.cx) + "&num=" + fmt.Sprint(limit) + "&q=" + url.QueryEscape(query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		// The key is in the request, never in the message.
		return nil, fmt.Errorf("search provider HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed googleResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	observed := time.Now().UTC()
	out := make([]SearchResult, 0, len(parsed.Items))
	for i, item := range parsed.Items {
		if strings.TrimSpace(item.Link) == "" {
			continue
		}
		out = append(out, SearchResult{
			Title: strings.TrimSpace(item.Title), URL: strings.TrimSpace(item.Link),
			Snippet: strings.TrimSpace(item.Snippet), Provider: g.Name(),
			ObservedAt: observed, Rank: i + 1,
		})
	}
	return out, nil
}

// StaticProvider replays a fixed result set. It exists so the pipeline's behaviour against a
// search engine can be tested exactly — including the engine failing — without a network call
// and without the tests depending on what a real engine happens to return today.
type StaticProvider struct {
	Results map[string][]SearchResult
	Err     error
	Off     bool
	Queries []string
}

func (s *StaticProvider) Name() string  { return "static" }
func (s *StaticProvider) Enabled() bool { return !s.Off }

func (s *StaticProvider) Search(_ context.Context, query string, limit int) ([]SearchResult, error) {
	s.Queries = append(s.Queries, query)
	if s.Err != nil {
		return nil, s.Err
	}
	results := s.Results[query]
	if results == nil {
		// Fall back to a substring match so a test can key on the distinctive part of a query
		// without restating the whole generated string.
		for key, value := range s.Results {
			if strings.Contains(query, key) {
				results = value
				break
			}
		}
	}
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	observed := time.Now().UTC()
	out := make([]SearchResult, 0, len(results))
	for i, r := range results {
		r.Provider, r.Rank = s.Name(), i+1
		if r.ObservedAt.IsZero() {
			r.ObservedAt = observed
		}
		out = append(out, r)
	}
	return out, nil
}
