package automation

import (
	"strings"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/relevance"
	"github.com/anthonyalando8/prospector/internal/send"
)

// eligible builds the one shape that passes: a strong-match job application, cleanly reviewed,
// to an identified engineering contact, with automation configured for that intent. Every test
// below starts from this and breaks exactly one thing, so a failure names the rule that matters
// rather than a pile of unrelated setup.
func eligible() Input {
	return Input{
		Now: time.Now().UTC(),
		Config: Config{Mode: send.ModeAuto,
			Allowlist: []string{string(plan.SpecificJobApplication)}, DailyCap: 1},
		Intent:           plan.SpecificJobApplication,
		SendPathReady:    true,
		ProfileActive:    true,
		ProfileRevision:  7,
		PlanStatus:       "selected",
		PlanRevision:     1,
		ContentHash:      "content-hash",
		CompositionState: "reviewed",
		CurrentRevision:  1,
		AttemptSource:    "generated",
		SemanticComplete: true,
		SemanticPassed:   true,
		Match: &opportunity.MatchResult{
			Classification:      opportunity.ClassificationStrong,
			FamilyConfidence:    opportunity.FamilyClear,
			DescriptionStatus:   "complete",
			SupportedCapability: []string{"cap-api", "cap-data"},
			Blockers:            explain.Reasons{},
		},
		OpportunityPinnedRevision:  3,
		OpportunityCurrentRevision: 3,
		OpportunityAvailability:    "open",
		Recipient: RecipientState{
			ContactID: 11, Address: "engineering@northwind.example",
			Function: relevance.FunctionEngineering, Status: "active",
			Endpoint: relevance.ClassifyEndpoint("engineering@northwind.example"),
		},
		BudgetAvailable: true,
	}
}

func blocked(d Decision, code string) bool { return d.Blockers.Has(code) }

// The control. Without this passing, every "not eligible" assertion below would be worthless.
func TestFullyCleanApplicationIsEligible(t *testing.T) {
	d := Evaluate(eligible())
	if !d.Eligible() {
		t.Fatalf("a fully clean strong-match application was refused: %v", d.Blockers.Codes())
	}
	if d.PolicyVersion != PolicyVersion {
		t.Fatalf("policy version = %q", d.PolicyVersion)
	}
	if len(d.Reasons) == 0 {
		t.Fatal("an eligible decision recorded no supporting reasons")
	}
}

// A. A possible match can never be automated.
func TestPossibleMatchIsNeverEligible(t *testing.T) {
	in := eligible()
	in.Match.Classification = opportunity.ClassificationPossible
	d := Evaluate(in)
	if d.Eligible() || !blocked(d, "not_a_strong_match") {
		t.Fatalf("a possible match was eligible: %v", d.Blockers.Codes())
	}
}

// B. An unrelated role can never be automated.
func TestUnrelatedRoleIsNeverEligible(t *testing.T) {
	in := eligible()
	in.Match.Classification = opportunity.ClassificationUnrelated
	in.Match.FamilyConfidence = opportunity.FamilyNone
	in.Match.Blockers = explain.Reasons{{Code: "unrelated_application_role",
		Text: "This role is not in an approved desired engineering family."}}
	d := Evaluate(in)
	if d.Eligible() {
		t.Fatal("an unrelated role was eligible")
	}
	if !blocked(d, "match_unrelated_application_role") {
		t.Fatalf("the matcher's own blocker was not carried through: %v", d.Blockers.Codes())
	}
}

// C and U. Recipients that are not a person, or not an identifiable one, are never automated.
func TestRecipientQualityGates(t *testing.T) {
	for name, tc := range map[string]struct {
		address  string
		function string
		want     string
	}{
		"generic mailbox":     {"info@northwind.example", relevance.FunctionGeneric, "recipient_generic_mailbox"},
		"unknown function":    {"jm@northwind.example", relevance.FunctionUnknown, "recipient_function_unknown"},
		"commercial function": {"sales@northwind.example", relevance.FunctionCommercial, "recipient_function_inappropriate"},
		// The exact live address found in Phase 6.
		"sentry ingest key": {"77585ea9215747518da6489b9fdd8494@o212792.ingest.sentry.io",
			relevance.FunctionUnknown, "recipient_not_a_human_mailbox"},
		"noreply": {"noreply@northwind.example", relevance.FunctionUnknown, "recipient_not_a_human_mailbox"},
	} {
		t.Run(name, func(t *testing.T) {
			in := eligible()
			in.Recipient.Address = tc.address
			in.Recipient.Function = tc.function
			in.Recipient.Endpoint = relevance.ClassifyEndpoint(tc.address)
			d := Evaluate(in)
			if d.Eligible() {
				t.Fatalf("%s was eligible", name)
			}
			if !blocked(d, tc.want) {
				t.Fatalf("blockers = %v, want %s", d.Blockers.Codes(), tc.want)
			}
		})
	}
}

// D. An identified recruiting contact is acceptable, as is an employer-published address.
func TestAppropriateRecipientsAreEligible(t *testing.T) {
	recruiting := eligible()
	recruiting.Recipient.Function = relevance.FunctionRecruiting
	recruiting.Recipient.Address = "careers@northwind.example"
	recruiting.Recipient.Endpoint = relevance.ClassifyEndpoint("careers@northwind.example")
	if d := Evaluate(recruiting); !d.Eligible() {
		t.Fatalf("a recruiting contact was refused: %v", d.Blockers.Codes())
	}

	// An address the employer itself published is the stated destination, so a generic-looking
	// local part is correct rather than suspicious.
	published := eligible()
	published.Recipient.Function = relevance.FunctionGeneric
	published.Recipient.SourcePublished = true
	published.Recipient.Address = "jobs@northwind.example"
	published.Recipient.Endpoint = relevance.ClassifyEndpoint("jobs@northwind.example")
	if d := Evaluate(published); !d.Eligible() {
		t.Fatalf("a source-published application address was refused: %v", d.Blockers.Codes())
	}

	// But a published address that is not a human mailbox is still refused.
	telemetry := published
	telemetry.Recipient.Address = "abc123def456abc123def456@sentry.io"
	telemetry.Recipient.Endpoint = relevance.ClassifyEndpoint(telemetry.Recipient.Address)
	if d := Evaluate(telemetry); d.Eligible() {
		t.Fatal("a telemetry endpoint was eligible because it was source-published")
	}
}

// E. A hand-edited revision is permanently outside automation.
func TestHumanEditedRevisionIsNeverEligible(t *testing.T) {
	in := eligible()
	in.AttemptSource = "human"
	d := Evaluate(in)
	if d.Eligible() || !blocked(d, "human_edited_revision") {
		t.Fatalf("a hand-edited revision was eligible: %v", d.Blockers.Codes())
	}
	// A later regenerated revision qualifies on its own.
	in.AttemptSource = "generated"
	if d := Evaluate(in); !d.Eligible() {
		t.Fatalf("a regenerated revision was refused: %v", d.Blockers.Codes())
	}
}

// F. Any semantic finding ends automation, including a review-severity one.
func TestSemanticFindingsEndAutomation(t *testing.T) {
	for name, finding := range map[string]Finding{
		"review severity": {Severity: "review", Code: "fit_uncertain", Text: "The fit is not clearly supported."},
		"block severity":  {Severity: "block", Code: "invented_fact", Text: "Unsupported claim."},
	} {
		t.Run(name, func(t *testing.T) {
			in := eligible()
			in.SemanticFindings = []Finding{finding}
			d := Evaluate(in)
			if d.Eligible() {
				t.Fatalf("a %s semantic finding was eligible", name)
			}
			if !blocked(d, "semantic_"+finding.Code) {
				t.Fatalf("blockers = %v", d.Blockers.Codes())
			}
		})
	}
	// An informational finding is not a reason to stop.
	in := eligible()
	in.SemanticFindings = []Finding{{Severity: "info", Code: "minor_length", Text: "Slightly long."}}
	if d := Evaluate(in); !d.Eligible() {
		t.Fatalf("an informational finding blocked automation: %v", d.Blockers.Codes())
	}
}

func TestIncompleteOrFailedReviewEndsAutomation(t *testing.T) {
	incomplete := eligible()
	incomplete.SemanticComplete = false
	if d := Evaluate(incomplete); d.Eligible() || !blocked(d, "semantic_review_incomplete") {
		t.Fatalf("an incomplete review was eligible: %v", d.Blockers.Codes())
	}
	failed := eligible()
	failed.SemanticPassed = false
	if d := Evaluate(failed); d.Eligible() || !blocked(d, "semantic_review_not_passed") {
		t.Fatalf("a failed review was eligible: %v", d.Blockers.Codes())
	}
}

func TestDeterministicFindingsEndAutomation(t *testing.T) {
	in := eligible()
	in.DeterministicFindings = []Finding{{Severity: "block", Code: "unapproved_technology",
		Text: "Names a technology outside the approved profile."}}
	d := Evaluate(in)
	if d.Eligible() || !blocked(d, "deterministic_unapproved_technology") {
		t.Fatalf("a deterministic finding was eligible: %v", d.Blockers.Codes())
	}
}

// G and S. Stale inputs and a moved or closed opportunity end automation.
func TestStaleAndChangedOpportunityEndAutomation(t *testing.T) {
	stale := eligible()
	stale.StaleReasons = []string{"opportunity_changed"}
	if d := Evaluate(stale); d.Eligible() || !blocked(d, "stale_inputs") {
		t.Fatalf("a stale draft was eligible: %v", d.Blockers.Codes())
	}

	moved := eligible()
	moved.OpportunityCurrentRevision = moved.OpportunityPinnedRevision + 1
	if d := Evaluate(moved); d.Eligible() || !blocked(d, "opportunity_revision_moved") {
		t.Fatalf("a superseded opportunity revision was eligible: %v", d.Blockers.Codes())
	}

	closed := eligible()
	closed.OpportunityAvailability = "closed"
	if d := Evaluate(closed); d.Eligible() || !blocked(d, "opportunity_closed") {
		t.Fatalf("a closed vacancy was eligible: %v", d.Blockers.Codes())
	}
}

// H. Suppression ends automation.
func TestSuppressionEndsAutomation(t *testing.T) {
	address := eligible()
	address.AddressSuppressed = true
	if d := Evaluate(address); d.Eligible() || !blocked(d, "address_suppressed") {
		t.Fatalf("a suppressed address was eligible: %v", d.Blockers.Codes())
	}
	domain := eligible()
	domain.DomainSuppressed = true
	if d := Evaluate(domain); d.Eligible() || !blocked(d, "domain_suppressed") {
		t.Fatalf("a suppressed domain was eligible: %v", d.Blockers.Codes())
	}
}

// I and X. An unresolved attempt halts automation for that message.
func TestUnresolvedAttemptHaltsAutomation(t *testing.T) {
	in := eligible()
	in.UnresolvedAttempts = 1
	d := Evaluate(in)
	if d.Eligible() || !blocked(d, "unresolved_send_attempt") {
		t.Fatalf("automation continued through unresolved delivery state: %v", d.Blockers.Codes())
	}
}

// J and K. Speculative enquiry and service outreach are review-required regardless of the
// allowlist, because the policy itself refuses to automate them.
func TestSpeculativeAndServiceAreNeverAutomatable(t *testing.T) {
	for _, intent := range []plan.Intent{plan.SpeculativeJobEnquiry, plan.ServiceOutreach} {
		t.Run(string(intent), func(t *testing.T) {
			in := eligible()
			in.Intent = intent
			// Deliberately allowlist it and satisfy everything else.
			in.Config.Allowlist = []string{string(intent)}
			in.Match = nil
			d := Evaluate(in)
			if d.Eligible() {
				t.Fatalf("%s was eligible even though the policy never automates it", intent)
			}
			if !blocked(d, "intent_not_automatable") {
				t.Fatalf("blockers = %v", d.Blockers.Codes())
			}
			if IntentCapable(intent) {
				t.Fatalf("%s must not be reported as automatable", intent)
			}
			if IntentCapabilityReason(intent) == "" {
				t.Fatal("the refusal has no stated reason")
			}
		})
	}
}

// N. The allowlist refuses unknown values, wildcards, and intents this policy never automates.
func TestAllowlistParsing(t *testing.T) {
	empty, err := ParseAllowlist("")
	if err != nil || len(empty) != 0 {
		t.Fatalf("an empty allowlist should parse to nothing: %v %v", empty, err)
	}
	one, err := ParseAllowlist(" Specific_Job_Application ")
	if err != nil || len(one) != 1 || one[0] != string(plan.SpecificJobApplication) {
		t.Fatalf("allowlist = %v, err = %v", one, err)
	}
	// Duplicates collapse rather than being rejected: the configuration still means one thing.
	if got, err := ParseAllowlist("specific_job_application,specific_job_application"); err != nil || len(got) != 1 {
		t.Fatalf("duplicates should collapse: %v %v", got, err)
	}
	for _, invalid := range []string{"all", "*", "any", "everything", "hiring", "application", "SPECULATIVE"} {
		if _, err := ParseAllowlist(invalid); err == nil {
			t.Fatalf("%q was accepted as an allowlist value", invalid)
		}
	}
	// An intent that exists but is never automatable is refused at configuration time, so the
	// mistake surfaces at startup rather than as a silent no-op per message.
	for _, refused := range []string{"speculative_job_enquiry", "service_outreach"} {
		if _, err := ParseAllowlist(refused); err == nil {
			t.Fatalf("%q was accepted even though the policy never automates it", refused)
		}
	}
}

// O. An empty allowlist produces no automatic candidates.
func TestEmptyAllowlistBlocksEverything(t *testing.T) {
	in := eligible()
	in.Config.Allowlist = []string{}
	d := Evaluate(in)
	if d.Eligible() || !blocked(d, "allowlist_empty") {
		t.Fatalf("an empty allowlist permitted automation: %v", d.Blockers.Codes())
	}
}

func TestNonAutoModeBlocksEverything(t *testing.T) {
	for _, mode := range []string{send.ModeDisabled, send.ModeReview, send.ModeManualSend} {
		in := eligible()
		in.Config.Mode = mode
		d := Evaluate(in)
		if d.Eligible() || !blocked(d, "rollout_mode_not_auto") {
			t.Fatalf("mode %s permitted automation: %v", mode, d.Blockers.Codes())
		}
	}
}

// P. The automation cap blocks further automatic sends.
func TestAutomationCapBlocksFurtherSends(t *testing.T) {
	in := eligible()
	in.AutoSentToday = in.Config.DailyCap
	d := Evaluate(in)
	if d.Eligible() || !blocked(d, "automation_cap_reached") {
		t.Fatalf("the automation cap did not engage: %v", d.Blockers.Codes())
	}
	// A cap of zero is not "unlimited"; it is a missing configuration.
	unset := eligible()
	unset.Config.DailyCap = 0
	if d := Evaluate(unset); d.Eligible() || !blocked(d, "automation_cap_not_configured") {
		t.Fatalf("a zero cap permitted automation: %v", d.Blockers.Codes())
	}
}

// M. A company already in conversation gets no second automatic first contact.
func TestOneAutomaticFirstContactPerCompany(t *testing.T) {
	in := eligible()
	in.ActiveThreadsForDomain = 1
	d := Evaluate(in)
	if d.Eligible() || !blocked(d, "domain_already_in_conversation") {
		t.Fatalf("a second parallel thread was permitted: %v", d.Blockers.Codes())
	}
}

// T. An inactive profile ends automation, so a profile change invalidates old eligibility.
func TestProfileMustBeActive(t *testing.T) {
	in := eligible()
	in.ProfileActive = false
	d := Evaluate(in)
	if d.Eligible() || !blocked(d, "profile_not_active") {
		t.Fatalf("automation ran without an active profile: %v", d.Blockers.Codes())
	}
}

// V. A draft a person approved stays on the manual path.
func TestManuallyApprovedDraftStaysManual(t *testing.T) {
	in := eligible()
	in.ApprovalExists = true
	d := Evaluate(in)
	if d.Eligible() || !blocked(d, "already_approved_manually") {
		t.Fatalf("a manually approved draft was picked up by automation: %v", d.Blockers.Codes())
	}
}

func TestBudgetAndSendPathGates(t *testing.T) {
	budget := eligible()
	budget.BudgetAvailable = false
	budget.BudgetReason = "daily AI request budget exhausted"
	d := Evaluate(budget)
	if d.Eligible() || !blocked(d, "budget_unavailable") {
		t.Fatalf("automation ran without budget: %v", d.Blockers.Codes())
	}
	if !strings.Contains(d.Blockers[len(d.Blockers)-1].Text, "exhausted") {
		t.Fatal("the budget blocker does not carry the actual reason")
	}

	notReady := eligible()
	notReady.SendPathReady = false
	notReady.SendPathReasons = []string{"SMTP credentials are not set"}
	if d := Evaluate(notReady); d.Eligible() || !blocked(d, "send_path_not_ready") {
		t.Fatalf("automation ran with an unready send path: %v", d.Blockers.Codes())
	}
}

// The matcher's own gates are carried through rather than re-implemented, so the two can never
// drift into disagreeing about the same opportunity.
func TestMatcherBlockersAreCarriedThrough(t *testing.T) {
	in := eligible()
	in.Match.Blockers = explain.Reasons{
		{Code: "unresolved_mandatory_requirement", Text: "Mandatory requirement Terraform is not supported."},
		{Code: "known_preference_conflict", Text: "The stated location conflicts with approved preferences."},
	}
	d := Evaluate(in)
	if d.Eligible() {
		t.Fatal("an opportunity with matcher blockers was eligible")
	}
	for _, want := range []string{"match_unresolved_mandatory_requirement", "match_known_preference_conflict"} {
		if !blocked(d, want) {
			t.Fatalf("blockers = %v, want %s", d.Blockers.Codes(), want)
		}
	}
}

// §4: a years-of-experience requirement the profile does not support is a matcher blocker, and
// automation inherits it. Nothing here infers experience from anything.
func TestYearsOfExperienceRequirementBlocksAutomation(t *testing.T) {
	in := eligible()
	in.Match.Classification = opportunity.ClassificationPossible
	in.Match.Blockers = explain.Reasons{{Code: "unresolved_mandatory_requirement",
		Text: "Mandatory requirement 5 years of experience is not supported by the approved profile."}}
	d := Evaluate(in)
	if d.Eligible() {
		t.Fatal("an unmet mandatory experience requirement did not block automation")
	}
	if !blocked(d, "not_a_strong_match") || !blocked(d, "match_unresolved_mandatory_requirement") {
		t.Fatalf("blockers = %v", d.Blockers.Codes())
	}
}

func TestIncompleteDescriptionAndWeakEvidenceBlock(t *testing.T) {
	partial := eligible()
	partial.Match.DescriptionStatus = "partial"
	if d := Evaluate(partial); d.Eligible() || !blocked(d, "description_not_complete") {
		t.Fatalf("a partial description was eligible: %v", d.Blockers.Codes())
	}
	thin := eligible()
	thin.Match.SupportedCapability = []string{"cap-api"}
	if d := Evaluate(thin); d.Eligible() || !blocked(d, "insufficient_supported_capabilities") {
		t.Fatalf("one supported capability was enough: %v", d.Blockers.Codes())
	}
	unclear := eligible()
	unclear.Match.FamilyConfidence = opportunity.FamilyAmbiguous
	if d := Evaluate(unclear); d.Eligible() || !blocked(d, "family_not_clear") {
		t.Fatalf("an ambiguous family was eligible: %v", d.Blockers.Codes())
	}
}

// W. Every blocker is reported, not merely the first, so the audit record explains the whole
// distance between this draft and eligibility.
func TestAllBlockersAreReported(t *testing.T) {
	in := eligible()
	in.Config.Mode = send.ModeReview
	in.Config.Allowlist = []string{}
	in.ProfileActive = false
	in.AttemptSource = "human"
	in.AddressSuppressed = true
	in.Match.Classification = opportunity.ClassificationPossible
	d := Evaluate(in)
	for _, want := range []string{
		"rollout_mode_not_auto", "allowlist_empty", "profile_not_active",
		"human_edited_revision", "address_suppressed", "not_a_strong_match",
	} {
		if !blocked(d, want) {
			t.Fatalf("blocker %s is missing from %v", want, d.Blockers.Codes())
		}
	}
	if len(d.Blockers) < 6 {
		t.Fatalf("only %d blockers reported; the decision must explain all of them", len(d.Blockers))
	}
}

// Configuration is necessary but never sufficient: a perfectly configured system still refuses
// a draft that does not qualify.
func TestConfigurationAloneCannotAuthorize(t *testing.T) {
	in := eligible()
	in.Match.Classification = opportunity.ClassificationPossible
	if d := Evaluate(in); d.Eligible() {
		t.Fatal("a fully permissive configuration made a possible match eligible")
	}
}

func TestLoadConfigValidation(t *testing.T) {
	cfg, err := LoadConfig(send.ModeAuto, "specific_job_application", "1")
	if err != nil || cfg.DailyCap != 1 || len(cfg.Allowlist) != 1 {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	if _, err := LoadConfig(send.ModeAuto, "all", ""); err == nil {
		t.Fatal("a wildcard allowlist was accepted")
	}
	if _, err := LoadConfig(send.ModeAuto, "", "not-a-number"); err == nil {
		t.Fatal("a non-numeric cap was accepted")
	}
	if _, err := LoadConfig(send.ModeAuto, "", "-1"); err == nil {
		t.Fatal("a negative cap was accepted")
	}
	// A cap far above what controlled observation means is refused rather than obeyed.
	if _, err := LoadConfig(send.ModeAuto, "", "50"); err == nil {
		t.Fatal("an implausibly high automation cap was accepted")
	}
	// The default is safe.
	def := DefaultConfig()
	if def.Mode != send.ModeReview || len(def.Allowlist) != 0 || def.DailyCap != 1 {
		t.Fatalf("the default configuration is not safe: %+v", def)
	}
}

// The computed automation state, which decides whether the policy is consulted at all.
func TestAutomationStateRequiresEverything(t *testing.T) {
	ready := send.Readiness{Ready: true}
	full := send.AutomationInputs{Mode: send.ModeAuto,
		Allowlist:     []string{string(plan.SpecificJobApplication)},
		SchemaVersion: send.RequiredAutomationSchemaVersion, Readiness: ready, DailyCap: 1}
	if !send.EvaluateAutomation(full).Enabled {
		t.Fatal("a fully configured automation state reported disabled")
	}
	for name, mutate := range map[string]func(send.AutomationInputs) send.AutomationInputs{
		"review mode":     func(i send.AutomationInputs) send.AutomationInputs { i.Mode = send.ModeReview; return i },
		"empty allowlist": func(i send.AutomationInputs) send.AutomationInputs { i.Allowlist = nil; return i },
		"old schema":      func(i send.AutomationInputs) send.AutomationInputs { i.SchemaVersion = 24; return i },
		"send not ready":  func(i send.AutomationInputs) send.AutomationInputs { i.Readiness = send.Readiness{}; return i },
		"no cap":          func(i send.AutomationInputs) send.AutomationInputs { i.DailyCap = 0; return i },
	} {
		state := send.EvaluateAutomation(mutate(full))
		if state.Enabled {
			t.Fatalf("automation reported enabled with %s", name)
		}
		if len(state.Reasons) == 0 {
			t.Fatalf("automation disabled by %s gave no reason", name)
		}
	}
}
