package web

import (
	"bytes"
	"html/template"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
)

// The opportunities list is meant to be the work that is still live. A posting that expired, was
// filled or was taken down is held back unless asked for, which is the whole point of recording
// the suppression: before this, nothing could write one and nothing would have hidden it.
func TestTheOpportunityListHidesSuppressedPostingsUnlessAsked(t *testing.T) {
	filter, warnings := opportunityFilter(url.Values{})
	if filter.Suppressed != "hidden" {
		t.Errorf("the default list does not hide suppressed postings: %q", filter.Suppressed)
	}
	if len(warnings) != 0 {
		t.Errorf("an empty query produced warnings: %v", warnings)
	}

	for _, choice := range []string{"only", "any"} {
		filter, warnings = opportunityFilter(url.Values{"suppressed": {choice}})
		if filter.Suppressed != choice {
			t.Errorf("suppressed=%s was not honoured: %q", choice, filter.Suppressed)
		}
		if len(warnings) != 0 {
			t.Errorf("suppressed=%s produced warnings: %v", choice, warnings)
		}
		// The choice has to survive pagination, or page 2 of the suppressed list is the live list.
		rebuilt := opportunityValues(filter)
		if rebuilt.Get("suppressed") != choice {
			t.Errorf("suppressed=%s was lost rebuilding the URL: %v", choice, rebuilt)
		}
	}

	// An unknown value falls back to the safe default rather than showing everything, and says so.
	filter, warnings = opportunityFilter(url.Values{"suppressed": {"everything-please"}})
	if filter.Suppressed != "hidden" {
		t.Errorf("an invalid value did not fall back to hiding: %q", filter.Suppressed)
	}
	if len(warnings) == 0 {
		t.Error("an invalid suppressed filter was ignored silently")
	}

	// The default stays out of the URL, so ordinary links are not littered with it.
	if got := opportunityValues(store.OpportunityListFilter{Suppressed: "hidden"}).Get("suppressed"); got != "" {
		t.Errorf("the default was written into the URL: %q", got)
	}
}

// opportunityPageData is the shape the detail handler passes, reduced to what the template reads.
// Parsing a template proves nothing about executing it: a wrong field name or a method that does
// not exist only fails when the page is actually rendered.
func opportunityPageData(suppression *store.OpportunitySuppression, history []store.OpportunitySuppression) map[string]any {
	return map[string]any{
		"ID": int64(7), "Documents": []string{"{}"}, "Facts": opportunity.DiscoveredOpportunity{},
		"Revision": 1, "DiscoveredAt": time.Now(), "LastSeenAt": time.Now(), "UpdatedAt": time.Now(),
		"CompanyID": int64(3), "CompanyName": "Example Ltd",
		"BackURL": "/opportunities", "BackLabel": "Back to opportunities", "CurrentURL": "/opportunities/7",
		"Profile":     map[string]any{"Active": true, "Reason": ""},
		"Relevance":   nil,
		"Suppression": suppression, "SuppressionHistory": history,
		"SuppressionReasons": store.SuppressionReasonChoices(),
	}
}

func renderOpportunityPage(t *testing.T, data map[string]any) string {
	t.Helper()
	tmpl, err := template.ParseFiles("templates/opportunity.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("the opportunity page does not render: %v", err)
	}
	return out.String()
}

// A posting with no suppression offers the action, with the vocabulary explained rather than left
// as a list of slugs to guess between.
func TestTheOpportunityPageOffersSuppression(t *testing.T) {
	page := renderOpportunityPage(t, opportunityPageData(nil, nil))

	for _, expected := range []string{
		`action="/opportunities/7/suppress"`,
		`name="reason"`,
		`value="no_longer_hiring"`,
		"No longer hiring",
		"The posting is gone or the employer has stopped recruiting for it.",
	} {
		if !strings.Contains(page, expected) {
			t.Errorf("the suppress form is missing %q", expected)
		}
	}
	if strings.Contains(page, "/reinstate") {
		t.Error("a posting that is not suppressed offers to reinstate it")
	}
}

// A suppressed posting says what was decided, by whom, on what basis, and offers the way back.
// An automatic decision also shows the evidence it relied on, because a decision nobody made has
// to be checkable.
func TestTheOpportunityPageExplainsAnExistingSuppression(t *testing.T) {
	decided := time.Date(2026, 3, 14, 9, 30, 0, 0, time.UTC)
	automatic := store.OpportunitySuppression{
		ID: 1, OpportunityID: 7, OpportunityRevision: 1,
		Reason: store.SuppressExpired, Basis: store.BasisExpiryPassed,
		DecidedBy: store.SuppressionSweepActor, DecidedAt: decided,
		Evidence: map[string]any{"source": "weworkremotely"},
	}
	page := renderOpportunityPage(t, opportunityPageData(&automatic, []store.OpportunitySuppression{automatic}))

	for _, expected := range []string{
		"Suppressed as Expired",
		"the published closing date has passed",
		store.SuppressionSweepActor,
		"14 Mar 2026",
		`action="/opportunities/7/reinstate"`,
		"weworkremotely",
	} {
		if !strings.Contains(page, expected) {
			t.Errorf("the suppression notice is missing %q", expected)
		}
	}
	if strings.Contains(page, `action="/opportunities/7/suppress"`) {
		t.Error("an already-suppressed posting offers to suppress it again")
	}

	// A source revision published after the decision is new information worth saying out loud,
	// without lifting the decision on the source's behalf.
	data := opportunityPageData(&automatic, []store.OpportunitySuppression{automatic})
	data["Revision"] = 4
	data["SuppressionOutdated"] = true
	if page = renderOpportunityPage(t, data); !strings.Contains(page, "has published revision 4 since this was suppressed") {
		t.Error("a newer source revision is not surfaced on a suppressed posting")
	}

	// A reinstated decision renders its own ending, which is the case that would panic on a nil
	// SupersededAt if the template reached for it unguarded.
	lifted := automatic
	when := decided.Add(48 * time.Hour)
	lifted.SupersededAt = &when
	lifted.SupersededBy = "operator"
	lifted.SupersedeNote = "employer reposted it"
	page = renderOpportunityPage(t, opportunityPageData(nil, []store.OpportunitySuppression{lifted}))
	for _, expected := range []string{"Reinstated by operator", "employer reposted it"} {
		if !strings.Contains(page, expected) {
			t.Errorf("the suppression history is missing %q", expected)
		}
	}
}

// The list has to say what it is holding back, and label a suppressed row as a decision rather
// than as a database slug.
func TestTheOpportunityListShowsWhatItIsHiding(t *testing.T) {
	tmpl, err := template.ParseFiles("templates/opportunities.html")
	if err != nil {
		t.Fatal(err)
	}
	row := store.OpportunityListRow{
		Fit: "possible_match", Selection: "ignored",
		Suppressed: true, SuppressionReason: store.SuppressNoLongerHiring,
		SuppressionBasis: store.BasisAbsentFromSource,
	}
	row.ID, row.CompanyID, row.CompanyName = 7, 3, "Example Ltd"
	data := map[string]any{
		"Rows":   []store.OpportunityListRow{row},
		"Filter": store.OpportunityListFilter{Suppressed: "only"},
		"Facets": store.OpportunityFacets{Suppressed: 12, Sources: []string{"weworkremotely"}},
		"Total":  1, "Page": 1, "Pages": 1, "First": 1, "Last": 1,
		"CurrentURL": "/opportunities?suppressed=only",
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("the opportunities list does not render: %v", err)
	}
	page := out.String()
	if !strings.Contains(page, "No longer hiring") {
		t.Error("the suppressed row does not say why it was dropped")
	}
	if strings.Contains(page, "no_longer_hiring<") {
		t.Error("the row shows a raw slug")
	}
	if !strings.Contains(page, `name="suppressed"`) {
		t.Error("the list offers no control over suppressed postings")
	}

	// With the default filter, the page says how many postings it is holding back and links to them.
	data["Filter"] = store.OpportunityListFilter{Suppressed: "hidden"}
	out.Reset()
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatal(err)
	}
	if page = out.String(); !strings.Contains(page, "12 suppressed") || !strings.Contains(page, "suppressed=only") {
		t.Error("the list does not say how many suppressed postings it is hiding")
	}
}
