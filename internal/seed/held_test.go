package seed

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
)

func TestHimalayasIdentityFallsBackToGUIDAndApplicationLink(t *testing.T) {
	cases := []struct {
		name, payload, wantID, wantURL, wantApply string
	}{
		{"legacy id and slug", `{"id":42,"slug":"go-dev","companyName":"Acme","title":"Go Developer"}`, "42", "https://himalayas.app/jobs/go-dev", ""},
		{"guid url", `{"guid":"https://himalayas.app/companies/acme/jobs/go-dev","applicationLink":"https://acme.example/careers/1","companyName":"Acme","title":"Go Developer","pubDate":1761472800000}`,
			"https://himalayas.app/companies/acme/jobs/go-dev", "https://himalayas.app/companies/acme/jobs/go-dev", "https://acme.example/careers/1"},
		{"slug-style guid", `{"guid":"acme-go-developer-abc123","applicationLink":"https://acme.example/careers/1","companyName":"Acme","title":"Go Developer"}`,
			"acme-go-developer-abc123", "https://acme.example/careers/1", "https://acme.example/careers/1"},
	}
	for _, c := range cases {
		d, ok := himalayasOpportunity(json.RawMessage(c.payload))
		if !ok || d.ExternalID != c.wantID || d.URL != c.wantURL || d.Metadata[opportunity.MetaApplyURL] != c.wantApply {
			t.Errorf("%s: ok=%v id=%q url=%q apply=%q", c.name, ok, d.ExternalID, d.URL, d.Metadata[opportunity.MetaApplyURL])
		}
		if d.Identity() == "" {
			t.Errorf("%s: no stable identity", c.name)
		}
	}
	d, _ := himalayasOpportunity(json.RawMessage(`{"guid":"x","companyName":"Acme","title":"Go","pubDate":1761472800000}`))
	if d.PublishedAt == nil || d.PublishedAt.Year() != 2025 {
		t.Errorf("millisecond pubDate not read: %v", d.PublishedAt)
	}
	// With no identity at all, the fields the source did send are recorded for diagnosis.
	d, _ = himalayasOpportunity(json.RawMessage(`{"companyName":"Acme","title":"Go","jobRef":"zz"}`))
	if d.Identity() != "" || d.Metadata["source_fields"] != "companyName,jobRef,title" {
		t.Errorf("missing identity diagnostics: %q", d.Metadata["source_fields"])
	}
}

func TestBrighterMondayAbsoluteEmployerLinksAndHeldCardEvidence(t *testing.T) {
	db := seedFixtureDB(t)
	ctx := context.Background()
	page := `<div data-cy="listing-cards-components"><a href="https://www.brightermonday.co.ke/company/acme-ltd">Acme Ltd</a><a data-cy="listing-title-link" href="/listings/backend-dev-1">Backend Developer</a></div>
<div data-cy="listing-cards-components"><p class="employer">Unnamed Employer Text</p><a data-cy="listing-title-link" href="/listings/python-dev-2">Python Developer</a></div>`
	kenya := NewJobBoardSeeder("synthetic")
	kenya.retryDelay = 0
	kenya.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "www.brightermonday.co.ke" && r.URL.Path == "/jobs" {
			return response(r, 200, page), nil
		}
		return response(r, 404, ""), nil
	})}
	if _, err := kenya.Seed(ctx, db, redis.NewClient(&redis.Options{Addr: "unused.invalid:6379"})); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM opportunity o JOIN company c ON c.id=o.company_id WHERE o.source='brightermonday.co.ke' AND c.name='Acme Ltd'`).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("absolute employer link not read: %d %v", stored, err)
	}
	held, err := db.ListHeldListings(ctx, store.HeldListingFilter{Source: "brightermonday.co.ke", Reason: "missing_employer"}, 10, 0)
	if err != nil || len(held) != 1 {
		t.Fatalf("held listings: %d %v", len(held), err)
	}
	if card := held[0].Discovery.Metadata["listing_card_html"]; !strings.Contains(card, "Unnamed Employer Text") {
		t.Fatalf("held listing lacks its card markup: %q", card)
	}
}

func TestAmbiguousEmployerIsHeldThenAttachedByOperator(t *testing.T) {
	db := seedFixtureDB(t)
	ctx := context.Background()
	// The workspace already knows this employer under another spelling, at the domain the new
	// spelling's guess produces.
	existing, err := db.CreateCompany(ctx, "Harbor Web Ltd", "harborweb.com", "remoteok", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	nomads := NewWorkingNomadsSeeder("synthetic")
	nomads.retryDelay = 0
	nomads.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		return response(r, 200, `[{"url":"https://www.workingnomads.com/jobs/harbor-django","title":"Django Developer","company_name":"Harbor Web","category_name":"Development","location":"Anywhere","description":"Django."}]`), nil
	})}
	cache := redis.NewClient(&redis.Options{Addr: "unused.invalid:6379"})
	if added, err := nomads.Seed(ctx, db, cache); err != nil || added != 0 {
		t.Fatalf("an ambiguous employer must not be stored automatically: added=%d err=%v", added, err)
	}
	held, err := db.ListHeldListings(ctx, store.HeldListingFilter{Reason: "ambiguous_company_identity"}, 10, 0)
	if err != nil || len(held) != 1 {
		t.Fatalf("held: %d %v", len(held), err)
	}
	candidates, err := db.CompanyCandidates(ctx, held[0].Discovery)
	if err != nil || len(candidates) != 1 || candidates[0].ID != existing.ID {
		t.Fatalf("candidates: %+v %v", candidates, err)
	}
	result, err := db.AttachHeldListing(ctx, held[0].ID, existing.ID, "operator")
	if err != nil || result.ID == 0 {
		t.Fatalf("attach: %+v %v", result, err)
	}
	// The next run of the source neither duplicates the listing nor holds it back again.
	if added, err := nomads.Seed(ctx, db, cache); err != nil || added != 0 {
		t.Fatalf("repeat after attach: added=%d err=%v", added, err)
	}
	if n, _ := db.CountHeldListings(ctx, store.HeldListingFilter{Reason: "ambiguous_company_identity"}); n != 0 {
		t.Fatalf("resolved listing still counted as held: %d", n)
	}
	var companyID int64
	if err := db.QueryRow(ctx, `SELECT company_id FROM opportunity WHERE source='workingnomads'`).Scan(&companyID); err != nil || companyID != existing.ID {
		t.Fatalf("listing attached to %d, want %d (%v)", companyID, existing.ID, err)
	}
	var companies int
	db.QueryRow(ctx, `SELECT count(*) FROM company`).Scan(&companies)
	if companies != 1 {
		t.Fatalf("attaching must not create or merge companies: %d companies", companies)
	}
	// Attaching the same employer name elsewhere is refused rather than silently moved.
	other, _ := db.CreateCompany(ctx, "Elsewhere", "elsewhere.example", "manual", "", "manual")
	if _, err := db.AttachHeldListing(ctx, held[0].ID, other.ID, "operator"); err != store.ErrAliasConflict {
		t.Fatalf("conflicting attach: %v", err)
	}
}
