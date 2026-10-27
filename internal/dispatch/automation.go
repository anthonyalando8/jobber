package dispatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/automation"
	"github.com/anthonyalando8/prospector/internal/relevance"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
)

// AutomationActor is recorded as the approver when the automation policy authorizes a send. The
// authorization record is the same send_approval row a person would create, pinning the same
// content hash, recipient and intent — only the actor differs, and it differs visibly.
const AutomationActor = "automation:" + automation.PolicyVersion

// AutomationOutcome is the result of considering one draft for automatic sending.
type AutomationOutcome struct {
	Decision automation.Decision
	// Recorded is the persisted decision row, present whether or not the draft was eligible.
	Recorded *store.AutomationDecision
	// Sent is filled only when transmission actually happened.
	Sent *Outcome
	// Withdrawn is set when the draft was eligible, was authorized, and then failed the
	// re-check performed immediately before transmission.
	Withdrawn string
}

// AutomationConfig is the operator configuration the runner evaluates against.
type AutomationConfig struct {
	Config automation.Config
	// BudgetAvailable and BudgetReason come from the AI request budget. Automation respects the
	// same budget as manual work rather than having an allowance of its own.
	BudgetAvailable bool
	BudgetReason    string
}

// EvaluateAutomation decides whether one draft may be sent without a person, and records the
// decision either way. It transmits nothing.
func (s *Service) EvaluateAutomation(ctx context.Context, draftID int64, cfg AutomationConfig) (AutomationOutcome, error) {
	out := AutomationOutcome{}
	input, view, err := s.automationInput(ctx, draftID, cfg)
	if err != nil {
		return out, err
	}
	out.Decision = automation.Evaluate(input)
	recorded, err := s.recordDecision(ctx, out.Decision, view, input)
	if err != nil {
		return out, err
	}
	out.Recorded = recorded
	return out, nil
}

// AutoSend is the automated path. It claims the draft, evaluates the policy, authorizes the send
// under the automation actor, re-evaluates everything immediately before transmission, and only
// then calls the same canonical send path a person uses.
//
// The re-evaluation is not belt-and-braces. Eligibility is computed from a snapshot, and between
// that snapshot and the reservation the recipient can be marked bad, the opportunity can close,
// the profile can be activated or changed, an address can be suppressed, or another send can
// consume the cap. Any of those has to cancel the automatic execution rather than be discovered
// afterwards.
func (s *Service) AutoSend(ctx context.Context, draftID int64, owner string, cfg AutomationConfig) (AutomationOutcome, error) {
	out := AutomationOutcome{}
	if strings.TrimSpace(owner) == "" {
		return out, fmt.Errorf("an automated send must record which worker performed it")
	}
	if err := s.db.ClaimAutomationCandidate(ctx, draftID, owner, 5*time.Minute); err != nil {
		return out, err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.db.ReleaseAutomationClaim(releaseCtx, draftID, owner)
	}()

	out, err := s.EvaluateAutomation(ctx, draftID, cfg)
	if err != nil || !out.Decision.Eligible() {
		return out, err
	}

	// Authorize, pinned to exactly the reviewed bytes. ApproveForSend independently refuses a
	// draft that is not reviewed or whose content hash does not match what is stored, so the
	// automation policy cannot authorize something the composition layer would not.
	view, err := s.load(ctx, Request{DraftID: draftID, Kind: send.KindFirstContact})
	if err != nil {
		return out, err
	}
	approval, err := s.db.ApproveForSend(ctx, draftID, view.composed.Revision, view.hash, AutomationActor)
	if err != nil {
		return out, err
	}

	// Re-evaluate against freshly read state. Anything changed since the decision cancels it.
	recheckInput, _, err := s.automationInput(ctx, draftID, cfg)
	if err != nil {
		return out, err
	}
	recheck := automation.Evaluate(recheckInput)
	if !recheck.Eligible() {
		reason := strings.Join(recheck.Blockers.Codes(), ", ")
		if revokeErr := s.db.RevokeApproval(ctx, draftID, "automation re-check failed: "+reason); revokeErr != nil {
			return out, revokeErr
		}
		out.Decision = recheck
		out.Withdrawn = reason
		if recorded, recordErr := s.recordDecision(ctx, recheck, view, recheckInput); recordErr == nil {
			out.Recorded = recorded
		}
		return out, nil
	}

	// The canonical path. It runs the full send preflight again, reserves before transmitting,
	// and is the only code that reaches a transport.
	sent, err := s.Send(ctx, Request{DraftID: draftID, Initiator: AutomationActor, Kind: send.KindFirstContact})
	if err != nil {
		return out, err
	}
	out.Sent = &sent
	if !sent.Decision.Allowed {
		// The send preflight refused what automation thought was fine. That is the preflight
		// doing its job as the final authority; withdraw the authorization rather than leaving
		// a live approval nobody asked for.
		_ = s.db.RevokeApproval(ctx, draftID, "send preflight refused: "+strings.Join(sent.Decision.Blockers.Codes(), ", "))
		out.Withdrawn = strings.Join(sent.Decision.Blockers.Codes(), ", ")
		return out, nil
	}
	if out.Recorded != nil && sent.Attempt != nil {
		if err := s.db.LinkAutomationDecisionToAttempt(ctx, out.Recorded.ID, sent.Attempt.ID); err != nil {
			return out, err
		}
	}
	_ = approval
	return out, nil
}

// automationInput gathers one consistent snapshot for the policy, reusing the same view the send
// preflight is built from so the two cannot disagree about the state of the world.
func (s *Service) automationInput(ctx context.Context, draftID int64, cfg AutomationConfig) (automation.Input, *view, error) {
	v, err := s.load(ctx, Request{DraftID: draftID, Kind: send.KindFirstContact})
	if err != nil {
		return automation.Input{}, nil, err
	}
	snapshot := v.composed.Snapshot
	in := automation.Input{
		Now: time.Now().UTC(), Config: cfg.Config, Intent: snapshot.Intent,
		SendPathReady: s.readiness.Ready, SendPathReasons: s.readiness.Reasons,
		PlanStatus: v.planRow.Status, PlanRevision: snapshot.PlanRevision,
		ContentHash: v.hash, CompositionState: v.composed.Status,
		// Automation stays as strict as before: a changed prompt or model also counts as stale.
		CurrentRevision: v.composed.Revision, StaleReasons: append(append([]string{}, v.composed.StaleReasons...), v.composed.ProviderChanges...),
		Match: snapshot.Match, OpportunityPinnedRevision: snapshot.OpportunityRevision,
		AddressSuppressed: v.pre.AddressSuppressed, DomainSuppressed: v.pre.DomainSuppressed,
		ActiveThreadsForDomain: v.pre.ActiveThreadsForDomain,
		BudgetAvailable:        cfg.BudgetAvailable, BudgetReason: cfg.BudgetReason,
	}

	if active, err := s.db.ActiveProfile(ctx); err == nil && active != nil {
		in.ProfileActive, in.ProfileRevision = true, active.ID
	}

	// The revision actually under consideration, and how it was produced.
	if v.composed.Revision > 0 && len(v.composed.Attempts) >= v.composed.Revision {
		last := v.composed.Attempts[v.composed.Revision-1]
		in.AttemptSource = last.Source
		for _, f := range last.Validation {
			in.DeterministicFindings = append(in.DeterministicFindings,
				automation.Finding{Severity: f.Severity, Code: f.Code, Text: f.Explanation})
		}
		if last.Review != nil {
			in.SemanticComplete, in.SemanticPassed = last.Review.Complete, last.Review.Passed
			for _, f := range last.Review.Findings {
				in.SemanticFindings = append(in.SemanticFindings,
					automation.Finding{Severity: f.Severity, Code: f.Code, Text: f.Explanation})
			}
		}
	}

	// The opportunity as it stands now, not as the snapshot remembers it.
	if snapshot.OpportunityID != 0 {
		if row, err := s.db.GetOpportunity(ctx, snapshot.OpportunityID); err == nil {
			in.OpportunityCurrentRevision = row.Revision
			in.OpportunityAvailability = row.Facts.Availability
		}
	} else {
		in.OpportunityCurrentRevision = in.OpportunityPinnedRevision
	}

	// The snapshot stores the contact's raw role string, not a classification, so the function
	// is derived here from the live contact exactly as the recipient ranker derives it. Reading
	// the raw role as if it were a classification would have made every contact look
	// inappropriate, which is safe but wrong for the wrong reason.
	in.Recipient = automation.RecipientState{
		ContactID: snapshot.Recipient.ContactID, Address: snapshot.Recipient.Address,
		Function: relevance.FunctionUnknown,
		Endpoint: relevance.ClassifyEndpoint(snapshot.Recipient.Address),
	}
	if v.contact != nil {
		in.Recipient.Status = v.contact.Status
		contact := relevance.Contact{ID: v.contact.ID, Kind: v.contact.Kind, Value: v.contact.Value,
			Quality: v.contact.Quality, Status: v.contact.Status}
		if v.contact.PersonName != nil {
			contact.Person = *v.contact.PersonName
		}
		if v.contact.Role != nil {
			contact.Role = *v.contact.Role
		}
		in.Recipient.Function = relevance.ClassifyContact(contact).Function
	}
	if snapshot.Recipient.Relevance == "source_published" {
		in.Recipient.SourcePublished = true
	}

	if unresolved, err := s.db.UnresolvedAttemptsForDraft(ctx, draftID); err == nil {
		in.UnresolvedAttempts = unresolved
	}
	// A global unresolved attempt halts automation too: the system does not know what it has
	// already done, and the cap it is about to check is computed from those same records.
	if unresolved, err := s.db.UnresolvedSendAttempts(ctx); err == nil && unresolved > in.UnresolvedAttempts {
		in.UnresolvedAttempts = unresolved
	}
	if sent, err := s.db.AutoSentToday(ctx, startOfLocalDay(time.Now())); err == nil {
		in.AutoSentToday = sent
	}
	// An approval made by a person keeps this draft on the manual path. One made by automation
	// is this runner's own authorization and is not an obstacle to it.
	if v.approval != nil && v.approval.ID != 0 && !strings.HasPrefix(v.approval.ApprovedBy, "automation:") {
		in.ApprovalExists = true
	}
	return in, v, nil
}

func (s *Service) recordDecision(ctx context.Context, d automation.Decision, v *view, in automation.Input) (*store.AutomationDecision, error) {
	snapshot := v.composed.Snapshot
	row := store.AutomationDecisionFrom(d, v.composed.ID, v.composed.Revision, v.hash)
	row.RolloutMode = in.Config.Mode
	row.Allowlist = in.Config.Allowlist
	row.DailyCap = in.Config.DailyCap
	row.RecipientEmail = snapshot.Recipient.Address
	if snapshot.Recipient.ContactID != 0 {
		id := snapshot.Recipient.ContactID
		row.RecipientContactID = &id
	}
	if snapshot.ProfileRevisionID != 0 {
		id := snapshot.ProfileRevisionID
		row.ProfileRevisionID = &id
	}
	if snapshot.OpportunityID != 0 {
		id, revision := snapshot.OpportunityID, snapshot.OpportunityRevision
		row.OpportunityID, row.OpportunityRevision = &id, &revision
	}
	if snapshot.PlanID != 0 {
		id, revision := snapshot.PlanID, snapshot.PlanRevision
		row.PlanID, row.PlanRevision = &id, &revision
	}
	row.SemanticStatus = semanticStatus(in)
	row.SemanticHash = v.hash
	if row.ContentHash == "" {
		// The decision must still be recorded, and a refusal about an empty composition is
		// exactly the kind of thing worth keeping. The column requires a value, so say so.
		row.ContentHash = "no-content"
	}
	return s.db.RecordAutomationDecision(ctx, row)
}

func semanticStatus(in automation.Input) string {
	switch {
	case !in.SemanticComplete:
		return "incomplete"
	case !in.SemanticPassed:
		return "failed"
	case len(in.SemanticFindings) > 0:
		return "passed_with_findings"
	}
	return "passed_clean"
}

// startOfLocalDay is where the automation cap resets. Local rather than UTC, because the cap
// exists for a person watching what went out today.
func startOfLocalDay(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}
