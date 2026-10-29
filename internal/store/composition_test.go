package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/plan"
)

func compositionFixture(t *testing.T, db *DB) composition.Snapshot {
	t.Helper()
	ctx := context.Background()
	profileID := activateSyntheticProfile(t, db, syntheticProfileJSON)
	job := ingestFixtureJob(t, db, "Composition Fixture", "composition.example", "compose-job", "Backend Engineer", backendJobText)
	match := assessAndStore(t, db, job.ID, profileID)
	row, err := db.GetOpportunity(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	contact, err := db.CreateContact(ctx, row.CompanyID, "email", "careers@composition.example", nil, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	id, rev := row.ID, row.Revision
	p, err := db.CreateContactPlan(ctx, ContactPlan{
		CompanyID: row.CompanyID, Intent: string(plan.SpecificJobApplication), OpportunityID: &id, OpportunityRevision: &rev,
		MatchClassification: match.Classification, RecipientContactID: contact.ID, RecipientEmail: contact.Value,
		RecipientRelevance: "review_required", RecipientReason: "Synthetic generic mailbox", ProfileRevisionID: profileID,
		SelectionOrigin: "manual", Status: "selected", RequiresReview: true,
		Selection: plan.Selection{SelectorVersion: plan.SelectorVersion, Intent: plan.SpecificJobApplication,
			Capabilities:    []plan.Selected{{ID: "cap-api"}, {ID: "cap-data"}},
			ForbiddenClaims: []plan.Claim{{Code: "recipient_payment_problem", Text: "No payment need evidenced"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.BuildCompositionSnapshot(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestCompositionPersistenceAndAdmission(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	s := compositionFixture(t, db)
	if len(s.Technologies) != 2 || len(s.Capabilities) != 2 || len(s.Projects) != 0 {
		t.Fatalf("selection not frozen: %+v", s.Selection)
	}
	id, err := db.QueueComposition(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueComposition(ctx, s); err == nil {
		t.Fatal("parallel generation admitted")
	}
	var wg sync.WaitGroup
	owners := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner, err := db.ClaimComposition(ctx, id, 0, 6, 6, 6)
			if err == nil {
				owners <- owner
			}
		}()
	}
	wg.Wait()
	close(owners)
	var owner string
	count := 0
	for value := range owners {
		owner = value
		count++
	}
	if count != 1 {
		t.Fatalf("claims=%d", count)
	}
	if err := db.ConsumeCompositionCall(ctx, id, "wrong-owner", "compose"); err == nil {
		t.Fatal("foreign reservation consumed")
	}
	if err := db.ConsumeCompositionCall(ctx, id, owner, "compose"); err != nil {
		t.Fatal(err)
	}
	email := composition.Email{Subject: "Application: Backend Engineer", Body: "Synthetic first revision", Intent: s.Intent, Recipient: s.Recipient.Address, OpportunityID: s.OpportunityID}
	if err := db.RecordCompositionAttempt(ctx, id, owner, composition.Attempt{Source: "generated", Email: email}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE draft SET body='legacy edit' WHERE id=$1`, id); err == nil {
		t.Fatal("legacy edit bypassed review history")
	}
	if _, err := db.Exec(ctx, `UPDATE draft_composition_revision SET attempt='{}' WHERE draft_id=$1`, id); err == nil {
		t.Fatal("history mutable")
	}
	if _, err := db.Exec(ctx, `UPDATE draft_composition SET snapshot='{}' WHERE draft_id=$1`, id); err == nil {
		t.Fatal("snapshot mutable")
	}
	if err := db.FinishComposition(ctx, id, owner, "review_required"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimComposition(ctx, id, 0, 1, 6, 6); err == nil {
		t.Fatal("stale edit revision admitted")
	}
	owner, err = db.ClaimComposition(ctx, id, 1, 1, 6, 6)
	if err != nil {
		t.Fatal(err)
	}
	email.Body = "Synthetic human edit claiming React"
	findings := composition.Validate(s, email)
	if !composition.HasBlock(findings) {
		t.Fatal("unapproved edit was not blocked")
	}
	if err := db.RecordCompositionAttempt(ctx, id, owner, composition.Attempt{Source: "human", Email: email, Validation: findings, Failure: "review_pending"}); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishComposition(ctx, id, owner, "review_required"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetComposition(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || len(got.Attempts) != 2 || got.Attempts[0].Email.Body != "Synthetic first revision" || got.Status != "review_required" {
		t.Fatalf("history: %+v", got)
	}
	var calls, reserved int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM composition_call`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM composition_reservation`).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || reserved != 0 {
		t.Fatalf("calls=%d reservations=%d", calls, reserved)
	}
	// A second email for the same plan is refused while the first is live; discarding it is the
	// deliberate way to compose again.
	var exists *CompositionExistsError
	if _, err := db.QueueComposition(ctx, s); !errors.As(err, &exists) || exists.DraftID != id {
		t.Fatalf("duplicate composition admitted: %v", err)
	}
	if err := db.DiscardComposition(ctx, id, "test"); err != nil {
		t.Fatal(err)
	}
	next, err := db.QueueComposition(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimComposition(ctx, next, 0, 6, 6, 6); err == nil {
		t.Fatal("actual request not counted against quota")
	}
	if err := db.CancelQueuedComposition(ctx, next); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueComposition(ctx, s); err != nil {
		t.Fatalf("failed admission blocked regeneration: %v", err)
	}
	// The extension trigger preserves legacy operations, including deletion.
	legacy, err := db.CreateDraft(ctx, s.CompanyID, &s.Recipient.ContactID, "email", "legacy", "legacy", "[]", "needs_manual")
	if err != nil {
		t.Fatal(err)
	}
	tag, err := db.Exec(ctx, `DELETE FROM draft WHERE id=$1`, legacy.ID)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("legacy delete: %v", err)
	}
}

func TestCompositionStalenessAndPinnedHistory(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	s := compositionFixture(t, db)
	id, err := db.QueueComposition(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	check := func(snapshot composition.Snapshot, want string) {
		t.Helper()
		reasons, err := db.CompositionStaleReasons(ctx, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(reasons, ","), want) {
			t.Fatalf("missing %s: %v", want, reasons)
		}
	}
	obsolete := s
	obsolete.PolicyVersion = "old"
	check(obsolete, "composition_policy_changed")
	if _, err := db.Exec(ctx, `UPDATE contact SET value='changed@composition.example' WHERE id=$1`, s.Recipient.ContactID); err != nil {
		t.Fatal(err)
	}
	check(s, "recipient_changed")
	if _, err := db.BuildCompositionSnapshot(ctx, s.PlanID); err == nil {
		t.Fatal("stale recipient accepted")
	}
	if err := db.SetContactPlanStatus(ctx, s.PlanID, "paused"); err != nil {
		t.Fatal(err)
	}
	check(s, "contact_plan_changed")
	ingestFixtureJob(t, db, "Composition Fixture", "composition.example", "compose-job", "Senior Backend Engineer", backendJobText+"\nUpdated requirements")
	check(s, "opportunity_changed")
	activateSyntheticProfile(t, db, strings.Replace(syntheticProfileJSON, "Designs and builds REST APIs", "Builds documented REST APIs", 1))
	check(s, "profile_changed")
	got, err := db.GetComposition(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Snapshot.Recipient.Address != s.Recipient.Address || got.Snapshot.AdvertisedTitle != "Backend Engineer" || got.Snapshot.InputHash != s.InputHash {
		t.Fatal("history silently rebased")
	}
	if _, err := db.QueueComposition(ctx, s); err == nil {
		t.Fatal("stale regeneration admitted")
	}
}

// A second plan for the same vacancy and intent cannot start a second email either, and a draft
// whose run is still going cannot be discarded out from under it.
func TestOneLiveCompositionPerVacancy(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	first := compositionFixture(t, db)
	id, err := db.QueueComposition(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DiscardComposition(ctx, id, "test"); !errors.Is(err, ErrCompositionNotDiscardable) {
		t.Fatalf("a queued run was discarded: %v", err)
	}
	owner, err := db.ClaimComposition(ctx, id, 0, 6, 6, 6)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecordCompositionAttempt(ctx, id, owner, composition.Attempt{Source: "generated", Email: composition.Email{Subject: "Application: Backend Engineer", Body: "Synthetic"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishComposition(ctx, id, owner, "review_required"); err != nil {
		t.Fatal(err)
	}
	// The first plan is closed and a new one made for the same vacancy, as when the recipient is
	// changed.
	if _, err := db.Exec(ctx, `UPDATE contact_plan SET status='closed' WHERE id=$1`, first.PlanID); err != nil {
		t.Fatal(err)
	}
	second := compositionFixture(t, db)
	if second.PlanID == first.PlanID || second.OpportunityID != first.OpportunityID {
		t.Fatalf("fixture did not make a second plan for the same vacancy: %d/%d %d/%d", first.PlanID, second.PlanID, first.OpportunityID, second.OpportunityID)
	}
	var exists *CompositionExistsError
	if _, err := db.QueueComposition(ctx, second); !errors.As(err, &exists) || exists.DraftID != id {
		t.Fatalf("second email for the same vacancy admitted: %v", err)
	}
	if err := db.DiscardComposition(ctx, id, "test"); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.CountCompositionList(ctx, CompositionListFilter{}); n != 0 {
		t.Fatalf("discarded draft still listed: %d", n)
	}
	if n, _ := db.CountCompositionList(ctx, CompositionListFilter{Status: "discarded"}); n != 1 {
		t.Fatalf("discarded filter found %d", n)
	}
	if _, err := db.QueueComposition(ctx, second); err != nil {
		t.Fatalf("composing after discard refused: %v", err)
	}
}

// A new matcher version that reaches the same assessment does not make a composed application
// stale; until the job is re-assessed under the current matcher it does, and a changed
// conclusion still does.
func TestMatcherVersionAloneDoesNotStaleADraft(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	s := compositionFixture(t, db)
	if reasons, err := db.CompositionStaleReasons(ctx, s); err != nil || len(reasons) != 0 {
		t.Fatalf("fresh snapshot stale: %v %v", reasons, err)
	}
	older := s
	pinned := *s.Match
	pinned.MatcherVersion = "fit-v1"
	older.Match = &pinned
	if reasons, err := db.CompositionStaleReasons(ctx, older); err != nil || len(reasons) != 0 {
		t.Fatalf("a version-only change staled the draft: %v %v", reasons, err)
	}
	changed := *s.Match
	changed.Classification = "possible_match"
	differs := s
	differs.Match = &changed
	if reasons, _ := db.CompositionStaleReasons(ctx, differs); !slices.Contains(reasons, "match_changed") {
		t.Fatalf("a changed assessment must stale the draft: %v", reasons)
	}
	if _, err := db.Exec(ctx, `DELETE FROM opportunity_match WHERE opportunity_id=$1`, s.OpportunityID); err != nil {
		t.Fatal(err)
	}
	if reasons, _ := db.CompositionStaleReasons(ctx, older); !slices.Contains(reasons, "match_changed") {
		t.Fatalf("an unassessed job must read as stale until re-assessed: %v", reasons)
	}
}
