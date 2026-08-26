package seed

import (
	"context"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/anthonyalando8/prospector/internal/activity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
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
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
		} `xml:"item"`
	} `xml:"channel"`
}

// Seed fetches each category feed, splits WWR's "Company: Position" title format, and creates a
// company per unique employer — same guessed-domain-then-verify flow every US-source seeder in
// this package uses.
func (w *WeWorkRemotelySeeder) Seed(ctx context.Context, db *store.DB, redisConn *redis.Client) (int, error) {
	added := 0

	for _, feedURL := range weWorkRemotelyFeeds {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
		if err != nil {
			log.Printf("WeWorkRemotelySeeder: build request failed for %s: %v", feedURL, err)
			continue
		}
		req.Header.Set("User-Agent", w.userAgent)

		resp, err := w.client.Do(req)
		if err != nil {
			log.Printf("WeWorkRemotelySeeder: fetch failed for %s: %v", feedURL, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			log.Printf("WeWorkRemotelySeeder: HTTP %d from %s", resp.StatusCode, feedURL)
			continue
		}

		var feed wwrRSS
		err = xml.NewDecoder(resp.Body).Decode(&feed)
		resp.Body.Close()
		if err != nil {
			log.Printf("WeWorkRemotelySeeder: parse failed for %s: %v", feedURL, err)
			continue
		}

		for _, item := range feed.Channel.Items {
			// WWR's title format is consistently "Company: Position" — split on the first ": ".
			idx := strings.Index(item.Title, ": ")
			if idx == -1 {
				continue
			}
			companyName := strings.TrimSpace(item.Title[:idx])
			position := strings.TrimSpace(item.Title[idx+2:])
			if companyName == "" || position == "" || !isRelevantJobTitle(position) {
				continue
			}

			domain := extractDomain(companyName)
			if domain == "" {
				continue
			}

			extra := ""
			if doc, err := goquery.NewDocumentFromReader(strings.NewReader(item.Description)); err == nil {
				extra = strings.TrimSpace(doc.Text())
			}
			jobText := usJobSignalText(position, companyName, extra)

			company, created, err := w.CreateCompanyIfNew(ctx, db, redisConn, companyName, domain, "weworkremotely", item.Link, "candidate")
			if err != nil {
				log.Printf("WeWorkRemotelySeeder: failed to create company for %s: %v", companyName, err)
				continue
			}
			if !created {
				continue
			}

			added++
			activity.Log(ctx, redisConn, "worker:discover", fmt.Sprintf("Found %s (%s) — remote: %s", companyName, domain, position), true)

			jobURL := item.Link
			if _, err := db.CreateSignal(ctx, company.ID, "hiring_backend", jobText, &jobURL); err != nil {
				log.Printf("WeWorkRemotelySeeder: failed to save signal for %s: %v", companyName, err)
			}
		}
	}

	return added, nil
}
