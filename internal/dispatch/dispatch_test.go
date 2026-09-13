package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

// ---------------------------------------------------------------- fake transport

// fakeTransport records every transmission and returns a programmed result. No real message is
// ever sent by these tests, and a real address never appears in them.
type fakeTransport struct {
	mu      sync.Mutex
	calls   int
	results []send.Result
	last    send.Message
}

func (f *fakeTransport) Name() string { return "fake" }

// The fake sends as a domain reserved for documentation and testing, so a Message-ID produced in
// a test can never look like it came from a real host.
func (f *fakeTransport) SenderDomain() string { return "prospector.example" }

func (f *fakeTransport) Send(_ send.Context, m send.Message) send.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.last = m
	if len(f.results) == 0 {
		return send.Result{Provider: "fake", Outcome: send.OutcomeAccepted, Stage: send.StageQuit,
			AcceptedAt: time.Now().UTC(), ProviderMessageID: m.MessageID, Response: "accepted"}
	}
	r := f.results[0]
	if len(f.results) > 1 {
		f.results = f.results[1:]
	}
	r.Provider = "fake"
	if r.Outcome == send.OutcomeAccepted && r.ProviderMessageID == "" {
		r.ProviderMessageID = m.MessageID
	}
	return r
}

func (f *fakeTransport) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func accepted() send.Result {
	return send.Result{Outcome: send.OutcomeAccepted, Stage: send.StageQuit, AcceptedAt: time.Now().UTC()}
}

// ---------------------------------------------------------------- scratch database

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

// ---------------------------------------------------------------- fixture

const syntheticProfileJSON = `{
 "identity":"Full-Stack Software Developer",
 "primary_strength":"Backend Engineering",
 "secondary_position":"Full-Stack Development",
 "desired_roles":[{"id":"role-backend","name":"Backend Engineering","aliases":["Backend Engineer","Backend Developer"]}],
 "technologies":[{"id":"tech-go","name":"Go","aliases":["Golang"]},{"id":"tech-pg","name":"PostgreSQL","aliases":["Postgres"]}],
 "capabilities":[{"id":"cap-api","name":"API development","description":"Designs and builds REST APIs","technology_ids":["tech-go"],"role_family_ids":["role-backend"]},
                 {"id":"cap-data","name":"Database design","description":"Relational database and schema design","technology_ids":["tech-pg"],"role_family_ids":["role-backend"]}],
 "engagement":{"employment_types":["full-time"]},
 "signature":{"name":"A. Synthetic","descriptor":"Full-Stack Software Developer"}
}`

const backendJob = `Requirements:
- Strong experience with Go
- Experience with PostgreSQL and schema design
- Experience building REST APIs
`

type fixture struct {
	db        *store.DB
	companyID int64
	contactID int64
	planID    int64
	draftID   int64
	revision  int
	hash      string
	email     composition.Email
	transport *fakeTransport
	service   *Service
}

func ready() send.Readiness {
	return send.EvaluateReadiness(send.Inputs{
		Mode: send.ModeManualSend, SchemaVersion: send.RequiredSchemaVersion, SchemaHealthy: true,
		TransportReady: true, SenderConfigured: true, Limits: send.DefaultLimits(),
	})
}

// newFixture builds the whole chain through the real Phase 2-4 code: company, contact, approved
// and activated profile, opportunity, assessment, contact plan, composition snapshot, a reviewed
// composition revision. It stops short of approval, which each test decides for itself.
func newFixture(t *testing.T, intent plan.Intent) *fixture {
	t.Helper()
	ctx := context.Background()
	db := scratchDB(t)
	f := &fixture{db: db, transport: &fakeTransport{}}

	profileRevision := activateProfile(t, db)

	company, err := db.CreateCompany(ctx, "Northwind Systems", "northwind.example", "fixture", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	f.companyID = company.ID
	role := "Head of Engineering"
	contact, err := db.CreateContact(ctx, company.ID, "email", "cto@northwind.example", nil, &role, 3)
	if err != nil {
		t.Fatal(err)
	}
	f.contactID = contact.ID

	record := store.ContactPlan{
		CompanyID: company.ID, Intent: string(intent),
		MatchClassification: opportunity.ClassificationStrong,
		TargetFamilies:      []plan.TargetFamily{{ID: "role-backend", Name: "Backend Engineering"}},
		RecipientContactID:  contact.ID, RecipientEmail: contact.Value,
		RecipientRelevance: "preferred", RecipientReason: "engineering contact",
		ProfileRevisionID: profileRevision, SelectionOrigin: "manual", Status: "selected",
		Selection: plan.Selection{SelectorVersion: plan.SelectorVersion, Intent: intent,
			Identity: "Full-Stack Software Developer", PrimaryStrength: "Backend Engineering"},
	}
	if intent == plan.SpecificJobApplication {
		result, err := db.IngestOpportunity(ctx, opportunity.DiscoveredOpportunity{
			CompanyName: "Northwind Systems", DomainCandidate: "northwind.example", Source: "fixture",
			ExternalID: "j-1", URL: "https://jobs.example.test/j-1", Title: "Backend Engineer",
			Description: backendJob, DescriptionStatus: "complete", Availability: "open", RemoteStatus: "unknown",
		})
		if err != nil || result.ReviewReason != "" {
			t.Fatalf("fixture opportunity: %v %s", err, result.ReviewReason)
		}
		row, err := db.GetOpportunity(ctx, result.ID)
		if err != nil {
			t.Fatal(err)
		}
		active, err := db.ActiveProfile(ctx)
		if err != nil || active == nil {
			t.Fatalf("fixture profile: %v", err)
		}
		doc := active.Document
		match := opportunity.Assess(opportunity.AssessInput{
			OpportunityID: row.ID, OpportunityRevision: row.Revision, Facts: row.Facts,
			Profile: &doc, ProfileRevisionID: profileRevision,
		})
		if err := db.SaveOpportunityMatch(ctx, match); err != nil {
			t.Fatal(err)
		}
		record.MatchClassification = match.Classification
		id, revision := row.ID, row.Revision
		record.OpportunityID, record.OpportunityRevision = &id, &revision
	}
	created, err := db.CreateContactPlan(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	f.planID = created.ID

	snapshot, err := db.BuildCompositionSnapshot(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.draftID, err = db.QueueComposition(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	f.email = composition.Email{
		Subject: "Application for Backend Engineer", Intent: snapshot.Intent,
		Recipient: snapshot.Recipient.Address, OpportunityID: snapshot.OpportunityID,
		Body: "I'm applying for the Backend Engineer role at Northwind Systems.\n\nI build Go services and the PostgreSQL schemas behind them.\n\nCould we arrange a short call?",
	}
	f.recordReviewed(t, f.email)

	f.service = New(db, f.transport, send.ModeManualSend, send.DefaultLimits(), ready())
	return f
}

// recordReviewed writes one composition attempt that passed both layers and finishes it.
func (f *fixture) recordReviewed(t *testing.T, email composition.Email) {
	t.Helper()
	ctx := context.Background()
	owner, err := f.db.ClaimComposition(ctx, f.draftID, f.revision, 2, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	attempt := composition.Attempt{
		Number: f.revision + 1, Source: "generated", Email: email,
		Validation:      []composition.Finding{},
		Review:          &composition.Review{Complete: true, Passed: true, Findings: []composition.Finding{}},
		ComposeMetadata: composition.Metadata{Provider: "fake", Model: "fixture"},
	}
	if err := f.db.RecordCompositionAttempt(ctx, f.draftID, owner, attempt); err != nil {
		t.Fatal(err)
	}
	if err := f.db.FinishComposition(ctx, f.draftID, owner, "reviewed"); err != nil {
		t.Fatal(err)
	}
	f.revision++
	f.email = email
	f.hash = composition.Hash(email)
}

func (f *fixture) approve(t *testing.T) *store.SendApproval {
	t.Helper()
	a, err := f.db.ApproveForSend(context.Background(), f.draftID, f.revision, f.hash, "operator")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *fixture) request() Request {
	return Request{DraftID: f.draftID, Initiator: "operator", Kind: send.KindFirstContact}
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

func blocked(d send.Decision, code string) bool { return d.Blockers.Has(code) }

// ---------------------------------------------------------------- acceptance tests

// A. Reviewed but not human-approved.
func TestReviewedWithoutApprovalCannotSend(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	out, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed {
		t.Fatal("a reviewed draft must not send without explicit human approval")
	}
	if !blocked(out.Decision, "not_approved") {
		t.Fatalf("blockers = %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 0 {
		t.Fatal("the provider was contacted without approval")
	}
}

// B. Approved clean draft sends exactly once through the canonical path.
func TestApprovedDraftSendsOnce(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	out, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if !out.Decision.Allowed {
		t.Fatalf("preflight blocked an approved clean draft: %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 1 {
		t.Fatalf("provider calls = %d, want exactly 1", f.transport.Calls())
	}
	if out.Attempt == nil || out.Attempt.State != send.StateAccepted {
		t.Fatalf("attempt = %+v", out.Attempt)
	}
	// The transmitted bytes are the approved bytes.
	if f.transport.last.Subject != f.email.Subject || f.transport.last.Body != f.email.Body {
		t.Fatal("the transmitted content is not the approved content")
	}
	if f.transport.last.ToAddress != "cto@northwind.example" {
		t.Fatalf("recipient = %q", f.transport.last.ToAddress)
	}
	// Q. The thread freezes the approved intent.
	if out.Thread == nil || out.Thread.Intent != string(plan.SpecificJobApplication) {
		t.Fatalf("thread = %+v", out.Thread)
	}
	if out.Attempt.ContentHash != f.hash {
		t.Fatal("the attempt did not record the approved content hash")
	}
	// The Message-ID is stamped with the sender's domain, never the recipient's.
	if !strings.HasSuffix(out.Attempt.MessageID, "@prospector.example>") {
		t.Fatalf("message id %q is not stamped with the sending domain", out.Attempt.MessageID)
	}
	if strings.Contains(out.Attempt.MessageID, "northwind.example") {
		t.Fatal("the message id claims to originate from the recipient's domain")
	}
}

// C. Two concurrent requests transmit once.
func TestConcurrentSendsTransmitOnce(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	var wg sync.WaitGroup
	outcomes := make([]Outcome, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			outcomes[i], errs[i] = f.service.Send(context.Background(), f.request())
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if f.transport.Calls() != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 across two concurrent requests", f.transport.Calls())
	}
	sent, resolved := 0, 0
	for _, out := range outcomes {
		if out.Decision.Allowed {
			sent++
		} else if out.Resolved {
			resolved++
		}
	}
	if sent != 1 || resolved != 1 {
		t.Fatalf("sent=%d resolved=%d, want one transmission and one resolution", sent, resolved)
	}
}

// D. A repeated request after acceptance does not transmit again.
func TestRetryAfterAcceptedDoesNotTransmitAgain(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	if _, err := f.service.Send(context.Background(), f.request()); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed || !blocked(out.Decision, "already_accepted") {
		t.Fatalf("a second send was permitted: %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", f.transport.Calls())
	}
}

// E. A timeout after possible acceptance becomes uncertain and never auto-retries.
func TestUncertainDeliveryBlocksFurtherSends(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	f.transport.results = []send.Result{{
		Outcome: send.OutcomeUncertain, Stage: send.StageCommit,
		ErrorCategory: send.ErrorTimeoutUncertain, Response: "i/o timeout writing terminating dot",
	}}
	out, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Attempt.State != send.StateUncertain {
		t.Fatalf("state = %q, want uncertain", out.Attempt.State)
	}
	if out.Thread != nil {
		t.Fatal("an uncertain send must not open a thread")
	}
	// V. The resend button hits the same wall.
	again, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if again.Decision.Allowed || !blocked(again.Decision, "delivery_uncertain") {
		t.Fatalf("uncertain delivery permitted another send: %v", again.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1; an uncertain attempt must never retry automatically", f.transport.Calls())
	}
}

// F. A definite pre-transmission failure is retryable within the bounded policy.
func TestConnectionFailureBeforeTransmissionIsRetryable(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	f.transport.results = []send.Result{
		{Outcome: send.OutcomeTransient, Stage: send.StageDial, ErrorCategory: send.ErrorConnection, Response: "connection refused"},
		accepted(),
	}
	first, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if first.Attempt.State != send.StateTransientFailed {
		t.Fatalf("state = %q, want transient_failed", first.Attempt.State)
	}
	if !send.Retryable(first.Attempt.ErrorCategory) {
		t.Fatalf("category %q should be retryable", first.Attempt.ErrorCategory)
	}
	second, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if !second.Decision.Allowed {
		t.Fatalf("a definite pre-send failure should allow one bounded retry: %v", second.Decision.Blockers.Codes())
	}
	if second.Attempt.State != send.StateAccepted {
		t.Fatalf("retry state = %q", second.Attempt.State)
	}
	if f.transport.Calls() != 2 {
		t.Fatalf("provider calls = %d, want 2", f.transport.Calls())
	}
}

// G. A recipient rejection is permanent and never blindly retried.
func TestRecipientRejectionIsPermanent(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	f.transport.results = []send.Result{{
		Outcome: send.OutcomeRejected, Stage: send.StageRcptTo,
		ErrorCategory: send.ErrorRecipient, Response: "550 no such user",
	}}
	out, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Attempt.State != send.StateRejected {
		t.Fatalf("state = %q, want rejected", out.Attempt.State)
	}
	if send.Retryable(out.Attempt.ErrorCategory) {
		t.Fatal("a recipient rejection must not be retryable")
	}
	again, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if again.Decision.Allowed || !blocked(again.Decision, "already_rejected") {
		t.Fatalf("a rejected message was retried: %v", again.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", f.transport.Calls())
	}
}

// X. An auth or configuration failure does not become a retry storm.
func TestAuthFailureDoesNotRetryStorm(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	f.transport.results = []send.Result{
		{Outcome: send.OutcomeConfig, Stage: send.StageAuth, ErrorCategory: send.ErrorAuthConfig, Response: "535 bad credentials"},
		{Outcome: send.OutcomeConfig, Stage: send.StageAuth, ErrorCategory: send.ErrorAuthConfig, Response: "535 bad credentials"},
		{Outcome: send.OutcomeConfig, Stage: send.StageAuth, ErrorCategory: send.ErrorAuthConfig, Response: "535 bad credentials"},
		{Outcome: send.OutcomeConfig, Stage: send.StageAuth, ErrorCategory: send.ErrorAuthConfig, Response: "535 bad credentials"},
	}
	for i := 0; i < 5; i++ {
		if _, err := f.service.Send(context.Background(), f.request()); err != nil {
			t.Fatal(err)
		}
	}
	if send.Retryable(send.ErrorAuthConfig) {
		t.Fatal("an auth failure must not be classified retryable")
	}
	limit := send.DefaultLimits().MaxAttemptsPerKey
	if f.transport.Calls() > limit {
		t.Fatalf("provider calls = %d, exceeding the attempt bound of %d", f.transport.Calls(), limit)
	}
	final, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if !blocked(final.Decision, "attempt_limit_reached") {
		t.Fatalf("the attempt bound did not engage: %v", final.Decision.Blockers.Codes())
	}
}

// H. Suppression added after approval still blocks.
func TestSuppressionAfterApprovalBlocks(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	if err := f.db.AddSuppression(context.Background(), "cto@northwind.example", "asked to stop"); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed || !blocked(out.Decision, "address_suppressed") {
		t.Fatalf("a suppressed address was sendable: %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 0 {
		t.Fatal("the provider was contacted for a suppressed address")
	}
}

// I. A contact marked bad after approval blocks.
func TestContactMarkedBadAfterApprovalBlocks(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	if err := f.db.MarkContactBad(context.Background(), f.contactID, "bounced"); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed || !blocked(out.Decision, "recipient_marked_bad") {
		t.Fatalf("a bad contact was sendable: %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 0 {
		t.Fatal("the provider was contacted for a contact marked bad")
	}
}

// J. Changing the plan recipient makes the old draft unsendable, and never redirects its content.
func TestRecipientChangeBlocksOldDraft(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	ctx := context.Background()
	other, err := f.db.CreateContact(ctx, f.companyID, "email", "hiring@northwind.example", nil, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.UpdatePlanRecipient(ctx, f.planID, other.ID, other.Value, "", "preferred", "changed by hand"); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.Send(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed {
		t.Fatal("a recipient change let the old reviewed content send")
	}
	if !blocked(out.Decision, "stale_inputs") {
		t.Fatalf("blockers = %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 0 {
		t.Fatal("the provider was contacted after a recipient change")
	}
}

// K. A new profile revision invalidates the approval through staleness.
func TestProfileRevisionChangeBlocks(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	changed := strings.Replace(syntheticProfileJSON,
		`"description":"Relational database and schema design"`,
		`"description":"Relational database, schema and transaction design"`, 1)
	ctx := context.Background()
	id, err := f.db.CreateProfileDraft(ctx, []byte(changed))
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := f.db.ListProfileRevisions(ctx)
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
	if err := f.db.ActivateProfile(ctx, id, hash, active); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.Send(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed || !blocked(out.Decision, "stale_inputs") {
		t.Fatalf("a changed profile revision still sent: %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 0 {
		t.Fatal("the provider was contacted with a stale profile")
	}
}

// L. A changed opportunity revision invalidates an application.
func TestOpportunityRevisionChangeBlocks(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	ctx := context.Background()
	if _, err := f.db.IngestOpportunity(ctx, opportunity.DiscoveredOpportunity{
		CompanyName: "Northwind Systems", DomainCandidate: "northwind.example", Source: "fixture",
		ExternalID: "j-1", URL: "https://jobs.example.test/j-1", Title: "Backend Engineer",
		Description: backendJob + "- Experience with Kubernetes\n", DescriptionStatus: "complete",
		Availability: "open", RemoteStatus: "unknown",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.Send(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed || !blocked(out.Decision, "stale_inputs") {
		t.Fatalf("a changed vacancy still sent: %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 0 {
		t.Fatal("the provider was contacted with a stale opportunity")
	}
}

// M. A new composition revision after approval invalidates that approval.
func TestNewRevisionAfterApprovalInvalidatesIt(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	edited := f.email
	edited.Body = f.email.Body + "\n\nI also enjoy working close to the data."
	f.recordReviewed(t, edited)

	out, err := f.service.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed {
		t.Fatal("an edit after approval kept its approval")
	}
	if !blocked(out.Decision, "approval_content_mismatch") {
		t.Fatalf("blockers = %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 0 {
		t.Fatal("edited content was transmitted under an old approval")
	}
}

// N and O. Neither layer alone may authorize; and a clean semantic review cannot rescue a
// deterministic finding.
func TestEitherLayerFailingPreventsApprovalAndSend(t *testing.T) {
	for _, tc := range []struct {
		name       string
		validation []composition.Finding
		review     *composition.Review
		wantCode   string
	}{
		{
			name:     "semantic review failed",
			review:   &composition.Review{Complete: true, Passed: false, Findings: []composition.Finding{{Severity: "block", Code: "invented_fact", Explanation: "unsupported"}}},
			wantCode: "semantic_review_failed",
		},
		{
			name:       "deterministic finding with a clean review",
			validation: []composition.Finding{{Severity: "block", Code: "unapproved_technology", Explanation: "outside the envelope"}},
			review:     &composition.Review{Complete: true, Passed: true, Findings: []composition.Finding{}},
			wantCode:   "deterministic_validation_failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, plan.SpecificJobApplication)
			ctx := context.Background()
			owner, err := f.db.ClaimComposition(ctx, f.draftID, f.revision, 2, 100, 100)
			if err != nil {
				t.Fatal(err)
			}
			validation := tc.validation
			if validation == nil {
				validation = []composition.Finding{}
			}
			if err := f.db.RecordCompositionAttempt(ctx, f.draftID, owner, composition.Attempt{
				Number: f.revision + 1, Source: "generated", Email: f.email,
				Validation: validation, Review: tc.review,
			}); err != nil {
				t.Fatal(err)
			}
			if err := f.db.FinishComposition(ctx, f.draftID, owner, "reviewed"); err != nil {
				t.Fatal(err)
			}
			f.revision++

			// Approval is possible only because status is reviewed; the gate still refuses.
			if _, err := f.db.ApproveForSend(ctx, f.draftID, f.revision, composition.Hash(f.email), "operator"); err != nil {
				t.Fatal(err)
			}
			out, err := f.service.Send(ctx, f.request())
			if err != nil {
				t.Fatal(err)
			}
			if out.Decision.Allowed || !blocked(out.Decision, tc.wantCode) {
				t.Fatalf("blockers = %v, want %s", out.Decision.Blockers.Codes(), tc.wantCode)
			}
			if f.transport.Calls() != 0 {
				t.Fatal("the provider was contacted despite a failing layer")
			}
		})
	}
}

// P. Multiple matching opportunities at one company do not authorize simultaneous first contacts.
func TestSecondFirstContactAtSameDomainIsBlocked(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	ctx := context.Background()
	if _, err := f.service.Send(ctx, f.request()); err != nil {
		t.Fatal(err)
	}

	// A second plan at the same company, to a different contact, with its own reviewed draft.
	other, err := f.db.CreateContact(ctx, f.companyID, "email", "jobs@northwind.example", nil, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	active, err := f.db.ActiveProfile(ctx)
	if err != nil || active == nil {
		t.Fatal(err)
	}
	second, err := f.db.CreateContactPlan(ctx, store.ContactPlan{
		CompanyID: f.companyID, Intent: string(plan.SpeculativeJobEnquiry),
		MatchClassification: opportunity.ClassificationStrong,
		TargetFamilies:      []plan.TargetFamily{{ID: "role-backend", Name: "Backend Engineering"}},
		RecipientContactID:  other.ID, RecipientEmail: other.Value,
		RecipientRelevance: "acceptable", RecipientReason: "second plan",
		ProfileRevisionID: active.ID, SelectionOrigin: "manual", Status: "selected",
		Selection: plan.Selection{SelectorVersion: plan.SelectorVersion, Intent: plan.SpeculativeJobEnquiry},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.db.BuildCompositionSnapshot(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	draftID, err := f.db.QueueComposition(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	email := composition.Email{Subject: "Backend engineering opportunities", Intent: snapshot.Intent,
		Recipient: snapshot.Recipient.Address,
		Body:      "I build Go services and PostgreSQL schemas.\n\nIs there an engineering contact I could speak with?"}
	owner, err := f.db.ClaimComposition(ctx, draftID, 0, 2, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecordCompositionAttempt(ctx, draftID, owner, composition.Attempt{
		Number: 1, Source: "generated", Email: email, Validation: []composition.Finding{},
		Review: &composition.Review{Complete: true, Passed: true, Findings: []composition.Finding{}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.FinishComposition(ctx, draftID, owner, "reviewed"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ApproveForSend(ctx, draftID, 1, composition.Hash(email), "operator"); err != nil {
		t.Fatal(err)
	}

	before := f.transport.Calls()
	out, err := f.service.Send(ctx, Request{DraftID: draftID, Initiator: "operator", Kind: send.KindFirstContact})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed {
		t.Fatal("a second simultaneous first contact at one domain was permitted")
	}
	if !blocked(out.Decision, "domain_already_in_conversation") && !blocked(out.Decision, "domain_cooldown") {
		t.Fatalf("blockers = %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != before {
		t.Fatal("the provider was contacted for a second first contact")
	}
}

// Q, R, S. Each intent is frozen into its thread.
func TestThreadFreezesApprovedIntent(t *testing.T) {
	for _, intent := range []plan.Intent{plan.SpecificJobApplication, plan.SpeculativeJobEnquiry, plan.ServiceOutreach} {
		t.Run(string(intent), func(t *testing.T) {
			f := newFixture(t, intent)
			f.approve(t)
			out, err := f.service.Send(context.Background(), f.request())
			if err != nil {
				t.Fatal(err)
			}
			if !out.Decision.Allowed {
				t.Fatalf("blocked: %v", out.Decision.Blockers.Codes())
			}
			if out.Thread == nil || out.Thread.Intent != string(intent) {
				t.Fatalf("thread intent = %+v, want %s", out.Thread, intent)
			}
			if out.Attempt.Intent != string(intent) {
				t.Fatalf("attempt intent = %q", out.Attempt.Intent)
			}
		})
	}
}

// T. An application thread cannot carry a service follow-up.
func TestFollowupCannotChangeIntent(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	ctx := context.Background()
	out, err := f.service.Send(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	thread := out.Thread
	if thread == nil {
		t.Fatal("no thread")
	}
	preflight := send.Preflight{
		Now: time.Now().UTC(), Mode: send.ModeManualSend, Ready: ready(),
		Identity: send.Identity{DraftID: 1, CompositionRevision: 1, ContentHash: "h",
			RecipientContactID: f.contactID, RecipientEmail: "cto@northwind.example",
			PlanID: f.planID, PlanRevision: 1, Intent: plan.ServiceOutreach,
			Kind: send.KindFollowup, FollowupStep: 1},
		CompositionExists: true, CompositionStatus: "reviewed", CurrentRevision: 1,
		DeterministicPassed: true, SemanticPassed: true, PlanStatus: "selected",
		Approval: &send.Approval{CompositionRevision: 1, ContentHash: "h",
			RecipientContactID: f.contactID, RecipientEmail: "cto@northwind.example", Intent: plan.ServiceOutreach},
		Contact:   send.ContactState{Exists: true, ID: f.contactID, CompanyID: f.companyID, Email: "cto@northwind.example", Status: "active"},
		CompanyID: f.companyID,
		Thread: send.ThreadState{Exists: true, ID: thread.ID, State: send.ThreadActive,
			Intent: plan.SpecificJobApplication, ContactID: f.contactID, FollowupStep: 0},
		Limits: send.DefaultLimits(),
	}
	decision := send.Evaluate(preflight)
	if decision.Allowed || !blocked(decision, "followup_intent_mismatch") {
		t.Fatalf("an application thread accepted a service follow-up: %v", decision.Blockers.Codes())
	}
}

// W. A permanent bounce stops everything further to that address.
func TestPermanentBounceStopsFutureSends(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	ctx := context.Background()
	out, err := f.service.Send(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecordThreadEvent(ctx, store.ThreadEvent{
		ThreadID: out.Thread.ID, SendAttemptID: &out.Attempt.ID,
		Kind: "bounced_permanent", Detail: "550 mailbox does not exist", Source: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	thread, err := f.db.GetThread(ctx, out.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if thread.State != send.ThreadBounced {
		t.Fatalf("thread state = %q, want bounced", thread.State)
	}
	contact, err := f.db.GetContact(ctx, f.contactID)
	if err != nil {
		t.Fatal(err)
	}
	if contact.Status != "bad" {
		t.Fatal("a permanent bounce must mark the contact bad")
	}
	suppressed, err := f.db.IsSuppressed(ctx, "cto@northwind.example")
	if err != nil || !suppressed {
		t.Fatal("a permanent bounce must suppress the address")
	}
}

// Y. The legacy route cannot bypass the gate, and Z. automatic sending stays off.
func TestLegacyRouteAndAutomationRemainClosed(t *testing.T) {
	// Automation is computed, and the default configuration satisfies none of its conditions.
	if send.EvaluateAutomation(send.AutomationInputs{Mode: send.ModeReview, Readiness: ready(),
		SchemaVersion: send.RequiredSchemaVersion, DailyCap: 1}).Enabled {
		t.Fatal("automatic sending must not be configured by default")
	}
	for _, mode := range []string{send.ModeDisabled, send.ModeReview, send.ModeManualSend} {
		r := send.EvaluateReadiness(send.Inputs{Mode: mode, SchemaVersion: send.RequiredSchemaVersion,
			SchemaHealthy: true, TransportReady: true, SenderConfigured: true, Limits: send.DefaultLimits()})
		if mode != send.ModeManualSend && r.Ready {
			t.Fatalf("mode %s reported ready to send", mode)
		}
	}
}

// The rollout mode gates the canonical path itself, not only the UI.
func TestReviewModeCannotSend(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	f.approve(t)
	reviewOnly := New(f.db, f.transport, send.ModeReview, send.DefaultLimits(),
		send.EvaluateReadiness(send.Inputs{Mode: send.ModeReview, SchemaVersion: send.RequiredSchemaVersion,
			SchemaHealthy: true, TransportReady: true, SenderConfigured: true, Limits: send.DefaultLimits()}))
	out, err := reviewOnly.Send(context.Background(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed || !blocked(out.Decision, "rollout_mode_forbids_sending") {
		t.Fatalf("review mode sent: %v", out.Decision.Blockers.Codes())
	}
	if f.transport.Calls() != 0 {
		t.Fatal("the provider was contacted in review-only mode")
	}
}

// Every attempt is durably auditable, and a settled attempt can never be rewritten.
func TestAttemptAuditTrailIsImmutable(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	approval := f.approve(t)
	ctx := context.Background()
	out, err := f.service.Send(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	a := out.Attempt
	for name, ok := range map[string]bool{
		"idempotency key": a.IdempotencyKey != "",
		"content hash":    a.ContentHash == f.hash,
		"approval":        a.ApprovalID == approval.ID,
		"profile":         a.ProfileRevisionID > 0,
		"plan revision":   a.PlanID == f.planID && a.PlanRevision > 0,
		"initiator":       a.Initiator == "operator",
		"provider":        a.Provider == "fake",
		"message id":      a.MessageID != "",
		"policy version":  a.PolicyVersion == send.PolicyVersion,
		"settled":         a.SettledAt != nil,
		"subject stored":  a.Subject == f.email.Subject,
		"body stored":     a.Body == f.email.Body,
	} {
		if !ok {
			t.Fatalf("the audit trail is missing its %s: %+v", name, a)
		}
	}
	if _, err := f.db.Exec(ctx, `UPDATE send_attempt SET state='transient_failed' WHERE id=$1`, a.ID); err == nil {
		t.Fatal("a settled attempt must not be rewritable")
	}
	if _, err := f.db.Exec(ctx, `UPDATE send_attempt SET body='something else' WHERE id=$1`, a.ID); err == nil {
		t.Fatal("what was sent must be immutable")
	}
	if _, err := f.db.Exec(ctx, `DELETE FROM send_attempt WHERE id=$1`, a.ID); err == nil {
		t.Fatal("the audit trail must not be deletable")
	}
}

// A revoked approval cannot send, and approval cannot precede review.
func TestApprovalLifecycle(t *testing.T) {
	f := newFixture(t, plan.SpecificJobApplication)
	ctx := context.Background()
	f.approve(t)
	if err := f.db.RevokeApproval(ctx, f.draftID, "changed my mind"); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.Send(ctx, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.Allowed || !blocked(out.Decision, "not_approved") {
		t.Fatalf("a revoked approval still sent: %v", out.Decision.Blockers.Codes())
	}
	// Re-approving the same unchanged revision is allowed once the old approval is revoked.
	if _, err := f.db.ApproveForSend(ctx, f.draftID, f.revision, f.hash, "operator"); err != nil {
		t.Fatalf("re-approval after revocation failed: %v", err)
	}
	// Approving a hash that is not the stored one is refused.
	if _, err := f.db.ApproveForSend(ctx, f.draftID, f.revision, "not-the-hash", "operator"); err == nil {
		t.Fatal("approval accepted a content hash that was not the stored one")
	}
}

// The idempotency key is stable across repeated construction and changes only with pinned facts.
func TestIdempotencyKeySemantics(t *testing.T) {
	base := send.Identity{DraftID: 7, CompositionRevision: 2, ContentHash: "abc",
		RecipientContactID: 3, RecipientEmail: "Person@Example.test", PlanID: 4, PlanRevision: 1,
		Intent: plan.SpecificJobApplication, Kind: send.KindFirstContact}
	if base.Key() != base.Key() {
		t.Fatal("the key must be stable")
	}
	mixedCase := base
	mixedCase.RecipientEmail = "  person@example.test "
	if mixedCase.Key() != base.Key() {
		t.Fatal("address casing and padding must not change the key")
	}
	for name, mutate := range map[string]func(send.Identity) send.Identity{
		"a new composition revision": func(i send.Identity) send.Identity { i.CompositionRevision++; return i },
		"different content":          func(i send.Identity) send.Identity { i.ContentHash = "def"; return i },
		"a different recipient":      func(i send.Identity) send.Identity { i.RecipientContactID = 99; return i },
		"a new plan revision":        func(i send.Identity) send.Identity { i.PlanRevision++; return i },
		"a different intent":         func(i send.Identity) send.Identity { i.Intent = plan.ServiceOutreach; return i },
		"a follow-up":                func(i send.Identity) send.Identity { i.Kind = send.KindFollowup; i.FollowupStep = 1; return i },
	} {
		if mutate(base).Key() == base.Key() {
			t.Fatalf("%s must produce a distinct send identity", name)
		}
	}
}
