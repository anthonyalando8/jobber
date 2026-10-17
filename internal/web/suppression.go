package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/anthonyalando8/prospector/internal/store"
)

// suppressionNoteLimit keeps a free-text note to something a person will actually read back.
const suppressionNoteLimit = 500

// OpportunitySuppress takes a posting that has expired, been filled or been taken down out of the
// working queues. It is a POST because it writes a decision, and it records a reason rather than
// a bare flag: six months later the useful question is not whether this was dropped but why.
func (h *Handler) OpportunitySuppress(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid opportunity ID", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	known := false
	for _, candidate := range store.SuppressionReasons {
		known = known || candidate == reason
	}
	if !known {
		http.Error(w, "Choose why this opportunity is being suppressed", http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if len([]rune(note)) > suppressionNoteLimit {
		note = string([]rune(note)[:suppressionNoteLimit])
	}
	// An operator's own decision carries no machine evidence beyond the fact that they made it
	// while looking at this page, so that is what is recorded.
	evidence := map[string]any{"decided_from": "opportunity detail page"}
	_, err := h.db.SuppressOpportunity(r.Context(), id, reason, store.BasisOperator, note, "operator", evidence)
	if errors.Is(err, store.ErrAlreadySuppressed) {
		// Two tabs, or a sweep that got there first. The outcome the operator wanted is already
		// true, so this is not an error to shout about.
		h.redirectToOpportunity(w, r, id)
		return
	}
	if err != nil {
		http.Error(w, "Cannot suppress opportunity: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.logActivity(r.Context(), "opportunity",
		"Suppressed opportunity "+strconv.FormatInt(id, 10)+" ("+strings.ReplaceAll(reason, "_", " ")+")", true)
	h.redirectToOpportunity(w, r, id)
}

// OpportunityReinstate puts a suppressed posting back into the working queues, restoring the
// selection status it held before. An automatic suppression reinstated here is not suppressed
// again on the same reasoning: the sweep reads the superseded decision and leaves it alone.
func (h *Handler) OpportunityReinstate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid opportunity ID", http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if len([]rune(note)) > suppressionNoteLimit {
		note = string([]rune(note)[:suppressionNoteLimit])
	}
	_, err := h.db.ReinstateOpportunity(r.Context(), id, "operator", note)
	if errors.Is(err, store.ErrNotSuppressed) {
		h.redirectToOpportunity(w, r, id)
		return
	}
	if err != nil {
		http.Error(w, "Cannot reinstate opportunity: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.logActivity(r.Context(), "opportunity", "Reinstated opportunity "+strconv.FormatInt(id, 10), true)
	h.redirectToOpportunity(w, r, id)
}

// redirectToOpportunity returns to the detail page, keeping whatever list the operator came from
// so suppressing one row does not lose their place in a filtered list.
func (h *Handler) redirectToOpportunity(w http.ResponseWriter, r *http.Request, id int64) {
	target := "/opportunities/" + strconv.FormatInt(id, 10)
	if back := r.FormValue("return"); back != "" {
		if safe := safeReturn(back, ""); safe != "" {
			target += "?return=" + url.QueryEscape(safe)
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
