package seed

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// The extra feeds widen coverage without duplicating: a listing two feeds both return is stored
// once, a listing only the new feed carries is added, and one feed failing keeps the others.
func TestExtraFeedsWidenCoverageWithoutDuplicates(t *testing.T) {
	db := seedFixtureDB(t)
	ctx := context.Background()
	cache := redis.NewClient(&redis.Options{Addr: "unused.invalid:6379", MaxRetries: -1})
	defer cache.Close()
	shared := `{"id":7001,"company_name":"Nimbus Data","title":"Senior Platform Engineer","url":"https://remotive.com/remote-jobs/7001","description":"<p>Go services on Kubernetes.</p>","candidate_required_location":"Worldwide"}`
	remotive := NewRemotiveSeeder("synthetic")
	remotive.retryDelay = 0
	remotive.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Query().Get("category") {
		case "software-dev":
			return response(r, 200, `{"jobs":[`+shared+`]}`), nil
		case "devops":
			return response(r, 200, `{"jobs":[`+shared+`,{"id":7002,"company_name":"Tundra Payments","title":"Site Reliability Engineer","url":"https://remotive.com/remote-jobs/7002","description":"<p>Linux, Docker, Go.</p>"}]}`), nil
		}
		return response(r, 404, ""), nil
	})}
	if added, err := remotive.Seed(ctx, db, cache); err != nil || added != 2 {
		t.Fatalf("remotive: added=%d err=%v (want the shared listing once plus the devops-only one)", added, err)
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM opportunity WHERE source='remotive'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("stored %d remotive rows, want 2", n)
	}

	jobicy := NewJobicySeeder("synthetic")
	jobicy.retryDelay = 0
	jobicy.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.RawQuery, "tag=golang") {
			return response(r, 503, "down"), nil
		}
		return response(r, 200, `{"jobs":[{"id":901,"url":"https://jobicy.com/jobs/901","jobTitle":"API Engineer","companyName":"Kestrel Labs","jobDescription":"<p>REST APIs in Go.</p>"}]}`), nil
	})}
	if added, err := jobicy.Seed(ctx, db, cache); err != nil || added != 1 {
		t.Fatalf("jobicy with one feed down: added=%d err=%v", added, err)
	}
}
