package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/opportunity"
)

func accessFixture(t *testing.T, db *DB, title, location, applyURL string, published time.Time) int64 {
	t.Helper()
	d := opportunity.DiscoveredOpportunity{Source: "weworkremotely", CompanyName: "Fixture " + title, DomainCandidate: "fixture-" + strings.ReplaceAll(opportunity.NormalizeTitle(title), " ", "-") + ".example",
		Title: title, URL: "https://weworkremotely.com/remote-jobs/" + title, Location: location, Description: "Go and PostgreSQL.",
		DescriptionStatus: "complete", RemoteStatus: "remote", PublishedAt: &published, Metadata: map[string]string{}}
	if applyURL != "" {
		d.Metadata[opportunity.MetaApplyURL] = applyURL
	}
	result, err := db.IngestOpportunity(context.Background(), d)
	if err != nil || result.ID == 0 {
		t.Fatalf("ingest %s: %+v %v", title, result, err)
	}
	return result.ID
}

// The SQL ordering and the Go explanation must be the same formula, or the list would be sorted
// by a number the page cannot explain.
func TestPursuitScoreSQLMatchesGo(t *testing.T) {
	db := phase2DB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	accessFixture(t, db, "Worldwide Go contract", "Anywhere in the World", "https://jobs.lever.co/fixture/1", now.Add(-48*time.Hour))
	accessFixture(t, db, "US only backend", "USA Only", "", now.Add(-20*24*time.Hour))
	accessFixture(t, db, "Old silent listing", "", "", now.Add(-90*24*time.Hour))
	if n, err := db.RefreshOpportunityAccess(ctx, 50); err != nil || n != 3 {
		t.Fatalf("classified %d, err %v", n, err)
	}
	if n, err := db.RefreshOpportunityAccess(ctx, 50); err != nil || n != 0 {
		t.Fatalf("a current classification was recomputed: %d %v", n, err)
	}
	rows, err := db.ListOpportunityPage(ctx, OpportunityListFilter{Sort: "pursue"}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Facts.Title != "Worldwide Go contract" || rows[2].Facts.Title != "Old silent listing" && rows[2].Facts.Title != "US only backend" {
		t.Fatalf("default order is not best-to-pursue first: %v", titles(rows))
	}
	for _, r := range rows {
		published := r.DiscoveredAt
		if r.Facts.PublishedAt != nil {
			published = *r.Facts.PublishedAt
		}
		age := int(time.Since(published).Hours() / 24)
		if want := opportunity.PursuitScore(r.Fit, r.Access, age); want != r.Pursuit {
			t.Errorf("%s: SQL %d, Go %d (%+v)", r.Facts.Title, r.Pursuit, want, r.Access)
		}
	}
	accessible, err := db.ListOpportunityPage(ctx, OpportunityListFilter{Access: "accessible"}, 10, 0)
	if err != nil || len(accessible) != 1 || accessible[0].Facts.Title != "Worldwide Go contract" {
		t.Fatalf("accessible filter: %v %v", titles(accessible), err)
	}
	ats, err := db.ListOpportunityPage(ctx, OpportunityListFilter{Route: opportunity.RouteEmployerATS}, 10, 0)
	if err != nil || len(ats) != 1 {
		t.Fatalf("route filter: %v %v", titles(ats), err)
	}
}

func TestOpportunityTrackingLifecycle(t *testing.T) {
	db := phase2DB(t)
	ctx := context.Background()
	id := accessFixture(t, db, "Tracked role", "Worldwide", "", time.Now())
	if got, err := db.GetOpportunityTracking(ctx, id); err != nil || got != nil {
		t.Fatalf("untracked listing reported %+v %v", got, err)
	}
	if err := db.SetOpportunityTracking(ctx, id, "saved", "looks promising"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetOpportunityTracking(ctx, id, "applied", ""); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetOpportunityTracking(ctx, id)
	if err != nil || got.State != "applied" || got.Note != "looks promising" || got.AppliedAt == nil {
		t.Fatalf("tracking after apply: %+v %v", got, err)
	}
	applied := *got.AppliedAt
	if err := db.SetOpportunityTracking(ctx, id, "interviewing", "first call booked"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetOpportunityTracking(ctx, id)
	if !got.AppliedAt.Equal(applied) || got.Note != "first call booked" {
		t.Fatalf("applied date or note lost: %+v", got)
	}
	if err := db.SetOpportunityTracking(ctx, id, "bogus", ""); err == nil {
		t.Fatal("an unknown state was accepted")
	}
	active, err := db.ListOpportunityPage(ctx, OpportunityListFilter{Tracking: "active"}, 10, 0)
	if err != nil || len(active) != 1 || active[0].Tracking != "interviewing" {
		t.Fatalf("tracking filter: %+v %v", active, err)
	}
	if err := db.ClearOpportunityTracking(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetOpportunityTracking(ctx, id); got != nil {
		t.Fatal("tracking not cleared")
	}
}

func titles(rows []OpportunityListRow) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.Facts.Title)
	}
	return out
}
