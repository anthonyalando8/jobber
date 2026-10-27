package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
)

// remoteOKAPIURL is RemoteOK's public JSON job feed — no scraping, no login, no anti-bot wall,
// and (per their own site) explicitly meant for this kind of consumption. This is deliberately
// the first US-reachable source: Upwork/Freelancer/Fiverr forbid automated scraping and route
// applications through their own bidding system, not off-platform cold contact — RemoteOK is a
// plain job board (companies posting openings), the same shape as the Kenyan job-board seeder,
// just US/remote-market.
const remoteOKAPIURL = "https://remoteok.com/api"

// RemoteOKSeeder pulls remote job postings from RemoteOK's public API — individual companies
// (often startups) hiring remote engineers, not a marketplace of one-off gigs.
type RemoteOKSeeder struct {
	*BaseSeeder
}

// NewRemoteOKSeeder creates a RemoteOK seeder.
func NewRemoteOKSeeder(userAgent string) *RemoteOKSeeder {
	return &RemoteOKSeeder{BaseSeeder: NewBaseSeeder(userAgent)}
}

// Name returns the seeder name.
func (r *RemoteOKSeeder) Name() string { return "remoteok" }

// remoteOKJob is the subset of RemoteOK's job JSON this seeder actually uses. The feed's first
// element is a legal/metadata notice, not a job — it unmarshals with Company/Position empty and
// is filtered out below like any other non-match.
type remoteOKJob struct {
	Date        string          `json:"date"`
	ID          json.RawMessage `json:"id"`
	Company     string          `json:"company"`
	Position    string          `json:"position"`
	Tags        []string        `json:"tags"`
	Description string          `json:"description"`
	URL         string          `json:"url"`
	Location    string          `json:"location"`
	// ApplyURL and CompanyURL are published by the feed and were previously discarded. They are
	// the difference between knowing where to apply and guessing a domain.
	ApplyURL   string `json:"apply_url"`
	CompanyURL string `json:"company_url"`
}

// Seed stores bounded source listings independently of company discovery and Redis.
func (r *RemoteOKSeeder) Seed(ctx context.Context, db *store.DB, _ *redis.Client) (int, error) {
	run := newBatch(r.Name())
	raw, err := r.fetchListing(ctx, remoteOKAPIURL)
	if !run.feed(remoteOKAPIURL, err) {
		return run.result()
	}
	var parsed []remoteOKJob
	if err = json.Unmarshal(raw, &parsed); err != nil {
		return 0, fmt.Errorf("remoteok: unexpected response: %w", err)
	}
	for i, job := range parsed {
		if i >= maxFeedJobs {
			break
		}
		if job.Company == "" && job.Position == "" && job.URL == "" && externalID(job.ID) == "" {
			continue
		}
		d := opportunity.DiscoveredOpportunity{Source: "remoteok", ExternalID: externalID(job.ID), CompanyName: job.Company, Title: job.Position, URL: job.URL, Location: job.Location, Description: descriptionText(job.Description), PublishedAt: sourceTimestamp(job.Date), DescriptionStatus: "complete", RemoteStatus: "remote", Metadata: map[string]string{}}
		opportunity.RecordPublishedRoute(d.Metadata, "remoteok:feed", job.CompanyURL, job.ApplyURL, "", "")
		run.ingest(ctx, db, d)
	}
	return run.result()
}
