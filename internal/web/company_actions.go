package web

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

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
	note := strings.TrimSpace(r.FormValue("note"))

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

	// A status is just a label — what the prospect actually said ("asking about M-Pesa rates")
	// is what makes it useful later. Tagged with the status so the log reads as a history, not
	// just a pile of loose notes.
	if note != "" {
		if err := h.db.AppendCompanyNote(ctx, id, "["+status+"] "+note); err != nil {
			log.Printf("CompanyStatus: failed to append note for company %d: %v", id, err)
		}
	}

	h.logActivity(ctx, "outcome", fmt.Sprintf("Company #%d marked %s", id, status), true)
	http.Redirect(w, r, "/companies/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// CompanyAddNote appends a timestamped note without changing status — for logging something
// (a call, a follow-up question, context for later) that isn't itself an outcome.
func (h *Handler) CompanyAddNote(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	note := strings.TrimSpace(r.FormValue("note"))

	if note == "" {
		http.Error(w, "note required", http.StatusBadRequest)
		return
	}

	if err := h.db.AppendCompanyNote(ctx, id, note); err != nil {
		http.Error(w, "Failed to add note", http.StatusInternalServerError)
		return
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

// ContactAdd adds a new contact to a company by hand — the gap when enrichment finds nothing,
// or finds a dead email and a human has the real one.
func (h *Handler) ContactAdd(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	kind := r.FormValue("kind")
	value := strings.TrimSpace(r.FormValue("value"))
	if kind == "" || value == "" {
		http.Error(w, "kind and value required", http.StatusBadRequest)
		return
	}

	var personName, role *string
	if v := strings.TrimSpace(r.FormValue("person_name")); v != "" {
		personName = &v
	}
	if v := strings.TrimSpace(r.FormValue("role")); v != "" {
		role = &v
	}

	// A human typed this in directly — trust it as the best-quality contact on file, same
	// reasoning as a manually-verified domain.
	if _, err := h.db.CreateContact(ctx, id, kind, value, personName, role, 3); err != nil {
		http.Error(w, "Failed to add contact: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.logActivity(ctx, "contact", fmt.Sprintf("Added %s contact %s", kind, value), true)
	http.Redirect(w, r, "/companies/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// ContactUpdate edits an existing contact's value/name/role — fixing a dead or mistyped email in
// place rather than deleting and re-adding.
func (h *Handler) ContactUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	contactID, _ := strconv.ParseInt(chi.URLParam(r, "contactID"), 10, 64)

	contact, err := h.db.GetContact(ctx, contactID)
	if err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	value := strings.TrimSpace(r.FormValue("value"))
	if value == "" {
		http.Error(w, "value required", http.StatusBadRequest)
		return
	}
	var personName, role *string
	if v := strings.TrimSpace(r.FormValue("person_name")); v != "" {
		personName = &v
	}
	if v := strings.TrimSpace(r.FormValue("role")); v != "" {
		role = &v
	}

	if err := h.db.UpdateContact(ctx, contactID, value, personName, role); err != nil {
		http.Error(w, "Failed to update contact: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.logActivity(ctx, "contact", "Updated contact: "+value, true)
	http.Redirect(w, r, "/companies/"+strconv.FormatInt(contact.CompanyID, 10), http.StatusSeeOther)
}

// ContactDelete removes a contact outright — for a duplicate or a completely wrong entry, as
// opposed to ContactMarkBad which keeps the record but stops it being auto-picked.
func (h *Handler) ContactDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	contactID, _ := strconv.ParseInt(chi.URLParam(r, "contactID"), 10, 64)

	contact, err := h.db.GetContact(ctx, contactID)
	if err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	if err := h.db.DeleteContact(ctx, contactID); err != nil {
		http.Error(w, "Failed to delete contact: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.logActivity(ctx, "contact", "Deleted contact "+contact.Value, true)
	http.Redirect(w, r, "/companies/"+strconv.FormatInt(contact.CompanyID, 10), http.StatusSeeOther)
}

// ContactMarkBad flags a contact as dead — a bounce noticed in the operator's own inbox, or an
// SMTP rejection at send time — so the next auto-pick (send-time recipient selection) skips it
// without losing the record or the reason.
func (h *Handler) ContactMarkBad(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	contactID, _ := strconv.ParseInt(chi.URLParam(r, "contactID"), 10, 64)
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reason == "" {
		reason = "marked dead manually"
	}

	contact, err := h.db.GetContact(ctx, contactID)
	if err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	if err := h.db.MarkContactBad(ctx, contactID, reason); err != nil {
		http.Error(w, "Failed to update contact: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.logActivity(ctx, "contact", fmt.Sprintf("Marked %s as bad: %s", contact.Value, reason), false)
	http.Redirect(w, r, "/companies/"+strconv.FormatInt(contact.CompanyID, 10), http.StatusSeeOther)
}

// ContactReactivate undoes ContactMarkBad — for a contact flagged in error.
func (h *Handler) ContactReactivate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	contactID, _ := strconv.ParseInt(chi.URLParam(r, "contactID"), 10, 64)

	contact, err := h.db.GetContact(ctx, contactID)
	if err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	if err := h.db.ReactivateContact(ctx, contactID); err != nil {
		http.Error(w, "Failed to update contact: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.logActivity(ctx, "contact", "Reactivated contact "+contact.Value, true)
	http.Redirect(w, r, "/companies/"+strconv.FormatInt(contact.CompanyID, 10), http.StatusSeeOther)
}
