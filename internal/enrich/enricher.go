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

// Enricher extracts contacts and signals from websites
type Enricher struct {
	redis      *redis.Client
	crawlDelay time.Duration
	userAgent  string
	pageCache  map[string][]byte // in-process hit for a URL fetched earlier in this process's life
}

// NewEnricher creates a new enricher
func NewEnricher(redis *redis.Client, crawlDelayMS int, userAgent string) *Enricher {
	return &Enricher{
		redis:      redis,
		crawlDelay: time.Duration(crawlDelayMS) * time.Millisecond,
		userAgent:  userAgent,
		pageCache:  make(map[string][]byte),
	}
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

		// Extract signals
		signals := e.extractSignals(string(html), &pageURL)
		for _, signal := range signals {
			_, _ = db.CreateSignal(ctx, company.ID, signal.Kind, signal.Detail, &pageURL)
			signalsFound++
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
	if cached, ok := e.pageCache[url]; ok {
		return cached, nil
	}

	if row, err := db.GetPageCache(ctx, url); err != nil {
		log.Printf("fetchPage: page_cache read error for %s: %v", url, err)
	} else if row != nil && time.Since(row.FetchedAt) < pageCacheTTL {
		if row.Status != http.StatusOK {
			return nil, fmt.Errorf("cached HTTP %d for %s", row.Status, url)
		}
		body := []byte(row.Body)
		e.pageCache[url] = body
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

	e.pageCache[url] = body
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

// Signal represents extracted evidence
type Signal struct {
	Kind   string
	Detail string
}

// extractSignals finds tech stack and business indicators
func (e *Enricher) extractSignals(html string, pageURL *string) []Signal {
	var signals []Signal
	htmlLower := strings.ToLower(html)

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
			signals = append(signals, Signal{kind, "Found " + tech + " in site code"})
		}
	}

	// Business indicators
	if strings.Contains(htmlLower, "add to cart") || strings.Contains(htmlLower, "checkout") || strings.Contains(htmlLower, "pay now") || strings.Contains(htmlLower, "lipa") {
		signals = append(signals, Signal{"checkout_present", "E-commerce or payment functionality detected"})
	}

	// Payment indicators
	if strings.Contains(htmlLower, "paybill") || strings.Contains(htmlLower, "till number") {
		signals = append(signals, Signal{"paybill_listed", "Accepts M-Pesa payments"})
	}

	// Freshness
	if strings.Contains(htmlLower, "2024") || strings.Contains(htmlLower, "2025") {
		signals = append(signals, Signal{"active_site", "Recently updated content"})
	} else if strings.Contains(htmlLower, "2022") || strings.Contains(htmlLower, "2021") {
		signals = append(signals, Signal{"stale_site", "Outdated site (2021-2022)"})
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
			signals = append(signals, Signal{"offers_tech_services", "Site markets " + kw + " as a service"})
			break // one hit is enough evidence — no need to pile up near-duplicate signals
		}
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
