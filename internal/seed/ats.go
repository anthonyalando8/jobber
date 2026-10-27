package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
)

// ATS boards are employers' own applicant tracking systems. Greenhouse, Lever and Ashby each
// publish a documented, unauthenticated job-board API intended for exactly this use (employers
// embed it in their own careers pages), so reading one is reading the employer's own listing, with
// the employer's own application link, rather than a copy on an intermediary.
//
// Which boards to read comes from two places:
//   - ATS_BOARDS, a comma-separated list such as "greenhouse:gitlab,lever:acme,ashby:example".
//   - Any board a stored listing already links to. When a job found on Remote OK applies through
//     jobs.lever.co/acme, that employer's whole board is worth reading, and the link is evidence
//     that the board is real. No board is guessed.
const maxATSBoardsPerRun = 30

var atsBoardPatterns = map[string]*regexp.Regexp{
	"greenhouse": regexp.MustCompile(`^https?://(?:boards|job-boards)(?:\.eu)?\.greenhouse\.io/(?:embed/job_board\?for=)?([A-Za-z0-9_-]+)`),
	"lever":      regexp.MustCompile(`^https?://jobs\.(?:eu\.)?lever\.co/([A-Za-z0-9_.-]+)`),
	"ashby":      regexp.MustCompile(`^https?://jobs\.ashbyhq\.com/([A-Za-z0-9_.%-]+)`),
}

// ATSBoard is one employer board.
type ATSBoard struct {
	Provider, Slug string
	// Company is the employer name known from the listing that linked to this board, if any.
	Company string
}

// BoardFromURL recognises an employer ATS board from any link into it.
func BoardFromURL(raw string) (ATSBoard, bool) {
	for provider, pattern := range atsBoardPatterns {
		if m := pattern.FindStringSubmatch(strings.TrimSpace(raw)); m != nil {
			slug, err := url.PathUnescape(m[1])
			if err != nil || slug == "" || slug == "embed" {
				return ATSBoard{}, false
			}
			return ATSBoard{Provider: provider, Slug: slug}, true
		}
	}
	return ATSBoard{}, false
}

// ParseATSBoards reads the ATS_BOARDS configuration.
func ParseATSBoards(raw string) []ATSBoard {
	out := []ATSBoard{}
	for _, item := range strings.Split(raw, ",") {
		provider, slug, ok := strings.Cut(strings.TrimSpace(item), ":")
		provider = strings.ToLower(strings.TrimSpace(provider))
		slug = strings.TrimSpace(slug)
		if !ok || slug == "" || atsBoardPatterns[provider] == nil {
			continue
		}
		out = append(out, ATSBoard{Provider: provider, Slug: slug})
	}
	return out
}

// ATSSeeder reads employer boards on Greenhouse, Lever and Ashby.
type ATSSeeder struct {
	*BaseSeeder
	configured []ATSBoard
	// pause spaces requests to the same provider.
	pause time.Duration
}

// NewATSSeeder creates the employer-board seeder from ATS_BOARDS.
func NewATSSeeder(userAgent string) *ATSSeeder {
	return &ATSSeeder{BaseSeeder: NewBaseSeeder(userAgent), configured: ParseATSBoards(os.Getenv("ATS_BOARDS")), pause: 750 * time.Millisecond}
}

// Name returns the seeder name.
func (a *ATSSeeder) Name() string { return "ats" }

// Boards is the configured boards plus every board a stored listing links to, deduplicated.
func (a *ATSSeeder) Boards(ctx context.Context, db *store.DB) []ATSBoard {
	seen := map[string]bool{}
	out := []ATSBoard{}
	add := func(b ATSBoard) {
		key := b.Provider + ":" + strings.ToLower(b.Slug)
		if !seen[key] {
			seen[key] = true
			out = append(out, b)
		}
	}
	for _, b := range a.configured {
		add(b)
	}
	if db != nil {
		links, err := db.ATSLinks(ctx)
		if err != nil {
			log.Printf("seed ats: reading discovered board links: %v", err)
		}
		sort.Slice(links, func(i, j int) bool { return links[i].URL < links[j].URL })
		for _, link := range links {
			if b, ok := BoardFromURL(link.URL); ok {
				b.Company = link.Company
				add(b)
			}
		}
	}
	return out
}

// Seed reads each board, keeping engineering roles only.
func (a *ATSSeeder) Seed(ctx context.Context, db *store.DB, _ *redis.Client) (int, error) {
	run := newBatch(a.Name())
	boards := a.Boards(ctx, db)
	if len(boards) > maxATSBoardsPerRun {
		boards = boards[:maxATSBoardsPerRun]
	}
	for i, board := range boards {
		if i > 0 && a.pause > 0 {
			select {
			case <-ctx.Done():
				return run.result()
			case <-time.After(a.pause):
			}
		}
		listings, err := a.readBoard(ctx, board)
		if !run.feed(board.Provider+":"+board.Slug, err) {
			continue
		}
		for _, d := range listings {
			if engineeringTitle(d.Title) {
				run.ingest(ctx, db, d)
			}
		}
	}
	return run.result()
}

func (a *ATSSeeder) readBoard(ctx context.Context, b ATSBoard) ([]opportunity.DiscoveredOpportunity, error) {
	switch b.Provider {
	case "greenhouse":
		return a.greenhouse(ctx, b)
	case "lever":
		return a.lever(ctx, b)
	case "ashby":
		return a.ashby(ctx, b)
	}
	return nil, fmt.Errorf("unknown ATS provider %q", b.Provider)
}

// companyName prefers the name a listing already gave the employer, so a board discovered from
// a Remote OK job joins the same company record instead of creating a near-duplicate.
func companyName(b ATSBoard, published string) string {
	if strings.TrimSpace(b.Company) != "" {
		return strings.TrimSpace(b.Company)
	}
	if strings.TrimSpace(published) != "" {
		return strings.TrimSpace(published)
	}
	words := strings.FieldsFunc(b.Slug, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func atsOpportunity(source, company, id, title, link, apply, location, employment, description string, published *time.Time, remote bool) opportunity.DiscoveredOpportunity {
	d := opportunity.DiscoveredOpportunity{
		Source: source, ExternalID: id, CompanyName: company, Title: strings.TrimSpace(title), URL: link,
		Location: strings.TrimSpace(location), EmploymentType: strings.TrimSpace(employment),
		Description: description, PublishedAt: published, DescriptionStatus: "complete",
		RemoteStatus: "unknown", Metadata: map[string]string{},
	}
	if remote {
		d.RemoteStatus = "remote"
	}
	if apply == "" {
		apply = link
	}
	// The employer's own board is the published application route.
	opportunity.RecordPublishedRoute(d.Metadata, source+":board", "", apply, "", "")
	return d
}

func (a *ATSSeeder) greenhouse(ctx context.Context, b ATSBoard) ([]opportunity.DiscoveredOpportunity, error) {
	base := "https://boards-api.greenhouse.io/v1/boards/" + url.PathEscape(b.Slug)
	raw, err := a.fetchListing(ctx, base+"/jobs?content=true")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Jobs []struct {
			ID          json.RawMessage `json:"id"`
			Title       string          `json:"title"`
			AbsoluteURL string          `json:"absolute_url"`
			UpdatedAt   string          `json:"updated_at"`
			Content     string          `json:"content"`
			Location    struct {
				Name string `json:"name"`
			} `json:"location"`
		} `json:"jobs"`
	}
	if err = json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("greenhouse %s: unexpected response: %w", b.Slug, err)
	}
	name := b.Company
	if name == "" {
		// The board's display name is a separate, cheap request; the slug is the fallback.
		if meta, err := a.fetchSource(ctx, base); err == nil {
			var board struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(meta, &board) == nil {
				name = board.Name
			}
		}
	}
	name = companyName(ATSBoard{Slug: b.Slug, Company: name}, "")
	out := []opportunity.DiscoveredOpportunity{}
	for _, job := range parsed.Jobs {
		location := job.Location.Name
		out = append(out, atsOpportunity("greenhouse", name, externalID(job.ID), job.Title, job.AbsoluteURL, "", location, "",
			descriptionText(html.UnescapeString(job.Content)), sourceTimestamp(job.UpdatedAt), strings.Contains(strings.ToLower(location), "remote")))
	}
	return out, nil
}

func (a *ATSSeeder) lever(ctx context.Context, b ATSBoard) ([]opportunity.DiscoveredOpportunity, error) {
	raw, err := a.fetchListing(ctx, "https://api.lever.co/v0/postings/"+url.PathEscape(b.Slug)+"?mode=json")
	if err != nil {
		return nil, err
	}
	var parsed []struct {
		ID               string `json:"id"`
		Text             string `json:"text"`
		HostedURL        string `json:"hostedUrl"`
		ApplyURL         string `json:"applyUrl"`
		DescriptionPlain string `json:"descriptionPlain"`
		AdditionalPlain  string `json:"additionalPlain"`
		CreatedAt        int64  `json:"createdAt"`
		WorkplaceType    string `json:"workplaceType"`
		Lists            []struct {
			Text    string `json:"text"`
			Content string `json:"content"`
		} `json:"lists"`
		Categories struct {
			Location   string `json:"location"`
			Commitment string `json:"commitment"`
		} `json:"categories"`
	}
	if err = json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("lever %s: unexpected response: %w", b.Slug, err)
	}
	name := companyName(b, "")
	out := []opportunity.DiscoveredOpportunity{}
	for _, job := range parsed {
		parts := []string{job.DescriptionPlain}
		for _, list := range job.Lists {
			parts = append(parts, list.Text, descriptionText(list.Content))
		}
		parts = append(parts, job.AdditionalPlain)
		var published *time.Time
		if job.CreatedAt > 0 {
			t := time.UnixMilli(job.CreatedAt).UTC()
			published = &t
		}
		remote := strings.EqualFold(job.WorkplaceType, "remote") || strings.Contains(strings.ToLower(job.Categories.Location), "remote")
		out = append(out, atsOpportunity("lever", name, job.ID, job.Text, job.HostedURL, job.ApplyURL, job.Categories.Location,
			job.Categories.Commitment, strings.TrimSpace(strings.Join(parts, "\n\n")), published, remote))
	}
	return out, nil
}

func (a *ATSSeeder) ashby(ctx context.Context, b ATSBoard) ([]opportunity.DiscoveredOpportunity, error) {
	raw, err := a.fetchListing(ctx, "https://api.ashbyhq.com/posting-api/job-board/"+url.PathEscape(b.Slug))
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Jobs []struct {
			ID               string `json:"id"`
			Title            string `json:"title"`
			Location         string `json:"location"`
			IsRemote         bool   `json:"isRemote"`
			IsListed         *bool  `json:"isListed"`
			EmploymentType   string `json:"employmentType"`
			DescriptionPlain string `json:"descriptionPlain"`
			DescriptionHTML  string `json:"descriptionHtml"`
			PublishedAt      string `json:"publishedAt"`
			JobURL           string `json:"jobUrl"`
			ApplyURL         string `json:"applyUrl"`
		} `json:"jobs"`
	}
	if err = json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("ashby %s: unexpected response: %w", b.Slug, err)
	}
	name := companyName(b, "")
	out := []opportunity.DiscoveredOpportunity{}
	for _, job := range parsed.Jobs {
		if job.IsListed != nil && !*job.IsListed {
			continue
		}
		description := job.DescriptionPlain
		if description == "" {
			description = descriptionText(job.DescriptionHTML)
		}
		out = append(out, atsOpportunity("ashby", name, job.ID, job.Title, job.JobURL, job.ApplyURL, job.Location,
			job.EmploymentType, description, sourceTimestamp(job.PublishedAt), job.IsRemote))
	}
	return out, nil
}
