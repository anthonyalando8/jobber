package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/profile"
	"github.com/jackc/pgx/v5/pgxpool"
)

func phase2DB(t *testing.T) *DB {
	t.Helper()
	conn := migrationTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrations(ctx, conn); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig = conn.Config().Copy()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var actual string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&actual); err != nil || actual != conn.Config().Database {
		t.Fatal("scratch database assertion failed")
	}
	return &DB{pool: pool}
}

func TestLegacyBackfillPreservesOriginalsAndResumes(t *testing.T) {
	db := phase2DB(t)
	ctx := context.Background()
	company, err := db.CreateCompany(ctx, "Synthetic Employer", "legacy-synthetic.example", "remoteok", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	jobURL := "https://remoteok.com/jobs/synthetic-123"
	_, err = db.CreateSignal(ctx, company.ID, "hiring_backend", "Backend Engineer at Synthetic Employer. Synthetic API requirements.", &jobURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateSignal(ctx, company.ID, "hiring_backend", "Unstructured source card without a recoverable title", &jobURL)
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err = db.QueryRow(ctx, `SELECT md5(string_agg(to_jsonb(s)::text,'' ORDER BY id)) FROM signal s`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	first, err := db.BackfillLegacyOpportunities(ctx, 1)
	if err != nil || first.Converted != 1 || first.Processed != 1 {
		t.Fatalf("first batch %+v %v", first, err)
	}
	second, err := db.BackfillLegacyOpportunities(ctx, 100)
	if err != nil || second.ReviewRequired != 1 {
		t.Fatalf("second batch %+v %v", second, err)
	}
	repeat, err := db.BackfillLegacyOpportunities(ctx, 100)
	if err != nil || repeat.Processed != 0 {
		t.Fatal("backfill not idempotent", err)
	}
	if err = db.QueryRow(ctx, `SELECT md5(string_agg(to_jsonb(s)::text,'' ORDER BY id)) FROM signal s`).Scan(&after); err != nil || before != after {
		t.Fatal("legacy source changed", err)
	}
	var mapped int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM legacy_opportunity_backfill b JOIN opportunity_revision r ON r.opportunity_id=b.opportunity_id AND r.revision=b.opportunity_revision WHERE b.status='converted'`).Scan(&mapped); err != nil || mapped != 1 {
		t.Fatal("mapping missing", err)
	}
}

func TestPhase2MigrationsPreserveLegacyTables(t *testing.T) {
	conn := migrationTestDB(t)
	ctx := context.Background()
	ms, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms[:16] {
		if _, err = conn.Exec(ctx, migrationBody(m)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = conn.Exec(ctx, `UPDATE template SET body='Synthetic operator customization' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = BaselineLegacy(ctx, conn); err != nil {
		t.Fatal(err)
	}
	before, err := InspectSchema(ctx, conn, true)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect preservation after each additive script, including original sequences.
	for _, m := range ms[16:] {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, migrationBody(m)); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version,name,checksum,kind) VALUES($1,$2,$3,'applied')`, m.Version, m.Name, m.Checksum); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		after, err := InspectSchema(ctx, conn, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range applicationTables {
			if before.Tables[name].DataHash != after.Tables[name].DataHash || before.Tables[name].Rows != after.Tables[name].Rows {
				t.Fatalf("migration %d changed %s", m.Version, name)
			}
		}
		for name, value := range before.Sequences {
			if after.Sequences[name] != value {
				t.Fatalf("migration changed original sequence %s", name)
			}
		}
	}
	if err = CheckSchema(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `ALTER TABLE candidate_profile_revision DISABLE TRIGGER immutable_profile_revision`); err != nil {
		t.Fatal(err)
	}
	if err = CheckSchema(ctx, conn); err == nil {
		t.Fatal("disabled invariant accepted at startup")
	}
}

func TestProfileApprovalAndImmutableHistory(t *testing.T) {
	db := phase2DB(t)
	ctx := context.Background()
	revisions, err := db.ListProfileRevisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 1 || revisions[0].Active || len(revisions[0].Document.Technologies) != 0 {
		t.Fatal("seed activated or invented facts")
	}
	seed := revisions[0]
	if err = db.ActivateProfile(ctx, seed.ID, seed.ContentHash, 0); err != nil {
		t.Fatal(err)
	}
	doc := seed.Document
	doc.Technologies = []profile.Technology{{ID: "synthetic-tech", Name: "Synthetic Test Technology"}}
	raw, _ := json.Marshal(doc)
	id, err := db.CreateProfileDraft(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err = db.ListProfileRevisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if revisions[0].ID != id || revisions[0].Active || !revisions[1].Active {
		t.Fatal("draft silently changed active profile")
	}
	activeProfile, err := db.ActiveProfile(ctx)
	if err != nil || activeProfile == nil || activeProfile.ID != seed.ID || len(activeProfile.Document.Technologies) != 0 {
		t.Fatal("unapproved technology exposed as active evidence", err)
	}
	if err = db.ActivateProfile(ctx, id, revisions[0].ContentHash, 0); err == nil {
		t.Fatal("stale tab approved")
	}
	if err = db.ActivateProfile(ctx, id, "wrong hash", seed.ID); err == nil {
		t.Fatal("wrong content approved")
	}
	if err = db.ActivateProfile(ctx, id, revisions[0].ContentHash, seed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE candidate_profile_revision SET document='{}' WHERE id=$1`, id); err == nil {
		t.Fatal("approved facts mutable")
	}
	doc.Capabilities = []profile.Capability{{ID: "synthetic-cap", Name: "Synthetic capability", TechnologyIDs: []string{"missing"}}}
	raw, _ = json.Marshal(doc)
	if _, err = db.CreateProfileDraft(ctx, raw); err == nil {
		t.Fatal("invalid reference saved")
	}
}

func TestOpportunityIdentityRevisionsAndConcurrency(t *testing.T) {
	db := phase2DB(t)
	ctx := context.Background()
	company, err := db.CreateCompany(ctx, "Synthetic Employer", "synthetic.example", "manual", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	d := opportunity.DiscoveredOpportunity{CompanyName: company.Name, DomainCandidate: company.Domain, Source: "synthetic", ExternalID: "job-1", URL: "https://jobs.example/1", Title: "Marketing Manager", Description: "Synthetic requirements", DescriptionStatus: "complete"}
	first, err := db.IngestOpportunity(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	again, err := db.IngestOpportunity(ctx, d)
	if err != nil || first.ID != again.ID || again.Revision != 1 {
		t.Fatalf("repeat: %+v %v", again, err)
	}
	var firstSeen, lastSeen time.Time
	if err = db.QueryRow(ctx, `SELECT discovered_at,last_seen_at FROM opportunity WHERE id=$1`, first.ID).Scan(&firstSeen, &lastSeen); err != nil || !lastSeen.After(firstSeen) {
		t.Fatal("repeat did not refresh observation", err)
	}
	d.Description = strings.Repeat("Engineering café. ", 500)
	changed, err := db.IngestOpportunity(ctx, d)
	if err != nil || changed.Revision != 2 {
		t.Fatalf("revision: %+v %v", changed, err)
	}
	d.ExternalID = "job-2"
	d.Title = "Backend Engineer"
	second, err := db.IngestOpportunity(ctx, d)
	if err != nil || second.ID == first.ID {
		t.Fatalf("distinct IDs merged: %+v %v", second, err)
	}
	d.ExternalID = "job-3"
	d.Title = "Finance Officer"
	results := make(chan IngestionResult, 4)
	errors := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { r, e := db.IngestOpportunity(ctx, d); results <- r; errors <- e }()
	}
	var concurrentID int64
	for i := 0; i < 4; i++ {
		r := <-results
		if e := <-errors; e != nil {
			t.Fatal(e)
		}
		if concurrentID != 0 && concurrentID != r.ID {
			t.Fatal("concurrent duplicate")
		}
		concurrentID = r.ID
	}
	list, err := db.ListOpportunities(ctx, "", company.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("existing employer lost jobs: %d %v", len(list), err)
	}
	filtered, err := db.ListOpportunitiesFiltered(ctx, "synthetic", company.ID, "backend", "unknown", 50, 0)
	if err != nil || len(filtered) != 1 || filtered[0].Facts.Title != "Backend Engineer" {
		t.Fatal("source/title/availability filter mismatch", err)
	}
	inspected, err := db.GetOpportunity(ctx, first.ID)
	if err != nil || inspected.Revision != 2 || inspected.DiscoveredAt.IsZero() || inspected.LastSeenAt.IsZero() {
		t.Fatal("inspection missing revision or timestamps", err)
	}
	d.Source = "other-synthetic"
	other, err := db.IngestOpportunity(ctx, d)
	if err != nil || other.ID == concurrentID {
		t.Fatal("sources merged", err)
	}
	d.ExternalID = ""
	d.URL = ""
	missing, err := db.IngestOpportunity(ctx, d)
	if err != nil || missing.ID != 0 || missing.ReviewReason != "missing_source_identity" {
		t.Fatal("fabricated identity", err)
	}
	d.ExternalID = "unknown-title"
	d.Title = ""
	unknown, err := db.IngestOpportunity(ctx, d)
	if err != nil || unknown.ID == 0 {
		t.Fatal(err)
	}
	history, err := db.OpportunityHistory(ctx, unknown.ID)
	if err != nil || !strings.Contains(string(history[0]), `"title": ""`) { // JSONB spacing is stable in pgx textual output.
		var facts opportunity.DiscoveredOpportunity
		if len(history) == 0 || json.Unmarshal(history[0], &facts) != nil || facts.Title != "" {
			t.Fatal("missing title fabricated", err)
		}
	}
}

func TestOpportunityURLIdentityEnrichmentAndCompanyCollision(t *testing.T) {
	db := phase2DB(t)
	ctx := context.Background()
	company, err := db.CreateCompany(ctx, "Synthetic URL Employer", "url-synthetic.example", "manual", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	d := opportunity.DiscoveredOpportunity{Source: "synthetic", CompanyName: company.Name, DomainCandidate: company.Domain, URL: "https://jobs.example/view?job=123&utm_source=a", Title: "Backend Engineer", Description: "Synthetic requirements"}
	first, err := db.IngestOpportunity(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	d.ExternalID = "actual-source-id"
	d.URL = "https://jobs.example/view?utm_source=b&job=123"
	second, err := db.IngestOpportunity(ctx, d)
	if err != nil || second.ID != first.ID || second.Revision != 1 {
		t.Fatal("external ID enrichment duplicated/revised job", err)
	}
	var key string
	if err = db.QueryRow(ctx, `SELECT source_key FROM opportunity WHERE id=$1`, first.ID).Scan(&key); err != nil || !strings.HasPrefix(key, "url:") {
		t.Fatal("original identity key changed", err)
	}
	d.ExternalID = "different-id"
	third, err := db.IngestOpportunity(ctx, d)
	if err != nil || third.ID == first.ID || !third.Created {
		t.Fatal("same title with different external IDs merged", err)
	}
	if _, err = db.Exec(ctx, `UPDATE opportunity_revision SET document='{}' WHERE opportunity_id=$1`, third.ID); err == nil {
		t.Fatal("opportunity revision mutable")
	}
	d.ExternalID = "collision-id"
	d.CompanyName = "Different Synthetic Employer"
	collision, err := db.IngestOpportunity(ctx, d)
	if err != nil || collision.ID != 0 || collision.ReviewReason != "ambiguous_company_identity" {
		t.Fatal("domain/name collision merged", err)
	}
}
