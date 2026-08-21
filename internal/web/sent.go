package web

import (
	"net/http"
	"strconv"
)

// sentPageSize matches companiesPageSize — no particular reason they need to differ.
const sentPageSize = 50

// Sent shows every email actually sent, newest first, with the exact subject/body that went out
// and whether it's been marked replied — the direct answer to "the dashboard says 5 sent, how do
// I see them" instead of cross-referencing companies and drafts by hand.
func (h *Handler) Sent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	pageStr := r.URL.Query().Get("page")
	page := 0
	if pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p - 1
		}
	}
	limit := sentPageSize
	offset := page * limit

	emails, err := h.db.ListSentEmails(ctx, limit, offset)
	if err != nil {
		http.Error(w, "Failed to load sent emails", http.StatusInternalServerError)
		return
	}

	total, err := h.db.CountSentEmails(ctx)
	if err != nil {
		total = 0
	}
	totalPages := (total + limit - 1) / limit
	if totalPages < 1 {
		totalPages = 1
	}

	prevURL, nextURL := "", ""
	if page > 0 {
		prevURL = "/sent?page=" + strconv.Itoa(page)
	}
	if page+1 < totalPages {
		nextURL = "/sent?page=" + strconv.Itoa(page+2)
	}

	w.Header().Set("Content-Type", "text/html")
	renderTemplate(w, "sent.html", map[string]interface{}{
		"emails":     emails,
		"page":       page + 1,
		"totalPages": totalPages,
		"total":      total,
		"hasPrev":    page > 0,
		"hasNext":    page+1 < totalPages,
		"prevURL":    prevURL,
		"nextURL":    nextURL,
	})
}
