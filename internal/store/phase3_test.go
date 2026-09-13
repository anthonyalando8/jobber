package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/evidence"
	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/profile"
	"github.com/anthonyalando8/prospector/internal/relevance"
)

// syntheticProfileJSON is a fixture document. It is only ever saved into a scratch database
// created by this test run; the live candidate profile is never touched or activated.
const syntheticProfileJSON = `{
 "identity":"Full-Stack Software Developer",
 "primary_strength":"Backend Engineering",
 "secondary_position":"Full-Stack Development",
 "desired_roles":[{"id":"role-backend","name":"Backend Engineering","aliases":["Backend Engineer","Backend Developer"]}],
 "technologies":[{"id":"tech-go","name":"Go","aliases":["Golang"]},{"id":"tech-postgres","name":"PostgreSQL","aliases":["Postgres"]}],
 "capabilities":[
  {"id":"cap-api","name":"API development","description":"Designs and builds REST APIs","technology_ids":["tech-go"],"role_family_ids":["role-backend"]},
  {"id":"cap-data","name":"Database design","description":"Relational database and schema design","technology_ids":["tech-postgres"],"role_family_ids":["role-backend"]}],
 "signature":{}
}`

const backendJobText = `Requirements:
- Strong experience with Go
- Experience with PostgreSQL and schema design
- Experience building REST APIs
`

// activateSyntheticProfile saves and activates a fixture revision in the scratch database and
// returns its revision id.
func activateSyntheticProfile(t *testing.T, db *DB, document string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := db.CreateProfileDraft(ctx, []byte(document))
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := db.ListProfileRevisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	var active int64
	for _, r := range revisions {
		if r.Active {
			active = r.ID
		}
		if r.ID == id {
			hash = r.ContentHash
		}
	}
	if err := db.ActivateProfile(ctx, id, hash, active); err != nil {
		t.Fatal(err)
	}
	return id
}

func ingestFixtureJob(t *testing.T, db *DB, companyName, domain, externalID, title, description string) IngestionResult {
	t.Helper()
	result, err := db.IngestOpportunity(context.Background(), opportunity.DiscoveredOpportunity{
		CompanyName: companyName, DomainCandidate: domain, Source: "fixture",
		ExternalID: externalID, URL: "https://jobs.example.test/" + externalID,
		Title: title, Description: description, DescriptionStatus: "complete",
		Availability: "open", RemoteStatus: "unknown",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReviewReason != "" {
		t.Fatalf("fixture job went to ingestion review: %s", result.ReviewReason)
	}
	return result
}

func assessAndStore(t *testing.T, db *DB, opportunityID, profileRevisionID int64) opportunity.MatchResult {
	t.Helper()
	ctx := context.Background()
	row, err := db.GetOpportunity(ctx, opportunityID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := db.ActiveProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var doc *profile.Document
	if active != nil {
		copied := active.Document
		doc = &copied
	}
	result := opportunity.Assess(opportunity.AssessInput{
		OpportunityID: row.ID, OpportunityRevision: row.Revision, Facts: row.Facts,
		Profile: doc, ProfileRevisionID: profileRevisionID,
	})
	if result.Classification != opportunity.ClassificationProfileRequired {
		if err := db.SaveOpportunityMatch(ctx, result); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

// Phase 3 migrations must add columns and tables without disturbing a single original row.
func TestPhase3MigrationsPreserveLegacySignals(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	company, err := db.CreateCompany(ctx, "Legacy Fixture Ltd", "legacy-fixture.example", "jobboard", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	url := "https://legacy-fixture.example/"
	for i := 0; i < 3; i++ {
		if _, err := db.CreateSignal(ctx, company.ID, "tech:react", "Found react in site code", &url); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := db.ListEvidence(ctx, company.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 3 {
		t.Fatalf("preserved legacy duplicates = %d, want all 3 rows kept", len(facts))
	}
	for _, f := range facts {
		if !f.Legacy() {
			t.Fatalf("signal %d should be labelled a legacy detection, got %q", f.SignalID, f.ExtractionMethod)
		}
		if f.Detail != "Found react in site code" {
			t.Fatalf("legacy detection text was altered: %q", f.Detail)
		}
		if f.FirstSeen.IsZero() || f.LastSeen.IsZero() {
			t.Fatal("legacy rows should report observation times from created_at")
		}
	}
	// J: the three preserved rows are one fact.
	groups := evidence.GroupFacts(facts)
	if len(groups) != 1 || groups[0].Observations != 3 || !groups[0].Legacy {
		t.Fatalf("legacy duplicates should group into one legacy fact, got %+v", groups)
	}

	// Legacy detection text is history and cannot be rewritten into a quotation.
	_, err = db.Exec(ctx, `UPDATE signal SET detail = 'We urgently need a React rewrite' WHERE id = $1`, facts[0].SignalID)
	if err == nil {
		t.Fatal("rewriting a legacy detection into a quotation must be refused")
	}
	if !strings.Contains(err.Error(), "historical evidence") {
		t.Fatalf("unexpected refusal: %v", err)
	}
}

// New canonical evidence deduplicates; the same fact on three pages stays one row.
func TestUpsertEvidenceDeduplicatesAcrossPages(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	company, err := db.CreateCompany(ctx, "Evidence Fixture Ltd", "evidence-fixture.example", "manual", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	pages := []string{"https://evidence-fixture.example/", "https://evidence-fixture.example/about", "https://evidence-fixture.example/services"}
	var first int64
	for i, page := range pages {
		id, inserted, err := db.UpsertEvidence(ctx, company.ID, EvidenceInput{
			Kind: "offers_tech_services", Detail: "Site markets software development as a service",
			Excerpt: "We build custom software for clients", SourceURL: page,
			ExtractionMethod: "site_text_v1", Confidence: "medium",
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = id
			if !inserted {
				t.Fatal("the first observation should insert a row")
			}
		} else {
			if inserted {
				t.Fatal("a repeated observation must not insert another row")
			}
			if id != first {
				t.Fatalf("repeat observation wrote row %d, want %d", id, first)
			}
		}
	}
	facts, err := db.ListEvidence(ctx, company.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("stored rows = %d, want 1 canonical fact", len(facts))
	}
	if facts[0].Observations != 3 {
		t.Fatalf("observations = %d, want 3", facts[0].Observations)
	}
	if facts[0].Excerpt == "" || facts[0].SourceURL == "" {
		t.Fatal("canonical evidence must retain its excerpt and source URL")
	}
	if facts[0].Legacy() {
		t.Fatal("new canonical evidence must not be labelled a legacy detection")
	}
}

// K. Several vacancies at one employer are stored and assessed independently.
func TestMultipleOpportunitiesAtOneCompanyAssessIndependently(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	revisionID := activateSyntheticProfile(t, db, syntheticProfileJSON)
	ingestFixtureJob(t, db, "Multi Role Ltd", "multi-role.example", "j-1", "Marketing Manager", backendJobText)
	ingestFixtureJob(t, db, "Multi Role Ltd", "multi-role.example", "j-2", "Finance Officer", backendJobText)
	backend := ingestFixtureJob(t, db, "Multi Role Ltd", "multi-role.example", "j-3", "Backend Engineer", backendJobText)

	rows, err := db.ListOpportunities(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("stored opportunities = %d, want 3", len(rows))
	}
	companyID := rows[0].CompanyID
	for _, row := range rows {
		if row.CompanyID != companyID {
			t.Fatal("all three vacancies should belong to one company")
		}
		assessAndStore(t, db, row.ID, revisionID)
	}
	stored, err := db.LatestMatchesForCompany(ctx, companyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 {
		t.Fatalf("stored assessments = %d, want one per vacancy", len(stored))
	}
	if got := stored[backend.ID].Result.Classification; got == opportunity.ClassificationUnrelated {
		t.Fatalf("the backend vacancy was classified %q", got)
	}
	counts, err := db.MatchClassificationCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts[opportunity.ClassificationUnrelated] != 2 {
		t.Fatalf("classification counts = %v, want two unrelated", counts)
	}
}

// P. Activating a new profile revision makes every earlier assessment stale.
func TestActivatingNewProfileRevisionMakesMatchesStale(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	revisionID := activateSyntheticProfile(t, db, syntheticProfileJSON)
	job := ingestFixtureJob(t, db, "Stale Profile Ltd", "stale-profile.example", "j-1", "Backend Engineer", backendJobText)
	assessAndStore(t, db, job.ID, revisionID)

	row, err := db.GetOpportunity(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.LatestMatchesForCompany(ctx, row.CompanyID)
	if err != nil {
		t.Fatal(err)
	}
	if stored[job.ID].Stale {
		t.Fatalf("assessment should be current before activation: %s", stored[job.ID].StaleWhy)
	}
	if current, err := db.GetCurrentOpportunityMatch(ctx, job.ID); err != nil || current == nil {
		t.Fatalf("current match should be readable: %v %v", current, err)
	}

	extended := strings.Replace(syntheticProfileJSON,
		`{"id":"cap-data","name":"Database design","description":"Relational database and schema design","technology_ids":["tech-postgres"],"role_family_ids":["role-backend"]}`,
		`{"id":"cap-data","name":"Database design","description":"Relational database, schema and transaction design","technology_ids":["tech-postgres"],"role_family_ids":["role-backend"]}`, 1)
	activateSyntheticProfile(t, db, extended)

	stored, err = db.LatestMatchesForCompany(ctx, row.CompanyID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored[job.ID].Stale {
		t.Fatal("activating a new profile revision must make the previous assessment stale")
	}
	if !strings.Contains(stored[job.ID].StaleWhy, "profile revision") {
		t.Fatalf("stale reason should name the profile revision, got %q", stored[job.ID].StaleWhy)
	}
	current, err := db.GetCurrentOpportunityMatch(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current != nil {
		t.Fatal("a stale assessment must never be returned as the current match")
	}
}

// Q. A changed advertised description makes the prior assessment stale and requires a rerun.
func TestChangedOpportunityRevisionMakesMatchStale(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	revisionID := activateSyntheticProfile(t, db, syntheticProfileJSON)
	job := ingestFixtureJob(t, db, "Changing Job Ltd", "changing-job.example", "j-1", "Backend Engineer", backendJobText)
	assessAndStore(t, db, job.ID, revisionID)

	updated := ingestFixtureJob(t, db, "Changing Job Ltd", "changing-job.example", "j-1",
		"Backend Engineer", backendJobText+"- Experience with React\n")
	if updated.Revision != 2 {
		t.Fatalf("changed description should create revision 2, got %d", updated.Revision)
	}
	row, err := db.GetOpportunity(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.LatestMatchesForCompany(ctx, row.CompanyID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored[job.ID].Stale {
		t.Fatal("a changed source revision must make the prior assessment stale")
	}
	if !strings.Contains(stored[job.ID].StaleWhy, "revision") {
		t.Fatalf("stale reason should name the source revision, got %q", stored[job.ID].StaleWhy)
	}
	if current, err := db.GetCurrentOpportunityMatch(ctx, job.ID); err != nil || current != nil {
		t.Fatalf("stale assessment returned as current: %v %v", current, err)
	}
	// Rerunning against the new revision restores a current assessment.
	assessAndStore(t, db, job.ID, revisionID)
	current, err := db.GetCurrentOpportunityMatch(ctx, job.ID)
	if err != nil || current == nil {
		t.Fatalf("rerun assessment should be current: %v %v", current, err)
	}
	if current.OpportunityRevision != 2 {
		t.Fatalf("current assessment pins revision %d, want 2", current.OpportunityRevision)
	}
}

// An assessment without an active approved profile is never stored.
func TestMatchWithoutActiveProfileIsNotStored(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	job := ingestFixtureJob(t, db, "No Profile Ltd", "no-profile.example", "j-1", "Backend Engineer", backendJobText)
	result := assessAndStore(t, db, job.ID, 0)
	if result.Classification != opportunity.ClassificationProfileRequired {
		t.Fatalf("classification = %q, want profile_required", result.Classification)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM opportunity_match`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stored %d assessments without an active approved profile, want 0", count)
	}
	if err := db.SaveOpportunityMatch(ctx, result); err == nil {
		t.Fatal("storing a profile_required assessment must be refused")
	}
}

// O. The unrelated-role invariant is enforced by the database itself, not only by callers.
func TestDatabaseRefusesUnrelatedSpecificApplication(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	revisionID := activateSyntheticProfile(t, db, syntheticProfileJSON)
	job := ingestFixtureJob(t, db, "Override Ltd", "override.example", "j-1", "Marketing Manager", backendJobText)
	match := assessAndStore(t, db, job.ID, revisionID)
	if match.Classification != opportunity.ClassificationUnrelated {
		t.Fatalf("fixture should be unrelated, got %q", match.Classification)
	}
	row, err := db.GetOpportunity(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	contact, err := db.CreateContact(ctx, row.CompanyID, "email", "cto@override.example", nil, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	// A caller bypassing every Go-level check still cannot record this plan.
	id, revision := job.ID, row.Revision
	_, err = db.CreateContactPlan(ctx, ContactPlan{
		CompanyID: row.CompanyID, Intent: string(plan.SpecificJobApplication),
		OpportunityID: &id, OpportunityRevision: &revision,
		MatchClassification: opportunity.ClassificationUnrelated,
		RecipientContactID:  contact.ID, RecipientEmail: contact.Value,
		RecipientRelevance: "preferred", RecipientReason: "test", ProfileRevisionID: revisionID,
		SelectionOrigin: "manual", Status: "selected",
	})
	if err == nil {
		t.Fatal("the database must refuse a specific application against an unrelated role")
	}
	// A speculative plan at the same company is allowed, with the listing as provenance only.
	speculative, err := db.CreateContactPlan(ctx, ContactPlan{
		CompanyID: row.CompanyID, Intent: string(plan.SpeculativeJobEnquiry),
		ProvenanceOpportunityID: &id, MatchClassification: opportunity.ClassificationUnrelated,
		TargetFamilies:     []plan.TargetFamily{{ID: "role-backend", Name: "Backend Engineering"}},
		RecipientContactID: contact.ID, RecipientEmail: contact.Value,
		RecipientRelevance: "preferred", RecipientReason: "technical contact",
		ProfileRevisionID: revisionID, SelectionOrigin: "manual", Status: "selected",
		Reasons: explain.Reasons{}.Add("unrelated_discovery_is_provenance_only", "provenance only"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if speculative.OpportunityID != nil {
		t.Fatal("a speculative enquiry must not pin an applied-for vacancy")
	}
	if speculative.ProvenanceOpportunityID == nil || *speculative.ProvenanceOpportunityID != job.ID {
		t.Fatal("the discovery listing should be retained as provenance")
	}
}

// A specific application pins a real vacancy revision, and one live plan exists per key.
func TestContactPlanPinsRevisionAndIsUniquePerKey(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	revisionID := activateSyntheticProfile(t, db, syntheticProfileJSON)
	job := ingestFixtureJob(t, db, "Plan Ltd", "plan.example", "j-1", "Backend Engineer", backendJobText)
	match := assessAndStore(t, db, job.ID, revisionID)
	row, err := db.GetOpportunity(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	contact, err := db.CreateContact(ctx, row.CompanyID, "email", "careers@plan.example", nil, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	id, revision := job.ID, row.Revision
	base := ContactPlan{
		CompanyID: row.CompanyID, Intent: string(plan.SpecificJobApplication),
		OpportunityID: &id, OpportunityRevision: &revision,
		MatchClassification: match.Classification,
		RecipientContactID:  contact.ID, RecipientEmail: contact.Value,
		RecipientRelevance: "preferred", RecipientReason: "recruiting contact",
		ProfileRevisionID: revisionID, SelectionOrigin: "manual", Status: "selected",
		Selection: plan.Selection{SelectorVersion: plan.SelectorVersion, Intent: plan.SpecificJobApplication},
	}
	created, err := db.CreateContactPlan(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if created.OpportunityRevision == nil || *created.OpportunityRevision != row.Revision {
		t.Fatal("a specific application must pin the exact opportunity revision")
	}
	if _, err := db.CreateContactPlan(ctx, base); err == nil {
		t.Fatal("a second live plan for the same key must be refused")
	}
	if err := db.SetContactPlanStatus(ctx, created.ID, "closed"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContactPlan(ctx, base); err != nil {
		t.Fatalf("a new plan should be allowed once the previous one is closed: %v", err)
	}
}

// The frozen recipient cannot change without a new plan revision and a new snapshot.
func TestRecipientIsFrozenPerPlanRevision(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	revisionID := activateSyntheticProfile(t, db, syntheticProfileJSON)
	job := ingestFixtureJob(t, db, "Freeze Ltd", "freeze.example", "j-1", "Backend Engineer", backendJobText)
	match := assessAndStore(t, db, job.ID, revisionID)
	row, err := db.GetOpportunity(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.CreateContact(ctx, row.CompanyID, "email", "careers@freeze.example", nil, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateContact(ctx, row.CompanyID, "email", "cto@freeze.example", nil, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	id, revision := job.ID, row.Revision
	created, err := db.CreateContactPlan(ctx, ContactPlan{
		CompanyID: row.CompanyID, Intent: string(plan.SpecificJobApplication),
		OpportunityID: &id, OpportunityRevision: &revision, MatchClassification: match.Classification,
		RecipientContactID: first.ID, RecipientEmail: first.Value,
		RecipientRelevance: "preferred", RecipientReason: "recruiting contact",
		ProfileRevisionID: revisionID, SelectionOrigin: "manual", Status: "selected",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A silent in-place recipient swap is refused outright.
	_, err = db.Exec(ctx, `UPDATE contact_plan SET recipient_contact_id = $2, recipient_email = $3 WHERE id = $1`,
		created.ID, second.ID, second.Value)
	if err == nil {
		t.Fatal("changing the recipient without a new plan revision must be refused")
	}

	updated, err := db.UpdatePlanRecipient(ctx, created.ID, second.ID, second.Value, "", "review_required", "technical contact chosen by hand")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 {
		t.Fatalf("plan revision = %d, want 2 after a recipient change", updated.Revision)
	}
	if !updated.RequiresReview {
		t.Fatal("a recipient change must reopen review")
	}
	snapshots, err := db.ContactPlanRevisions(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("plan revisions = %d, want 2 immutable snapshots", len(snapshots))
	}
	var frozen ContactPlan
	if err := json.Unmarshal(snapshots[1], &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.RecipientEmail != first.Value {
		t.Fatalf("the first snapshot froze %q, want %q", frozen.RecipientEmail, first.Value)
	}
	if _, err := db.Exec(ctx, `UPDATE contact_plan_revision SET snapshot = '{}' WHERE plan_id = $1`, created.ID); err == nil {
		t.Fatal("plan revision snapshots must be immutable")
	}
	// A contact belonging to another company can never be selected.
	other, err := db.CreateCompany(ctx, "Other Ltd", "other-freeze.example", "manual", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := db.CreateContact(ctx, other.ID, "email", "someone@other-freeze.example", nil, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdatePlanRecipient(ctx, created.ID, outsider.ID, outsider.Value, "", "preferred", "cross-company"); err == nil {
		t.Fatal("a contact from another company must be refused")
	}
}

// Company relevance is stored with its full explanation, not just a number.
func TestCompanyRelevanceRoundTrips(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	revisionID := activateSyntheticProfile(t, db, syntheticProfileJSON)
	company, err := db.CreateCompany(ctx, "Relevance Ltd", "relevance.example", "manual", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	result := relevance.Result{
		AssessorVersion: relevance.AssessorVersion, CompanyID: company.ID, ProfileRevisionID: revisionID,
		Score: 45, Relevant: true,
		Categories: []relevance.Category{{Key: "engineering_need", Label: "need", Points: 15, Max: 30}},
		Reasons:    explain.Reasons{}.Add("stated_engineering_need", "The company states a need"),
	}
	if err := db.SaveCompanyRelevance(ctx, result); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.GetCompanyRelevance(ctx, company.ID)
	if err != nil || loaded == nil {
		t.Fatalf("relevance should round trip: %v %v", loaded, err)
	}
	if loaded.Score != 45 || !loaded.Relevant || loaded.PaymentNeedEvidenced {
		t.Fatalf("unexpected stored relevance: %+v", loaded)
	}
	if len(loaded.Reasons) != 1 || !loaded.Reasons.Has("stated_engineering_need") {
		t.Fatalf("stored relevance must keep its reasons, got %v", loaded.Reasons)
	}
}

// phase3DB creates an isolated scratch database and re-asserts the destination before any
// Phase 3 fixture write, keeping the Phase 2 rule that a mutating integration test proves where
// it is writing before it writes.
func phase3DB(t *testing.T) *DB {
	t.Helper()
	db := phase2DB(t)
	var name string
	if err := db.QueryRow(context.Background(), `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "prospector_test_") {
		t.Fatalf("refusing to run a mutating test against %q: expected a scratch database", name)
	}
	return db
}
