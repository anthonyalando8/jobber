package web

import (
	"bytes"
	"context"
	"html/template"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/go-chi/chi/v5"
)

func manualPage(t *testing.T, status string, blockers []composition.Finding, approval *store.SendApproval) string {
	t.Helper()
	tmpl, err := template.ParseFiles("templates/composition.html")
	if err != nil {
		t.Fatal(err)
	}
	email := composition.Email{Subject: "Application for Backend Engineer", Body: "Synthetic body."}
	draft := store.ComposedDraft{ID: 7, Status: status, Revision: 3,
		Snapshot: composition.Snapshot{Intent: plan.SpecificJobApplication, PlanID: 9, Recipient: composition.Recipient{Address: "jobs@example.test"}},
		Attempts: []composition.Attempt{{Source: "human", Email: email}}}
	view := sendView{Mode: send.ModeManualSend, Readiness: send.Readiness{Ready: true}, Approval: approval, Hash: "h"}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]any{"Draft": draft, "Email": email, "Send": view, "ManualBlockers": blockers}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestManualApprovalPanel(t *testing.T) {
	page := manualPage(t, "review_required", nil, nil)
	for _, want := range []string{"Approve manually", `action="/compositions/7/approval/manual"`, `name="acknowledge" value="yes" required`, "nothing is sent until you press Send"} {
		if !strings.Contains(page, want) {
			t.Fatalf("manual approval panel missing %q", want)
		}
	}
	page = manualPage(t, "review_required", []composition.Finding{{Severity: "block", Code: "fabricated_attachment", Explanation: "No attachment exists."}}, nil)
	if strings.Contains(page, "/approval/manual") || !strings.Contains(page, "fabricated_attachment") || !strings.Contains(page, "cannot override them") {
		t.Fatal("a critical finding must replace the manual approval form with what to fix")
	}
	if page := manualPage(t, "reviewed", nil, nil); strings.Contains(page, "/approval/manual") {
		t.Fatal("a reviewed email uses the normal approval, not the override")
	}
	approved := manualPage(t, "review_required", nil, &store.SendApproval{ID: 1, ApprovedBy: "operator", Basis: store.ApprovalManualOverride, OverrideNote: "stylistic only", CompositionRevision: 3, ApprovedAt: time.Now()})
	if !strings.Contains(approved, "Approved manually, despite AI feedback") || !strings.Contains(approved, "stylistic only") {
		t.Fatal("a manual approval must be labelled as such")
	}
	if ai := manualPage(t, "reviewed", nil, &store.SendApproval{ID: 1, ApprovedBy: "operator", Basis: store.ApprovalReviewed}); strings.Contains(ai, "despite AI feedback") {
		t.Fatal("an AI-passed approval was labelled manual")
	}
}

// The acknowledgement is required before anything is read or written.
func TestManualApprovalRequiresAcknowledgement(t *testing.T) {
	h := &Handler{}
	form := url.Values{"revision": {"3"}, "content_hash": {"h"}}
	r := httptest.NewRequest("POST", "/compositions/7/approval/manual", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "7")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.CompositionApproveManual(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "read the AI feedback") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}
