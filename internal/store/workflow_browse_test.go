package store

import (
	"context"
	"github.com/anthonyalando8/prospector/internal/composition"
	"testing"
)

func TestCompositionAttentionDrillDownEqualsDashboard(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	s := compositionFixture(t, db)
	ids := map[string]int64{}
	for _, state := range []string{"review_required", "reviewed", "failed"} {
		id, err := db.QueueComposition(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		owner, err := db.ClaimComposition(ctx, id, 0, 6, 18, 60)
		if err != nil {
			t.Fatal(err)
		}
		a := composition.Attempt{Source: "ai", Email: composition.Email{Subject: "Synthetic attention fixture", Body: "Synthetic fixture; no provider invocation"}}
		if err := db.RecordCompositionAttempt(ctx, id, owner, a); err != nil {
			t.Fatal(err)
		}
		if err := db.FinishComposition(ctx, id, owner, state); err != nil {
			t.Fatal(err)
		}
		ids[state] = id
	}
	_, err := db.Exec(ctx, `INSERT INTO draft(company_id,channel,body,state) VALUES($1,'email','historical','pending')`, s.CompanyID)
	if err != nil {
		t.Fatal(err)
	}
	n, err := db.CountCompositionList(ctx, CompositionListFilter{Attention: "required"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.DashboardCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || n != snapshot.CompositionReview+snapshot.ApprovalPending {
		t.Fatal("attention destination differs from metric")
	}
	for attention, want := range map[string]int64{"semantic_review": ids["review_required"], "approval_pending": ids["reviewed"]} {
		rows, err := db.ListCompositionPage(ctx, CompositionListFilter{Attention: attention}, 50, 0)
		if err != nil || len(rows) != 1 || rows[0].ID != want {
			t.Fatal("review concepts mixed", attention, err)
		}
	}
	// A live approval on the exact current composition revision removes human-approval attention.
	_, err = db.Exec(ctx, `INSERT INTO send_approval(draft_id,composition_revision,content_hash,input_hash,recipient_contact_id,recipient_email,intent,approved_by) SELECT $1,1,r.content_hash,$2,$3,$4,$5,'synthetic-test' FROM draft_composition_revision r WHERE r.draft_id=$1 AND r.revision=1`, ids["reviewed"], s.InputHash, s.Recipient.ContactID, s.Recipient.Address, string(s.Intent))
	if err != nil {
		t.Fatal(err)
	}
	n, err = db.CountCompositionList(ctx, CompositionListFilter{Attention: "approval_pending"})
	if err != nil || n != 0 {
		t.Fatal("approved current draft still awaiting approval", err)
	}
	_, err = db.Exec(ctx, `UPDATE draft SET state='sent' WHERE id=$1`, ids["review_required"])
	if err != nil {
		t.Fatal(err)
	}
	n, err = db.CountCompositionList(ctx, CompositionListFilter{Attention: "required"})
	if err != nil || n != 0 {
		t.Fatal("sent draft wrongly requires attention", err)
	}
}

func TestReplyDrillDownUsesRecordedEventsNotOnlyCurrentState(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	s := compositionFixture(t, db)
	draftID, err := db.QueueComposition(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CancelQueuedComposition(ctx, draftID); err != nil {
		t.Fatal(err)
	}
	var threadID int64
	err = db.QueryRow(ctx, `INSERT INTO outbound_thread(company_id,plan_id,plan_revision,intent,recipient_contact_id,recipient_email,root_draft_id,state) VALUES($1,$2,$3,$4,$5,$6,$7,'closed') RETURNING id`, s.CompanyID, s.PlanID, s.PlanRevision, string(s.Intent), s.Recipient.ContactID, s.Recipient.Address, draftID).Scan(&threadID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = db.Exec(ctx, `INSERT INTO thread_event(thread_id,kind,source) VALUES($1,'replied','operator')`, threadID); err != nil {
			t.Fatal(err)
		}
	}
	n, err := db.CountThreadList(ctx, ThreadListFilter{Event: "replied"})
	if err != nil || n != 1 {
		t.Fatal("reply rows duplicated or closed conversation hidden", err)
	}
	rows, err := db.ListThreadPage(ctx, ThreadListFilter{Event: "replied"}, 50, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != threadID || rows[0].State != "closed" {
		t.Fatal("reply destination changed event semantics", err)
	}
	snapshot, err := db.DashboardCounts(ctx)
	if err != nil || snapshot.Replies != n {
		t.Fatal("reply metric and destination differ", err)
	}
}
