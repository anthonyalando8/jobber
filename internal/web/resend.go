package web

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// SentResend handles a bounce: a send_log row that looked successful at send time but the
// recipient's mail provider rejected it asynchronously — a bounce notification arriving in the
// human's own inbox minutes or hours later, which this tool has no way to see on its own. It
// records the bounce with a reason, marks the contact that address belongs to as bad so
// auto-pick stops offering it again, clones the original draft as a new pending one, and sends
// the operator straight to /review — reusing the exact same editable "Send to" field and
// retry-on-failure flow a fresh draft already gets, rather than building a second send path.
func (h *Handler) SentResend(w http.ResponseWriter, r *http.Request) {
	if !LegacySendEnabled {
		http.Error(w, "Legacy resend is permanently closed; an accepted message is never re-transmitted, and an uncertain one needs reconciliation.", http.StatusGone)
		return
	}
	ctx := r.Context()
	sendLogID, _ := strconv.ParseInt(chi.URLParam(r, "sendLogID"), 10, 64)
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reason == "" {
		reason = "bounced"
	}

	sl, err := h.db.GetSendLog(ctx, sendLogID)
	if err != nil {
		http.Error(w, "Send log not found", http.StatusNotFound)
		return
	}
	if sl.BouncedAt != nil {
		http.Error(w, "Already marked bounced", http.StatusConflict)
		return
	}

	original, err := h.db.GetDraft(ctx, sl.DraftID)
	if err != nil {
		http.Error(w, "Original draft not found", http.StatusNotFound)
		return
	}

	if err := h.db.MarkBounced(ctx, sendLogID, reason); err != nil {
		log.Printf("SentResend: failed to mark send_log %d bounced: %v", sendLogID, err)
	}

	// The address that bounced shouldn't keep getting auto-picked — find the contact it came
	// from, if it's still on file under the same value, and flag it too.
	if contacts, err := h.db.GetContactsByCompany(ctx, sl.CompanyID); err == nil {
		for _, c := range contacts {
			if c.Kind == "email" && c.Value == sl.ToValue && c.Status != "bad" {
				if err := h.db.MarkContactBad(ctx, c.ID, reason); err != nil {
					log.Printf("SentResend: failed to mark contact %d bad: %v", c.ID, err)
				}
				break
			}
		}
	}

	subject := ""
	if original.Subject != nil {
		subject = *original.Subject
	}
	newDraft, err := h.db.CreateDraft(ctx, sl.CompanyID, nil, "email", subject, original.Body, "", "pending")
	if err != nil {
		http.Error(w, "Failed to prepare resend: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.logActivity(ctx, "bounce", fmt.Sprintf("Marked bounced (%s) for company #%d, prepared resend", reason, sl.CompanyID), false)

	http.Redirect(w, r, "/review?draft="+strconv.FormatInt(newDraft.ID, 10), http.StatusSeeOther)
}
