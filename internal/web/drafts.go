package web

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/go-chi/chi/v5"
)

// draftsPageSize matches companiesPageSize — no particular reason they need to differ.
const draftsPageSize = 50

// draftView shadows DraftListItem's *string fields with plain strings (empty when nil) — the
// underlying struct is fine for Go code, but html/template can't compare a *string with eq/ne
// (requires a basic kind) or print one meaningfully (prints the address, not the value).
type draftView struct {
	*store.DraftListItem
	Subject        string
	Decision       string
	DecisionReason string
}

// Drafts lists every draft regardless of state — the direct fix for "/review only shows drafts
// that need review, so a blocked or needs_manual draft is invisible everywhere." Filterable by
// state/recency/company, with the same numbered pagination companies/sent already use.
func (h *Handler) Drafts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	f := store.DraftFilter{
		State:        r.URL.Query().Get("state"),
		CreatedSince: r.URL.Query().Get("created"),
		Search:       strings.TrimSpace(r.URL.Query().Get("q")),
	}
	page := 0
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p - 1
	}
	limit := draftsPageSize
	offset := page * limit

	rawDrafts, err := h.db.ListDrafts(ctx, f, limit, offset)
	if err != nil {
		log.Printf("Drafts: failed to list drafts (%+v page=%d): %v", f, page, err)
		http.Error(w, "Failed to load drafts", http.StatusInternalServerError)
		return
	}
	// html/template can't compare or print a *string directly (eq/ne require a basic kind, and
	// printing a pointer prints its address, not its value) — dereference to plain strings once
	// here rather than let the template break on any row with a nil Decision/Subject.
	drafts := make([]draftView, len(rawDrafts))
	for i, d := range rawDrafts {
		drafts[i] = draftView{DraftListItem: d}
		if d.Subject != nil {
			drafts[i].Subject = *d.Subject
		}
		if d.Decision != nil {
			drafts[i].Decision = *d.Decision
		}
		if d.DecisionReason != nil {
			drafts[i].DecisionReason = *d.DecisionReason
		}
	}

	total, err := h.db.CountDrafts(ctx, f)
	if err != nil {
		log.Printf("Drafts: failed to count drafts: %v", err)
	}
	totalPages := (total + limit - 1) / limit
	if totalPages < 1 {
		totalPages = 1
	}
	currentPage := page + 1

	urlFor := func(p int) string { return draftsURL(f, p) }
	prevURL, nextURL := "", ""
	if page > 0 {
		prevURL = urlFor(currentPage - 1)
	}
	if currentPage < totalPages {
		nextURL = urlFor(currentPage + 1)
	}

	w.Header().Set("Content-Type", "text/html")
	renderTemplate(w, "drafts.html", map[string]interface{}{
		"drafts":     drafts,
		"state":      f.State,
		"created":    f.CreatedSince,
		"query":      f.Search,
		"page":       currentPage,
		"totalPages": totalPages,
		"total":      total,
		"hasPrev":    page > 0,
		"hasNext":    currentPage < totalPages,
		"prevURL":    prevURL,
		"nextURL":    nextURL,
		"currentURL": urlFor(currentPage),
		"pageLinks":  paginationWindow(currentPage, totalPages, urlFor),
	})
}

func draftsURL(f store.DraftFilter, page int) string {
	v := url.Values{}
	if f.State != "" {
		v.Set("state", f.State)
	}
	if f.CreatedSince != "" {
		v.Set("created", f.CreatedSince)
	}
	if f.Search != "" {
		v.Set("q", f.Search)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return "/drafts"
	}
	return "/drafts?" + v.Encode()
}

// DraftDiscard marks a draft discarded — the one action that frees its company up for a fresh
// draft attempt (CompanyDraft and the autonomous drafting loop both refuse to draft a company
// that already has any non-discarded draft). Redirects back to wherever the request came from
// (the company page, or /drafts itself) so this can be triggered from either.
func (h *Handler) DraftDiscard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	d, err := h.db.GetDraft(ctx, id)
	if err != nil {
		http.Error(w, "Draft not found", http.StatusNotFound)
		return
	}

	if err := h.db.DiscardDraft(ctx, id); err != nil {
		http.Error(w, "Failed to discard draft", http.StatusInternalServerError)
		return
	}

	h.logActivity(ctx, "draft", fmt.Sprintf("Discarded draft #%d for company #%d — free to redraft", id, d.CompanyID), false)

	returnTo := r.FormValue("return_to")
	if strings.HasPrefix(returnTo, "/") && !strings.HasPrefix(returnTo, "//") {
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/drafts", http.StatusSeeOther)
}
