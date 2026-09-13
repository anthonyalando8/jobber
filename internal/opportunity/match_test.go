package opportunity

import (
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/profile"
)

// syntheticProfile is a fixture, not the operator's real facts. Nothing here is approved or
// activated on any live database; the technologies, capabilities and the payments project are
// invented purely so the deterministic rules have something to match against.
func syntheticProfile() profile.Document {
	serviceWork, remote := true, true
	return profile.Document{
		Identity:          "Full-Stack Software Developer",
		PrimaryStrength:   "Backend Engineering",
		SecondaryPosition: "Full-Stack Development",
		DesiredRoles: []profile.JobFamily{
			{ID: "role-backend", Name: "Backend Engineering", Aliases: []string{"Backend Engineer", "Backend Developer"}},
			{ID: "role-fullstack", Name: "Full-Stack Development", Aliases: []string{"Full Stack Developer", "Full-Stack Developer", "Fullstack Developer"}},
			{ID: "role-software", Name: "Software Engineering", Aliases: []string{"Software Engineer", "Software Developer"}},
		},
		Technologies: []profile.Technology{
			{ID: "tech-go", Name: "Go", Aliases: []string{"Golang"}},
			{ID: "tech-postgres", Name: "PostgreSQL", Aliases: []string{"Postgres"}},
		},
		Capabilities: []profile.Capability{
			{ID: "cap-api", Name: "API development", Description: "Designs and builds REST APIs and service integrations",
				TechnologyIDs: []string{"tech-go"}, RoleFamilyIDs: []string{"role-backend"}},
			{ID: "cap-data", Name: "Database design", Description: "Relational database and schema design",
				TechnologyIDs: []string{"tech-postgres"}, RoleFamilyIDs: []string{"role-backend"}},
			{ID: "cap-distributed", Name: "Distributed systems", Description: "Distributed workflow and asynchronous processing design",
				RoleFamilyIDs: []string{"role-backend"}},
			{ID: "cap-payments", Name: "Payment integration", Description: "Payment gateway and M-Pesa integration work",
				RoleFamilyIDs: []string{"role-backend"}},
		},
		Projects: []profile.Project{
			{ID: "proj-payments", Name: "Synthetic payments platform",
				Description: "A synthetic payments system used only as a test fixture",
				Responsibilities: []profile.Fact{
					{ID: "fact-go", Text: "Implemented Go services backed by PostgreSQL transactions"},
					{ID: "fact-workflow", Text: "Designed a distributed systems workflow for asynchronous settlement"},
				},
				TechnologyIDs: []string{"tech-go", "tech-postgres"},
				CapabilityIDs: []string{"cap-api", "cap-data", "cap-distributed"},
				Domains:       []string{"payments"}},
		},
		Engagement: &profile.Engagement{
			EmploymentTypes: []string{"full-time", "contract"}, ServiceWork: &serviceWork, Remote: &remote},
	}
}

func assessFixture(t *testing.T, title, description, status string, doc *profile.Document) MatchResult {
	t.Helper()
	if status == "" {
		status = "complete"
	}
	return Assess(AssessInput{
		OpportunityID: 42, OpportunityRevision: 1,
		Facts: DiscoveredOpportunity{
			Title: title, Description: description, DescriptionStatus: status,
			Source: "fixture", Availability: "open", RemoteStatus: "unknown",
		},
		Profile: doc, ProfileRevisionID: 7,
	})
}

const backendDescription = `We are hiring a Backend Engineer to own our services.

Requirements:
- Strong experience with Go
- Experience with PostgreSQL and relational schema design
- Experience building REST APIs
- Experience with distributed systems

Employment type: Full-time
`

// A. A clearly non-engineering title stays unrelated no matter what the description says.
func TestUnrelatedTitleSurvivesEngineeringWordsInDescription(t *testing.T) {
	doc := syntheticProfile()
	cases := []struct{ name, title, description string }{
		{"marketing manager", "Marketing Manager",
			"Requirements:\n- Run our software campaign\n- Experience with Go and REST APIs\n"},
		{"finance officer", "Finance Officer",
			"Requirements:\n- Own the billing system and collections\n- Reconciliation of payments\n"},
		{"product marketing manager", "Product Marketing Manager",
			"Requirements:\n- Own API integrations messaging\n- Experience with PostgreSQL\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := assessFixture(t, tc.title, tc.description, "complete", &doc)
			if result.Classification != ClassificationUnrelated {
				t.Fatalf("classification = %q, want unrelated", result.Classification)
			}
			if result.AllowsSpecificApplication() {
				t.Fatal("an unrelated role must never allow a specific job application")
			}
			if result.Score != 0 {
				t.Fatalf("score = %d, want 0 for an unrelated role", result.Score)
			}
			if !result.Blockers.Has("unrelated_application_role") {
				t.Fatalf("missing unrelated_application_role blocker: %v", result.Blockers.Codes())
			}
			if result.OriginalTitle != tc.title {
				t.Fatalf("original advertised title was not preserved: %q", result.OriginalTitle)
			}
		})
	}
}

// B. A finance role using billing and reconciliation vocabulary must not read as a payment
// engineering opportunity.
func TestFinanceRoleWithBillingTextStatesNoPaymentRequirement(t *testing.T) {
	doc := syntheticProfile()
	result := assessFixture(t, "Finance Officer",
		"Requirements:\n- Manage billing, collections and payment reconciliation\n", "complete", &doc)
	if result.Classification != ClassificationUnrelated {
		t.Fatalf("classification = %q, want unrelated", result.Classification)
	}
	if result.StatesPaymentRequirement() {
		t.Fatal("an unrelated finance role must not be read as stating a payment-engineering requirement")
	}
}

// C. A genuine backend vacancy whose requirements are supported reaches strong match, and says
// exactly why.
func TestBackendEngineerStrongMatchExplainsItself(t *testing.T) {
	doc := syntheticProfile()
	result := assessFixture(t, "Backend Engineer", backendDescription, "complete", &doc)
	if result.Classification != ClassificationStrong {
		t.Fatalf("classification = %q (score %d), want strong_match; blockers %v",
			result.Classification, result.Score, result.Blockers.Codes())
	}
	if result.FamilyConfidence != FamilyClear || result.FamilyID != "role-backend" {
		t.Fatalf("family = %q/%q, want clear/role-backend", result.FamilyConfidence, result.FamilyID)
	}
	if !result.AutoSuggestsApplication() || !result.AllowsSpecificApplication() {
		t.Fatal("a strong match should support a proposed specific application")
	}
	if !result.Reasons.Has("desired_role_family_matched") {
		t.Fatalf("missing family reason: %v", result.Reasons.Codes())
	}
	if !result.Reasons.Has("capability_supported_by_requirement") {
		t.Fatalf("missing capability reasons: %v", result.Reasons.Codes())
	}
	if !result.Reasons.Has("project_supports_capability") {
		t.Fatalf("missing project reason: %v", result.Reasons.Codes())
	}
	for _, want := range []string{"Go", "PostgreSQL", "REST APIs", "Distributed systems"} {
		if !hasMatchedTerm(result.Matched, want) {
			t.Fatalf("requirement %q was not matched; matched = %v", want, matchedTerms(result.Matched))
		}
	}
	if len(result.SupportedCapability) < 2 {
		t.Fatalf("supported capabilities = %v, want at least two", result.SupportedCapability)
	}
}

// D. A relevant role does not become unrelated because one requirement is missing; the missing
// technology stays a disclosed gap and is never added to the profile.
func TestPartialFitKeepsBackendMatchesAndDisclosesMissingReact(t *testing.T) {
	doc := syntheticProfile()
	description := `Requirements:
- Strong experience with Go
- Experience with PostgreSQL
- Experience building REST APIs
- Experience with React on the frontend
`
	result := assessFixture(t, "Backend Engineer", description, "complete", &doc)
	if result.Classification != ClassificationPossible {
		t.Fatalf("classification = %q, want possible_match", result.Classification)
	}
	if result.FamilyConfidence != FamilyClear {
		t.Fatalf("family confidence = %q, want clear: a missing requirement must not unrelate a backend role", result.FamilyConfidence)
	}
	for _, want := range []string{"Go", "PostgreSQL", "REST APIs"} {
		if !hasMatchedTerm(result.Matched, want) {
			t.Fatalf("verified backend match %q was lost; matched = %v", want, matchedTerms(result.Matched))
		}
	}
	gap, ok := findGap(result.Gaps, "React")
	if !ok {
		t.Fatalf("React was not recorded as a gap; gaps = %v", gapTerms(result.Gaps))
	}
	if gap.Outcome != MatchUnmet {
		t.Fatalf("React gap outcome = %q, want unmet", gap.Outcome)
	}
	if len(gap.ProfileRefs) != 0 {
		t.Fatalf("React must not reference any approved profile item, got %v", gap.ProfileRefs)
	}
	if !result.Blockers.Has("unresolved_mandatory_requirement") {
		t.Fatalf("missing unresolved mandatory requirement blocker: %v", result.Blockers.Codes())
	}
	for _, tech := range doc.Technologies {
		if strings.EqualFold(tech.Name, "React") {
			t.Fatal("React must never be added to the approved profile")
		}
	}
}

// E. A full-stack title matches the approved full-stack family, but an unapproved frontend
// framework is still only a gap.
func TestFullStackTitleMatchesApprovedFamilyWithoutInventingFrontend(t *testing.T) {
	doc := syntheticProfile()
	for _, title := range []string{"Full Stack Developer", "Full-Stack Developer", "Fullstack Developer", "Full‑Stack Engineer"} {
		result := assessFixture(t, title, backendDescription, "complete", &doc)
		if result.FamilyConfidence != FamilyClear {
			t.Fatalf("%q: family confidence = %q, want clear", title, result.FamilyConfidence)
		}
		if result.FamilyID != "role-fullstack" {
			t.Fatalf("%q: family = %q, want role-fullstack", title, result.FamilyID)
		}
	}
	result := assessFixture(t, "Full-Stack Developer",
		"Requirements:\n- Experience with Go\n- Experience with PostgreSQL\n- Experience with Vue\n", "complete", &doc)
	if _, ok := findGap(result.Gaps, "Vue"); !ok {
		t.Fatalf("an unapproved frontend framework must appear as a gap; gaps = %v", gapTerms(result.Gaps))
	}
	for _, rm := range result.Matched {
		if rm.Requirement.Type == TypeFrontend {
			t.Fatalf("no frontend capability is approved, yet %q was matched", rm.Requirement.Term)
		}
	}
}

// F. A title alone is never enough for a strong match.
func TestWeakDescriptionCannotReachStrongMatch(t *testing.T) {
	doc := syntheticProfile()
	for _, tc := range []struct{ name, description, status string }{
		{"no description", "", "unavailable"},
		{"partial description", "Requirements:\n- Experience with Go\n", "partial"},
		{"complete but thin", "We need a backend person.", "complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := assessFixture(t, "Backend Engineer", tc.description, tc.status, &doc)
			if result.Classification != ClassificationPossible {
				t.Fatalf("classification = %q, want possible_match", result.Classification)
			}
			if !result.Blockers.Has("insufficient_requirements") {
				t.Fatalf("missing insufficient_requirements blocker: %v", result.Blockers.Codes())
			}
		})
	}
}

// A missing advertised title cannot establish a family, so it can never become an application.
func TestMissingTitleIsInsufficient(t *testing.T) {
	doc := syntheticProfile()
	result := assessFixture(t, "", backendDescription, "complete", &doc)
	if result.FamilyConfidence != FamilyNone {
		t.Fatalf("family confidence = %q, want none", result.FamilyConfidence)
	}
	if result.AllowsSpecificApplication() {
		t.Fatal("a vacancy with no advertised title must not authorize an application")
	}
	if !result.Reasons.Has("advertised_title_missing") {
		t.Fatalf("missing advertised_title_missing reason: %v", result.Reasons.Codes())
	}
}

// H. A vacancy that really does demand payment engineering states so, and the approved payments
// capability may then be matched directly.
func TestExplicitPaymentVacancyMatchesPaymentCapability(t *testing.T) {
	doc := syntheticProfile()
	description := `Requirements:
- Experience with Go
- Build and maintain our M-Pesa payment integration
- Experience with PostgreSQL
- Experience with REST APIs
`
	result := assessFixture(t, "Backend Engineer", description, "complete", &doc)
	if !result.StatesPaymentRequirement() {
		t.Fatal("a vacancy explicitly requiring payment integration should state a payment requirement")
	}
	if !containsString(result.SupportedCapability, "cap-payments") {
		t.Fatalf("approved payment capability was not matched; supported = %v", result.SupportedCapability)
	}
}

// An ambiguous engineering title is reviewable, never automatically strong.
func TestAmbiguousEngineeringTitleIsPossibleNotStrong(t *testing.T) {
	doc := syntheticProfile()
	result := assessFixture(t, "Engineer", backendDescription, "complete", &doc)
	if result.Classification != ClassificationPossible {
		t.Fatalf("classification = %q, want possible_match", result.Classification)
	}
	if result.FamilyConfidence != FamilyAmbiguous {
		t.Fatalf("family confidence = %q, want ambiguous", result.FamilyConfidence)
	}
	if !result.Blockers.Has("family_not_clear") {
		t.Fatalf("missing family_not_clear blocker: %v", result.Blockers.Codes())
	}
}

// A non-software engineering discipline is a business/discipline mismatch, not an ambiguity.
func TestNonSoftwareEngineeringDisciplinesAreUnrelated(t *testing.T) {
	doc := syntheticProfile()
	for _, title := range []string{"Sales Engineer", "Mechanical Engineer", "Civil Engineer", "Technical Recruiter"} {
		result := assessFixture(t, title, backendDescription, "complete", &doc)
		if result.Classification != ClassificationUnrelated {
			t.Fatalf("%q: classification = %q, want unrelated", title, result.Classification)
		}
	}
}

// An engineering title with an incidental domain word after the role designation stays relevant.
func TestDomainWordAfterRoleDesignationDoesNotUnrelateTheTitle(t *testing.T) {
	doc := syntheticProfile()
	result := assessFixture(t, "Software Engineer - Marketing Platform", backendDescription, "complete", &doc)
	if result.FamilyConfidence != FamilyClear {
		t.Fatalf("family confidence = %q, want clear", result.FamilyConfidence)
	}
}

// L. Unknown engagement preferences stay unknown: nothing is invented and the condition is
// disclosed as a gap rather than scored.
func TestUnknownEngagementPreferencesAreDisclosedNotInvented(t *testing.T) {
	doc := syntheticProfile()
	doc.Engagement = nil
	result := assessFixture(t, "Backend Engineer", backendDescription, "complete", &doc)
	gap, ok := findGap(result.Gaps, "employment: full-time")
	if !ok {
		t.Fatalf("employment condition should be a disclosed unknown; gaps = %v", gapTerms(result.Gaps))
	}
	if gap.Outcome != MatchUnknown {
		t.Fatalf("employment condition outcome = %q, want unknown", gap.Outcome)
	}
	if !result.Reasons.Has("condition_unknown") {
		t.Fatalf("missing condition_unknown reason: %v", result.Reasons.Codes())
	}
}

// A confirmed preference list that genuinely excludes the advertised terms is a conflict, and a
// conflict blocks a strong match.
func TestKnownPreferenceConflictBlocksStrongMatch(t *testing.T) {
	doc := syntheticProfile()
	doc.Engagement.EmploymentTypes = []string{"full-time"}
	description := strings.Replace(backendDescription, "Employment type: Full-time", "Employment type: Internship", 1)
	result := assessFixture(t, "Backend Engineer", description, "complete", &doc)
	if result.Classification == ClassificationStrong {
		t.Fatal("a known engagement conflict must prevent a strong match")
	}
	if !result.Blockers.Has("known_preference_conflict") {
		t.Fatalf("missing known_preference_conflict blocker: %v", result.Blockers.Codes())
	}
}

// K. Every vacancy at one company is assessed on its own.
func TestOpportunitiesAtOneCompanyAreAssessedIndependently(t *testing.T) {
	doc := syntheticProfile()
	want := map[string]string{
		"Marketing Manager":    ClassificationUnrelated,
		"Finance Officer":      ClassificationUnrelated,
		"Backend Engineer":     ClassificationStrong,
		"Full Stack Developer": ClassificationStrong,
	}
	for title, expected := range want {
		result := assessFixture(t, title, backendDescription, "complete", &doc)
		if result.Classification != expected {
			t.Fatalf("%q: classification = %q, want %q (blockers %v)",
				title, result.Classification, expected, result.Blockers.Codes())
		}
	}
}

// Without an active approved profile there is no authoritative fit at all.
func TestNoActiveProfileYieldsProfileRequired(t *testing.T) {
	result := assessFixture(t, "Backend Engineer", backendDescription, "complete", nil)
	if result.Classification != ClassificationProfileRequired {
		t.Fatalf("classification = %q, want profile_required", result.Classification)
	}
	if result.AllowsSpecificApplication() {
		t.Fatal("no application may be authorized without an active approved profile")
	}
	if !result.Reasons.Has("profile_required") {
		t.Fatalf("missing profile_required reason: %v", result.Reasons.Codes())
	}
}

// Unparsed requirement lines stay visible rather than being treated as absent requirements.
func TestUnrecognizedRequirementsStayVisible(t *testing.T) {
	doc := syntheticProfile()
	result := assessFixture(t, "Backend Engineer",
		"Requirements:\n- Experience with Go\n- Familiarity with our proprietary Zorbex ledger runtime\n", "complete", &doc)
	if len(result.Unparsed) == 0 {
		t.Fatal("an unrecognized requirement line must remain visible as unparsed")
	}
	if !result.Reasons.Has("unparsed_requirements_present") {
		t.Fatalf("missing unparsed_requirements_present reason: %v", result.Reasons.Codes())
	}
}

// Ordinary English must not be read as the Go programming language.
func TestGoIsNotMatchedFromOrdinaryEnglish(t *testing.T) {
	requirements := ExtractRequirements("Requirements:\n- Willing to go the extra mile for customers\n- Ready to go live quickly\n")
	for _, r := range requirements {
		if r.Term == "Go" {
			t.Fatalf("ordinary English was read as the Go language: %q", r.Excerpt)
		}
	}
	requirements = ExtractRequirements("Requirements:\n- Strong experience with Go\n")
	if len(requirements) == 0 || requirements[0].Term != "Go" {
		t.Fatalf("a genuine Go requirement was not recognized: %+v", requirements)
	}
}

// Perks and boilerplate are not requirements.
func TestIgnoredSectionsProduceNoRequirements(t *testing.T) {
	requirements := ExtractRequirements("Benefits:\n- Free lunch and AWS credits\n- Experience days out\n")
	if len(requirements) != 0 {
		t.Fatalf("benefits must not become requirements: %+v", requirements)
	}
}

func hasMatchedTerm(matched []RequirementMatch, term string) bool {
	for _, rm := range matched {
		if rm.Requirement.Term == term {
			return true
		}
	}
	return false
}

func matchedTerms(matched []RequirementMatch) []string {
	out := []string{}
	for _, rm := range matched {
		out = append(out, rm.Requirement.Term)
	}
	return out
}

func gapTerms(gaps []RequirementMatch) []string {
	out := []string{}
	for _, rm := range gaps {
		out = append(out, rm.Requirement.Term)
	}
	return out
}

func findGap(gaps []RequirementMatch, term string) (RequirementMatch, bool) {
	for _, rm := range gaps {
		if rm.Requirement.Term == term {
			return rm, true
		}
	}
	return RequirementMatch{}, false
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// Phase 3.5 calibration, from real stored data: a job board put a posting qualifier in front of
// the role, and reading "full time" as the role designation downgraded a clear Software
// Engineer title to ambiguous. Only listed qualifiers are skipped, so an ordinary leading noun
// still decides the family and an engineering-adjacent management title is not promoted.
func TestLeadingPostingQualifierDoesNotHideTheRole(t *testing.T) {
	doc := syntheticProfile()
	for _, tc := range []struct{ title, wantFamily string }{
		{"FULL TIME: Software Engineer Position - React and Rest", FamilyClear},
		{"Urgent: Backend Engineer", FamilyClear},
		{"Remote: Full Stack Developer", FamilyClear},
		{"Hiring: Marketing Manager", FamilyNone},
		{"Full time: Marketing Manager", FamilyNone},
		// A leading noun that is not a posting qualifier still decides the designation, so a
		// management title is not promoted into a hands-on engineering family.
		{"Manager, Software Engineering", FamilyAmbiguous},
		{"Software Engineer - Marketing Platform", FamilyClear},
	} {
		result := assessFixture(t, tc.title, backendDescription, "complete", &doc)
		if result.FamilyConfidence != tc.wantFamily {
			t.Fatalf("%q: family confidence = %q, want %q", tc.title, result.FamilyConfidence, tc.wantFamily)
		}
	}
}

// Phase 3.5 calibration, from real stored data: the remote boards carry Spanish and Portuguese
// titles. "DESARROLLADOR FULL STACK", a genuine Python/Angular/Go full-stack role, was rejected
// because no English role noun appeared. Recognizing the noun relaxes nothing else: the title
// still needs an approved desired specialization for a clear family, and a non-engineering
// function in the role designation still wins.
func TestNonEnglishEngineeringRoleNouns(t *testing.T) {
	doc := syntheticProfile()
	for _, tc := range []struct{ title, wantFamily string }{
		{"DESARROLLADOR FULL STACK", FamilyClear},
		{"Desarrollador/a Full Stack", FamilyClear},
		{"Desenvolvedor Backend", FamilyClear},
		{"Ingeniero de Software", FamilyClear},
		// An engineering noun without an approved specialization is still only ambiguous.
		{"Desarrollador", FamilyAmbiguous},
		// A non-engineering function still decides the designation.
		{"Ejecutivo de Marketing", FamilyNone},
	} {
		result := assessFixture(t, tc.title, backendDescription, "complete", &doc)
		if result.FamilyConfidence != tc.wantFamily {
			t.Fatalf("%q: family confidence = %q, want %q", tc.title, result.FamilyConfidence, tc.wantFamily)
		}
	}
}

// Phase 3.5 calibration, from real stored descriptions: listings state their whole stack in
// ordinary prose rather than in bullets, and reading only bullets and requirement sections lost
// all of it. A prose sentence is read only when it both states what is built or used and names
// something the dictionary recognizes, so company and benefits prose stays out.
func TestStackStatedInProseIsExtracted(t *testing.T) {
	requirements := ExtractRequirements(
		"Ship across web and mobile from the same codebase. We use Go, TypeScript, React, GraphQL and Postgres.\n")
	found := map[string]Requirement{}
	for _, r := range requirements {
		found[r.Term] = r
	}
	for _, want := range []string{"Go", "TypeScript", "React", "GraphQL", "PostgreSQL"} {
		if _, ok := found[want]; !ok {
			t.Fatalf("stack term %q stated in prose was not extracted; got %v", want, terms(requirements))
		}
	}
	// Prose does not say whether a demand is essential, so it is never treated as mandatory.
	for _, r := range requirements {
		if r.Mandatory {
			t.Fatalf("prose requirement %q must not be mandatory", r.Term)
		}
		if r.Section != SectionProse {
			t.Fatalf("prose requirement %q should be marked %q, got %q", r.Term, SectionProse, r.Section)
		}
		if r.Excerpt == "" {
			t.Fatalf("prose requirement %q lost its source excerpt", r.Term)
		}
	}
}

// A label introducing a list of technologies is the other real shape listings use.
func TestLabelledStackListIsExtracted(t *testing.T) {
	requirements := ExtractRequirements("Core: Linux, Nginx, PHP-FPM\nData: MySQL, Redis\nCloud: AWS\n")
	for _, want := range []string{"Linux", "PHP", "MySQL", "Redis", "AWS"} {
		if !hasTerm(requirements, want) {
			t.Fatalf("labelled stack term %q was not extracted; got %v", want, terms(requirements))
		}
	}
}

// Company and benefits prose must not become requirements, even though it is prose.
func TestCompanyProseDoesNotBecomeRequirements(t *testing.T) {
	for _, text := range []string{
		"Acme is the leading digital sleep clinic in the region, replacing slow referrals with same-week care.\n",
		"We are a remote-first team that values ownership, curiosity and shipping fast.\n",
		"Benefits:\nWe offer free lunch, AWS credits and a Kubernetes training budget.\n",
	} {
		if requirements := ExtractRequirements(text); len(requirements) != 0 {
			t.Fatalf("company or benefits prose became requirements: %v (from %q)", terms(requirements), text)
		}
	}
}

// Ordinary English that merely contains a dictionary word must not become a requirement. Each of
// these produced a false requirement against real stored descriptions before calibration.
func TestOrdinaryEnglishDoesNotBecomeRequirements(t *testing.T) {
	for _, tc := range []struct{ text, mustNot string }{
		{"You'll build the team and the systems the rest of engineering depends on.\n", "REST APIs"},
		{"Independent workers and full-time employees exploring client work are welcome to apply.\n", "Background processing"},
		{"Support campaign closeout, reconciliation, and reporting processes.\n", "Payment reconciliation"},
	} {
		if hasTerm(ExtractRequirements(tc.text), tc.mustNot) {
			t.Fatalf("%q was read as requirement %q", tc.text, tc.mustNot)
		}
	}
	// The genuine spellings still match.
	for _, tc := range []struct{ text, want string }{
		{"- Experience building REST APIs\n", "REST APIs"},
		{"- Experience with payment reconciliation at scale\n", "Payment reconciliation"},
		{"- Experience with background jobs and job queues\n", "Background processing"},
		{"- Strong automated testing discipline\n", "Automated testing"},
	} {
		if !hasTerm(ExtractRequirements(tc.text), tc.want) {
			t.Fatalf("genuine requirement %q was lost from %q", tc.want, tc.text)
		}
	}
}

// Requirement prose in Spanish is read too, since the boards carry it.
func TestSpanishRequirementProseIsExtracted(t *testing.T) {
	requirements := ExtractRequirements("Desarrollar servicios y APIs REST utilizando principalmente Python y Go.\n")
	for _, want := range []string{"Python", "Go"} {
		if !hasTerm(requirements, want) {
			t.Fatalf("Spanish requirement term %q was not extracted; got %v", want, terms(requirements))
		}
	}
}

func terms(requirements []Requirement) []string {
	out := []string{}
	for _, r := range requirements {
		if r.Term != "" {
			out = append(out, r.Term)
		}
	}
	return out
}

func hasTerm(requirements []Requirement, want string) bool {
	for _, r := range requirements {
		if r.Term == want {
			return true
		}
	}
	return false
}

// Phase 3.5 residual gaps found against real stored descriptions.
func TestCalibrationResidualExtractionCases(t *testing.T) {
	// "Familiarity with X" is the same cue as "familiar with X".
	if !hasTerm(ExtractRequirements("Familiarity with scripting languages (e.g., Python, Bash).\n"), "Python") {
		t.Fatal("a Familiarity-with sentence should be read as a requirement line")
	}
	// "Own the shared infrastructure stack (Postgres, Redis)" states what is operated.
	requirements := ExtractRequirements("Own the shared infrastructure stack (Postgres, Redis, Celery).\n")
	for _, want := range []string{"PostgreSQL", "Redis"} {
		if !hasTerm(requirements, want) {
			t.Fatalf("%q was not extracted from an ownership sentence; got %v", want, terms(requirements))
		}
	}
	// Bare "own" inside another word is not a signal.
	if len(ExtractRequirements("A sense of ownership is known to matter to our downtown team.\n")) != 0 {
		t.Fatal("ownership prose must not be read as a stack sentence")
	}
	// "Work Authorization" is an eligibility heading, not a demand to build authorization.
	if hasTerm(ExtractRequirements("- Work Authorization: must be eligible to work in the US\n"), "Authentication and authorization") {
		t.Fatal("work authorization was read as a backend authorization requirement")
	}
	if !hasTerm(ExtractRequirements("- Experience with authentication and authorization in APIs\n"), "Authentication and authorization") {
		t.Fatal("a genuine authorization requirement was lost")
	}
}

// Phase 3.5 calibration, against the real approved profile: a Go/TypeScript/React vacancy was
// matching 23 capabilities, including double-entry accounting, payment integrations and
// verifiable credentials, because those capabilities happen to reference Go and PostgreSQL. A
// shared technology says the candidate knows the technology; it does not say the vacancy demands
// that capability. The domain a candidate has worked in must not speak for the whole profile.
func TestSharedTechnologyDoesNotEvidenceAnUnrelatedCapability(t *testing.T) {
	doc := syntheticProfile()
	doc.Capabilities = append(doc.Capabilities, profile.Capability{
		ID: "cap-double-entry", Name: "Double-entry accounting systems",
		Description:   "Wallet and accounting architecture using journals and postings.",
		TechnologyIDs: []string{"tech-go", "tech-postgres"}, RoleFamilyIDs: []string{"role-backend"},
	})
	result := assessFixture(t, "Backend Engineer", backendDescription, "complete", &doc)
	if containsString(result.SupportedCapability, "cap-double-entry") {
		t.Fatalf("a Go and PostgreSQL vacancy must not evidence double-entry accounting; supported = %v",
			result.SupportedCapability)
	}
	// The capabilities the vacancy really does name are still supported.
	for _, want := range []string{"cap-api", "cap-data"} {
		if !containsString(result.SupportedCapability, want) {
			t.Fatalf("%s should still be supported; supported = %v", want, result.SupportedCapability)
		}
	}
	// A vacancy that does name the domain still matches the domain capability.
	payments := assessFixture(t, "Backend Engineer",
		"Requirements:\n- Experience with Go\n- Build our M-Pesa payment integration\n- Experience with PostgreSQL\n",
		"complete", &doc)
	if !containsString(payments.SupportedCapability, "cap-payments") {
		t.Fatalf("a vacancy naming payment integration should match the payments capability; supported = %v",
			payments.SupportedCapability)
	}
}

// The two capabilities that score are the two the vacancy asks for most, not the two that sort
// first alphabetically.
func TestSupportedCapabilitiesAreOrderedBySupport(t *testing.T) {
	doc := syntheticProfile()
	result := assessFixture(t, "Backend Engineer", backendDescription, "complete", &doc)
	if len(result.SupportedCapability) < 2 {
		t.Fatalf("expected several supported capabilities, got %v", result.SupportedCapability)
	}
	if result.SupportedCapability[0] != "cap-api" {
		t.Fatalf("the most-supported capability should lead, got %v", result.SupportedCapability)
	}
}
