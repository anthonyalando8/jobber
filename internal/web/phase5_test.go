package web

import (
	"bytes"
	"html/template"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
)

func compositionPage(t *testing.T, view sendView) string {
	t.Helper()
	tmpl, err := template.ParseFiles("templates/composition.html")
	if err != nil {
		t.Fatal(err)
	}
	email := composition.Email{Subject: "Application for Backend Engineer", Body: "Synthetic body."}
	draft := store.ComposedDraft{ID: 4, Status: "reviewed", Revision: 2,
		Snapshot: composition.Snapshot{Intent: plan.SpecificJobApplication, PlanID: 9,
			Recipient: composition.Recipient{Address: "synthetic@example.test", Name: "Synthetic Person"}},
		Attempts: []composition.Attempt{{Source: "generated", Email: email}}}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]any{"Draft": draft, "Email": email, "Send": view}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// The rule from the Phase 5 brief: the page never offers a send that preflight would refuse.
// The button is absent, not disabled, and the refusal is shown in the operator's own terms.
func TestSendButtonAbsentWhenPreflightWouldFail(t *testing.T) {
	page := compositionPage(t, sendView{
		Mode: send.ModeManualSend, Readiness: send.Readiness{Ready: true, Mode: send.ModeManualSend},
		Decision: send.Decision{Allowed: false, Blockers: explain.Reasons{
			{Code: "address_suppressed", Text: "This address asked not to be contacted again."}}},
	})
	if strings.Contains(page, `action="/compositions/4/send"`) {
		t.Fatal("the page offered a send that preflight would refuse")
	}
	for _, want := range []string{"address_suppressed", "asked not to be contacted"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the refusal reason %q is not shown to the operator", want)
		}
	}
}

func TestSendButtonPresentOnlyWhenPreflightPasses(t *testing.T) {
	page := compositionPage(t, sendView{
		Mode: send.ModeManualSend, Readiness: send.Readiness{Ready: true, Mode: send.ModeManualSend},
		Decision: send.Decision{Allowed: true},
		Approval: &store.SendApproval{ID: 1, CompositionRevision: 2, ApprovedBy: "operator",
			ApprovedAt: time.Now().UTC(), RecipientEmail: "synthetic@example.test",
			Intent: string(plan.SpecificJobApplication)},
	})
	if !strings.Contains(page, `action="/compositions/4/send"`) {
		t.Fatal("an approved, passing draft offered no way to send it")
	}
}

// Approval is a separate act from review, and the page must say so rather than implying that a
// reviewed draft is cleared to go.
func TestUnapprovedDraftOffersApprovalNotSending(t *testing.T) {
	page := compositionPage(t, sendView{
		Mode: send.ModeManualSend, Readiness: send.Readiness{Ready: true, Mode: send.ModeManualSend},
		Hash: "content-hash-value",
		Decision: send.Decision{Allowed: false, Blockers: explain.Reasons{
			{Code: "not_approved", Text: "This draft has no live send approval."}}},
	})
	if strings.Contains(page, `action="/compositions/4/send"`) {
		t.Fatal("an unapproved draft offered a send button")
	}
	if !strings.Contains(page, `action="/compositions/4/approval"`) {
		t.Fatal("a reviewed draft offered no way to approve it")
	}
	// The approval form pins the exact content the operator read.
	if !strings.Contains(page, `name="content_hash" value="content-hash-value"`) {
		t.Fatal("approval is not pinned to the content shown on the page")
	}
	if !strings.Contains(page, "is not approval") {
		t.Fatal("the page does not distinguish review from approval")
	}
}

// An unready send path explains itself instead of silently doing nothing.
func TestUnreadySendPathStatesItsReasons(t *testing.T) {
	page := compositionPage(t, sendView{
		Mode: send.ModeReview,
		Readiness: send.Readiness{Ready: false, Mode: send.ModeReview,
			Reasons: []string{"rollout mode v2_review does not permit sending"}},
		Decision: send.Decision{Allowed: false, Blockers: explain.Reasons{
			{Code: "rollout_mode_forbids_sending", Text: "The rollout mode does not permit sending."}}},
	})
	if !strings.Contains(page, "does not permit sending") {
		t.Fatal("the page does not say why the send path is unavailable")
	}
	if strings.Contains(page, `action="/compositions/4/send"`) {
		t.Fatal("a draft was sendable in review-only mode")
	}
}

// Attempt history is shown, including the uncertain case, which is the one an operator must not
// resolve by pressing send again.
func TestSendHistoryShowsUncertainAttempt(t *testing.T) {
	page := compositionPage(t, sendView{
		Mode: send.ModeManualSend, Readiness: send.Readiness{Ready: true},
		Decision: send.Decision{Allowed: false, Blockers: explain.Reasons{
			{Code: "delivery_uncertain", Text: "A previous attempt may have been delivered."}}},
		Attempts: []*store.SendAttempt{{ID: 11, State: send.StateUncertain, ProviderStage: send.StageCommit,
			Provider: "smtp", ErrorCategory: send.ErrorTimeoutUncertain, ReservedAt: time.Now().UTC()}},
	})
	for _, want := range []string{"uncertain", "delivery_uncertain", "never retried automatically"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the uncertain attempt is not visible as %q", want)
		}
	}
}

func TestPhase5TemplatesParse(t *testing.T) {
	entries, err := os.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, err := template.ParseFiles(filepath.Join("templates", entry.Name())); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
	}
}

func TestThreadTemplateShowsFrozenIntentAndConsequences(t *testing.T) {
	tmpl, err := template.ParseFiles("templates/thread.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]any{
		"Thread": &store.OutboundThread{ID: 3, CompanyID: 1, RootDraftID: 4, State: send.ThreadActive,
			Intent: string(plan.SpecificJobApplication), RecipientEmail: "synthetic@example.test"},
		"Events": []store.ThreadEvent{{Kind: "note", Source: "operator", Detail: "Synthetic note"}},
	}); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	for _, want := range []string{"Frozen intent", string(plan.SpecificJobApplication), "suppresses this address", "Synthetic note"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the conversation page is missing %q", want)
		}
	}
}

// The legacy routes stay permanently closed, and no handler outside dispatch may send.
func TestLegacySendRoutesStayClosed(t *testing.T) {
	if LegacySendEnabled {
		t.Fatal("the legacy send route must stay permanently closed")
	}
	h := &Handler{}
	for name, call := range map[string]func(*httptest.ResponseRecorder){
		"follow-up": func(w *httptest.ResponseRecorder) {
			h.ReviewFollowupSend(w, httptest.NewRequest("POST", "/followups/1/send", nil))
		},
		"resend": func(w *httptest.ResponseRecorder) {
			h.SentResend(w, httptest.NewRequest("POST", "/sent/1/resend", nil))
		},
	} {
		w := httptest.NewRecorder()
		call(w)
		if w.Code != 410 {
			t.Fatalf("the legacy %s route returned %d, want 410 Gone", name, w.Code)
		}
	}
}

// Phase 5 establishes guarded manual sending only.
func TestAutomaticSendingRemainsDisabled(t *testing.T) {
	if automationUnderDefaultConfig().Enabled {
		t.Fatal("Phase 5 must not enable unrestricted automatic outreach")
	}
}

// A handler with no database reports why it cannot send rather than looking ready.
func TestDispatcherWithoutDatabaseIsNotReady(t *testing.T) {
	t.Setenv("EMAIL_PIPELINE_MODE", send.ModeManualSend)
	d := newDispatcher(nil)
	if d.Readiness().Ready {
		t.Fatal("a dispatcher with no database reported ready to send")
	}
}
