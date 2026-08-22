package seed

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/anthonyalando8/prospector/internal/activity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/anthonyalando8/prospector/internal/verify"
	"github.com/redis/go-redis/v9"
)

// jobBoardQuery describes one job board search page and the site-specific selectors needed
// to pull job cards out of it. Each site renders differently, so a single generic selector
// set (the old approach) silently matched nothing on some sites — these were checked against
// the live markup directly.
type jobBoardQuery struct {
	site    string
	term    string
	url     string
	card    string // selector for one job card, relative to the document
	jobLink string // selector for the job title link, relative to the card
	company string // selector for the company name element, relative to the card
	// companyAttr, if set, reads the company name from this attribute (e.g. "alt" on a logo
	// image) instead of the element's text.
	companyAttr string
}

// JobBoardSeeder scrapes job postings from Kenya job boards
type JobBoardSeeder struct {
	*BaseSeeder
}

// NewJobBoardSeeder creates a job board seeder
func NewJobBoardSeeder(userAgent string) *JobBoardSeeder {
	return &JobBoardSeeder{
		BaseSeeder: NewBaseSeeder(userAgent),
	}
}

// Name returns the seeder name
func (j *JobBoardSeeder) Name() string {
	return "jobboard"
}

// Seed scrapes job board listings
func (j *JobBoardSeeder) Seed(ctx context.Context, db *store.DB, redis *redis.Client) (int, error) {
	added := 0

	// searchTerms covers the roles/stacks likely to signal exactly the backend/API/payments
	// work this tool is prospecting for. brightermonday.co.ke and myjobmag.co.ke serve plain
	// HTTP and don't bot-block; fuzu.com, clutch.co and goodfirms.co were dropped — all three
	// return 403 to a non-browser client, which a CSS selector fix can't solve.
	searchTerms := []string{
		"backend", "golang", "django", "python", "node", "nodejs", "php", "laravel",
		"software+engineer", "software+developer",
	}

	var queries []jobBoardQuery
	for _, term := range searchTerms {
		queries = append(queries,
			jobBoardQuery{
				site: "brightermonday.co.ke", term: term,
				url:     "https://www.brightermonday.co.ke/jobs?q=" + term,
				card:    `div[data-cy="listing-cards-components"]`,
				jobLink: `a[data-cy="listing-title-link"]`,
				company: `a[href^="/company/"]`,
			},
			jobBoardQuery{
				site: "myjobmag.co.ke", term: term,
				url:         "https://www.myjobmag.co.ke/jobs?q=" + term,
				card:        "li.job-list-li",
				jobLink:     "li.job-info h2 a",
				company:     "li.job-logo a img",
				companyAttr: "alt",
			},
		)
	}

	for _, q := range queries {
		resp, err := j.Fetch(ctx, q.url)
		if err != nil {
			log.Printf("Failed to fetch %s (%s): %v", q.site, q.term, err)
			continue
		}

		doc, err := goquery.NewDocumentFromReader(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("Parse error for %s: %v", q.site, err)
			continue
		}
		base := resp.Request.URL

		count := 0
		doc.Find(q.card).Each(func(i int, s *goquery.Selection) {
			if count > 10 {
				return // Limit to 10 per page
			}

			companySel := s.Find(q.company).First()
			var companyName string
			if q.companyAttr != "" {
				companyName, _ = companySel.Attr(q.companyAttr)
			} else {
				companyName = companySel.Text()
			}
			companyName = cleanCompanyName(strings.TrimSpace(companyName))
			if companyName == "" {
				return
			}

			jobLinkSel := s.Find(q.jobLink).First()
			jobURL, _ := jobLinkSel.Attr("href")
			jobText := strings.TrimSpace(s.Text())

			if jobURL == "" {
				return
			}

			if u, err := base.Parse(jobURL); err == nil {
				jobURL = u.String()
			}

			// Normalize domain
			domain := extractDomain(companyName)
			if domain == "" {
				return
			}

			// Create company with job posting as signal
			// "candidate": extractDomain is a guessed slug of the display name, never trusted
			// directly — it goes through internal/verify before enrichment will touch it.
			company, created, _ := j.CreateCompanyIfNew(ctx, db, redis, companyName, domain, "jobboard", q.url, "candidate")
			if created {
				added++
				activity.Log(ctx, redis, "worker:discover", fmt.Sprintf("Found %s (%s) — %s listing", companyName, domain, q.site), true)

				// Store job posting as signal
				_, _ = db.CreateSignal(ctx, company.ID, "hiring_backend", jobText[:min(300, len(jobText))], &jobURL)
				count++
			}
		})
	}

	return added, nil
}

// confidentialEmployerPhrases catches the placeholder client name some job boards show when a
// recruiting agency posts on behalf of an employer without disclosing who it is ("Accounts
// Assistant at a Reputable Company"). There is no real, identifiable company here — no domain
// search would ever find one — so these are skipped outright rather than turned into a company
// record that can never be verified or contacted.
var confidentialEmployerPhrases = []string{
	"reputable company", "confidential client", "our client", "leading company", "leading firm",
}

// cleanCompanyName recovers the real company name from noisy scraped text. Some sources'
// "company" field is actually the whole listing headline ("<job title> at <Company>") rather
// than just the company name — a real example: "Procurement Officer at City Eye Hospital" got
// domain-guessed as "procurementofficercityeyehospital.co.ke", which was never going to resolve.
// Returns "" for a confidential-employer placeholder, since there's nothing real to extract.
func cleanCompanyName(raw string) string {
	name := strings.TrimSpace(raw)
	lower := strings.ToLower(name)
	for _, phrase := range confidentialEmployerPhrases {
		if strings.Contains(lower, phrase) {
			return ""
		}
	}
	if idx := strings.LastIndex(lower, " at "); idx != -1 {
		name = strings.TrimSpace(name[idx+len(" at "):])
	}
	return name
}

// extractDomain guesses a starting domain from a company name — just the first, most likely
// candidate (see internal/verify.CandidateDomains for the full ranked list and why a single
// hyphen-joined ".com" guess missed most real Kenyan business domains). This is only ever a
// starting point: the company is created with domain_status="candidate", so the verify loop
// will confirm it or work through the rest of the candidate list before anything gets enriched
// against it.
func extractDomain(name string) string {
	candidates := verify.CandidateDomains(name)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ManualSeeder parses CSV paste
type ManualSeeder struct {
	*BaseSeeder
	CSV string
}

// NewManualSeeder creates a manual seeder
func NewManualSeeder(csv string, userAgent string) *ManualSeeder {
	return &ManualSeeder{
		BaseSeeder: NewBaseSeeder(userAgent),
		CSV:        csv,
	}
}

// Name returns the seeder name
func (m *ManualSeeder) Name() string {
	return "manual"
}

var validManualSegments = map[string]bool{
	"agency": true, "incubator": true, "hiring": true, "sme": true,
}

// Seed parses CSV rows: name,domain,segment,notes. segment and notes are optional — a plain
// name,domain row still works. Uses encoding/csv so a notes field containing a comma just needs
// to be quoted ("like this, with a comma") rather than corrupting column alignment.
//
// Reads row by row rather than ReadAll: LLM-generated CSV often has a stray malformed line
// trailing the real data (e.g. a footnote/reference block with an unescaped quote) — one bad
// line must not throw away every good row that came before it.
func (m *ManualSeeder) Seed(ctx context.Context, db *store.DB, redis *redis.Client) (int, error) {
	added := 0
	skipped := 0

	reader := csv.NewReader(strings.NewReader(m.CSV))
	reader.FieldsPerRecord = -1 // rows may have 2, 3, or 4 columns
	reader.TrimLeadingSpace = true

	for {
		parts, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			skipped++
			log.Printf("ManualSeeder: skipping malformed CSV row: %v", err)
			continue
		}

		if len(parts) < 2 {
			continue
		}

		name := strings.TrimSpace(parts[0])
		domain := strings.TrimSpace(parts[1])
		if name == "" || domain == "" || strings.HasPrefix(name, "#") {
			continue
		}
		// Tolerate a header row like "name,domain,segment,notes".
		if strings.EqualFold(name, "name") && strings.EqualFold(domain, "domain") {
			continue
		}

		segment := ""
		if len(parts) >= 3 {
			s := strings.ToLower(strings.TrimSpace(parts[2]))
			if validManualSegments[s] {
				segment = s
			}
		}

		notes := ""
		if len(parts) >= 4 {
			notes = strings.TrimSpace(parts[3])
		}

		// "manual": a human typed this domain into the CSV — trust it, no verification needed.
		company, created, _ := m.CreateCompanyIfNew(ctx, db, redis, name, domain, "manual", "", "manual")
		if !created {
			continue
		}

		if segment != "" {
			// nil breakdown: this is just tagging a segment on import, not a real scoring pass —
			// the scorer will fill in score_breakdown once this company is actually scored.
			_ = db.UpdateCompanyScore(ctx, company.ID, 0, segment, nil)
		}
		if notes != "" {
			_ = db.UpdateCompanyNotes(ctx, company.ID, notes)
		}

		added++
	}

	if skipped > 0 {
		log.Printf("ManualSeeder: added %d companies, skipped %d malformed row(s)", added, skipped)
	}

	return added, nil
}
