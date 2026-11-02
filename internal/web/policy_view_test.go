package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
)

// The draft page says what the sending policy decided and why, and never makes an automatic
// approval or send look like a person's.
func TestDraftPageShowsPolicyDecisionAndWhoActed(t *testing.T) {
	tmpl, err := template.ParseFiles("templates/composition.html")
	if err != nil {
		t.Fatal(err)
	}
	email := composition.Email{Subject: "Application for Backend Engineer", Body: "Synthetic body."}
	draft := store.ComposedDraft{ID: 9, Status: "reviewed", Revision: 2,
		Snapshot: composition.Snapshot{Intent: plan.SpecificJobApplication, Recipient: composition.Recipient{Address: "jobs@example.test"}},
		Attempts: []composition.Attempt{{Source: "generated", Email: email}}}
	render := func(data map[string]any) string {
		t.Helper()
		var b bytes.Buffer
		if err := tmpl.Execute(&b, data); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	blocked := render(map[string]any{"Draft": draft, "Email": email, "Send": sendView{Mode: send.ModeAuto},
		"PolicyDecision": &store.AutomationDecision{Outcome: "blocked", PolicyVersion: "auto-v2", EvaluatedAt: time.Now(),
			Blockers: explain.Reasons{{Code: "address_suppressed", Text: "This address asked not to be contacted again."}}}})
	for _, want := range []string{"BLOCKED", "address_suppressed", "must not be sent in this form"} {
		if !strings.Contains(blocked, want) {
			t.Fatalf("blocked decision missing %q", want)
		}
	}
	auto := render(map[string]any{"Draft": draft, "Email": email, "Send": sendView{Mode: send.ModeAuto,
		Approval: &store.SendApproval{ID: 1, ApprovedBy: "automation:auto-v2", Basis: store.ApprovalReviewed, ApprovedAt: time.Now()},
		Attempts: []*store.SendAttempt{{ID: 3, State: "accepted", Initiator: "automation:auto-v2"}}}})
	if !strings.Contains(auto, "Approved automatically by the sending policy") || !strings.Contains(auto, "sent automatically") {
		t.Fatal("an automatic approval or send is not labelled as automatic")
	}
	manual := render(map[string]any{"Draft": draft, "Email": email, "Send": sendView{Mode: send.ModeManualSend,
		Approval: &store.SendApproval{ID: 1, ApprovedBy: "operator", Basis: store.ApprovalReviewed, ApprovedAt: time.Now()},
		Attempts: []*store.SendAttempt{{ID: 4, State: "accepted", Initiator: "operator"}}}})
	if strings.Contains(manual, "Approved automatically") || !strings.Contains(manual, "sent by operator") {
		t.Fatal("a manual approval or send is labelled as automatic")
	}
}
