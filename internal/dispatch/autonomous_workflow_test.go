package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/assess"
	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/outreach"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
)

// fakeWriter writes a factual application from exactly the facts it is handed, the way the real
// writer is instructed to; fakeReviewer passes it. Neither touches a network.
type fakeWriter struct{ calls int }

func (w *fakeWriter) Compose(_ context.Context, r composition.WriteRequest) (composition.Email, composition.Metadata, error) {
	w.calls++
	s := r.Snapshot
	claims := []string{}
	for _, c := range s.AllowedClaims {
		claims = append(claims, c.ID)
	}
	body := "I'm applying for the " + s.AdvertisedTitle + " role at " + s.CompanyName + ".\n\n" +
		"I build backend services and the relational schemas behind them, with careful validation and tests. " +
		"The role's focus on APIs and data design matches the work I do every day, and I would bring the same care to your platform.\n\n" +
		"I'd be glad to talk through the details. Could we arrange a short call?"
	return composition.Email{Subject: "Application for " + s.AdvertisedTitle, Body: body, Intent: s.Intent,
			Recipient: s.Recipient.Address, OpportunityID: s.OpportunityID, UsedClaimIDs: claims},
		composition.Metadata{Provider: "fake", Model: "writer"}, nil
}

type fakeReviewer struct{}

func (fakeReviewer) Review(context.Context, composition.Snapshot, composition.Email) (composition.Review, composition.Metadata, error) {
	return composition.Review{Complete: true, Passed: true, Findings: []composition.Finding{}}, composition.Metadata{Provider: "fake", Model: "reviewer"}, nil
}

// The complete autonomous path, against a real database and a fake mail provider: a strong match
// with a confirmed employer and an engineering contact is planned and drafted by the outreach
// worker, decided AUTO_SEND by the policy and sent exactly once. Nothing happens while the
// operator's switches are off, and nothing is drafted twice for the same company.
func TestAutonomousWorkflowEndToEnd(t *testing.T) {
	db, company, contact := autonomousFixture(t)
	ctx := context.Background()
	writer := &fakeWriter{}
	worker := &outreach.Worker{DB: db, Assess: assess.New(db), Composer: writer, Reviewer: fakeReviewer{},
		HourLimit: 100, DayLimit: 100, DailyDrafts: 3}

	// Automatic drafting off (the default): nothing happens.
	if res, err := worker.Tick(ctx); err != nil || !res.Off || writer.calls != 0 {
		t.Fatalf("drafted while switched off: %+v %v", res, err)
	}
	_ = db.SetSetting(ctx, store.SettingAutoCompose, "on", "test")
	_ = db.SetSetting(ctx, store.SettingAutoSend, "on", "test")
	_ = db.SetSetting(ctx, store.SettingAutoSendSince, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), "test")

	res, err := worker.Tick(ctx)
	if err != nil || res.Drafted != 1 {
		t.Fatalf("outreach tick: %+v %v", res, err)
	}
	if writer.calls != 1 {
		t.Fatalf("the draft needed %d writes; the fixture should pass validation first time", writer.calls)
	}
	plans, err := db.ListContactPlansForCompany(ctx, company.ID)
	if err != nil || len(plans) != 1 || plans[0].SelectionOrigin != "suggested" || plans[0].RecipientContactID != contact.ID {
		t.Fatalf("plan: %+v %v", plans, err)
	}
	// The same company is never drafted twice.
	if res, err := worker.Tick(ctx); err != nil || res.Drafted != 0 || writer.calls != 1 {
		t.Fatalf("drafted twice: %+v %v calls=%d", res, err, writer.calls)
	}

	transport := &fakeTransport{}
	service := New(db, transport, send.ModeAuto, send.DefaultLimits(), autoReady()).
		WithAutomation(send.EvaluateAutomation(send.AutomationInputs{Mode: send.ModeAuto,
			Allowlist: []string{string(plan.SpecificJobApplication)}, SchemaVersion: send.RequiredAutomationSchemaVersion,
			Readiness: autoReady(), DailyCap: 1}))
	candidates, err := db.AutomationCandidates(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("automation candidates: %v %v", candidates, err)
	}
	out, err := service.AutoSend(ctx, candidates[0], "worker-1", autoConfig())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Outcome != "eligible_for_auto" || out.Sent == nil || !out.Sent.Decision.Allowed || transport.Calls() != 1 {
		t.Fatalf("not sent: %s %v %+v", out.Decision.Outcome, out.Decision.Blockers.Codes(), out.Sent)
	}
	if transport.last.ToAddress != "ada@fjord.example" || !strings.Contains(transport.last.Body, "Backend Engineer") {
		t.Fatalf("sent to %q: %q", transport.last.ToAddress, transport.last.Body)
	}
	// Running the sending step again sends nothing more.
	if again, _ := db.AutomationCandidates(ctx, time.Now().Add(-time.Hour), 10); len(again) != 0 {
		t.Fatalf("a sent draft is still a candidate: %v", again)
	}
}

// autonomousFixture is a strong-match Backend Engineer posting at a company with a verified domain
// and an engineering contact, assessed and ready for the outreach worker.
func autonomousFixture(t *testing.T) (*store.DB, *store.Company, *store.Contact) {
	t.Helper()
	db := scratchDB(t)
	ctx := context.Background()
	activateProfile(t, db)
	company, err := db.CreateCompany(ctx, "Fjord Analytics", "fjord.example", "fixture", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateCompanyDomainStatus(ctx, company.ID, "verified", 0.95); err != nil {
		t.Fatal(err)
	}
	role := "Engineering Manager"
	name := "Ada Lovelace"
	contact, err := db.CreateContact(ctx, company.ID, "email", "ada@fjord.example", &name, &role, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.IngestOpportunity(ctx, opportunity.DiscoveredOpportunity{CompanyName: "Fjord Analytics", DomainCandidate: "fjord.example",
		Source: "fixture", ExternalID: "fj-1", URL: "https://jobs.example.test/fj-1", Title: "Backend Engineer",
		Description: backendJob, DescriptionStatus: "complete", Availability: "open", RemoteStatus: "unknown"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := assess.New(db).ProcessNew(ctx, 50); err != nil {
		t.Fatal(err)
	}
	return db, company, contact
}

// A restart in the middle of drafting leaves a plan whose draft never completed. The worker picks
// it up again with the same plan, rather than abandoning the posting or making a second plan.
func TestOutreachRecoversFromAnInterruptedDraft(t *testing.T) {
	db, company, _ := autonomousFixture(t)
	ctx := context.Background()
	_ = db.SetSetting(ctx, store.SettingAutoCompose, "on", "test")
	worker := &outreach.Worker{DB: db, Assess: assess.New(db), Composer: &fakeWriter{}, Reviewer: fakeReviewer{},
		HourLimit: 100, DayLimit: 100, DailyDrafts: 3}
	if res, err := worker.Tick(ctx); err != nil || res.Drafted != 1 {
		t.Fatalf("first draft: %+v %v", res, err)
	}
	// Simulate the process dying mid-run: the run is left failed.
	if _, err := db.Exec(ctx, `UPDATE draft_composition SET status='failed' WHERE plan_id IN (SELECT id FROM contact_plan WHERE company_id=$1)`, company.ID); err != nil {
		t.Fatal(err)
	}
	if res, err := worker.Tick(ctx); err != nil || res.Drafted != 1 {
		t.Fatalf("the interrupted draft was not retried: %+v %v", res, err)
	}
	plans, _ := db.ListContactPlansForCompany(ctx, company.ID)
	if len(plans) != 1 {
		t.Fatalf("retry made %d plans, want the same one", len(plans))
	}
	var drafts int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM draft_composition WHERE plan_id=$1`, plans[0].ID).Scan(&drafts)
	if drafts != 2 {
		t.Fatalf("drafts for the plan: %d, want the failed one kept plus the retry", drafts)
	}
}
