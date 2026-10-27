package web

import (
	"bytes"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/anthonyalando8/prospector/internal/worker"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func dashboardTestView() *dashboardView {
	return &dashboardView{Greeting: "Good afternoon, Anthony", Health: "System needs attention", Counts: &store.DashboardCounts{}, Rollout: rolloutView{Mode: send.ModeManualSend, ManualSendPermitted: true}}
}
func renderDashboardTest(t *testing.T, v *dashboardView) string {
	t.Helper()
	tmpl, err := template.ParseFiles("templates/dashboard.html")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
func configureDashboardGemini(t *testing.T) {
	t.Helper()
	t.Setenv("EMAIL_COMPOSITION_ENABLED", "true")
	t.Setenv("EMAIL_COMPOSE_PROVIDER", "gemini")
	t.Setenv("EMAIL_REVIEW_PROVIDER", "gemini")
	t.Setenv("EMAIL_COMPOSE_MODEL", "synthetic-compose-model")
	t.Setenv("EMAIL_REVIEW_MODEL", "synthetic-review-model")
	t.Setenv("GEMINI_API_KEY", "synthetic-private-key-one,synthetic-private-key-two")
	t.Setenv("AI_REQUESTS_PER_HOUR", "18")
	t.Setenv("AI_REQUESTS_PER_DAY", "60")
}
func TestDashboardUsesInitializedV2GeminiAndEffectiveLimits(t *testing.T) {
	configureDashboardGemini(t)
	h := NewHandler(nil, nil)
	if h.HasAIProvider() {
		t.Fatal("legacy provider must remain disabled")
	}
	runtime := h.compositionConfig()
	composer, err := runtime.composer()
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := runtime.reviewer()
	if err != nil {
		t.Fatal(err)
	}
	if composer != runtime.Compose || reviewer != runtime.Review {
		t.Fatal("composition must share initialized providers")
	}
	a := aiView(runtime, &store.DashboardCounts{HourUsed: 2, DayUsed: 7, Reserved: 3})
	if a.Name != "Google Gemini" || !a.Usable || a.HourLimit != 18 || a.DayLimit != 60 || a.HourUsed != 2 || a.DayUsed != 7 {
		t.Fatalf("unexpected AI state: %+v", a)
	}
	v := dashboardTestView()
	v.AI = a
	page := renderDashboardTest(t, v)
	for _, want := range []string{"Google Gemini", "synthetic-compose-model", "synthetic-review-model", "2 / 18", "7 / 60", "Approval required before sending", "Automation off"} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(page, "synthetic-private-key") || strings.Contains(page, "No AI provider configured") {
		t.Fatal("secrets or false legacy status leaked")
	}
	t.Setenv("AI_REQUESTS_PER_HOUR", "99")
	hour, day := h.compositionConfig().limits()
	if hour != 18 || day != 60 {
		t.Fatal("runtime configuration drifted")
	}
	if got := h.redactActivity("failure synthetic-private-key-one"); strings.Contains(got, "synthetic-private-key") {
		t.Fatal("activity credential leaked")
	}
}
func TestDashboardUnavailableProviderAndEmptyActivity(t *testing.T) {
	configureDashboardGemini(t)
	t.Setenv("GEMINI_API_KEY", "")
	a := aiView(loadCompositionRuntime(), &store.DashboardCounts{})
	if a.Configured || a.Usable {
		t.Fatal("failed initialization must not imply usability")
	}
	v := dashboardTestView()
	v.AI = a
	page := renderDashboardTest(t, v)
	if !strings.Contains(page, "No activity recorded") || !strings.Contains(page, "Unavailable") {
		t.Fatal("missing empty or unavailable state")
	}
}
func TestDashboardDoesNotInventRequestDefaults(t *testing.T) {
	configureDashboardGemini(t)
	t.Setenv("AI_REQUESTS_PER_HOUR", "")
	t.Setenv("AI_REQUESTS_PER_DAY", "")
	a := aiView(loadCompositionRuntime(), &store.DashboardCounts{})
	if a.HourLimit != 0 || a.DayLimit != 0 || a.Usable {
		t.Fatal("missing quotas must not inherit legacy defaults")
	}
}
func TestDashboardActivityAgesAndGreeting(t *testing.T) {
	now := time.Date(2026, 9, 13, 14, 0, 0, 0, time.UTC)
	if dashboardAgo(now.Add(-18*time.Minute), now) != "18 minutes ago" {
		t.Fatal("inaccurate activity age")
	}
	if dashboardAgo(time.Time{}, now) == "just now" {
		t.Fatal("missing activity cannot be just now")
	}
	for _, tc := range []struct {
		hour   int
		period string
	}{{8, "morning"}, {14, "afternoon"}, {21, "evening"}} {
		if !strings.Contains(greeting(time.Date(2026, 9, 13, tc.hour, 0, 0, 0, time.UTC)), tc.period) {
			t.Fatal("wrong greeting")
		}
	}
}

func TestDashboardWorkerRuntimeIndependentOfAutomation(t *testing.T) {
	now := time.Now()
	v := dashboardTestView()
	// Every loop the scheduler reports, including contact discovery — which runs with research
	// and is independent of sending, so a healthy summary must account for it.
	source := func() []worker.LoopStatus {
		states := make([]worker.LoopStatus, 0, len(worker.ReportedLoops))
		for _, name := range worker.ReportedLoops {
			states = append(states, worker.LoopStatus{Name: name, AliveAt: now, Busy: name == "research"})
		}
		return states
	}
	populateWorkerView(v, source, now)
	if !strings.Contains(v.WorkerSummary, "running") || v.Rollout.AutomaticSending || len(v.Workers) != len(worker.ReportedLoops) || !v.Workers[1].Busy {
		t.Fatal("research confused with automation state")
	}
	stale := dashboardTestView()
	populateWorkerView(stale, source, now.Add(time.Minute))
	if len(stale.Problems) == 0 || stale.Workers[1].Alive {
		t.Fatal("overdue heartbeat must not be healthy")
	}
	missing := dashboardTestView()
	populateWorkerView(missing, nil, now)
	if len(missing.Problems) == 0 {
		t.Fatal("missing scheduler must be explicit")
	}
}
func TestLiveSnapshotFailureDoesNotFabricateZeroCounts(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	h.DashboardSnapshot(w, httptest.NewRequest(http.MethodGet, "/dashboard/snapshot", nil))
	if w.Code != 503 {
		t.Fatal("database failure must not return a healthy zero snapshot")
	}
}

// An alert with somewhere to go renders as a link to it; one without stays plain text. Metric
// cards carry no decorative strip, and colour marks only a count waiting on a person.
func TestDashboardAlertsLinkToTheirFixAndCardsStayNeutral(t *testing.T) {
	v := dashboardTestView()
	v.addProblem("1 company cannot be searched for contacts until the website domain is corrected", "/companies/42#company-domain", "Correct the domain")
	v.addProblem("Recent research worker failures; see recent activity", "", "")
	v.Metrics = dashboardMetrics(&store.DashboardCounts{Companies: 3, CompositionReview: 2})
	page := renderDashboardTest(t, v)
	if !strings.Contains(page, `<a href="/companies/42#company-domain">Correct the domain`) {
		t.Fatal("the domain alert does not link to the company's domain field")
	}
	if !strings.Contains(page, "<li><span>Recent research worker failures; see recent activity</span></li>") {
		t.Fatal("an alert with no destination should render without a link")
	}
	if strings.Contains(page, "metric-tone") {
		t.Fatal("metric cards still render the coloured edge strip")
	}
	if strings.Count(page, "metric-attention") != 1 {
		t.Fatalf("only the drafts card should be marked for attention, got %d", strings.Count(page, "metric-attention"))
	}
	none := renderDashboardTest(t, &dashboardView{Counts: &store.DashboardCounts{}, Metrics: dashboardMetrics(&store.DashboardCounts{})})
	if strings.Contains(none, "metric-attention") {
		t.Fatal("an empty drafts count must not be highlighted")
	}
}
