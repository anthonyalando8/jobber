package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// templateVarsFor lists which {{vars}} actually get filled for a given segment — shown next to
// the edit box so editing copy doesn't require knowing the Go code to know what's available.
// Every segment gets company/first_name/observation/hypothesis; the rest are conditional.
func templateVarsFor(segment string) []string {
	vars := []string{"{{company}}", "{{first_name}}", "{{observation}}", "{{hypothesis}}"}
	switch segment {
	case "hiring", "hiring_us", "enterprise":
		vars = append(vars, "{{role}}", "{{service_pitch}}")
	case "application":
		vars = append(vars, "{{role}}")
	case "agency":
		vars = append(vars, "{{service_pitch}}")
	}
	if segment == "hiring_us" || segment == "application" {
		vars = append(vars, "{{mailing_address}}")
	}
	return vars
}

// Templates lists every outreach template for editing — the direct fix for "editing copy means
// asking someone to write a SQL migration": GetTemplate reads straight from this table with no
// caching, so a save here takes effect on the very next draft, no restart or deploy involved.
func (h *Handler) Templates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	templates, err := h.db.ListTemplates(ctx)
	if err != nil {
		http.Error(w, "Failed to load templates", http.StatusInternalServerError)
		return
	}

	type templateView struct {
		ID          int64
		Segment     string
		Channel     string
		RoleFilter  string
		Subject     string
		Body        string
		DisplayOnly bool
		Vars        []string
	}

	views := make([]templateView, len(templates))
	for i, t := range templates {
		subject := ""
		if t.Subject != nil {
			subject = *t.Subject
		}
		roleFilter := ""
		if t.RoleFilter != nil {
			roleFilter = *t.RoleFilter
		}
		views[i] = templateView{
			ID:          t.ID,
			Segment:     t.Segment,
			Channel:     t.Channel,
			RoleFilter:  roleFilter,
			Subject:     subject,
			Body:        t.Body,
			DisplayOnly: t.DisplayOnly,
			Vars:        templateVarsFor(t.Segment),
		}
	}

	w.Header().Set("Content-Type", "text/html")
	renderTemplate(w, "templates_admin.html", map[string]interface{}{
		"templates": views,
		"saved":     r.URL.Query().Get("saved") == "1",
	})
}

// TemplateUpdate saves edited subject/body for one template.
func (h *Handler) TemplateUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	body := r.FormValue("body")
	if strings.TrimSpace(body) == "" {
		http.Error(w, "body required", http.StatusBadRequest)
		return
	}

	var subject *string
	if s := strings.TrimSpace(r.FormValue("subject")); s != "" {
		subject = &s
	}

	tmpl, err := h.db.GetTemplateByID(ctx, id)
	if err != nil {
		http.Error(w, "Template not found", http.StatusNotFound)
		return
	}

	if err := h.db.UpdateTemplate(ctx, id, subject, body); err != nil {
		http.Error(w, "Failed to save template", http.StatusInternalServerError)
		return
	}

	h.logActivity(ctx, "template", fmt.Sprintf("Edited %s/%s template", tmpl.Segment, tmpl.Channel), true)
	http.Redirect(w, r, "/templates?saved=1", http.StatusSeeOther)
}
