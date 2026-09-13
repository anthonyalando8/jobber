package store

import (
	"context"
	"testing"

	"github.com/anthonyalando8/prospector/internal/plan"
)

// legacyFixture creates one pre-v2 sent message: a draft with no composition row, plus its
// send_log entry. That is exactly the shape Phase 6 has to handle.
func legacyFixture(t *testing.T, db *DB) (companyID, contactID, draftID, sendLogID int64) {
	t.Helper()
	ctx := context.Background()
	company, err := db.CreateCompany(ctx, "Legacy Fixture Ltd", "legacy.example", "fixture", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	contact, err := db.CreateContact(ctx, company.ID, "email", "old@legacy.example", nil, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx,
		`INSERT INTO draft(company_id,contact_id,channel,subject,body,state)
		 VALUES($1,$2,'email','Original legacy subject','Original legacy body.','sent') RETURNING id`,
		company.ID, contact.ID).Scan(&draftID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx,
		`INSERT INTO send_log(draft_id,company_id,to_value,channel) VALUES($1,$2,$3,'email') RETURNING id`,
		draftID, company.ID, contact.Value).Scan(&sendLogID); err != nil {
		t.Fatal(err)
	}
	return company.ID, contact.ID, draftID, sendLogID
}

// A legacy draft is identified by the absence of a composition, not by a label that could drift.
func TestLegacyDraftIdentification(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	_, _, draftID, _ := legacyFixture(t, db)

	isLegacy, err := db.IsLegacyDraft(ctx, draftID)
	if err != nil {
		t.Fatal(err)
	}
	if !isLegacy {
		t.Fatal("a draft with no composition must be identified as legacy")
	}
	missing, err := db.IsLegacyDraft(ctx, 999999)
	if err != nil {
		t.Fatal(err)
	}
	if missing {
		t.Fatal("a draft that does not exist is not a legacy draft")
	}
}

// An unsent legacy draft is listed for inspection, and its text is returned unchanged.
func TestListLegacyUnsentDraftsPreservesText(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	company, err := db.CreateCompany(ctx, "Unsent Legacy Ltd", "unsent.example", "fixture", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	contact, err := db.CreateContact(ctx, company.ID, "email", "unsent@unsent.example", nil, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	body := "Legacy body with {{company}} placeholders left exactly as written."
	if _, err := db.Exec(ctx,
		`INSERT INTO draft(company_id,contact_id,channel,subject,body,state,decision)
		 VALUES($1,$2,'email','Legacy subject',$3,'pending','review_required')`,
		company.ID, contact.ID, body); err != nil {
		t.Fatal(err)
	}
	drafts, err := db.ListLegacyUnsentDrafts(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range drafts {
		if d.CompanyID == company.ID {
			found = true
			if d.Body != body {
				t.Fatalf("the legacy body was altered:\n%q", d.Body)
			}
		}
	}
	if !found {
		t.Fatal("an unsent legacy draft was not listed")
	}
}

// Classification is a human statement: it is recorded with its basis, superseded rather than
// overwritten, and never invented from legacy state.
func TestLegacyThreadClassificationLifecycle(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	_, contactID, _, sendLogID := legacyFixture(t, db)

	threads, err := db.ListLegacyThreads(ctx, true, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) == 0 {
		t.Fatal("the new legacy thread is not listed as unclassified")
	}
	if threads[0].Classification != nil {
		t.Fatal("a fresh legacy thread must start with no classification")
	}
	if threads[0].Classified() {
		t.Fatal("an unclassified thread must not be follow-up ready")
	}
	if threads[0].Body != "Original legacy body." {
		t.Fatalf("the sent body was not returned as written: %q", threads[0].Body)
	}

	// A basis is required: a classification that cannot be audited is not accepted.
	if _, err := db.ClassifyLegacyThread(ctx, LegacyClassification{
		SendLogID: sendLogID, Intent: string(plan.SpecificJobApplication), ClassifiedBy: "operator",
	}); err == nil {
		t.Fatal("a classification without a basis was accepted")
	}
	if _, err := db.ClassifyLegacyThread(ctx, LegacyClassification{
		SendLogID: sendLogID, Intent: "hiring", Basis: "the old segment said so", ClassifiedBy: "operator",
	}); err == nil {
		t.Fatal("a legacy segment value was accepted as an intent")
	}

	first, err := db.ClassifyLegacyThread(ctx, LegacyClassification{
		SendLogID: sendLogID, Intent: string(plan.ServiceOutreach),
		RecipientContactID: &contactID, Basis: "the message offers contract backend work",
		ClassifiedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.RecipientContactID == nil || *first.RecipientContactID != contactID {
		t.Fatal("a determinate classification should keep the pinned recipient")
	}

	// Correcting it supersedes rather than erases: both statements survive.
	second, err := db.ClassifyLegacyThread(ctx, LegacyClassification{
		SendLogID: sendLogID, Intent: string(plan.SpecificJobApplication),
		Basis: "on rereading it names the advertised role", ClassifiedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	history, err := db.LegacyClassificationHistory(ctx, sendLogID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("classification history has %d entries, want both statements", len(history))
	}
	current, err := db.ListLegacyThreads(ctx, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	var live *LegacyClassification
	for _, thread := range current {
		if thread.SendLogID == sendLogID {
			live = thread.Classification
		}
	}
	if live == nil || live.ID != second.ID {
		t.Fatal("the current classification is not the most recent statement")
	}

	// Unknown is a real answer, and it pins nothing.
	unknown, err := db.ClassifyLegacyThread(ctx, LegacyClassification{
		SendLogID: sendLogID, Intent: LegacyIntentUnknown, RecipientContactID: &contactID,
		OpportunityID: &sendLogID, Basis: "the message is too generic to tell", ClassifiedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if unknown.RecipientContactID != nil || unknown.OpportunityID != nil {
		t.Fatal("an unknown classification must not pin a recipient or opportunity")
	}
}

// A recorded classification cannot be edited or deleted; the database enforces it.
func TestLegacyClassificationIsImmutable(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	_, _, _, sendLogID := legacyFixture(t, db)
	recorded, err := db.ClassifyLegacyThread(ctx, LegacyClassification{
		SendLogID: sendLogID, Intent: string(plan.SpeculativeJobEnquiry),
		Basis: "it asks about engineering openings", ClassifiedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE legacy_thread_intent SET intent='service_outreach' WHERE id=$1`, recorded.ID); err == nil {
		t.Fatal("a recorded classification must not be editable")
	}
	if _, err := db.Exec(ctx, `UPDATE legacy_thread_intent SET basis='something else' WHERE id=$1`, recorded.ID); err == nil {
		t.Fatal("the basis for a classification must not be rewritable")
	}
	if _, err := db.Exec(ctx, `DELETE FROM legacy_thread_intent WHERE id=$1`, recorded.ID); err == nil {
		t.Fatal("a classification must not be deletable")
	}
}

// The inventory counts what is there and never reports a deletion.
func TestLegacyInventoryCountsRealRows(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	legacyFixture(t, db)

	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	inventory, err := LegacyInventoryReport(ctx, conn.Conn())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]LegacyCategory{}
	for _, c := range inventory.Categories {
		byName[c.Name] = c
	}
	if byName["legacy_sent_drafts"].Count < 1 {
		t.Fatal("the sent legacy draft was not counted")
	}
	if byName["legacy_threads_unclassified"].Count < 1 {
		t.Fatal("the unclassified thread was not counted")
	}
	if byName["legacy_unsent_drafts"].Disposition != DispositionStaleBlocked {
		t.Fatalf("legacy unsent drafts have disposition %q", byName["legacy_unsent_drafts"].Disposition)
	}
	if len(inventory.Preservation) == 0 {
		t.Fatal("the inventory recorded no preservation digests")
	}
	for name, digest := range inventory.Preservation {
		if digest == "" {
			t.Fatalf("preservation digest %q is empty", name)
		}
	}
	if inventory.SchemaVersion < 24 {
		t.Fatalf("inventory reports schema %d, want at least 24", inventory.SchemaVersion)
	}
}

// The preservation digests are what prove nothing rewrote historical outbound text: they must
// change when the text changes, and stay put when unrelated rows are added.
func TestPreservationDigestsDetectChange(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	_, _, draftID, _ := legacyFixture(t, db)

	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	before, err := LegacyInventoryReport(ctx, conn.Conn())
	if err != nil {
		t.Fatal(err)
	}
	// Adding an unrelated classification must not disturb the sent-content digest.
	if _, err := db.ClassifyLegacyThread(ctx, LegacyClassification{
		SendLogID: findSendLog(t, db, draftID), Intent: LegacyIntentUnknown,
		Basis: "cannot tell", ClassifiedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	after, err := LegacyInventoryReport(ctx, conn.Conn())
	if err != nil {
		t.Fatal(err)
	}
	if before.Preservation["sent_draft_bodies"] != after.Preservation["sent_draft_bodies"] {
		t.Fatal("classifying a thread changed the sent-content digest; it must not touch history")
	}

	// Changing what was sent must be detected.
	if _, err := db.Exec(ctx, `UPDATE draft SET body='rewritten' WHERE id=$1`, draftID); err != nil {
		t.Fatal(err)
	}
	tampered, err := LegacyInventoryReport(ctx, conn.Conn())
	if err != nil {
		t.Fatal(err)
	}
	if tampered.Preservation["sent_draft_bodies"] == after.Preservation["sent_draft_bodies"] {
		t.Fatal("rewriting a sent body did not change the preservation digest")
	}
}

func findSendLog(t *testing.T, db *DB, draftID int64) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(context.Background(), `SELECT id FROM send_log WHERE draft_id=$1`, draftID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// The v2 dashboard counts must not silently read zero because a status string drifted.
func TestPipelineStatsRunAgainstRealSchema(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	legacyFixture(t, db)
	stats, err := db.GetPipelineStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Companies < 1 {
		t.Fatal("the company count is wrong")
	}
	if stats.LegacyThreads < 1 {
		t.Fatal("legacy threads were not counted")
	}
	if stats.LegacyUnclassified < 1 {
		t.Fatal("unclassified legacy threads were not counted")
	}
	if stats.ProfileActive {
		t.Fatal("a scratch database reported an active profile")
	}
}
