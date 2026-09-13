package web

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/anthonyalando8/prospector/internal/profile"
	"github.com/anthonyalando8/prospector/internal/store"
)

func (h *Handler) Profile(w http.ResponseWriter, r *http.Request) {
	revisions, err := h.db.ListProfileRevisions(r.Context())
	if err != nil {
		http.Error(w, "Cannot load profile", 500)
		return
	}
	type item struct {
		store.ProfileRevision
		JSON string
	}
	items := []item{}
	var active int64
	initial := ""
	for _, revision := range revisions {
		raw, _ := json.MarshalIndent(revision.Document, "", "  ")
		text := string(raw)
		items = append(items, item{revision, text})
		if initial == "" {
			initial = text
		}
		if revision.Active {
			active = revision.ID
		}
	}
	renderTemplate(w, "profile.html", map[string]any{"Revisions": items, "Active": active, "Initial": initial})
}

func (h *Handler) ProfileSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, profile.MaxDocumentBytes*4)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid or oversized form", 400)
		return
	}
	if _, err := h.db.CreateProfileDraft(r.Context(), []byte(r.FormValue("document"))); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/profile", http.StatusSeeOther)
}

func (h *Handler) ProfileActivate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("revision_id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "Invalid revision", 400)
		return
	}
	active, err := strconv.ParseInt(r.FormValue("active_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid observed active revision", 400)
		return
	}
	if r.FormValue("confirm") != "approved" {
		http.Error(w, "Explicit approval of the displayed facts is required", 400)
		return
	}
	if err = h.db.ActivateProfile(r.Context(), id, r.FormValue("hash"), active); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	http.Redirect(w, r, "/profile", http.StatusSeeOther)
}
