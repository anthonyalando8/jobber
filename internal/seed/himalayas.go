package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
	"strings"
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
		Description          string          `json:"description"`
		EmploymentType       string          `json:"employmentType"`
		LocationRestrictions []string        `json:"locationRestrictions"`
		ID                   json.RawMessage `json:"id"`
		CompanyName          string          `json:"companyName"`
		Title                string          `json:"title"`
		Excerpt              string          `json:"excerpt"`
		Slug                 string          `json:"slug"`
	} `json:"jobs"`
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
	for i, job := range parsed.Jobs {
		if i >= maxFeedJobs {
			break
		}
		if job.CompanyName == "" && job.Title == "" && job.Slug == "" && externalID(job.ID) == "" {
			continue
		}
		jobURL := ""
		if job.Slug != "" {
			jobURL = "https://himalayas.app/jobs/" + job.Slug
		}
		description, status := job.Excerpt, "partial"
		if job.Description != "" {
			description, status = job.Description, "complete"
		}
		d := opportunity.DiscoveredOpportunity{Source: "himalayas", ExternalID: externalID(job.ID), CompanyName: job.CompanyName, Title: job.Title, URL: jobURL, Description: descriptionText(description), EmploymentType: job.EmploymentType, Location: strings.Join(job.LocationRestrictions, ", "), DescriptionStatus: status, RemoteStatus: "remote"}
		run.ingest(ctx, db, d)
	}
	return run.result()
}
