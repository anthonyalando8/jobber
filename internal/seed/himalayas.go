package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/anthonyalando8/prospector/internal/activity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
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
	Jobs []struct {
		CompanyName string `json:"companyName"`
		Title       string `json:"title"`
		Excerpt     string `json:"excerpt"`
		Slug        string `json:"slug"`
	} `json:"jobs"`
}

// Seed fetches Himalayas' public API and creates a company per unique employer — same
// guessed-domain-then-verify flow every US-source seeder in this package uses.
func (h *HimalayasSeeder) Seed(ctx context.Context, db *store.DB, redisConn *redis.Client) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, himalayasAPIURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", h.userAgent)

	resp, err := h.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetch %s: %w", himalayasAPIURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d from Himalayas", resp.StatusCode)
	}

	var parsed himalayasResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return 0, fmt.Errorf("decode Himalayas response: %w", err)
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

		jobText := usJobSignalText(job.Title, job.CompanyName, job.Excerpt)
		jobURL := "https://himalayas.app/jobs/" + job.Slug

		company, created, err := h.CreateCompanyIfNew(ctx, db, redisConn, job.CompanyName, domain, "himalayas", jobURL, "candidate")
		if err != nil {
			log.Printf("HimalayasSeeder: failed to create company for %s: %v", job.CompanyName, err)
			continue
		}
		if !created {
			continue
		}

		added++
		activity.Log(ctx, redisConn, "worker:discover", fmt.Sprintf("Found %s (%s) — remote: %s", job.CompanyName, domain, job.Title), true)

		if _, err := db.CreateSignal(ctx, company.ID, "hiring_backend", jobText, &jobURL); err != nil {
			log.Printf("HimalayasSeeder: failed to save signal for %s: %v", job.CompanyName, err)
		}
	}

	return added, nil
}
