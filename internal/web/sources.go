package web

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/anthonyalando8/prospector/internal/seed"
	"github.com/go-chi/chi/v5"
)

// Sources shows every discovery source: what it reads, when it last ran, whether that worked and
// what it has produced. Before this page a failing source was visible only in server logs, which
// is how the international boards sat idle without anyone noticing.
func (h *Handler) Sources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	counts, err := h.db.SourceCounts(ctx)
	if err != nil {
		http.Error(w, "Cannot load source counts", http.StatusInternalServerError)
		return
	}
	held, err := h.db.IngestionReviewCounts(ctx)
	if err != nil {
		http.Error(w, "Cannot load held listings", http.StatusInternalServerError)
		return
	}
	renderTemplate(w, "sources.html", map[string]any{
		"Sources": h.seedRegistry.Statuses(ctx, h.redis), "Counts": counts, "Held": held,
		"Ran": r.URL.Query().Get("ran"), "Added": r.URL.Query().Get("added"), "Failed": r.URL.Query().Get("error"),
	})
}

// manualSourceTimeout bounds a source run started from the Sources page. The request waits for
// it so the page can say what happened; the slowest source (the Kenyan boards, about twenty
// pages) finishes well inside this.
const manualSourceTimeout = 4 * time.Minute

// SourceRun runs one discovery source now, then classifies and assesses what it found, and
// returns to the Sources page with the outcome. "all" runs every scheduled source.
func (h *Handler) SourceRun(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	names := []string{name}
	if name == "all" {
		names = h.seedRegistry.Scheduled()
	} else if info, ok := seed.InfoFor(name); !ok || !info.Scheduled || !contains(h.seedRegistry.List(), name) {
		http.Error(w, "Unknown source", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), manualSourceTimeout*time.Duration(len(names)))
	defer cancel()
	added, failed := 0, ""
	for _, source := range names {
		result, err := h.seedRegistry.Run(ctx, source, h.db, h.redis)
		label := source
		if info, ok := seed.InfoFor(source); ok {
			label = info.Label
		}
		if err != nil {
			h.logActivity(ctx, "seed:"+source, label+": source failed; see the Sources page", false)
			if failed != "" {
				failed += ", "
			}
			failed += label
			continue
		}
		added += result.Added
		h.logActivity(ctx, "seed:"+source, label+": "+strconv.Itoa(result.Added)+" new opportunities", true)
	}
	if h.assessor != nil {
		if _, _, err := h.assessor.ProcessNew(ctx, 500); err != nil {
			log.Printf("sources: process new listings: %v", err)
		}
	}
	q := url.Values{"ran": {name}, "added": {strconv.Itoa(added)}}
	if failed != "" {
		q.Set("error", failed)
	}
	http.Redirect(w, r, "/sources?"+q.Encode(), http.StatusSeeOther)
}
