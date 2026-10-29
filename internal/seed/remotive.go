package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
)

// remotiveFeeds are Remotive's public, documented JSON API, filtered by category server-side:
// software development, and DevOps/sysadmin, which is where platform, infrastructure, cloud and
// site-reliability roles are filed. The matcher, not the category, decides fit.
var remotiveFeeds = []string{
	"https://remotive.com/api/remote-jobs?category=software-dev",
	"https://remotive.com/api/remote-jobs?category=devops",
}

// RemotiveSeeder pulls remote job postings from Remotive's public API.
type RemotiveSeeder struct {
	*BaseSeeder
}

// NewRemotiveSeeder creates a Remotive seeder.
func NewRemotiveSeeder(userAgent string) *RemotiveSeeder {
	return &RemotiveSeeder{BaseSeeder: NewBaseSeeder(userAgent)}
}

// Name returns the seeder name.
func (r *RemotiveSeeder) Name() string { return "remotive" }

type remotiveResponse struct {
	Jobs []struct {
		PublicationDate string          `json:"publication_date"`
		Location        string          `json:"candidate_required_location"`
		EmploymentType  string          `json:"job_type"`
		ID              json.RawMessage `json:"id"`
		CompanyName     string          `json:"company_name"`
		Title           string          `json:"title"`
		URL             string          `json:"url"`
		Description     string          `json:"description"`
	} `json:"jobs"`
}

// Seed stores bounded source listings independently of company discovery and Redis.
func (r *RemotiveSeeder) Seed(ctx context.Context, db *store.DB, _ *redis.Client) (int, error) {
	run := newBatch(r.Name())
	for _, feed := range remotiveFeeds {
		raw, err := r.fetchListing(ctx, feed)
		if !run.feed(feed, err) {
			continue
		}
		var parsed remotiveResponse
		if err = json.Unmarshal(raw, &parsed); err != nil {
			run.unreadable(feed, fmt.Errorf("remotive: unexpected response: %w", err))
			continue
		}
		for i, job := range parsed.Jobs {
			if i >= maxFeedJobs {
				break
			}
			if job.CompanyName == "" && job.Title == "" && job.URL == "" && externalID(job.ID) == "" {
				continue
			}
			d := opportunity.DiscoveredOpportunity{Source: "remotive", ExternalID: externalID(job.ID), CompanyName: job.CompanyName, Title: job.Title, URL: job.URL, Location: job.Location, EmploymentType: job.EmploymentType, Description: descriptionText(job.Description), PublishedAt: sourceTimestamp(job.PublicationDate), DescriptionStatus: "complete", RemoteStatus: "remote"}
			run.ingest(ctx, db, d)
		}
	}
	return run.result()
}
