package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/draft"
	"github.com/anthonyalando8/prospector/internal/store"
)

func (h *Handler) Compositions(w http.ResponseWriter, r *http.Request) { h.compositionList(w, r) }

func (h *Handler) CompositionGenerate(w http.ResponseWriter, r *http.Request) {
	if !h.compositionConfig().Enabled {
		http.Error(w, "Manual composition is disabled; configure its providers and request budget first", 503)
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid plan", 400)
		return
	}
	h.startComposition(w, r, id)
}

// CompositionRecompose discards a draft and composes its plan again, as one deliberate action.
// A draft with a send attempt is part of the send record and is refused.
func (h *Handler) CompositionRecompose(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid draft", 400)
		return
	}
	d, err := h.db.GetComposition(r.Context(), id)
	if err != nil {
		http.Error(w, "Cannot load composition", 404)
		return
	}
	if !h.compositionConfig().Enabled {
		http.Error(w, "Manual composition is disabled; configure its providers and request budget first", 503)
		return
	}
	if err := h.db.DiscardComposition(r.Context(), id, "discarded to compose again"); err != nil {
		if errors.Is(err, store.ErrCompositionNotDiscardable) {
			http.Error(w, err.Error(), 409)
			return
		}
		http.Error(w, "Cannot discard draft", 500)
		return
	}
	h.invalidateDashboard()
	h.logActivity(r.Context(), "composition", "Discarded draft "+strconv.FormatInt(id, 10)+" to compose again", true)
	h.startComposition(w, r, d.Snapshot.PlanID)
}

// CompositionDiscard retires a draft without composing a new one.
func (h *Handler) CompositionDiscard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid draft", 400)
		return
	}
	if err := h.db.DiscardComposition(r.Context(), id, "discarded by operator"); err != nil {
		if errors.Is(err, store.ErrCompositionNotDiscardable) {
			http.Error(w, err.Error(), 409)
			return
		}
		http.Error(w, "Cannot discard draft", 500)
		return
	}
	h.invalidateDashboard()
	h.logActivity(r.Context(), "composition", "Discarded draft "+strconv.FormatInt(id, 10), true)
	http.Redirect(w, r, "/compositions", http.StatusSeeOther)
}

func (h *Handler) startComposition(w http.ResponseWriter, r *http.Request, id int64) {
	if !h.compositionConfig().Enabled {
		http.Error(w, "Manual composition is disabled; configure its providers and request budget first", 503)
		return
	}
	s, err := h.db.BuildCompositionSnapshot(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	composer, err := h.compositionConfig().composer()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	reviewer, err := h.compositionConfig().reviewer()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	hour, day := h.compositionConfig().limits()
	if hour < 6 || day < 6 {
		http.Error(w, "Request quotas must reserve up to six compose/review attempts", 503)
		return
	}
	draftID, err := h.db.QueueComposition(r.Context(), s)
	var exists *store.CompositionExistsError
	if errors.As(err, &exists) {
		// Open the email that already exists instead of paying to write a duplicate.
		http.Redirect(w, r, "/compositions/"+strconv.FormatInt(exists.DraftID, 10)+"?existing=1", http.StatusSeeOther)
		return
	}
	if err != nil {
		http.Error(w, "Cannot queue composition: "+err.Error(), 409)
		return
	}
	owner, err := h.db.ClaimComposition(r.Context(), draftID, 0, 6, hour, day)
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = h.db.CancelQueuedComposition(cleanup, draftID)
		cancel()
		http.Error(w, err.Error(), 429)
		return
	}
	// The HTTP request returns promptly. The explicitly requested run is bounded;
	// crash/lease expiry is visible as failure and never triggers an automatic retry.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
		defer cancel()
		if err := draft.RunComposition(ctx, h.db, draftID, owner, s, composer, reviewer, nil); err != nil {
			log.Printf("Composition draft %d failed; inspect its persisted attempts", draftID)
		}
	}()
	http.Redirect(w, r, "/compositions/"+strconv.FormatInt(draftID, 10), http.StatusSeeOther)
}

func (h *Handler) CompositionView(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid draft", 400)
		return
	}
	d, err := h.db.GetComposition(r.Context(), id)
	if err != nil {
		http.Error(w, "Cannot load composition", 404)
		return
	}
	email := composition.Email{}
	for _, a := range d.Attempts {
		if a.Email.Subject != "" {
			email = a.Email
		}
	}
	renderTemplate(w, "composition.html", map[string]any{
		"Draft": d, "BackURL": safeReturn(r.URL.Query().Get("return"), "/compositions"), "BackLabel": returnLabel(safeReturn(r.URL.Query().Get("return"), "/compositions")), "Email": email, "Send": h.buildSendView(r.Context(), id, email),
		"Fixes": currentFixes(d), "Existing": r.URL.Query().Get("existing") == "1"})
}

func (h *Handler) CompositionEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid draft", 400)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid edit", 400)
		return
	}
	revision, err := strconv.Atoi(r.FormValue("revision"))
	if err != nil {
		http.Error(w, "Missing content revision", 400)
		return
	}
	d, err := h.db.GetComposition(r.Context(), id)
	if err != nil {
		http.Error(w, "Cannot load draft", 404)
		return
	}
	if d.Revision != revision || d.Status == "running" || len(d.StaleReasons) > 0 {
		http.Error(w, "Draft changed, is running or is stale; reload or deliberately regenerate from a current plan", 409)
		return
	}
	if !h.compositionConfig().Enabled {
		http.Error(w, "Manual composition/review is disabled", 503)
		return
	}
	reviewer, err := h.compositionConfig().reviewer()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	email := composition.Email{Intent: d.Snapshot.Intent, Recipient: d.Snapshot.Recipient.Address, OpportunityID: d.Snapshot.OpportunityID}
	for _, a := range d.Attempts {
		if a.Email.Subject != "" {
			email = a.Email
		}
	}
	email.Subject, email.Body = r.FormValue("subject"), r.FormValue("body")
	hour, day := h.compositionConfig().limits()
	owner, err := h.db.ClaimComposition(r.Context(), id, revision, 1, hour, day)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	// Save the edited bytes before the network request, invalidating old review.
	a := composition.Attempt{Source: "human", Email: email, Validation: composition.Validate(d.Snapshot, email), Failure: "review_pending"}
	if err = h.db.RecordCompositionAttempt(r.Context(), id, owner, a); err != nil {
		_ = h.db.FinishComposition(r.Context(), id, owner, "failed")
		http.Error(w, "Could not save edit", 500)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := draft.RunComposition(ctx, h.db, id, owner, d.Snapshot, nil, reviewer, &email); err != nil {
			log.Printf("Composition edit review failed for draft %d", id)
		}
	}()
	http.Redirect(w, r, "/compositions/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}
