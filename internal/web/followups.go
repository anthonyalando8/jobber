package web

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/anthonyalando8/prospector/internal/draft"
	"github.com/anthonyalando8/prospector/internal/store"
)

type followupItem struct {
	SendLogID     int64
	Company       *store.Company
	Original      *store.Draft
	ToEmail       string
	FollowUp1Due  bool
	FollowUp1Text string
	FollowUp2Due  bool
	FollowUp2Text string
}

// Followups shows sends whose +3d or +8d follow-up is due, with the original message and the
// pre-filled follow-up text alongside — display/copy-paste, same as the other display-only
// channels in the spec.
func (h *Handler) Followups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	logs, err := h.db.ListFollowupsDue(ctx, time.Now())
	if err != nil {
		http.Error(w, "Failed to load follow-ups", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	items := make([]followupItem, 0, len(logs))

	for _, sl := range logs {
		company, err := h.db.GetCompany(ctx, sl.CompanyID)
		if err != nil {
			continue
		}
		original, err := h.db.GetDraft(ctx, sl.DraftID)
		if err != nil {
			continue
		}

		firstName := "there"
		contacts, _ := h.db.GetContactsByCompany(ctx, sl.CompanyID)
		for _, c := range contacts {
			if c.Kind == "email" && c.Value == sl.ToValue && c.PersonName != nil && *c.PersonName != "" {
				firstName = *c.PersonName
				break
			}
		}

		item := followupItem{SendLogID: sl.ID, Company: company, Original: original, ToEmail: sl.ToValue}
		if sl.FollowUp1Due != nil && !sl.FollowUp1Due.After(now) {
			item.FollowUp1Due = true
			item.FollowUp1Text = draft.RenderFollowUp1(firstName)
		}
		if sl.FollowUp2Due != nil && !sl.FollowUp2Due.After(now) {
			item.FollowUp2Due = true
			item.FollowUp2Text = draft.RenderFollowUp2(company.Name)
		}
		items = append(items, item)
	}

	w.Header().Set("Content-Type", "text/html")
	renderTemplate(w, "followups.html", map[string]interface{}{
		"items": items,
	})
}

// ReviewFollowupSend sends a follow-up — review.md §31: "do not automatically send
// follow-ups... the user should explicitly approve each follow-up." This button is that
// explicit approval. It builds the follow-up text the same way the display-only preview
// already did, saves it as a real draft row, and sends it through the same guarded
// executeSend() path as everything else — just marked isFollowup so the 30-day same-domain
// rule (meant to stop a new unsolicited contact, not a planned follow-up) doesn't block it.
func (h *Handler) ReviewFollowupSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sendLogID, _ := strconv.ParseInt(r.FormValue("send_log_id"), 10, 64)
	which := r.FormValue("which")

	sl, err := h.db.GetSendLog(ctx, sendLogID)
	if err != nil {
		http.Error(w, "Send log not found", http.StatusNotFound)
		return
	}
	if sl.RepliedAt != nil {
		http.Error(w, "This company already replied — no follow-up needed", http.StatusConflict)
		return
	}

	company, err := h.db.GetCompany(ctx, sl.CompanyID)
	if err != nil {
		http.Error(w, "Company not found", http.StatusNotFound)
		return
	}
	original, err := h.db.GetDraft(ctx, sl.DraftID)
	if err != nil {
		http.Error(w, "Original draft not found", http.StatusNotFound)
		return
	}

	firstName := "there"
	contacts, _ := h.db.GetContactsByCompany(ctx, sl.CompanyID)
	for _, c := range contacts {
		if c.Kind == "email" && c.Value == sl.ToValue && c.PersonName != nil && *c.PersonName != "" {
			firstName = *c.PersonName
			break
		}
	}

	var body string
	switch which {
	case "1":
		if sl.FollowUp1SentAt != nil {
			http.Error(w, "Follow-up 1 was already sent", http.StatusConflict)
			return
		}
		body = draft.RenderFollowUp1(firstName)
	case "2":
		if sl.FollowUp2SentAt != nil {
			http.Error(w, "Follow-up 2 was already sent", http.StatusConflict)
			return
		}
		body = draft.RenderFollowUp2(company.Name)
	default:
		http.Error(w, "which must be 1 or 2", http.StatusBadRequest)
		return
	}

	subject := "Following up"
	if original.Subject != nil && *original.Subject != "" {
		subject = "Re: " + *original.Subject
	}

	newDraft, err := h.db.CreateDraft(ctx, sl.CompanyID, nil, "email", subject, body, "", "pending")
	if err != nil {
		http.Error(w, "Failed to save follow-up draft", http.StatusInternalServerError)
		return
	}

	if err := h.executeSend(ctx, sl.CompanyID, newDraft.ID, true, ""); err != nil {
		msg := err.Error()
		status := http.StatusInternalServerError
		if se, ok := err.(*sendError); ok {
			status = se.status
			msg = se.msg
		}
		h.logActivity(ctx, "followup", fmt.Sprintf("Failed to send follow-up %s to %s: %s", which, company.Name, msg), false)
		http.Error(w, msg, status)
		return
	}

	var markErr error
	if which == "1" {
		markErr = h.db.MarkFollowup1Sent(ctx, sendLogID)
	} else {
		markErr = h.db.MarkFollowup2Sent(ctx, sendLogID)
	}
	if markErr != nil {
		log.Printf("ReviewFollowupSend: sent but failed to mark followup %s sent for send_log %d: %v", which, sendLogID, markErr)
	}

	h.logActivity(ctx, "followup", fmt.Sprintf("Sent follow-up %s to %s (%s)", which, company.Name, sl.ToValue), true)
	http.Redirect(w, r, "/followups", http.StatusSeeOther)
}
