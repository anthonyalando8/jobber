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

// remotiveAPIURL is Remotive's public, documented JSON API — filtered to the software-dev
// category server-side, so this doesn't need the title-phrase filter to do as much work as the
// broader feeds (RemoteOK, WWR's general "programming" category) that pull everything.
const remotiveAPIURL = "https://remotive.com/api/remote-jobs?category=software-dev"

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
		CompanyName string `json:"company_name"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description"`
	} `json:"jobs"`
}

// Seed fetches Remotive's public API and creates a company per unique employer — same
// guessed-domain-then-verify flow every US-source seeder in this package uses.
func (r *RemotiveSeeder) Seed(ctx context.Context, db *store.DB, redisConn *redis.Client) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remotiveAPIURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", r.userAgent)

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetch %s: %w", remotiveAPIURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d from Remotive", resp.StatusCode)
	}

	var parsed remotiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return 0, fmt.Errorf("decode Remotive response: %w", err)
	}

	added := 0
	for _, job := range parsed.Jobs {
		if job.CompanyName == "" || job.Title == "" || !isRelevantJobTitle(job.Title) {
			continue
		}

		domain := extractDomain(job.CompanyName)
		if domain == "" {
			continue
		}

		extra := ""
		if doc, err := goquery.NewDocumentFromReader(strings.NewReader(job.Description)); err == nil {
			extra = strings.TrimSpace(doc.Text())
		}
		jobText := usJobSignalText(job.Title, job.CompanyName, extra)

		company, created, err := r.CreateCompanyIfNew(ctx, db, redisConn, job.CompanyName, domain, "remotive", job.URL, "candidate")
		if err != nil {
			log.Printf("RemotiveSeeder: failed to create company for %s: %v", job.CompanyName, err)
			continue
		}
		if !created {
			continue
		}

		added++
		activity.Log(ctx, redisConn, "worker:discover", fmt.Sprintf("Found %s (%s) — remote: %s", job.CompanyName, domain, job.Title), true)

		jobURL := job.URL
		if _, err := db.CreateSignal(ctx, company.ID, "hiring_backend", jobText, &jobURL); err != nil {
			log.Printf("RemotiveSeeder: failed to save signal for %s: %v", job.CompanyName, err)
		}
	}

	return added, nil
}
