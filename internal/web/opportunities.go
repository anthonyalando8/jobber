package web

import (
	"bytes"
	"encoding/json"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// opportunitiesPageSize matches the other listing pages.
const opportunitiesPageSize = 50

func (h *Handler) Opportunities(w http.ResponseWriter, r *http.Request) {
	var companyID int64
	if value := r.URL.Query().Get("company"); value != "" {
		var err error
		companyID, err = strconv.ParseInt(value, 10, 64)
		if err != nil || companyID <= 0 {
			http.Error(w, "Invalid company ID", 400)
			return
		}
	}
	source := r.URL.Query().Get("source")
	title, availability := r.URL.Query().Get("title"), r.URL.Query().Get("availability")

	page := 1
	if value := r.URL.Query().Get("page"); value != "" {
		if n, err := strconv.Atoi(value); err == nil && n > 0 {
			page = n
		}
	}
	total, err := h.db.CountOpportunitiesFiltered(r.Context(), source, companyID, title, availability)
	if err != nil {
		http.Error(w, "Cannot count opportunities", 500)
		return
	}
	pages := (total + opportunitiesPageSize - 1) / opportunitiesPageSize
	if pages == 0 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	rows, err := h.db.ListOpportunitiesFiltered(r.Context(), source, companyID, title, availability,
		opportunitiesPageSize, (page-1)*opportunitiesPageSize)
	if err != nil {
		http.Error(w, "Cannot load opportunities", 500)
		return
	}
	// Filters travel with the page links, so paging never silently drops the filter that
	// produced the list.
	filters := url.Values{}
	for key, value := range map[string]string{"source": source, "title": title, "availability": availability} {
		if value != "" {
			filters.Set(key, value)
		}
	}
	if companyID > 0 {
		filters.Set("company", strconv.FormatInt(companyID, 10))
	}
	query := filters.Encode()
	if query != "" {
		query += "&"
	}
	first := 0
	if total > 0 {
		first = (page-1)*opportunitiesPageSize + 1
	}
	renderTemplate(w, "opportunities.html", map[string]any{
		"Rows": rows, "Source": source, "Company": companyID, "Title": title,
		"Availability": availability, "Total": total, "Page": page, "Pages": pages,
		"Query": template.URL(query), "HasPrev": page > 1, "HasNext": page < pages,
		"Prev": page - 1, "Next": page + 1,
		"First": first, "Last": first + len(rows) - 1,
	})
}

func (h *Handler) Opportunity(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "Invalid opportunity ID", 400)
		return
	}
	history, err := h.db.OpportunityHistory(r.Context(), id)
	if err != nil {
		http.Error(w, "Cannot load opportunity", 404)
		return
	}
	current, err := h.db.GetOpportunity(r.Context(), id)
	if err != nil {
		http.Error(w, "Cannot load opportunity", 500)
		return
	}
	documents := []string{}
	for _, raw := range history {
		var buf bytes.Buffer
		if err = json.Indent(&buf, raw, "", "  "); err != nil {
			http.Error(w, "Invalid stored revision", 500)
			return
		}
		documents = append(documents, buf.String())
	}
	var facts opportunity.DiscoveredOpportunity
	if err = json.Unmarshal(history[0], &facts); err != nil {
		http.Error(w, "Invalid source revision", 500)
		return
	}
	// The stored assessment, its staleness, and the company relevance that would justify
	// contacting this employer at all. Nothing here is generated on this GET: the page shows the
	// last stored assessment and offers an explicit button to recompute it.
	profileState, err := h.assessor.Profile(r.Context())
	if err != nil {
		http.Error(w, "Cannot read candidate profile state", 500)
		return
	}
	stored, err := h.db.LatestMatchForOpportunity(r.Context(), id)
	if err != nil {
		http.Error(w, "Cannot load fit assessment", 500)
		return
	}
	companyRelevance, err := h.db.GetCompanyRelevance(r.Context(), current.CompanyID)
	if err != nil {
		http.Error(w, "Cannot load company relevance", 500)
		return
	}
	data := map[string]any{"ID": id, "Documents": documents, "Facts": facts, "Revision": current.Revision,
		"DiscoveredAt": current.DiscoveredAt, "LastSeenAt": current.LastSeenAt, "UpdatedAt": current.UpdatedAt,
		"CompanyID": current.CompanyID, "Profile": profileState, "Relevance": companyRelevance}
	if stored != nil {
		data["Match"] = stored.Result
		data["Stale"] = stored.Stale
		data["StaleWhy"] = stored.StaleWhy
		data["Assessed"] = stored.Assessed
	}
	renderTemplate(w, "opportunity.html", data)
}
