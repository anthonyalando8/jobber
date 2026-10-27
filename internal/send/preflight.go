package send

import (
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/plan"
)

// Limits are the configurable send controls. Defaults are deliberately conservative: this tool
// exists to send a few relevant emails, not to reach every matching job.
type Limits struct {
	DailyCap                 int
	PerDomainActive          int
	MinIntervalSeconds       int
	FirstContactCooldownDays int
	MaxAttemptsPerKey        int
	MaxFollowups             int
	FollowupCooldownDays     int
}

func DefaultLimits() Limits {
	return Limits{
		DailyCap:                 20,
		PerDomainActive:          1,
		MinIntervalSeconds:       90,
		FirstContactCooldownDays: 30,
		MaxAttemptsPerKey:        3,
		MaxFollowups:             2,
		FollowupCooldownDays:     3,
	}
}

// Approval is the explicit human authorization. Semantic review passing is not approval.
type Approval struct {
	ID                  int64
	CompositionRevision int
	ContentHash         string
	InputHash           string
	RecipientContactID  int64
	RecipientEmail      string
	Intent              plan.Intent
	ApprovedBy          string
	ApprovedAt          time.Time
	Revoked             bool
	// ManualOverride is a person approving an email the AI review did not pass. It answers the
	// AI's editorial objections, and nothing else: every other check below still applies.
	ManualOverride bool
}

// ContactState is the recipient as it exists right now, for comparison against the pinned one.
type ContactState struct {
	Exists    bool
	ID        int64
	CompanyID int64
	Email     string
	Status    string
}

// PriorAttempt is one previous attempt against the same idempotency key.
type PriorAttempt struct {
	ID    int64
	State string
}

// ThreadState is the durable conversation a follow-up would continue.
type ThreadState struct {
	Exists       bool
	ID           int64
	State        string
	Intent       plan.Intent
	ContactID    int64
	FollowupStep int
	LastSentAt   time.Time
}

// Preflight is every fact the gate is allowed to read. Nothing here is fetched lazily during
// evaluation, so the decision is a pure function of a single consistent snapshot of state.
type Preflight struct {
	Now      time.Time
	Mode     string // rollout mode
	Ready    Readiness
	Identity Identity

	// Composition state.
	CompositionExists   bool
	CompositionStatus   string // reviewed | review_required | queued | running | failed
	CurrentRevision     int
	DeterministicPassed bool
	SemanticPassed      bool
	// DeterministicCritical is true when the current text fails a critical deterministic check
	// under the current rules. Even a manual approval cannot send such an email.
	DeterministicCritical bool
	StaleReasons          []string

	// Pinned plan and approval.
	PlanStatus string
	Approval   *Approval

	// Recipient and suppression.
	Contact           ContactState
	CompanyID         int64
	Domain            string
	AddressSuppressed bool
	DomainSuppressed  bool

	// History.
	PriorAttempts []PriorAttempt
	// ActiveThreadsForDomain counts live first-contact conversations at this domain.
	ActiveThreadsForDomain int
	// LastFirstContactToDomain is the most recent accepted first contact at this domain.
	LastFirstContactToDomain time.Time
	SentToday                int
	LastSendAt               time.Time

	// Follow-up context, only consulted when Identity.Kind is a follow-up.
	Thread ThreadState

	Limits Limits
}

// Decision is the gate's answer. Allowed is true only when nothing blocks.
type Decision struct {
	Allowed  bool            `json:"allowed"`
	Key      string          `json:"idempotency_key"`
	Blockers explain.Reasons `json:"blockers,omitempty"`
	Notes    explain.Reasons `json:"notes,omitempty"`
	// ResolvesTo names an existing attempt when a duplicate request should attach to it rather
	// than transmit again.
	ResolvesTo int64 `json:"resolves_to,omitempty"`
}

// Evaluate is the canonical send preflight. Every send, from any caller, passes through it.
// There is no argument that disables a check.
func Evaluate(in Preflight) Decision {
	d := Decision{Blockers: explain.Reasons{}, Notes: explain.Reasons{}}
	if in.Limits.DailyCap == 0 {
		in.Limits = DefaultLimits()
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if err := in.Identity.Validate(); err != nil {
		d.Blockers = d.Blockers.Add("invalid_send_identity", err.Error())
		return d
	}
	d.Key = in.Identity.Key()

	// Rollout gate first: nothing else matters if this build may not send at all.
	if !in.Ready.Ready {
		for _, r := range in.Ready.Reasons {
			d.Blockers = d.Blockers.Add("not_ready", r)
		}
	}
	// Both sending modes reach this point. The difference between them is who authorized the
	// message, which the approval record already says: this gate only asks whether the
	// deployment permits transmission at all.
	if !SendingPermitted(in.Mode) {
		d.Blockers = d.Blockers.Add("rollout_mode_forbids_sending",
			"Rollout mode is "+in.Mode+". Only "+ModeManualSend+" or "+ModeAuto+" permits transmission.")
	}

	// Composition must exist, be finished, and have passed both layers.
	if !in.CompositionExists {
		d.Blockers = d.Blockers.Add("no_composition", "There is no v2 composition for this draft.")
		return d
	}
	manual := in.Approval != nil && !in.Approval.Revoked && in.Approval.ManualOverride
	if manual {
		// A person read the AI findings and approved anyway. That settles the AI review and its
		// warnings; it does not settle a critical factual check, an unfinished run, or anything
		// after this point.
		if in.CompositionStatus != "review_required" && in.CompositionStatus != "reviewed" {
			d.Blockers = d.Blockers.Add("not_reviewed",
				"The composition status is "+in.CompositionStatus+"; it must finish before it can be sent.")
		}
		if in.DeterministicCritical {
			d.Blockers = d.Blockers.Add("deterministic_critical",
				"A critical deterministic check fails on this text. A manual approval cannot override it; edit the email.")
		}
		d.Notes = d.Notes.Add("manual_override",
			"Approved by "+in.Approval.ApprovedBy+" despite AI review feedback. The findings are kept on record.")
	} else {
		if in.CompositionStatus != "reviewed" {
			d.Blockers = d.Blockers.Add("not_reviewed",
				"The composition status is "+in.CompositionStatus+"; only a reviewed draft can be approved or sent.")
		}
		if !in.DeterministicPassed {
			d.Blockers = d.Blockers.Add("deterministic_validation_failed",
				"Deterministic validation reported findings on the approved revision. Semantic review cannot override this.")
		}
		if !in.SemanticPassed {
			d.Blockers = d.Blockers.Add("semantic_review_failed",
				"The complete-email semantic review did not pass.")
		}
	}
	if in.Identity.CompositionRevision != in.CurrentRevision {
		d.Blockers = d.Blockers.Add("revision_superseded",
			"The approved revision is no longer the current one; regenerate or re-approve.")
	}
	for _, stale := range in.StaleReasons {
		d.Blockers = d.Blockers.Add("stale_inputs",
			"A pinned input changed since this draft was composed: "+stale+".")
	}

	// Explicit human approval, bound to the exact content and recipient.
	switch {
	case in.Approval == nil:
		d.Blockers = d.Blockers.Add("not_approved",
			"This draft has not been explicitly approved for sending. Review passing is not approval.")
	case in.Approval.Revoked:
		d.Blockers = d.Blockers.Add("approval_revoked", "The send approval was revoked.")
	default:
		a := in.Approval
		if a.CompositionRevision != in.Identity.CompositionRevision || a.ContentHash != in.Identity.ContentHash {
			d.Blockers = d.Blockers.Add("approval_content_mismatch",
				"The approval was given for different content. Editing or regenerating invalidates approval.")
		}
		if a.RecipientContactID != in.Identity.RecipientContactID || !sameAddress(a.RecipientEmail, in.Identity.RecipientEmail) {
			d.Blockers = d.Blockers.Add("approval_recipient_mismatch",
				"The approval was given for a different recipient.")
		}
		if a.Intent != in.Identity.Intent {
			d.Blockers = d.Blockers.Add("approval_intent_mismatch",
				"The approval was given for a different intent.")
		}
	}

	if in.PlanStatus != "selected" {
		d.Blockers = d.Blockers.Add("plan_not_selected",
			"The contact plan is "+in.PlanStatus+"; a paused or closed plan cannot send.")
	}

	// The recipient must still be exactly the pinned one. It is never re-picked here.
	switch {
	case !in.Contact.Exists:
		d.Blockers = d.Blockers.Add("recipient_missing", "The pinned contact no longer exists.")
	case in.Contact.ID != in.Identity.RecipientContactID:
		d.Blockers = d.Blockers.Add("recipient_changed", "The pinned contact no longer matches.")
	case !sameAddress(in.Contact.Email, in.Identity.RecipientEmail):
		d.Blockers = d.Blockers.Add("recipient_address_changed",
			"The contact's address changed after approval. Re-plan and re-review rather than sending to a different address.")
	case in.Contact.Status == "bad":
		d.Blockers = d.Blockers.Add("recipient_marked_bad", "The pinned contact is marked bad.")
	case in.Contact.CompanyID != in.CompanyID:
		d.Blockers = d.Blockers.Add("recipient_company_mismatch", "The contact belongs to another company.")
	}
	if strings.TrimSpace(in.Identity.RecipientEmail) == "" || !strings.Contains(in.Identity.RecipientEmail, "@") {
		d.Blockers = d.Blockers.Add("recipient_address_invalid", "The pinned address is not a usable email address.")
	}

	// Suppression is re-checked here, so an approval given before a suppression was added is
	// not a way past it.
	if in.AddressSuppressed {
		d.Blockers = d.Blockers.Add("address_suppressed", "This address asked not to be contacted again.")
	}
	if in.DomainSuppressed {
		d.Blockers = d.Blockers.Add("domain_suppressed", "This domain asked not to be contacted again.")
	}

	// Duplicate protection.
	attempts := 0
	for _, prior := range in.PriorAttempts {
		attempts++
		if BlockingState(prior.State) {
			d.ResolvesTo = prior.ID
			switch prior.State {
			case StateAccepted:
				d.Blockers = d.Blockers.Add("already_accepted",
					"This exact message was already accepted by the provider. Sending again would duplicate it.")
			case StateUncertain:
				d.Blockers = d.Blockers.Add("delivery_uncertain",
					"A previous attempt may or may not have been accepted. Resolve it by reconciliation before any further send; retrying blindly is how duplicates are created.")
			case StateRejected:
				d.Blockers = d.Blockers.Add("already_rejected",
					"The provider permanently rejected this message. Correct the recipient or content rather than retrying.")
			default:
				d.Blockers = d.Blockers.Add("send_in_flight",
					"Another send of this exact message is already reserved or in flight.")
			}
		}
	}
	if attempts >= in.Limits.MaxAttemptsPerKey {
		d.Blockers = d.Blockers.Add("attempt_limit_reached",
			"This message reached its attempt limit. Investigate rather than retrying.")
	}

	// Volume and cooldown controls.
	if in.SentToday >= in.Limits.DailyCap {
		d.Blockers = d.Blockers.Add("daily_cap_reached", "The daily send cap has been reached.")
	}
	if !in.LastSendAt.IsZero() && in.Limits.MinIntervalSeconds > 0 {
		if wait := time.Duration(in.Limits.MinIntervalSeconds)*time.Second - now.Sub(in.LastSendAt); wait > 0 {
			d.Blockers = d.Blockers.Add("min_interval_not_elapsed",
				"Sending too quickly; wait before the next message.")
		}
	}

	if in.Identity.Kind == KindFirstContact {
		if in.ActiveThreadsForDomain >= in.Limits.PerDomainActive {
			d.Blockers = d.Blockers.Add("domain_already_in_conversation",
				"There is already an active outbound conversation at this domain. Multiple matching opportunities do not authorize multiple simultaneous first contacts.")
		}
		if !in.LastFirstContactToDomain.IsZero() && in.Limits.FirstContactCooldownDays > 0 {
			cooldown := time.Duration(in.Limits.FirstContactCooldownDays) * 24 * time.Hour
			if now.Sub(in.LastFirstContactToDomain) < cooldown {
				d.Blockers = d.Blockers.Add("domain_cooldown",
					"This domain was contacted recently; the first-contact cooldown has not elapsed.")
			}
		}
	} else {
		evaluateFollowup(in, now, &d)
	}

	d.Allowed = len(d.Blockers) == 0
	return d
}

// evaluateFollowup adds the checks that only apply to a continuation of an existing thread.
func evaluateFollowup(in Preflight, now time.Time, d *Decision) {
	if !in.Thread.Exists {
		d.Blockers = d.Blockers.Add("no_thread",
			"A follow-up must continue a real thread whose first message was accepted.")
		return
	}
	if in.Thread.State != ThreadActive {
		d.Blockers = d.Blockers.Add("thread_not_active",
			"The thread is "+in.Thread.State+"; a replied, closed, bounced or do-not-contact thread receives no follow-up.")
	}
	// Intent continuity: an application thread cannot become a service conversation.
	if in.Thread.Intent != in.Identity.Intent {
		d.Blockers = d.Blockers.Add("followup_intent_mismatch",
			"The thread's approved intent is "+string(in.Thread.Intent)+" and this follow-up claims "+
				string(in.Identity.Intent)+". A new purpose requires a new contact plan, not a follow-up.")
	}
	if in.Thread.ContactID != in.Identity.RecipientContactID {
		d.Blockers = d.Blockers.Add("followup_recipient_mismatch",
			"A follow-up goes to the original recipient.")
	}
	if in.Identity.FollowupStep != in.Thread.FollowupStep+1 {
		d.Blockers = d.Blockers.Add("followup_step_out_of_order",
			"Follow-up steps run in order; the previous step must be sent or explicitly skipped first.")
	}
	if in.Identity.FollowupStep > in.Limits.MaxFollowups {
		d.Blockers = d.Blockers.Add("followup_limit_reached", "The follow-up limit for this thread is reached.")
	}
	if in.Limits.FollowupCooldownDays > 0 && !in.Thread.LastSentAt.IsZero() {
		cooldown := time.Duration(in.Limits.FollowupCooldownDays) * 24 * time.Hour
		if now.Sub(in.Thread.LastSentAt) < cooldown {
			d.Blockers = d.Blockers.Add("followup_cooldown",
				"The follow-up cooldown has not elapsed. Elapsed time alone is never the reason to send.")
		}
	}
}

func sameAddress(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

var _ = reason
