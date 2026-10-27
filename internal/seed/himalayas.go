package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
	"sort"
	"strings"
	"time"
)

// himalayasAPIURL is Himalayas' public, documented JSON API. Its excerpt field is already plain
// text (unlike RemoteOK/Remotive's HTML descriptions), so no goquery stripping is needed here.
const himalayasAPIURL = "https://himalayas.app/jobs/api"

// HimalayasSeeder pulls remote job postings from Himalayas' public API.
type HimalayasSeeder struct {
	*BaseSeeder
}

// NewHimalayasSeeder creates a Himalayas seeder.
func NewHimalayasSeeder(userAgent string) *HimalayasSeeder {
	return &HimalayasSeeder{BaseSeeder: NewBaseSeeder(userAgent)}
}

// Name returns the seeder name.
func (h *HimalayasSeeder) Name() string { return "himalayas" }

type himalayasResponse struct {
	Jobs []json.RawMessage `json:"jobs"`
}

// himalayasJob reads both the field names this adapter was written against (id, slug) and the
// ones Himalayas publishes now according to third-party integrations (guid, applicationLink,
// companySlug, pubDate). Production evidence agreed: every listing arrived with a company and a
// title but no id or slug, so all twenty in each response were held back as having no source
// identity. Both sets are read so the adapter works whichever the live API sends.
type himalayasJob struct {
	ID                   json.RawMessage `json:"id"`
	GUID                 string          `json:"guid"`
	Slug                 string          `json:"slug"`
	ApplicationLink      string          `json:"applicationLink"`
	CompanyName          string          `json:"companyName"`
	Title                string          `json:"title"`
	Excerpt              string          `json:"excerpt"`
	Description          string          `json:"description"`
	EmploymentType       string          `json:"employmentType"`
	LocationRestrictions []string        `json:"locationRestrictions"`
	PubDate              json.RawMessage `json:"pubDate"`
	ExpiryDate           json.RawMessage `json:"expiryDate"`
}

// Seed stores bounded source listings independently of company discovery and Redis.
func (h *HimalayasSeeder) Seed(ctx context.Context, db *store.DB, _ *redis.Client) (int, error) {
	run := newBatch(h.Name())
	raw, err := h.fetchListing(ctx, himalayasAPIURL)
	if !run.feed(himalayasAPIURL, err) {
		return run.result()
	}
	var parsed himalayasResponse
	if err = json.Unmarshal(raw, &parsed); err != nil {
		return 0, fmt.Errorf("himalayas: unexpected response: %w", err)
	}
	for i, item := range parsed.Jobs {
		if i >= maxFeedJobs {
			break
		}
		d, ok := himalayasOpportunity(item)
		if ok {
			run.ingest(ctx, db, d)
		}
	}
	return run.result()
}

// himalayasOpportunity maps one job. Identity falls back in a fixed order (numeric id, then guid)
// so it is the same on every run; nothing is derived from position or time. The listing URL is
// the Himalayas job page when a slug is published, otherwise the guid when it is a URL, otherwise
// the employer's application link.
func himalayasOpportunity(item json.RawMessage) (opportunity.DiscoveredOpportunity, bool) {
	var job himalayasJob
	if err := json.Unmarshal(item, &job); err != nil {
		return opportunity.DiscoveredOpportunity{}, false
	}
	if job.CompanyName == "" && job.Title == "" && job.Slug == "" && job.GUID == "" && externalID(job.ID) == "" {
		return opportunity.DiscoveredOpportunity{}, false
	}
	id := externalID(job.ID)
	if id == "" {
		id = strings.TrimSpace(job.GUID)
	}
	jobURL := ""
	switch {
	case job.Slug != "":
		jobURL = "https://himalayas.app/jobs/" + job.Slug
	case strings.HasPrefix(job.GUID, "http://") || strings.HasPrefix(job.GUID, "https://"):
		jobURL = strings.TrimSpace(job.GUID)
	case strings.HasPrefix(job.ApplicationLink, "http"):
		jobURL = strings.TrimSpace(job.ApplicationLink)
	}
	description, status := job.Excerpt, "partial"
	if job.Description != "" {
		description, status = job.Description, "complete"
	}
	d := opportunity.DiscoveredOpportunity{
		Source: "himalayas", ExternalID: id, CompanyName: strings.TrimSpace(job.CompanyName), Title: strings.TrimSpace(job.Title),
		URL: jobURL, Description: descriptionText(description), EmploymentType: job.EmploymentType,
		Location: strings.Join(job.LocationRestrictions, ", "), DescriptionStatus: status, RemoteStatus: "remote",
		PublishedAt: flexibleTimestamp(job.PubDate), ExpiresAt: flexibleTimestamp(job.ExpiryDate), Metadata: map[string]string{},
	}
	if strings.HasPrefix(job.ApplicationLink, "http") {
		opportunity.RecordPublishedRoute(d.Metadata, "himalayas:feed", "", strings.TrimSpace(job.ApplicationLink), "", "")
	}
	if id == "" && jobURL == "" {
		// Held back for having no identity. Record which fields the source did send, so the
		// held-back view shows what the adapter would need to read instead of only that it failed.
		d.Metadata["source_fields"] = fieldNames(item)
	}
	return d, true
}

// fieldNames lists the top-level keys of a JSON object, sorted, for diagnostics.
func fieldNames(item json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(item, &fields) != nil {
		return ""
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// flexibleTimestamp reads a date published as an RFC 3339 string or as a Unix epoch number in
// seconds or milliseconds.
func flexibleTimestamp(raw json.RawMessage) *time.Time {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return sourceTimestamp(text)
	}
	var number float64
	if json.Unmarshal(raw, &number) != nil || number <= 0 {
		return nil
	}
	seconds := int64(number)
	if seconds > 1e11 { // milliseconds
		seconds /= 1000
	}
	t := time.Unix(seconds, 0).UTC()
	return &t
}
