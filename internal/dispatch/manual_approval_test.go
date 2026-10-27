package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
)

// recordUnpassed writes a revision the AI review did not pass and leaves it review_required.
func (f *fixture) recordUnpassed(t *testing.T, email composition.Email, review *composition.Review, failure string) {
	t.Helper()
	ctx := context.Background()
	owner, err := f.db.ClaimComposition(ctx, f.draftID, f.revision, 2, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecordCompositionAttempt(ctx, f.draftID, owner, composition.Attempt{
		Number: f.revision + 1, Source: "human", Email: email, Validation: []composition.Finding{},
		Review: review, Failure: failure,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.FinishComposition(ctx, f.draftID, owner, "review_required"); err != nil {
		t.Fatal(err)
	}
	f.revision++
	f.email = email
	f.hash = composition.Hash(email)
}

// plainEmail names no technology: the dispatch fixture's plan selects none, so any technology
// would be a critical finding of its own.
func plainEmail(e composition.Email) composition.Email {
	e.Body = "I'm applying for the Backend Engineer role at Northwind Systems.\n\nI build backend services and the database schemas behind them.\n\nCould we arrange a short call?"
	return e
}

var aiObjection = &composition.Review{Complete: true, Passed: false, Findings: []composition.Finding{
	{Severity: "review", Code: "weak_relevance", Explanation: "The link to the role could be clearer.", Excerpt: "I build Go services"},
}}

// The person approves an email the AI review did not pass, and it sends through the normal path,
// once, recorded as a manual approval, with the AI verdict and findings left exactly as they were.
func TestManualApprovalSendsDespiteAIFeedback(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	ctx := context.Background()
	f.recordUnpassed(t, plainEmail(f.email), aiObjection, "")

	if _, err := f.db.ApproveForSend(ctx, f.draftID, f.revision, f.hash, "operator"); err == nil {
		t.Fatal("the AI-reviewed approval path must still require a passed review")
	}
	out, err := f.service.Send(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed || !blocked(out.Decision, "not_approved") || f.transport.Calls() != 0 {
		t.Fatalf("an unapproved, unpassed email was sendable: %v", out.Decision.Blockers.Codes())
	}

	if _, err := f.db.ApproveManually(ctx, f.draftID, f.revision, "stale-hash", "operator", ""); err == nil {
		t.Fatal("a manual approval of content the person did not see was accepted")
	}
	approval, err := f.db.ApproveManually(ctx, f.draftID, f.revision, f.hash, "operator", "warnings are stylistic")
	if err != nil {
		t.Fatal(err)
	}
	if approval.Basis != store.ApprovalManualOverride {
		t.Fatalf("basis = %q", approval.Basis)
	}
	live, err := f.db.LiveApproval(ctx, f.draftID)
	if err != nil || live.Basis != store.ApprovalManualOverride || live.OverrideNote != "warnings are stylistic" {
		t.Fatalf("manual approval not recorded as such: %+v %v", live, err)
	}

	if _, err := f.db.Exec(ctx, `UPDATE send_approval SET basis='reviewed' WHERE id=$1`, approval.ID); err == nil {
		t.Fatal("a manual approval could be relabelled as an AI-reviewed one")
	}
	preview, err := f.service.Preview(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Allowed || !preview.Notes.Has("manual_override") {
		t.Fatalf("manual approval should allow sending and say so: blockers %v notes %v", preview.Blockers.Codes(), preview.Notes.Codes())
	}
	// Previewing is not sending: only the explicit send call transmits.
	if f.transport.Calls() != 0 {
		t.Fatal("approval or preview transmitted")
	}
	out, err = f.service.Send(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if !out.Decision.Allowed || f.transport.Calls() != 1 || f.transport.last.Body != f.email.Body {
		t.Fatalf("manual approval did not send the approved bytes once: %v", out.Decision.Blockers.Codes())
	}
	// Duplicate-send protection is unchanged.
	again, err := f.service.Send(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if again.Decision.Allowed || f.transport.Calls() != 1 {
		t.Fatal("a manually approved email was sent twice")
	}

	composed, err := f.db.GetComposition(ctx, f.draftID)
	if err != nil {
		t.Fatal(err)
	}
	last := composed.Attempts[composed.Revision-1]
	if composed.Status != "review_required" || last.Review == nil || last.Review.Passed || len(last.Review.Findings) != 1 {
		t.Fatalf("the AI verdict was rewritten: status %q review %+v", composed.Status, last.Review)
	}
}

// A review that never completed (a provider outage) can also be overridden by the person.
func TestManualApprovalWhenReviewUnavailable(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	ctx := context.Background()
	f.recordUnpassed(t, plainEmail(f.email), nil, "review_unavailable")
	if _, err := f.db.ApproveManually(ctx, f.draftID, f.revision, f.hash, "operator", ""); err != nil {
		t.Fatal(err)
	}
	if out, err := f.service.Send(ctx, f.request()); err != nil || !out.Decision.Allowed || f.transport.Calls() != 1 {
		t.Fatalf("send after manual approval: %v %v", err, out.Decision.Blockers.Codes())
	}
}

// Manual approval answers the AI, not the facts: a false attachment claim cannot be approved,
// and even an approval that somehow exists cannot send it.
func TestManualApprovalCannotOverrideCriticalChecks(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	ctx := context.Background()
	bad := plainEmail(f.email)
	bad.Body += "\n\nMy CV is attached for your review."
	f.recordUnpassed(t, bad, aiObjection, "")

	_, err := f.db.ApproveManually(ctx, f.draftID, f.revision, f.hash, "operator", "")
	var refused *store.ErrNotManuallyApprovable
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "fabricated_attachment") || strings.Contains(refused.Reason, "unapproved_technology") {
		t.Fatalf("a false attachment claim was not refused for that reason: %v", err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO send_approval(draft_id,composition_revision,content_hash,input_hash,recipient_contact_id,recipient_email,intent,approved_by,basis)
		SELECT c.draft_id,c.current_revision,r.content_hash,c.input_hash,$2,$3,c.snapshot->>'intent','operator','manual_override'
		FROM draft_composition c JOIN draft_composition_revision r ON r.draft_id=c.draft_id AND r.revision=c.current_revision WHERE c.draft_id=$1`,
		f.draftID, f.contactID, "cto@northwind.example"); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.Send(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed || !blocked(out.Decision, "deterministic_critical") || f.transport.Calls() != 0 {
		t.Fatalf("a critical finding was sent on a manual approval: %v", out.Decision.Blockers.Codes())
	}
}

// Every other send rule still applies to a manual approval: the rollout mode, and a newer
// revision superseding the approved one.
func TestManualApprovalKeepsEveryOtherSendRule(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	ctx := context.Background()
	f.recordUnpassed(t, plainEmail(f.email), aiObjection, "")
	if _, err := f.db.ApproveManually(ctx, f.draftID, f.revision, f.hash, "operator", ""); err != nil {
		t.Fatal(err)
	}
	reviewOnly := New(f.db, f.transport, send.ModeReview, send.DefaultLimits(), ready())
	if out, err := reviewOnly.Send(ctx, f.request()); err != nil || out.Decision.Allowed {
		t.Fatalf("review-only mode sent a manually approved email: %v", err)
	}
	edited := f.email
	edited.Body += "\n\nThanks for reading."
	f.recordUnpassed(t, edited, aiObjection, "")
	out, err := f.service.Send(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed || f.transport.Calls() != 0 {
		t.Fatalf("an approval for an older revision sent new text: %v", out.Decision.Blockers.Codes())
	}
}

// The normal path is unchanged and recorded as approved after review.
func TestReviewedApprovalIsRecordedAsReviewed(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	live, err := f.db.LiveApproval(context.Background(), f.draftID)
	if err != nil || live.Basis != store.ApprovalReviewed || live.OverrideNote != "" {
		t.Fatalf("approval after a passed review: %+v %v", live, err)
	}
}
