package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/anthonyalando8/prospector/internal/contactsearch"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/store"
)

// contactBudgetConfig reads the contact-discovery daily allowance from the environment. The
// defaults are conservative on purpose: this pipeline should work carefully on the few companies
// that are actually blocked, not spend the day covering everything.
func contactBudgetConfig() contactsearch.BudgetConfig {
	cfg := contactsearch.DefaultBudgetConfig()
	envInt("CONTACT_DISCOVERY_COMPANIES_PER_DAY", &cfg.CompaniesPerDay)
	envInt("CONTACT_DISCOVERY_SEARCHES_PER_DAY", &cfg.SearchesPerDay)
	envInt("CONTACT_DISCOVERY_FETCHES_PER_DAY", &cfg.FetchesPerDay)
	envInt("CONTACT_DISCOVERY_AI_CALLS_PER_DAY", &cfg.AICallsPerDay)
	return cfg
}

// contactEngineConfig reads the pipeline's timing and its one genuinely dangerous switch.
//
// CONTACT_DISCOVERY_INFER_EMAILS is off unless it is set to exactly "true". Pattern inference is
// the only part of this pipeline that produces an address nobody published, and an address like
// that reaching a mailbox is a message to a person who was never named as a recipient. Even when
// it is on, an inferred address is held for review and can never be sent to automatically.
func contactEngineConfig() contactsearch.Config {
	cfg := contactsearch.DefaultConfig()
	envDays("CONTACT_DISCOVERY_FOUND_COOLDOWN_DAYS", &cfg.CooldownFound)
	envDays("CONTACT_DISCOVERY_PARTIAL_COOLDOWN_DAYS", &cfg.CooldownPartial)
	envDays("CONTACT_DISCOVERY_NO_RESULT_COOLDOWN_DAYS", &cfg.CooldownNoResult)
	envDays("CONTACT_DISCOVERY_IDENTITY_COOLDOWN_DAYS", &cfg.CooldownIdentityBlocked)
	envHours("CONTACT_DISCOVERY_FAILURE_COOLDOWN_HOURS", &cfg.CooldownFailure)
	envInt("CONTACT_DISCOVERY_MAX_ATTEMPTS", &cfg.MaxAttempts)
	envInt("CONTACT_DISCOVERY_MAX_PAGES", &cfg.MaxPagesPerCompany)
	envInt("CONTACT_DISCOVERY_MAX_SEARCHES", &cfg.MaxSearchesPerCompany)
	cfg.Inference.Enabled = os.Getenv("CONTACT_DISCOVERY_INFER_EMAILS") == "true"
	return cfg
}

func envInt(key string, target *int) {
	if value := os.Getenv(key); value != "" {
		if n, err := strconv.Atoi(value); err == nil && n >= 0 {
			*target = n
		}
	}
}

func envDays(key string, target *time.Duration) {
	var days int
	envInt(key, &days)
	if days > 0 {
		*target = time.Duration(days) * 24 * time.Hour
	}
}

func envHours(key string, target *time.Duration) {
	var hours int
	envInt(key, &hours)
	if hours > 0 {
		*target = time.Duration(hours) * time.Hour
	}
}

// contactDiscoveryView is everything the detail page shows: the verdict, the queue state, the
// budget, and every discovered candidate with its provenance.
type contactDiscoveryView struct {
	Company    *store.Company
	Task       *store.ContactDiscoveryTask
	Candidates []contactCandidateView
	Usage      contactsearch.Usage
	Provider   string
	ProviderOn bool
	Inference  bool
	Identity   contactsearch.Identity

	// Destination is where speculative or service outreach would go. ApplicationDestination is
	// where an application to this company's vacancy would go. They are separate because they are
	// separate questions: an employer-published apply route answers the second completely and the
	// first not at all, and showing only one of them understates what is possible.
	Destination            *plan.Destination
	ApplicationDestination *plan.Destination
	Gap                    string

	// Verdict is the page's headline state: ready, review or blocked.
	Verdict       string
	VerdictTitle  string
	VerdictDetail string

	Promoted, NeedsReview, PeopleWithoutEmail, Routes int
	Status                                            string

	// Budget bars, computed here rather than in the template: the shared renderer has no
	// function map, and arithmetic in a template is the kind of thing that silently divides by
	// zero on the day a limit is unset.
	CompaniesPct, SearchesPct, FetchesPct int
}

// contactCandidateView is one candidate with the few presentation facts the template would
// otherwise have to derive itself.
type contactCandidateView struct {
	store.ContactCandidateRow
	// Tone drives the candidate's accent colour: what state it is in at a glance.
	Tone string
	// Headline is what this candidate is called, given that a candidate may be a person, a bare
	// mailbox, or a route with no person at all.
	Headline string
	Inferred bool
	// Note is the one caveat worth stating inline, when there is one.
	Note string
}

// Ready reports whether there is anywhere acceptable to send anything at all, by either route.
func (v *contactDiscoveryView) Ready() bool {
	return (v.Destination != nil && v.Destination.Acceptable()) ||
		(v.ApplicationDestination != nil && v.ApplicationDestination.Acceptable())
}

// percentOf renders a budget counter as a bar width, capped at 100.
func percentOf(used, limit int) int {
	if limit <= 0 {
		return 0
	}
	if used >= limit {
		return 100
	}
	return used * 100 / limit
}

// ContactDiscovery shows one company's contact-discovery state and everything it has found.
// It is a GET and it searches nothing: seeing what is known must never spend budget or touch
// anybody's server.
func (h *Handler) ContactDiscovery(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid company ID", http.StatusBadRequest)
		return
	}
	view, err := h.contactDiscoveryView(r, id)
	if err != nil {
		http.Error(w, "Cannot load contact discovery: "+err.Error(), http.StatusInternalServerError)
		return
	}
	view.Status = r.URL.Query().Get("status")
	renderTemplate(w, "contact_discovery.html", map[string]any{
		"View": view, "CompanyID": id,
		"BackURL":   safeReturn(r.URL.Query().Get("return"), "/companies/"+strconv.FormatInt(id, 10)),
		"BackLabel": returnLabel(safeReturn(r.URL.Query().Get("return"), "/companies/"+strconv.FormatInt(id, 10))),
	})
}

// ContactDiscoveryRun is the manual "Find contact" action. It enqueues the company and runs the
// same pipeline the worker runs — the same eligibility reasoning, the same identity gate, the
// same budget, the same promotion rules. There is deliberately no ad-hoc manual path: a second
// implementation is a second set of rules, and it is always the second one that skips a check.
func (h *Handler) ContactDiscoveryRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid company ID", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	company, err := h.db.GetCompany(ctx, id)
	if err != nil {
		http.Error(w, "Company not found", http.StatusNotFound)
		return
	}
	input, err := h.discoveryInputFor(ctx, id)
	if err != nil {
		http.Error(w, "Cannot prepare the contact search: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := h.db.RequestContactDiscovery(ctx, input); err != nil {
		http.Error(w, "Cannot queue the contact search: "+err.Error(), http.StatusInternalServerError)
		return
	}
	result, err := h.contactEngine.DiscoverCompany(ctx, id)
	if err != nil {
		h.logActivity(ctx, "contact_discovery", "Contact search failed for "+company.Name+": "+err.Error(), false)
		http.Error(w, "The contact search failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.logActivity(ctx, "contact_discovery", contactSearchSummary(company.Name, result), result.State != store.DiscoveryFailed)

	target := "/companies/" + strconv.FormatInt(id, 10) + "/contact-search"
	if back := r.FormValue("return"); back != "" {
		target += "?return=" + safeReturnQuery(back)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func contactSearchSummary(companyName string, result contactsearch.Result) string {
	if result.Skipped != "" {
		return companyName + ": " + result.Skipped
	}
	summary := companyName + ": " + strconv.Itoa(len(result.Candidates)) + " candidate(s), " +
		strconv.Itoa(result.Promoted) + " promoted to contacts, " +
		strconv.Itoa(result.PagesFetched) + " page(s) read, " +
		strconv.Itoa(result.SearchesRun) + " search(es) run"
	if result.Resolved() {
		return summary + " — an acceptable destination now exists"
	}
	return summary + " — still no acceptable destination"
}

// discoveryInputFor builds the queue entry for a company, reusing the eligibility query so a
// manual request carries the same priority, reason and intent an automatic one would.
func (h *Handler) discoveryInputFor(ctx context.Context, companyID int64) (store.DiscoveryCandidateInput, error) {
	eligible, err := h.db.ListCompaniesForContactDiscovery(ctx, 200)
	if err != nil {
		return store.DiscoveryCandidateInput{}, err
	}
	for _, item := range eligible {
		if item.CompanyID == companyID {
			return item, nil
		}
	}
	// The company is not in the automatic queue — it may be in cooldown, or it may already have
	// an acceptable destination. An operator asking explicitly is entitled to a search anyway;
	// it is recorded as the manual request it is, at the lowest priority.
	company, err := h.db.GetCompany(ctx, companyID)
	if err != nil {
		return store.DiscoveryCandidateInput{}, err
	}
	return store.DiscoveryCandidateInput{
		CompanyID: company.ID, CompanyName: company.Name, Domain: company.Domain,
		DomainStatus: company.DomainStatus, Priority: store.PrioritySelectedPossible,
		EligibilityReason: "an operator asked for a contact search on this company",
	}, nil
}

func (h *Handler) contactDiscoveryView(r *http.Request, companyID int64) (*contactDiscoveryView, error) {
	ctx := r.Context()
	company, err := h.db.GetCompany(ctx, companyID)
	if err != nil {
		return nil, err
	}
	task, err := h.db.GetContactDiscoveryTask(ctx, companyID)
	if err != nil {
		return nil, err
	}
	rows, err := h.db.ListContactCandidates(ctx, companyID)
	if err != nil {
		return nil, err
	}
	view := &contactDiscoveryView{
		Company: company, Task: task,
		Provider:   h.contactEngine.Provider().Name(),
		ProviderOn: h.contactEngine.Provider().Enabled(),
		Inference:  contactEngineConfig().Inference.Enabled,
		Usage:      h.contactEngine.Budget().Usage(ctx),
	}
	view.Identity = contactsearch.EstablishIdentity(contactsearch.CompanyIdentity{
		Name: company.Name, Domain: company.Domain, DomainStatus: company.DomainStatus,
	})

	discoveredRoute := ""
	for _, row := range rows {
		view.Candidates = append(view.Candidates, presentCandidate(row))
		switch row.Status {
		case "promoted":
			view.Promoted++
		case "review_required":
			view.NeedsReview++
		}
		if row.Kind == "person" && row.Email == "" && row.Status != "superseded" {
			view.PeopleWithoutEmail++
		}
		if row.Kind == "application_url" || row.Kind == "application_email" {
			view.Routes++
			if row.Kind == "application_url" && row.EmployerOwned && discoveredRoute == "" {
				discoveredRoute = row.ApplicationURL
			}
		}
	}

	// Where an approach would go right now, judged by the recipient rules rather than by this
	// page's own opinion.
	intent := plan.Intent("")
	opportunityID := int64(0)
	if task != nil {
		intent = plan.Intent(task.Intent)
		if task.OpportunityID != nil {
			opportunityID = *task.OpportunityID
		}
	}
	proposal, err := h.assessor.Propose(ctx, companyID, 0, intent)
	if err == nil {
		judged := intent
		if judged == "" {
			judged = plan.SpeculativeJobEnquiry
		}
		input := plan.DestinationInput{Intent: judged, Contacts: proposal.View.Contacts}
		if destination, ok := plan.ResolveDestination(input); ok {
			view.Destination = &destination
		}
		view.Gap = plan.DestinationGap(input)

		// The vacancy's own question, answered separately. A route this pipeline discovered on
		// the employer's own pages counts the same as one the job board recorded: both are the
		// employer saying where to apply, and only the place we read it differs.
		if opportunityID > 0 {
			source := h.sourceApplicationRoute(ctx, opportunityID)
			if source.URL == "" {
				source.URL = discoveredRoute
			}
			application := input
			application.Intent = plan.SpecificJobApplication
			application.Source = source
			if destination, ok := plan.ResolveDestination(application); ok {
				view.ApplicationDestination = &destination
			}
		}
	}
	view.CompaniesPct = percentOf(view.Usage.Companies, view.Usage.CompaniesLimit)
	view.SearchesPct = percentOf(view.Usage.Searches, view.Usage.SearchesLimit)
	view.FetchesPct = percentOf(view.Usage.Fetches, view.Usage.FetchesLimit)
	view.Verdict, view.VerdictTitle, view.VerdictDetail = verdictFor(view)
	return view, nil
}

// sourceApplicationRoute reads what the job source published about where to apply.
func (h *Handler) sourceApplicationRoute(ctx context.Context, opportunityID int64) plan.SourceApplicationAddress {
	row, err := h.db.GetOpportunity(ctx, opportunityID)
	if err != nil {
		return plan.SourceApplicationAddress{}
	}
	return plan.SourceApplicationAddress{
		Email: row.Facts.Metadata[opportunity.MetaApplyEmail],
		URL:   row.Facts.Metadata[opportunity.MetaApplyURL],
	}
}

// verdictFor states the page's headline in the words an operator needs: can this be acted on,
// does it need a person first, or is there nothing here yet.
func verdictFor(v *contactDiscoveryView) (tone, title, detail string) {
	switch {
	case !v.Identity.Established:
		return "blocked", "Employer identity is not established",
			"No contact search runs until this is resolved. Searching for the people of the wrong " +
				"company produces contacts that look well evidenced and are about somebody else."
	case v.ApplicationDestination != nil && v.ApplicationDestination.Acceptable():
		return "ready", "This vacancy can be applied to",
			"The employer published an application route. No email address is needed to use it."
	case v.Destination != nil && v.Destination.Acceptable():
		return "ready", "There is an acceptable recipient",
			"This contact satisfies the recipient rules for the purpose being searched for."
	case v.Destination != nil:
		return "review", "A recipient exists, but a person must confirm it",
			"Nothing here is refused outright; its relevance to this purpose is simply unverified."
	default:
		return "blocked", "There is nowhere acceptable to send anything", ""
	}
}

// presentCandidate derives the handful of display facts the template should not have to work out.
func presentCandidate(row store.ContactCandidateRow) contactCandidateView {
	item := contactCandidateView{ContactCandidateRow: row}
	item.Inferred = row.EmailProvenance == "inferred_pattern"

	switch {
	case row.PersonName != "":
		item.Headline = row.PersonName
	case row.Kind == "application_url":
		item.Headline = "Application route"
		if row.ATSProvider != "" {
			item.Headline = row.ATSProvider + " application route"
		}
	case row.Email != "":
		item.Headline = row.Email
	default:
		item.Headline = "Unnamed candidate"
	}

	switch {
	case item.Inferred:
		item.Tone = "inferred"
	case row.Status == "promoted":
		item.Tone = "promoted"
	case row.Status == "review_required":
		item.Tone = "review"
	}

	switch {
	case item.Inferred:
		item.Note = "Nobody published this address. It was derived from this employer's observed " +
			"address format, and it can never be sent to automatically."
	case row.Kind == "person" && row.Email == "":
		item.Note = "A person and a role were found. No address was published for them, and none is " +
			"guessed from a name."
	case row.Email != "" && !row.EndpointContactable:
		item.Note = "This address is not a mailbox a person reads, so it cannot be a recipient."
	}
	return item
}

// safeReturnQuery escapes a return path for use in a redirect, after the same validation the
// rest of the navigation uses.
func safeReturnQuery(raw string) string {
	return url.QueryEscape(safeReturn(raw, "/companies"))
}
