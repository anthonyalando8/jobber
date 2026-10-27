package web

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/anthonyalando8/prospector/internal/worker"
)

type dashboardMetric struct {
	Label                                string
	Count                                int
	Detail, Href, Tone, Icon, DetailHref string
}
type dashboardAction struct{ Label, Detail, Href string }
type dashboardActivity struct {
	Action, Detail, Ago, Exact, Href string
	Success                          bool
}
type dashboardWorker struct {
	Name, State, LastCycle  string
	Company, LastCycleExact string
	Failures                int
	Busy, Alive             bool
}
type dashboardAI struct {
	Name, Model, ReviewName, ReviewModel, State                         string
	Configured, Usable                                                  bool
	HourUsed, HourLimit, DayUsed, DayLimit, Reserved, Failures, Percent int
}
type dashboardView struct {
	Greeting, Health, HealthTone, WorkerSummary, Updated string
	Problems                                             []dashboardProblem
	Metrics                                              []dashboardMetric
	AI                                                   dashboardAI
	Counts                                               *store.DashboardCounts
	Rollout                                              rolloutView
	Workers                                              []dashboardWorker
	Activity                                             []dashboardActivity
	Opportunities                                        []store.DashboardOpportunity
	Next                                                 []dashboardAction
	SnapshotAt                                           time.Time
	LastResearch                                         string
	// HeldBack counts listings held at ingestion in the last 7 days; they are not opportunities.
	HeldBack int
}

func (h *Handler) SetWorkerStatus(source func() []worker.LoopStatus) { h.workerStatus = source }
func greeting(now time.Time) string {
	period := "evening"
	if now.Hour() < 12 {
		period = "morning"
	} else if now.Hour() < 18 {
		period = "afternoon"
	}
	return "Good " + period + ", Anthony"
}
func dashboardAgo(at, now time.Time) string {
	if at.IsZero() {
		return "Not recorded"
	}
	if at.After(now) {
		return "Timestamp ahead of server"
	}
	d := now.Sub(at)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		if int(d.Minutes()) == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		if int(d.Hours()) == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	if int(d.Hours()/24) == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
func aiView(runtime *compositionRuntime, c *store.DashboardCounts) dashboardAI {
	a := dashboardAI{Name: providerName(runtime.Compose), ReviewName: providerName(runtime.Review), HourLimit: runtime.Hour, DayLimit: runtime.Day,
		HourUsed: c.HourUsed, DayUsed: c.DayUsed, Reserved: c.Reserved, Failures: c.ProviderFailures}
	a.Configured = runtime.Compose != nil && runtime.Review != nil
	if runtime.Compose != nil {
		a.Model = runtime.Compose.Model
	}
	if runtime.Review != nil {
		a.ReviewModel = runtime.Review.Model
	}
	a.Usable = a.Configured && runtime.Enabled && runtime.Hour >= 6 && runtime.Day >= 6
	switch {
	case !a.Configured:
		a.State = "Provider initialization incomplete"
	case !runtime.Enabled:
		a.State = "Manual composition disabled"
	case runtime.Hour < 6 || runtime.Day < 6:
		a.State = "Request budget not configured (minimum 6)"
	case c.ProviderFailures > 0:
		a.State = "Initialized; recent request failures"
	default:
		a.State = "Initialized; remote availability checked on request"
	}
	if a.DayLimit > 0 {
		a.Percent = min(100, c.DayUsed*100/a.DayLimit)
	}
	return a
}
func (h *Handler) dashboardSnapshot(ctx context.Context) (*dashboardView, error) {
	h.dashboardMu.Lock()
	defer h.dashboardMu.Unlock()
	now := time.Now()
	if h.dashboardCache != nil && now.Sub(h.dashboardCache.SnapshotAt) < 10*time.Second {
		return h.dashboardCache, nil
	}
	if h.db == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	c, err := h.db.DashboardCounts(ctx)
	if err != nil {
		return nil, err
	}
	opportunities, err := h.db.DashboardOpportunities(ctx)
	if err != nil {
		return nil, err
	}
	events, err := h.db.DashboardEvents(ctx)
	if err != nil {
		return nil, err
	}
	v := &dashboardView{Greeting: greeting(now), Counts: c, AI: aiView(h.compositionConfig(), c), Rollout: h.rollout(),
		Health: "System running normally", HealthTone: "green", WorkerSummary: "Research worker status unavailable", SnapshotAt: now,
		Updated: now.Format("15:04:05 MST"), Opportunities: opportunities}
	if now.Sub(h.schemaChecked) > time.Minute {
		h.schemaOK = h.db.CheckSchema(ctx) == nil
		h.schemaChecked = now
	}
	if !c.ProfileActive {
		v.addProblem("Activate a candidate profile before planning outreach", "/profile", "Open profile")
	}
	if !h.schemaOK {
		v.addProblem("Database schema requires attention", "", "")
	}
	if h.redis == nil || h.redis.Ping(ctx).Err() != nil {
		v.addProblem("Activity service unavailable", "", "")
	}
	if !v.AI.Usable {
		v.addProblem(v.AI.State, "", "")
	}
	if c.ProviderFailures > 0 {
		v.addProblem(fmt.Sprintf("%d provider request failures in the past 24 hours", c.ProviderFailures), "", "")
	}
	if c.Failed > 0 {
		v.addProblem(fmt.Sprintf("%d failed or expired compositions", c.Failed), "/compositions?status=failed", "Open compositions")
	}
	if v.Rollout.Mode == send.ModeManualSend && !v.Rollout.ManualSendPermitted {
		for _, reason := range v.Rollout.ReadinessReasons {
			v.addProblem("Manual sending unavailable: "+reason, "", "")
		}
	}
	if c.Reconciliation > 0 {
		v.addProblem(fmt.Sprintf("%d unresolved send attempts; reconcile before retrying", c.Reconciliation), "/messages?state=uncertain", "Review send records")
	}
	populateWorkerView(v, h.workerStatus, now)
	if c.LastResearch != nil {
		v.LastResearch = dashboardAgo(*c.LastResearch, now)
	} else {
		v.LastResearch = "No company researched yet"
	}
	v.Metrics = dashboardMetrics(c)
	if !c.ProfileActive {
		v.Next = append(v.Next, dashboardAction{"Activate your candidate profile", "Approve the facts used to assess opportunities", "/profile"})
	}
	if c.CompositionReview > 0 {
		v.Next = append(v.Next, dashboardAction{fmt.Sprintf("Resolve %d composition reviews", c.CompositionReview), "Semantic review required before approval", "/compositions?attention=semantic_review"})
	}
	if c.ApprovalPending > 0 {
		v.Next = append(v.Next, dashboardAction{fmt.Sprintf("Review %d drafts for approval", c.ApprovalPending), "Reviewed drafts awaiting explicit human approval", "/compositions?attention=approval_pending"})
	}
	if c.OpportunityReview > 0 {
		v.Next = append(v.Next, dashboardAction{fmt.Sprintf("Resolve %d opportunity reviews", c.OpportunityReview), "Selection needs operator attention", "/opportunities?assessment=review_required"})
	}
	// The bottleneck, named directly. A strong match with nowhere to send an application is not
	// a matching problem, and showing it next to the other match counts is what made it look like
	// one for so long.
	if c.StrongMissingDestination > 0 {
		v.Next = append(v.Next, dashboardAction{
			fmt.Sprintf("%d strong %s · %d missing an acceptable destination", c.Strong, plural(c.Strong, "match", "matches"), c.StrongMissingDestination),
			"Contact discovery searches for an application route or a hiring contact",
			"/opportunities?fit=strong_match&recipient=missing"})
	}
	if c.Strong > 0 {
		v.Next = append(v.Next, dashboardAction{fmt.Sprintf("Explore %d strong matches", c.Strong), "Open an opportunity to plan your approach", "/opportunities?fit=strong_match"})
	}
	if c.ResearchQueue > 0 {
		v.Next = append(v.Next, dashboardAction{fmt.Sprintf("Research %d companies", c.ResearchQueue), "Research continues independently of automatic sending", "/companies?research=queue"})
	}
	if c.ContactSearchIdentityBlocked > 0 {
		// Point at the company itself when there is one, so the domain can be corrected in place;
		// otherwise at the filtered list of every affected company.
		text := fmt.Sprintf("%d compan%s cannot be searched for contacts until the website domain is corrected",
			c.ContactSearchIdentityBlocked, plural(c.ContactSearchIdentityBlocked, "y", "ies"))
		href, action := "/companies?contact_search=identity_blocked", "Review companies"
		if ids, err := h.db.IdentityBlockedCompanies(ctx, 2); err == nil && len(ids) == 1 {
			href, action = "/companies/"+strconv.FormatInt(ids[0], 10)+"#company-domain", "Correct the domain"
		}
		v.addProblem(text, href, action)
	}
	if h.seedRegistry != nil {
		failing := 0
		for _, s := range h.seedRegistry.Statuses(ctx, h.redis) {
			if s.Scheduled && !s.LastRun.IsZero() && !s.OK {
				failing++
			}
		}
		if failing > 0 {
			v.addProblem(fmt.Sprintf("%d discovery source%s failed on the last run", failing, plural(failing, "", "s")), "/sources", "Open sources")
		}
	}
	if held, err := h.db.CountHeldListings(ctx, store.HeldListingFilter{Recent: true}); err == nil {
		v.HeldBack = held
	}
	if ambiguous, err := h.db.CountHeldListings(ctx, store.HeldListingFilter{Reason: "ambiguous_company_identity", Recent: true}); err == nil && ambiguous > 0 {
		v.addProblem(fmt.Sprintf("%d listing%s held back because the employer matches more than one company", ambiguous, plural(ambiguous, "", "s")),
			"/sources/held?reason=ambiguous_company_identity", "Review listings")
	}
	// Health is decided after every alert is in. Deciding it earlier is how the dashboard came to
	// say "System running normally" above an alert added later.
	if len(v.Problems) > 0 {
		v.Health = "System needs attention"
		v.HealthTone = "amber"
	}
	if len(v.Next) > 4 {
		v.Next = v.Next[:4]
	}
	activity := []store.DashboardEvent{}
	activity = append(activity, events...)
	if h.redis != nil {
		for _, e := range h.recentActivity(ctx) {
			label := activityLabel(e.Action)
			// Existing activity details can contain upstream errors. Redact configured
			// credentials before putting any detail into a rendered response.
			activity = append(activity, store.DashboardEvent{Action: label, Detail: h.redactActivity(e.Detail), Timestamp: e.Timestamp, Success: e.Success})
		}
	}
	sort.SliceStable(activity, func(i, j int) bool { return activity[i].Timestamp.After(activity[j].Timestamp) })
	for _, e := range activity {
		v.Activity = append(v.Activity, dashboardActivity{Action: e.Action, Detail: e.Detail, Success: e.Success, Ago: dashboardAgo(e.Timestamp, now), Exact: e.Timestamp.Format(time.RFC3339), Href: e.Href})
		if len(v.Activity) == 6 {
			break
		}
	}
	h.dashboardCache = v
	return v, nil
}

// invalidateDashboard drops the cached snapshot, so an alert the operator has just resolved is
// gone on their next look rather than up to ten seconds later.
func (h *Handler) invalidateDashboard() {
	h.dashboardMu.Lock()
	h.dashboardCache = nil
	h.dashboardMu.Unlock()
}

func dashboardMetrics(c *store.DashboardCounts) []dashboardMetric {
	// Tone is empty for an ordinary count and "attention" only for work waiting on a person.
	drafts := c.CompositionReview + c.ApprovalPending
	draftTone := ""
	if drafts > 0 {
		draftTone = "attention"
	}
	return []dashboardMetric{
		{"Companies", c.Companies, "In your workspace", "/companies", "", "", ""},
		{"Opportunities", c.Opportunities, fmt.Sprintf("%d not yet assessed", c.Unassessed), "/opportunities", "", "", "/opportunities?assessment=unassessed"},
		{"Strong matches", c.Strong, "Current assessment", "/opportunities?fit=strong_match", "", "", ""},
		{"Possible matches", c.Possible, "Current assessment", "/opportunities?fit=possible_match", "", "", ""},
		{"Unrelated", c.Unrelated, "Current assessment", "/opportunities?fit=unrelated", "", "", ""},
		{"Drafts needing attention", drafts, fmt.Sprintf("%d review \u00b7 %d approval", c.CompositionReview, c.ApprovalPending), "/compositions?attention=required", draftTone, "", ""},
		{"Sent", c.Sent, "Accepted v2 send attempts", "/messages?state=accepted", "", "", ""},
		{"Replies", c.Replies, fmt.Sprintf("%d conversations with bounces", c.Bounces), "/threads?event=replied", "", "", ""},
		{"Research queue", c.ResearchQueue, "Verified domains ready", "/companies?research=queue", "", "", ""},
		{"Legacy drafts", c.LegacyDrafts, "Historical drafts", "/legacy/drafts", "", "", ""},
	}
}
func populateWorkerView(v *dashboardView, source func() []worker.LoopStatus, now time.Time) {
	if v.Rollout.Mode == send.ModeDisabled {
		v.WorkerSummary = "Research stopped \u00b7 rollout disabled"
	} else if source != nil {
		states := source()
		alive := len(states) == len(worker.ReportedLoops)
		busy := false
		for _, s := range states {
			if !s.LastFailureAt.IsZero() && now.Sub(s.LastFailureAt) < time.Hour {
				v.addProblem("Recent "+s.Name+" worker failures; see recent activity", "", "")
			}
			live := !s.AliveAt.IsZero() && now.Sub(s.AliveAt) < 35*time.Second
			alive = alive && live
			busy = busy || s.Busy
			state := "Waiting for next cycle"
			if s.Busy {
				state = "Working"
			}
			if !live {
				state = "Heartbeat overdue"
			}
			v.Workers = append(v.Workers, dashboardWorker{Name: s.Name, State: state, Alive: live, Busy: s.Busy, LastCycle: dashboardAgo(s.CompletedAt, now), Company: s.Company, Failures: s.Failures, LastCycleExact: s.CompletedAt.Format(time.RFC3339)})
		}
		if alive {
			v.WorkerSummary = "Research workers running"
			if busy {
				v.WorkerSummary += " \u00b7 cycle in progress"
			}
		} else {
			v.addProblem("Research worker heartbeat unavailable or overdue", "", "")
		}
	} else {
		v.addProblem("Research worker runtime unavailable", "", "")
	}
}
func activityLabel(action string) string {
	switch action {
	case "worker:research":
		return "Research cycle"
	case "worker:discover":
		return "Discovery cycle"
	case "worker:enrich", "enrich":
		return "Company research"
	case "worker:verify":
		return "Domain verification"
	case "worker:domain_resolve":
		return "Domain research"
	case "worker:contact_discovery", "contact_discovery":
		return "Contact discovery"
	case "match":
		return "Opportunity assessment"
	}
	return strings.ReplaceAll(strings.TrimPrefix(action, "worker:"), "_", " ")
}
func (h *Handler) redactActivity(detail string) string {
	runtime := h.compositionConfig()
	for _, p := range []*composition.JSONProvider{runtime.Compose, runtime.Review} {
		if p != nil {
			for _, key := range p.Keys {
				if key != "" {
					detail = strings.ReplaceAll(detail, key, "[redacted]")
				}
			}
		}
	}
	for _, name := range []string{"SMTP_PASS", "GOOGLE_SEARCH_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "ADMIN_ACCESS_KEY"} {
		for _, key := range strings.Split(os.Getenv(name), ",") {
			if key = strings.TrimSpace(key); key != "" {
				detail = strings.ReplaceAll(detail, key, "[redacted]")
			}
		}
	}
	return detail
}
func (h *Handler) DashboardSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	v, err := h.dashboardSnapshot(ctx)
	if err != nil {
		http.Error(w, "Live dashboard data unavailable", 503)
		return
	}
	t, err := template.ParseFiles("internal/web/templates/dashboard.html")
	if err != nil {
		http.Error(w, "Dashboard template unavailable", 500)
		return
	}
	var rendered bytes.Buffer
	if err := t.ExecuteTemplate(&rendered, "snapshot", v); err != nil {
		http.Error(w, "Dashboard rendering unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(openExternalLinksInNewTab(rendered.String())))
}

// dashboardProblem is one operational alert. Href and Action are set only when there is a page
// where the problem can be inspected or corrected; purely informational alerts carry neither.
type dashboardProblem struct {
	Text, Href, Action string
}

func (v *dashboardView) addProblem(text, href, action string) {
	v.Problems = append(v.Problems, dashboardProblem{Text: text, Href: href, Action: action})
}
