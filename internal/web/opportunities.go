package web

import (
	"bytes"
	"encoding/json"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// opportunitiesPageSize matches the other listing pages.
const opportunitiesPageSize = 50

func (h *Handler) Opportunities(w http.ResponseWriter, r *http.Request) {
	f, warnings := opportunityFilter(r.URL.Query())
	page := pageNumber(r.URL.Query())
	total, err := h.db.CountOpportunityList(r.Context(), f)
	if err != nil {
		http.Error(w, "Cannot count opportunities", 500)
		return
	}
	pages := max(1, (total+opportunitiesPageSize-1)/opportunitiesPageSize)
	page = min(page, pages)
	rows, err := h.db.ListOpportunityPage(r.Context(), f, opportunitiesPageSize, (page-1)*opportunitiesPageSize)
	if err != nil {
		http.Error(w, "Cannot load opportunities", 500)
		return
	}
	facets, err := h.db.OpportunityFacets(r.Context())
	if err != nil {
		http.Error(w, "Cannot load filter counts", 500)
		return
	}
	if f.Source != "" {
		found := false
		for _, source := range facets.Sources {
			found = found || source == f.Source
		}
		if !found {
			facets.Sources = append(facets.Sources, f.Source)
		}
	}
	values := opportunityValues(f)
	current := listURL("/opportunities", values, page)
	first := 0
	if total > 0 {
		first = (page-1)*opportunitiesPageSize + 1
	}
	companyName := ""
	if f.CompanyID > 0 {
		company, err := h.db.GetCompany(r.Context(), f.CompanyID)
		if err == nil {
			companyName = company.Name
		}
	}
	renderTemplate(w, "opportunities.html", map[string]any{"Rows": rows, "Filter": f, "Facets": facets, "Warnings": warnings, "ActiveFilters": activeFilters(values), "CompanyName": companyName,
		"Total": total, "Page": page, "Pages": pages, "First": first, "Last": max(0, first+len(rows)-1), "CurrentURL": current,
		"HasPrev": page > 1, "HasNext": page < pages, "PrevURL": listURL("/opportunities", values, page-1), "NextURL": listURL("/opportunities", values, page+1),
		"PageLinks": paginationWindow(page, pages, func(n int) string { return listURL("/opportunities", values, n) })})
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
	// Whether this posting is currently suppressed, and the whole history of having been dropped
	// and put back. The history is shown rather than just the live decision because an automatic
	// suppression an operator already overruled is the thing most worth seeing on this page.
	suppressionHistory, err := h.db.OpportunitySuppressionHistory(r.Context(), id)
	if err != nil {
		http.Error(w, "Cannot load suppression history", 500)
		return
	}
	var suppression *store.OpportunitySuppression
	for i := range suppressionHistory {
		if suppressionHistory[i].Live() {
			suppression = &suppressionHistory[i]
		}
	}
	data := map[string]any{"ID": id, "Documents": documents, "Facts": facts, "Revision": current.Revision,
		"DiscoveredAt": current.DiscoveredAt, "LastSeenAt": current.LastSeenAt, "UpdatedAt": current.UpdatedAt,
		"CompanyID": current.CompanyID, "CompanyName": current.CompanyName, "BackURL": safeReturn(r.URL.Query().Get("return"), "/opportunities"), "BackLabel": returnLabel(safeReturn(r.URL.Query().Get("return"), "/opportunities")), "CurrentURL": r.URL.RequestURI(), "Profile": profileState, "Relevance": companyRelevance,
		"Suppression": suppression, "SuppressionHistory": suppressionHistory, "SuppressionReasons": store.SuppressionReasonChoices()}
	// A source revision published after the suppression is new information: the employer may have
	// reopened the role. Saying so is not the same as lifting the decision, which stays an
	// operator's call.
	if suppression != nil && current.Revision > suppression.OpportunityRevision {
		data["SuppressionOutdated"] = true
	}
	if stored != nil {
		data["Match"] = stored.Result
		data["Stale"] = stored.Stale
		data["StaleWhy"] = stored.StaleWhy
		data["Assessed"] = stored.Assessed
	}
	renderTemplate(w, "opportunity.html", data)
}
