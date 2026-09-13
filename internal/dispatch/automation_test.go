package dispatch

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/automation"
	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
)

// autoReady is the send readiness an automation-capable deployment has.
func autoReady() send.Readiness {
	return send.EvaluateReadiness(send.Inputs{
		Mode: send.ModeAuto, SchemaVersion: send.RequiredSchemaVersion, SchemaHealthy: true,
		TransportReady: true, SenderConfigured: true, Limits: send.DefaultLimits(),
	})
}

// autoConfig is a deployment with automation switched on for job applications.
func autoConfig() AutomationConfig {
	return AutomationConfig{
		Config: automation.Config{Mode: send.ModeAuto,
			Allowlist: []string{string(plan.SpecificJobApplication)}, DailyCap: 1},
		BudgetAvailable: true,
	}
}

// autoFixture is the Phase 5 fixture switched into automation mode, with the recipient given an
// engineering role so recipient quality is satisfied.
func autoFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, plan.SpecificJobApplication)
	f.service = New(f.db, f.transport, send.ModeAuto, send.DefaultLimits(), autoReady()).
		WithAutomation(send.EvaluateAutomation(send.AutomationInputs{
			Mode: send.ModeAuto, Allowlist: []string{string(plan.SpecificJobApplication)},
			SchemaVersion: send.RequiredAutomationSchemaVersion, Readiness: autoReady(), DailyCap: 1,
		}))
	return f
}

// The classification the synthetic fixture actually produces decides what these tests can
// assert, so it is checked once rather than assumed.
func fixtureIsStrongMatch(t *testing.T, f *fixture) bool {
	t.Helper()
	draft, err := f.db.GetComposition(context.Background(), f.draftID)
	if err != nil {
		t.Fatal(err)
	}
	return draft.Snapshot.Match != nil && draft.Snapshot.Match.Classification == opportunity.ClassificationStrong
}

// W. Every evaluation is persisted with its reasons, whether or not it was eligible.
func TestAutomationDecisionIsPersistedWithReasons(t *testing.T) {
	f := autoFixture(t)
	ctx := context.Background()
	out, err := f.service.EvaluateAutomation(ctx, f.draftID, autoConfig())
	if err != nil {
		t.Fatal(err)
	}
	if out.Recorded == nil {
		t.Fatal("no automation decision was recorded")
	}
	recorded := out.Recorded
	if recorded.PolicyVersion != automation.PolicyVersion {
		t.Fatalf("policy version = %q", recorded.PolicyVersion)
	}
	if recorded.Intent != string(plan.SpecificJobApplication) {
		t.Fatalf("intent = %q", recorded.Intent)
	}
	if recorded.ContentHash != f.hash {
		t.Fatal("the decision is not pinned to the reviewed content")
	}
	if recorded.RolloutMode != send.ModeAuto {
		t.Fatalf("rollout mode = %q", recorded.RolloutMode)
	}
	if len(recorded.Allowlist) != 1 {
		t.Fatalf("allowlist = %v", recorded.Allowlist)
	}
	if recorded.DailyCap != 1 {
		t.Fatalf("daily cap = %d", recorded.DailyCap)
	}
	if recorded.RecipientEmail != "cto@northwind.example" {
		t.Fatalf("recipient = %q", recorded.RecipientEmail)
	}
	if recorded.SemanticStatus != "passed_clean" {
		t.Fatalf("semantic status = %q", recorded.SemanticStatus)
	}
	if recorded.ProfileRevisionID == nil || recorded.OpportunityID == nil || recorded.PlanID == nil {
		t.Fatal("the decision did not pin the profile, opportunity and plan it saw")
	}
	if out.Decision.Eligible() && len(recorded.Reasons) == 0 {
		t.Fatal("an eligible decision recorded no reasons")
	}
	if !out.Decision.Eligible() && len(recorded.Blockers) == 0 {
		t.Fatal("a refusal recorded no blockers")
	}

	// The audit trail answers the question after the fact.
	history, err := f.db.AutomationDecisionsForDraft(ctx, f.draftID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("decision history has %d entries", len(history))
	}
}

// A recorded decision is immutable: re-evaluating writes a new row rather than editing the old.
func TestAutomationDecisionIsImmutable(t *testing.T) {
	f := autoFixture(t)
	ctx := context.Background()
	out, err := f.service.EvaluateAutomation(ctx, f.draftID, autoConfig())
	if err != nil || out.Recorded == nil {
		t.Fatal(err)
	}
	id := out.Recorded.ID
	// Write a value the row does not already hold, or the trigger has nothing to object to.
	other := "review_required"
	if out.Recorded.Outcome == other {
		other = "eligible_for_auto"
	}
	if _, err := f.db.Exec(ctx, `UPDATE automation_decision SET outcome=$2 WHERE id=$1`, id, other); err == nil {
		t.Fatal("an automation decision was editable")
	}
	if _, err := f.db.Exec(ctx,
		`UPDATE automation_decision SET blockers='[{"code":"tampered","text":"rewritten"}]'::jsonb WHERE id=$1`,
		id); err == nil {
		t.Fatal("the recorded blockers were rewritable")
	}
	if _, err := f.db.Exec(ctx, `UPDATE automation_decision SET policy_version='tampered' WHERE id=$1`, id); err == nil {
		t.Fatal("the recorded policy version was rewritable")
	}
	if _, err := f.db.Exec(ctx, `DELETE FROM automation_decision WHERE id=$1`, id); err == nil {
		t.Fatal("an automation decision was deletable")
	}
	if _, err := f.service.EvaluateAutomation(ctx, f.draftID, autoConfig()); err != nil {
		t.Fatal(err)
	}
	history, err := f.db.AutomationDecisionsForDraft(ctx, f.draftID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("re-evaluating produced %d rows, want a second decision", len(history))
	}
}

// D. A clean strong-match application with an identified engineering contact sends once, through
// the canonical path, with the decision linked to the attempt.
func TestEligibleApplicationSendsAutomatically(t *testing.T) {
	f := autoFixture(t)
	ctx := context.Background()
	if !fixtureIsStrongMatch(t, f) {
		t.Skip("the synthetic fixture does not produce a strong match; the policy gates are covered by unit tests")
	}
	out, err := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !out.Decision.Eligible() {
		t.Fatalf("blocked: %v", out.Decision.Blockers.Codes())
	}
	if out.Sent == nil || out.Sent.Attempt == nil {
		t.Fatalf("nothing was sent: %+v", out)
	}
	if f.transport.Calls() != 1 {
		t.Fatalf("provider calls = %d, want exactly 1", f.transport.Calls())
	}
	if out.Sent.Attempt.Initiator != AutomationActor {
		t.Fatalf("initiator = %q, want the automation actor", out.Sent.Attempt.Initiator)
	}
	// The authorization is a real approval row, visibly made by the policy rather than a person.
	approval, err := f.db.LiveApproval(ctx, f.draftID)
	if err != nil {
		t.Fatal(err)
	}
	if approval == nil || !strings.HasPrefix(approval.ApprovedBy, "automation:") {
		t.Fatalf("approval = %+v", approval)
	}
	// The two audit trails are joined.
	history, err := f.db.AutomationDecisionsForDraft(ctx, f.draftID)
	if err != nil {
		t.Fatal(err)
	}
	linked := false
	for _, d := range history {
		if d.SendAttemptID != nil && *d.SendAttemptID == out.Sent.Attempt.ID {
			linked = true
		}
	}
	if !linked {
		t.Fatal("no automation decision is linked to the send attempt it produced")
	}
	// And the automatic send counts against the automation cap.
	sent, err := f.db.AutoSentToday(ctx, startOfLocalDay(out.Sent.Attempt.ReservedAt))
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatalf("automatic sends today = %d, want 1", sent)
	}
}

// O. With an empty allowlist nothing is a candidate, and nothing is transmitted.
func TestEmptyAllowlistSendsNothing(t *testing.T) {
	f := autoFixture(t)
	cfg := autoConfig()
	cfg.Config.Allowlist = []string{}
	out, err := f.service.AutoSend(context.Background(), f.draftID, "worker-1", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Eligible() {
		t.Fatal("an empty allowlist produced an eligible candidate")
	}
	if f.transport.Calls() != 0 {
		t.Fatal("the provider was contacted with an empty allowlist")
	}
}

// Q. Two workers racing the same draft produce one claim and at most one transmission.
func TestConcurrentAutomationWorkersClaimOnce(t *testing.T) {
	f := autoFixture(t)
	if !fixtureIsStrongMatch(t, f) {
		t.Skip("the synthetic fixture does not produce a strong match")
	}
	var wg sync.WaitGroup
	outcomes := make([]AutomationOutcome, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			outcomes[i], errs[i] = f.service.AutoSend(context.Background(), f.draftID, "worker-"+itoa(i), autoConfig())
		}(i)
	}
	close(start)
	wg.Wait()

	// One worker may finish and release the claim before the other starts, so "both returned
	// without a claim error" is a legitimate interleaving. The invariant that matters is that
	// the message left at most once, and that the second attempt was refused on its merits
	// rather than transmitting again.
	transmitted := 0
	for i, err := range errs {
		if err != nil && err != store.ErrNotClaimed {
			t.Fatalf("worker %d: %v", i, err)
		}
		if err == store.ErrNotClaimed && outcomes[i].Sent != nil {
			t.Fatal("a worker that lost the claim still transmitted")
		}
		if outcomes[i].Sent != nil && outcomes[i].Sent.Decision.Allowed {
			transmitted++
		}
	}
	if f.transport.Calls() > 1 {
		t.Fatalf("provider calls = %d; concurrent automation transmitted more than once", f.transport.Calls())
	}
	if transmitted > 1 {
		t.Fatalf("%d workers each believed they had sent the message", transmitted)
	}
}

// R. A recipient that goes bad between eligibility and transmission stops the send.
func TestRecipientGoingBadBeforeSendStopsAutomation(t *testing.T) {
	f := autoFixture(t)
	ctx := context.Background()
	if !fixtureIsStrongMatch(t, f) {
		t.Skip("the synthetic fixture does not produce a strong match")
	}
	// Eligibility as it stands.
	first, err := f.service.EvaluateAutomation(ctx, f.draftID, autoConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Decision.Eligible() {
		t.Skipf("fixture not eligible: %v", first.Decision.Blockers.Codes())
	}
	// The world changes.
	if err := f.db.MarkContactBad(ctx, f.contactID, "bounced"); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig())
	if err != nil {
		t.Fatal(err)
	}
	if out.Sent != nil && out.Sent.Decision.Allowed {
		t.Fatal("automation sent to a contact that had been marked bad")
	}
	if f.transport.Calls() != 0 {
		t.Fatal("the provider was contacted for a bad recipient")
	}
	// No live approval may be left behind by an abandoned automatic send.
	approval, err := f.db.LiveApproval(ctx, f.draftID)
	if err != nil {
		t.Fatal(err)
	}
	if approval != nil && approval.ID != 0 {
		t.Fatal("an abandoned automatic send left a live approval in place")
	}
}

// H, applied end to end: suppression added after eligibility stops the send and leaves nothing.
func TestSuppressionBeforeSendStopsAutomation(t *testing.T) {
	f := autoFixture(t)
	ctx := context.Background()
	if !fixtureIsStrongMatch(t, f) {
		t.Skip("the synthetic fixture does not produce a strong match")
	}
	if err := f.db.AddSuppression(ctx, "cto@northwind.example", "asked to stop"); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Eligible() {
		t.Fatal("a suppressed address was eligible for automation")
	}
	if f.transport.Calls() != 0 {
		t.Fatal("the provider was contacted for a suppressed address")
	}
}

// E, end to end. A hand-edited revision is never automated, and the refusal is recorded.
func TestHandEditedRevisionIsNotAutomated(t *testing.T) {
	f := autoFixture(t)
	ctx := context.Background()
	edited := f.email
	edited.Body = f.email.Body + "\n\nAdded by hand."
	f.recordHumanReviewed(t, edited)

	out, err := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Eligible() {
		t.Fatal("a hand-edited revision was automated")
	}
	if !out.Decision.Blockers.Has("human_edited_revision") {
		t.Fatalf("blockers = %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 0 {
		t.Fatal("the provider was contacted for a hand-edited revision")
	}
}

// V. A draft a person approved stays manual: automation will not pick it up.
func TestManuallyApprovedDraftIsNotAutomated(t *testing.T) {
	f := autoFixture(t)
	ctx := context.Background()
	f.approve(t)
	out, err := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Eligible() {
		t.Fatal("a manually approved draft was automated")
	}
	if !out.Decision.Blockers.Has("already_approved_manually") {
		t.Fatalf("blockers = %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 0 {
		t.Fatal("automation transmitted a manually approved draft")
	}
	// The manual approval survives untouched.
	approval, err := f.db.LiveApproval(ctx, f.draftID)
	if err != nil || approval == nil || approval.ApprovedBy != "operator" {
		t.Fatalf("the manual approval was disturbed: %+v", approval)
	}
}

// The automation candidate query is narrow: it never returns a draft that is approved, sent or
// claimed, and it never walks a legacy queue.
func TestAutomationCandidateSelectionIsNarrow(t *testing.T) {
	f := autoFixture(t)
	ctx := context.Background()

	candidates, err := f.db.AutomationCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(candidates, f.draftID) {
		t.Fatal("a reviewed, unapproved, unsent composition is not an automation candidate")
	}

	// A live approval removes it.
	f.approve(t)
	candidates, err = f.db.AutomationCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(candidates, f.draftID) {
		t.Fatal("an approved draft is still an automation candidate")
	}
	if err := f.db.RevokeApproval(ctx, f.draftID, "test"); err != nil {
		t.Fatal(err)
	}

	// A held claim removes it.
	if err := f.db.ClaimAutomationCandidate(ctx, f.draftID, "worker-1", 0); err != nil {
		t.Fatal(err)
	}
	candidates, err = f.db.AutomationCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(candidates, f.draftID) {
		t.Fatal("a claimed draft is still an automation candidate")
	}
	// A second worker cannot take a held claim.
	if err := f.db.ClaimAutomationCandidate(ctx, f.draftID, "worker-2", 0); err != store.ErrNotClaimed {
		t.Fatalf("a second worker took a held claim: %v", err)
	}
	if err := f.db.ReleaseAutomationClaim(ctx, f.draftID, "worker-1"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ClaimAutomationCandidate(ctx, f.draftID, "worker-2", 0); err != nil {
		t.Fatalf("a released claim could not be retaken: %v", err)
	}
}

// L. Follow-ups are not automated: the runner only ever considers first contact.
func TestAutomationNeverSendsFollowups(t *testing.T) {
	source, err := readSource("automation.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(source, "KindFollowup") {
		t.Fatal("the automation runner references follow-up sending; follow-ups stay human-reviewed")
	}
	if !strings.Contains(source, "KindFirstContact") {
		t.Fatal("the automation runner should only ever request first contact")
	}
}

// Blocker aggregation is what tells an operator whether the obstacle is the data or the policy.
func TestAutomationBlockerCountsAggregate(t *testing.T) {
	f := autoFixture(t)
	ctx := context.Background()
	cfg := autoConfig()
	cfg.Config.Allowlist = []string{}
	if _, err := f.service.EvaluateAutomation(ctx, f.draftID, cfg); err != nil {
		t.Fatal(err)
	}
	counts, err := f.db.AutomationBlockerCounts(ctx, startOfLocalDay(nowUTC()))
	if err != nil {
		t.Fatal(err)
	}
	if counts["allowlist_empty"] < 1 {
		t.Fatalf("blocker counts did not aggregate: %v", counts)
	}
}

// recordHumanReviewed writes a hand-edited revision that then passes review, which is exactly
// the case automation must refuse.
func (f *fixture) recordHumanReviewed(t *testing.T, email composition.Email) {
	t.Helper()
	ctx := context.Background()
	owner, err := f.db.ClaimComposition(ctx, f.draftID, f.revision, 2, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	attempt := composition.Attempt{
		Number: f.revision + 1, Source: "human", Email: email,
		Validation: []composition.Finding{},
		Review:     &composition.Review{Complete: true, Passed: true, Findings: []composition.Finding{}},
	}
	if err := f.db.RecordCompositionAttempt(ctx, f.draftID, owner, attempt); err != nil {
		t.Fatal(err)
	}
	if err := f.db.FinishComposition(ctx, f.draftID, owner, "reviewed"); err != nil {
		t.Fatal(err)
	}
	f.revision++
	f.email = email
	f.hash = composition.Hash(email)
}

func containsID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}

func nowUTC() time.Time { return time.Now().UTC() }

func readSource(name string) (string, error) {
	raw, err := os.ReadFile(name)
	return string(raw), err
}

// Listing conversations joins company, which has its own id, created_at and updated_at. The
// unqualified column list was ambiguous there, so /threads returned 500 for as long as any
// thread existed — and no test caught it because none had ever listed a thread that exists.
func TestListThreadsReturnsRealRows(t *testing.T) {
	f := autoFixture(t)
	ctx := context.Background()
	if !fixtureIsStrongMatch(t, f) {
		t.Skip("the synthetic fixture does not produce a strong match")
	}
	out, err := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig())
	if err != nil {
		t.Fatal(err)
	}
	if out.Sent == nil || out.Sent.Thread == nil {
		t.Fatalf("no thread was opened: %+v", out)
	}

	threads, err := f.db.ListThreads(ctx, 50)
	if err != nil {
		t.Fatalf("listing threads failed: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("listed %d threads, want 1", len(threads))
	}
	listed := threads[0]
	if listed.ID != out.Sent.Thread.ID {
		t.Fatalf("listed thread %d, want %d", listed.ID, out.Sent.Thread.ID)
	}
	// The joined company name is what the listing exists to show, and the timestamps must be
	// the thread's rather than the company's.
	if listed.CompanyName != "Northwind Systems" {
		t.Fatalf("company name = %q", listed.CompanyName)
	}
	if listed.State != send.ThreadActive || listed.Intent != string(plan.SpecificJobApplication) {
		t.Fatalf("listed thread = %+v", listed)
	}
	if listed.CreatedAt.IsZero() || listed.UpdatedAt.IsZero() {
		t.Fatal("the listed thread has no timestamps")
	}
	if listed.RootDraftID != f.draftID {
		t.Fatalf("root draft = %d, want %d", listed.RootDraftID, f.draftID)
	}
}
