package web

import (
	"net/url"
	"testing"
)

// The filters an operator needs to find the work that is actually blocked. Without these,
// "6 strong matches" and "6 strong matches with nowhere to send" look like the same list.
func TestOpportunityFilterAcceptsDestinationAndContactSearchStates(t *testing.T) {
	values := url.Values{}
	values.Set("fit", "strong_match")
	values.Set("recipient", "missing")
	values.Set("contact_search", "pending")

	filter, warnings := opportunityFilter(values)
	if len(warnings) != 0 {
		t.Fatalf("valid filters were rejected: %v", warnings)
	}
	if filter.Fit != "strong_match" || filter.Recipient != "missing" || filter.ContactSearch != "pending" {
		t.Fatalf("filters were not parsed: %+v", filter)
	}

	// The filter must survive a round trip, or pagination and back-links silently drop it.
	rebuilt := opportunityValues(filter)
	if rebuilt.Get("recipient") != "missing" || rebuilt.Get("contact_search") != "pending" {
		t.Fatalf("destination filters were lost when the URL was rebuilt: %v", rebuilt.Encode())
	}
	if listURL("/opportunities", rebuilt, 1) == "/opportunities" {
		t.Fatal("the filtered list URL dropped every filter")
	}
}

func TestUnknownDestinationFilterIsRefusedRatherThanGuessed(t *testing.T) {
	values := url.Values{}
	values.Set("recipient", "probably_fine")
	filter, warnings := opportunityFilter(values)
	if filter.Recipient != "" {
		t.Fatalf("an unknown destination filter was accepted: %q", filter.Recipient)
	}
	if len(warnings) == 0 {
		t.Fatal("an unknown filter was dropped silently instead of being reported")
	}
}

// The contact-discovery page must be a legal place to return from, or the "Find contact" action
// on the plan and opportunity pages loses its way back.
func TestContactDiscoveryIsAValidReturnPath(t *testing.T) {
	path := "/companies/42/contact-search"
	if got := safeReturn(path, "/companies"); got != path {
		t.Fatalf("the contact discovery page was refused as a return path: %q", got)
	}
	if label := returnLabel(path); label != "Back to contact discovery" {
		t.Fatalf("unexpected return label %q", label)
	}
	// An off-site return is still refused.
	if got := safeReturn("https://elsewhere.example/steal", "/companies"); got != "/companies" {
		t.Fatalf("an external return path was accepted: %q", got)
	}
}

// Inference is the one switch here that creates a fact nobody published, so it must be off
// unless it is turned on deliberately and exactly.
func TestEmailInferenceIsOffUnlessExplicitlyEnabled(t *testing.T) {
	for _, value := range []string{"", "false", "1", "yes", "TRUE"} {
		t.Setenv("CONTACT_DISCOVERY_INFER_EMAILS", value)
		if contactEngineConfig().Inference.Enabled {
			t.Fatalf("email inference was enabled by CONTACT_DISCOVERY_INFER_EMAILS=%q", value)
		}
	}
	t.Setenv("CONTACT_DISCOVERY_INFER_EMAILS", "true")
	if !contactEngineConfig().Inference.Enabled {
		t.Fatal("email inference stayed off when it was explicitly enabled")
	}
}

// The budget defaults conservatively, and AI extraction is opt-in rather than opt-out.
func TestContactBudgetDefaultsAreConservative(t *testing.T) {
	t.Setenv("CONTACT_DISCOVERY_COMPANIES_PER_DAY", "")
	cfg := contactBudgetConfig()
	if cfg.CompaniesPerDay <= 0 || cfg.CompaniesPerDay > 50 {
		t.Fatalf("the default company allowance is not conservative: %d", cfg.CompaniesPerDay)
	}
	if cfg.AICallsPerDay != 0 {
		t.Fatalf("AI extraction is on by default with %d calls; it must be opt-in", cfg.AICallsPerDay)
	}
	t.Setenv("CONTACT_DISCOVERY_COMPANIES_PER_DAY", "3")
	if contactBudgetConfig().CompaniesPerDay != 3 {
		t.Fatal("the company allowance is not configurable")
	}
}
