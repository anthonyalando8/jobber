package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
)

// jobicyAPIURL is Jobicy's public remote-jobs API, filtered to the development industry. Jobicy
// asks consumers to poll sparingly and to link back to the listing; the scheduler runs each source
// at most every few hours and every stored listing keeps Jobicy's own URL.
const jobicyAPIURL = "https://jobicy.com/api/v2/remote-jobs?count=50&industry=dev"

// JobicySeeder pulls remote developer jobs from Jobicy's public API.
type JobicySeeder struct{ *BaseSeeder }

// NewJobicySeeder creates a Jobicy seeder.
func NewJobicySeeder(userAgent string) *JobicySeeder {
	return &JobicySeeder{BaseSeeder: NewBaseSeeder(userAgent)}
}

// Name returns the seeder name.
func (j *JobicySeeder) Name() string { return "jobicy" }

type jobicyResponse struct {
	Jobs []struct {
		ID          json.RawMessage `json:"id"`
		URL         string          `json:"url"`
		Title       string          `json:"jobTitle"`
		Company     string          `json:"companyName"`
		Geo         string          `json:"jobGeo"`
		Types       json.RawMessage `json:"jobType"`
		Excerpt     string          `json:"jobExcerpt"`
		Description string          `json:"jobDescription"`
		PubDate     string          `json:"pubDate"`
	} `json:"jobs"`
}

// Seed stores bounded source listings.
func (j *JobicySeeder) Seed(ctx context.Context, db *store.DB, _ *redis.Client) (int, error) {
	run := newBatch(j.Name())
	raw, err := j.fetchListing(ctx, jobicyAPIURL)
	if !run.feed(jobicyAPIURL, err) {
		return run.result()
	}
	var parsed jobicyResponse
	if err = json.Unmarshal(raw, &parsed); err != nil {
		return 0, fmt.Errorf("jobicy: unexpected response: %w", err)
	}
	for i, job := range parsed.Jobs {
		if i >= maxFeedJobs {
			break
		}
		if job.Company == "" && job.Title == "" {
			continue
		}
		description, status := job.Description, "complete"
		if description == "" {
			description, status = job.Excerpt, "partial"
		}
		run.ingest(ctx, db, opportunity.DiscoveredOpportunity{
			Source: "jobicy", ExternalID: externalID(job.ID), CompanyName: strings.TrimSpace(job.Company),
			Title: strings.TrimSpace(job.Title), URL: job.URL, Location: strings.TrimSpace(job.Geo),
			EmploymentType: stringOrList(job.Types), Description: descriptionText(description),
			PublishedAt: sourceTimestamp(job.PubDate), DescriptionStatus: status, RemoteStatus: "remote",
		})
	}
	return run.result()
}

// stringOrList reads a field some APIs publish as a string and others as a list of strings.
func stringOrList(raw json.RawMessage) string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return strings.TrimSpace(one)
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return strings.TrimSpace(strings.Join(many, ", "))
	}
	return ""
}
