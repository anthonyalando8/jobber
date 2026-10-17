package contactsearch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/enrich"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/relevance"
	"github.com/anthonyalando8/prospector/internal/store"
)

// PageFetcher is the bounded public-page fetch this pipeline uses. *enrich.Enricher satisfies
// it, which is the point: contact discovery reads pages through the same rate limiter, the same
// shared page cache, the same timeout and the same size and content-type limits company research
// already uses, rather than opening a second, subtly different way of hitting other people's
// servers.
type PageFetcher interface {
	FetchPublicPage(ctx context.Context, db *store.DB, url string) ([]byte, error)
}

// loginRequiredHosts need an account to show anything useful. Section 7 of the brief is
// unambiguous about these: if a platform requires a login and offers no supported public access,
// the answer is to skip it. There is no scraping of authenticated sessions here, and no attempt
// to work around what these sites do not publish.
var loginRequiredHosts = []string{
	"linkedin.com", "facebook.com", "instagram.com", "x.com", "twitter.com",
	"glassdoor.com", "xing.com", "crunchbase.com", "zoominfo.com", "apollo.io",
	"rocketreach.co", "hunter.io", "lusha.com", "signalhire.com", "contactout.com",
}

// Config holds the pipeline's tunables. Every duration is configurable because the right cooldown
// depends on how fast a corpus turns over, which is an operator's knowledge rather than a
// constant.
type Config struct {
	// Owner names the claim holder, so a stuck claim can be attributed.
	Owner      string
	ClaimLease time.Duration

	// Cooldowns, by how the previous search ended. Section 16: a company searched thoroughly
	// with no useful result is not asked again twenty minutes later.
	CooldownFound           time.Duration
	CooldownPartial         time.Duration
	CooldownNoResult        time.Duration
	CooldownIdentityBlocked time.Duration
	CooldownFailure         time.Duration
	CooldownExhausted       time.Duration

	// MaxAttempts bounds retries after transient failures. Past it, the company is marked failed
	// and left alone for CooldownExhausted rather than retried forever.
	MaxAttempts int

	MaxPagesPerCompany    int
	MaxSearchesPerCompany int

	Inference InferenceConfig
}

// DefaultConfig returns conservative defaults: slow, bounded, and biased toward doing nothing
// rather than doing something repeatedly.
func DefaultConfig() Config {
	return Config{
		Owner:                   "contact-discovery-worker",
		ClaimLease:              10 * time.Minute,
		CooldownFound:           30 * 24 * time.Hour,
		CooldownPartial:         3 * 24 * time.Hour,
		CooldownNoResult:        7 * 24 * time.Hour,
		CooldownIdentityBlocked: 14 * 24 * time.Hour,
		CooldownFailure:         6 * time.Hour,
		CooldownExhausted:       14 * 24 * time.Hour,
		MaxAttempts:             4,
		MaxPagesPerCompany:      8,
		MaxSearchesPerCompany:   4,
	}
}

// Engine runs contact discovery. One engine serves the worker tick, the manual "Find contact"
// action and the rehearsal tool — the same implementation each time, for the same reason
// ResolveFailedDomains and GenerateAndSaveDraft are shared elsewhere in this codebase: a second
// path is a second set of rules, and it is always the second one that skips a check.
type Engine struct {
	db       *store.DB
	fetcher  PageFetcher
	provider SearchProvider
	budget   *Budget
	cfg      Config
}

// NewEngine builds an engine. A nil provider is replaced with NullProvider, so discovery still
// runs against the employer's own pages when no search engine is configured.
func NewEngine(db *store.DB, fetcher PageFetcher, provider SearchProvider, budget *Budget, cfg Config) *Engine {
	if provider == nil {
		provider = NullProvider{}
	}
	defaults := DefaultConfig()
	if cfg.Owner == "" {
		cfg.Owner = defaults.Owner
	}
	if cfg.ClaimLease <= 0 {
		cfg.ClaimLease = defaults.ClaimLease
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaults.MaxAttempts
	}
	if cfg.MaxPagesPerCompany <= 0 {
		cfg.MaxPagesPerCompany = defaults.MaxPagesPerCompany
	}
	if cfg.MaxSearchesPerCompany <= 0 {
		cfg.MaxSearchesPerCompany = defaults.MaxSearchesPerCompany
	}
	for _, pair := range []struct {
		target   *time.Duration
		fallback time.Duration
	}{
		{&cfg.CooldownFound, defaults.CooldownFound},
		{&cfg.CooldownPartial, defaults.CooldownPartial},
		{&cfg.CooldownNoResult, defaults.CooldownNoResult},
		{&cfg.CooldownIdentityBlocked, defaults.CooldownIdentityBlocked},
		{&cfg.CooldownFailure, defaults.CooldownFailure},
		{&cfg.CooldownExhausted, defaults.CooldownExhausted},
	} {
		if *pair.target <= 0 {
			*pair.target = pair.fallback
		}
	}
	return &Engine{db: db, fetcher: fetcher, provider: provider, budget: budget, cfg: cfg}
}

// Provider exposes the configured search provider, so a UI can say honestly whether one exists.
func (e *Engine) Provider() SearchProvider { return e.provider }

// Budget exposes the budget for display.
func (e *Engine) Budget() *Budget { return e.budget }

// CandidateOutcome is one discovered candidate and what was decided about it.
type CandidateOutcome struct {
	Candidate   Candidate `json:"candidate"`
	CandidateID int64     `json:"candidate_id"`
	Decision    string    `json:"decision"`
	Reason      string    `json:"reason"`
	ContactID   int64     `json:"contact_id,omitempty"`
}

// Result is the complete record of one company's search: what was looked at, what was found,
// what was decided, and whether the company now has somewhere to send anything.
type Result struct {
	CompanyID   int64  `json:"company_id"`
	CompanyName string `json:"company"`
	Domain      string `json:"domain"`
	State       string `json:"state"`
	// Skipped is set when no search was run, with the reason. It is not a failure: most of the
	// time it means the company already had somewhere acceptable to send.
	Skipped  string   `json:"skipped,omitempty"`
	Identity Identity `json:"identity"`
	Intent   string   `json:"intent"`
	Strategy Strategy `json:"strategy"`

	PagesFetched int      `json:"pages_fetched"`
	SearchesRun  int      `json:"searches_run"`
	PagesSkipped []string `json:"pages_skipped,omitempty"`
	// SearchFailure is a search provider that was asked and could not answer. It is deliberately
	// separate from SearchNote: not being configured is not a failure, and counting it as one
	// made a rehearsal against an unconfigured provider report five failures that never happened.
	SearchFailure string `json:"search_failure,omitempty"`
	// SearchNote explains why no search engine was consulted, when none was.
	SearchNote string `json:"search_note,omitempty"`

	Candidates []CandidateOutcome `json:"candidates"`

	DestinationBefore *plan.Destination `json:"destination_before,omitempty"`
	DestinationAfter  *plan.Destination `json:"destination_after,omitempty"`
	// ApplicationDestination is where an application to the vacancy that made this company
	// eligible would actually go, judged under the application rules rather than the outreach
	// ones. It exists because the two questions have genuinely different answers: an
	// employer-published apply route is a complete answer for an application and no answer at all
	// for a speculative approach, and reporting only the second understates what is possible.
	// Computing it chooses no intent; it answers a question about a vacancy that already exists.
	ApplicationDestination *plan.Destination `json:"application_destination,omitempty"`
	GapBefore              string            `json:"gap_before,omitempty"`
	GapAfter               string            `json:"gap_after,omitempty"`

	RoutesFound        int `json:"application_routes_found"`
	PublishedEmails    int `json:"published_emails_found"`
	RecruitersFound    int `json:"recruiting_people_found"`
	EngineersFound     int `json:"engineering_people_found"`
	PeopleWithoutEmail int `json:"people_found_without_email"`
	InferredAddresses  int `json:"inferred_addresses"`
	Promoted           int `json:"promoted_to_contacts"`
}

// Resolved reports whether this company ended the search with somewhere acceptable to send.
//
// Either answer counts. A company whose vacancy can be applied to through the employer's own
// published route is not blocked on contact data, even with no address on file — which is the
// whole point of defining the blocker as "no acceptable destination" rather than "no email".
func (r Result) Resolved() bool {
	if r.DestinationAfter != nil && r.DestinationAfter.Acceptable() {
		return true
	}
	return r.ApplicationDestination != nil && r.ApplicationDestination.Acceptable()
}

// BatchResult is one worker tick or one rehearsal pass.
type BatchResult struct {
	Examined  int      `json:"companies_examined"`
	Searched  int      `json:"companies_searched"`
	Resolved  int      `json:"companies_resolved"`
	Skipped   int      `json:"companies_skipped"`
	Failed    int      `json:"companies_failed"`
	Results   []Result `json:"results"`
	StopEarly string   `json:"stopped_early,omitempty"`
}

// RunBatch works through the eligible queue, best companies first, until the batch limit or the
// budget runs out. It stops at the first budget refusal rather than skipping past it, because a
// budget that only stops some work is not a budget.
func (e *Engine) RunBatch(ctx context.Context, limit int) (BatchResult, error) {
	batch := BatchResult{}
	companies, err := e.db.ListCompaniesForContactDiscovery(ctx, limit)
	if err != nil {
		return batch, err
	}
	for _, company := range companies {
		if allowed, reason := e.budget.Allow(ctx, CounterCompanies); !allowed {
			batch.StopEarly = reason
			break
		}
		if _, err := e.db.EnqueueContactDiscovery(ctx, company); err != nil {
			return batch, err
		}
		result, err := e.DiscoverCompany(ctx, company.CompanyID)
		if err != nil {
			return batch, err
		}
		batch.Examined++
		batch.Results = append(batch.Results, result)
		switch {
		case result.Skipped != "":
			batch.Skipped++
		case result.State == store.DiscoveryFailed:
			batch.Failed++
			batch.Searched++
		default:
			batch.Searched++
		}
		if result.Resolved() {
			batch.Resolved++
		}
	}
	return batch, nil
}

// DiscoverCompany runs the whole pipeline for one company.
//
// The order is deliberate. Eligibility and the identity gate come before any network request, so
// a company we should not be researching costs nothing and reveals nothing. The employer's own
// pages are read before any search engine is consulted, because they are both the highest-trust
// source and the one that needs no external service. Promotion into the contact table happens
// last, only for addresses that cleared every check, and the destination is recomputed afterwards
// so the answer to "does this company now have somewhere to send" is measured rather than assumed.
func (e *Engine) DiscoverCompany(ctx context.Context, companyID int64) (Result, error) {
	company, err := e.db.GetCompany(ctx, companyID)
	if err != nil {
		return Result{}, err
	}
	result := Result{CompanyID: company.ID, CompanyName: company.Name, Domain: company.Domain}

	task, err := e.db.GetContactDiscoveryTask(ctx, companyID)
	if err != nil {
		return result, err
	}
	intent := plan.Intent("")
	var opportunityID int64
	if task != nil {
		intent = plan.Intent(task.Intent)
		if task.OpportunityID != nil {
			opportunityID = *task.OpportunityID
		}
	}
	result.Intent = string(intent)

	// What the company already has. If there is already somewhere acceptable to send, this
	// company is not blocked on contact data and its budget is better spent elsewhere.
	destinationInput, sourceRoute, err := e.destinationInput(ctx, companyID, opportunityID, intent)
	if err != nil {
		return result, err
	}
	if before, ok := plan.ResolveDestination(destinationInput); ok {
		result.DestinationBefore = &before
	}
	result.GapBefore = plan.DestinationGap(destinationInput)
	if plan.HasAcceptableDestination(destinationInput) {
		result.Skipped = "This company already has an acceptable destination, so no search was run."
		result.State = store.DiscoveryCompleted
		result.DestinationAfter = result.DestinationBefore
		if err := e.complete(ctx, companyID, result, e.cfg.CooldownFound); err != nil {
			return result, err
		}
		return result, nil
	}

	// Who this employer actually is. Everything after this point involves looking for named
	// people, and looking for the named people of the wrong company is the failure this gate
	// exists to prevent.
	result.Identity = EstablishIdentity(CompanyIdentity{
		Name: company.Name, Domain: company.Domain, DomainStatus: company.DomainStatus,
		SourceEmployerURL: sourceRoute.EmployerURL,
	})
	if !result.Identity.Established {
		result.State = store.DiscoveryIdentityBlocked
		result.Skipped = result.Identity.Reason
		e.budget.Record(ctx, CounterIdentityBlocked, 1)
		if err := e.complete(ctx, companyID, result, e.cfg.CooldownIdentityBlocked); err != nil {
			return result, err
		}
		return result, nil
	}

	claimed, err := e.db.ClaimContactDiscovery(ctx, companyID, e.cfg.Owner, e.cfg.ClaimLease)
	if err != nil {
		return result, err
	}
	if !claimed {
		result.Skipped = "Another contact search for this company is already in progress."
		result.State = store.DiscoverySearching
		return result, nil
	}
	if err := e.db.MarkContactDiscoverySearching(ctx, companyID, e.cfg.Owner); err != nil {
		return result, err
	}
	e.budget.Record(ctx, CounterCompanies, 1)

	result.Strategy = StrategyFor(intent, company.Name, result.Identity.Domain)
	collected := map[string]Candidate{}

	// 1. What the job source itself published. This is the strongest evidence there is, it is
	//    already stored, and it costs nothing to read.
	for _, candidate := range e.candidatesFromSource(sourceRoute, company.Name) {
		mergeCandidate(collected, candidate, result.Identity.Domain)
	}

	// 2. The posting page itself. This is where an apply route actually lives: a board records
	//    the employer and the text, but prints the application link in the page rather than in a
	//    structured field, so reading only the stored description misses the one thing a
	//    candidate needs most.
	e.readJobPosting(ctx, company, sourceRoute, &result, collected)

	// 3. The employer's own pages. Reachable with no search engine at all.
	e.readEmployerPages(ctx, company, &result, collected)

	// 4. The public web, when a provider is configured and the employer's own pages did not
	//    answer the question.
	if !e.satisfied(collected, result.Strategy) {
		e.runSearches(ctx, company, &result, collected)
	}

	// 5. Optional pattern inference, off by default and fenced off from publication.
	e.applyInference(collected, result.Identity.Domain)

	if err := e.persist(ctx, company, &result, collected); err != nil {
		return result, err
	}

	// What the company has now, measured through the same recipient rules as before.
	after, route, err := e.destinationInput(ctx, companyID, opportunityID, intent)
	if err != nil {
		return result, err
	}
	if destination, ok := plan.ResolveDestination(after); ok {
		result.DestinationAfter = &destination
	}
	result.GapAfter = plan.DestinationGap(after)

	// An application route this search discovered counts alongside one the job board published.
	// Both are the employer saying where to apply; only the place we read it from differs.
	if opportunityID > 0 {
		applicationSource := plan.SourceApplicationAddress{Email: route.ApplyEmail, URL: route.ApplyURL}
		if applicationSource.URL == "" {
			applicationSource.URL = bestDiscoveredRoute(collected)
		}
		application := after
		application.Intent = plan.SpecificJobApplication
		application.Source = applicationSource
		if destination, ok := plan.ResolveDestination(application); ok {
			result.ApplicationDestination = &destination
		}
	}

	state, cooldown := e.conclude(result, task)
	result.State = state
	if err := e.complete(ctx, companyID, result, cooldown); err != nil {
		return result, err
	}
	e.recordOutcomeCounters(ctx, result)
	return result, nil
}

// sourceRoute is what the job posting published about where to apply and who the employer is.
type sourceRoute struct {
	EmployerURL  string
	ApplyURL     string
	ApplyEmail   string
	EmailSource  string
	PostingURL   string
	PostingTitle string
	Description  string
}

// destinationInput assembles what the recipient rules need to judge this company, using the same
// classifiers the plan page and the send path use.
func (e *Engine) destinationInput(ctx context.Context, companyID, opportunityID int64, intent plan.Intent) (plan.DestinationInput, sourceRoute, error) {
	contacts, err := e.db.GetContactsByCompany(ctx, companyID)
	if err != nil {
		return plan.DestinationInput{}, sourceRoute{}, err
	}
	assessed := make([]relevance.Contact, 0, len(contacts))
	for _, c := range contacts {
		item := relevance.Contact{ID: c.ID, Kind: c.Kind, Value: c.Value, Quality: c.Quality, Status: c.Status}
		if c.PersonName != nil {
			item.Person = *c.PersonName
		}
		if c.Role != nil {
			item.Role = *c.Role
		}
		assessed = append(assessed, item)
	}
	route := sourceRoute{}
	if opportunityID > 0 {
		row, err := e.db.GetOpportunity(ctx, opportunityID)
		if err == nil {
			meta := row.Facts.Metadata
			route = sourceRoute{
				EmployerURL: meta[opportunity.MetaEmployerURL], ApplyURL: meta[opportunity.MetaApplyURL],
				ApplyEmail: meta[opportunity.MetaApplyEmail], EmailSource: meta[opportunity.MetaApplyEmailSource],
				PostingURL: row.Facts.URL, PostingTitle: row.Facts.Title, Description: row.Facts.Description,
			}
		}
	}
	// With no established intent, the destination is judged as a speculative enquiry would judge
	// it: the employment-side rules, without an application route counting as an answer. That is
	// the conservative reading, and it never invents an intent to widen what qualifies.
	judgedIntent := intent
	if judgedIntent == "" {
		judgedIntent = plan.SpeculativeJobEnquiry
	}
	return plan.DestinationInput{
		Intent:   judgedIntent,
		Contacts: relevance.ClassifyContacts(assessed),
		Source:   plan.SourceApplicationAddress{Email: route.ApplyEmail, URL: route.ApplyURL},
	}, route, nil
}

// candidatesFromSource records what the job board stated in its own structured fields. A
// structured field is a statement by the source, which is why it ranks just below the posting
// itself and above anything scraped from a third party.
func (e *Engine) candidatesFromSource(route sourceRoute, companyName string) []Candidate {
	observed := time.Now().UTC()
	var out []Candidate
	if address := strings.TrimSpace(route.ApplyEmail); address != "" {
		excerpt := "The job source recorded \"" + address + "\" as this vacancy's application address"
		if route.EmailSource != "" {
			excerpt += " (" + route.EmailSource + ")"
		}
		excerpt += "."
		out = append(out, Candidate{
			Kind: KindApplicationEmail, Email: strings.ToLower(address),
			EmailProvenance: ProvenanceSource, Verification: VerificationPublished,
			RoleTitle: "Published application address", EmployerClaimed: companyName,
			Evidence: []Evidence{{SourceType: SourceMetadata, SourceURL: route.PostingURL,
				Excerpt: excerpt, Method: MethodSourceMetadata, ObservedAt: observed}},
		})
	}
	if route.ApplyURL != "" {
		provider, _ := atsProviderFor(route.ApplyURL)
		out = append(out, Candidate{
			Kind: KindApplicationURL, ApplicationURL: route.ApplyURL, ATSProvider: provider,
			EmailProvenance: ProvenanceNone, Verification: VerificationPublished,
			RoleTitle: "Published application route", EmployerClaimed: companyName,
			Evidence: []Evidence{{SourceType: SourceMetadata, SourceURL: route.PostingURL,
				Excerpt: "The job source recorded " + route.ApplyURL + " as this vacancy's application route.",
				Method:  MethodSourceMetadata, ObservedAt: observed}},
		})
	}
	// The posting's own description, read for an application address stated in its text.
	if route.Description != "" {
		page := Page{URL: route.PostingURL, HTML: route.Description, SourceType: SourceJobPosting}
		out = append(out, ExtractFromPage(page, companyName, "")...)
	}
	return Ground(out)
}

// readJobPosting reads the advertisement itself and takes the application route out of it.
//
// This is the highest-trust source in the pipeline and the one most likely to answer the question
// outright, because an apply link is exactly what a job advertisement is for. It also needs no
// search provider, which matters more than it sounds: for a corpus sourced from job boards, this
// is the path that works with nothing configured at all.
//
// Only application routes are taken from here, never people or mailboxes. A board's job page is
// the employer's advertisement wrapped in somebody else's website — the surrounding markup
// carries the board's own staff, other employers' listings and unrelated addresses, none of which
// are facts about this employer. The route is further required to name the employer, so a page
// listing several jobs cannot contribute another company's apply link.
func (e *Engine) readJobPosting(ctx context.Context, company *store.Company, route sourceRoute, result *Result, collected map[string]Candidate) {
	postingURL := strings.TrimSpace(route.PostingURL)
	if postingURL == "" || requiresLogin(postingURL) {
		return
	}
	if result.PagesFetched >= e.cfg.MaxPagesPerCompany {
		return
	}
	if allowed, reason := e.budget.Allow(ctx, CounterFetches); !allowed {
		result.PagesSkipped = append(result.PagesSkipped, reason)
		return
	}
	body, err := e.fetcher.FetchPublicPage(ctx, e.db, postingURL)
	e.budget.Record(ctx, CounterFetches, 1)
	result.PagesFetched++
	if err != nil {
		result.PagesSkipped = append(result.PagesSkipped, "could not read the job posting at "+postingURL)
		return
	}

	page := Page{URL: postingURL, HTML: string(body), SourceType: SourceJobPosting}
	for _, candidate := range Ground(extractApplicationRoutes(page, enrich.PageText(page.HTML), result.Identity.Domain, time.Now().UTC())) {
		if !routeNamesEmployer(candidate.ApplicationURL, company.Name, result.Identity.Domain) {
			continue
		}
		candidate.EmployerClaimed = company.Name
		mergeCandidate(collected, candidate, result.Identity.Domain)
	}
}

// routeNamesEmployer reports whether an application route identifies the employer we are
// researching, by carrying its domain label or its name in the link.
//
// A job board page is a page about one employer surrounded by links about others. Without this,
// the apply link of whichever unrelated job happened to be rendered alongside would be recorded
// as this employer's application route — an error that would look perfectly well-evidenced and
// send a candidate to the wrong company's form.
func routeNamesEmployer(routeURL, companyName, companyDomain string) bool {
	target := strings.ToLower(routeURL)
	if target == "" {
		return false
	}
	// A route on the employer's own domain is self-evidently theirs.
	if SameEmployerHost(routeURL, companyDomain) {
		return true
	}
	for _, token := range employerTokens(companyName, companyDomain) {
		if len(token) >= 4 && strings.Contains(target, token) {
			return true
		}
	}
	return false
}

// employerTokens are the forms an employer's name plausibly takes inside a URL: its domain label,
// and its name with the separators a URL would drop.
func employerTokens(companyName, companyDomain string) []string {
	tokens := []string{}
	if domain := normalizeHost(companyDomain); domain != "" {
		if label, _, found := strings.Cut(domain, "."); found && label != "" {
			tokens = append(tokens, label)
		}
	}
	name := strings.ToLower(strings.TrimSpace(companyName))
	var compact strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			compact.WriteRune(r)
		}
	}
	if compact.Len() > 0 {
		tokens = append(tokens, compact.String())
	}
	return tokens
}

// readEmployerPages fetches the employer's own careers, team and contact pages. This is where
// most of the value is, and it works with no search provider configured at all.
func (e *Engine) readEmployerPages(ctx context.Context, company *store.Company, result *Result, collected map[string]Candidate) {
	domain := result.Identity.Domain
	if domain == "" {
		return
	}
	for _, path := range CareerPagePaths {
		if result.PagesFetched >= e.cfg.MaxPagesPerCompany {
			return
		}
		if allowed, reason := e.budget.Allow(ctx, CounterFetches); !allowed {
			result.PagesSkipped = append(result.PagesSkipped, reason)
			return
		}
		pageURL := "https://" + domain + path
		body, err := e.fetcher.FetchPublicPage(ctx, e.db, pageURL)
		e.budget.Record(ctx, CounterFetches, 1)
		result.PagesFetched++
		if err != nil {
			continue // a 404 on /team is the ordinary case, not a failure worth recording
		}
		page := Page{URL: pageURL, HTML: string(body), SourceType: employerSourceType(path)}
		for _, candidate := range ExtractFromPage(page, company.Name, domain) {
			mergeCandidate(collected, candidate, domain)
		}
	}
}

// employerSourceType classifies an employer-owned page by what it is, because a careers page and
// an about page are not equally good evidence about hiring.
func employerSourceType(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.Contains(lower, "career") || strings.Contains(lower, "job") || strings.Contains(lower, "join") || strings.Contains(lower, "work-with-us"):
		return SourceCareersPage
	case strings.Contains(lower, "team") || strings.Contains(lower, "people") || strings.Contains(lower, "leadership"):
		return SourceTeamPage
	default:
		return SourceEmployerPage
	}
}

// runSearches consults the configured search provider and reads the pages worth reading.
//
// Snippets are recorded as the weak evidence they are, and a promising result is followed to the
// actual page — section 19: do not trust snippets alone. A provider failure is bounded and
// recorded; it never becomes a retry loop and never becomes an invented result.
func (e *Engine) runSearches(ctx context.Context, company *store.Company, result *Result, collected map[string]Candidate) {
	if !e.provider.Enabled() {
		result.SearchNote = "No search provider is configured, so only the employer's own pages were read."
		return
	}
	domain := result.Identity.Domain
	for _, query := range result.Strategy.Queries {
		if result.SearchesRun >= e.cfg.MaxSearchesPerCompany {
			return
		}
		if allowed, reason := e.budget.Allow(ctx, CounterSearches); !allowed {
			result.SearchNote = reason
			return
		}
		hits, err := e.provider.Search(ctx, query.Text, 5)
		// The request was spent whether or not it answered, so it is counted either way.
		e.budget.Record(ctx, CounterSearches, 1)
		result.SearchesRun++
		if err != nil {
			result.SearchFailure = "Search provider failed: " + err.Error()
			e.budget.Record(ctx, CounterFailures, 1)
			return // bounded: one provider failure ends this company's searching, not the batch
		}
		for _, hit := range hits {
			for _, candidate := range ExtractFromSnippet(hit, company.Name, domain) {
				mergeCandidate(collected, candidate, domain)
			}
			if requiresLogin(hit.URL) {
				result.PagesSkipped = append(result.PagesSkipped,
					hit.URL+" requires an account to read, so it was skipped rather than worked around")
				continue
			}
			if result.PagesFetched >= e.cfg.MaxPagesPerCompany {
				continue
			}
			if allowed, reason := e.budget.Allow(ctx, CounterFetches); !allowed {
				result.PagesSkipped = append(result.PagesSkipped, reason)
				continue
			}
			body, err := e.fetcher.FetchPublicPage(ctx, e.db, hit.URL)
			e.budget.Record(ctx, CounterFetches, 1)
			result.PagesFetched++
			if err != nil {
				continue
			}
			page := Page{URL: hit.URL, HTML: string(body), SourceType: searchResultSourceType(hit.URL, domain), Provider: hit.Provider}
			for _, candidate := range ExtractFromPage(page, company.Name, domain) {
				mergeCandidate(collected, candidate, domain)
			}
		}
	}
}

// searchResultSourceType decides what a fetched search result actually is. A page on the
// employer's own domain is the employer speaking; anything else is a third party describing it.
func searchResultSourceType(pageURL, companyDomain string) string {
	if SameEmployerHost(pageURL, companyDomain) {
		return employerSourceType(pageURL)
	}
	return SourceThirdParty
}

func requiresLogin(rawURL string) bool {
	host := normalizeHost(hostOf(rawURL))
	for _, known := range loginRequiredHosts {
		if host == known || strings.HasSuffix(host, "."+known) {
			return true
		}
	}
	return false
}

// satisfied reports whether what has been found already answers the question, so the search
// budget is not spent confirming something the employer's own careers page already said.
func (e *Engine) satisfied(collected map[string]Candidate, strategy Strategy) bool {
	for _, candidate := range collected {
		classified := Classify(candidate)
		if strategy.AcceptsApplicationRoute && classified.Kind == KindApplicationURL &&
			classified.Confidence == ConfidenceHigh {
			return true
		}
		if classified.Email != "" && classified.EndpointContactable &&
			classified.Confidence == ConfidenceHigh &&
			(classified.Function == relevance.FunctionRecruiting || classified.Function == relevance.FunctionEngineering) {
			return true
		}
	}
	return false
}

// applyInference derives addresses for known people from an observed company format, when
// inference is enabled at all. It is a separate step, after everything published has been
// collected, because the format can only be read from addresses that were actually published.
func (e *Engine) applyInference(collected map[string]Candidate, domain string) {
	if !e.cfg.Inference.Enabled {
		return
	}
	known := make([]Candidate, 0, len(collected))
	for _, candidate := range collected {
		known = append(known, candidate)
	}
	format, ok := DetectEmailFormat(known, domain)
	if !ok {
		return
	}
	for _, candidate := range known {
		if candidate.Kind != KindPerson || candidate.Email != "" {
			continue
		}
		if inferred, ok := InferAddress(candidate, format, e.cfg.Inference); ok {
			mergeCandidate(collected, inferred, domain)
		}
	}
}

// mergeCandidate folds one discovered candidate into the working set, combining evidence rather
// than creating a second entry for the same identity.
func mergeCandidate(collected map[string]Candidate, candidate Candidate, domain string) {
	key, _ := DeduplicateKey(candidate, domain)
	if key == "" {
		return // nothing identifies this candidate, so there is nothing to store
	}
	existing, ok := collected[key]
	if !ok {
		collected[key] = candidate
		return
	}
	// Fill gaps; never overwrite a fact already recorded with a later reading of it.
	if existing.PersonName == "" {
		existing.PersonName = candidate.PersonName
	}
	if existing.RoleTitle == "" {
		existing.RoleTitle = candidate.RoleTitle
	}
	if existing.EmployerClaimed == "" {
		existing.EmployerClaimed = candidate.EmployerClaimed
	}
	if existing.ApplicationURL == "" {
		existing.ApplicationURL = candidate.ApplicationURL
	}
	if existing.ATSProvider == "" {
		existing.ATSProvider = candidate.ATSProvider
	}
	if existing.ProfileURL == "" {
		existing.ProfileURL = candidate.ProfileURL
	}
	// The stronger publication claim wins: an address seen once in a snippet and once on the
	// employer's careers page is an employer-published address.
	if provenanceRank(candidate.EmailProvenance) > provenanceRank(existing.EmailProvenance) {
		existing.EmailProvenance = candidate.EmailProvenance
	}
	if candidate.Verification == VerificationPublished {
		existing.Verification = VerificationPublished
	}
	existing.Evidence = append(existing.Evidence, candidate.Evidence...)
	collected[key] = existing
}

func provenanceRank(provenance string) int {
	switch provenance {
	case ProvenanceSource:
		return 4
	case ProvenanceEmployer:
		return 3
	case ProvenanceThirdParty:
		return 2
	case ProvenanceInferred:
		return 1
	}
	return 0
}

// persist classifies every candidate, stores it with all of its provenance, and promotes the ones
// that cleared every check into the contact table.
func (e *Engine) persist(ctx context.Context, company *store.Company, result *Result, collected map[string]Candidate) error {
	ordered := make([]Candidate, 0, len(collected))
	for _, candidate := range collected {
		ordered = append(ordered, Classify(candidate))
	}
	ordered = RankForIntent(result.Strategy, ordered)

	for _, candidate := range ordered {
		key, basis := DeduplicateKey(candidate, result.Identity.Domain)
		if key == "" {
			continue
		}
		decision, reason := PromotionDecision(candidate)
		status := "new"
		switch decision {
		case PromoteNeedsReview:
			status = "review_required"
		case PromoteToContact:
			status = "new" // promotion sets it below, once the contact row exists
		}

		evidence := make([]store.SaveContactEvidenceInput, 0, len(candidate.Evidence))
		for _, item := range candidate.Evidence {
			evidence = append(evidence, store.SaveContactEvidenceInput{
				SourceType: item.SourceType, SourceTrust: item.Trust(), SourceURL: item.SourceURL,
				Excerpt: item.Excerpt, ExcerptHash: item.ExcerptHash(), Method: item.Method,
				Provider: item.Provider, Employer: item.EmployerOwned(), ObservedAt: item.ObservedAt,
			})
		}
		candidateID, err := e.db.SaveContactCandidate(ctx, store.SaveContactCandidateInput{
			CompanyID: company.ID, IdentityKey: key, IdentityBasis: basis, Kind: candidate.Kind,
			PersonName: candidate.PersonName, RoleTitle: candidate.RoleTitle,
			EmployerClaimed: candidate.EmployerClaimed, Email: candidate.Email,
			EmailProvenance: candidate.EmailProvenance, Verification: candidate.Verification,
			ApplicationURL: candidate.ApplicationURL, ATSProvider: candidate.ATSProvider,
			RecipientFunction: candidate.Function, FunctionEvidence: candidate.FunctionEvidence,
			EndpointPurpose: candidate.EndpointPurpose, EndpointContactable: candidate.EndpointContactable,
			Confidence: candidate.Confidence, BestSourceTrust: candidate.BestTrust(),
			EmployerOwned: candidate.HasEmployerOwnedEvidence(), Status: status, RejectedReason: reasonFor(decision, reason),
			Evidence: evidence,
		})
		if err != nil {
			return fmt.Errorf("store candidate for %s: %w", company.Name, err)
		}

		outcome := CandidateOutcome{Candidate: candidate, CandidateID: candidateID, Decision: decision, Reason: reason}
		if decision == PromoteToContact {
			contact, err := e.db.PromoteContactCandidate(ctx, candidateID, ContactQuality(candidate))
			if err != nil {
				return fmt.Errorf("promote candidate for %s: %w", company.Name, err)
			}
			outcome.ContactID = contact.ID
			result.Promoted++
			// The same person found earlier without an address is superseded by this one, so the
			// operator sees one person rather than two records of the same person.
			if candidate.PersonName != "" {
				if err := e.db.SupersedeNamedCandidate(ctx, company.ID, NormalizePersonName(candidate.PersonName), candidateID); err != nil {
					return err
				}
			}
		}
		result.Candidates = append(result.Candidates, outcome)
		countCandidate(result, candidate)
	}
	return nil
}

func reasonFor(decision, reason string) string {
	if decision == PromoteToContact {
		return ""
	}
	return reason
}

func countCandidate(result *Result, candidate Candidate) {
	switch {
	case candidate.Kind == KindApplicationURL:
		result.RoutesFound++
	case candidate.Kind == KindApplicationEmail:
		result.RoutesFound++
		result.PublishedEmails++
	case candidate.Inferred():
		result.InferredAddresses++
	case candidate.Email != "" && publishedProvenance(candidate.EmailProvenance):
		result.PublishedEmails++
	}
	if candidate.PersonName != "" && candidate.Email == "" {
		result.PeopleWithoutEmail++
	}
	switch candidate.Function {
	case relevance.FunctionRecruiting:
		if candidate.PersonName != "" {
			result.RecruitersFound++
		}
	case relevance.FunctionEngineering, relevance.FunctionLeadership:
		if candidate.PersonName != "" {
			result.EngineersFound++
		}
	}
}

// bestDiscoveredRoute returns the strongest application route this search found, or empty. Only
// a route resting on employer-owned evidence qualifies: a third party linking to an apply page is
// not the employer stating where to apply.
func bestDiscoveredRoute(collected map[string]Candidate) string {
	best, bestTrust := "", 0
	for _, candidate := range collected {
		if candidate.Kind != KindApplicationURL || candidate.ApplicationURL == "" {
			continue
		}
		if !candidate.HasEmployerOwnedEvidence() {
			continue
		}
		if trust := candidate.BestTrust(); trust > bestTrust {
			best, bestTrust = candidate.ApplicationURL, trust
		}
	}
	return best
}

// conclude decides the queue state and how long before this company is worth asking about again.
func (e *Engine) conclude(result Result, task *store.ContactDiscoveryTask) (string, time.Duration) {
	attempts := 0
	if task != nil {
		attempts = task.Attempts
	}
	switch {
	case result.SearchFailure != "" && len(result.Candidates) == 0:
		// A transient provider failure, retried on a bounded schedule and then left alone. The
		// company is not marked no_result, because nothing was actually learned about it.
		if attempts >= e.cfg.MaxAttempts {
			return store.DiscoveryFailed, e.cfg.CooldownExhausted
		}
		return store.DiscoveryFailed, e.cfg.CooldownFailure
	case result.Resolved():
		return store.DiscoveryCompleted, e.cfg.CooldownFound
	case len(result.Candidates) == 0:
		return store.DiscoveryNoResult, e.cfg.CooldownNoResult
	case result.needsReview():
		return store.DiscoveryReviewRequired, e.cfg.CooldownPartial
	default:
		return store.DiscoveryPartial, e.cfg.CooldownPartial
	}
}

func (r Result) needsReview() bool {
	for _, outcome := range r.Candidates {
		if outcome.Decision == PromoteNeedsReview {
			return true
		}
	}
	return false
}

func (e *Engine) complete(ctx context.Context, companyID int64, result Result, cooldown time.Duration) error {
	return e.db.CompleteContactDiscovery(ctx, companyID, store.ContactDiscoveryOutcome{
		State:         result.State,
		FailureReason: result.SearchFailure,
		Cooldown:      cooldown,
		Summary: map[string]any{
			"pages_fetched": result.PagesFetched, "searches_run": result.SearchesRun,
			"candidates": len(result.Candidates), "promoted": result.Promoted,
			"application_routes": result.RoutesFound, "published_emails": result.PublishedEmails,
			"recruiting_people": result.RecruitersFound, "engineering_people": result.EngineersFound,
			"people_without_email": result.PeopleWithoutEmail, "inferred_addresses": result.InferredAddresses,
			"resolved": result.Resolved(), "gap": result.GapAfter, "skipped": result.Skipped,
			"identity_blocker": result.Identity.Blocker,
		},
	})
}

func (e *Engine) recordOutcomeCounters(ctx context.Context, result Result) {
	e.budget.Record(ctx, CounterContactsFound, result.Promoted)
	e.budget.Record(ctx, CounterRoutesFound, result.RoutesFound)
	e.budget.Record(ctx, CounterRecruitersFound, result.RecruitersFound)
	e.budget.Record(ctx, CounterEngineeringFound, result.EngineersFound)
	e.budget.Record(ctx, CounterPeopleWithoutMail, result.PeopleWithoutEmail)
	if result.State == store.DiscoveryNoResult {
		e.budget.Record(ctx, CounterNoResult, 1)
	}
	if result.State == store.DiscoveryFailed {
		e.budget.Record(ctx, CounterFailures, 1)
	}
}
