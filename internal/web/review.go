package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/anthonyalando8/prospector/internal/store"
)

// parseSkipped parses the "skipped" query/form param — a comma-separated list of draft ids the
// reviewer has already passed over this session — used to keep NextPendingDraft from re-showing
// them immediately.
func parseSkipped(s string) []int64 {
	if s == "" {
		return []int64{}
	}
	parts := strings.Split(s, ",")
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		if id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func joinSkipped(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

// reviewQueueLimit caps how many rows the sidebar "jump to a draft" list shows — the queue
// itself has no hard cap, but a personal-scale review session doesn't need more than this
// visible at once, and it keeps the query cheap.
const reviewQueueLimit = 100

// Review is the main screen: one pending draft at a time, company facts and signals alongside
// an editable subject/body, plus the full queue in a sidebar so a specific draft can be opened
// directly (?draft=<id>) instead of only reachable by skipping through everything ahead of it.
func (h *Handler) Review(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	skipped := parseSkipped(r.URL.Query().Get("skipped"))

	queue, err := h.db.ListReviewQueue(ctx, reviewQueueLimit)
	if err != nil {
		http.Error(w, "Failed to load review queue", http.StatusInternalServerError)
		return
	}

	var dft *store.Draft
	if jumpTo := r.URL.Query().Get("draft"); jumpTo != "" {
		if id, err := strconv.ParseInt(jumpTo, 10, 64); err == nil {
			if d, err := h.db.GetDraft(ctx, id); err == nil && d.State == "pending" &&
				(d.Decision == nil || *d.Decision != "blocked") {
				dft = d
			}
		}
		// An invalid/stale/blocked draft id falls through to the normal skip-based pick below
		// rather than erroring — the queue list itself won't link to anything invalid, but a
		// stale bookmark or a draft that got sent elsewhere in the meantime shouldn't dead-end.
	}
	if dft == nil {
		dft, err = h.db.NextPendingDraft(ctx, skipped)
		if err != nil {
			http.Error(w, "Failed to load next draft", http.StatusInternalServerError)
			return
		}
	}

	if dft == nil {
		w.Header().Set("Content-Type", "text/html")
		renderTemplate(w, "review.html", map[string]interface{}{
			"empty":       true,
			"hasSkipped":  len(skipped) > 0,
			"skippedList": joinSkipped(skipped),
			"queue":       queue,
		})
		return
	}

	company, err := h.db.GetCompany(ctx, dft.CompanyID)
	if err != nil {
		http.Error(w, "Company not found", http.StatusNotFound)
		return
	}

	signals, _ := h.db.GetSignalsByCompany(ctx, dft.CompanyID)
	contacts, _ := h.db.GetContactsByCompany(ctx, dft.CompanyID)

	citedIDs := map[string]bool{}
	if dft.EvidenceUsed != nil {
		for _, idStr := range strings.Split(*dft.EvidenceUsed, ",") {
			idStr = strings.TrimSpace(idStr)
			if idStr != "" {
				citedIDs[idStr] = true
			}
		}
	}

	type signalView struct {
		*store.Signal
		Cited bool
	}
	signalViews := make([]signalView, len(signals))
	for i, s := range signals {
		signalViews[i] = signalView{Signal: s, Cited: citedIDs[strconv.FormatInt(s.ID, 10)]}
	}

	// Dereferenced to plain strings for the template — html/template prints a *string as its
	// address, not its value.
	decision := ""
	if dft.Decision != nil {
		decision = *dft.Decision
	}
	decisionReason := ""
	if dft.DecisionReason != nil {
		decisionReason = *dft.DecisionReason
	}

	w.Header().Set("Content-Type", "text/html")
	renderTemplate(w, "review.html", map[string]interface{}{
		"empty":          false,
		"company":        company,
		"draft":          dft,
		"signals":        signalViews,
		"contacts":       contacts,
		"skippedList":    joinSkipped(skipped),
		"decision":       decision,
		"decisionReason": decisionReason,
		"queue":          queue,
		"activeDraftID":  dft.ID,
	})
}

// reviewRedirect sends the reviewer back to /review, carrying forward the skip list.
func reviewRedirect(w http.ResponseWriter, r *http.Request, skippedList string) {
	url := "/review"
	if skippedList != "" {
		url += "?skipped=" + skippedList
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func saveEditIfChanged(h *Handler, r *http.Request, draftID int64) {
	body := r.FormValue("body")
	if body != "" {
		_ = h.db.UpdateDraftBody(r.Context(), draftID, body)
	}
}

// ReviewSend saves any edit then sends via the same guarded path as the company-page send
// button, and moves on to the next draft.
func (h *Handler) ReviewSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	draftID, _ := strconv.ParseInt(r.FormValue("draft_id"), 10, 64)
	companyID, _ := strconv.ParseInt(r.FormValue("company_id"), 10, 64)
	skippedList := r.FormValue("skipped")

	saveEditIfChanged(h, r, draftID)

	if err := h.executeSend(ctx, companyID, draftID, false); err != nil {
		msg := err.Error()
		status := http.StatusInternalServerError
		if se, ok := err.(*sendError); ok {
			status = se.status
		}
		http.Error(w, msg, status)
		return
	}

	reviewRedirect(w, r, skippedList)
}

// ReviewSave persists an edit without sending, then moves on — the draft stays "pending" so it
// remains sendable later, but is skipped for the rest of this session so it doesn't reappear
// immediately.
func (h *Handler) ReviewSave(w http.ResponseWriter, r *http.Request) {
	draftID, _ := strconv.ParseInt(r.FormValue("draft_id"), 10, 64)
	skipped := parseSkipped(r.FormValue("skipped"))

	saveEditIfChanged(h, r, draftID)

	skipped = append(skipped, draftID)
	reviewRedirect(w, r, joinSkipped(skipped))
}

// ReviewSkip moves to the next draft without changing anything.
func (h *Handler) ReviewSkip(w http.ResponseWriter, r *http.Request) {
	draftID, _ := strconv.ParseInt(r.FormValue("draft_id"), 10, 64)
	skipped := parseSkipped(r.FormValue("skipped"))
	skipped = append(skipped, draftID)
	reviewRedirect(w, r, joinSkipped(skipped))
}

// ReviewNeedsManual marks the draft needs_manual — it naturally drops out of the pending queue.
func (h *Handler) ReviewNeedsManual(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	draftID, _ := strconv.ParseInt(r.FormValue("draft_id"), 10, 64)
	skippedList := r.FormValue("skipped")

	if err := h.db.UpdateDraftState(ctx, draftID, "needs_manual"); err != nil {
		http.Error(w, "Failed to update draft", http.StatusInternalServerError)
		return
	}

	reviewRedirect(w, r, skippedList)
}
