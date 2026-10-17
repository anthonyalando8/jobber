package contactsearch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/relevance"
)

// J. A company whose stored domain contradicts the domain its own name states is not a company
// we have identified. Production produced exactly this pair, and the verifier accepted it,
// because the company's name does appear on the wrong site.
func TestAmbiguousCompanyIdentityRefusesContactDiscovery(t *testing.T) {
	identity := EstablishIdentity(CompanyIdentity{
		Name: "Wakacje.pl", Domain: "wakacje.com", DomainStatus: "verified",
	})
	if identity.Established {
		t.Fatal("a name/domain mismatch was accepted as an established employer identity")
	}
	if identity.Blocker != BlockerNameDomainMismatch {
		t.Fatalf("expected a name/domain mismatch blocker, got %q", identity.Blocker)
	}
	if !strings.Contains(identity.Reason, "wakacje.pl") {
		t.Fatalf("the refusal does not say which domain the name states: %q", identity.Reason)
	}
}

func TestVerifiedAndManualDomainsEstablishIdentity(t *testing.T) {
	for _, status := range []string{"verified", "manual"} {
		identity := EstablishIdentity(CompanyIdentity{Name: "Sticker Mule", Domain: "stickermule.com", DomainStatus: status})
		if !identity.Established {
			t.Fatalf("domain_status %q should establish identity: %s", status, identity.Reason)
		}
	}
}

func TestGuessedAndAggregatorDomainsAreRefused(t *testing.T) {
	if EstablishIdentity(CompanyIdentity{Name: "Acme", Domain: "acme.com", DomainStatus: "candidate"}).Established {
		t.Fatal("a candidate (guessed) domain was treated as an established employer")
	}
	blocked := EstablishIdentity(CompanyIdentity{Name: "Acme", Domain: "remoteok.com", DomainStatus: "verified"})
	if blocked.Established || blocked.Blocker != BlockerDomainIsAggregator {
		t.Fatal("a job board domain was accepted as an employer's own site")
	}
}

// A published employer URL that disagrees with the stored domain is the same failure seen from
// the source's side.
func TestPublishedEmployerURLMustAgreeWithStoredDomain(t *testing.T) {
	identity := EstablishIdentity(CompanyIdentity{
		Name: "Cosuno", Domain: "cosuno.com", DomainStatus: "verified",
		SourceEmployerURL: "https://www.cosuno.de/careers",
	})
	if identity.Established {
		t.Fatal("a stored domain contradicting the published employer site was accepted")
	}
}

// D and E. A job posting that publishes an application address and an apply route produces both,
// each with the provenance that says the source stated it.
func TestPublishedApplicationRoutesArePreservedWithProvenance(t *testing.T) {
	page := Page{
		URL:        "https://boards.example-employer.com/jobs/1",
		SourceType: SourceJobPosting,
		HTML: `<div>We are hiring a Go engineer. To apply, send your CV to careers@exampleemployer.com ` +
			`or <a href="https://boards.greenhouse.io/exampleemployer/jobs/44">apply here</a>.</div>`,
	}
	found := ExtractFromPage(page, "Example Employer", "exampleemployer.com")
	var route, address *Candidate
	for i := range found {
		switch found[i].Kind {
		case KindApplicationURL:
			route = &found[i]
		case KindApplicationEmail:
			address = &found[i]
		}
	}
	if route == nil {
		t.Fatal("the published Greenhouse application route was not discovered")
	}
	if route.ATSProvider != "Greenhouse" {
		t.Fatalf("the applicant tracking system was not identified: %q", route.ATSProvider)
	}
	if address == nil {
		t.Fatal("the published application address was not discovered")
	}
	if address.EmailProvenance != ProvenanceSource {
		t.Fatalf("a job posting's address should be source-published, got %q", address.EmailProvenance)
	}
	if len(address.Evidence) == 0 || !strings.Contains(address.Evidence[0].Excerpt, "careers@exampleemployer.com") {
		t.Fatal("the stored excerpt does not quote the address it claims")
	}
}

// F and G. A recruiter named on a public page yields a person and a title. No address appears,
// so none is created.
func TestRecruiterFoundWithoutAddressYieldsPersonOnly(t *testing.T) {
	page := Page{
		URL: "https://exampleemployer.com/team", SourceType: SourceTeamPage,
		HTML: `<ul><li>Jane Doe, Technical Recruiter, leads hiring for our platform team.</li></ul>`,
	}
	found := ExtractFromPage(page, "Example Employer", "exampleemployer.com")
	person := findPerson(found, "Jane Doe")
	if person == nil {
		t.Fatalf("the named recruiter was not discovered; got %d candidates", len(found))
	}
	if person.Email != "" {
		t.Fatalf("an address was invented for a person nobody published one for: %q", person.Email)
	}
	if person.EmailProvenance != ProvenanceNone {
		t.Fatalf("a person with no address should carry no email provenance, got %q", person.EmailProvenance)
	}
	classified := Classify(*person)
	if classified.Function != relevance.FunctionRecruiting {
		t.Fatalf("a Technical Recruiter was not classified as recruiting: %q", classified.Function)
	}
	if decision, _ := PromotionDecision(classified); decision != PromoteHold {
		t.Fatalf("a person with no address must not be promoted; decision was %q", decision)
	}
}

// A name and an address stated in the same sentence are one evidenced fact, not an inference.
func TestPersonAndAddressPublishedTogetherBecomeOneCandidate(t *testing.T) {
	page := Page{
		URL: "https://exampleemployer.com/careers", SourceType: SourceCareersPage,
		HTML: `<p>Questions about hiring? Jane Doe, Technical Recruiter — jane.doe@exampleemployer.com</p>`,
	}
	found := ExtractFromPage(page, "Example Employer", "exampleemployer.com")
	person := findPerson(found, "Jane Doe")
	if person == nil || person.Email != "jane.doe@exampleemployer.com" {
		t.Fatalf("the co-published name and address were not joined into one candidate: %+v", found)
	}
	if person.EmailProvenance != ProvenanceEmployer {
		t.Fatalf("an address on the employer's own careers page should be employer-published, got %q", person.EmailProvenance)
	}
	classified := Classify(*person)
	if decision, reason := PromotionDecision(classified); decision != PromoteToContact {
		t.Fatalf("an employer-published recruiter address should be promotable: %s (%s)", decision, reason)
	}
}

// The extractor must miss rather than invent. Page furniture next to a job title is not a person.
func TestPageFurnitureIsNotReadAsAPerson(t *testing.T) {
	page := Page{
		URL: "https://exampleemployer.com/careers", SourceType: SourceCareersPage,
		HTML: `<p>Our Engineering Manager will review your application. Read more about the team.</p>`,
	}
	for _, candidate := range ExtractFromPage(page, "Example Employer", "exampleemployer.com") {
		if candidate.PersonName != "" {
			t.Fatalf("invented a person from page furniture: %q", candidate.PersonName)
		}
	}
}

// I. A search snippet claiming an address is a lead, never a published contact.
func TestSnippetOnlyEvidenceCannotProduceAUsableContact(t *testing.T) {
	result := SearchResult{
		Title: "Contact Example Employer", URL: "https://third-party-directory.example/acme",
		Snippet:  "Reach their recruiting team at recruiting@exampleemployer.com for open roles.",
		Provider: "static", ObservedAt: time.Now(),
	}
	found := ExtractFromSnippet(result, "Example Employer", "exampleemployer.com")
	if len(found) == 0 {
		t.Fatal("the snippet lead was discarded entirely; it should be kept as weak evidence")
	}
	for _, candidate := range found {
		classified := Classify(candidate)
		if classified.BestTrust() > SnippetOnlyTrust {
			t.Fatalf("snippet evidence was ranked above a snippet: trust %d", classified.BestTrust())
		}
		if classified.Confidence != ConfidenceLow {
			t.Fatalf("a snippet-only candidate reached %q confidence", classified.Confidence)
		}
		if decision, _ := PromotionDecision(classified); decision == PromoteToContact {
			t.Fatal("a snippet-only address was promoted into the contact table")
		}
	}
}

// K. A telemetry endpoint stays unusable however it was found.
func TestTelemetryEndpointRemainsUnusable(t *testing.T) {
	classified := Classify(Candidate{
		Kind: KindMailbox, Email: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6@o212792.ingest.sentry.io",
		EmailProvenance: ProvenanceEmployer, Verification: VerificationPublished,
		Evidence: []Evidence{{SourceType: SourceEmployerPage, Excerpt: "found on the page"}},
	})
	if classified.EndpointContactable {
		t.Fatal("a Sentry ingestion endpoint was treated as a mailbox a person reads")
	}
	if classified.Verification != VerificationNotHumanMailbox {
		t.Fatalf("expected not_human_mailbox, got %q", classified.Verification)
	}
	if decision, _ := PromotionDecision(classified); decision == PromoteToContact {
		t.Fatal("a telemetry endpoint was promoted into the contact table")
	}
}

// L. A careers mailbox is classified as the role mailbox it is, and never renamed into a person.
func TestCareersMailboxIsNotANamedRecruiter(t *testing.T) {
	classified := Classify(Candidate{
		Kind: KindMailbox, Email: "careers@exampleemployer.com",
		EmailProvenance: ProvenanceEmployer, Verification: VerificationPublished,
		Evidence: []Evidence{{SourceType: SourceCareersPage, Excerpt: "careers@exampleemployer.com"}},
	})
	if classified.PersonName != "" {
		t.Fatalf("a mailbox local part became a person: %q", classified.PersonName)
	}
	if classified.Function != relevance.FunctionRecruiting {
		t.Fatalf("careers@ should classify as recruiting by function, got %q", classified.Function)
	}
	if ContactQuality(classified) == 3 {
		t.Fatal("an unnamed role mailbox was given named-contact quality")
	}
}

// M. A commercial address is not a destination for a job application.
func TestCommercialAddressIsNotSuitableForAJobApplication(t *testing.T) {
	contacts := []relevance.ContactAssessment{classifyStored("sales@exampleemployer.com", "", "")}
	input := plan.DestinationInput{Intent: plan.SpecificJobApplication, Contacts: contacts}
	if plan.HasAcceptableDestination(input) {
		t.Fatal("a sales address was accepted as a destination for a job application")
	}
	destination, ok := plan.ResolveDestination(input)
	if !ok || destination.Relevance != plan.RecipientReview {
		t.Fatalf("a commercial address should surface as review_required, got %+v", destination)
	}
}

// S. An employer-published apply route makes a specific application destination-ready with no
// address at all. This is the correction to "missing contact means no email".
func TestApplicationURLMakesAJobApplicationDestinationReady(t *testing.T) {
	input := plan.DestinationInput{
		Intent: plan.SpecificJobApplication,
		Source: plan.SourceApplicationAddress{URL: "https://boards.greenhouse.io/exampleemployer/jobs/44"},
	}
	if !plan.HasAcceptableDestination(input) {
		t.Fatal("a published apply route did not make a job application destination-ready")
	}
	destination, _ := plan.ResolveDestination(input)
	if destination.Kind != plan.DestinationApplicationURL || !destination.SourcePublished {
		t.Fatalf("the apply route was not surfaced as the published destination: %+v", destination)
	}
}

// The same route is not a destination for outreach that is not an application.
func TestApplicationRouteIsNotADestinationForServiceOutreach(t *testing.T) {
	input := plan.DestinationInput{
		Intent: plan.ServiceOutreach,
		Source: plan.SourceApplicationAddress{URL: "https://boards.greenhouse.io/exampleemployer/jobs/44"},
	}
	if plan.HasAcceptableDestination(input) {
		t.Fatal("an applicant tracking route was accepted as a destination for service outreach")
	}
}

// C. Only generic mailboxes still counts as blocked, so discovery is allowed to run.
func TestGenericMailboxOnlyLeavesCompanyBlocked(t *testing.T) {
	input := plan.DestinationInput{
		Intent:   plan.SpeculativeJobEnquiry,
		Contacts: []relevance.ContactAssessment{classifyStored("info@exampleemployer.com", "", "")},
	}
	if plan.HasAcceptableDestination(input) {
		t.Fatal("a generic info@ mailbox was treated as an acceptable destination")
	}
	gap := plan.DestinationGap(input)
	if !strings.Contains(gap, "generic") {
		t.Fatalf("the gap does not explain that only generic mailboxes exist: %q", gap)
	}
}

// 26. A generic mailbox is kept and ranked below a real recruiting contact, not deleted.
func TestGenericMailboxIsRetainedButOutrankedByANamedRecruiter(t *testing.T) {
	contacts := []relevance.ContactAssessment{
		classifyStored("info@exampleemployer.com", "", ""),
		classifyStored("jane.doe@exampleemployer.com", "Jane Doe", "Technical Recruiter"),
	}
	options := plan.RankRecipients(plan.SpeculativeJobEnquiry, contacts, plan.SourceApplicationAddress{})
	if len(options) != 2 {
		t.Fatalf("a generic mailbox was dropped rather than ranked: %d options", len(options))
	}
	if options[0].Assessment.Contact.Value != "jane.doe@exampleemployer.com" {
		t.Fatalf("the named recruiter did not outrank the generic mailbox: %q", options[0].Assessment.Contact.Value)
	}
}

// H. Inference, when enabled, produces something marked as inferred and nothing else.
func TestInferredAddressIsNeverPublishedOrPromotable(t *testing.T) {
	published := Candidate{
		Kind: KindPerson, PersonName: "John Smith", RoleTitle: "Engineering Manager",
		Email: "john.smith@exampleemployer.com", EmailProvenance: ProvenanceEmployer,
		Evidence: []Evidence{{SourceType: SourceTeamPage, SourceURL: "https://exampleemployer.com/team",
			Excerpt: "John Smith, Engineering Manager — john.smith@exampleemployer.com"}},
	}
	person := Candidate{
		Kind: KindPerson, PersonName: "Jane Doe", RoleTitle: "Technical Recruiter",
		Evidence: []Evidence{{SourceType: SourceTeamPage, SourceURL: "https://exampleemployer.com/team",
			Excerpt: "Jane Doe, Technical Recruiter"}},
	}
	format, ok := DetectEmailFormat([]Candidate{published, person}, "exampleemployer.com")
	if !ok || format.Pattern != FormatFirstDotLast {
		t.Fatalf("the company's observed address format was not read: %+v", format)
	}
	if format.Example != "john.smith@exampleemployer.com" {
		t.Fatalf("the format does not carry the published address it was read from: %+v", format)
	}

	if _, ok := InferAddress(person, format, InferenceConfig{Enabled: false}); ok {
		t.Fatal("inference produced an address while disabled")
	}
	inferred, ok := InferAddress(person, format, InferenceConfig{Enabled: true})
	if !ok {
		t.Fatal("inference produced nothing for a known person and a known format")
	}
	if inferred.Email != "jane.doe@exampleemployer.com" {
		t.Fatalf("unexpected inferred address %q", inferred.Email)
	}
	if inferred.EmailProvenance != ProvenanceInferred || inferred.Verification != VerificationInferred {
		t.Fatalf("an inferred address was not marked as inferred: %+v", inferred)
	}
	classified := Classify(inferred)
	if classified.Confidence != ConfidenceLow {
		t.Fatalf("an inferred address reached %q confidence", classified.Confidence)
	}
	decision, reason := PromotionDecision(classified)
	if decision != PromoteNeedsReview {
		t.Fatalf("an inferred address was not held for review: %s", decision)
	}
	if !strings.Contains(reason, "never becomes send-eligible") {
		t.Fatalf("the reason does not state that inference is not send-eligible: %q", reason)
	}
	// The evidence must say plainly that nobody published this.
	supported := false
	for _, e := range inferred.Evidence {
		if strings.Contains(e.Excerpt, "Address not published") {
			supported = true
		}
	}
	if !supported {
		t.Fatal("an inferred address does not carry evidence stating it was not published")
	}
}

// A format cannot be read from an address with no matching known person: that would be a guess
// about a guess.
func TestEmailFormatIsNotInventedWithoutAKnownPerson(t *testing.T) {
	published := Candidate{
		Kind: KindMailbox, Email: "jdoe@exampleemployer.com", EmailProvenance: ProvenanceEmployer,
		Evidence: []Evidence{{SourceType: SourceEmployerPage, Excerpt: "jdoe@exampleemployer.com"}},
	}
	if _, ok := DetectEmailFormat([]Candidate{published}, "exampleemployer.com"); ok {
		t.Fatal("an address format was inferred with no known person to match it against")
	}
}

// R. Anything asserting a fact its own source does not contain is rejected. This is the check
// that applies to an AI extractor's output as much as to the deterministic ones.
func TestUnsupportedFactsAreRejectedRegardlessOfOrigin(t *testing.T) {
	invented := Candidate{
		Kind: KindPerson, PersonName: "Jane Doe", RoleTitle: "Technical Recruiter",
		Email: "jane.doe@exampleemployer.com", EmailProvenance: ProvenanceEmployer,
		Evidence: []Evidence{{SourceType: SourceCareersPage, SourceURL: "https://exampleemployer.com/careers",
			Excerpt: "Jane Doe leads technical recruitment at Example Employer.",
			Method:  MethodAIExtract}},
	}
	if kept := Ground([]Candidate{invented}); len(kept) != 0 {
		t.Fatal("an extractor's address survived grounding although its source never contained it")
	}

	noEvidence := Candidate{Kind: KindMailbox, Email: "someone@exampleemployer.com",
		Evidence: []Evidence{{SourceType: SourceCareersPage, Excerpt: "  "}}}
	if kept := Ground([]Candidate{noEvidence}); len(kept) != 0 {
		t.Fatal("a candidate with an empty excerpt survived grounding")
	}

	supported := invented
	supported.Evidence[0].Excerpt = "Jane Doe, Technical Recruiter — jane.doe@exampleemployer.com"
	if kept := Ground([]Candidate{supported}); len(kept) != 1 {
		t.Fatal("a properly grounded candidate was rejected")
	}
}

// N. One person seen on several sources is one candidate carrying every observation.
func TestSameContactFromMultipleSourcesIsOneCandidateWithAllEvidence(t *testing.T) {
	collected := map[string]Candidate{}
	base := Candidate{Kind: KindPerson, PersonName: "Jane Doe", RoleTitle: "Technical Recruiter",
		Email: "jane.doe@exampleemployer.com", EmailProvenance: ProvenanceThirdParty}
	for _, source := range []struct{ kind, url, excerpt string }{
		{SourceTeamPage, "https://exampleemployer.com/team", "Jane Doe, Technical Recruiter — jane.doe@exampleemployer.com"},
		{SourceCareersPage, "https://exampleemployer.com/careers", "Contact jane.doe@exampleemployer.com about open roles."},
		{SourceSearchSnippet, "https://directory.example/jane", "Jane Doe — jane.doe@exampleemployer.com"},
	} {
		candidate := base
		if source.kind == SourceCareersPage {
			candidate.EmailProvenance = ProvenanceEmployer
		}
		candidate.Evidence = []Evidence{{SourceType: source.kind, SourceURL: source.url, Excerpt: source.excerpt}}
		mergeCandidate(collected, candidate, "exampleemployer.com")
	}
	if len(collected) != 1 {
		t.Fatalf("one person became %d candidates", len(collected))
	}
	for _, candidate := range collected {
		if len(candidate.Evidence) != 3 {
			t.Fatalf("expected three evidence records, got %d", len(candidate.Evidence))
		}
		if candidate.BestTrust() != SourceTrust(SourceCareersPage) {
			t.Fatalf("the strongest source did not set the candidate's trust: %d", candidate.BestTrust())
		}
		if candidate.EmailProvenance != ProvenanceEmployer {
			t.Fatalf("the stronger publication claim did not win: %q", candidate.EmailProvenance)
		}
	}
}

// Two different people with similar names must never collapse into one.
func TestSimilarNamesAreNotMergedIntoOnePerson(t *testing.T) {
	collected := map[string]Candidate{}
	for _, name := range []string{"Jan de Vries", "Jan de Vroom"} {
		mergeCandidate(collected, Candidate{Kind: KindPerson, PersonName: name, RoleTitle: "Engineering Manager",
			Evidence: []Evidence{{SourceType: SourceTeamPage, Excerpt: name + ", Engineering Manager"}}},
			"exampleemployer.com")
	}
	if len(collected) != 2 {
		t.Fatalf("two different people were merged into %d candidate(s)", len(collected))
	}
}

// T. The three intents look for genuinely different people, and only an application accepts an
// applicant tracking route as an answer.
func TestSearchStrategyDiffersByIntent(t *testing.T) {
	application := StrategyFor(plan.SpecificJobApplication, "Example Employer", "exampleemployer.com")
	service := StrategyFor(plan.ServiceOutreach, "Example Employer", "exampleemployer.com")

	if !application.AcceptsApplicationRoute {
		t.Fatal("a specific job application does not accept an application route")
	}
	if service.AcceptsApplicationRoute {
		t.Fatal("service outreach accepts an applicant tracking route as a destination")
	}
	if application.PreferredFunctions[0] != relevance.FunctionRecruiting {
		t.Fatalf("an application does not prefer recruiting first: %v", application.PreferredFunctions)
	}
	if service.PreferredFunctions[0] != relevance.FunctionEngineering {
		t.Fatalf("service outreach does not prefer engineering first: %v", service.PreferredFunctions)
	}
	for _, query := range service.Queries {
		if strings.Contains(strings.ToLower(query.Text), "recruiter") {
			t.Fatalf("service outreach searches for a recruiter: %q", query.Text)
		}
	}
	if !queriesMention(application.Queries, "careers email") {
		t.Fatalf("an application does not search for a published application address: %v", application.Queries)
	}

	route := Classify(Candidate{Kind: KindApplicationURL, ApplicationURL: "https://boards.greenhouse.io/x/jobs/1",
		Evidence: []Evidence{{SourceType: SourceCareersPage, Excerpt: "boards.greenhouse.io/x/jobs/1"}}})
	engineer := Classify(Candidate{Kind: KindPerson, PersonName: "Ada Byron", RoleTitle: "Head of Engineering",
		Evidence: []Evidence{{SourceType: SourceTeamPage, Excerpt: "Ada Byron, Head of Engineering"}}})

	if top := RankForIntent(application, []Candidate{engineer, route})[0]; top.Kind != KindApplicationURL {
		t.Fatal("an application did not rank the employer's published route first")
	}
	if top := RankForIntent(service, []Candidate{route, engineer})[0]; top.PersonName != "Ada Byron" {
		t.Fatal("service outreach did not rank the engineering leader first")
	}
}

// Q. A provider failure is an error the caller records, never a fabricated result.
func TestSearchProviderFailureIsBoundedAndHonest(t *testing.T) {
	provider := &StaticProvider{Err: errors.New("HTTP 429 rate limited")}
	results, err := provider.Search(context.Background(), "\"Example Employer\" recruiter", 5)
	if err == nil {
		t.Fatal("a failing provider did not report an error")
	}
	if len(results) != 0 {
		t.Fatal("a failing provider returned results")
	}
	if _, err := (NullProvider{}).Search(context.Background(), "anything", 5); err == nil {
		t.Fatal("an unconfigured provider silently succeeded")
	}
	if (NullProvider{}).Enabled() {
		t.Fatal("an unconfigured provider reported itself enabled")
	}
}

// Login-gated platforms are skipped rather than worked around.
func TestLoginGatedPlatformsAreSkipped(t *testing.T) {
	for _, url := range []string{
		"https://www.linkedin.com/in/someone", "https://rocketreach.co/someone",
		"https://www.glassdoor.com/Overview/acme",
	} {
		if !requiresLogin(url) {
			t.Fatalf("%s was not recognized as requiring an account", url)
		}
	}
	if requiresLogin("https://exampleemployer.com/team") {
		t.Fatal("an ordinary public page was treated as login-gated")
	}
}

// Budget refusals are stated rather than silent, and an exhausted counter stops work.
func TestBudgetRefusesAndExplains(t *testing.T) {
	budget := NewBudget(nil, BudgetConfig{CompaniesPerDay: 1})
	if allowed, _ := budget.Allow(context.Background(), CounterCompanies); !allowed {
		t.Fatal("a fresh budget refused the first company")
	}
	if allowed, reason := budget.Allow(context.Background(), CounterAICalls); allowed || reason == "" {
		t.Fatal("AI extraction is allowed by default; it must be opt-in and must explain itself")
	}
}

func classifyStored(address, person, role string) relevance.ContactAssessment {
	return relevance.ClassifyContact(relevance.Contact{
		ID: 1, Kind: "email", Value: address, Person: person, Role: role, Status: "active", Quality: 2,
	})
}

func findPerson(candidates []Candidate, name string) *Candidate {
	for i := range candidates {
		if candidates[i].PersonName == name {
			return &candidates[i]
		}
	}
	return nil
}

func queriesMention(queries []Query, fragment string) bool {
	for _, query := range queries {
		if strings.Contains(strings.ToLower(query.Text), fragment) {
			return true
		}
	}
	return false
}

// A page served as Latin-1 puts bytes in its text that are not UTF-8 characters. This was found
// against the live corpus: one stray 0xa0 in one excerpt aborted a whole company's search,
// because Postgres refuses a byte that is not a character. An unreadable byte must cost the
// observation nothing.
func TestInvalidUTF8InAScrapedPageDoesNotDestroyTheObservation(t *testing.T) {
	latin1 := "Questions about hiring?\xa0Jane Doe, Technical Recruiter\xa0— jane.doe@exampleemployer.com"
	page := Page{URL: "https://exampleemployer.com/careers", SourceType: SourceCareersPage,
		HTML: "<p>" + latin1 + "</p>"}

	found := ExtractFromPage(page, "Example Employer", "exampleemployer.com")
	if len(found) == 0 {
		t.Fatal("an unreadable byte discarded every candidate on the page")
	}
	for _, candidate := range found {
		for _, evidence := range candidate.Evidence {
			if !utf8.ValidString(evidence.Excerpt) {
				t.Fatalf("a stored excerpt is not valid UTF-8: %q", evidence.Excerpt)
			}
		}
	}
	if person := findPerson(found, "Jane Doe"); person == nil || person.Email != "jane.doe@exampleemployer.com" {
		t.Fatal("the recruiter was lost because the page carried an unreadable byte")
	}
}

// A navigation menu is not a leadership team. Against the live corpus this exact text produced a
// person called "Features Cosuno" with the title "cto" — because "Contractors" contains "cto"
// and the title match was a plain substring search. A short title is the most useful one to
// match and the most dangerous to match loosely.
func TestJobTitleSubstringsInsideOrdinaryWordsDoNotCreatePeople(t *testing.T) {
	page := Page{
		URL: "https://exampleemployer.com/careers", SourceType: SourceCareersPage,
		HTML: `<nav>Company | Example Dropdown Link 1 Link 2 Contractors Features Example KI ` +
			`Success stories Features Example AI Success Stories</nav>`,
	}
	for _, candidate := range ExtractFromPage(page, "Example Employer", "exampleemployer.com") {
		if candidate.PersonName != "" {
			t.Fatalf("a navigation menu produced a person: %q — %q", candidate.PersonName, candidate.RoleTitle)
		}
	}

	// The same title as a real word still matches.
	real := Page{URL: "https://exampleemployer.com/team", SourceType: SourceTeamPage,
		HTML: `<p>Ada Byron, CTO, leads the platform team.</p>`}
	if findPerson(ExtractFromPage(real, "Example Employer", "exampleemployer.com"), "Ada Byron") == nil {
		t.Fatal("requiring whole-word titles stopped a genuine CTO from being found")
	}
}

// An unconfigured search provider is a capability gap, not a failure. Reporting it as a failure
// made a rehearsal claim five provider failures that never happened.
func TestUnconfiguredProviderIsNotReportedAsAFailure(t *testing.T) {
	engine := NewEngine(nil, nil, NullProvider{}, NewBudget(nil, BudgetConfig{}), DefaultConfig())
	result := Result{Strategy: StrategyFor(plan.SpeculativeJobEnquiry, "Example Employer", "exampleemployer.com")}
	engine.runSearches(context.Background(), nil, &result, map[string]Candidate{})

	if result.SearchFailure != "" {
		t.Fatalf("an unconfigured provider was recorded as a failure: %q", result.SearchFailure)
	}
	if result.SearchNote == "" {
		t.Fatal("an unconfigured provider was not explained at all")
	}
}

// A two-word title must not supply its own second word as somebody's name. Against the live
// corpus "Talent Acquisition Professional" produced a person called "Acquisition Professional",
// which is a job description with capital letters.
func TestATitleDoesNotSupplyItsOwnWordsAsAName(t *testing.T) {
	page := Page{URL: "https://exampleemployer.com/careers", SourceType: SourceCareersPage,
		HTML: `<p>We are looking for a Talent Acquisition Professional to join the team.</p>`}
	for _, candidate := range ExtractFromPage(page, "Example Employer", "exampleemployer.com") {
		if candidate.PersonName != "" {
			t.Fatalf("a job advert produced a person: %q", candidate.PersonName)
		}
	}

	// A real person after the title is still read.
	byline := Page{URL: "https://exampleemployer.com/team", SourceType: SourceTeamPage,
		HTML: `<p>Technical Recruiter Ada Byron looks after platform hiring.</p>`}
	if findPerson(ExtractFromPage(byline, "Example Employer", "exampleemployer.com"), "Ada Byron") == nil {
		t.Fatal("a name following its title was no longer found")
	}
}

// An application route the employer publishes on its own careers page is the same fact as one the
// job board recorded, and must count the same way.
func TestDiscoveredApplicationRouteCountsAsTheEmployerStatingWhereToApply(t *testing.T) {
	collected := map[string]Candidate{}
	mergeCandidate(collected, Candidate{Kind: KindApplicationURL,
		ApplicationURL: "https://jobs.ashbyhq.com/exampleemployer",
		Evidence: []Evidence{{SourceType: SourceCareersPage, SourceURL: "https://exampleemployer.com/careers",
			Excerpt: "Browse our roles at jobs.ashbyhq.com/exampleemployer"}}}, "exampleemployer.com")
	// A third party linking to the same page is not the employer saying anything.
	mergeCandidate(collected, Candidate{Kind: KindApplicationURL,
		ApplicationURL: "https://jobs.ashbyhq.com/someone-else",
		Evidence: []Evidence{{SourceType: SourceThirdParty, SourceURL: "https://directory.example",
			Excerpt: "jobs.ashbyhq.com/someone-else"}}}, "exampleemployer.com")

	route := bestDiscoveredRoute(collected)
	if route != "https://jobs.ashbyhq.com/exampleemployer" {
		t.Fatalf("the employer-published route was not chosen: %q", route)
	}

	destination, ok := plan.ResolveDestination(plan.DestinationInput{
		Intent: plan.SpecificJobApplication,
		Source: plan.SourceApplicationAddress{URL: route},
	})
	if !ok || !destination.Acceptable() {
		t.Fatal("a discovered apply route did not make the vacancy destination-ready")
	}
}

// The job posting is where an apply route actually lives. A board records the employer and the
// text but prints the application link in the page, so reading only the stored description misses
// the one thing a candidate needs most — which is exactly what happened to every strong match in
// the live corpus.
func TestApplicationRouteIsTakenFromTheJobPostingPage(t *testing.T) {
	posting := Page{
		URL:        "https://weworkremotely.com/remote-jobs/stickermule-software-engineer",
		SourceType: SourceJobPosting,
		HTML: `<div class="listing"><h1>Software engineer at Sticker Mule</h1>
			<a href="https://jobs.ashbyhq.com/stickermule/6db27241-e2d4-4f35">Apply for this position</a></div>`,
	}
	found := Ground(extractApplicationRoutes(posting, "Software engineer at Sticker Mule Apply for this position", "stickermule.com", time.Now()))
	if len(found) == 0 {
		t.Fatal("the posting's own application route was not extracted")
	}
	if found[0].ATSProvider != "Ashby" {
		t.Fatalf("the applicant tracking system was not identified: %q", found[0].ATSProvider)
	}
	if !routeNamesEmployer(found[0].ApplicationURL, "Sticker Mule", "stickermule.com") {
		t.Fatal("the employer's own apply route was not recognized as theirs")
	}
	if Classify(found[0]).BestTrust() != SourceTrust(SourceJobPosting) {
		t.Fatal("a route read from the posting itself is not carrying the posting's trust")
	}
}

// A board page is one employer's advertisement surrounded by links about others. Taking the wrong
// one would look perfectly well-evidenced and send a candidate to another company's form.
func TestAnotherEmployersApplyLinkIsNotTakenFromABoardPage(t *testing.T) {
	for _, route := range []string{
		"https://jobs.ashbyhq.com/someothercompany/1234",
		"https://boards.greenhouse.io/unrelatedcorp/jobs/99",
	} {
		if routeNamesEmployer(route, "Sticker Mule", "stickermule.com") {
			t.Fatalf("%s was accepted as Sticker Mule's application route", route)
		}
	}
	// The employer's own domain is self-evidently theirs, however the path is spelled.
	if !routeNamesEmployer("https://stickermule.com/careers/apply", "Sticker Mule", "stickermule.com") {
		t.Fatal("an apply route on the employer's own domain was rejected")
	}
	// A name with punctuation still matches the form a URL would use.
	if !routeNamesEmployer("https://jobs.lever.co/stickermule/abc", "Sticker Mule, Inc.", "stickermule.com") {
		t.Fatal("the employer's name was not recognized in its own apply route")
	}
}
