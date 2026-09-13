// Package automation decides whether one already-reviewed, already-approvable v2 message is safe
// enough to send without a person looking at it first.
//
// It is a decision layer on top of the existing pipeline, not a second pipeline. Everything it
// consults was produced by the ordinary path — the approved profile, the matcher, the contact
// plan, the two validation layers, the staleness rules, suppression and the send preflight — and
// nothing here can substitute for any of them. An eligible message still goes through
// send.Evaluate and still reserves its idempotency key before transmission.
//
// The default answer is review_required. Eligibility has to be argued for, item by item, and a
// single unsatisfied condition ends it.
package automation

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/relevance"
	"github.com/anthonyalando8/prospector/internal/send"
)

// PolicyVersion changes whenever the rules below change. It is persisted with every decision so
// an old decision can be read against the rules that actually produced it.
const PolicyVersion = "auto-v1"

// Outcomes.
const (
	EligibleForAuto = "eligible_for_auto"
	ReviewRequired  = "review_required"
)

// ModeAuto is re-exported from the send package so this package's callers do not have to import
// both to read one decision.
const ModeAuto = send.ModeAuto

// Intent capability. This is a property of the policy itself rather than of configuration: an
// intent that is not capable cannot be made automatic by any setting, including the allowlist.
//
// Only specific job applications are capable in auto-v1. A job application is anchored to an
// advertised role that the matcher verified against approved profile facts, which is the one
// case where "is this message truthful and appropriate" reduces to checks already enforced.
// Speculative enquiries and service outreach have no such anchor: their correctness depends on
// company-relevance evidence that the live corpus cannot yet support, and their failure mode is
// a fluent, plausible message that invents a reason to be writing. Human review is the control
// that catches exactly that, so it stays.
func IntentCapable(intent plan.Intent) bool {
	return intent == plan.SpecificJobApplication
}

// IntentCapabilityReason states why an intent is not capable, for the UI and the report.
func IntentCapabilityReason(intent plan.Intent) string {
	switch intent {
	case plan.SpecificJobApplication:
		return "A specific job application is anchored to an advertised role the matcher verified against approved profile facts."
	case plan.SpeculativeJobEnquiry:
		return "A speculative enquiry has no advertised role to anchor it. Its correctness depends on company-relevance " +
			"evidence with real provenance and an identified engineering or talent recipient, neither of which the stored " +
			"evidence currently supports. It stays human-reviewed."
	case plan.ServiceOutreach:
		return "Service outreach asserts that a company has an engineering need. Inventing that need from weak signals was " +
			"the previous pipeline's worst failure, and no automated check can establish it. It stays human-reviewed."
	}
	return "Unknown intent."
}

// Config is the operator's automation configuration. Every field here is necessary but not
// sufficient: none of them can make an individual message eligible.
type Config struct {
	Mode string
	// Allowlist names the intents that may even be considered. Empty means no automatic sends.
	Allowlist []string
	// DailyCap is the automation-specific cap, deliberately far below the manual send cap. Its
	// purpose is controlled observation, not throughput.
	DailyCap int
}

// DefaultConfig is safe: review-only, nothing allowlisted, one send a day if ever enabled.
func DefaultConfig() Config {
	return Config{Mode: send.ModeReview, Allowlist: []string{}, DailyCap: 1}
}

func (c Config) Allows(intent plan.Intent) bool {
	for _, allowed := range c.Allowlist {
		if allowed == string(intent) {
			return true
		}
	}
	return false
}

// ParseAllowlist reads AUTO_SEND_INTENTS. It refuses wildcards and unknown values rather than
// ignoring them: a typo that silently disabled automation would be safe, but a typo that
// silently enabled something would not, and a configuration that does not mean what it says is
// the wrong thing to run either way.
func ParseAllowlist(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}, nil
	}
	seen := map[string]bool{}
	intents := []string{}
	for _, part := range strings.Split(raw, ",") {
		value := strings.ToLower(strings.TrimSpace(part))
		if value == "" {
			continue
		}
		if value == "all" || value == "*" || value == "any" {
			return nil, fmt.Errorf("AUTO_SEND_INTENTS does not support %q: name each intent explicitly", value)
		}
		intent := plan.Intent(value)
		if !intent.Valid() {
			return nil, fmt.Errorf("AUTO_SEND_INTENTS contains unknown intent %q", value)
		}
		if !IntentCapable(intent) {
			return nil, fmt.Errorf("AUTO_SEND_INTENTS contains %q, which this policy version never automates: %s",
				value, IntentCapabilityReason(intent))
		}
		if !seen[value] {
			seen[value] = true
			intents = append(intents, value)
		}
	}
	sort.Strings(intents)
	return intents, nil
}

// Input is everything the policy is allowed to consider. Like the send preflight, it is an
// explicit snapshot gathered once: the decision is a pure function of this struct, so two checks
// can never disagree because they read the database a moment apart.
type Input struct {
	Now    time.Time
	Config Config

	Intent plan.Intent
	// SendPathReady is send.Readiness: whether an approved message could be transmitted at all.
	SendPathReady    bool
	SendPathReasons  []string
	ProfileActive    bool
	ProfileRevision  int64
	PlanStatus       string
	PlanRevision     int
	ContentHash      string
	CompositionState string
	// CurrentRevision and AttemptSource identify the revision under consideration and how it was
	// produced. A human-edited revision is never automatable.
	CurrentRevision int
	AttemptSource   string
	StaleReasons    []string

	DeterministicFindings []Finding
	SemanticComplete      bool
	SemanticPassed        bool
	SemanticFindings      []Finding

	Match *opportunity.MatchResult
	// OpportunityCurrentRevision is the opportunity's revision right now, compared against the
	// revision the composition pinned.
	OpportunityPinnedRevision  int
	OpportunityCurrentRevision int
	OpportunityAvailability    string

	Recipient         RecipientState
	AddressSuppressed bool
	DomainSuppressed  bool

	// Send-side facts, read from the same tables the preflight uses.
	ActiveThreadsForDomain int
	AutoSentToday          int
	UnresolvedAttempts     int
	ApprovalExists         bool

	BudgetAvailable bool
	BudgetReason    string
}

// Finding is the shape of a validation or review finding, kept local so this package does not
// depend on the composition package's larger surface.
type Finding struct {
	Severity string
	Code     string
	Text     string
}

// RecipientState is the pinned recipient as it stands now.
type RecipientState struct {
	ContactID int64
	Address   string
	// Function is a relevance classification (relevance.Function*: recruiting, engineering,
	// leadership, commercial, generic or unknown) — never a raw stored role string, which is a
	// different vocabulary entirely.
	Function string
	Status   string
	// SourcePublished marks an application address the employer itself published, which is the
	// one case where a non-personal address is the correct destination.
	SourcePublished bool
	Endpoint        relevance.EndpointAssessment
}

// Decision is the answer, with every reason that produced it.
type Decision struct {
	Outcome       string          `json:"outcome"`
	PolicyVersion string          `json:"policy_version"`
	Intent        string          `json:"intent"`
	EvaluatedAt   time.Time       `json:"evaluated_at"`
	Reasons       explain.Reasons `json:"reasons,omitempty"`
	Blockers      explain.Reasons `json:"blockers,omitempty"`
}

func (d Decision) Eligible() bool { return d.Outcome == EligibleForAuto }

// Evaluate is the automation gate. It returns every blocker rather than the first, because the
// operator needs to know everything standing between a draft and eligibility, not the earliest
// thing the loop happened to notice.
func Evaluate(in Input) Decision {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	d := Decision{Outcome: ReviewRequired, PolicyVersion: PolicyVersion,
		Intent: string(in.Intent), EvaluatedAt: now, Reasons: explain.Reasons{}, Blockers: explain.Reasons{}}

	// --- what the policy itself will never automate -------------------------------------
	if !in.Intent.Valid() {
		d.Blockers = d.Blockers.Add("invalid_intent", "The message has no valid authoritative intent.")
		return d
	}
	if !IntentCapable(in.Intent) {
		d.Blockers = d.Blockers.Add("intent_not_automatable", IntentCapabilityReason(in.Intent))
		// Deliberately keep evaluating: the operator should see everything else that would also
		// have to be true, not just the first refusal.
	}

	// --- configuration: necessary, never sufficient --------------------------------------
	if in.Config.Mode != ModeAuto {
		d.Blockers = d.Blockers.Add("rollout_mode_not_auto",
			"Rollout mode is "+in.Config.Mode+". Selective automation runs only in "+ModeAuto+".")
	}
	if len(in.Config.Allowlist) == 0 {
		d.Blockers = d.Blockers.Add("allowlist_empty",
			"AUTO_SEND_INTENTS is empty, so no intent may be sent automatically.")
	} else if !in.Config.Allows(in.Intent) {
		d.Blockers = d.Blockers.Add("intent_not_allowlisted",
			"The intent "+string(in.Intent)+" is not in AUTO_SEND_INTENTS.")
	}
	if !in.SendPathReady {
		reason := "The send path is not ready."
		if len(in.SendPathReasons) > 0 {
			reason += " " + strings.Join(in.SendPathReasons, "; ")
		}
		d.Blockers = d.Blockers.Add("send_path_not_ready", reason)
	}

	// --- the approved profile ------------------------------------------------------------
	if !in.ProfileActive {
		d.Blockers = d.Blockers.Add("profile_not_active",
			"No approved candidate profile revision is active, so nothing about this message is authoritative.")
	}

	// --- the match -----------------------------------------------------------------------
	evaluateMatch(&d, in)

	// --- the plan and its freezing -------------------------------------------------------
	if in.PlanStatus != "selected" {
		d.Blockers = d.Blockers.Add("plan_not_selected",
			"The contact plan is "+orUnknown(in.PlanStatus)+" rather than selected.")
	}
	if len(in.StaleReasons) > 0 {
		d.Blockers = d.Blockers.Add("stale_inputs",
			"An input changed after this draft was written: "+strings.Join(in.StaleReasons, ", ")+".")
	}
	if in.OpportunityPinnedRevision != in.OpportunityCurrentRevision {
		d.Blockers = d.Blockers.Add("opportunity_revision_moved",
			fmt.Sprintf("The composition pinned opportunity revision %d but the stored opportunity is now at %d.",
				in.OpportunityPinnedRevision, in.OpportunityCurrentRevision))
	}
	if in.OpportunityAvailability == "closed" {
		d.Blockers = d.Blockers.Add("opportunity_closed", "The advertised role is recorded as closed.")
	}

	// --- both review layers --------------------------------------------------------------
	evaluateReview(&d, in)

	// --- how this revision was produced --------------------------------------------------
	// A human-edited revision is permanently outside automation. Automation is justified by
	// measured composer and reviewer behaviour; hand-written text has no such measurement
	// behind it, and sending it unattended would be automating something never observed.
	if in.AttemptSource == "human" {
		d.Blockers = d.Blockers.Add("human_edited_revision",
			"This revision was edited by hand. A hand-edited revision is never sent automatically; "+
				"a later regenerated revision can qualify on its own.")
	}

	// --- the recipient -------------------------------------------------------------------
	evaluateRecipient(&d, in)

	// --- suppression and volume ----------------------------------------------------------
	if in.AddressSuppressed {
		d.Blockers = d.Blockers.Add("address_suppressed", "This address asked not to be contacted again.")
	}
	if in.DomainSuppressed {
		d.Blockers = d.Blockers.Add("domain_suppressed", "This domain asked not to be contacted again.")
	}
	if in.ActiveThreadsForDomain > 0 {
		d.Blockers = d.Blockers.Add("domain_already_in_conversation",
			"A conversation with this company is already open. Automation never opens a second parallel thread, "+
				"however many matching roles the company has advertised.")
	}
	if in.Config.DailyCap <= 0 {
		d.Blockers = d.Blockers.Add("automation_cap_not_configured", "No automation-specific daily cap is configured.")
	} else if in.AutoSentToday >= in.Config.DailyCap {
		d.Blockers = d.Blockers.Add("automation_cap_reached",
			fmt.Sprintf("The automation cap of %d automatic send(s) per day has been reached.", in.Config.DailyCap))
	}

	// --- unresolved delivery state -------------------------------------------------------
	// An attempt still in flight or of unknown outcome means the system does not know what it
	// has already done. Automation must not proceed through that.
	if in.UnresolvedAttempts > 0 {
		d.Blockers = d.Blockers.Add("unresolved_send_attempt",
			fmt.Sprintf("%d send attempt(s) are unresolved (reserved, sending or uncertain). "+
				"Automation halts until a person establishes what happened.", in.UnresolvedAttempts))
	}
	if in.ApprovalExists {
		d.Blockers = d.Blockers.Add("already_approved_manually",
			"This draft already carries a manual approval. Manual sending stays a separate, human-driven act.")
	}

	// --- budget --------------------------------------------------------------------------
	if !in.BudgetAvailable {
		reason := in.BudgetReason
		if reason == "" {
			reason = "The AI request budget is exhausted or paused."
		}
		d.Blockers = d.Blockers.Add("budget_unavailable", reason)
	}

	if len(d.Blockers) == 0 {
		d.Outcome = EligibleForAuto
		d.Reasons = d.Reasons.Add("all_automation_gates_passed",
			"Every automation condition is satisfied for this exact content, recipient and revision. "+
				"The send preflight still runs immediately before transmission.")
	}
	return d
}

func evaluateMatch(d *Decision, in Input) {
	if in.Intent != plan.SpecificJobApplication {
		return
	}
	if in.Match == nil {
		d.Blockers = d.Blockers.Add("no_match_assessment",
			"No stored fit assessment exists for the selected opportunity.")
		return
	}
	if in.Match.Classification != opportunity.ClassificationStrong {
		d.Blockers = d.Blockers.Add("not_a_strong_match",
			"The fit is "+in.Match.Classification+". Only a strong match can be automated; a possible match is "+
				"precisely the case a person has to judge.")
	}
	if in.Match.FamilyConfidence != opportunity.FamilyClear {
		d.Blockers = d.Blockers.Add("family_not_clear",
			"The advertised title does not clearly state an approved desired engineering family.")
	}
	if in.Match.DescriptionStatus != "complete" {
		d.Blockers = d.Blockers.Add("description_not_complete",
			"The stored job description is "+orUnknown(in.Match.DescriptionStatus)+
				". A title alone can never justify an automatic application.")
	}
	// The matcher's own blockers are the authority on unresolved mandatory requirements,
	// eligibility conditions and preference conflicts. Restating them here as automation
	// blockers keeps one source of truth and shows the operator the same wording twice rather
	// than two subtly different rules.
	for _, blocker := range in.Match.Blockers {
		d.Blockers = d.Blockers.Add("match_"+blocker.Code, blocker.Text, blocker.Refs...)
	}
	if len(in.Match.SupportedCapability) < 2 {
		d.Blockers = d.Blockers.Add("insufficient_supported_capabilities",
			"Fewer than two approved capabilities are supported by an actual requirement excerpt.")
	}
	if len(in.Match.SupportedCapability) > 0 {
		d.Reasons = d.Reasons.Add("capabilities_evidenced",
			fmt.Sprintf("%d approved capabilities are supported by actual requirement excerpts.",
				len(in.Match.SupportedCapability)))
	}
}

func evaluateReview(d *Decision, in Input) {
	if in.CompositionState != "reviewed" {
		d.Blockers = d.Blockers.Add("composition_not_reviewed",
			"The composition is "+orUnknown(in.CompositionState)+" rather than reviewed.")
	}
	for _, f := range in.DeterministicFindings {
		if f.Severity == "info" {
			continue
		}
		d.Blockers = d.Blockers.Add("deterministic_"+f.Code, f.Text)
	}
	if !in.SemanticComplete {
		d.Blockers = d.Blockers.Add("semantic_review_incomplete",
			"The semantic review is missing or incomplete. An absent review is never treated as a pass.")
	}
	if !in.SemanticPassed {
		d.Blockers = d.Blockers.Add("semantic_review_not_passed", "The semantic reviewer did not pass this email.")
	}
	// Any finding at all, including a review-severity one, ends automation. A finding is the
	// reviewer saying it is not certain, and uncertainty is what a person is for.
	for _, f := range in.SemanticFindings {
		if f.Severity == "info" {
			continue
		}
		d.Blockers = d.Blockers.Add("semantic_"+f.Code, f.Text)
	}
	if in.ContentHash == "" {
		d.Blockers = d.Blockers.Add("no_content_hash", "The reviewed content has no hash to pin the decision to.")
	}
}

func evaluateRecipient(d *Decision, in Input) {
	r := in.Recipient
	if r.ContactID == 0 || strings.TrimSpace(r.Address) == "" {
		d.Blockers = d.Blockers.Add("no_pinned_recipient", "No recipient is pinned on this composition.")
		return
	}
	if r.Status == "bad" {
		d.Blockers = d.Blockers.Add("recipient_marked_bad", "The pinned contact is marked bad.")
	}
	// An address that is not a mailbox for humans can never be automated, whatever else is true
	// about it. This is the Phase 6 Sentry ingest address, made structurally impossible rather
	// than merely unlikely.
	if !r.Endpoint.Contactable {
		d.Blockers = d.Blockers.Add("recipient_not_a_human_mailbox",
			"The pinned address is not a mailbox intended for human correspondence: "+r.Endpoint.Evidence+".")
	}
	if r.SourcePublished {
		d.Reasons = d.Reasons.Add("source_published_application_address",
			"The employer published this address as the place to apply, which is the stated destination "+
				"rather than an inferred one.")
		return
	}
	switch r.Function {
	case relevance.FunctionRecruiting, relevance.FunctionEngineering:
		d.Reasons = d.Reasons.Add("recipient_function_appropriate",
			"The pinned contact's function is "+r.Function+", which suits a job application.")
	case relevance.FunctionGeneric:
		d.Blockers = d.Blockers.Add("recipient_generic_mailbox",
			"The pinned address is a generic mailbox reaching no identified person or function.")
	case relevance.FunctionUnknown:
		d.Blockers = d.Blockers.Add("recipient_function_unknown",
			"The pinned contact has no identified function. Automation requires a recruiting or engineering "+
				"contact, or an application address the employer published.")
	default:
		d.Blockers = d.Blockers.Add("recipient_function_inappropriate",
			"The pinned contact's function is "+r.Function+", which does not suit a job application.")
	}
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

// LoadConfig reads the automation configuration from the environment. It returns an error
// rather than a usable-looking default when the configuration does not parse: a misconfigured
// allowlist must stop startup, not be quietly ignored.
func LoadConfig(mode, rawAllowlist, rawCap string) (Config, error) {
	cfg := DefaultConfig()
	cfg.Mode = mode
	allowlist, err := ParseAllowlist(rawAllowlist)
	if err != nil {
		return cfg, err
	}
	cfg.Allowlist = allowlist
	if raw := strings.TrimSpace(rawCap); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return cfg, fmt.Errorf("AUTO_SEND_DAILY_CAP must be a non-negative whole number, got %q", raw)
		}
		// A cap far above the observation window this exists for is almost certainly a mistake,
		// and the cost of the mistake is unattended email.
		if n > 5 {
			return cfg, fmt.Errorf("AUTO_SEND_DAILY_CAP is %d; selective automation is for controlled "+
				"observation and refuses a cap above 5. Raise it deliberately in code once behaviour is measured", n)
		}
		cfg.DailyCap = n
	}
	return cfg, nil
}
