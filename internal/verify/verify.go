// Package verify decides whether a company's domain is trustworthy enough to enrich.
// review.md §10: never let a guessed domain ("Acme Ltd" -> "acme-ltd.com") proceed straight
// into deep enrichment — a wrong domain silently poisons every signal/contact/draft that
// follows it. Every domain a seeder attaches to a company starts as "candidate" (guessed) or
// "manual" (a human typed it, trusted immediately) and only "candidate" needs a Verifier.
package verify

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

// Status is where a domain sits in the verification state machine.
type Status string

const (
	StatusUnknown   Status = "unknown"
	StatusCandidate Status = "candidate"
	StatusVerified  Status = "verified"
	StatusFailed    Status = "failed"
	StatusManual    Status = "manual"
)

// Company is the minimal shape a Verifier needs — kept independent of internal/store so this
// package has no dependency on the store or seed packages.
type Company struct {
	Name   string
	Domain string
}

// Verifier decides whether a candidate domain actually belongs to the named company.
// Implementations must never error out on an ordinary "couldn't confirm it" outcome — that's
// StatusFailed, not an error. A returned error means verification itself broke (bad request
// construction, etc.), not that the domain looked wrong.
type Verifier interface {
	Verify(ctx context.Context, company Company) (Status, float64, error)
}

// HeuristicVerifier is the "no search API configured" fallback review.md §10 asks for:
// fetch the candidate domain directly and check whether the company's own name shows up on
// its homepage. Cheap, no external quota, and conservative — a mismatch or a dead site both
// come back StatusFailed (visible for manual review), never silently discarded.
type HeuristicVerifier struct {
	client    *http.Client
	userAgent string
}

// NewHeuristicVerifier creates the default verifier.
func NewHeuristicVerifier(userAgent string) *HeuristicVerifier {
	return &HeuristicVerifier{
		client:    &http.Client{Timeout: 8 * time.Second},
		userAgent: userAgent,
	}
}

// maxBodyBytes bounds how much of a homepage we read — a name match is either near the top
// (title, hero) or not meaningfully there at all; no need to pull a whole large page.
const maxBodyBytes = 256 * 1024

func (v *HeuristicVerifier) Verify(ctx context.Context, company Company) (Status, float64, error) {
	url := company.Domain
	if !strings.Contains(url, "://") {
		url = "https://" + url
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return StatusFailed, 0, nil
	}
	req.Header.Set("User-Agent", v.userAgent)

	resp, err := v.client.Do(req)
	if err != nil {
		return StatusFailed, 0, nil // dead/unreachable site — not a system error
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return StatusFailed, 0, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return StatusFailed, 0, nil
	}

	confidence := nameMatchConfidence(company.Name, string(body))
	if confidence >= 0.5 {
		return StatusVerified, confidence, nil
	}
	return StatusFailed, confidence, nil
}

// noiseWords are stripped before comparing a company name against page text — legal suffixes
// and generic words that would match almost any Kenyan business site regardless of identity.
var noiseWords = map[string]bool{
	"ltd": true, "limited": true, "kenya": true, "co": true, "company": true,
	"inc": true, "incorporated": true, "plc": true, "group": true, "the": true, "and": true,
}

// nameTokens reduces a company name to the significant words worth matching against page
// text — short/noise words are dropped since they'd match almost anything.
func nameTokens(name string) []string {
	name = strings.ToLower(name)
	var tokens []string
	for _, w := range strings.FieldsFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if len(w) < 3 || noiseWords[w] {
			continue
		}
		tokens = append(tokens, w)
	}
	return tokens
}

// CandidateDomains generates a ranked list of plausible domains for a company name — the old
// single-guess heuristic (lowercase, hyphen-join every word, always ".com") failed outright
// for real companies whose domain isn't a literal slug of the display name: a Kenyan business
// is at least as likely to sit on .co.ke as .com, and a real domain almost never has a hyphen
// even when the legal name has several words ("CFAO Motors Kenya limited (Formerly Toyota
// Kenya)" was being guessed as "cfao-motors-kenya-limited-formerly-toyota-kenya.com" — a
// domain that was never going to exist). The verify loop tries these in order and stops at
// the first one that actually resolves and matches; a name with nothing left after stripping
// parentheticals/noise words returns nil, same as before.
func CandidateDomains(name string) []string {
	// A parenthetical aside ("(Formerly Toyota Kenya)", "(K) Ltd") is never part of a domain —
	// strip it before tokenizing rather than folding it into the guess.
	if i := strings.Index(name, "("); i >= 0 {
		name = name[:i]
	}

	tokens := nameTokens(name)
	if len(tokens) == 0 {
		return nil
	}
	concatenated := strings.Join(tokens, "")
	dashed := strings.Join(tokens, "-")

	var out []string
	seen := map[string]bool{}
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	add(concatenated + ".co.ke")
	add(concatenated + ".com")
	if dashed != concatenated {
		add(dashed + ".co.ke")
		add(dashed + ".com")
	}
	return out
}

// nameMatchConfidence is the fraction of the company's significant name tokens that appear
// verbatim in the page text. A company with no significant tokens left after stripping noise
// words (e.g. name is just "ABC Ltd") can never be confidently matched — treat as no match
// rather than dividing by zero.
func nameMatchConfidence(companyName, pageText string) float64 {
	tokens := nameTokens(companyName)
	if len(tokens) == 0 {
		return 0
	}

	pageText = strings.ToLower(pageText)
	matched := 0
	for _, t := range tokens {
		if strings.Contains(pageText, t) {
			matched++
		}
	}
	return float64(matched) / float64(len(tokens))
}
