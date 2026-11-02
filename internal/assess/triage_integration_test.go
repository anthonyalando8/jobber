package assess

import (
	"context"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/anthonyalando8/prospector/internal/triage"
)

// End to end over a real store: ingestion, the deterministic matcher, then triage. Suppression is
// reversible, a person's restore is respected, nothing is re-processed without a reason, and
// relevant or contradictory postings are never hidden.
func TestTriageWorkerOverRealAssessments(t *testing.T) {
	db := scratchDB(t)
	ctx := context.Background()
	service := New(db)
	activateProfile(t, db)
	filler := strings.Repeat("You will coordinate with regional teams and report weekly to management. ", 5)
	accountant := ingest(t, db, "Ledger Partners", "tr-1", "Senior Accountant", "Prepare monthly statements and reconciliations. "+filler)
	salesEngineer := ingest(t, db, "Ledger Partners", "tr-2", "Sales Engineer", backendJobText+filler)
	backend := ingest(t, db, "Ledger Partners", "tr-3", "Backend Engineer", backendJobText)
	if _, _, err := service.ProcessNew(ctx, 50); err != nil {
		t.Fatal(err)
	}
	active := func() map[int64]bool {
		rows, err := db.ListOpportunityPage(ctx, store.OpportunityListFilter{Suppressed: "hidden"}, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		out := map[int64]bool{}
		for _, r := range rows {
			out[r.ID] = true
		}
		return out
	}

	// Suppression off (the default): triage records what it would do and hides nothing.
	res, err := triage.Run(ctx, db, 100)
	if err != nil || res.Considered != 2 || res.WouldSuppress != 1 || res.ManualReview != 1 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	if v := active(); !v[accountant] || !v[salesEngineer] || !v[backend] {
		t.Fatalf("triage hid something while suppression was off: %v", v)
	}
	stats, _ := db.TriageStats(ctx, triage.Version, false)
	if stats.PendingSuppression != 1 || stats.ManualReview != 1 || stats.Awaiting != 0 {
		t.Fatalf("stats: %+v", stats)
	}
	// Unchanged inputs are never processed twice.
	if res, _ := triage.Run(ctx, db, 100); res.Considered != 0 {
		t.Fatalf("unchanged postings were triaged again: %+v", res)
	}

	// Pausing stops triage and nothing else.
	_ = db.SetSetting(ctx, store.SettingTriagePaused, "on", "test")
	if res, _ := triage.Run(ctx, db, 100); !res.Paused {
		t.Fatal("pause ignored")
	}
	_ = db.SetSetting(ctx, store.SettingTriagePaused, "off", "test")

	// Suppression on: the pending high-confidence posting is suppressed, reversibly.
	_ = db.SetSetting(ctx, store.SettingTriageSuppress, "on", "test")
	if res, err := triage.Run(ctx, db, 100); err != nil || res.Suppressed != 1 || res.Considered != 1 {
		t.Fatalf("activation pass: %+v %v", res, err)
	}
	if v := active(); v[accountant] || !v[salesEngineer] || !v[backend] {
		t.Fatalf("after activation: %v", v)
	}
	suppression, err := db.CurrentOpportunitySuppression(ctx, accountant)
	if err != nil || suppression == nil || suppression.Reason != store.SuppressNotRelevant || suppression.Basis != store.BasisTriage {
		t.Fatalf("suppression record: %+v %v", suppression, err)
	}

	// A person restores it: it comes back and triage never suppresses that revision again, even
	// under new rules.
	if _, err := db.ReinstateOpportunity(ctx, accountant, "operator", "worth a look"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE opportunity_triage SET triage_version='triage-v0' WHERE opportunity_id=$1`, accountant); err != nil {
		t.Fatal(err)
	}
	if res, err := triage.Run(ctx, db, 100); err != nil || res.Kept != 1 || res.Suppressed != 0 {
		t.Fatalf("after restore: %+v %v", res, err)
	}
	if !active()[accountant] {
		t.Fatal("triage re-suppressed a posting a person restored")
	}

	// The assessment itself was never rewritten.
	var classification string
	if err := db.QueryRow(ctx, `SELECT classification FROM opportunity_match WHERE opportunity_id=$1`, accountant).Scan(&classification); err != nil || classification != "unrelated" {
		t.Fatalf("assessment changed: %s %v", classification, err)
	}
}
