package enrich

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anthonyalando8/prospector/internal/activity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
)

// attemptCooldown is how long a company that yielded nothing extractable is left alone before
// AdminEnrich will try it again. Without this, a company stuck at status="new" (because nothing
// was found) gets re-crawled — 7 pages, ~1.2s apart — on every single click of "Enrich New".
const attemptCooldown = 6 * time.Hour

// pageCacheTTL is how long a cached page fetch (in Postgres, review.md §13) is trusted before
// it's fetched fresh again. Independent of attemptCooldown: that's "don't retry a company that
// found nothing"; this is "don't refetch a page whose content is probably unchanged."
const pageCacheTTL = 24 * time.Hour

// memoryCacheTTL and memoryCacheMax bound the in-process page cache. The previous version was
// an unsynchronized map that never expired and never shrank, so a long-running worker could
// feed indefinitely stale content into research — and race against the HTTP enrich path while
// doing it. Phase 3 reads this content as evidence, so neither is acceptable any more.
const memoryCacheTTL = 15 * time.Minute
const memoryCacheMax = 512

type cachedPage struct {
	body      []byte
	fetchedAt time.Time
}

// Enricher extracts contacts and evidence from websites. Everything it retrieves is treated as
// untrusted data: it is stored as quoted evidence and is never turned into an instruction.
type Enricher struct {
	redis      *redis.Client
	crawlDelay time.Duration
	userAgent  string

	mu        sync.Mutex
	pageCache map[string]cachedPage
}

// NewEnricher creates a new enricher
func NewEnricher(redis *redis.Client, crawlDelayMS int, userAgent string) *Enricher {
	return &Enricher{
		redis:      redis,
		crawlDelay: time.Duration(crawlDelayMS) * time.Millisecond,
		userAgent:  userAgent,
		pageCache:  make(map[string]cachedPage),
	}
}

// cacheGet returns a fresh in-process entry. Expired entries are dropped rather than served.
func (e *Enricher) cacheGet(url string) ([]byte, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry, ok := e.pageCache[url]
	if !ok {
		return nil, false
	}
	if time.Since(entry.fetchedAt) >= memoryCacheTTL {
		delete(e.pageCache, url)
		return nil, false
	}
	return entry.body, true
}

// cachePut stores an entry, evicting expired ones and, if still at capacity, the oldest, so a
// long crawl cannot grow this map without bound.
func (e *Enricher) cachePut(url string, body []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.pageCache) >= memoryCacheMax {
		oldest, oldestAt := "", time.Now()
		for key, entry := range e.pageCache {
			if time.Since(entry.fetchedAt) >= memoryCacheTTL {
				delete(e.pageCache, key)
				continue
			}
			if entry.fetchedAt.Before(oldestAt) {
				oldest, oldestAt = key, entry.fetchedAt
			}
		}
		if len(e.pageCache) >= memoryCacheMax && oldest != "" {
			delete(e.pageCache, oldest)
		}
	}
	e.pageCache[url] = cachedPage{body: body, fetchedAt: time.Now()}
}

func attemptedKey(companyID int64) string {
	return "enrich:attempted:" + strconv.FormatInt(companyID, 10)
}

// RecentlyAttempted reports whether this company was tried within the cooldown window and found
// nothing — callers should skip it entirely (no HTTP fetches) rather than re-crawl it.
func (e *Enricher) RecentlyAttempted(ctx context.Context, companyID int64) bool {
	exists, err := e.redis.Exists(ctx, attemptedKey(companyID)).Result()
	if err != nil {
		return false // fail open: a Redis hiccup shouldn't block enrichment
	}
	return exists > 0
}

// EnrichCompany extracts contacts and signals from a company's website. found reports whether
// anything was actually extracted — callers should not count this as a success on found=false,
// since the company's status is only advanced to "enriched" when found is true.
func (e *Enricher) EnrichCompany(ctx context.Context, company *store.Company, db *store.DB) (found bool, err error) {
	if company.Domain == "" {
		return false, nil // Skip if no domain
	}

	log.Printf("Enriching %s (%s)...", company.Name, company.Domain)
	activity.Log(ctx, e.redis, "worker:enrich", fmt.Sprintf("Enriching %s (%s)…", company.Name, company.Domain), true)

	// Parse domain to build URLs
	baseDomain := company.Domain
	if !strings.Contains(baseDomain, "://") {
		baseDomain = "https://" + baseDomain
	}

	// Fetch key pages
	pages := []string{
		baseDomain,
		baseDomain + "/about",
		baseDomain + "/contact",
		baseDomain + "/careers",
		baseDomain + "/pricing",
		baseDomain + "/team",
		baseDomain + "/portfolio",
	}

	contactsFound := 0
	signalsFound := 0

	for _, pageURL := range pages {
		html, err := e.fetchPage(ctx, db, pageURL)
		if err != nil {
			continue
		}

		// Extract contacts
		contacts := e.extractEmails(string(html))
		for _, email := range contacts {
			_, _ = db.CreateContact(ctx, company.ID, "email", email, nil, nil, e.emailQuality(email))
			contactsFound++
		}

		phones := e.extractPhones(string(html))
		for _, phone := range phones {
			_, _ = db.CreateContact(ctx, company.ID, "phone", phone, nil, nil, 2)
			contactsFound++
		}

		// Extract evidence. Each distinct fact is stored once per company with its source URL
		// and excerpt; seeing the same fact again on another page refreshes the observation
		// rather than inserting a row that would look like independent confirmation.
		for _, item := range e.extractSignals(string(html), pageURL) {
			_, inserted, err := db.UpsertEvidence(ctx, company.ID, store.EvidenceInput{
				Kind: item.Kind, Detail: item.Detail, Excerpt: item.Excerpt,
				SourceURL: pageURL, ExtractionMethod: item.Method, Confidence: item.Confidence,
			})
			if err != nil {
				log.Printf("EnrichCompany: evidence write failed for %d: %v", company.ID, err)
				continue
			}
			if inserted {
				signalsFound++
			}
		}
	}

	// Update status
	if contactsFound > 0 || signalsFound > 0 {
		_ = db.UpdateCompanyStatus(ctx, company.ID, "enriched")
		log.Printf("Enriched %s: %d contacts, %d signals", company.Name, contactsFound, signalsFound)
		activity.Log(ctx, e.redis, "worker:enrich", fmt.Sprintf("Enriched %s: %d contacts, %d signals", company.Name, contactsFound, signalsFound), true)
		return true, nil
	}

	// Nothing extractable — leave status as "new" (it may be worth a retry later, e.g. a site
	// that was briefly down) but note the attempt so the next "Enrich New" click doesn't
	// immediately re-crawl it.
	_ = e.redis.Set(ctx, attemptedKey(company.ID), 1, attemptCooldown).Err()
	activity.Log(ctx, e.redis, "worker:enrich", fmt.Sprintf("%s: nothing extractable", company.Name), false)
	return false, nil
}

// fetchPage fetches a page, checking the in-process cache, then the persistent Postgres
// page_cache (review.md §13 — survives a restart, shared across the HTTP-triggered enrich
// path and the worker's research loop), before actually hitting the network behind a real
// per-host rate limit (review.md §12).
func (e *Enricher) fetchPage(ctx context.Context, db *store.DB, url string) ([]byte, error) {
	if cached, ok := e.cacheGet(url); ok {
		return cached, nil
	}

	if row, err := db.GetPageCache(ctx, url); err != nil {
		log.Printf("fetchPage: page_cache read error for %s: %v", url, err)
	} else if row != nil && time.Since(row.FetchedAt) < pageCacheTTL {
		if row.Status != http.StatusOK {
			return nil, fmt.Errorf("cached HTTP %d for %s", row.Status, url)
		}
		body := []byte(row.Body)
		e.cachePut(url, body)
		return body, nil
	}

	e.waitForHost(ctx, url)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", e.userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err // network-level failure — nothing to cache, just retry next time
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	if err := db.UpsertPageCache(ctx, url, resp.StatusCode, string(body)); err != nil {
		log.Printf("fetchPage: page_cache write error for %s: %v", url, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	e.cachePut(url, body)
	return body, nil
}

// waitForHost is the real per-host rate limit review.md §12 asks for — the previous version
// wrote a crawl:host:{host} key but never checked it, so it was purely decorative and every
// fetch just slept a flat e.crawlDelay regardless of which host it was hitting. SetNX gives
// exclusive use of the host for crawlDelay; a caller that loses the race waits and retries
// rather than proceeding, so two fetches to the same host — whether from two goroutines, or
// the HTTP enrich path and the worker's research loop running at once — can never land closer
// together than crawlDelay apart.
func (e *Enricher) waitForHost(ctx context.Context, pageURL string) {
	host := pageURL
	if u, err := url.Parse(pageURL); err == nil && u.Host != "" {
		host = u.Host
	}
	key := "crawl:host:" + host

	for {
		ok, err := e.redis.SetNX(ctx, key, time.Now().Unix(), e.crawlDelay).Result()
		if err != nil || ok {
			return // fail open on a Redis hiccup rather than crawl forever
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// retinaAssetSuffix catches image-asset filenames using the "@2x"/"@3x" retina naming
// convention (e.g. "hero_90x@2x.png") — these match the email regex's shape (word@word.ext) but
// are never real addresses.
var retinaAssetSuffix = regexp.MustCompile(`@\d+x\.[a-zA-Z0-9]+$`)

// nonEmailExtensions are file extensions that occasionally masquerade as an email's TLD (e.g.
// "design@2x.png" or "sprite@1x.svg") — a real email address never ends in one of these.
var nonEmailExtensions = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true, "svg": true, "webp": true,
	"ico": true, "bmp": true, "tiff": true, "avif": true, "css": true, "js": true,
	"woff": true, "woff2": true, "ttf": true, "eot": true, "pdf": true, "mp4": true, "mp3": true,
}

// placeholderEmailDomains are domains that only ever appear in example markup, form
// placeholders, and documentation — never a real inbox. example.com/.org/.net are IANA-reserved
// specifically for this (RFC 2606); the rest are just the domains that show up in the wild in
// HTML forms and privacy-policy boilerplate. Without this, a site's own "e.g. you@example.com"
// placeholder text gets scraped and drafted to as if it were the company's real contact.
var placeholderEmailDomains = map[string]bool{
	"example.com": true, "example.org": true, "example.net": true,
	"domain.com": true, "yourdomain.com": true, "email.com": true,
	"test.com": true, "sample.com": true, "yoursite.com": true, "company.com": true,
}

// extractEmails finds email addresses
func (e *Enricher) extractEmails(html string) []string {
	// Email regex
	re := regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	matches := re.FindAllString(html, -1)

	seen := make(map[string]bool)
	var unique []string
	for _, email := range matches {
		email = strings.ToLower(email)
		// Skip common non-prospect emails
		if strings.Contains(email, "noreply") || strings.Contains(email, "no-reply") {
			continue
		}
		// Skip image-asset filenames that happen to look like an email (e.g. "logo@2x.png").
		if retinaAssetSuffix.MatchString(email) {
			continue
		}
		if dot := strings.LastIndex(email, "."); dot != -1 && nonEmailExtensions[email[dot+1:]] {
			continue
		}
		if at := strings.LastIndex(email, "@"); at != -1 && placeholderEmailDomains[email[at+1:]] {
			continue
		}
		if !seen[email] {
			unique = append(unique, email)
			seen[email] = true
		}
	}
	return unique
}

// extractPhones finds phone numbers
func (e *Enricher) extractPhones(html string) []string {
	// Kenya phone pattern: +254 or 0, followed by 1 or 7, then 8 digits
	re := regexp.MustCompile(`(\+?254|0)[17]\d{8}`)
	matches := re.FindAllString(html, -1)

	seen := make(map[string]bool)
	var unique []string
	for _, phone := range matches {
		// Normalize to +254 format
		if strings.HasPrefix(phone, "0") {
			phone = "+254" + phone[1:]
		} else if strings.HasPrefix(phone, "254") {
			phone = "+" + phone
		}
		if !seen[phone] {
			unique = append(unique, phone)
			seen[phone] = true
		}
	}
	return unique
}

// Signal represents one extracted piece of evidence, carrying the source excerpt that supports
// it rather than only the conclusion a keyword matcher drew.
type Signal struct {
	Kind       string
	Detail     string
	Excerpt    string
	Method     string
	Confidence string
}

// These turn a page into readable text so an excerpt can quote the company rather than
// paraphrase it.
var (
	scriptStyle   = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</(script|style|noscript)>`)
	tagStripper   = regexp.MustCompile(`(?s)<[^>]*>`)
	whitespaceRun = regexp.MustCompile(`\s+`)
	sentenceSplit = regexp.MustCompile(`(?:[.!?]\s+|\n)`)
)

func pageText(html string) string {
	text := scriptStyle.ReplaceAllString(html, " ")
	text = tagStripper.ReplaceAllString(text, " ")
	text = strings.NewReplacer("&amp;", "&", "&nbsp;", " ", "&#39;", "'", "&quot;", "\"").Replace(text)
	return strings.TrimSpace(whitespaceRun.ReplaceAllString(text, " "))
}

// excerptFor returns the sentence in which a term actually appears, bounded, so stored evidence
// quotes the source instead of asserting a conclusion the source never stated.
func excerptFor(text, term string) string {
	lower := strings.ToLower(text)
	idx := strings.Index(lower, strings.ToLower(term))
	if idx < 0 {
		return ""
	}
	start := 0
	for _, loc := range sentenceSplit.FindAllStringIndex(text, -1) {
		if loc[1] > idx {
			break
		}
		start = loc[1]
	}
	end := len(text)
	if loc := sentenceSplit.FindStringIndex(text[idx:]); loc != nil {
		end = idx + loc[0]
	}
	excerpt := strings.TrimSpace(text[start:end])
	if len(excerpt) > 400 {
		excerpt = excerpt[:400]
	}
	return excerpt
}

// engineeringRequestCues are phrases in which a company states an engineering need in its own
// words. A technology appearing in page source is never one of these.
var engineeringRequestCues = []string{
	"we are hiring", "we're hiring", "we are looking for", "we're looking for",
	"join our engineering", "join our team", "open positions", "open roles",
	"we need a developer", "looking to hire", "now hiring",
}

var engineeringWords = []string{
	"developer", "engineer", "engineering", "backend", "back-end", "full stack",
	"full-stack", "software", "api", "programmer",
}

// productCues introduce a company describing what it actually builds or sells.
var productCues = []string{
	"we build", "we develop", "we design", "we provide", "we offer", "we help",
	"our platform", "our software", "our product", "we specialise", "we specialize",
}

// extractSignals finds tech stack and business indicators. A detection states only what was
// detected; internal/evidence records separately what each kind can and cannot establish.
func (e *Enricher) extractSignals(html string, pageURL string) []Signal {
	var signals []Signal
	htmlLower := strings.ToLower(html)
	text := pageText(html)

	// Tech stack indicators
	techSignals := map[string]string{
		"woocommerce": "tech:woocommerce",
		"shopify":     "tech:shopify",
		"wordpress":   "tech:wordpress",
		"firebase":    "tech:firebase",
		"flutter":     "tech:flutter",
		"react":       "tech:react",
		"vue.js":      "tech:vue",
		"django":      "tech:django",
		"rails":       "tech:rails",
		"nodejs":      "tech:nodejs",
		"golang":      "tech:golang",
	}

	for tech, kind := range techSignals {
		if strings.Contains(htmlLower, tech) {
			signals = append(signals, Signal{Kind: kind, Detail: "Found " + tech + " in site code",
				Excerpt: excerptFor(text, tech), Method: "site_keyword_v1", Confidence: "low"})
		}
	}

	// Business indicators
	if strings.Contains(htmlLower, "add to cart") || strings.Contains(htmlLower, "checkout") || strings.Contains(htmlLower, "pay now") || strings.Contains(htmlLower, "lipa") {
		signals = append(signals, Signal{Kind: "checkout_present",
			Detail: "Checkout or payment functionality is present on the site", Method: "site_keyword_v1", Confidence: "medium"})
	}

	// Payment indicators
	if strings.Contains(htmlLower, "paybill") || strings.Contains(htmlLower, "till number") {
		signals = append(signals, Signal{Kind: "paybill_listed", Detail: "The site lists an M-Pesa paybill or till",
			Excerpt: excerptFor(text, "paybill"), Method: "site_keyword_v1", Confidence: "medium"})
	}

	// Freshness
	if strings.Contains(htmlLower, "2024") || strings.Contains(htmlLower, "2025") {
		signals = append(signals, Signal{Kind: "active_site", Detail: "A recent year string appears in the page text",
			Method: "site_keyword_v1", Confidence: "low"})
	} else if strings.Contains(htmlLower, "2022") || strings.Contains(htmlLower, "2021") {
		signals = append(signals, Signal{Kind: "stale_site", Detail: "Only older year strings appear in the page text",
			Method: "site_keyword_v1", Confidence: "low"})
	}

	// Self-described technical/creative service provider — a company that itself builds
	// websites, apps, or software for clients. Without this, a real web/software agency's own
	// signals were only ever tech-stack ("runs WordPress") or freshness ("site hasn't been
	// updated since 2022") — both of which, aimed at a company that professionally does this
	// work, read as either irrelevant or condescending ("you don't know your own site is
	// outdated?"), not as a useful hook. See relevantSignalsForSegment in
	// internal/draft/generate.go for where this changes what the model is allowed to build an
	// observation from for the "agency" segment.
	techServiceKeywords := []string{
		"web design", "web development", "website design", "software development",
		"app development", "mobile app development", "digital agency", "digital solutions",
		"ui/ux", "ux design", "custom software", "software solutions", "software company",
	}
	for _, kw := range techServiceKeywords {
		if strings.Contains(htmlLower, kw) {
			signals = append(signals, Signal{Kind: "offers_tech_services", Detail: "Site markets " + kw + " as a service",
				Excerpt: excerptFor(text, kw), Method: "site_text_v1", Confidence: "medium"})
			break // one hit is enough evidence — no need to pile up near-duplicate signals
		}
	}

	// What the company says about itself, quoted. This is the evidence that can make a service
	// or collaboration reason truthful, as opposed to a technology detected in page source.
	for _, cue := range productCues {
		excerpt := excerptFor(text, cue)
		if excerpt == "" {
			continue
		}
		signals = append(signals, Signal{Kind: "product_description",
			Detail: "The company describes its own work", Excerpt: excerpt,
			Method: "site_text_v1", Confidence: "medium"})
		break
	}

	// An engineering need stated by the company itself. A cue alone is not enough: the same
	// sentence must actually be about engineering, or "we are hiring" for a cashier would read
	// as an engineering request.
	for _, cue := range engineeringRequestCues {
		excerpt := excerptFor(text, cue)
		if excerpt == "" {
			continue
		}
		lower := strings.ToLower(excerpt)
		engineering := false
		for _, word := range engineeringWords {
			if strings.Contains(lower, word) {
				engineering = true
			}
		}
		if !engineering {
			continue
		}
		signals = append(signals, Signal{Kind: "engineering_request",
			Detail: "The company states an engineering hiring need", Excerpt: excerpt,
			Method: "site_text_v1", Confidence: "high"})
		break
	}

	return signals
}

// emailQuality ranks email quality
// genericLocalParts are local-parts that are never a real person's name — generic address
// words (info/support/admin) and, critically, placeholder/pronoun words that show up in form
// markup ("you@example.com"). The old version only checked for a handful of role-based words
// and defaulted anything else to "named" (quality 3, the highest) — which is exactly how
// "you@example.com" ended up outranking a company's real info@ address and getting sent to.
var genericLocalParts = map[string]bool{
	"info": true, "hello": true, "contact": true, "admin": true, "support": true,
	"team": true, "general": true, "office": true, "help": true, "enquiries": true,
	"enquiry": true, "mail": true, "email": true, "you": true, "your": true, "me": true,
	"my": true, "user": true, "name": true, "test": true, "sample": true, "webmaster": true,
}

func (e *Enricher) emailQuality(email string) int {
	// named 3 > role-based 2 > info@/generic 1
	if strings.Contains(email, "@") {
		parts := strings.Split(email, "@")
		local := strings.ToLower(parts[0])

		if genericLocalParts[local] || strings.Contains(local, "info") || strings.Contains(local, "hello") || strings.Contains(local, "contact") {
			return 1
		}
		if strings.Contains(local, "ceo") || strings.Contains(local, "founder") || strings.Contains(local, "sales") || strings.Contains(local, "dev") {
			return 2
		}
		return 3 // named email
	}
	return 1
}
