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
	"net/url"
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

// Market describes where a company was discovered, which is what decides whether a Kenyan domain
// is a good first guess or a bad one.
type Market int

const (
	// MarketKenya is a Kenyan job board or a manually entered Kenyan company. A local business
	// is at least as likely to sit on .co.ke as .com.
	MarketKenya Market = iota
	// MarketInternational is a global remote board. Preferring .co.ke there produced domains
	// like delinea.co.ke for a US company and wakacje.co.ke for a Polish one — guesses that
	// never resolve, so research never runs and no contact is ever found.
	MarketInternational
)

// internationalSources are the remote job boards whose listings are global. Anything not named
// here is treated as Kenyan, which keeps the original behaviour for the Kenyan boards and for
// manual entry.
var internationalSources = map[string]bool{
	"remoteok":       true,
	"weworkremotely": true,
	"remotive":       true,
	"himalayas":      true,
}

// MarketForSource reports which market a discovery source belongs to.
func MarketForSource(source string) Market {
	if internationalSources[strings.ToLower(strings.TrimSpace(source))] {
		return MarketInternational
	}
	return MarketKenya
}

// CandidateDomainsForMarket orders the same guesses by where the company was found. Both markets
// try both suffixes — a Kenyan company can be on .com and an international one can, in principle,
// be on .co.ke — but the order differs, and order is what matters when only the first candidate
// is stored before verification has a chance to run.
func CandidateDomainsForMarket(name string, market Market) []string {
	if market == MarketKenya {
		return CandidateDomains(name)
	}
	candidates := CandidateDomains(name)
	international := make([]string, 0, len(candidates))
	local := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.HasSuffix(candidate, ".co.ke") {
			local = append(local, candidate)
			continue
		}
		international = append(international, candidate)
	}
	return append(international, local...)
}

// DomainFromURL extracts a registrable host from a URL the source itself published. This
// outranks every generated candidate: a URL the employer put in their own listing is stated
// fact, where a generated domain is only a plausible-looking guess.
//
// It returns nothing for a URL that points at the job board rather than the employer, because a
// link to remoteok.com is not the company's website.
func DomainFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimPrefix(host, "www.")
	if host == "" || !strings.Contains(host, ".") {
		return ""
	}
	for board := range jobBoardHosts {
		if host == board || strings.HasSuffix(host, "."+board) {
			return ""
		}
	}
	return host
}

// jobBoardHosts are the aggregators themselves. A link to one of these describes where the
// listing was published, never who published it.
var jobBoardHosts = map[string]bool{
	"remoteok.com": true, "remoteok.io": true, "weworkremotely.com": true,
	"remotive.com": true, "remotive.io": true, "himalayas.app": true,
	"brightermonday.co.ke": true, "myjobmag.co.ke": true, "linkedin.com": true,
	"greenhouse.io": true, "lever.co": true, "workable.com": true,
	"ashbyhq.com": true, "breezy.hr": true, "bamboohr.com": true, "recruitee.com": true,
	"smartrecruiters.com": true, "jobvite.com": true, "workday.com": true,
	"myworkdayjobs.com": true, "indeed.com": true, "glassdoor.com": true,
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
