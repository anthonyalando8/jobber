package verify

import (
	"strings"
	"testing"
)

// The defect this fixes: every international listing was stored with a .co.ke domain, so
// research ran against a host that does not exist and no contact was ever found. Kenyan sources
// must keep their original behaviour; international ones must not.
func TestKenyanAndInternationalSourcesOrderDomainsDifferently(t *testing.T) {
	kenyan := CandidateDomainsForMarket("Savannah Informatics", MarketKenya)
	if len(kenyan) == 0 || !strings.HasSuffix(kenyan[0], ".co.ke") {
		t.Fatalf("a Kenyan-sourced company should try .co.ke first, got %v", kenyan)
	}

	international := CandidateDomainsForMarket("Delinea", MarketInternational)
	if len(international) == 0 || strings.HasSuffix(international[0], ".co.ke") {
		t.Fatalf("an internationally-sourced company must not try .co.ke first, got %v", international)
	}
	if international[0] != "delinea.com" {
		t.Fatalf("first international candidate = %q, want delinea.com", international[0])
	}

	// Both markets keep every candidate for the same name: a Kenyan company can be on .com, and
	// an international one could in principle be on .co.ke. Only the order changes, so the two
	// lists must be permutations of each other.
	const name = "Savannah Informatics"
	local := CandidateDomainsForMarket(name, MarketKenya)
	global := CandidateDomainsForMarket(name, MarketInternational)
	if len(local) != len(global) {
		t.Fatalf("candidate sets differ in size: %v vs %v", local, global)
	}
	counts := map[string]int{}
	for _, candidate := range local {
		counts[candidate]++
	}
	for _, candidate := range global {
		counts[candidate]--
	}
	for candidate, delta := range counts {
		if delta != 0 {
			t.Fatalf("%q appears in only one ordering: %v vs %v", candidate, local, global)
		}
	}
	if !strings.HasSuffix(local[0], ".co.ke") || strings.HasSuffix(global[0], ".co.ke") {
		t.Fatalf("ordering is wrong: kenyan %v, international %v", local, global)
	}
}

func TestMarketForSource(t *testing.T) {
	for _, source := range []string{"remoteok", "weworkremotely", "remotive", "himalayas", "RemoteOK"} {
		if MarketForSource(source) != MarketInternational {
			t.Fatalf("%s should be an international source", source)
		}
	}
	// Anything unrecognized stays Kenyan, which preserves the original behaviour for the Kenyan
	// boards and for manual entry rather than silently changing it.
	for _, source := range []string{"jobboard", "manual", "brightermonday.co.ke", "myjobmag.co.ke", ""} {
		if MarketForSource(source) != MarketKenya {
			t.Fatalf("%s should default to the Kenyan market", source)
		}
	}
}

// A URL the source published outranks every guess — but only when it actually names the
// employer. A link to the job board describes where the listing lives, not who posted it.
func TestDomainFromURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://www.delinea.com/careers":     "delinea.com",
		"http://wakacje.pl":                   "wakacje.pl",
		"cosuno.com":                          "cosuno.com",
		"https://jobs.example.test/apply?a=1": "jobs.example.test",
	} {
		if got := DomainFromURL(raw); got != want {
			t.Fatalf("DomainFromURL(%q) = %q, want %q", raw, got, want)
		}
	}
	// Boards and applicant-tracking systems are not employers.
	for _, raw := range []string{
		"https://remoteok.com/remote-jobs/123",
		"https://weworkremotely.com/remote-jobs/abc",
		"https://boards.greenhouse.io/acme/jobs/1",
		"https://acme.lever.co/apply",
		"https://www.linkedin.com/jobs/view/1",
		"https://myjobmag.co.ke/jobs/1",
		"", "not a url at all", "https://",
	} {
		if got := DomainFromURL(raw); got != "" {
			t.Fatalf("DomainFromURL(%q) = %q; a board or unusable URL must yield nothing", raw, got)
		}
	}
}
