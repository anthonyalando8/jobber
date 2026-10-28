package assess

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

// syntheticProfileJSON is a fixture. It is written only into a scratch database created by this
// test run; the live candidate profile is never read, written or activated here.
const syntheticProfileJSON = `{
 "identity":"Full-Stack Software Developer",
 "primary_strength":"Backend Engineering",
 "secondary_position":"Full-Stack Development",
 "desired_roles":[{"id":"role-backend","name":"Backend Engineering","aliases":["Backend Engineer","Backend Developer"]}],
 "technologies":[{"id":"tech-go","name":"Go","aliases":["Golang"]},{"id":"tech-postgres","name":"PostgreSQL","aliases":["Postgres"]}],
 "capabilities":[
  {"id":"cap-api","name":"API development","description":"Designs and builds REST APIs","technology_ids":["tech-go"],"role_family_ids":["role-backend"]},
  {"id":"cap-data","name":"Database design","description":"Relational database and schema design","technology_ids":["tech-postgres"],"role_family_ids":["role-backend"]}],
 "engagement":{"employment_types":["full-time"]},
 "signature":{}
}`

const backendJobText = `Requirements:
- Strong experience with Go
- Experience with PostgreSQL and schema design
- Experience building REST APIs
`

// scratchDB creates an isolated database for this test and proves the connection really points
// at it before any fixture write, as every mutating Phase 2 and Phase 3 test must.
func scratchDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := os.Getenv("MIGRATION_TEST_DATABASE_URL")
	if path := os.Getenv("MIGRATION_TEST_ENV_FILE"); path != "" {
		vars, err := godotenv.Read(path)
		if err != nil {
			t.Fatal("cannot read test connection environment")
		}
		dsn = vars["DATABASE_URL"]
	}
	if dsn == "" {
		t.Skip("set MIGRATION_TEST_DATABASE_URL or MIGRATION_TEST_ENV_FILE for isolated database tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("test admin connection failed (DSN withheld)")
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	name := "prospector_test_" + hex.EncodeToString(buf)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE template0"); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("scratch cleanup %s: %v", name, err)
		}
	})
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test config")
	}
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal("scratch connection failed")
	}
	if err := store.ApplyMigrations(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx, conn); err != nil {
		t.Fatalf("scratch schema check failed: %v", err)
	}
	conn.Close(ctx)

	cfg.Database = name
	db, err := store.NewDB(ctx, scratchDSN(dsn, name))
	if err != nil {
		t.Fatal("scratch pool failed")
	}
	t.Cleanup(db.Close)
	var actual string
	if err := db.QueryRow(ctx, `SELECT current_database()`).Scan(&actual); err != nil || actual != name {
		t.Fatalf("scratch database assertion failed: %q", actual)
	}
	return db
}

// scratchDSN rewrites only the database name, leaving credentials and options untouched.
func scratchDSN(dsn, name string) string {
	if i := strings.LastIndex(dsn, "/"); i >= 0 {
		rest := dsn[i+1:]
		query := ""
		if q := strings.Index(rest, "?"); q >= 0 {
			query = rest[q:]
		}
		return dsn[:i+1] + name + query
	}
	return dsn
}

func activateProfile(t *testing.T, db *store.DB) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := db.CreateProfileDraft(ctx, []byte(syntheticProfileJSON))
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

func ingest(t *testing.T, db *store.DB, company, externalID, title, description string) int64 {
	t.Helper()
	result, err := db.IngestOpportunity(context.Background(), opportunity.DiscoveredOpportunity{
		CompanyName: company, DomainCandidate: strings.ToLower(strings.ReplaceAll(company, " ", "-")) + ".example",
		Source: "fixture", ExternalID: externalID, URL: "https://jobs.example.test/" + externalID,
		Title: title, Description: description, DescriptionStatus: "complete",
		Availability: "open", RemoteStatus: "unknown",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReviewReason != "" {
		t.Fatalf("fixture went to ingestion review: %s", result.ReviewReason)
	}
	return result.ID
}

// Without an active approved profile nothing becomes authoritative: matching says so, no
// assessment is stored, and no plan can be created.
func TestInactiveProfileBlocksEverythingAuthoritative(t *testing.T) {
	db := scratchDB(t)
	ctx := context.Background()
	service := New(db)

	state, err := service.Profile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Active {
		t.Fatal("the seeded profile revision is approved but inactive and must not be treated as active")
	}
	if state.Reason == "" {
		t.Fatal("an inactive profile must explain itself")
	}

	jobID := ingest(t, db, "Inactive Profile Ltd", "j-1", "Backend Engineer", backendJobText)
	assessment, err := service.AssessOpportunity(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Match.Classification != opportunity.ClassificationProfileRequired {
		t.Fatalf("classification = %q, want profile_required", assessment.Match.Classification)
	}
	var stored int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM opportunity_match`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("stored %d assessments without an active profile, want 0", stored)
	}

	row, err := db.GetOpportunity(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContact(ctx, row.CompanyID, "email", "cto@inactive.example", nil, nil, 3); err != nil {
		t.Fatal(err)
	}
	_, err = service.CreatePlan(ctx, CreatePlanRequest{
		CompanyID: row.CompanyID, Intent: plan.SpeculativeJobEnquiry, Origin: "manual"})
	if err == nil {
		t.Fatal("no contact plan may be created without an active approved profile")
	}
	if !strings.Contains(err.Error(), "profile") {
		t.Fatalf("refusal should name the missing profile: %v", err)
	}
}

// A and N and O. An unrelated role cannot become an application through the service either, but
// it can still be the provenance of a speculative enquiry.
func TestUnrelatedRoleCannotBecomeApplicationThroughService(t *testing.T) {
	db := scratchDB(t)
	ctx := context.Background()
	service := New(db)
	activateProfile(t, db)

	jobID := ingest(t, db, "Provenance Ltd", "j-1", "Marketing Manager", backendJobText)
	row, err := db.GetOpportunity(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContact(ctx, row.CompanyID, "email", "cto@provenance-ltd.example", nil, nil, 3); err != nil {
		t.Fatal(err)
	}
	// Give the company a real engineering reason to be contacted at all.
	if _, _, err := db.UpsertEvidence(ctx, row.CompanyID, store.EvidenceInput{
		Kind: "engineering_request", Detail: "The company states an engineering hiring need",
		Excerpt:   "We are hiring backend developers to grow our platform team",
		SourceURL: "https://provenance-ltd.example/careers", ExtractionMethod: "site_text_v1", Confidence: "high",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshCompany(ctx, row.CompanyID); err != nil {
		t.Fatal(err)
	}

	// Manually forcing a high company score changes nothing.
	if err := db.UpdateCompanyScore(ctx, row.CompanyID, 100, "application", []byte(`[]`)); err != nil {
		t.Fatal(err)
	}
	_, err = service.CreatePlan(ctx, CreatePlanRequest{
		CompanyID: row.CompanyID, OpportunityID: jobID,
		Intent: plan.SpecificJobApplication, Origin: "manual"})
	if err == nil {
		t.Fatal("a manual override must not turn an unrelated role into an application")
	}
	if !strings.Contains(err.Error(), "unrelated") {
		t.Fatalf("refusal should cite the unrelated classification: %v", err)
	}

	created, err := service.CreatePlan(ctx, CreatePlanRequest{
		CompanyID: row.CompanyID, OpportunityID: jobID,
		Intent: plan.SpeculativeJobEnquiry, Origin: "manual"})
	if err != nil {
		t.Fatalf("a speculative enquiry should be recordable: %v", err)
	}
	if created.OpportunityID != nil {
		t.Fatal("a speculative enquiry must not pin an applied-for vacancy")
	}
	if created.ProvenanceOpportunityID == nil || *created.ProvenanceOpportunityID != jobID {
		t.Fatal("the discovery listing should be kept as provenance")
	}
	if len(created.TargetFamilies) == 0 {
		t.Fatal("a speculative enquiry must state its target engineering families")
	}
	for _, family := range created.TargetFamilies {
		if strings.Contains(strings.ToLower(family.Name), "marketing") {
			t.Fatalf("the unrelated advertised title leaked into the target families: %v", created.TargetFamilies)
		}
	}
	forbidden := map[string]bool{}
	for _, claim := range created.Selection.ForbiddenClaims {
		forbidden[claim.Code] = true
	}
	if !forbidden["unrelated_title_as_target_role"] {
		t.Fatalf("the plan must forbid using the unrelated title as the role sought: %+v", created.Selection.ForbiddenClaims)
	}
	if !forbidden["recipient_payment_problem"] {
		t.Fatalf("no payment need is evidenced, so a payment diagnosis must be forbidden: %+v", created.Selection.ForbiddenClaims)
	}
}

// A recorded plan pins the exact vacancy revision and carries its full reasoning.
func TestSpecificApplicationPlanPinsRevisionAndReasoning(t *testing.T) {
	db := scratchDB(t)
	ctx := context.Background()
	service := New(db)
	activateProfile(t, db)

	jobID := ingest(t, db, "Applying Ltd", "j-1", "Backend Engineer", backendJobText)
	row, err := db.GetOpportunity(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContact(ctx, row.CompanyID, "email", "careers@applying-ltd.example", nil, nil, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshCompany(ctx, row.CompanyID); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreatePlan(ctx, CreatePlanRequest{
		CompanyID: row.CompanyID, OpportunityID: jobID,
		Intent: plan.SpecificJobApplication, Origin: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if created.OpportunityID == nil || created.OpportunityRevision == nil {
		t.Fatal("a specific application must pin an opportunity revision")
	}
	if *created.OpportunityRevision != row.Revision {
		t.Fatalf("pinned revision %d, want %d", *created.OpportunityRevision, row.Revision)
	}
	if created.MatchClassification == opportunity.ClassificationUnrelated {
		t.Fatal("a recorded application must not carry an unrelated classification")
	}
	if created.ProfileRevisionID == 0 {
		t.Fatal("a plan must cite the approved profile revision it was built from")
	}
	if len(created.Reasons) == 0 {
		t.Fatal("a plan must record why it exists")
	}
	if created.RecipientEmail != "careers@applying-ltd.example" {
		t.Fatalf("recipient = %q", created.RecipientEmail)
	}
	if created.Selection.SelectorVersion == "" || len(created.Selection.AllowedRefs) == 0 {
		t.Fatalf("a plan must fix the approved facts the composer may use: %+v", created.Selection)
	}
}

// Proposals report a stale assessment rather than treating it as a current fit.
func TestStaleAssessmentIsNotUsedAsCurrentFit(t *testing.T) {
	db := scratchDB(t)
	ctx := context.Background()
	service := New(db)
	activateProfile(t, db)

	jobID := ingest(t, db, "Stale Ltd", "j-1", "Backend Engineer", backendJobText)
	row, err := db.GetOpportunity(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContact(ctx, row.CompanyID, "email", "careers@stale-ltd.example", nil, nil, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshCompany(ctx, row.CompanyID); err != nil {
		t.Fatal(err)
	}
	// The source changes after the assessment was stored.
	ingest(t, db, "Stale Ltd", "j-1", "Backend Engineer", backendJobText+"- Experience with React\n")

	proposal, err := service.Propose(ctx, row.CompanyID, jobID, plan.SpecificJobApplication)
	if err != nil {
		t.Fatal(err)
	}
	if !proposal.MatchStale {
		t.Fatal("the stored assessment should be reported stale after a source revision")
	}
	if !proposal.Decision.Blockers.Has("stale_assessment") {
		t.Fatalf("a stale assessment must be a stated blocker: %v", proposal.Decision.Blockers.Codes())
	}
	if _, err := service.CreatePlan(ctx, CreatePlanRequest{
		CompanyID: row.CompanyID, OpportunityID: jobID,
		Intent: plan.SpecificJobApplication, Origin: "manual"}); err == nil {
		t.Fatal("a plan must not be recorded against a stale assessment")
	}
}

// The Design Gallery case: research found only a checkout, so offering services is blocked. The
// operator, having looked at the site, can overrule that with a stated reason; the reason and the
// overruled rule are kept on the plan and reach the email writer.
func TestOperatorCanOverrideRelevanceBlockerWithAReason(t *testing.T) {
	db := scratchDB(t)
	ctx := context.Background()
	service := New(db)
	activateProfile(t, db)
	jobID := ingest(t, db, "Gallery Shop", "g-1", "Marketing Associate", "Requirements:\n- Social media marketing\n")
	row, err := db.GetOpportunity(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContact(ctx, row.CompanyID, "email", "customerservice@gallery-shop.example", nil, nil, 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.UpsertEvidence(ctx, row.CompanyID, store.EvidenceInput{
		Kind: "checkout_present", Detail: "Checkout or payment functionality is present on the site",
		SourceURL: "https://gallery-shop.example", ExtractionMethod: "site_text_v1", Confidence: "medium",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshCompany(ctx, row.CompanyID); err != nil {
		t.Fatal(err)
	}

	proposal, err := service.Propose(ctx, row.CompanyID, 0, plan.ServiceOutreach)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Decision.Ready() || !proposal.Decision.Overridable() {
		t.Fatalf("a checkout alone should block, overridably: %+v", proposal.Decision)
	}
	if _, err := service.CreatePlan(ctx, CreatePlanRequest{CompanyID: row.CompanyID, Intent: plan.ServiceOutreach, Origin: "manual"}); err == nil {
		t.Fatal("without an override the blocker must still hold")
	}
	reason := "Their site is an online store; I want to offer to improve its speed and checkout."
	created, err := service.CreatePlan(ctx, CreatePlanRequest{CompanyID: row.CompanyID, Intent: plan.ServiceOutreach, Origin: "manual",
		OverrideReason: reason, OverriddenBy: "anthony"})
	if err != nil {
		t.Fatalf("override refused: %v", err)
	}
	if !created.RequiresReview || !created.Reasons.Has(plan.OperatorOverrideCode) || !created.Reasons.Has("overridden_no_company_relevance") {
		t.Fatalf("override not recorded on the plan: %v", created.Reasons.Codes())
	}
	snapshot, err := db.BuildCompositionSnapshot(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range snapshot.ReviewReasons {
		found = found || (r.Code == plan.OperatorOverrideCode && r.Text == reason)
	}
	if !found {
		t.Fatal("the operator's reason does not reach the email writer")
	}

	// Applying for the unrelated vacancy is still refused, with or without a reason; the
	// blocker names the speculative and service routes instead.
	if _, err := service.CreatePlan(ctx, CreatePlanRequest{CompanyID: row.CompanyID, OpportunityID: jobID, Intent: plan.SpecificJobApplication,
		Origin: "manual", OverrideReason: reason}); err == nil {
		t.Fatal("an unrelated vacancy became a specific application")
	}
}
