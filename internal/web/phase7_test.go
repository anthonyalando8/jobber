package web

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/automation"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
)

// The defining Phase 7 property: completing the implementation does not enable anything.
func TestAutomationIsOffAfterImplementation(t *testing.T) {
	t.Setenv("EMAIL_PIPELINE_MODE", "")
	t.Setenv("AUTO_SEND_INTENTS", "")
	t.Setenv("AUTO_SEND_DAILY_CAP", "")

	d := newDispatcher(nil)
	if d.Mode() != send.ModeReview {
		t.Fatalf("default rollout mode = %q, want %q", d.Mode(), send.ModeReview)
	}
	state := d.Automation()
	if state.Enabled {
		t.Fatal("automation is enabled with the shipped default configuration")
	}
	if len(state.Reasons) == 0 {
		t.Fatal("automation is off but says nothing about why")
	}
	if len(autoSendIntents()) != 0 {
		t.Fatal("the shipped configuration allowlists an intent")
	}
}

// N. An unknown or wildcard AUTO_SEND_INTENTS is a configuration error, not a silent no-op.
func TestUnknownAutoSendIntentIsAConfigurationError(t *testing.T) {
	for _, invalid := range []string{"all", "*", "hiring", "application", "specific_job_application,all"} {
		if _, err := automation.LoadConfig(send.ModeAuto, invalid, ""); err == nil {
			t.Fatalf("AUTO_SEND_INTENTS=%q was accepted", invalid)
		}
	}
	// Startup reads the same loader, so an invalid value stops the process rather than being
	// ignored by the handler.
	source, err := os.ReadFile("../../cmd/prospector/main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	if !strings.Contains(body, "automation.LoadConfig(") {
		t.Fatal("startup does not validate the automation configuration")
	}
	if !strings.Contains(body, "Automation configuration is invalid") {
		t.Fatal("an invalid automation configuration does not stop startup")
	}
}

// Intents that this policy never automates are refused at configuration time, so the mistake is
// visible at startup rather than as a per-message no-op.
func TestNonAutomatableIntentsAreRefusedInConfiguration(t *testing.T) {
	for _, intent := range []plan.Intent{plan.SpeculativeJobEnquiry, plan.ServiceOutreach} {
		if _, err := automation.LoadConfig(send.ModeAuto, string(intent), ""); err == nil {
			t.Fatalf("%s was accepted into the allowlist", intent)
		}
		if automation.IntentCapable(intent) {
			t.Fatalf("%s is reported automatable", intent)
		}
	}
	if !automation.IntentCapable(plan.SpecificJobApplication) {
		t.Fatal("job applications should be the one automatable intent")
	}
}

// A handler with no database reports automation off, and says so rather than looking ready.
func TestAutomationStateWithoutDatabase(t *testing.T) {
	h := &Handler{}
	state := h.AutomationState()
	if state.Enabled {
		t.Fatal("a handler with no dispatcher reported automation enabled")
	}
	if len(state.Reasons) == 0 {
		t.Fatal("no reason was given")
	}
}

// The dashboard shows automation state, the cap, and the blocked cases — blocked cases are never
// hidden, because they are the evidence for whether automation is held back by data or policy.
func TestDashboardShowsAutomationAndItsBlockers(t *testing.T) {
	v := dashboardTestView()
	v.Rollout = rolloutView{Mode: send.ModeReview, AutomationReasons: []string{"no intent is allowlisted for automatic sending"}}
	page := renderDashboardTest(t, v)
	for _, want := range []string{"Automation off", "no intent is allowlisted", "No automatic sends"} {
		if !strings.Contains(page, want) {
			t.Fatalf("dashboard missing %q", want)
		}
	}
}

func TestOpportunitiesPageIsPaginated(t *testing.T) {
	tmpl, err := template.ParseFiles("templates/opportunities.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]any{
		"Rows": []store.Opportunity{}, "Source": "", "Company": int64(0), "Title": "backend",
		"Availability": "open", "Total": 273, "Page": 2, "Pages": 6,
		"Query":   template.URL("title=backend&availability=open&"),
		"HasPrev": true, "HasNext": true, "Prev": 1, "Next": 3, "First": 51, "Last": 100,
	}); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	for _, want := range []string{
		"51", "100", "273", "page 2 of 6",
		// Filters must travel with the page links, or paging silently drops the filter.
		"?title=backend&amp;availability=open&amp;page=1",
		"?title=backend&amp;availability=open&amp;page=3",
		"?title=backend&amp;availability=open&amp;page=6",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("the opportunities page is missing %q", want)
		}
	}
	if strings.Contains(page, "Showing up to 200") {
		t.Fatal("the page still claims a silent 200-row limit")
	}
}

// The page size and the count query have to agree, or the last page is wrong.
func TestOpportunitiesPageSizeIsBounded(t *testing.T) {
	if opportunitiesPageSize <= 0 || opportunitiesPageSize > 200 {
		t.Fatalf("page size %d is not a sensible listing window", opportunitiesPageSize)
	}
}

// Automation shares the AI budget rather than having an allowance of its own.
func TestAutomationSharesTheAIBudget(t *testing.T) {
	h := &Handler{}
	available, reason := h.AIBudgetAvailable(nil)
	if available {
		t.Fatal("a handler with no budget reported capacity available")
	}
	if reason == "" {
		t.Fatal("no reason was given for the unavailable budget")
	}
}

// The automation worker only ever asks about first contact, and only about drafts that are
// already reviewed. It neither composes nor researches.
func TestAutomationWorkerScopeIsNarrow(t *testing.T) {
	source, err := os.ReadFile("../worker/automation.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	for _, forbidden := range []string{"KindFollowup", "RunComposition", "Compose(", "Enrich"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the automation worker references %q; it must only consider reviewed first-contact drafts", forbidden)
		}
	}
	if !strings.Contains(body, "UnresolvedSendAttempts") {
		t.Fatal("the automation worker does not halt on unresolved delivery state")
	}
	if !strings.Contains(body, "AutomationCandidates") {
		t.Fatal("the automation worker does not use the narrow candidate query")
	}
}

// Discovery and sending are two separate decisions. Research workers find and describe companies
// and never send, so gating them on send automation would mean the corpus silently stopped
// growing the moment automation was switched off — which is the normal state.
func TestResearchWorkersAreNotGatedOnSendAutomation(t *testing.T) {
	source, err := os.ReadFile("../../cmd/prospector/main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	start := strings.Index(body, "automationState := handler.AutomationState()")
	if start < 0 {
		t.Fatal("the worker startup block moved; update this test deliberately")
	}
	end := strings.Index(body[start:], "// Start server")
	if end < 0 {
		end = len(body) - start
	}
	block := body[start : start+end]

	schedulerAt := strings.Index(block, "scheduler.Run(")
	if schedulerAt < 0 {
		t.Fatal("the research scheduler is never started")
	}
	// The scheduler must be started under the rollout-mode check, not under the automation one.
	preceding := block[:schedulerAt]
	if !strings.Contains(preceding, "pipelineMode != send.ModeDisabled") {
		t.Fatal("research workers are not gated on rollout mode")
	}
	if strings.Contains(preceding, "if automationState.Enabled {") {
		t.Fatal("research workers are gated on send automation; discovery would stop whenever automation is off")
	}
	// The automation runner must still be gated on automation being configured.
	runnerAt := strings.Index(block, "NewAutomationRunner(")
	if runnerAt < 0 {
		t.Fatal("the automation runner is never constructed")
	}
	if !strings.Contains(block[:runnerAt], "if automationState.Enabled {") {
		t.Fatal("the automation runner is not gated on automation being configured")
	}
}
