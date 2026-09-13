// prospector-refresh corrects and re-researches companies whose stored domain was a bad guess.
//
// It uses the ordinary paths: the same verifier the worker uses, the same UpdateCompanyDomain,
// and the same enricher. It creates no companies and no opportunities, so it cannot produce a
// duplicate; it only corrects a domain on an existing row and then researches it. Historical
// research records are left alone — enrichment adds and updates evidence, it does not delete.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/anthonyalando8/prospector/internal/enrich"
	"github.com/anthonyalando8/prospector/internal/queue"
	"github.com/anthonyalando8/prospector/internal/relevance"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/anthonyalando8/prospector/internal/verify"
	"github.com/joho/godotenv"
)

// Outcome is what happened to one company.
type Outcome struct {
	CompanyID      int64  `json:"company_id"`
	Company        string `json:"company"`
	Source         string `json:"source"`
	DomainBefore   string `json:"domain_before"`
	DomainAfter    string `json:"domain_after"`
	StatusBefore   string `json:"status_before"`
	StatusAfter    string `json:"status_after"`
	DomainChanged  bool   `json:"domain_changed"`
	Researched     bool   `json:"researched"`
	ContactsBefore int    `json:"contacts_before"`
	ContactsAfter  int    `json:"contacts_after"`
	Recruiting     int    `json:"identified_recruiting_contacts"`
	Engineering    int    `json:"identified_engineering_contacts"`
	Acceptable     bool   `json:"has_acceptable_contact"`
	Note           string `json:"note,omitempty"`
}

// Summary is the tally §7 asks for.
type Summary struct {
	StartedAt         time.Time `json:"started_at"`
	FinishedAt        time.Time `json:"finished_at"`
	Considered        int       `json:"considered"`
	DomainsCorrected  int       `json:"domains_corrected"`
	Researched        int       `json:"researched"`
	GainedContacts    int       `json:"gained_any_contact"`
	GainedRecruiting  int       `json:"gained_identified_recruiting_contact"`
	GainedEngineering int       `json:"gained_identified_engineering_contact"`
	WithAcceptable    int       `json:"with_acceptable_contact"`
	StillNoContact    int       `json:"still_no_acceptable_contact"`
	Outcomes          []Outcome `json:"outcomes"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("prospector-refresh", flag.ContinueOnError)
	limit := flags.Int("limit", 250, "maximum companies to process")
	out := flags.String("out", "", "write the summary as JSON here")
	dryRun := flags.Bool("dry-run", false, "report what would be attempted, change nothing")
	research := flags.Bool("research", true, "run enrichment on companies whose domain resolved")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return errors.New("could not load environment file")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()

	db, err := store.NewDB(ctx, dsn)
	if err != nil {
		return errors.New("database connection failed (connection string withheld)")
	}
	defer db.Close()

	userAgent := os.Getenv("CRAWL_USER_AGENT")
	if userAgent == "" {
		userAgent = "Prospector/1.0"
	}
	verifier := verify.NewHeuristicVerifier(userAgent)

	var enricher *enrich.Enricher
	if *research {
		redisURL := os.Getenv("REDIS_URL")
		if redisURL == "" {
			return errors.New("REDIS_URL is required for research; pass -research=false to skip it")
		}
		redisConn, err := queue.NewRedisPool(redisURL)
		if err != nil {
			return errors.New("redis connection failed")
		}
		defer redisConn.Close()
		enricher = enrich.NewEnricher(redisConn, 1200, userAgent)
	}

	companies, err := affectedCompanies(ctx, db, *limit)
	if err != nil {
		return err
	}
	summary := Summary{StartedAt: time.Now().UTC(), Considered: len(companies)}
	fmt.Printf("Refreshing %d internationally-sourced companies with a guessed domain\n", len(companies))
	if *dryRun {
		for _, c := range companies {
			fmt.Printf("  would attempt %-36.36s %-26s %s\n", c.Name, c.Domain, c.Source)
		}
		return nil
	}

	for i, company := range companies {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		outcome := Outcome{CompanyID: company.ID, Company: company.Name, Source: company.Source,
			DomainBefore: company.Domain, DomainAfter: company.Domain,
			StatusBefore: company.DomainStatus, StatusAfter: company.DomainStatus}
		outcome.ContactsBefore = countContacts(ctx, db, company.ID)

		// Try the market-ordered candidates through the same verifier the worker uses.
		resolved := ""
		for _, candidate := range verify.CandidateDomainsForMarket(company.Name, verify.MarketForSource(company.Source)) {
			status, confidence, err := verifier.Verify(ctx, verify.Company{Name: company.Name, Domain: candidate})
			if err != nil {
				continue
			}
			if status == verify.StatusVerified {
				resolved = candidate
				if candidate != company.Domain {
					if err := db.UpdateCompanyDomain(ctx, company.ID, candidate); err != nil {
						outcome.Note = "domain correction failed"
						break
					}
					outcome.DomainChanged = true
					summary.DomainsCorrected++
				}
				if err := db.UpdateCompanyDomainStatus(ctx, company.ID, string(verify.StatusVerified), confidence); err == nil {
					outcome.StatusAfter = string(verify.StatusVerified)
				}
				outcome.DomainAfter = candidate
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if resolved == "" {
			outcome.Note = "no candidate domain resolved to a matching site"
			summary.Outcomes = append(summary.Outcomes, finish(ctx, db, outcome, &summary))
			progress(i, len(companies))
			continue
		}

		if enricher != nil {
			refreshed, err := db.GetCompany(ctx, company.ID)
			if err == nil {
				if _, err := enricher.EnrichCompany(ctx, refreshed, db); err != nil {
					outcome.Note = "research failed"
				} else {
					outcome.Researched = true
					summary.Researched++
				}
			}
		}
		summary.Outcomes = append(summary.Outcomes, finish(ctx, db, outcome, &summary))
		progress(i, len(companies))
	}

	summary.FinishedAt = time.Now().UTC()
	printSummary(summary)
	if *out != "" {
		data, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out, append(data, '\n'), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// finish counts what the company ended up with, judged by the same rules the send path uses.
func finish(ctx context.Context, db *store.DB, outcome Outcome, summary *Summary) Outcome {
	contacts, err := db.GetContactsByCompany(ctx, outcome.CompanyID)
	if err != nil {
		return outcome
	}
	outcome.ContactsAfter = len(contacts)
	for _, c := range contacts {
		if c.Kind != "email" || c.Status == "bad" {
			continue
		}
		if !relevance.ClassifyEndpoint(c.Value).Contactable {
			continue
		}
		contact := relevance.Contact{ID: c.ID, Kind: c.Kind, Value: c.Value, Quality: c.Quality, Status: c.Status}
		if c.PersonName != nil {
			contact.Person = *c.PersonName
		}
		if c.Role != nil {
			contact.Role = *c.Role
		}
		switch relevance.ClassifyContact(contact).Function {
		case relevance.FunctionRecruiting:
			outcome.Recruiting++
			outcome.Acceptable = true
		case relevance.FunctionEngineering:
			outcome.Engineering++
			outcome.Acceptable = true
		}
	}
	if outcome.ContactsAfter > outcome.ContactsBefore {
		summary.GainedContacts++
	}
	if outcome.Recruiting > 0 {
		summary.GainedRecruiting++
	}
	if outcome.Engineering > 0 {
		summary.GainedEngineering++
	}
	if outcome.Acceptable {
		summary.WithAcceptable++
	} else {
		summary.StillNoContact++
	}
	return outcome
}

// affectedCompanies are internationally-sourced companies still carrying a guessed Kenyan domain,
// which is exactly the population the readiness investigation identified.
func affectedCompanies(ctx context.Context, db *store.DB, limit int) ([]*store.Company, error) {
	rows, err := db.Query(ctx, `
		SELECT id FROM company
		 WHERE source IN ('remoteok','weworkremotely','remotive','himalayas')
		   AND domain LIKE '%.co.ke'
		   AND domain_status NOT IN ('verified','manual')
		 ORDER BY id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	companies := make([]*store.Company, 0, len(ids))
	for _, id := range ids {
		company, err := db.GetCompany(ctx, id)
		if err != nil {
			continue
		}
		companies = append(companies, company)
	}
	return companies, nil
}

func countContacts(ctx context.Context, db *store.DB, companyID int64) int {
	contacts, err := db.GetContactsByCompany(ctx, companyID)
	if err != nil {
		return 0
	}
	return len(contacts)
}

func progress(i, total int) {
	if (i+1)%10 == 0 || i+1 == total {
		log.Printf("refresh: %d/%d", i+1, total)
	}
}

func printSummary(s Summary) {
	fmt.Printf("\nRefresh finished in %s\n", s.FinishedAt.Sub(s.StartedAt).Round(time.Second))
	fmt.Printf("  considered                              %4d\n", s.Considered)
	fmt.Printf("  domains corrected                       %4d\n", s.DomainsCorrected)
	fmt.Printf("  researched                              %4d\n", s.Researched)
	fmt.Printf("  gained any contact                      %4d\n", s.GainedContacts)
	fmt.Printf("  gained identified recruiting contact    %4d\n", s.GainedRecruiting)
	fmt.Printf("  gained identified engineering contact   %4d\n", s.GainedEngineering)
	fmt.Printf("  now have an acceptable contact          %4d\n", s.WithAcceptable)
	fmt.Printf("  still have no acceptable contact        %4d\n", s.StillNoContact)

	shown := 0
	for _, o := range s.Outcomes {
		if !o.DomainChanged && !o.Acceptable {
			continue
		}
		if shown == 0 {
			fmt.Println("\nCorrected or newly contactable:")
		}
		fmt.Printf("  %-34.34s %-24s -> %-24s contacts %d->%d%s\n",
			o.Company, o.DomainBefore, o.DomainAfter, o.ContactsBefore, o.ContactsAfter,
			acceptableMark(o.Acceptable))
		shown++
	}
	if shown == 0 {
		fmt.Println("\nNo company gained a corrected domain or an acceptable contact.")
	}
}

func acceptableMark(acceptable bool) string {
	if acceptable {
		return "  ACCEPTABLE CONTACT"
	}
	return ""
}
