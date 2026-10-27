package seed

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/redis/go-redis/v9"
)

// The fixtures below follow the shape each source publishes. They are not live responses: the
// test proves the path from a source payload to stored, classified opportunities, and that a
// failing feed is isolated.
const wwrFixture = `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>We Work Remotely</title>
<item><title>Acme Labs: Senior Go Engineer</title><region>Anywhere in the World</region><country></country><type>Contract</type><category>Back-End Programming</category>
<description>&lt;p&gt;Build REST APIs in Go and PostgreSQL.&lt;/p&gt;</description><pubDate>Mon, 26 Oct 2026 10:00:00 +0000</pubDate><expires_at>2026-12-01 00:00:00 UTC</expires_at>
<guid>https://weworkremotely.com/remote-jobs/acme-labs-senior-go-engineer</guid><link>https://weworkremotely.com/remote-jobs/acme-labs-senior-go-engineer</link></item>
<item><title>Statewide Corp: Backend Developer</title><region>USA Only</region><type>Full-Time</type><description>Python and Django.</description><pubDate>Mon, 26 Oct 2026 09:00:00 +0000</pubDate><link>https://weworkremotely.com/remote-jobs/statewide-backend</link></item>
<item><title>Untitled posting without employer</title><description>No company stated.</description><link>https://weworkremotely.com/remote-jobs/untitled</link></item>
</channel></rss>`

func sourcesTransport(t *testing.T) fixtureTransport {
	return func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "weworkremotely.com" && strings.Contains(r.URL.Path, "back-end"):
			return response(r, 200, wwrFixture), nil
		case r.URL.Host == "weworkremotely.com":
			// Every other feed is down: the run must still keep the back-end feed's listings.
			return response(r, 503, "unavailable"), nil
		case r.URL.Host == "remoteok.com":
			return response(r, 200, `[{"legal":"notice"},{"id":"ro-1","company":"Brightline Studio","position":"Full-Stack Developer","location":"EMEA","description":"<p>React and Django for client projects.</p>","url":"https://remoteok.com/remote-jobs/ro-1","apply_url":"https://jobs.lever.co/brightline/abc-123/apply","company_url":"https://brightline.example"}]`), nil
		case r.URL.Host == "api.lever.co" && r.URL.Path == "/v0/postings/brightline":
			return response(r, 200, `[{"id":"lv-1","text":"Backend Engineer (Contract)","hostedUrl":"https://jobs.lever.co/brightline/lv-1","applyUrl":"https://jobs.lever.co/brightline/lv-1/apply","descriptionPlain":"Go services for agency clients.","createdAt":1761472800000,"workplaceType":"remote","categories":{"location":"Remote - Worldwide","commitment":"Contract"}},{"id":"lv-2","text":"Account Executive","hostedUrl":"https://jobs.lever.co/brightline/lv-2","categories":{"location":"London"}}]`), nil
		case r.URL.Host == "boards-api.greenhouse.io" && r.URL.Path == "/v1/boards/configured/jobs":
			return response(r, 200, `{"jobs":[{"id":901,"title":"Software Engineer, Payments","absolute_url":"https://job-boards.greenhouse.io/configured/jobs/901","updated_at":"2026-10-20T12:00:00Z","location":{"name":"Remote, US"},"content":"&lt;p&gt;Must be authorized to work in the United States.&lt;/p&gt;"}]}`), nil
		case r.URL.Host == "boards-api.greenhouse.io" && r.URL.Path == "/v1/boards/configured":
			return response(r, 200, `{"name":"Configured Inc"}`), nil
		case r.URL.Host == "jobicy.com":
			return response(r, 200, `{"jobs":[{"id":555,"url":"https://jobicy.com/jobs/555-python-developer","jobTitle":"Python Developer","companyName":"Nimbus Data","jobGeo":"Anywhere","jobType":["full-time"],"jobDescription":"<p>FastAPI services.</p>","pubDate":"2026-10-25 08:00:00"}]}`), nil
		case r.URL.Host == "www.workingnomads.com":
			return response(r, 200, `[{"url":"https://www.workingnomads.com/jobs/django-dev","title":"Django Developer","description":"<p>Django and PostgreSQL.</p>","company_name":"Harbor Web","category_name":"Development","location":"Europe","pub_date":"2026-10-24T00:00:00Z"},{"url":"https://www.workingnomads.com/jobs/sales","title":"Sales Manager","company_name":"Harbor Web","category_name":"Sales"}]`), nil
		}
		t.Logf("unexpected request %s", r.URL)
		return response(r, 404, "not found"), nil
	}
}

func TestInternationalSourcesIngestClassifyAndStayIsolated(t *testing.T) {
	db := seedFixtureDB(t)
	ctx := context.Background()
	cache := redis.NewClient(&redis.Options{Addr: "unused.invalid:6379", MaxRetries: -1})
	defer cache.Close()
	transport := sourcesTransport(t)

	wwr := NewWeWorkRemotelySeeder("synthetic")
	remote := NewRemoteOKSeeder("synthetic")
	jobicy := NewJobicySeeder("synthetic")
	nomads := NewWorkingNomadsSeeder("synthetic")
	ats := NewATSSeeder("synthetic")
	ats.configured = ParseATSBoards("greenhouse:configured, bogus:thing")
	ats.pause = 0
	for _, base := range []*BaseSeeder{wwr.BaseSeeder, remote.BaseSeeder, jobicy.BaseSeeder, nomads.BaseSeeder, ats.BaseSeeder} {
		base.client = &http.Client{Transport: transport}
		base.retryDelay = 0
	}

	// Three of four WWR feeds fail; the fourth still lands, and the unattributed item is held for
	// review rather than failing the run.
	added, err := wwr.Seed(ctx, db, cache)
	if err != nil || added != 2 {
		t.Fatalf("wwr: added=%d err=%v", added, err)
	}
	for _, s := range []Seeder{remote, jobicy, nomads} {
		if added, err := s.Seed(ctx, db, cache); err != nil || added != 1 {
			t.Fatalf("%s: added=%d err=%v", s.Name(), added, err)
		}
	}
	// The Lever board is found through the Remote OK listing's apply link; the configured
	// Greenhouse board is read too. Only engineering roles are kept.
	boards := ats.Boards(ctx, db)
	if len(boards) != 2 {
		t.Fatalf("expected configured + discovered boards, got %+v", boards)
	}
	if added, err := ats.Seed(ctx, db, cache); err != nil || added != 2 {
		t.Fatalf("ats: added=%d err=%v", added, err)
	}
	// Repeating every source creates nothing new.
	for _, s := range []Seeder{wwr, remote, jobicy, nomads, ats} {
		if added, err := s.Seed(ctx, db, cache); err != nil || added != 0 {
			t.Fatalf("repeat %s: added=%d err=%v", s.Name(), added, err)
		}
	}

	type row struct {
		source, title, company, location, kenya, route, cost string
		contract                                             bool
	}
	rows, err := db.Query(ctx, `SELECT o.source,o.title,c.name,o.location,a.kenya_access,a.route,a.cost,a.contract FROM opportunity o JOIN company c ON c.id=o.company_id JOIN opportunity_access a ON a.opportunity_id=o.id`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.source, &r.title, &r.company, &r.location, &r.kenya, &r.route, &r.cost, &r.contract); err != nil {
			t.Fatal(err)
		}
		got[r.title] = r
	}
	rows.Close()
	want := map[string]row{
		"Senior Go Engineer":          {source: "weworkremotely", company: "Acme Labs", location: "Anywhere in the World", kenya: opportunity.KenyaOpen, route: opportunity.RouteJobBoard, cost: opportunity.CostUnknown, contract: true},
		"Backend Developer":           {source: "weworkremotely", company: "Statewide Corp", location: "USA Only", kenya: opportunity.KenyaRestricted, route: opportunity.RouteJobBoard, cost: opportunity.CostUnknown},
		"Full-Stack Developer":        {source: "remoteok", company: "Brightline Studio", location: "EMEA", kenya: opportunity.KenyaLikely, route: opportunity.RouteEmployerATS, cost: opportunity.CostFree},
		"Backend Engineer (Contract)": {source: "lever", company: "Brightline Studio", location: "Remote - Worldwide", kenya: opportunity.KenyaOpen, route: opportunity.RouteEmployerATS, cost: opportunity.CostFree, contract: true},
		"Software Engineer, Payments": {source: "greenhouse", company: "Configured Inc", location: "Remote, US", kenya: opportunity.KenyaRestricted, route: opportunity.RouteEmployerATS, cost: opportunity.CostFree},
		"Python Developer":            {source: "jobicy", company: "Nimbus Data", location: "Anywhere", kenya: opportunity.KenyaOpen, route: opportunity.RouteJobBoard, cost: opportunity.CostUnknown},
		"Django Developer":            {source: "workingnomads", company: "Harbor Web", location: "Europe", kenya: opportunity.KenyaRestricted, route: opportunity.RouteJobBoard, cost: opportunity.CostUnknown},
	}
	if len(got) != len(want) {
		t.Errorf("stored %d classified listings, want %d: %+v", len(got), len(want), got)
	}
	for title, w := range want {
		g, ok := got[title]
		if !ok {
			t.Errorf("missing %q", title)
			continue
		}
		w.title = title
		if g != w {
			t.Errorf("%q:\n got  %+v\n want %+v", title, g, w)
		}
	}
	// The ATS listing joined the company the Remote OK listing created, not a near-duplicate.
	var companies int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM company WHERE name ILIKE 'brightline%'`).Scan(&companies); err != nil || companies != 1 {
		t.Fatalf("brightline companies=%d err=%v", companies, err)
	}
	var held int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ingestion_review WHERE source='weworkremotely' AND reason='missing_employer'`).Scan(&held); err != nil || held != 1 {
		t.Fatalf("unattributed WWR item held=%d err=%v", held, err)
	}
	// The expiry date WWR publishes is stored, so the suppression sweep can retire the listing.
	var expires int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM opportunity WHERE source='weworkremotely' AND expires_at IS NOT NULL`).Scan(&expires); err != nil || expires != 1 {
		t.Fatalf("wwr expires_at stored on %d rows, err=%v", expires, err)
	}
}

func TestEverySourceFailingIsReportedAsAnError(t *testing.T) {
	wwr := NewWeWorkRemotelySeeder("synthetic")
	wwr.retryDelay = 0
	calls := 0
	wwr.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return response(r, 403, "blocked"), nil
	})}
	added, err := wwr.Seed(context.Background(), nil, nil)
	if err == nil || added != 0 || !strings.Contains(err.Error(), "403") {
		t.Fatalf("a fully blocked source must report its refusal: added=%d err=%v", added, err)
	}
	if calls != len(weWorkRemotelyFeeds) {
		t.Fatalf("a 403 must not be retried: %d calls for %d feeds", calls, len(weWorkRemotelyFeeds))
	}
}

func TestTransientFailuresAreRetriedOnce(t *testing.T) {
	b := NewBaseSeeder("synthetic")
	b.retryDelay = 0
	calls := 0
	b.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(r, 503, "busy"), nil
		}
		return response(r, 200, "ok"), nil
	})}
	raw, err := b.fetchListing(context.Background(), "https://example.test/feed")
	if err != nil || string(raw) != "ok" || calls != 2 {
		t.Fatalf("retry: raw=%q err=%v calls=%d", raw, err, calls)
	}
}

func TestBoardFromURL(t *testing.T) {
	cases := map[string]string{
		"https://jobs.lever.co/brightline/abc/apply":         "lever:brightline",
		"https://boards.greenhouse.io/gitlab/jobs/123":       "greenhouse:gitlab",
		"https://job-boards.greenhouse.io/configured/jobs/9": "greenhouse:configured",
		"https://jobs.ashbyhq.com/example/1d2e3f":            "ashby:example",
		"https://remoteok.com/remote-jobs/123":               "",
		"https://jobs.lever.co.evil.example/brightline":      "",
	}
	for link, want := range cases {
		b, ok := BoardFromURL(link)
		got := ""
		if ok {
			got = b.Provider + ":" + b.Slug
		}
		if got != want {
			t.Errorf("%s: got %q want %q", link, got, want)
		}
	}
}
