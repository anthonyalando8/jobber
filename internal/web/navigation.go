package web

import (
	"fmt"
	"github.com/anthonyalando8/prospector/internal/store"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

func enumFilter(values url.Values, key string, allowed string, warnings *[]string) string {
	value := strings.ToLower(strings.TrimSpace(values.Get(key)))
	if value == "" {
		return ""
	}
	for _, valid := range strings.Split(allowed, ",") {
		if value == valid {
			return value
		}
	}
	*warnings = append(*warnings, fmt.Sprintf("Ignored invalid %s filter: %q", key, value))
	return ""
}
func textFilter(value string, warnings *[]string) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > 200 {
		*warnings = append(*warnings, "Search/filter text was limited to 200 characters")
		value = string([]rune(value)[:min(200, len([]rune(value)))])
	}
	return value
}
func positiveID(values url.Values, key string, warnings *[]string) int64 {
	value := values.Get(key)
	if value == "" {
		return 0
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		*warnings = append(*warnings, "Ignored invalid "+key)
		return 0
	}
	return id
}
func pageNumber(values url.Values) int {
	n, err := strconv.Atoi(values.Get("page"))
	if err != nil || n < 1 {
		return 1
	}
	return n
}
func opportunityFilter(values url.Values) (store.OpportunityListFilter, []string) {
	warnings := []string{}
	f := store.OpportunityListFilter{}
	f.Query = textFilter(values.Get("q"), &warnings)
	f.Title = textFilter(values.Get("title"), &warnings)
	f.Source = textFilter(values.Get("source"), &warnings)
	f.Fit = enumFilter(values, "fit", "strong_match,possible_match,unrelated", &warnings)
	f.Assessment = enumFilter(values, "assessment", "assessed,unassessed,review_required", &warnings)
	f.Availability = enumFilter(values, "availability", "open,closed,unknown", &warnings)
	f.Description = enumFilter(values, "description", "complete,partial,unavailable", &warnings)
	f.Selection = enumFilter(values, "selection", "new,selected,review_required,ignored", &warnings)
	f.Sort = enumFilter(values, "sort", "pursue,updated,newest,best,company,title,priority", &warnings)
	f.Access = enumFilter(values, "access", "accessible,open,likely,restricted,unknown", &warnings)
	f.Route = enumFilter(values, "route", "direct_contact,employer_site,employer_ats,job_board,unknown", &warnings)
	f.Cost = enumFilter(values, "cost", "not_paid,free,registration,paid,unknown", &warnings)
	f.Tracking = enumFilter(values, "tracking", "active,none,saved,applied,interviewing,offer,rejected,not_interested", &warnings)
	f.Contract = enumFilter(values, "contract", "1", &warnings) == "1"
	f.Recipient = enumFilter(values, "recipient", "acceptable,missing,generic", &warnings)
	f.ContactSearch = enumFilter(values, "contact_search", "pending,claimed,searching,completed,partial,no_result,review_required,identity_blocked,failed", &warnings)
	f.Priority = enumFilter(values, "view", "priority", &warnings) == "priority"
	// A suppressed posting has expired, been filled or been taken down, so the browse page hides
	// it unless asked. Defaulting to "hidden" rather than "any" is the point of the feature: the
	// list is meant to be the work that is still live.
	f.Suppressed = enumFilter(values, "suppressed", "hidden,only,any", &warnings)
	if f.Suppressed == "" {
		f.Suppressed = "hidden"
	}
	f.CompanyID = positiveID(values, "company_id", &warnings)
	if values.Get("company_id") == "" {
		f.CompanyID = positiveID(values, "company", &warnings)
	}
	return f, warnings
}
func opportunityValues(f store.OpportunityListFilter) url.Values {
	v := url.Values{}
	for key, value := range map[string]string{"q": f.Query, "title": f.Title, "fit": f.Fit, "assessment": f.Assessment, "source": f.Source, "availability": f.Availability, "description": f.Description, "selection": f.Selection, "sort": f.Sort, "recipient": f.Recipient, "contact_search": f.ContactSearch,
		"access": f.Access, "route": f.Route, "cost": f.Cost, "tracking": f.Tracking} {
		if value != "" {
			v.Set(key, value)
		}
	}
	if f.CompanyID > 0 {
		v.Set("company_id", strconv.FormatInt(f.CompanyID, 10))
	}
	if f.Priority {
		v.Set("view", "priority")
	}
	if f.Contract {
		v.Set("contract", "1")
	}
	// "hidden" is the default, so it stays out of the URL; the other two are a deliberate choice
	// and must survive pagination and filter changes.
	if f.Suppressed == "only" || f.Suppressed == "any" {
		v.Set("suppressed", f.Suppressed)
	}
	return v
}
func listURL(path string, values url.Values, page int) string {
	copy := url.Values{}
	for k, v := range values {
		copy[k] = append([]string(nil), v...)
	}
	if page > 1 {
		copy.Set("page", strconv.Itoa(page))
	}
	if len(copy) == 0 {
		return path
	}
	return path + "?" + copy.Encode()
}
func activeFilters(values url.Values) []string {
	labels := []string{}
	keys := []string{"q", "fit", "access", "route", "cost", "tracking", "contract", "lead", "business", "assessment", "source", "company_id", "availability", "description", "selection", "recipient", "contact_search", "view", "research", "has_opportunities", "has_strong", "domain_status", "status", "attention", "event", "state", "sort", "suppressed"}
	for _, k := range keys {
		if v := values.Get(k); v != "" {
			labels = append(labels, k+": "+strings.ReplaceAll(v, "_", " "))
		}
	}
	return labels
}

var planReturnPath = regexp.MustCompile(`^/companies/[1-9][0-9]*/plan$`)
var contactSearchReturnPath = regexp.MustCompile(`^/companies/[1-9][0-9]*/contact-search$`)
var entityReturnPath = regexp.MustCompile(`^/(companies|opportunities|compositions|threads)/[1-9][0-9]*$`)

func safeReturn(raw, fallback string) string {
	if raw == "" || len(raw) > 3000 || unsafeReturnCharacters(raw) {
		return fallback
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return fallback
	}
	allowed := map[string]bool{"/sources/held": true, "/sources": true, "/": true, "/opportunities": true, "/companies": true, "/compositions": true, "/threads": true, "/messages": true, "/search": true, "/legacy/drafts": true, "/legacy/threads": true}
	if !allowed[u.Path] && !entityReturnPath.MatchString(u.Path) && !planReturnPath.MatchString(u.Path) &&
		!contactSearchReturnPath.MatchString(u.Path) {
		return fallback
	}
	u.Fragment = ""
	return u.String()
}

func unsafeReturnCharacters(raw string) bool {
	for _, c := range raw {
		if c == 92 || c < 32 {
			return true
		}
	}
	return false
}

func returnLabel(path string) string {
	u, err := url.Parse(path)
	if err != nil {
		return "Back to previous view"
	}
	switch u.Path {
	case "/":
		return "Back to dashboard"
	case "/opportunities":
		return "Back to opportunities"
	case "/companies":
		return "Back to companies"
	case "/compositions":
		return "Back to compositions"
	case "/threads":
		return "Back to conversations"
	case "/messages":
		return "Back to send records"
	case "/search":
		return "Back to search results"
	}
	if planReturnPath.MatchString(u.Path) {
		return "Back to contact plan"
	}
	if contactSearchReturnPath.MatchString(u.Path) {
		return "Back to contact discovery"
	}
	if strings.HasPrefix(u.Path, "/companies/") {
		return "Back to company"
	}
	if strings.HasPrefix(u.Path, "/opportunities/") {
		return "Back to opportunity"
	}
	return "Back to previous view"
}
