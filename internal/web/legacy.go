package web

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/anthonyalando8/prospector/internal/automation"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/store"
)

// legacyDraftExplanation is the one wording used everywhere a person meets a pre-v2 draft. It
// says why the draft cannot be used and what to do instead, rather than reporting a failure.
const legacyDraftExplanation = "This draft was created by the legacy pipeline and does not carry the " +
	"candidate profile, opportunity, recipient and review context that v2 requires. It is kept exactly as " +
	"written, for reference. To contact this company now, create or select a contact plan and generate a " +
	"new composition, which is then validated and reviewed before it can be approved."

// LegacyDrafts lists pre-v2 unsent drafts. They are readable and permanently unsendable; the
// page exists so they are visible and explained rather than quietly filtered out of the UI.
func (h *Handler) LegacyDrafts(w http.ResponseWriter, r *http.Request) {
	drafts, err := h.db.ListLegacyUnsentDrafts(r.Context(), 200)
	if err != nil {
		http.Error(w, "Cannot load legacy drafts", http.StatusInternalServerError)
		return
	}
	renderTemplate(w, "legacy_drafts.html", map[string]any{
		"Drafts": drafts, "Explanation": legacyDraftExplanation,
	})
}

// LegacyThreads lists pre-v2 sent messages and what, if anything, a person has concluded about
// each one's original purpose.
func (h *Handler) LegacyThreads(w http.ResponseWriter, r *http.Request) {
	onlyUnclassified := r.URL.Query().Get("filter") == "unclassified"
	threads, err := h.db.ListLegacyThreads(r.Context(), onlyUnclassified, 200)
	if err != nil {
		http.Error(w, "Cannot load legacy threads", http.StatusInternalServerError)
		return
	}
	unclassified := 0
	for _, t := range threads {
		if t.Classification == nil {
			unclassified++
		}
	}
	renderTemplate(w, "legacy_threads.html", map[string]any{
		"Threads": threads, "OnlyUnclassified": onlyUnclassified, "Unclassified": unclassified,
		"Intents": []string{
			string(plan.SpecificJobApplication), string(plan.SpeculativeJobEnquiry),
			string(plan.ServiceOutreach), store.LegacyIntentUnknown,
		},
	})
}

// LegacyThreadClassify records what a person concluded about one old thread. It is the only way
// an old thread acquires an intent: nothing infers one from the company's segment, the old
// application flag, the template that produced the message, or its wording.
func (h *Handler) LegacyThreadClassify(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid thread", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid classification", http.StatusBadRequest)
		return
	}
	classification := store.LegacyClassification{
		SendLogID:    id,
		Intent:       r.FormValue("intent"),
		Basis:        strings.TrimSpace(r.FormValue("basis")),
		ClassifiedBy: "operator",
	}
	// Pinned references are optional and are accepted only alongside a determinate intent. The
	// store enforces the same rule; this is the friendlier half of it.
	if v := strings.TrimSpace(r.FormValue("opportunity_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			classification.OpportunityID = &n
		}
	}
	if v := strings.TrimSpace(r.FormValue("recipient_contact_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			classification.RecipientContactID = &n
		}
	}
	if _, err := h.db.ClassifyLegacyThread(r.Context(), classification); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/legacy/threads", http.StatusSeeOther)
}

// ---------------------------------------------------------------- closed legacy controls

// legacyClosed answers a legacy control with 410 and an explanation of the v2 route that
// replaces it. A dead end with an actionable next step beats a 500 that looks like a bug.
func legacyClosed(w http.ResponseWriter, explanation string) {
	http.Error(w, explanation, http.StatusGone)
}

// LegacyGenerateClosed replaces the old per-company and batch AI generation. Those paths wrote
// straight into a template with no profile, opportunity, evidence selection or review.
func (h *Handler) LegacyGenerateClosed(w http.ResponseWriter, r *http.Request) {
	legacyClosed(w, "Legacy draft generation is permanently closed. It filled a template from company "+
		"signals with no candidate profile, no selected opportunity, no evidence selection and no review. "+
		"Open the company, select or create a contact plan, and generate a v2 composition instead.")
}

// LegacyTemplateEditClosed keeps the old templates readable but frozen. They are historical
// copy; the text v2 actually uses lives in versioned prompt files, not in this table.
func (h *Handler) LegacyTemplateEditClosed(w http.ResponseWriter, r *http.Request) {
	legacyClosed(w, "Legacy templates are read-only. They are retained as history and no longer produce "+
		"any outgoing message. v2 composition is driven by the versioned prompts in internal/composition/prompts, "+
		"which are changed in the repository and reviewed, not edited live.")
}

// ---------------------------------------------------------------- review-only rollout state

// rolloutView is the operational state shown on the dashboard and the review screens. It is
// computed, never a hard-coded claim about what the system is currently allowed to do.
type rolloutView struct {
	Mode                string
	SendReady           bool
	ReadinessReasons    []string
	AutomaticSending    bool
	AutomationReasons   []string
	AutoSendIntents     []string
	AutomationDailyCap  int
	ManualSendPermitted bool
}

func (h *Handler) rollout() rolloutView {
	v := rolloutView{AutoSendIntents: autoSendIntents(), AutomationDailyCap: automationDailyCap()}
	if h.dispatcher == nil {
		v.Mode = os.Getenv("EMAIL_PIPELINE_MODE")
		return v
	}
	readiness := h.dispatcher.Readiness()
	state := h.dispatcher.Automation()
	v.Mode = h.dispatcher.Mode()
	v.SendReady = readiness.Ready
	v.ReadinessReasons = readiness.Reasons
	// Automation is computed, never asserted: "enabled" here means the policy may be consulted,
	// not that any particular draft will be sent.
	v.AutomaticSending = state.Enabled
	v.AutomationReasons = state.Reasons
	if len(state.Allowlist) > 0 {
		v.AutoSendIntents = state.Allowlist
	}
	v.ManualSendPermitted = readiness.Ready
	return v
}

// automationDailyCap reports the configured automation cap for display. A parse failure shows
// the safe default rather than a number that was never accepted.
func automationDailyCap() int {
	cfg, err := automation.LoadConfig(os.Getenv("EMAIL_PIPELINE_MODE"),
		os.Getenv("AUTO_SEND_INTENTS"), os.Getenv("AUTO_SEND_DAILY_CAP"))
	if err != nil {
		return automation.DefaultConfig().DailyCap
	}
	return cfg.DailyCap
}

// autoSendIntents reads the automation allowlist. It is empty in Phase 6 and the parser exists
// so that "empty" is a checked fact rather than an assumption: a value set by accident shows up
// in the dashboard and in the tests instead of silently taking effect.
func autoSendIntents() []string {
	raw := strings.TrimSpace(os.Getenv("AUTO_SEND_INTENTS"))
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	intents := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			intents = append(intents, p)
		}
	}
	return intents
}

// ---------------------------------------------------------------- review reasons

// reviewReason turns one composition state into a specific, actionable reason. The point of
// keeping these distinct rather than collapsing them into "needs review" is that Phase 7 has to
// decide which categories could ever be safe to automate, and that decision needs the category.
type reviewReason struct {
	Code   string
	Text   string
	Action string
}

// compositionReviewReasons explains why a composition is not ready for approval. An empty
// result means the draft is reviewable on its own terms, not that it may be sent.
func compositionReviewReasons(d *store.ComposedDraft) []reviewReason {
	reasons := []reviewReason{}
	if d == nil {
		return reasons
	}
	for _, stale := range d.StaleReasons {
		reasons = append(reasons, reviewReason{
			Code: "stale_" + stale, Text: "An input changed after this draft was written: " + stale + ".",
			Action: "Regenerate the composition from the current contact plan.",
		})
	}
	switch d.Status {
	case "queued":
		reasons = append(reasons, reviewReason{Code: "composition_queued",
			Text:   "Composition has been requested but has not run yet.",
			Action: "Wait for the run to finish, or inspect the draft if it has been queued for a long time."})
	case "running":
		reasons = append(reasons, reviewReason{Code: "composition_running",
			Text: "A compose/review run is in progress.", Action: "Refresh this page."})
	case "failed":
		reasons = append(reasons, reviewReason{Code: "composition_failed",
			Text:   "The compose/review run did not finish. Its attempts are recorded below.",
			Action: "Inspect the recorded attempts, then regenerate deliberately. Nothing retries on its own."})
	}
	if d.Revision == 0 || len(d.Attempts) < d.Revision {
		return reasons
	}
	last := d.Attempts[d.Revision-1]
	for _, f := range last.Validation {
		if f.Severity == "info" {
			continue
		}
		reasons = append(reasons, reviewReason{Code: f.Code, Text: f.Explanation,
			Action: "Deterministic check. Correct the text or the approved facts it relies on."})
	}
	if last.Review == nil {
		reasons = append(reasons, reviewReason{Code: "review_unavailable",
			Text:   "No semantic review result is recorded for this revision.",
			Action: "Re-run review. A missing review can never be treated as a pass."})
		return reasons
	}
	if !last.Review.Complete {
		reasons = append(reasons, reviewReason{Code: "review_incomplete",
			Text:   "The semantic reviewer did not return a complete result.",
			Action: "Re-run review; an incomplete result is not a pass."})
	}
	for _, f := range last.Review.Findings {
		if f.Severity == "info" {
			continue
		}
		reasons = append(reasons, reviewReason{Code: f.Code, Text: f.Explanation,
			Action: "Semantic finding. Judge it against the source evidence, then edit or accept deliberately."})
	}
	return reasons
}

// legacyDraftReviewReason is the single reason a pre-v2 draft can never be approved. It is a
// category of its own so the review backlog does not blur it together with a v2 finding.
func legacyDraftReviewReason() reviewReason {
	return reviewReason{Code: "legacy_draft", Text: legacyDraftExplanation,
		Action: "Create or select a contact plan for this company and generate a v2 composition."}
}
