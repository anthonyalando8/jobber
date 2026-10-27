package web

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/anthonyalando8/prospector/internal/seed"
	"github.com/anthonyalando8/prospector/internal/store"
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
		"Sources": h.seedRegistry.Statuses(ctx, h.redis), "Counts": counts, "Held": held, "Reasons": heldReasons,
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

// heldReasons explains each hold reason in the operator's terms: what it means and what, if
// anything, can be done about it.
var heldReasons = map[string]struct{ Label, Meaning, Action string }{
	"missing_employer": {"No employer named",
		"The listing reached the adapter without an employer name, so there is no company to attach it to.",
		"If the card markup below shows an employer, the source's page layout changed and the adapter's employer selector needs updating. If it does not, the employer is confidential and the listing cannot be pursued."},
	"missing_source_identity": {"No stable identity",
		"The listing had neither an ID nor a link, so a repeat run could not tell it apart from a new listing.",
		"The source fields recorded below show what the source sent; the adapter needs to read whichever of them is stable."},
	"ambiguous_company_identity": {"Company match is ambiguous",
		"The employer name and its web domain point at different companies already in the workspace, typically the same employer spelled two ways.",
		"If one of the companies listed is this employer, attach the listing to it. The choice is remembered for this employer name. Otherwise leave it held."},
	"source_employer_changed": {"Employer changed",
		"A listing the workspace already holds now names a different employer.",
		"Usually a source correction. Check the original listing; no automatic change is made."},
	"ambiguous_source_identity": {"Duplicate identity",
		"More than one stored listing matches this listing's identity.",
		"Needs a look at the matching stored listings."},
	"missing_or_invalid_domain_candidate": {"No usable domain",
		"No web domain could be derived for a new employer, and a company needs one.",
		"Add the employer yourself on the Companies page with its real domain; the listing attaches on the next run."},
	"company_association_mismatch": {"Company mismatch",
		"The listing was tied to a company whose name does not match.", "Check the original listing."},
}

const heldPageSize = 50

// HeldListings shows the listings behind a held-back count, so the count on the Sources page is
// not a dead end: what each listing was, why it was held, and what can be done.
func (h *Handler) HeldListings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	f := store.HeldListingFilter{Source: q.Get("source"), Reason: q.Get("reason"), Recent: q.Get("all") == "", Resolved: q.Get("resolved") == "1"}
	total, err := h.db.CountHeldListings(ctx, f)
	if err != nil {
		http.Error(w, "Cannot count held listings", http.StatusInternalServerError)
		return
	}
	pages := max(1, (total+heldPageSize-1)/heldPageSize)
	page := min(pageNumber(q), pages)
	rows, err := h.db.ListHeldListings(ctx, f, heldPageSize, (page-1)*heldPageSize)
	if err != nil {
		http.Error(w, "Cannot load held listings", http.StatusInternalServerError)
		return
	}
	type view struct {
		store.HeldListing
		Candidates []store.Company
	}
	items := make([]view, 0, len(rows))
	for _, row := range rows {
		item := view{HeldListing: row}
		if row.Reason == "ambiguous_company_identity" && row.ResolvedAt == nil {
			item.Candidates, _ = h.db.CompanyCandidates(ctx, row.Discovery)
		}
		items = append(items, item)
	}
	urlFor := func(p int) string {
		v := url.Values{}
		for _, key := range []string{"source", "reason", "all", "resolved"} {
			if q.Get(key) != "" {
				v.Set(key, q.Get(key))
			}
		}
		if p > 1 {
			v.Set("page", strconv.Itoa(p))
		}
		return "/sources/held?" + v.Encode()
	}
	explain, ok := heldReasons[f.Reason]
	renderTemplate(w, "held.html", map[string]any{
		"Items": items, "Filter": f, "Total": total, "Page": page, "Pages": pages, "Reasons": heldReasons,
		"Explain": explain, "HasExplain": ok, "ShowAll": q.Get("all") != "", "Attached": q.Get("attached"),
		"HasPrev": page > 1, "HasNext": page < pages, "PrevURL": urlFor(page - 1), "NextURL": urlFor(page + 1), "CurrentURL": urlFor(page),
	})
}

// HeldListingAttach records the operator's decision that a held listing's employer is an existing
// company, stores the listing there, and returns to the list.
func (h *Handler) HeldListingAttach(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	companyID, err := strconv.ParseInt(r.FormValue("company_id"), 10, 64)
	if !ok || err != nil || companyID <= 0 {
		http.Error(w, "Choose a company", http.StatusBadRequest)
		return
	}
	result, err := h.db.AttachHeldListing(r.Context(), id, companyID, "operator")
	if err != nil {
		http.Error(w, "Cannot attach: "+err.Error(), http.StatusConflict)
		return
	}
	if h.assessor != nil {
		if _, err := h.assessor.AssessOpportunity(r.Context(), result.ID); err != nil {
			log.Printf("held attach: assess %d: %v", result.ID, err)
		}
	}
	h.invalidateDashboard()
	h.logActivity(r.Context(), "held:attach", "Attached held listing to company "+strconv.FormatInt(companyID, 10), true)
	back := safeReturn(r.FormValue("return"), "/sources/held")
	http.Redirect(w, r, back, http.StatusSeeOther)
}
