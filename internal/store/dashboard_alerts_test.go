package store

import (
	"context"
	"testing"
	"time"
)

// Each opportunity card links to the Opportunities list, which hides suppressed postings by
// default, so the dashboard number must leave them out too, and the four fit buckets still add up.
func TestDashboardOpportunityCountsMatchTheListTheyLinkTo(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	discoveryFixture(t, db, "Live Count Fixture", "livecount.example", "strong_match")
	_, suppressedCompany := makeSuppressibleOpportunity(t, db, suppressionFixture{})
	rows, err := db.ListOpportunityPage(ctx, OpportunityListFilter{CompanyID: suppressedCompany}, 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatal("suppression fixture missing", err)
	}
	if _, err := db.SuppressOpportunity(ctx, rows[0].ID, SuppressNoLongerHiring, BasisOperator, "gone", "operator", nil); err != nil {
		t.Fatal(err)
	}

	c, err := db.DashboardCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := db.CountOpportunityList(ctx, OpportunityListFilter{Suppressed: "hidden"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Opportunities != listed {
		t.Fatalf("dashboard shows %d opportunities, the list it links to shows %d", c.Opportunities, listed)
	}
	if c.Suppressed != 1 {
		t.Fatalf("suppressed count %d, want 1", c.Suppressed)
	}
	if sum := c.Strong + c.Possible + c.Unrelated + c.Unassessed; sum != c.Opportunities {
		t.Fatalf("fit buckets add to %d, total is %d", sum, c.Opportunities)
	}
	strongListed, _ := db.CountOpportunityList(ctx, OpportunityListFilter{Fit: "strong_match", Suppressed: "hidden"})
	if c.Strong != strongListed {
		t.Fatalf("strong card %d, strong list %d", c.Strong, strongListed)
	}
}

// A company blocked for identity is findable, and correcting its domain re-queues the search so
// the dashboard alert clears; a company in any other state is left alone.
func TestIdentityBlockedCompanyIsListedAndClearsWhenRequeued(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	blocked, _ := discoveryFixture(t, db, "Blocked Identity Fixture", "blockedidentity.example", "strong_match")
	other, _ := discoveryFixture(t, db, "Searched Fixture", "searchedfixture.example", "strong_match")
	eligible, err := db.ListCompaniesForContactDiscovery(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{blocked, other} {
		if _, err := db.EnqueueContactDiscovery(ctx, *findEligible(eligible, id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CompleteContactDiscovery(ctx, blocked, ContactDiscoveryOutcome{State: DiscoveryIdentityBlocked, Cooldown: 14 * 24 * time.Hour,
		Summary: map[string]any{"skipped": "The domain is unverified."}}); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteContactDiscovery(ctx, other, ContactDiscoveryOutcome{State: DiscoveryNoResult, Cooldown: time.Hour}); err != nil {
		t.Fatal(err)
	}

	ids, err := db.IdentityBlockedCompanies(ctx, 2)
	if err != nil || len(ids) != 1 || ids[0] != blocked {
		t.Fatalf("blocked companies %v (%v), want [%d]", ids, err, blocked)
	}
	if n, err := db.CountCompanies(ctx, CompanyFilter{ContactSearch: "identity_blocked"}); err != nil || n != 1 {
		t.Fatalf("companies filter found %d (%v), want 1", n, err)
	}

	for _, id := range []int64{blocked, other} {
		if err := db.RequeueBlockedContactDiscovery(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	c, err := db.DashboardCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContactSearchIdentityBlocked != 0 {
		t.Fatalf("alert count still %d after the domain was corrected", c.ContactSearchIdentityBlocked)
	}
	task, _ := db.GetContactDiscoveryTask(ctx, blocked)
	if task.State != DiscoveryPending || task.NextEligibleAt.After(time.Now()) {
		t.Fatalf("blocked company not re-queued: %s at %s", task.State, task.NextEligibleAt)
	}
	if task, _ = db.GetContactDiscoveryTask(ctx, other); task.State != DiscoveryNoResult {
		t.Fatalf("a non-blocked search was disturbed: %s", task.State)
	}
}
