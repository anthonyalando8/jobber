package web

import (
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/anthonyalando8/prospector/internal/draft"
	"github.com/anthonyalando8/prospector/internal/sendpolicy"
	"github.com/go-chi/chi/v5"
)

// allowedStatusUpdates are the terminal/manual statuses a human can set from the company page.
// "new"/"enriched"/"drafted"/"approved"/"sent" are pipeline-managed and not settable here.
// The set is a superset of review.md §32's outcome vocabulary (no_response/interested/
// not_interested/wrong_contact/referred/meeting/proposal/won/lost) plus the original
// replied/call/quoted/skip this UI already had wired up — kept rather than replaced so
// existing data and the dashboard's stats stay meaningful.
var allowedStatusUpdates = map[string]bool{
	"replied": true, "call": true, "quoted": true, "won": true, "lost": true, "skip": true,
	"no_response": true, "interested": true, "not_interested": true, "wrong_contact": true,
	"referred": true, "meeting": true, "proposal": true,
}

// statusImpliesReply is the subset of allowedStatusUpdates that means the company actually
// responded — used to keep send_log.replied_at (what the dashboard's "Replies" count and the
// /followups due-list both key off) in sync with outcomes recorded here. "no_response",
// "skip", and "wrong_contact" deliberately don't imply a reply.
var statusImpliesReply = map[string]bool{
	"replied": true, "call": true, "quoted": true, "won": true, "lost": true,
	"interested": true, "not_interested": true, "referred": true, "meeting": true, "proposal": true,
}

// CompanyEnrich enriches a single company — the per-company "enrich" button spec section 6
// calls for on the company detail page.
func (h *Handler) CompanyEnrich(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	company, err := h.db.GetCompany(ctx, id)
	if err != nil {
		http.Error(w, "Company not found", http.StatusNotFound)
		return
	}

	found, err := h.enricher.EnrichCompany(ctx, company, h.db)
	if err != nil {
		http.Error(w, "Enrich failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if found {
		if err := h.scorer.Score(ctx, company, h.db); err != nil {
			log.Printf("CompanyEnrich: scoring failed for %s: %v", company.Name, err)
		}
		h.logActivity(ctx, "enrich", "Enriched "+company.Name+" (from company page)", true)
	} else {
		h.logActivity(ctx, "enrich", company.Name+": nothing extractable", false)
	}

	http.Redirect(w, r, "/companies/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// CompanyDraft generates a draft for a single company — the per-company "draft" button.
func (h *Handler) CompanyDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	if h.aiProvider == nil {
		http.Error(w, "AI provider not configured", http.StatusInternalServerError)
		return
	}

	company, err := h.db.GetCompany(ctx, id)
	if err != nil {
		http.Error(w, "Company not found", http.StatusNotFound)
		return
	}

	existing, _ := h.db.GetLatestDraftForCompany(ctx, id)
	if existing != nil && existing.State != "discarded" {
		http.Error(w, "A draft already exists for this company — discard it first", http.StatusConflict)
		return
	}

	// A human explicitly chose this one company, so the score/cooldown heuristics that gate
	// autonomous candidate selection don't apply here — but the hard hourly/daily quota caps
	// still protect the same scarce AI budget every other path shares.
	if allowed, reason := h.aiBudget.Allow(ctx, false); !allowed {
		h.logActivity(ctx, "draft", "Skipped "+company.Name+": "+reason, false)
		http.Error(w, "AI budget exhausted: "+reason, http.StatusTooManyRequests)
		return
	}

	gen, err := draft.GenerateAndSaveDraft(ctx, h.db, h.draftService, h.SendPolicy(), company, h.redis)
	if gen != nil && gen.APICalled {
		h.aiBudget.Record(ctx)
	}
	if err != nil {
		h.logActivity(ctx, "draft", "Failed to draft "+company.Name+": "+err.Error(), false)
		http.Error(w, "Draft failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.logActivity(ctx, "draft", "Drafted "+company.Name+" ("+gen.State+", from company page)", true)

	// A human clicked this button, but if the resulting draft is confident enough to auto-send
	// on its own merits, there's no reason to make them come back and click Send too.
	if gen.Decision == sendpolicy.AutoSend {
		if err := h.ExecuteSend(ctx, company.ID, gen.DraftID); err != nil {
			log.Printf("CompanyDraft: auto-send failed for %s: %v — left pending for review", company.Name, err)
		} else {
			h.logActivity(ctx, "auto_send", fmt.Sprintf("Auto-sent to %s (score %d)", company.Name, company.Score), true)
		}
	}

	http.Redirect(w, r, "/companies/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// CompanyStatus records a human-decided outcome — replied/call/quoted/won/lost/skip — the
// per-company "skip" button plus the outcome tracking spec section 6 calls for. Without this,
// a company just sits at status=sent forever with no way to record what actually happened.
func (h *Handler) CompanyStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	status := r.FormValue("status")

	if !allowedStatusUpdates[status] {
		http.Error(w, "Unknown status: "+status, http.StatusBadRequest)
		return
	}

	if err := h.db.UpdateCompanyStatus(ctx, id, status); err != nil {
		http.Error(w, "Failed to update status", http.StatusInternalServerError)
		return
	}

	// Keep send_log.replied_at in sync with the outcome — otherwise recording "interested" or
	// "won" here does nothing to stop /followups from still offering a follow-up to someone who
	// plainly already responded, and the dashboard's "Replies" count never reflects it either.
	if statusImpliesReply[status] {
		if err := h.db.MarkRepliedForCompany(ctx, id); err != nil {
			log.Printf("CompanyStatus: failed to mark replied for company %d: %v", id, err)
		}
	}

	http.Redirect(w, r, "/companies/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// CompanyVerifyDomain is the human override for a domain the automatic verifier couldn't
// confirm (or hasn't reached yet) — the developer eyeballs the site and confirms it's really
// this company's, flipping domain_status to "manual" so the research loop will enrich it.
// Without this, a candidate/failed domain the verifier got wrong would be a dead end forever.
func (h *Handler) CompanyVerifyDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	company, err := h.db.GetCompany(ctx, id)
	if err != nil {
		http.Error(w, "Company not found", http.StatusNotFound)
		return
	}

	if err := h.db.UpdateCompanyDomainStatus(ctx, id, "manual", 1.0); err != nil {
		http.Error(w, "Failed to update domain status", http.StatusInternalServerError)
		return
	}

	h.logActivity(ctx, "verify", company.Name+"'s domain manually confirmed", true)
	http.Redirect(w, r, "/companies/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// Suppress adds an email or domain to the suppression list — the other end of the "tell me to
// stop and I won't write again" line every sent email carries. Without this the promise had
// nothing behind it: the table and the send-time check existed, but nothing ever wrote to it.
func (h *Handler) Suppress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	value := r.FormValue("value")
	reason := r.FormValue("reason")

	if value == "" {
		http.Error(w, "value required", http.StatusBadRequest)
		return
	}

	if err := h.db.AddSuppression(ctx, value, reason); err != nil {
		http.Error(w, "Failed to add suppression", http.StatusInternalServerError)
		return
	}

	h.logActivity(ctx, "suppress", value+" will no longer be contacted"+suffixIfReason(reason), true)
	http.Redirect(w, r, "/companies/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func suffixIfReason(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}
