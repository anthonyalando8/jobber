package store

import (
	"context"
	"errors"
	"testing"

	"github.com/anthonyalando8/prospector/internal/opportunity"
)

// Dismissing an opportunity is a live suppression with reason not_pursuing. These tests cover
// the list behaviour the Active/Dismissed views rely on, restoring, repeat requests, rediscovery
// by the source, and that nothing attached to the opportunity is touched.
func TestDismissedOpportunityLeavesEveryActiveViewAndComesBack(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	keep := ingestFixtureJob(t, db, "Dismissal Fixture", "dismissal.example", "dm-1", "Backend Engineer", backendJobText)
	gone := ingestFixtureJob(t, db, "Dismissal Fixture", "dismissal.example", "dm-2", "Platform Engineer", backendJobText)
	if err := db.SetOpportunityTracking(ctx, gone.ID, "saved", "looked promising"); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetOpportunity(ctx, gone.ID)
	if err != nil {
		t.Fatal(err)
	}
	active := func(f OpportunityListFilter) int {
		t.Helper()
		f.Suppressed = "hidden"
		n, err := db.CountOpportunityList(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if active(OpportunityListFilter{CompanyID: row.CompanyID}) != 2 {
		t.Fatal("fixture: both opportunities should start active")
	}

	if _, err := db.SuppressOpportunity(ctx, gone.ID, SuppressNotPursuing, BasisOperator, "", "operator", nil); err != nil {
		t.Fatal(err)
	}
	// A second click, or a second tab, changes nothing and is reported as already done.
	if _, err := db.SuppressOpportunity(ctx, gone.ID, SuppressNotPursuing, BasisOperator, "", "operator", nil); !errors.Is(err, ErrAlreadySuppressed) {
		t.Fatalf("repeat dismissal: %v", err)
	}

	// Hidden from the default list, from a search for it, from its company's list and from
	// the counts the filters show; present in the Dismissed view.
	if n := active(OpportunityListFilter{CompanyID: row.CompanyID}); n != 1 {
		t.Fatalf("active list still shows the dismissed job: %d", n)
	}
	if n := active(OpportunityListFilter{Query: "Platform Engineer"}); n != 0 {
		t.Fatalf("a search for the dismissed job still finds it: %d", n)
	}
	if n, _ := db.CountOpportunityList(ctx, OpportunityListFilter{CompanyID: row.CompanyID, Suppressed: "only"}); n != 1 {
		t.Fatalf("dismissed view shows %d", n)
	}
	page, err := db.ListOpportunityPage(ctx, OpportunityListFilter{CompanyID: row.CompanyID, Suppressed: "hidden", Sort: "title"}, 1, 0)
	if err != nil || len(page) != 1 || page[0].ID != keep.ID {
		t.Fatalf("paged active list: %+v %v", page, err)
	}
	if listed, err := db.ListActiveOpportunities(ctx, row.CompanyID); err != nil || len(listed) != 1 || listed[0].ID != keep.ID {
		t.Fatalf("company and plan pages would still show it: %d %v", len(listed), err)
	}
	facets, err := db.OpportunityFacets(ctx)
	if err != nil || facets.Suppressed < 1 || facets.Tracked != 0 {
		t.Fatalf("facets: %+v %v", facets, err)
	}

	// Nothing attached to it was deleted.
	if tracking, err := db.GetOpportunityTracking(ctx, gone.ID); err != nil || tracking == nil || tracking.State != "saved" {
		t.Fatalf("tracking lost on dismissal: %+v %v", tracking, err)
	}
	if history, err := db.OpportunityHistory(ctx, gone.ID); err != nil || len(history) == 0 {
		t.Fatalf("history lost on dismissal: %v", err)
	}

	// The source publishes it again, changed: it is the same record, a new revision, and it
	// stays dismissed. Rediscovery never silently brings it back.
	again, err := db.IngestOpportunity(ctx, rediscovered(row.Facts))
	if err != nil || again.ID != gone.ID || again.Revision != 2 {
		t.Fatalf("rediscovery: %+v %v", again, err)
	}
	if n, _ := db.CountOpportunityList(ctx, OpportunityListFilter{CompanyID: row.CompanyID, Suppressed: "any"}); n != 2 {
		t.Fatalf("rediscovery created a duplicate: %d rows", n)
	}
	if n := active(OpportunityListFilter{CompanyID: row.CompanyID}); n != 1 {
		t.Fatal("rediscovery brought a dismissed job back into the active list")
	}

	// Restoring puts it back exactly where it was; restoring twice is harmless.
	if _, err := db.ReinstateOpportunity(ctx, gone.ID, "operator", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReinstateOpportunity(ctx, gone.ID, "operator", ""); !errors.Is(err, ErrNotSuppressed) {
		t.Fatalf("repeat restore: %v", err)
	}
	if n := active(OpportunityListFilter{CompanyID: row.CompanyID}); n != 2 {
		t.Fatalf("restored job not back in the active list: %d", n)
	}
	if tracking, _ := db.GetOpportunityTracking(ctx, gone.ID); tracking == nil || tracking.State != "saved" {
		t.Fatal("tracking lost on restore")
	}
}

// rediscovered is the same listing as the source would publish it again, with a changed
// description so it becomes a new revision.
func rediscovered(facts opportunity.DiscoveredOpportunity) opportunity.DiscoveredOpportunity {
	facts.Description += "\nWe have also added a staging environment."
	return facts
}
