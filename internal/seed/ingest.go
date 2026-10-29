package seed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
)

const maxSourceBytes = 2 * 1024 * 1024
const maxFeedJobs = 200

// batch ingests one source run's listings so that a single bad row or feed is logged and skipped
// instead of aborting everything after it. Before this, the first failing item ended the run: one
// malformed RSS entry from We Work Remotely dropped the rest of that feed and every later feed.
type batch struct {
	source                      string
	added, seen, review, failed int
	feedsTried, feedsFailed     int
	firstErr                    error
}

func newBatch(source string) *batch { return &batch{source: source} }

// feed records the outcome of fetching or parsing one feed or listing page.
func (b *batch) feed(url string, err error) bool {
	b.feedsTried++
	if err != nil {
		b.feedsFailed++
		if b.firstErr == nil {
			b.firstErr = err
		}
		log.Printf("seed %s: %s: %v", b.source, url, err)
		return false
	}
	return true
}

// unreadable marks a feed that answered but could not be parsed, after feed counted it as tried.
func (b *batch) unreadable(url string, err error) {
	b.feedsFailed++
	if b.firstErr == nil {
		b.firstErr = err
	}
	log.Printf("seed %s: %s: %v", b.source, url, err)
}

func (b *batch) ingest(ctx context.Context, db *store.DB, d opportunity.DiscoveredOpportunity) {
	b.seen++
	result, err := IngestOpportunity(ctx, db, d)
	switch {
	case err != nil:
		b.failed++
		if b.firstErr == nil {
			b.firstErr = err
		}
		log.Printf("seed %s: ingest %q: %v", b.source, d.Title, err)
	case result.ReviewReason != "":
		b.review++
	case result.Created:
		b.added++
	}
}

// result reports the run. It is an error only when nothing usable happened: every feed failed, or
// listings were seen and every one of them failed to store.
func (b *batch) result() (int, error) {
	if b.review > 0 || b.failed > 0 {
		log.Printf("seed %s: %d seen, %d new, %d held for review, %d failed", b.source, b.seen, b.added, b.review, b.failed)
	}
	if b.feedsTried > 0 && b.feedsFailed == b.feedsTried {
		return b.added, fmt.Errorf("all %d feed(s) failed: %w", b.feedsTried, b.firstErr)
	}
	if b.seen > 0 && b.failed == b.seen {
		return b.added, fmt.Errorf("every listing failed to store: %w", b.firstErr)
	}
	return b.added, nil
}

// The shared boundary deliberately has no Redis dependency or title-fit filter.
func IngestOpportunity(ctx context.Context, db *store.DB, d opportunity.DiscoveredOpportunity) (store.IngestionResult, error) {
	if d.Metadata == nil {
		d.Metadata = map[string]string{}
	}
	if d.Metadata["extraction"] == "" {
		d.Metadata["extraction"] = "source_adapter_normalized_text_v1"
	}
	// An application address stated in the posting is a published route, so capture it before
	// anything guesses. Adapters that have a structured field set it themselves; this covers the
	// common case of a description saying where to apply.
	if d.Metadata[opportunity.MetaApplyEmail] == "" {
		if address := opportunity.PublishedApplicationEmail(d.Description); address != "" {
			opportunity.RecordPublishedRoute(d.Metadata, d.Source+":description", "", "", address, "description")
		}
	}
	if d.DomainCandidate == "" {
		d.DomainCandidate = domainForSource(d)
	}
	result, err := db.IngestOpportunity(ctx, d)
	if err != nil || result.ID == 0 {
		return result, err
	}
	// Classify how the listing can be pursued as it arrives, so it is filterable at once. A failure
	// here is not an ingestion failure: the discovery worker's backfill classifies anything missed.
	if err := db.SaveOpportunityAccess(ctx, result.ID, result.Revision, opportunity.ClassifyAccess(d)); err != nil {
		log.Printf("seed %s: classifying opportunity %d: %v", d.Source, result.ID, err)
	}
	return result, nil
}

// fetchListing fetches a feed or listing page, retrying once after a short pause when the failure
// is plausibly transient (network error, timeout, 429 or 5xx). A 403 or 404 is not retried: it is
// an answer, and asking again only adds load on a site that has already said no.
func (b *BaseSeeder) fetchListing(ctx context.Context, url string) ([]byte, error) {
	raw, err := b.fetchSource(ctx, url)
	if err == nil || !transient(err) {
		return raw, err
	}
	select {
	case <-ctx.Done():
		return nil, err
	case <-time.After(b.retryDelay):
	}
	return b.fetchSource(ctx, url)
}

// statusError keeps the HTTP status so retry and reporting can tell a refusal from an outage.
type statusError struct{ code int }

func (e statusError) Error() string { return fmt.Sprintf("source HTTP %d", e.code) }

func transient(err error) bool {
	var status statusError
	if errors.As(err, &status) {
		return status.code == http.StatusTooManyRequests || status.code >= 500
	}
	return !errors.Is(err, errOversized)
}

var errOversized = fmt.Errorf("source response exceeds %d bytes", maxSourceBytes)

func (b *BaseSeeder) fetchSource(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", b.userAgent)
	req.Header.Set("Accept", "application/json, application/rss+xml, text/html")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError{resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSourceBytes {
		return nil, errOversized
	}
	return raw, nil
}

func descriptionText(raw string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return ""
	}
	doc.Find("script,style,nav,header,footer,noscript").Remove()
	doc.Find("p,li,div,section,h1,h2,h3,br").Each(func(_ int, s *goquery.Selection) { s.AppendHtml("\n") })
	lines := []string{}
	for _, line := range strings.Split(doc.Text(), "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n\n")
}

func externalID(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

func sourceTimestamp(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05 MST", "2006-01-02 15:04:05", "2006-01-02", time.RFC1123Z, time.RFC1123} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return &parsed
		}
	}
	return nil
}

func (b *BaseSeeder) completeJobDescription(ctx context.Context, d opportunity.DiscoveredOpportunity, sourceHost string) opportunity.DiscoveredOpportunity {
	if d.Metadata == nil {
		d.Metadata = map[string]string{}
	}
	u, err := url.Parse(d.URL)
	if err != nil || u.Hostname() != sourceHost || (u.Scheme != "http" && u.Scheme != "https") {
		d.Metadata["detail_warning"] = "missing_or_external_detail_url"
		return d
	}
	raw, err := b.fetchSource(ctx, d.URL)
	if err != nil {
		d.Metadata["detail_warning"] = "detail_fetch_failed"
		return d
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(raw)))
	if err != nil {
		d.Metadata["detail_warning"] = "detail_parse_failed"
		return d
	}
	content := doc.Find(`[itemprop="description"], .job-description, .job-details, article`).First()
	html, _ := content.Html()
	text := descriptionText(html)
	if text == "" {
		d.Metadata["detail_warning"] = "description_selector_missing"
		return d
	}
	d.Description = text
	d.DescriptionStatus = "complete"
	d.Metadata["description_source"] = d.URL
	return d
}
