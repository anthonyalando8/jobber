package web

import (
	"github.com/anthonyalando8/prospector/internal/store"
	"net/url"
	"strings"
	"testing"
)

func TestDashboardMetricDrillDownDestinations(t *testing.T) {
	expected := map[string]string{"Strong matches": "/opportunities?fit=strong_match", "Possible matches": "/opportunities?fit=possible_match", "Unrelated": "/opportunities?fit=unrelated", "Research queue": "/companies?research=queue", "Drafts needing attention": "/compositions?attention=required", "Legacy drafts": "/legacy/drafts", "Sent": "/messages?state=accepted", "Replies": "/threads?event=replied"}
	for _, metric := range dashboardMetrics(&store.DashboardCounts{}) {
		if want, ok := expected[metric.Label]; ok && metric.Href != want {
			t.Fatalf("%s goes to %s instead of %s", metric.Label, metric.Href, want)
		}
		if metric.Label == "Opportunities" && metric.DetailHref != "/opportunities?assessment=unassessed" {
			t.Fatal("unassessed lost destination")
		}
	}
}
func TestOpportunityURLPreservesAllFiltersAndPage(t *testing.T) {
	raw := "fit=strong_match&q=backend&company_id=12&source=remoteok&assessment=assessed&availability=open&description=complete&selection=new&sort=best&view=priority"
	values, _ := url.ParseQuery(raw)
	f, warnings := opportunityFilter(values)
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	next, _ := url.Parse(listURL("/opportunities", opportunityValues(f), 2))
	for key, value := range values {
		if next.Query().Get(key) != value[0] {
			t.Fatalf("pagination lost %s", key)
		}
	}
	if next.Query().Get("page") != "2" {
		t.Fatal("wrong next page")
	}
	reset, _ := url.Parse(listURL("/opportunities", opportunityValues(f), 1))
	if reset.Query().Get("page") != "" {
		t.Fatal("new filter URLs should reset to page 1")
	}
}
func TestInvalidFiltersWarnAndNormalize(t *testing.T) {
	f, warnings := opportunityFilter(url.Values{"fit": {"nonsense"}, "availability": {"unknown-enum"}, "company_id": {"bad"}, "sort": {"sql injection"}})
	if len(warnings) != 4 || f.Fit != "" || f.Availability != "" || f.CompanyID != 0 || f.Sort != "" {
		t.Fatal("invalid filters not safely normalized")
	}
	f, warnings = opportunityFilter(url.Values{"fit": {" STRONG_MATCH "}, "company": {"12"}})
	if len(warnings) != 0 || f.Fit != "strong_match" || f.CompanyID != 12 {
		t.Fatal("normalization/legacy aliases broken")
	}
}
func TestDetailReturnContextRejectsExternalURLs(t *testing.T) {
	good := "/opportunities?fit=strong_match&page=2"
	if safeReturn(good, "/opportunities") != good {
		t.Fatal("working context lost")
	}
	for _, bad := range []string{"https://evil.example", "//evil.example", "javascript:alert(1)", "/unknown", "/companies/../../evil", "/opportunities\\evil", "/opportunities\r\nLocation:evil"} {
		if safeReturn(bad, "/opportunities") != "/opportunities" {
			t.Fatalf("unsafe return accepted: %q", bad)
		}
	}
}
func TestDashboardExactOpportunityLinkAndPanelScope(t *testing.T) {
	v := dashboardTestView()
	v.Opportunities = []store.DashboardOpportunity{{ID: 123, Company: "Synthetic", Title: "Backend", Match: "strong_match"}}
	page := renderDashboardTest(t, v)
	if !strings.Contains(page, "/opportunities/123?return=") || !strings.Contains(page, "/opportunities?view=priority&amp;sort=priority") {
		t.Fatal("panel lost exact record or matching list context")
	}
	if strings.Contains(page, "Opportunities to review") {
		t.Fatal("priority matches mislabeled as review-required")
	}
}
func TestCompanyURLPreservesResearchAndCoverage(t *testing.T) {
	u, _ := url.Parse(companiesURL(store.CompanyFilter{Search: "needle", Research: "queue", Source: "manual", HasOpportunities: "yes", HasStrong: "yes"}, 2))
	for k, v := range map[string]string{"q": "needle", "research": "queue", "source": "manual", "has_opportunities": "yes", "has_strong": "yes", "page": "2"} {
		if u.Query().Get(k) != v {
			t.Fatalf("lost %s", k)
		}
	}
}

func TestNestedSidebarKeepsEntitySectionActive(t *testing.T) {
	t.Chdir("../..")
	for name, section := range map[string]string{"opportunity.html": "/opportunities", "company.html": "/companies", "plan.html": "/companies", "composition.html": "/compositions", "thread.html": "/threads"} {
		page, err := applicationShell(name, "<html><head></head><body></body></html>")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(page, `href="`+section+`" class="active" aria-current="page"`) {
			t.Fatalf("%s lost active %s", name, section)
		}
	}
}
