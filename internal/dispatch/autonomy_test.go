package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/automation"
	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
)

// strongAutoFixture is autoFixture with the strong-match precondition checked once.
func strongAutoFixture(t *testing.T) *fixture {
	t.Helper()
	f := autoFixture(t)
	if !fixtureIsStrongMatch(t, f) {
		t.Skip("the synthetic fixture does not produce a strong match")
	}
	return f
}

// The global pause stops automatic sending at once, and lifting it lets the same draft go.
func TestGlobalPauseStopsAutomaticSending(t *testing.T) {
	f := strongAutoFixture(t)
	ctx := context.Background()
	if err := f.db.SetSetting(ctx, store.SettingSendingPaused, "on", "test"); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig())
	if err != nil || out.Sent != nil || f.transport.Calls() != 0 || !out.Decision.Blockers.Has("automatic_sending_paused") {
		t.Fatalf("sent while paused: %+v %v", out.Decision.Blockers.Codes(), err)
	}
	if out.Decision.Outcome != automation.ReviewRequired {
		t.Fatalf("a pause is not a reason to block forever: %s", out.Decision.Outcome)
	}
	_ = f.db.SetSetting(ctx, store.SettingSendingPaused, "off", "test")
	if out, err := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig()); err != nil || out.Sent == nil || f.transport.Calls() != 1 {
		t.Fatalf("did not resume: %v %v", out.Decision.Blockers.Codes(), err)
	}
}

// Turning automatic sending on never sends what was already waiting.
func TestHistoricalDraftsAreNeverSentAutomatically(t *testing.T) {
	f := strongAutoFixture(t)
	ctx := context.Background()
	enableAutoSend(t, f, time.Now().Add(time.Minute)) // switched on after the draft was written
	out, err := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig())
	if err != nil || out.Sent != nil || f.transport.Calls() != 0 || !out.Decision.Blockers.Has("written_before_automation_enabled") {
		t.Fatalf("a historical draft was sent: %v %v", out.Decision.Blockers.Codes(), err)
	}
	// Switched off entirely: nothing goes either.
	_ = f.db.SetSetting(ctx, store.SettingAutoSend, "off", "test")
	if out, _ := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig()); out.Sent != nil || !out.Decision.Blockers.Has("automatic_sending_off") {
		t.Fatalf("sent while switched off: %v", out.Decision.Blockers.Codes())
	}
}

// A provider failure is recorded and the automatic retry waits out its backoff instead of
// hammering the provider.
func TestProviderFailureBacksOffThenSendsOnce(t *testing.T) {
	f := strongAutoFixture(t)
	ctx := context.Background()
	f.transport.results = []send.Result{{Outcome: send.OutcomeTransient, Stage: send.StageDial, ErrorCategory: send.ErrorConnection, Response: "connection refused"}}
	first, err := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig())
	if err != nil || first.Sent == nil || first.Sent.Attempt == nil || first.Sent.Attempt.State != send.StateTransientFailed {
		t.Fatalf("first attempt: %+v %v", first.Sent, err)
	}
	// Immediately after: backing off, nothing transmitted.
	backoff, err := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig())
	if err != nil || backoff.Sent != nil || f.transport.Calls() != 1 || !backoff.Decision.Blockers.Has("retry_backoff") {
		t.Fatalf("retried without backing off: %v %v", backoff.Decision.Blockers.Codes(), err)
	}
	// The send record is immutable, so the window cannot be wound back here; that the backoff
	// expires is covered by the policy's own test, and that a transient failure may be retried by
	// the send path's TestConnectionFailureBeforeTransmissionIsRetryable.
}

// A critical factual finding (here an unsupported technology claim) is BLOCKED, never sent.
func TestCriticalFindingIsBlocked(t *testing.T) {
	f := strongAutoFixture(t)
	ctx := context.Background()
	owner, err := f.db.ClaimComposition(ctx, f.draftID, f.revision, 2, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	bad := f.email
	bad.Body += "\n\nI have also run Kubernetes clusters in production for years."
	if err := f.db.RecordCompositionAttempt(ctx, f.draftID, owner, composition.Attempt{Number: f.revision + 1, Source: "generated", Email: bad,
		Validation: []composition.Finding{{Severity: "block", Code: "unapproved_technology", Explanation: "Kubernetes is not in the approved profile."}},
		Review:     &composition.Review{Complete: true, Passed: true, Findings: []composition.Finding{}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.FinishComposition(ctx, f.draftID, owner, "reviewed"); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.AutoSend(ctx, f.draftID, "worker-1", autoConfig())
	if err != nil || out.Sent != nil || f.transport.Calls() != 0 || out.Decision.Outcome != automation.Blocked {
		t.Fatalf("a critical finding was not blocked: %s %v %v", out.Decision.Outcome, out.Decision.Blockers.Codes(), err)
	}
	history, _ := f.db.AutomationDecisionsForDraft(ctx, f.draftID)
	if len(history) == 0 || history[0].Outcome != automation.Blocked {
		t.Fatalf("the BLOCKED decision was not recorded: %+v", history)
	}
}
