package store

import (
	"context"
	"fmt"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"testing"
)

func TestOpportunityBrowseFiltersSearchPaginationAndClassificationSort(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	profileID := activateSyntheticProfile(t, db, syntheticProfileJSON)
	ids := map[string]int64{}
	for i, class := range []string{"strong_match", "possible_match", "unrelated"} {
		job := ingestFixtureJob(t, db, "Navigation Needle Company", "navigation-needle.example", class, "Needle backend "+class, backendJobText)
		ids[class] = job.ID
		family := "ambiguous"
		if class == "strong_match" {
			family = "clear"
		}
		if class == "unrelated" {
			family = "none"
		}
		if _, err := db.Exec(ctx, `INSERT INTO opportunity_match(opportunity_id,opportunity_revision,profile_revision_id,matcher_version,classification,score,family_confidence,result) VALUES($1,$2,$3,$4,$5,$6,$7,'{}')`, job.ID, job.Revision, profileID, opportunity.MatcherVersion, class, i*50, family); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 55; i++ {
		ingestFixtureJob(t, db, "Other Company", "navigation-other.example", fmt.Sprintf("other-%d", i), fmt.Sprintf("Other role %d", i), backendJobText)
	}
	for class, id := range ids {
		f := OpportunityListFilter{Fit: class}
		n, err := db.CountOpportunityList(ctx, f)
		if err != nil || n != 1 {
			t.Fatalf("%s count %d: %v", class, n, err)
		}
		rows, err := db.ListOpportunityPage(ctx, f, 50, 0)
		if err != nil || len(rows) != 1 || rows[0].ID != id {
			t.Fatalf("%s filter leaked rows: %v", class, err)
		}
	}
	first, err := db.ListOpportunityPage(ctx, OpportunityListFilter{}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range first {
		if row.CompanyName == "Navigation Needle Company" {
			t.Fatal("search fixture should be beyond unfiltered page 1")
		}
	}
	for _, q := range []string{"Needle backend", "Navigation Needle Company"} {
		rows, err := db.ListOpportunityPage(ctx, OpportunityListFilter{Query: q}, 50, 0)
		if err != nil || len(rows) != 3 {
			t.Fatalf("whole-corpus search %q failed: %d %v", q, len(rows), err)
		}
	}
	search, err := db.ListOpportunityPage(ctx, OpportunityListFilter{Query: "Other", Fit: "strong_match"}, 50, 0)
	if err != nil || len(search) != 0 {
		t.Fatal("search and fit must intersect")
	}
	unassessed, err := db.CountOpportunityList(ctx, OpportunityListFilter{Assessment: "unassessed"})
	if err != nil || unassessed != 55 {
		t.Fatal("wrong authoritative unassessed set", unassessed, err)
	}
	best, err := db.ListOpportunityPage(ctx, OpportunityListFilter{Sort: "best"}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if best[0].Fit != "strong_match" || best[1].Fit != "possible_match" || best[2].Fit != "unrelated" {
		t.Fatal("score overrode classification")
	}
	company := best[0].CompanyID
	n, err := db.CountOpportunityList(ctx, OpportunityListFilter{CompanyID: company})
	if err != nil || n != 3 {
		t.Fatal("company filter leaked rows", n, err)
	}
	second, err := db.ListOpportunityPage(ctx, OpportunityListFilter{Query: "Other"}, 50, 50)
	if err != nil || len(second) != 5 {
		t.Fatal("filtered pagination incorrect", err)
	}
	_, err = db.Exec(ctx, `UPDATE opportunity SET selection_status='review_required',availability_status='closed',description_status='partial' WHERE id=$1`, ids["strong_match"])
	if err != nil {
		t.Fatal(err)
	}
	n, err = db.CountOpportunityList(ctx, OpportunityListFilter{Fit: "strong_match", Availability: "closed", Description: "partial", Assessment: "review_required"})
	if err != nil || n != 1 {
		t.Fatal("status filter intersection failed", n, err)
	}
	facets, err := db.OpportunityFacets(ctx)
	if err != nil || facets.Strong != 1 || facets.Possible != 1 || facets.Unrelated != 1 || facets.Unassessed != 55 {
		t.Fatal("facet counts drifted", facets, err)
	}
	priority, err := db.ListOpportunityPage(ctx, OpportunityListFilter{Priority: true, Sort: "priority"}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range priority {
		if row.Fit == "unrelated" || row.ID == ids["strong_match"] {
			t.Fatal("priority panel scope drifted")
		}
	}
	// Activate a new profile: current revisions with old assessments are now unassessed.
	activateSyntheticProfile(t, db, syntheticProfileJSON)
	n, err = db.CountOpportunityList(ctx, OpportunityListFilter{Assessment: "unassessed"})
	if err != nil || n != 58 {
		t.Fatal("historical profile assessment treated as current", n, err)
	}
	// Parameterized input never changes query structure.
	n, err = db.CountOpportunityList(ctx, OpportunityListFilter{Query: "' OR 1=1 --"})
	if err != nil || n != 0 {
		t.Fatal("unsafe query handling", err)
	}
}
func TestResearchQueueDrillDownEqualsDashboard(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	waiting, err := db.CreateCompany(ctx, "Waiting", "waiting.example", "manual", "", "verified")
	if err != nil {
		t.Fatal(err)
	}
	future, err := db.CreateCompany(ctx, "Future", "future.example", "manual", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `UPDATE company SET next_research_at=now()+interval '1 day' WHERE id=$1`, future.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateCompany(ctx, "Failed domain", "failed-domain.example", "manual", "", "failed")
	if err != nil {
		t.Fatal(err)
	}
	f := CompanyFilter{Research: "queue"}
	n, err := db.CountCompanies(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.DashboardCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListCompanies(ctx, f, 50, 0)
	if err != nil || n != snapshot.ResearchQueue || n != 1 || len(rows) != 1 || rows[0].ID != waiting.ID {
		t.Fatal("queue link does not represent metric", err)
	}
	noOps, err := db.CountCompanies(ctx, CompanyFilter{HasOpportunities: "no", Source: "manual"})
	if err != nil || noOps != 3 {
		t.Fatal("company filters incorrect", err)
	}
}
