package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/anthonyalando8/prospector/internal/addresscheck"
	"github.com/anthonyalando8/prospector/internal/store"
)

func (h *Handler) addressRunner() *addresscheck.Runner {
	if h.addressChecker != nil {
		return h.addressChecker()
	}
	return addresscheck.FromEnvironment(h.db)
}

// addressNotices are the outcomes a candidate action reports back to the page.
var addressNotices = map[string]string{
	"accepted":    "Address accepted. It is now a contact and can be chosen as a recipient.",
	"rejected":    "Address rejected. It is kept with its evidence, and rediscovering it will not reopen it.",
	"checked":     "Address checked.",
	"checked_ai":  "Address checked, with an AI opinion.",
	"ai_skipped":  "Address checked without an AI opinion: ",
	"not_waiting": "That address is no longer waiting for review; someone may have decided it already.",
}

// CandidateAction handles accept, reject and check on one discovered candidate.
func (h *Handler) CandidateAction(w http.ResponseWriter, r *http.Request) {
	companyID, ok := pathID(r, "id")
	candidateID, ok2 := pathID(r, "cid")
	if !ok || !ok2 {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if owner, err := h.candidateCompany(r, candidateID); err != nil || owner != companyID {
		http.Error(w, "Candidate not found for this company", http.StatusNotFound)
		return
	}
	notice, detail := "", ""
	switch action := r.FormValue("action"); action {
	case "accept":
		contact, err := h.db.AcceptContactCandidate(ctx, candidateID, "operator")
		if err != nil {
			notice = "not_waiting"
			break
		}
		notice = "accepted"
		h.logActivity(ctx, "contact", "Accepted discovered address "+contact.Value+" after review", true)
	case "reject":
		if err := h.db.RejectContactCandidate(ctx, candidateID, strings.TrimSpace(r.FormValue("reason"))); err != nil {
			notice = "not_waiting"
			break
		}
		notice = "rejected"
		h.logActivity(ctx, "contact", "Rejected a discovered address after review", true)
	case "check", "check_ai":
		check, note, err := h.addressRunner().CheckOne(ctx, candidateID, action == "check_ai", "operator")
		if err != nil {
			http.Error(w, "The address check failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		switch {
		case action == "check_ai" && note != "":
			notice, detail = "ai_skipped", note
		case check.AIVerdict != store.AIAddressNotRun:
			notice = "checked_ai"
		default:
			notice = "checked"
		}
	default:
		http.Error(w, "Unknown action", http.StatusBadRequest)
		return
	}
	target := "/companies/" + strconv.FormatInt(companyID, 10) + "/contact-search?notice=" + notice
	if detail != "" {
		target += "&detail=" + url.QueryEscape(detail)
	}
	if back := r.FormValue("return"); back != "" {
		target += "&return=" + safeReturnQuery(back)
	}
	http.Redirect(w, r, target+"#candidate-"+strconv.FormatInt(candidateID, 10), http.StatusSeeOther)
}

func (h *Handler) candidateCompany(r *http.Request, candidateID int64) (int64, error) {
	var companyID int64
	err := h.db.QueryRow(r.Context(), `SELECT company_id FROM contact_candidate WHERE id=$1`, candidateID).Scan(&companyID)
	return companyID, err
}

// noticeText turns the notice and detail query parameters into one sentence, or "".
func addressNoticeText(notice, detail string) string {
	text, ok := addressNotices[notice]
	if !ok {
		return ""
	}
	if notice == "ai_skipped" {
		if len(detail) > 200 {
			detail = detail[:200]
		}
		text += detail
	}
	return text
}

// CheckLabel and the methods below keep the template free of logic it cannot express.
func (c contactCandidateView) CheckLabel() string {
	if c.Check == nil {
		return ""
	}
	switch c.Check.Verdict {
	case store.AddressLooksDeliverable:
		return "Looks deliverable"
	case store.AddressDoubtful:
		return "Doubtful"
	case store.AddressWillNotReceive:
		return "Will not receive mail"
	}
	return c.Check.Verdict
}

func (c contactCandidateView) CheckTone() string {
	if c.Check == nil {
		return "neutral"
	}
	switch c.Check.Verdict {
	case store.AddressLooksDeliverable:
		return "green"
	case store.AddressWillNotReceive:
		return "red"
	}
	return "amber"
}

func (c contactCandidateView) AILabel() string {
	if c.Check == nil {
		return ""
	}
	switch c.Check.AIVerdict {
	case addresscheck.AIPlausible:
		return "AI: plausible"
	case addresscheck.AIDoubtful:
		return "AI: doubtful"
	case addresscheck.AIImplausible:
		return "AI: implausible"
	case addresscheck.AIFailed:
		return "AI: no answer"
	}
	return ""
}

func (c contactCandidateView) AITone() string {
	if c.Check == nil {
		return "neutral"
	}
	switch c.Check.AIVerdict {
	case addresscheck.AIPlausible:
		return "green"
	case addresscheck.AIImplausible:
		return "red"
	case addresscheck.AIDoubtful:
		return "amber"
	}
	return "neutral"
}

// Reviewable reports whether a person can accept or reject this candidate now.
func (c contactCandidateView) Reviewable() bool {
	return c.Status == "review_required" && c.Email != ""
}
