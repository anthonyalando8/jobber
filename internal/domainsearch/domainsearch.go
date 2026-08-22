// Package domainsearch resolves a company's real domain via Google's Custom Search JSON API
// when name-guessing (internal/verify.CandidateDomains) has already been exhausted and the
// company is stuck at domain_status='failed'. Free HTML-scraping of DuckDuckGo/Bing was tried
// first and rejected — both blocked plain scraped requests in testing (DuckDuckGo returned an
// HTTP 202 challenge page, Bing silently redirected to unrelated generic results) — so this
// talks to Google's real API instead, which has a genuine 100-query/day free tier.
//
// review.md's "never trust a guessed domain" rule still applies here: a search hit is only a
// candidate. The caller must still run it through the existing verify.Verifier (which checks the
// company's own name actually appears on the page) before promoting it to "verified" — this
// package only ever proposes candidates, it never verifies them itself.
package domainsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Searcher queries Google Custom Search for candidate domains, rate-limited against a daily
// budget so a background worker can never blow through the free-tier quota unattended.
type Searcher struct {
	apiKey     string
	cx         string
	client     *http.Client
	redis      *redis.Client
	dailyLimit int
}

// NewSearcher creates a Searcher. apiKey/cx empty means "not configured" — Enabled() reports
// false and every caller is expected to skip this step gracefully, the same way the rest of this
// tool treats an unconfigured AI provider as a no-op rather than an error.
func NewSearcher(redisConn *redis.Client, apiKey, cx string, dailyLimit int) *Searcher {
	if dailyLimit <= 0 {
		dailyLimit = 90 // headroom under Google's 100/day free tier
	}
	return &Searcher{
		apiKey:     apiKey,
		cx:         cx,
		client:     &http.Client{Timeout: 10 * time.Second},
		redis:      redisConn,
		dailyLimit: dailyLimit,
	}
}

// Enabled reports whether a real API key/search engine id is configured.
func (s *Searcher) Enabled() bool { return s.apiKey != "" && s.cx != "" }

const pausedKey = "domainsearch:paused"

// IsPaused reports whether a human has hit the runtime kill-switch — separate from Enabled()
// (which is about whether an API key/cx even exist). Unlike an env var, this takes effect on the
// very next tick with no restart: pausing mid-day because a mistake was noticed (a wrong domain
// getting resolved, quota burning faster than expected) shouldn't require touching .env and
// restarting the process while the worker might tick again in the meantime.
func (s *Searcher) IsPaused(ctx context.Context) bool {
	exists, err := s.redis.Exists(ctx, pausedKey).Result()
	if err != nil {
		return false // fail open on a Redis hiccup — same posture as the rest of this package
	}
	return exists > 0
}

// Pause sets the runtime kill-switch — no TTL, stays paused until Resume is called explicitly.
func (s *Searcher) Pause(ctx context.Context) error {
	return s.redis.Set(ctx, pausedKey, 1, 0).Err()
}

// Resume clears the runtime kill-switch.
func (s *Searcher) Resume(ctx context.Context) error {
	return s.redis.Del(ctx, pausedKey).Err()
}

func dayKey() string { return "domainsearch:day:" + time.Now().Format("2006-01-02") }

// Allow reports whether today's free-tier query budget still has room, and if not, why. Checked
// by ResolveFailedDomains before every single company, so a pause set mid-run stops the very
// next iteration, not just the next tick.
func (s *Searcher) Allow(ctx context.Context) (bool, string) {
	if !s.Enabled() {
		return false, "GOOGLE_SEARCH_API_KEY/GOOGLE_SEARCH_CX not configured"
	}
	if s.IsPaused(ctx) {
		return false, "paused"
	}
	val, err := s.redis.Get(ctx, dayKey()).Result()
	if err == redis.Nil {
		return true, ""
	}
	if err != nil {
		return true, "" // fail open on a Redis hiccup, same as the AI budget's own usage() does
	}
	used, _ := strconv.Atoi(val)
	if used >= s.dailyLimit {
		return false, fmt.Sprintf("daily search budget used (%d/%d)", used, s.dailyLimit)
	}
	return true, ""
}

// Record marks that one query was just spent — call only after an actual API call, successful
// or not, since Google bills/counts the request either way.
func (s *Searcher) Record(ctx context.Context) {
	key := dayKey()
	count, err := s.redis.Incr(ctx, key).Result()
	if err == nil && count == 1 {
		s.redis.Expire(ctx, key, 24*time.Hour)
	}
}

func attemptedKey(companyID int64) string {
	return "domainsearch:attempted:" + strconv.FormatInt(companyID, 10)
}

// RecentlyAttempted reports whether this company was already searched and found nothing —
// callers should skip it rather than spend another query on the same stubborn company every
// tick. Mirrors internal/enrich's RecentlyAttempted/attemptCooldown pattern.
func (s *Searcher) RecentlyAttempted(ctx context.Context, companyID int64) bool {
	exists, err := s.redis.Exists(ctx, attemptedKey(companyID)).Result()
	if err != nil {
		return false // fail open: a Redis hiccup shouldn't block resolution
	}
	return exists > 0
}

// MarkAttempted notes that a search for this company came up with nothing verifiable — don't
// retry for a week; a company this hard to find by name isn't going to resolve differently
// tomorrow, and the quota is better spent on companies not yet tried.
func (s *Searcher) MarkAttempted(ctx context.Context, companyID int64) {
	_ = s.redis.Set(ctx, attemptedKey(companyID), 1, 7*24*time.Hour).Err()
}

// noiseHosts are directories/social/aggregator platforms that routinely outrank a small Kenyan
// business's own site in search results — never trust these as "the company's domain". Checked
// as a suffix match so a subdomain (m.facebook.com, en.wikipedia.org) is caught too.
var noiseHosts = []string{
	"facebook.com", "linkedin.com", "twitter.com", "x.com", "instagram.com", "youtube.com",
	"tiktok.com", "pinterest.com", "wikipedia.org", "google.com", "bing.com",
	"brightermonday.co.ke", "myjobmag.co.ke", "glassdoor.com", "indeed.com",
	"jobwebkenya.com", "opportunitiesforyoungkenyans.co.ke", "yellowpageskenya.com",
	"kenyaplex.com", "opencorporates.com", "crunchbase.com", "bloomberg.com",
}

func isNoiseHost(host string) bool {
	for _, n := range noiseHosts {
		if host == n || strings.HasSuffix(host, "."+n) {
			return true
		}
	}
	return false
}

type googleSearchResponse struct {
	Items []struct {
		Link string `json:"link"`
	} `json:"items"`
}

// Search returns ranked candidate domains for a company name — Kenya-scoped, with directory/
// social noise filtered out. The caller is responsible for verifying each one before trusting
// it; this is a proposal list, not a confirmed result.
func (s *Searcher) Search(ctx context.Context, companyName string) ([]string, error) {
	if !s.Enabled() {
		return nil, fmt.Errorf("domainsearch: not configured")
	}

	q := companyName + " Kenya official website"
	endpoint := "https://www.googleapis.com/customsearch/v1?key=" + url.QueryEscape(s.apiKey) +
		"&cx=" + url.QueryEscape(s.cx) + "&num=5&q=" + url.QueryEscape(q)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("google search HTTP %d: %s", resp.StatusCode, string(body))
	}

	var parsed googleSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	var candidates []string
	seen := map[string]bool{}
	for _, item := range parsed.Items {
		u, err := url.Parse(item.Link)
		if err != nil || u.Host == "" {
			continue
		}
		host := strings.ToLower(strings.TrimPrefix(u.Host, "www."))
		if isNoiseHost(host) || seen[host] {
			continue
		}
		seen[host] = true
		candidates = append(candidates, host)
	}
	return candidates, nil
}
