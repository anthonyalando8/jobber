package seed

import (
	"context"
	"encoding/xml"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
	"strings"
)

// weWorkRemotelyFeeds are per-category RSS feeds — a real public feed (title format "Company:
// Position", purpose-built for exactly this kind of consumption), not scraped HTML, so this is
// less fragile than most sources in this package. Categories chosen for relevance to
// backend/full-stack contract work; WWR has many more categories (design, sales, ...) that
// aren't worth pulling.
var weWorkRemotelyFeeds = []string{
	"https://weworkremotely.com/categories/remote-back-end-programming-jobs.rss",
	"https://weworkremotely.com/categories/remote-full-stack-programming-jobs.rss",
	"https://weworkremotely.com/categories/remote-devops-sysadmin-jobs.rss",
	"https://weworkremotely.com/categories/remote-programming-jobs.rss",
}

// WeWorkRemotelySeeder pulls remote job postings from We Work Remotely's public RSS feeds.
type WeWorkRemotelySeeder struct {
	*BaseSeeder
}

// NewWeWorkRemotelySeeder creates a We Work Remotely seeder.
func NewWeWorkRemotelySeeder(userAgent string) *WeWorkRemotelySeeder {
	return &WeWorkRemotelySeeder{BaseSeeder: NewBaseSeeder(userAgent)}
}

// Name returns the seeder name.
func (w *WeWorkRemotelySeeder) Name() string { return "weworkremotely" }

type wwrRSS struct {
	Channel struct {
		Items []wwrItem `xml:"item"`
	} `xml:"channel"`
}

// wwrItem reads the WWR-specific fields as well as the standard RSS ones. Region and country are
// what say who may apply ("Anywhere in the World" versus "USA Only"); they were previously parsed
// past, which left every WWR listing with an empty location and an unknown eligibility.
type wwrItem struct {
	PubDate     string `xml:"pubDate"`
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	Region      string `xml:"region"`
	Country     string `xml:"country"`
	State       string `xml:"state"`
	Type        string `xml:"type"`
	Category    string `xml:"category"`
	ExpiresAt   string `xml:"expires_at"`
}

// Seed retains listings within the existing category feed scope, without fit filtering. Each
// feed is independent: one that fails or does not parse is logged and the others still run.
func (w *WeWorkRemotelySeeder) Seed(ctx context.Context, db *store.DB, _ *redis.Client) (int, error) {
	run := newBatch(w.Name())
	for _, url := range weWorkRemotelyFeeds {
		raw, err := w.fetchListing(ctx, url)
		if !run.feed(url, err) {
			continue
		}
		var feed wwrRSS
		if err = xml.Unmarshal(raw, &feed); err != nil {
			run.feedsFailed++
			if run.firstErr == nil {
				run.firstErr = err
			}
			continue
		}
		for i, item := range feed.Channel.Items {
			if i >= maxFeedJobs {
				break
			}
			run.ingest(ctx, db, wwrOpportunity(item))
		}
	}
	return run.result()
}

func wwrOpportunity(item wwrItem) opportunity.DiscoveredOpportunity {
	company, title, found := strings.Cut(item.Title, ": ")
	if !found {
		company = ""
		title = item.Title
	}
	location := strings.TrimSpace(item.Region)
	if country := strings.TrimSpace(item.Country); country != "" && !strings.EqualFold(country, location) {
		location = strings.Trim(location+", "+country, ", ")
	}
	d := opportunity.DiscoveredOpportunity{
		Source: "weworkremotely", CompanyName: strings.TrimSpace(company), Title: strings.TrimSpace(title),
		URL: strings.TrimSpace(item.Link), Description: descriptionText(item.Description),
		PublishedAt: sourceTimestamp(item.PubDate), ExpiresAt: sourceTimestamp(strings.TrimSpace(item.ExpiresAt)),
		DescriptionStatus: "complete", RemoteStatus: "remote", Location: location,
		EmploymentType: strings.TrimSpace(item.Type), Metadata: map[string]string{},
	}
	if d.URL == "" && strings.HasPrefix(item.GUID, "http") {
		d.URL = strings.TrimSpace(item.GUID)
	}
	if item.Category != "" {
		d.Metadata["source_category"] = strings.TrimSpace(item.Category)
	}
	return d
}
