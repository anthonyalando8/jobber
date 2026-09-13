package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	return db.IngestOpportunity(ctx, d)
}

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
		return nil, fmt.Errorf("source HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSourceBytes {
		return nil, fmt.Errorf("source response exceeds %d bytes", maxSourceBytes)
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
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02", time.RFC1123Z, time.RFC1123} {
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
