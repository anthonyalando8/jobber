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
		Items []struct {
			PubDate     string `xml:"pubDate"`
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
		} `xml:"item"`
	} `xml:"channel"`
}

// Seed retains listings within the existing category feed scope, without fit filtering.
func (w *WeWorkRemotelySeeder) Seed(ctx context.Context, db *store.DB, _ *redis.Client) (int, error) {
	added := 0
	for _, url := range weWorkRemotelyFeeds {
		raw, err := w.fetchSource(ctx, url)
		if err != nil {
			return added, err
		}
		var feed wwrRSS
		if err = xml.Unmarshal(raw, &feed); err != nil {
			return added, err
		}
		for i, item := range feed.Channel.Items {
			if i >= maxFeedJobs {
				break
			}
			company, title, found := strings.Cut(item.Title, ": ")
			if !found {
				company = ""
				title = item.Title
			}
			d := opportunity.DiscoveredOpportunity{Source: "weworkremotely", CompanyName: strings.TrimSpace(company), Title: strings.TrimSpace(title), URL: item.Link, Description: descriptionText(item.Description), PublishedAt: sourceTimestamp(item.PubDate), DescriptionStatus: "complete", RemoteStatus: "remote"}
			result, err := IngestOpportunity(ctx, db, d)
			if err != nil {
				return added, err
			}
			if result.Created {
				added++
			}
		}
	}
	return added, nil
}
