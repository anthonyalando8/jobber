package opportunity

import "testing"

func TestEligibilityReadsWhatTheListingStates(t *testing.T) {
	cases := []struct {
		name, source, location, description string
		eligibility, kenya                  string
	}{
		{"wwr worldwide", "weworkremotely", "Anywhere in the World", "", EligibilityWorldwide, KenyaOpen},
		{"wwr usa only", "weworkremotely", "USA Only", "", EligibilityCountry, KenyaRestricted},
		{"plain us", "remoteok", "US", "", EligibilityCountry, KenyaRestricted},
		{"europe", "remotive", "Europe", "", EligibilityRegion, KenyaRestricted},
		{"emea includes kenya", "jobicy", "EMEA", "", EligibilityRegion, KenyaLikely},
		{"kenya named", "himalayas", "Kenya, Nigeria", "", EligibilityCountry, KenyaOpen},
		{"kenyan board", "myjobmag.co.ke", "", "", EligibilityCountry, KenyaOpen},
		{"silent listing stays unclear", "remoteok", "", "Build APIs in Go.", EligibilityUnclear, KenyaUnknown},
		{"remote alone is unclear", "remotive", "Remote", "", EligibilityUnclear, KenyaUnknown},
		{"text restriction beats location", "remotive", "Worldwide", "Candidates must be based in the US.", EligibilityCountry, KenyaRestricted},
		{"work authorization", "remoteok", "", "You must be authorized to work in the United States.", EligibilityCountry, KenyaRestricted},
		{"open statement", "remoteok", "", "We hire candidates from anywhere in the world.", EligibilityWorldwide, KenyaOpen},
		{"anywhere in us is not worldwide", "remoteok", "Anywhere in the US", "", EligibilityCountry, KenyaRestricted},
		{"business is not us", "remoteok", "Business hours overlap", "", EligibilityUnclear, KenyaUnknown},
	}
	for _, c := range cases {
		a := ClassifyAccess(DiscoveredOpportunity{Source: c.source, Location: c.location, Description: c.description})
		if a.Eligibility != c.eligibility || a.KenyaAccess != c.kenya {
			t.Errorf("%s: got %s/%s, want %s/%s (evidence %q)", c.name, a.Eligibility, a.KenyaAccess, c.eligibility, c.kenya, a.EligibilityEvidence)
		}
	}
}

func TestRouteAndCostNeedEvidence(t *testing.T) {
	cases := []struct {
		name        string
		d           DiscoveredOpportunity
		route, cost string
	}{
		{"published email", DiscoveredOpportunity{URL: "https://remoteok.com/jobs/1", Metadata: map[string]string{MetaApplyEmail: "jobs@acme.example"}}, RouteDirectContact, CostFree},
		{"employer ats", DiscoveredOpportunity{URL: "https://remoteok.com/jobs/1", Metadata: map[string]string{MetaApplyURL: "https://jobs.lever.co/acme/123/apply"}}, RouteEmployerATS, CostFree},
		{"employer site", DiscoveredOpportunity{URL: "https://remoteok.com/jobs/1", Metadata: map[string]string{MetaApplyURL: "https://acme.example/careers/1"}}, RouteEmployerSite, CostFree},
		{"board only is not assumed paid", DiscoveredOpportunity{URL: "https://weworkremotely.com/remote-jobs/x"}, RouteJobBoard, CostUnknown},
		{"stated paywall", DiscoveredOpportunity{URL: "https://example-board.test/x", Description: "Upgrade to apply for this role."}, RouteJobBoard, CostPaid},
		{"stated registration", DiscoveredOpportunity{URL: "https://example-board.test/x", Description: "Please sign in to apply."}, RouteJobBoard, CostRegistration},
		{"nothing", DiscoveredOpportunity{}, RouteUnknown, CostUnknown},
	}
	for _, c := range cases {
		a := ClassifyAccess(c.d)
		if a.Route != c.route || a.Cost != c.cost {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, a.Route, a.Cost, c.route, c.cost)
		}
	}
}

func TestContractWorkIsRecognised(t *testing.T) {
	if !ClassifyAccess(DiscoveredOpportunity{EmploymentType: "Contract"}).Contract {
		t.Error("contract employment type not recognised")
	}
	if !ClassifyAccess(DiscoveredOpportunity{Title: "Freelance Django Developer"}).Contract {
		t.Error("freelance title not recognised")
	}
	if ClassifyAccess(DiscoveredOpportunity{Title: "Backend Engineer", EmploymentType: "Full-Time"}).Contract {
		t.Error("full-time role marked as contract")
	}
}

func TestPursuitScorePrefersAccessibleDirectWork(t *testing.T) {
	direct := Access{KenyaAccess: KenyaOpen, Route: RouteDirectContact, Cost: CostFree, Contract: true}
	restricted := Access{KenyaAccess: KenyaRestricted, Route: RouteJobBoard, Cost: CostUnknown}
	paid := Access{KenyaAccess: KenyaOpen, Route: RouteJobBoard, Cost: CostPaid}
	if PursuitScore("possible_match", direct, 3) <= PursuitScore("strong_match", restricted, 3) {
		t.Error("an accessible direct contract should outrank a US-only strong match")
	}
	if PursuitScore("possible_match", paid, 3) >= PursuitScore("possible_match", Access{KenyaAccess: KenyaOpen, Route: RouteJobBoard, Cost: CostUnknown}, 3) {
		t.Error("a paid application should rank below an otherwise identical free one")
	}
}
