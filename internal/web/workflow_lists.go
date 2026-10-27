package web

import (
	"github.com/anthonyalando8/prospector/internal/store"
	"net/http"
	"net/url"
	"strconv"
)

func paginationData(path string, values url.Values, total, page int) map[string]any {
	pages := max(1, (total+49)/50)
	page = min(page, pages)
	return map[string]any{"Total": total, "Page": page, "Pages": pages, "CurrentURL": listURL(path, values, page), "ActiveFilters": activeFilters(values), "HasPrev": page > 1, "HasNext": page < pages, "PrevURL": listURL(path, values, page-1), "NextURL": listURL(path, values, page+1), "PageLinks": paginationWindow(page, pages, func(n int) string { return listURL(path, values, n) })}
}
func (h *Handler) compositionList(w http.ResponseWriter, r *http.Request) {
	warnings := []string{}
	v := r.URL.Query()
	f := store.CompositionListFilter{Query: textFilter(v.Get("q"), &warnings), Status: enumFilter(v, "status", "queued,running,review_required,reviewed,failed,discarded", &warnings), Attention: enumFilter(v, "attention", "required,semantic_review,approval_pending", &warnings), CompanyID: positiveID(v, "company_id", &warnings)}
	values := url.Values{}
	for k, s := range map[string]string{"q": f.Query, "status": f.Status, "attention": f.Attention} {
		if s != "" {
			values.Set(k, s)
		}
	}
	if f.CompanyID > 0 {
		values.Set("company_id", strconv.FormatInt(f.CompanyID, 10))
	}
	n, err := h.db.CountCompositionList(r.Context(), f)
	if err != nil {
		http.Error(w, "Cannot count compositions", 500)
		return
	}
	data := paginationData("/compositions", values, n, pageNumber(v))
	rows, err := h.db.ListCompositionPage(r.Context(), f, 50, (data["Page"].(int)-1)*50)
	if err != nil {
		http.Error(w, "Cannot load compositions", 500)
		return
	}
	data["Rows"] = rows
	data["Filter"] = f
	data["Warnings"] = warnings
	renderTemplate(w, "compositions.html", data)
}
func (h *Handler) threadList(w http.ResponseWriter, r *http.Request) {
	warnings := []string{}
	v := r.URL.Query()
	f := store.ThreadListFilter{Query: textFilter(v.Get("q"), &warnings), State: enumFilter(v, "state", "active,replied,bounced,closed,do_not_contact", &warnings), Event: enumFilter(v, "event", "replied,bounced_permanent,bounced_transient,closed,do_not_contact,reconciled,note", &warnings), CompanyID: positiveID(v, "company_id", &warnings)}
	values := url.Values{}
	for k, s := range map[string]string{"q": f.Query, "state": f.State, "event": f.Event} {
		if s != "" {
			values.Set(k, s)
		}
	}
	if f.CompanyID > 0 {
		values.Set("company_id", strconv.FormatInt(f.CompanyID, 10))
	}
	n, err := h.db.CountThreadList(r.Context(), f)
	if err != nil {
		http.Error(w, "Cannot count conversations", 500)
		return
	}
	data := paginationData("/threads", values, n, pageNumber(v))
	rows, err := h.db.ListThreadPage(r.Context(), f, 50, (data["Page"].(int)-1)*50)
	if err != nil {
		http.Error(w, "Cannot load conversations", 500)
		return
	}
	data["Threads"] = rows
	data["Filter"] = f
	data["Warnings"] = warnings
	renderTemplate(w, "threads.html", data)
}
func (h *Handler) Messages(w http.ResponseWriter, r *http.Request) {
	warnings := []string{}
	v := r.URL.Query()
	f := store.MessageListFilter{State: enumFilter(v, "state", "accepted,reserved,sending,uncertain,rejected,transient_failed,cancelled", &warnings), CompanyID: positiveID(v, "company_id", &warnings)}
	values := url.Values{}
	if f.State != "" {
		values.Set("state", f.State)
	}
	if f.CompanyID > 0 {
		values.Set("company_id", strconv.FormatInt(f.CompanyID, 10))
	}
	n, err := h.db.CountMessageList(r.Context(), f)
	if err != nil {
		http.Error(w, "Cannot count send records", 500)
		return
	}
	data := paginationData("/messages", values, n, pageNumber(v))
	rows, err := h.db.ListMessagePage(r.Context(), f, 50, (data["Page"].(int)-1)*50)
	if err != nil {
		http.Error(w, "Cannot load send records", 500)
		return
	}
	data["Rows"] = rows
	data["Filter"] = f
	data["Warnings"] = warnings
	renderTemplate(w, "messages.html", data)
}
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	warnings := []string{}
	q := textFilter(r.URL.Query().Get("q"), &warnings)
	data := map[string]any{"Query": q, "Warnings": warnings, "CurrentURL": listURL("/search", url.Values{"q": []string{q}}, 1)}
	if q != "" {
		cf := store.CompanyFilter{Search: q}
		of := store.OpportunityListFilter{Query: q}
		companies, err := h.db.ListCompanies(r.Context(), cf, 20, 0)
		if err != nil {
			http.Error(w, "Search unavailable", 500)
			return
		}
		opportunities, err := h.db.ListOpportunityPage(r.Context(), of, 20, 0)
		if err != nil {
			http.Error(w, "Search unavailable", 500)
			return
		}
		companyCount, err := h.db.CountCompanies(r.Context(), cf)
		if err != nil {
			http.Error(w, "Search unavailable", 500)
			return
		}
		opportunityCount, err := h.db.CountOpportunityList(r.Context(), of)
		if err != nil {
			http.Error(w, "Search unavailable", 500)
			return
		}
		data["Companies"] = companies
		data["Opportunities"] = opportunities
		data["CompanyCount"] = companyCount
		data["OpportunityCount"] = opportunityCount
	}
	renderTemplate(w, "search.html", data)
}
