package seed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

type seenCacheHook struct{ Calls int }

func (h *seenCacheHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *seenCacheHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		h.Calls++
		switch typed := cmd.(type) {
		case *redis.BoolCmd:
			typed.SetVal(true)
		case *redis.StringCmd:
			typed.SetVal("1")
		case *redis.IntCmd:
			typed.SetVal(1)
		}
		return nil
	}
}
func (h *seenCacheHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if err := h.ProcessHook(nil)(ctx, cmd); err != nil {
				return err
			}
		}
		return nil
	}
}

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: r, Header: make(http.Header)}
}

func TestBoundedDescriptionAndFailedDetails(t *testing.T) {
	if got := descriptionText(`<nav>ignore</nav><p>First café.</p><script>ignore</script><p>Second.</p>`); got != "First café.\n\nSecond." {
		t.Fatal(got)
	}
	b := NewBaseSeeder("synthetic-test")
	b.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) { return response(r, 503, "unavailable"), nil })}
	d := opportunity.DiscoveredOpportunity{URL: "https://jobs.example/42", Description: "Original card", DescriptionStatus: "partial", Metadata: map[string]string{}}
	got := b.completeJobDescription(context.Background(), d, "jobs.example")
	if got.Description != d.Description || got.DescriptionStatus != "partial" || got.Metadata["detail_warning"] != "detail_fetch_failed" {
		t.Fatal("lost partial listing")
	}
	b.client.Transport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
		return response(r, 200, strings.Repeat("x", maxSourceBytes+1)), nil
	})
	if _, err := b.fetchSource(context.Background(), d.URL); err == nil {
		t.Fatal("unbounded HTTP response")
	}
}

func seedFixtureDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := os.Getenv("MIGRATION_TEST_DATABASE_URL")
	if path := os.Getenv("MIGRATION_TEST_ENV_FILE"); path != "" {
		vars, err := godotenv.Read(path)
		if err != nil {
			t.Fatal("test connection file unavailable")
		}
		dsn = vars["DATABASE_URL"]
	}
	if dsn == "" {
		t.Skip("isolated PostgreSQL fixture tests require MIGRATION_TEST_ENV_FILE")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("test server unavailable (DSN withheld)")
	}
	bytes := make([]byte, 8)
	if _, err = rand.Read(bytes); err != nil {
		t.Fatal(err)
	}
	name := "prospector_test_" + hex.EncodeToString(bytes)
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE template0"); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	cfg := admin.Config().Copy()
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal("scratch unavailable")
	}
	if err = store.ApplyMigrations(ctx, conn); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	conn.Close(ctx)
	// ConnString retains the originally parsed DSN even after cfg.Database changes.
	// Build the scratch DSN explicitly and assert the destination before any writes.
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("fixture test requires a PostgreSQL URL")
	}
	u.Path = "/" + name
	u.RawPath = ""
	query := u.Query()
	query.Del("dbname")
	query.Del("database")
	u.RawQuery = query.Encode()
	db, err := store.NewDB(ctx, u.String())
	if err != nil {
		t.Fatal("scratch pool unavailable")
	}
	t.Cleanup(db.Close)
	var actual string
	if err := db.QueryRow(ctx, `SELECT current_database()`).Scan(&actual); err != nil || actual != name {
		t.Fatal("scratch destination verification failed; no fixture writes permitted")
	}
	return db
}

func TestAllAdaptersRetainJobsWithoutRedisCompanyGate(t *testing.T) {
	db := seedFixtureDB(t)
	ctx := context.Background()
	cache := redis.NewClient(&redis.Options{Addr: "unused.invalid:6379", MaxRetries: -1})
	defer cache.Close()
	seen := &seenCacheHook{}
	cache.AddHook(seen) // Every discovery lookup would report already seen; no network is used.
	company, err := db.CreateCompany(ctx, "Synthetic Employer", "synthetic.example", "manual", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Hostname() {
		case "remoteok.com":
			return response(r, 200, `[{"id":"r1","company":"Synthetic Employer","position":"Marketing Manager","description":"<p>Marketing source description.</p>","url":"https://remoteok.com/jobs/r1"},{"id":"r2","company":"Synthetic Employer","position":"Full-Stack Developer","description":"Backend and frontend requirements","url":"https://remoteok.com/jobs/r2"}]`), nil
		case "remotive.com":
			return response(r, 200, `{"jobs":[{"id":123,"company_name":"Synthetic Employer","title":"Finance Officer","description":"Source finance description","url":"https://remotive.com/jobs/123"}]}`), nil
		case "himalayas.app":
			return response(r, 200, `{"jobs":[{"companyName":"Synthetic Employer","title":"Backend Engineer","excerpt":"Partial engineering requirements","slug":"synthetic-job"}]}`), nil
		case "weworkremotely.com":
			return response(r, 200, `<rss><channel><item><title>Synthetic Employer: Marketing Manager</title><link>https://weworkremotely.com/jobs/123</link><description>Source role description</description></item></channel></rss>`), nil
		default:
			if r.URL.Path == "/jobs" {
				return response(r, 200, `<div data-cy="listing-cards-components"><a href="/company/synthetic">Synthetic Employer</a><a data-cy="listing-title-link" href="/jobs/marketing-123">Marketing Manager</a><p>Card description</p></div><li class="job-list-li"><ul><li class="job-logo"><a><img alt="Synthetic Employer"></a></li><li class="job-info"><h2><a href="/job/finance-123">Finance Officer</a></h2></li></ul></li>`), nil
			}
			return response(r, 503, "detail unavailable"), nil
		}
	})
	remote := NewRemoteOKSeeder("synthetic")
	remotive := NewRemotiveSeeder("synthetic")
	himalayas := NewHimalayasSeeder("synthetic")
	wwr := NewWeWorkRemotelySeeder("synthetic")
	kenya := NewJobBoardSeeder("synthetic")
	for _, base := range []*BaseSeeder{remote.BaseSeeder, remotive.BaseSeeder, himalayas.BaseSeeder, wwr.BaseSeeder, kenya.BaseSeeder} {
		base.client = &http.Client{Transport: transport}
	}
	for _, adapter := range []Seeder{remote, remotive, himalayas, wwr, kenya} {
		added, err := adapter.Seed(ctx, db, cache)
		if err != nil || added < 1 {
			t.Fatalf("%s: added=%d error=%v", adapter.Name(), added, err)
		}
		added, err = adapter.Seed(ctx, db, cache)
		if err != nil || added != 0 {
			t.Fatalf("repeat %s duplicated: %d %v", adapter.Name(), added, err)
		}
	}
	if seen.Calls != 0 {
		t.Fatal("job ingestion consulted Redis discovery state")
	}
	jobs, err := db.ListOpportunities(ctx, "", company.ID)
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]bool{}
	for _, job := range jobs {
		if job.Revision != 1 {
			t.Fatalf("identical observation created revision %d for source %s", job.Revision, job.Facts.Source)
		}
		sources[job.Facts.Source] = true
		if job.CompanyID != company.ID {
			t.Fatal("employer duplicated")
		}
		if job.Facts.Title == "Marketing Manager" && job.Facts.NormalizedTitle != "marketing manager" {
			t.Fatal("unrelated title rewritten")
		}
	}
	for _, source := range []string{"remoteok", "remotive", "himalayas", "weworkremotely", "brightermonday.co.ke", "myjobmag.co.ke"} {
		if !sources[source] {
			t.Errorf("missing adapter source %s", source)
		}
	}
	var signals int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM signal WHERE company_id=$1`, company.ID).Scan(&signals); err != nil || signals != 0 {
		t.Fatal("new jobs still canonical hiring signals", err)
	}
}
