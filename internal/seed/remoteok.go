package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/anthonyalando8/prospector/internal/activity"
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
	Company     string   `json:"company"`
	Position    string   `json:"position"`
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
	Location    string   `json:"location"`
}

// Seed fetches RemoteOK's public feed, keeps backend/full-stack-relevant postings, and creates a
// company per unique employer — same guessed-domain-then-verify flow as the Kenyan job-board
// seeder (extractDomain here, internal/verify confirms or corrects it before enrichment ever
// touches it), since RemoteOK's public API doesn't reliably include a company website field.
func (r *RemoteOKSeeder) Seed(ctx context.Context, db *store.DB, redisConn *redis.Client) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteOKAPIURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	// RemoteOK returns 403 without a real-looking User-Agent; Accept: application/json is what
	// tells it to skip serving the HTML page instead.
	req.Header.Set("User-Agent", r.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetch %s: %w", remoteOKAPIURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("HTTP %d from RemoteOK", resp.StatusCode)
	}

	var jobs []remoteOKJob
	if err := json.NewDecoder(resp.Body).Decode(&jobs); err != nil {
		return 0, fmt.Errorf("decode RemoteOK response: %w", err)
	}

	added := 0
	for _, job := range jobs {
		if job.Company == "" || job.Position == "" || !isRelevantJobTitle(job.Position) {
			continue
		}

		domain := extractDomain(job.Company)
		if domain == "" {
			continue
		}

		// Plain-text snippet for the signal: parsed from the HTML description RemoteOK returns,
		// same as how the enricher turns a crawled page into signal text elsewhere in this
		// package, rather than storing raw HTML.
		jobText := job.Position + " at " + job.Company
		if doc, err := goquery.NewDocumentFromReader(strings.NewReader(job.Description)); err == nil {
			if text := strings.TrimSpace(doc.Text()); text != "" {
				jobText = jobText + ". " + text
			}
		}
		if len(jobText) > 300 {
			jobText = jobText[:300]
		}

		company, created, err := r.CreateCompanyIfNew(ctx, db, redisConn, job.Company, domain, "remoteok", job.URL, "candidate")
		if err != nil {
			log.Printf("RemoteOKSeeder: failed to create company for %s: %v", job.Company, err)
			continue
		}
		if !created {
			continue
		}

		added++
		activity.Log(ctx, redisConn, "worker:discover", fmt.Sprintf("Found %s (%s) — remote: %s", job.Company, domain, job.Position), true)

		jobURL := job.URL
		if _, err := db.CreateSignal(ctx, company.ID, "hiring_backend", jobText, &jobURL); err != nil {
			log.Printf("RemoteOKSeeder: failed to save signal for %s: %v", job.Company, err)
		}
	}

	return added, nil
}
