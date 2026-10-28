package web

import (
	"net/http"
	"strconv"

	"github.com/anthonyalando8/prospector/internal/assess"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/go-chi/chi/v5"
)

// Assessor exposes the Phase 3 service to the worker wiring and to tests.
func (h *Handler) Assessor() *assess.Service { return h.assessor }

func pathID(r *http.Request, key string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, key), 10, 64)
	return id, err == nil && id > 0
}

// OpportunityAssess recomputes one vacancy's fit against the active approved profile. It is a
// POST because it writes an assessment; nothing is ever generated on a GET.
func (h *Handler) OpportunityAssess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid opportunity ID", http.StatusBadRequest)
		return
	}
	if _, err := h.assessor.AssessOpportunity(r.Context(), id); err != nil {
		http.Error(w, "Cannot assess opportunity: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.logActivity(r.Context(), "match", "Reassessed opportunity "+strconv.FormatInt(id, 10), true)
	http.Redirect(w, r, "/opportunities/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// CompanyAssess recomputes every vacancy at a company plus its company relevance.
func (h *Handler) CompanyAssess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid company ID", http.StatusBadRequest)
		return
	}
	if _, err := h.assessor.RefreshCompany(r.Context(), id); err != nil {
		http.Error(w, "Cannot assess company: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.logActivity(r.Context(), "match", "Reassessed fit and relevance for company "+strconv.FormatInt(id, 10), true)
	http.Redirect(w, r, "/companies/"+strconv.FormatInt(id, 10)+"/plan", http.StatusSeeOther)
}

// CompanyPlan is the Phase 3 workspace: every vacancy with its classification and reasons, the
// company relevance evidence, ranked recipients, the resolved intent and the exact approved
// facts a future draft would be allowed to use.
func (h *Handler) CompanyPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid company ID", http.StatusBadRequest)
		return
	}
	var selected int64
	if value := r.URL.Query().Get("opportunity"); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed > 0 {
			selected = parsed
		}
	}
	requested := plan.Intent(r.URL.Query().Get("intent"))
	if requested != "" && !requested.Valid() {
		http.Error(w, "Unknown intent", http.StatusBadRequest)
		return
	}
	proposal, err := h.assessor.Propose(r.Context(), id, selected, requested)
	if err != nil {
		http.Error(w, "Cannot build a contact plan proposal: "+err.Error(), http.StatusInternalServerError)
		return
	}
	renderTemplate(w, "plan.html", map[string]any{
		"Proposal": proposal, "CurrentURL": r.URL.RequestURI(), "BackURL": safeReturn(r.URL.Query().Get("return"), "/companies/"+strconv.FormatInt(id, 10)),
		"View":      proposal.View,
		"Requested": string(requested),
		"Selected":  selected,
		"Intents": []map[string]string{
			{"Value": string(plan.SpecificJobApplication), "Label": plan.SpecificJobApplication.Label(), "Name": "Apply for a specific job"},
			{"Value": string(plan.SpeculativeJobEnquiry), "Label": plan.SpeculativeJobEnquiry.Label(), "Name": "Speculative job enquiry"},
			{"Value": string(plan.ServiceOutreach), "Label": plan.ServiceOutreach.Label(), "Name": "Offer services"},
		},
	})
}

// CompanyPlanCreate records an explicit contact plan. It authorizes future draft generation
// only; sending stays disabled by the construction interlock regardless.
func (h *Handler) CompanyPlanCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid company ID", http.StatusBadRequest)
		return
	}
	opportunityID, _ := strconv.ParseInt(r.FormValue("opportunity_id"), 10, 64)
	contactID, _ := strconv.ParseInt(r.FormValue("contact_id"), 10, 64)
	created, err := h.assessor.CreatePlan(r.Context(), assess.CreatePlanRequest{
		CompanyID: id, OpportunityID: opportunityID, ContactID: contactID,
		Intent: plan.Intent(r.FormValue("intent")), Origin: "manual",
		OverrideReason: r.FormValue("override_reason"), OverriddenBy: r.FormValue("overridden_by"),
	})
	if err != nil {
		http.Error(w, "Cannot record this contact plan: "+err.Error(), http.StatusBadRequest)
		return
	}
	message := "Recorded " + created.Intent + " plan " + strconv.FormatInt(created.ID, 10)
	if r.FormValue("override_reason") != "" {
		message += " (blockers overridden by the operator)"
	}
	h.logActivity(r.Context(), "plan", message, true)
	http.Redirect(w, r, "/companies/"+strconv.FormatInt(id, 10)+"/plan", http.StatusSeeOther)
}

// PlanRecipient changes who a plan contacts. This always creates a new plan revision and
// reopens review; it never edits an approved plan in place.
func (h *Handler) PlanRecipient(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid plan ID", http.StatusBadRequest)
		return
	}
	existing, err := h.db.GetContactPlan(r.Context(), id)
	if err != nil {
		http.Error(w, "Plan not found", http.StatusNotFound)
		return
	}
	contactID, err := strconv.ParseInt(r.FormValue("contact_id"), 10, 64)
	if err != nil || contactID <= 0 {
		http.Error(w, "Choose a contact", http.StatusBadRequest)
		return
	}
	proposal, err := h.assessor.Propose(r.Context(), existing.CompanyID, 0, plan.Intent(existing.Intent))
	if err != nil {
		http.Error(w, "Cannot rank recipients: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, option := range proposal.Recipients {
		if option.Assessment.Contact.ID != contactID {
			continue
		}
		if option.Relevance == plan.RecipientUnusable {
			http.Error(w, "That contact cannot receive email: "+option.Reason, http.StatusBadRequest)
			return
		}
		updated, err := h.db.UpdatePlanRecipient(r.Context(), id, contactID,
			option.Assessment.Contact.Value, option.Assessment.Contact.Person, option.Relevance, option.Reason)
		if err != nil {
			http.Error(w, "Cannot change the recipient: "+err.Error(), http.StatusInternalServerError)
			return
		}
		h.logActivity(r.Context(), "plan",
			"Plan "+strconv.FormatInt(id, 10)+" recipient changed; now revision "+strconv.Itoa(updated.Revision), true)
		http.Redirect(w, r, "/companies/"+strconv.FormatInt(existing.CompanyID, 10)+"/plan", http.StatusSeeOther)
		return
	}
	http.Error(w, "That contact does not belong to this company", http.StatusBadRequest)
}

// PlanStatus pauses, reselects or closes a plan. Plans are closed, never deleted.
func (h *Handler) PlanStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid plan ID", http.StatusBadRequest)
		return
	}
	existing, err := h.db.GetContactPlan(r.Context(), id)
	if err != nil {
		http.Error(w, "Plan not found", http.StatusNotFound)
		return
	}
	if err := h.db.SetContactPlanStatus(r.Context(), id, r.FormValue("status")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/companies/"+strconv.FormatInt(existing.CompanyID, 10)+"/plan", http.StatusSeeOther)
}
