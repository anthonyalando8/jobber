package web

import (
	"context"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/addresscheck"
	"github.com/anthonyalando8/prospector/internal/automation"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/anthonyalando8/prospector/internal/triage"
)

// dashboardAutomation is the automation card: what the autonomous parts are doing, what they are
// holding back and why, and the switches that control them.
type dashboardAutomation struct {
	// Permitted is the deployment's permission (EMAIL_PIPELINE_MODE=v2_auto with an allowlist and a
	// ready send path). The operator's switch only matters when it is true.
	Permitted     bool
	PermitReasons []string
	On, Paused    bool
	Since         string
	Mode          string // Manual, Automatic or Paused
	Intents       []string

	SentToday, DailyCap, HourlyCap, Remaining int
	AwaitingReview, BlockedWeek               int
	TopReasons                                []dashboardReasonCount

	AutoCompose            bool
	DraftsToday, DraftsCap int

	TriagePaused, TriageSuppress bool
	Triage                       store.TriageCounts

	AddressAI        bool
	AddressAICap     int
	Addresses        store.AddressReviewCounts
	AddressCompanies []store.AddressReviewCompany
}

type dashboardReasonCount struct {
	Code  string
	Count int
}

func (h *Handler) automationView(ctx context.Context, c *store.DashboardCounts) dashboardAutomation {
	a := dashboardAutomation{Mode: "Manual"}
	rollout := h.rollout()
	a.Permitted, a.PermitReasons, a.Intents = rollout.AutomaticSending, rollout.AutomationReasons, rollout.AutoSendIntents
	a.On = h.db.SettingOn(ctx, store.SettingAutoSend)
	a.Paused = h.db.SettingOn(ctx, store.SettingSendingPaused)
	if since, err := h.db.Setting(ctx, store.SettingAutoSendSince, ""); err == nil && since != "" {
		if t, err := time.Parse(time.RFC3339Nano, since); err == nil {
			a.Since = t.Local().Format("2 Jan 15:04")
		}
	}
	switch {
	case a.Paused:
		a.Mode = "Paused"
	case a.On && a.Permitted:
		a.Mode = "Automatic"
	}
	cfg, err := automation.LoadConfig(os.Getenv("EMAIL_PIPELINE_MODE"), os.Getenv("AUTO_SEND_INTENTS"), os.Getenv("AUTO_SEND_DAILY_CAP"))
	if err == nil {
		a.DailyCap, a.HourlyCap = cfg.DailyCap, cfg.HourlyCap
	}
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	a.SentToday, _ = h.db.AutoSentToday(ctx, midnight)
	if a.Remaining = a.DailyCap - a.SentToday; a.Remaining < 0 {
		a.Remaining = 0
	}
	if c != nil {
		a.AwaitingReview = c.CompositionReview + c.ApprovalPending
	}
	week := now.Add(-7 * 24 * time.Hour)
	a.BlockedWeek, _ = h.db.BlockedDraftsSince(ctx, week)
	if counts, err := h.db.AutomationBlockerCounts(ctx, week); err == nil {
		for code, n := range counts {
			a.TopReasons = append(a.TopReasons, dashboardReasonCount{Code: code, Count: n})
		}
		sort.Slice(a.TopReasons, func(i, j int) bool {
			if a.TopReasons[i].Count != a.TopReasons[j].Count {
				return a.TopReasons[i].Count > a.TopReasons[j].Count
			}
			return a.TopReasons[i].Code < a.TopReasons[j].Code
		})
		if len(a.TopReasons) > 5 {
			a.TopReasons = a.TopReasons[:5]
		}
	}
	a.AutoCompose = h.db.SettingOn(ctx, store.SettingAutoCompose)
	a.DraftsCap = 3
	if n, err := strconv.Atoi(os.Getenv("OUTREACH_DAILY_DRAFTS")); err == nil && n >= 0 {
		a.DraftsCap = n
	}
	a.DraftsToday, _ = h.db.AutoComposedSince(ctx, midnight)
	a.TriagePaused = h.db.SettingOn(ctx, store.SettingTriagePaused)
	a.TriageSuppress = h.db.SettingOn(ctx, store.SettingTriageSuppress)
	a.Triage, _ = h.db.TriageStats(ctx, triage.Version, a.TriageSuppress)
	a.AddressAI = h.db.SettingOn(ctx, store.SettingAddressAI)
	a.AddressAICap = addresscheck.DefaultAIDaily
	if n, err := strconv.Atoi(os.Getenv("ADDRESS_AI_DAILY")); err == nil && n >= 0 {
		a.AddressAICap = n
	}
	a.Addresses, _ = h.db.AddressReviewStats(ctx)
	a.AddressCompanies, _ = h.db.AddressReviewCompanies(ctx, 5)
	return a
}

// automationActions maps each control to the setting it writes. Every control is a plain on/off
// switch; nothing here sends, drafts or suppresses anything by itself.
var automationActions = map[string]struct{ key, value, label string }{
	"auto_on":         {store.SettingAutoSend, "on", "Automatic sending switched on"},
	"auto_off":        {store.SettingAutoSend, "off", "Automatic sending switched off"},
	"pause":           {store.SettingSendingPaused, "on", "Automatic sending paused"},
	"resume":          {store.SettingSendingPaused, "off", "Automatic sending resumed"},
	"compose_on":      {store.SettingAutoCompose, "on", "Automatic drafting switched on"},
	"compose_off":     {store.SettingAutoCompose, "off", "Automatic drafting switched off"},
	"triage_pause":    {store.SettingTriagePaused, "on", "Opportunity triage paused"},
	"triage_resume":   {store.SettingTriagePaused, "off", "Opportunity triage resumed"},
	"triage_hide_on":  {store.SettingTriageSuppress, "on", "Triage may now hide high-confidence unrelated postings"},
	"triage_hide_off": {store.SettingTriageSuppress, "off", "Triage only records; it no longer hides postings"},
	"address_ai_on":   {store.SettingAddressAI, "on", "Waiting contact addresses now get an AI opinion in the background"},
	"address_ai_off":  {store.SettingAddressAI, "off", "Background AI opinions on contact addresses switched off"},
}

// AutomationControl flips one operator switch from the dashboard.
func (h *Handler) AutomationControl(w http.ResponseWriter, r *http.Request) {
	action, ok := automationActions[strings.TrimSpace(r.FormValue("action"))]
	if !ok {
		http.Error(w, "Unknown automation control", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if err := h.db.SetSetting(ctx, action.key, action.value, "operator"); err != nil {
		http.Error(w, "Could not change the setting: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Switching automatic sending on starts a new window: only drafts written from now on may
	// be sent automatically, never the backlog that was already waiting.
	if action.key == store.SettingAutoSend && action.value == "on" {
		if err := h.db.SetSetting(ctx, store.SettingAutoSendSince, time.Now().UTC().Format(time.RFC3339Nano), "operator"); err != nil {
			http.Error(w, "Could not record when automatic sending started: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	h.invalidateDashboard()
	h.logActivity(ctx, "automation", action.label, true)
	http.Redirect(w, r, safeReturn(r.FormValue("return"), "/")+"#automation", http.StatusSeeOther)
}
