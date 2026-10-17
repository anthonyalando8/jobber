package store

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// suppressionFixture creates one opportunity with full control over the three facts the sweep
// reads: the availability the source reported, the closing date it published, and when the
// posting was last seen in a feed. Ingest always writes last_seen_at as now(), so these are
// inserted directly — the point of these tests is what happens weeks later.
type suppressionFixture struct {
	Source       string
	Availability string
	ExpiresAt    *time.Time
	LastSeen     time.Time
	Selection    string
}

func makeSuppressibleOpportunity(t *testing.T, db *DB, f suppressionFixture) (opportunityID, companyID int64) {
	t.Helper()
	ctx := context.Background()
	unique := fmt.Sprintf("%s-%d-%d", t.Name(), suppressionRunID, atomic.AddInt64(&suppressionFixtureSeq, 1))
	company, err := db.CreateCompany(ctx, "Suppression Fixture "+unique, "suppress-"+unique+".example", "fixture", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	companyID = company.ID
	if f.Availability == "" {
		f.Availability = "open"
	}
	if f.Selection == "" {
		f.Selection = "new"
	}
	if f.LastSeen.IsZero() {
		f.LastSeen = time.Now()
	}
	if f.Source == "" {
		f.Source = "fixture-board-" + unique
	}
	// The opportunity and its first revision reference each other through a deferred constraint,
	// so they have to be inserted in one transaction.
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx,
		`INSERT INTO opportunity(company_id,source,source_key,title,normalized_title,remote_status,
		   availability_status,description_status,selection_status,content_hash,current_revision,
		   expires_at,last_seen_at)
		 VALUES($1,$2,$3,'Backend Engineer','backend engineer','remote',$4,'complete',$5,$6,1,$7,$8)
		 RETURNING id`,
		companyID, f.Source, "key-"+unique, f.Availability, f.Selection, "hash-"+unique,
		f.ExpiresAt, f.LastSeen).Scan(&opportunityID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO opportunity_revision(opportunity_id,revision,document,content_hash)
		 VALUES($1,1,$2::jsonb,$3)`,
		opportunityID, `{"title":"Backend Engineer"}`, "hash-"+unique); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Nothing is cleaned up, matching the other store fixtures: opportunity revisions are
	// deliberately immutable, so a test cannot delete one without fighting the schema.
	return opportunityID, companyID
}

// Fixture identities have to be unique across runs as well as within one, because nothing is
// cleaned up: the run stamp separates runs, and the counter separates fixtures created in the
// same instant within a run.
var (
	suppressionRunID      = time.Now().UnixNano()
	suppressionFixtureSeq int64
)

func selectionStatus(t *testing.T, db *DB, opportunityID int64) string {
	t.Helper()
	var status string
	if err := db.QueryRow(context.Background(),
		`SELECT selection_status FROM opportunity WHERE id=$1`, opportunityID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// The whole point of suppressing a posting is that it stops being offered as work. The live flag
// is selection_status='ignored' precisely because the priority filter, the dashboard queue and
// contact-discovery eligibility already read it, so one write has to take effect in all of them.
func TestSuppressingAnOpportunityTakesItOutOfTheWorkingQueues(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	id, companyID := makeSuppressibleOpportunity(t, db, suppressionFixture{})

	visible, err := db.CountOpportunityList(ctx, OpportunityListFilter{CompanyID: companyID, Suppressed: "hidden"})
	if err != nil {
		t.Fatal(err)
	}
	if visible != 1 {
		t.Fatalf("the fixture is not visible before suppression: got %d rows", visible)
	}

	record, err := db.SuppressOpportunity(ctx, id, SuppressNoLongerHiring, BasisOperator,
		"the posting is gone from the careers page", "operator", map[string]any{"checked": "careers page"})
	if err != nil {
		t.Fatal(err)
	}
	if record.Reason != SuppressNoLongerHiring || record.DecidedBy != "operator" {
		t.Errorf("the decision was not recorded as made: %+v", record)
	}
	if got := selectionStatus(t, db, id); got != "ignored" {
		t.Errorf("suppression did not take effect: selection_status is %q", got)
	}

	if visible, err = db.CountOpportunityList(ctx, OpportunityListFilter{CompanyID: companyID, Suppressed: "hidden"}); err != nil {
		t.Fatal(err)
	}
	if visible != 0 {
		t.Errorf("a suppressed opportunity is still in the default list")
	}
	if visible, err = db.CountOpportunityList(ctx, OpportunityListFilter{CompanyID: companyID, Suppressed: "only"}); err != nil {
		t.Fatal(err)
	}
	if visible != 1 {
		t.Errorf("a suppressed opportunity cannot be found by asking for suppressed ones")
	}

	// The row still carries its reason, so a list of suppressed postings says why each was
	// dropped rather than just that it was.
	rows, err := db.ListOpportunityPage(ctx, OpportunityListFilter{CompanyID: companyID, Suppressed: "only"}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Suppressed || rows[0].SuppressionReason != SuppressNoLongerHiring {
		t.Errorf("the browse row does not carry the suppression: %+v", rows)
	}
	if rows[0].SuppressionLabel() != "No longer hiring" {
		t.Errorf("the badge would show a slug: %q", rows[0].SuppressionLabel())
	}
}

// Reinstating has to restore what was there. A posting that was 'selected' before someone marked
// it filled by mistake must come back selected, not reset to 'new' — the selection was a separate
// decision and suppression never asked about it.
func TestReinstatingRestoresTheSelectionTheOpportunityHadBefore(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	id, _ := makeSuppressibleOpportunity(t, db, suppressionFixture{Selection: "selected"})

	if _, err := db.SuppressOpportunity(ctx, id, SuppressFilled, BasisOperator, "", "operator", nil); err != nil {
		t.Fatal(err)
	}
	if got := selectionStatus(t, db, id); got != "ignored" {
		t.Fatalf("selection_status is %q before reinstating", got)
	}
	if _, err := db.ReinstateOpportunity(ctx, id, "operator", "employer reposted it"); err != nil {
		t.Fatal(err)
	}
	if got := selectionStatus(t, db, id); got != "selected" {
		t.Errorf("reinstating lost the previous selection: got %q, want %q", got, "selected")
	}

	// The decision is superseded, not deleted: having been dropped and put back is the useful fact.
	history, err := db.OpportunitySuppressionHistory(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("expected the suppression to be kept, got %d rows", len(history))
	}
	if history[0].Live() {
		t.Error("the suppression is still live after reinstating")
	}
	if history[0].SupersededBy != "operator" || history[0].SupersedeNote != "employer reposted it" {
		t.Errorf("the reinstatement was not recorded: %+v", history[0])
	}
	if current, err := db.CurrentOpportunitySuppression(ctx, id); err != nil || current != nil {
		t.Errorf("a reinstated opportunity still reports a current suppression: %v %v", current, err)
	}
}

// Suppressing twice would overwrite the first decision's reason and author, so it is refused.
// Two tabs, or an operator and the sweep, must not silently lose one of them.
func TestSuppressingAnAlreadySuppressedOpportunityIsRefused(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	id, _ := makeSuppressibleOpportunity(t, db, suppressionFixture{})

	if _, err := db.SuppressOpportunity(ctx, id, SuppressExpired, BasisOperator, "first", "operator", nil); err != nil {
		t.Fatal(err)
	}
	_, err := db.SuppressOpportunity(ctx, id, SuppressFilled, BasisOperator, "second", "someone else", nil)
	if !errors.Is(err, ErrAlreadySuppressed) {
		t.Fatalf("expected ErrAlreadySuppressed, got %v", err)
	}
	history, err := db.OpportunitySuppressionHistory(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Note != "first" {
		t.Errorf("the first decision was not preserved: %+v", history)
	}

	if _, err := db.ReinstateOpportunity(ctx, id, "operator", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReinstateOpportunity(ctx, id, "operator", ""); !errors.Is(err, ErrNotSuppressed) {
		t.Errorf("expected ErrNotSuppressed reinstating twice, got %v", err)
	}
}

// A reason and a basis that do not exist must not reach the database, where a CHECK constraint
// would report them as an opaque failure.
func TestSuppressionVocabularyIsChecked(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	id, _ := makeSuppressibleOpportunity(t, db, suppressionFixture{})

	if _, err := db.SuppressOpportunity(ctx, id, "because_i_said_so", BasisOperator, "", "operator", nil); err == nil {
		t.Error("an unknown reason was accepted")
	}
	if _, err := db.SuppressOpportunity(ctx, id, SuppressExpired, "a_hunch", "", "operator", nil); err == nil {
		t.Error("an unknown basis was accepted")
	}
	if _, err := db.SuppressOpportunity(ctx, id, SuppressExpired, BasisOperator, "", "", nil); err == nil {
		t.Error("a suppression with no author was accepted")
	}
	if got := selectionStatus(t, db, id); got != "new" {
		t.Errorf("a rejected suppression still changed the opportunity: %q", got)
	}
}
