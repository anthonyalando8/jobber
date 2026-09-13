package plan

import (
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/profile"
	"github.com/anthonyalando8/prospector/internal/relevance"
)

// syntheticProfile is a fixture. Nothing in it is approved or activated on any live database;
// the payments project exists specifically so the payments invariants can be exercised.
func syntheticProfile() profile.Document {
	serviceWork := true
	return profile.Document{
		Identity:          "Full-Stack Software Developer",
		PrimaryStrength:   "Backend Engineering",
		SecondaryPosition: "Full-Stack Development",
		DesiredRoles: []profile.JobFamily{
			{ID: "role-backend", Name: "Backend Engineering", Aliases: []string{"Backend Engineer", "Backend Developer"}},
			{ID: "role-fullstack", Name: "Full-Stack Development", Aliases: []string{"Full Stack Developer", "Full-Stack Developer"}},
		},
		Technologies: []profile.Technology{
			{ID: "tech-go", Name: "Go", Aliases: []string{"Golang"}},
			{ID: "tech-postgres", Name: "PostgreSQL", Aliases: []string{"Postgres"}},
		},
		Capabilities: []profile.Capability{
			{ID: "cap-api", Name: "API development", Description: "Designs and builds REST APIs",
				TechnologyIDs: []string{"tech-go"}, RoleFamilyIDs: []string{"role-backend"}},
			{ID: "cap-data", Name: "Database design", Description: "Relational database and schema design",
				TechnologyIDs: []string{"tech-postgres"}, RoleFamilyIDs: []string{"role-backend"}},
			{ID: "cap-distributed", Name: "Distributed systems", Description: "Distributed workflow design",
				RoleFamilyIDs: []string{"role-backend"}},
			{ID: "cap-payments", Name: "Payment integration", Description: "Payment gateway and M-Pesa integration work",
				RoleFamilyIDs: []string{"role-backend"}},
		},
		Projects: []profile.Project{
			{ID: "proj-payments", Name: "Synthetic payments platform",
				Description: "A synthetic payments system used only as a test fixture",
				Responsibilities: []profile.Fact{
					{ID: "fact-go", Text: "Implemented Go services backed by PostgreSQL transactions"},
					{ID: "fact-workflow", Text: "Designed a distributed systems workflow"},
				},
				TechnologyIDs: []string{"tech-go", "tech-postgres"},
				CapabilityIDs: []string{"cap-api", "cap-data", "cap-distributed"},
				Domains:       []string{"payments"}},
		},
		Engagement: &profile.Engagement{EmploymentTypes: []string{"full-time"}, ServiceWork: &serviceWork},
	}
}

func assess(doc *profile.Document, title, description string) opportunity.MatchResult {
	return opportunity.Assess(opportunity.AssessInput{
		OpportunityID: 11, OpportunityRevision: 1,
		Facts: opportunity.DiscoveredOpportunity{Title: title, Description: description,
			DescriptionStatus: "complete", Source: "fixture", Availability: "open", RemoteStatus: "unknown"},
		Profile: doc, ProfileRevisionID: 7,
	})
}

const backendVacancy = `Requirements:
- Strong experience with Go
- Experience with PostgreSQL and schema design
- Experience building REST APIs
- Experience with distributed systems

Employment type: Full-time
`

func relevantCompany() relevance.Result {
	return relevance.Result{Relevant: true, Score: 60, AssessorVersion: relevance.AssessorVersion}
}

// N and O. A high company score and an explicit operator request cannot turn an unrelated role
// into an application.
func TestUnrelatedRoleCanNeverBecomeSpecificApplication(t *testing.T) {
	doc := syntheticProfile()
	match := assess(&doc, "Marketing Manager", "Requirements:\n- Experience with Go and REST APIs\n")
	strongCompany := relevance.Result{Relevant: true, Score: 100, AssessorVersion: relevance.AssessorVersion}

	decision := ResolveIntent(IntentInput{Profile: &doc, Match: &match,
		Relevance: strongCompany, Requested: SpecificJobApplication})
	if decision.Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", decision.Outcome)
	}
	if !decision.Blockers.Has("unrelated_application_role") {
		t.Fatalf("missing unrelated_application_role blocker: %v", decision.Blockers.Codes())
	}
	if decision.Ready() {
		t.Fatal("a blocked decision must never be recordable as a plan")
	}
}

// A. The unrelated listing may support a speculative enquiry as provenance only.
func TestUnrelatedDiscoverySupportsSpeculativeEnquiryAsProvenanceOnly(t *testing.T) {
	doc := syntheticProfile()
	discovery := assess(&doc, "Marketing Manager", "Requirements:\n- Run our software campaign\n")
	decision := ResolveIntent(IntentInput{Profile: &doc, UnrelatedDiscovery: &discovery,
		Relevance: relevantCompany(), Requested: SpeculativeJobEnquiry})
	if decision.Outcome != OutcomeSuggested {
		t.Fatalf("outcome = %q, want suggested; reasons %v blockers %v",
			decision.Outcome, decision.Reasons.Codes(), decision.Blockers.Codes())
	}
	if !decision.Reasons.Has("unrelated_discovery_is_provenance_only") {
		t.Fatalf("provenance must be stated explicitly: %v", decision.Reasons.Codes())
	}
	if len(decision.TargetFamilies) == 0 {
		t.Fatal("a speculative enquiry must target approved engineering families")
	}
	for _, family := range decision.TargetFamilies {
		if strings.Contains(strings.ToLower(family.Name), "marketing") {
			t.Fatalf("the unrelated advertised title leaked into the target families: %v", decision.TargetFamilies)
		}
	}
	selection := SelectProof(SelectInput{Profile: &doc, Intent: SpeculativeJobEnquiry,
		Match: &discovery, Relevance: relevantCompany()})
	if !hasClaim(selection, "unrelated_title_as_target_role") {
		t.Fatalf("the composer must be forbidden from using the unrelated title as the role sought: %v", claimCodes(selection))
	}
}

// Without company relevance there is nothing honest to enquire about.
func TestSpeculativeEnquiryNeedsCompanyRelevance(t *testing.T) {
	doc := syntheticProfile()
	decision := ResolveIntent(IntentInput{Profile: &doc,
		Relevance: relevance.Result{Relevant: false}, Requested: SpeculativeJobEnquiry})
	if decision.Outcome != OutcomeIgnore {
		t.Fatalf("outcome = %q, want ignore", decision.Outcome)
	}
	if !decision.Blockers.Has("no_company_relevance") {
		t.Fatalf("missing no_company_relevance blocker: %v", decision.Blockers.Codes())
	}
}

// A strong fit may be proposed automatically; a possible fit always requires review.
func TestStrongFitIsSuggestedAndPossibleFitRequiresReview(t *testing.T) {
	doc := syntheticProfile()
	strong := assess(&doc, "Backend Engineer", backendVacancy)
	if strong.Classification != opportunity.ClassificationStrong {
		t.Fatalf("fixture is not a strong match: %s %v", strong.Classification, strong.Blockers.Codes())
	}
	decision := ResolveIntent(IntentInput{Profile: &doc, Match: &strong,
		Relevance: relevantCompany(), Requested: SpecificJobApplication})
	if decision.Outcome != OutcomeSuggested || decision.RequiresReview {
		t.Fatalf("strong fit: outcome = %q requiresReview = %v, want suggested/false", decision.Outcome, decision.RequiresReview)
	}

	possible := assess(&doc, "Backend Engineer", "Requirements:\n- Experience with Go\n- Experience with React\n")
	if possible.Classification != opportunity.ClassificationPossible {
		t.Fatalf("fixture is not a possible match: %s", possible.Classification)
	}
	decision = ResolveIntent(IntentInput{Profile: &doc, Match: &possible,
		Relevance: relevantCompany(), Requested: SpecificJobApplication})
	if decision.Outcome != OutcomeReviewRequired || !decision.RequiresReview {
		t.Fatalf("possible fit: outcome = %q requiresReview = %v, want review_required/true", decision.Outcome, decision.RequiresReview)
	}
	if !decision.Reasons.Has("possible_fit_requires_review") {
		t.Fatalf("missing possible_fit_requires_review: %v", decision.Reasons.Codes())
	}
}

// L. Unknown engagement preferences are surfaced as a review reason, never filled in.
func TestUnknownEngagementPreferencesForceReview(t *testing.T) {
	doc := syntheticProfile()
	doc.Engagement = nil
	match := assess(&doc, "Backend Engineer", backendVacancy)
	decision := ResolveIntent(IntentInput{Profile: &doc, Match: &match, Relevance: relevantCompany()})
	if decision.Outcome != OutcomeReviewRequired {
		t.Fatalf("outcome = %q, want review_required", decision.Outcome)
	}
	if !decision.Reasons.Has("engagement_preferences_unknown") {
		t.Fatalf("missing engagement_preferences_unknown: %v", decision.Reasons.Codes())
	}
	if doc.Engagement != nil {
		t.Fatal("intent resolution must never fill in engagement preferences")
	}
}

// When both purposes are plausible the system refuses to guess.
func TestAmbiguousPurposeRequiresAnExplicitChoice(t *testing.T) {
	doc := syntheticProfile()
	decision := ResolveIntent(IntentInput{Profile: &doc, Relevance: relevantCompany()})
	if decision.Outcome != OutcomeReviewRequired {
		t.Fatalf("outcome = %q, want review_required", decision.Outcome)
	}
	if !decision.Reasons.Has("ambiguous_purpose") {
		t.Fatalf("missing ambiguous_purpose: %v", decision.Reasons.Codes())
	}
}

// A website with a payment method is not a reason to offer services.
func TestServiceOutreachNeedsRelevanceNotAWebsite(t *testing.T) {
	doc := syntheticProfile()
	decision := ResolveIntent(IntentInput{Profile: &doc,
		Relevance: relevance.Result{Relevant: false, Score: 40}, Requested: ServiceOutreach})
	if decision.Outcome != OutcomeIgnore {
		t.Fatalf("outcome = %q, want ignore", decision.Outcome)
	}
	refused := false
	doc.Engagement.ServiceWork = &refused
	decision = ResolveIntent(IntentInput{Profile: &doc, Relevance: relevantCompany(), Requested: ServiceOutreach})
	if decision.Outcome != OutcomeBlocked || !decision.Blockers.Has("service_work_not_permitted") {
		t.Fatalf("a refused service preference must block: %q %v", decision.Outcome, decision.Blockers.Codes())
	}
}

// Nothing is authoritative without an active approved profile.
func TestNoActiveProfileBlocksEveryIntent(t *testing.T) {
	for _, intent := range []Intent{SpecificJobApplication, SpeculativeJobEnquiry, ServiceOutreach, ""} {
		decision := ResolveIntent(IntentInput{Relevance: relevantCompany(), Requested: intent})
		if decision.Outcome != OutcomeBlocked || !decision.Blockers.Has("profile_required") {
			t.Fatalf("intent %q: outcome = %q blockers %v, want blocked/profile_required",
				intent, decision.Outcome, decision.Blockers.Codes())
		}
		if decision.Ready() {
			t.Fatalf("intent %q became ready without an active approved profile", intent)
		}
	}
}

// I. A payments project proves backend ability without implying the recipient needs payment work.
func TestPaymentsProjectIsBackendProofOnlyWithoutPaymentNeed(t *testing.T) {
	doc := syntheticProfile()
	match := assess(&doc, "Backend Engineer", backendVacancy)
	if match.StatesPaymentRequirement() {
		t.Fatal("the backend fixture must not state a payment requirement")
	}
	selection := SelectProof(SelectInput{Profile: &doc, Intent: SpecificJobApplication,
		Match: &match, Relevance: relevantCompany()})

	project, ok := findSelected(selection.Projects, "proj-payments")
	if !ok {
		t.Fatalf("the payments project should be selectable as backend proof: %+v", selection.Projects)
	}
	if project.UseAs != UseAsProof {
		t.Fatalf("payments project use_as = %q, want proof", project.UseAs)
	}
	if !hasClaim(selection, "recipient_payment_problem") {
		t.Fatalf("a payment diagnosis must be explicitly forbidden: %v", claimCodes(selection))
	}
	if !selection.Notes.Has("payments_item_as_backend_proof_only") {
		t.Fatalf("missing payments-as-backend-proof note: %v", selection.Notes.Codes())
	}
	for _, capability := range selection.Capabilities {
		if capability.ID == "cap-payments" && capability.UseAs == UseAsCapability {
			t.Fatal("a payments capability must not lead when no payment need is evidenced")
		}
	}
	// The backend abilities are still offered plainly.
	for _, want := range []string{"cap-api", "cap-data"} {
		if _, ok := findSelected(selection.Capabilities, want); !ok {
			t.Fatalf("backend capability %s should be selected; got %v", want, selectedIDs(selection.Capabilities))
		}
	}
}

// H. Where the vacancy really does demand payment engineering, the payments capability leads.
func TestExplicitPaymentRequirementAllowsPaymentCapability(t *testing.T) {
	doc := syntheticProfile()
	match := assess(&doc, "Backend Engineer", `Requirements:
- Experience with Go
- Build our M-Pesa payment integration
- Experience with PostgreSQL
`)
	selection := SelectProof(SelectInput{Profile: &doc, Intent: SpecificJobApplication,
		Match: &match, Relevance: relevantCompany()})
	capability, ok := findSelected(selection.Capabilities, "cap-payments")
	if !ok {
		t.Fatalf("the approved payment capability should be selectable: %v", selectedIDs(selection.Capabilities))
	}
	if capability.UseAs != UseAsCapability {
		t.Fatalf("payment capability use_as = %q, want capability when a payment need is evidenced", capability.UseAs)
	}
	if hasClaim(selection, "recipient_payment_problem") {
		t.Fatalf("payment work is evidenced here, so it must not be a forbidden claim: %v", claimCodes(selection))
	}
	if !selection.Notes.Has("payment_need_evidenced") {
		t.Fatalf("missing payment_need_evidenced note: %v", selection.Notes.Codes())
	}
}

// G. With only a checkout on file, nothing payment-specific is offered.
func TestCheckoutOnlyCompanySelectsNoPaymentOffer(t *testing.T) {
	doc := syntheticProfile()
	selection := SelectProof(SelectInput{Profile: &doc, Intent: ServiceOutreach,
		Relevance: relevance.Result{Relevant: true, Score: 30}})
	for _, capability := range selection.Capabilities {
		if capability.ID == "cap-payments" {
			t.Fatalf("no payment need is evidenced, so a payments capability must not be offered: %+v", capability)
		}
	}
	if !hasClaim(selection, "recipient_payment_problem") {
		t.Fatalf("a payment diagnosis must be forbidden: %v", claimCodes(selection))
	}
	if selection.Positioning != PositioningBroad {
		t.Fatalf("positioning = %q, want broad positioning on weak evidence", selection.Positioning)
	}
}

// D. A missing requirement is a disclosed gap the composer may never claim.
func TestMissingRequirementBecomesAForbiddenClaim(t *testing.T) {
	doc := syntheticProfile()
	match := assess(&doc, "Backend Engineer", `Requirements:
- Experience with Go
- Experience with PostgreSQL
- Experience building REST APIs
- Experience with React
`)
	selection := SelectProof(SelectInput{Profile: &doc, Intent: SpecificJobApplication,
		Match: &match, Relevance: relevantCompany()})
	if !hasClaim(selection, "unapproved_requirement_react") {
		t.Fatalf("React must be a forbidden claim: %v", claimCodes(selection))
	}
	for _, ref := range selection.AllowedRefs {
		if strings.Contains(strings.ToLower(ref), "react") {
			t.Fatalf("React must never appear in the allowed references: %v", selection.AllowedRefs)
		}
	}
	if !hasClaim(selection, "unselected_profile_item") {
		t.Fatalf("the composer must be confined to the selected subset: %v", claimCodes(selection))
	}
}

// Selection is bounded, so the future composer cannot roam the whole profile.
func TestSelectionIsBounded(t *testing.T) {
	doc := syntheticProfile()
	match := assess(&doc, "Backend Engineer", backendVacancy)
	selection := SelectProof(SelectInput{Profile: &doc, Intent: SpecificJobApplication,
		Match: &match, Relevance: relevantCompany()})
	if len(selection.Capabilities) > MaxCapabilities {
		t.Fatalf("selected %d capabilities, maximum is %d", len(selection.Capabilities), MaxCapabilities)
	}
	if len(selection.Projects) > MaxProjects {
		t.Fatalf("selected %d projects, maximum is %d", len(selection.Projects), MaxProjects)
	}
	if len(selection.AllowedRefs) != len(selection.Capabilities)+len(selection.Projects) {
		t.Fatalf("allowed refs must be exactly the selected items: %v", selection.AllowedRefs)
	}
	if selection.Identity != doc.Identity || selection.PrimaryStrength != doc.PrimaryStrength {
		t.Fatalf("selection must carry the approved identity, got %q / %q", selection.Identity, selection.PrimaryStrength)
	}
}

// M and section 14. Recipients are ranked by their function for the purpose at hand.
func TestRecipientRankingRespectsIntent(t *testing.T) {
	contacts := relevance.ClassifyContacts([]relevance.Contact{
		{ID: 1, Kind: "email", Value: "info@example.test", Quality: 1, Status: "active"},
		{ID: 2, Kind: "email", Value: "careers@example.test", Quality: 2, Status: "active"},
		{ID: 3, Kind: "email", Value: "cto@example.test", Quality: 2, Status: "active"},
		{ID: 4, Kind: "email", Value: "dead@example.test", Quality: 3, Status: "bad"},
	})
	options := RankRecipients(SpecificJobApplication, contacts, SourceApplicationAddress{})
	best, ok := BestRecipient(options)
	if !ok {
		t.Fatal("a usable recipient should be found")
	}
	if best.Relevance != RecipientPreferred {
		t.Fatalf("best recipient relevance = %q, want preferred", best.Relevance)
	}
	for _, o := range options {
		switch o.Assessment.Contact.ID {
		case 1:
			if o.Relevance != RecipientReview {
				t.Fatalf("a generic mailbox must be review_required, got %q", o.Relevance)
			}
		case 4:
			if o.Relevance != RecipientUnusable {
				t.Fatalf("a contact marked bad must be unusable, got %q", o.Relevance)
			}
		}
	}
	// A published application address outranks everything else.
	options = RankRecipients(SpecificJobApplication, contacts, SourceApplicationAddress{Email: "info@example.test"})
	best, _ = BestRecipient(options)
	if best.Assessment.Contact.ID != 1 {
		t.Fatalf("the source's own application address should win, got contact %d", best.Assessment.Contact.ID)
	}

	// A recruiter is the wrong audience for a service offer.
	service := RankRecipients(ServiceOutreach, contacts, SourceApplicationAddress{})
	for _, o := range service {
		if o.Assessment.Contact.ID == 2 && o.Relevance != RecipientReview {
			t.Fatalf("a careers mailbox is not a service audience, got %q", o.Relevance)
		}
	}
}

// A company with only a generic mailbox can still be contacted, but only after review.
func TestGenericMailboxOnlyCompanyRequiresReview(t *testing.T) {
	contacts := relevance.ClassifyContacts([]relevance.Contact{
		{ID: 1, Kind: "email", Value: "info@example.test", Quality: 1, Status: "active"},
	})
	best, ok := BestRecipient(RankRecipients(SpeculativeJobEnquiry, contacts, SourceApplicationAddress{}))
	if !ok {
		t.Fatal("a generic mailbox is still reachable")
	}
	if best.Relevance != RecipientReview {
		t.Fatalf("relevance = %q, want review_required", best.Relevance)
	}
	if !strings.Contains(best.Reason, "unverified") {
		t.Fatalf("the reason should say the relevance is unverified: %q", best.Reason)
	}
}

func findSelected(items []Selected, id string) (Selected, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return Selected{}, false
}

func selectedIDs(items []Selected) []string {
	out := []string{}
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func hasClaim(s Selection, code string) bool {
	for _, claim := range s.ForbiddenClaims {
		if claim.Code == code {
			return true
		}
	}
	return false
}

func claimCodes(s Selection) []string {
	out := []string{}
	for _, claim := range s.ForbiddenClaims {
		out = append(out, claim.Code)
	}
	return out
}
