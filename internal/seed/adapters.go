package seed

import (
	"context"
	"encoding/csv"
	"fmt"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"io"
	"log"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
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
	run := newBatch(j.Name())

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
				// Matches the employer profile link whether it is written relative ("/company/x")
				// or absolute ("https://www.brightermonday.co.ke/company/x").
				company: `a[href^="/company/"], a[href*="brightermonday.co.ke/company/"]`,
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
		raw, err := j.fetchListing(ctx, q.url)
		if !run.feed(q.url, err) {
			continue
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(raw)))
		if err != nil {
			run.feedsFailed++
			log.Printf("Parse error for %s: %v", q.site, err)
			continue
		}
		// A page that loads but matches no job card means the site's markup changed (or it served a
		// challenge page). Saying so is the difference between "no jobs today" and "this source is
		// broken", which the source status page needs to show.
		if doc.Find(q.card).Length() == 0 {
			run.feedsFailed++
			if run.firstErr == nil {
				run.firstErr = fmt.Errorf("%s: no job cards matched the page markup", q.site)
			}
			continue
		}
		base, _ := url.Parse(q.url)

		count := 0
		doc.Find(q.card).Each(func(i int, s *goquery.Selection) {
			if count >= 10 {
				return // Limit to 10 per page
			}

			count++
			companySel := s.Find(q.company).First()
			var companyName string
			if q.companyAttr != "" {
				companyName, _ = companySel.Attr(q.companyAttr)
			} else {
				companyName = companySel.Text()
			}
			companyName = cleanCompanyName(strings.TrimSpace(companyName))
			jobLinkSel := s.Find(q.jobLink).First()
			jobURL, _ := jobLinkSel.Attr("href")
			if u, err := base.Parse(jobURL); err == nil && jobURL != "" {
				jobURL = u.String()
			}
			html, _ := s.Html()
			d := opportunity.DiscoveredOpportunity{Source: q.site, CompanyName: companyName, Title: strings.TrimSpace(jobLinkSel.Text()), URL: jobURL, Description: descriptionText(html), DescriptionStatus: "partial", Metadata: map[string]string{"listing_url": q.url}}
			if companyName == "" || jobURL == "" {
				// This listing will be held back (no employer or no link). Keep the card's own markup
				// with it so the held-back view shows what the page actually contained; that is the
				// evidence needed to tell a markup change from a confidential employer.
				d.Metadata["listing_card_html"] = boundedCardHTML(html)
			}
			// Detail fetch failures retain the listing and its explicit partial status.
			d = j.completeJobDescription(ctx, d, base.Hostname())
			run.ingest(ctx, db, d)
		})
	}

	return run.result()
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

// domainForSource picks the domain to store for a newly discovered company.
//
// Order matters more than it looks: only the first candidate is stored, and verification runs
// later if at all. A URL the source itself published is stated fact and wins outright. Failing
// that, the generated guesses are ordered by the market the listing came from — preferring
// .co.ke for an international remote board produced domains like delinea.co.ke for a US company,
// which never resolved, so research never ran and no contact was ever found.
func domainForSource(d opportunity.DiscoveredOpportunity) string {
	for _, published := range []string{d.Metadata[opportunity.MetaEmployerURL], d.Metadata[opportunity.MetaApplyURL]} {
		if host := verify.DomainFromURL(published); host != "" {
			return host
		}
	}
	candidates := verify.CandidateDomainsForMarket(d.CompanyName, verify.MarketForSource(d.Source))
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

// maxCardHTML bounds the diagnostic card markup kept on a held-back listing.
const maxCardHTML = 4096

func boundedCardHTML(html string) string {
	html = strings.Join(strings.Fields(html), " ")
	if len(html) > maxCardHTML {
		html = strings.ToValidUTF8(html[:maxCardHTML], "") + "…"
	}
	return html
}
