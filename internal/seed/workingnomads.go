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

// workingNomadsAPIURL is Working Nomads' public jobs export. It returns every category, so only
// development listings with an engineering title are kept.
const workingNomadsAPIURL = "https://www.workingnomads.com/api/exposed_jobs/"

// WorkingNomadsSeeder pulls remote development jobs from Working Nomads' public export.
type WorkingNomadsSeeder struct{ *BaseSeeder }

// NewWorkingNomadsSeeder creates a Working Nomads seeder.
func NewWorkingNomadsSeeder(userAgent string) *WorkingNomadsSeeder {
	return &WorkingNomadsSeeder{BaseSeeder: NewBaseSeeder(userAgent)}
}

// Name returns the seeder name.
func (w *WorkingNomadsSeeder) Name() string { return "workingnomads" }

type workingNomadsJob struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Company     string `json:"company_name"`
	Category    string `json:"category_name"`
	Location    string `json:"location"`
	PubDate     string `json:"pub_date"`
}

// Seed stores bounded development listings.
func (w *WorkingNomadsSeeder) Seed(ctx context.Context, db *store.DB, _ *redis.Client) (int, error) {
	run := newBatch(w.Name())
	raw, err := w.fetchListing(ctx, workingNomadsAPIURL)
	if !run.feed(workingNomadsAPIURL, err) {
		return run.result()
	}
	var parsed []workingNomadsJob
	if err = json.Unmarshal(raw, &parsed); err != nil {
		return 0, fmt.Errorf("workingnomads: unexpected response: %w", err)
	}
	kept := 0
	for _, job := range parsed {
		if kept >= maxFeedJobs {
			break
		}
		if !strings.EqualFold(strings.TrimSpace(job.Category), "development") || !engineeringTitle(job.Title) {
			continue
		}
		kept++
		run.ingest(ctx, db, opportunity.DiscoveredOpportunity{
			Source: "workingnomads", CompanyName: strings.TrimSpace(job.Company), Title: strings.TrimSpace(job.Title),
			URL: job.URL, Location: strings.TrimSpace(job.Location), Description: descriptionText(job.Description),
			PublishedAt: sourceTimestamp(job.PubDate), DescriptionStatus: "complete", RemoteStatus: "remote",
		})
	}
	return run.result()
}

// engineeringTitle keeps sources that publish every department (employer career boards, general
// exports) from filling the workspace with sales and HR roles. It is deliberately broad: the
// matcher, not this filter, decides fit.
func engineeringTitle(title string) bool {
	t := strings.ToLower(title)
	for _, word := range []string{"engineer", "developer", "programmer", "devops", "software", "backend", "back-end", "frontend", "front-end", "full stack", "full-stack", "fullstack", "architect", "golang", "python", "django"} {
		if strings.Contains(t, word) {
			return true
		}
	}
	// Short tokens only count as whole words, so "api" does not match "capital".
	for _, token := range strings.FieldsFunc(t, func(r rune) bool { return !(r >= 'a' && r <= 'z') }) {
		if token == "api" || token == "sre" || token == "go" {
			return true
		}
	}
	return false
}
