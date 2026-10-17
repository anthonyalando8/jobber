// prospector-contactsearch runs contact discovery over the real corpus and reports exactly what
// it found, starting with the companies that are actually blocked: strong matches with nowhere
// acceptable to send.
//
// It sends nothing. It creates no contact plan, approves nothing, and opens no SMTP connection —
// there is no code path from this binary to a transmission. What it does write is contact data:
// candidates with their provenance, and, for addresses the employer itself published, contact
// rows. -dry-run stops short of even that, reporting what would be discovered without storing
// anything, which is the right first pass against a live corpus.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/anthonyalando8/prospector/internal/contactsearch"
	"github.com/anthonyalando8/prospector/internal/enrich"
	"github.com/anthonyalando8/prospector/internal/queue"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/joho/godotenv"
)

// Run is the record of one rehearsal pass, in the terms the completion report asks for.
type Run struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	DryRun     bool      `json:"dry_run"`
	Provider   string    `json:"search_provider"`
	ProviderOn bool      `json:"search_provider_configured"`
	Inference  bool      `json:"email_inference_enabled"`

	CompaniesExamined int `json:"companies_examined"`
	SearchesRun       int `json:"searches_run"`
	PagesFetched      int `json:"pages_fetched"`

	ApplicationRoutesFound int `json:"published_application_routes_found"`
	PublishedEmailsFound   int `json:"published_emails_found"`
	RecruitingPeopleFound  int `json:"recruiting_people_found"`
	EngineeringPeopleFound int `json:"engineering_people_found"`
	PeopleWithoutEmail     int `json:"people_found_without_email"`
	InferredAddresses      int `json:"inferred_addresses"`
	PromotedToContacts     int `json:"promoted_to_contacts"`

	GainedDestination int `json:"companies_that_gained_an_acceptable_destination"`
	StillUnresolved   int `json:"companies_still_unresolved"`
	IdentityBlocked   int `json:"companies_refused_on_employer_identity"`
	ProviderFailures  int `json:"search_provider_failures"`
	// SearchesSkipped counts companies where no search engine was consulted at all — most often
	// because none is configured. That is a capability gap, not a failure, and conflating the two
	// hides how much of the corpus was never actually searched.
	SearchesSkipped int `json:"companies_with_no_search_engine_consulted"`

	Companies []CompanyReport `json:"companies"`
	StopEarly string          `json:"stopped_early,omitempty"`
	// EmailsSent is always zero. It is reported rather than assumed so the answer is on the
	// record rather than in someone's memory of what this binary does.
	EmailsSent int `json:"emails_sent"`
}

// CompanyReport is one company's outcome, readable without the full candidate dump.
type CompanyReport struct {
	CompanyID   int64  `json:"company_id"`
	Company     string `json:"company"`
	Domain      string `json:"domain"`
	State       string `json:"state"`
	Blocker     string `json:"identity_blocker,omitempty"`
	Skipped     string `json:"skipped,omitempty"`
	GapBefore   string `json:"gap_before,omitempty"`
	GapAfter    string `json:"gap_after,omitempty"`
	Destination string `json:"outreach_destination,omitempty"`
	Acceptable  bool   `json:"outreach_destination_acceptable"`
	// ApplicationDestination is where an application to this company's vacancy would go. It is
	// reported separately because it answers a different question from the outreach destination,
	// and a company can be ready for one and not the other.
	ApplicationDestination string   `json:"application_destination,omitempty"`
	ApplicationReady       bool     `json:"application_destination_ready"`
	Findings               []string `json:"findings,omitempty"`
	Failure                string   `json:"search_failure,omitempty"`
	SearchNote             string   `json:"search_note,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "contact search rehearsal failed:", err)
		os.Exit(1)
	}
}

func run() error {
	limit := flag.Int("limit", 10, "how many companies to examine")
	dryRun := flag.Bool("dry-run", false, "report what would be discovered without storing anything")
	company := flag.Int64("company", 0, "examine one company by id instead of the eligible queue")
	verbose := flag.Bool("verbose", false, "include every candidate and its evidence in the output")
	flag.Parse()

	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return err
	}
	ctx := context.Background()
	db, err := store.NewDB(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()

	redisConn, err := queue.NewRedisPool(os.Getenv("REDIS_URL"))
	if err != nil {
		return err
	}
	defer redisConn.Close()

	enricher := enrich.NewEnricher(redisConn, 1200, envOr("CRAWL_USER_AGENT", "ProspectorBot/1.0"))
	provider := contactsearch.NewGoogleProvider(os.Getenv("GOOGLE_SEARCH_API_KEY"), os.Getenv("GOOGLE_SEARCH_CX"))
	budget := contactsearch.NewBudget(redisConn, contactsearch.BudgetConfig{
		CompaniesPerDay: *limit * 2, SearchesPerDay: *limit * 4, FetchesPerDay: *limit * 10,
	})
	cfg := contactsearch.DefaultConfig()
	cfg.Owner = "contactsearch-rehearsal"
	cfg.Inference.Enabled = os.Getenv("CONTACT_DISCOVERY_INFER_EMAILS") == "true"

	report := Run{StartedAt: time.Now().UTC(), DryRun: *dryRun,
		Provider: provider.Name(), ProviderOn: provider.Enabled(), Inference: cfg.Inference.Enabled}

	if *dryRun {
		// A dry run reports the queue and the identity gate without touching the network or the
		// database. It answers "which companies would be searched, and would any of them be
		// refused" before a single request leaves this machine.
		return dryRunReport(ctx, db, *limit, report)
	}

	engine := contactsearch.NewEngine(db, enricher, provider, budget, cfg)

	var results []contactsearch.Result
	if *company > 0 {
		result, err := engine.DiscoverCompany(ctx, *company)
		if err != nil {
			return err
		}
		results = append(results, result)
	} else {
		batch, err := engine.RunBatch(ctx, *limit)
		if err != nil {
			return err
		}
		results, report.StopEarly = batch.Results, batch.StopEarly
	}

	for _, result := range results {
		report.CompaniesExamined++
		report.SearchesRun += result.SearchesRun
		report.PagesFetched += result.PagesFetched
		report.ApplicationRoutesFound += result.RoutesFound
		report.PublishedEmailsFound += result.PublishedEmails
		report.RecruitingPeopleFound += result.RecruitersFound
		report.EngineeringPeopleFound += result.EngineersFound
		report.PeopleWithoutEmail += result.PeopleWithoutEmail
		report.InferredAddresses += result.InferredAddresses
		report.PromotedToContacts += result.Promoted
		if result.Identity.Blocker != "" {
			report.IdentityBlocked++
		}
		if result.SearchFailure != "" {
			report.ProviderFailures++
		}
		if result.SearchNote != "" {
			report.SearchesSkipped++
		}
		if result.Resolved() {
			report.GainedDestination++
		} else {
			report.StillUnresolved++
		}
		report.Companies = append(report.Companies, companyReport(result, *verbose))
	}
	report.FinishedAt = time.Now().UTC()
	return print(report)
}

func companyReport(result contactsearch.Result, verbose bool) CompanyReport {
	item := CompanyReport{
		CompanyID: result.CompanyID, Company: result.CompanyName, Domain: result.Domain,
		State: result.State, Blocker: result.Identity.Blocker, Skipped: result.Skipped,
		GapBefore: result.GapBefore, GapAfter: result.GapAfter, Failure: result.SearchFailure,
		SearchNote: result.SearchNote,
	}
	if result.DestinationAfter != nil {
		item.Destination = result.DestinationAfter.Label + " (" + result.DestinationAfter.Value + ")"
		item.Acceptable = result.DestinationAfter.Acceptable()
	}
	if result.ApplicationDestination != nil {
		item.ApplicationDestination = result.ApplicationDestination.Label + " (" + result.ApplicationDestination.Value + ")"
		item.ApplicationReady = result.ApplicationDestination.Acceptable()
	}
	for _, outcome := range result.Candidates {
		line := outcome.Decision + ": "
		switch {
		case outcome.Candidate.PersonName != "":
			line += outcome.Candidate.PersonName + " — " + outcome.Candidate.RoleTitle
		case outcome.Candidate.ApplicationURL != "":
			line += "application route " + outcome.Candidate.ApplicationURL
		default:
			line += outcome.Candidate.Email
		}
		if outcome.Candidate.Email != "" && outcome.Candidate.PersonName != "" {
			line += " <" + outcome.Candidate.Email + ">"
		}
		line += " [" + outcome.Candidate.EmailProvenance + ", " + outcome.Candidate.Confidence + " confidence]"
		if verbose && len(outcome.Candidate.Evidence) > 0 {
			line += " — " + outcome.Candidate.Evidence[0].SourceURL + ": \"" + outcome.Candidate.Evidence[0].Excerpt + "\""
		}
		item.Findings = append(item.Findings, line)
	}
	return item
}

// dryRunReport lists what the queue holds and what the identity gate would say, with no network
// request and no write.
func dryRunReport(ctx context.Context, db *store.DB, limit int, report Run) error {
	eligible, err := db.ListCompaniesForContactDiscovery(ctx, limit)
	if err != nil {
		return err
	}
	for _, item := range eligible {
		company, err := db.GetCompany(ctx, item.CompanyID)
		if err != nil {
			return err
		}
		identity := contactsearch.EstablishIdentity(contactsearch.CompanyIdentity{
			Name: company.Name, Domain: company.Domain, DomainStatus: company.DomainStatus})
		entry := CompanyReport{CompanyID: company.ID, Company: company.Name, Domain: company.Domain,
			State: "would_search", Blocker: identity.Blocker, GapBefore: item.EligibilityReason}
		if !identity.Established {
			entry.State = "would_refuse"
			entry.Skipped = identity.Reason
			report.IdentityBlocked++
		}
		report.CompaniesExamined++
		report.Companies = append(report.Companies, entry)
	}
	report.FinishedAt = time.Now().UTC()
	return print(report)
}

func print(report Run) error {
	// EmailsSent is stated explicitly and is structurally zero: this binary imports no sender.
	report.EmailsSent = 0
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
