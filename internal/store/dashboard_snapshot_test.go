package store

import (
	"context"
	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"testing"
)

func TestDashboardSnapshotCountsAndReviewSemantics(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	empty, err := db.DashboardCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Companies != 0 || empty.Opportunities != 0 || empty.ProfileActive {
		t.Fatal("empty database misrepresented")
	}
	events, err := db.DashboardEvents(ctx)
	if err != nil || len(events) != 0 {
		t.Fatal("empty event source must stay empty", err)
	}
	s := compositionFixture(t, db)
	for i, class := range []string{opportunity.ClassificationStrong, opportunity.ClassificationPossible, opportunity.ClassificationUnrelated} {
		job := ingestFixtureJob(t, db, "Dashboard Fixture", "dashboard.example", class, "Backend Engineer", backendJobText)
		family := "ambiguous"
		if class == opportunity.ClassificationStrong {
			family = "clear"
		}
		if class == opportunity.ClassificationUnrelated {
			family = "none"
		}
		_, err := db.Exec(ctx, `INSERT INTO opportunity_match(opportunity_id,opportunity_revision,profile_revision_id,matcher_version,classification,score,family_confidence,result) VALUES($1,$2,$3,$4,$5,$6,$7,'{}')`, job.ID, job.Revision, s.ProfileRevisionID, opportunity.MatcherVersion, class, i*20, family)
		if err != nil {
			t.Fatal(err)
		}
	}
	// An older matcher must not add another match count for the same vacancy.
	_, err = db.Exec(ctx, `INSERT INTO opportunity_match(opportunity_id,opportunity_revision,profile_revision_id,matcher_version,classification,score,family_confidence,result) VALUES($1,$2,$3,'outdated','strong_match',70,'clear','{}')`, s.OpportunityID, s.OpportunityRevision, s.ProfileRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `INSERT INTO draft(company_id,channel,body,state) VALUES($1,'email','historical','pending')`, s.CompanyID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.QueueComposition(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.ClaimComposition(ctx, id, 0, 6, 18, 60)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ConsumeCompositionCall(ctx, id, owner, "compose"); err != nil {
		t.Fatal(err)
	}
	attempt := composition.Attempt{Source: "ai", Failure: "compose_failed", ComposeMetadata: composition.Metadata{Provider: "gemini", Model: "synthetic", ErrorCategory: "http_429"}}
	if err := db.RecordCompositionAttempt(ctx, id, owner, attempt); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishComposition(ctx, id, owner, "review_required"); err != nil {
		t.Fatal(err)
	}
	counts, err := db.DashboardCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Strong+counts.Possible+counts.Unrelated != 4 || counts.Unrelated != 1 || counts.LegacyDrafts != 1 || counts.CompositionReview != 1 || counts.ApprovalPending != 0 || counts.HourUsed != 1 || counts.DayUsed != 1 || counts.ProviderFailures != 1 {
		t.Fatalf("incorrect semantic counts: %+v", counts)
	}
	_, err = db.Exec(ctx, `UPDATE draft_composition SET status='reviewed' WHERE draft_id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	counts, err = db.DashboardCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.CompositionReview != 0 || counts.ApprovalPending != 1 {
		t.Fatal("human approval must be separate from semantic review")
	}
	rows, err := db.DashboardOpportunities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Match == opportunity.ClassificationUnrelated {
			t.Fatal("unrelated opportunity in attention list")
		}
	}
	// A profile change makes all previous match assessments stale for the dashboard.
	activateSyntheticProfile(t, db, syntheticProfileJSON)
	counts, err = db.DashboardCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Strong != 0 || counts.Possible != 0 || counts.Unrelated != 0 || counts.Unassessed != counts.Opportunities {
		t.Fatal("outdated profile assessments counted as current")
	}
}
