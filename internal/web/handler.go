package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/aibudget"
	"github.com/anthonyalando8/prospector/internal/domainsearch"
	"github.com/anthonyalando8/prospector/internal/draft"
	"github.com/anthonyalando8/prospector/internal/enrich"
	"github.com/anthonyalando8/prospector/internal/score"
	"github.com/anthonyalando8/prospector/internal/seed"
	"github.com/anthonyalando8/prospector/internal/sendpolicy"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/anthonyalando8/prospector/internal/verify"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

// Handler contains the dependencies for HTTP handlers
type Handler struct {
	db                 *store.DB
	redis              *redis.Client
	aiProvider         draft.AIProvider
	draftService       *draft.TemplateService
	seedRegistry       *seed.Registry
	enricher           *enrich.Enricher
	scorer             *score.Scorer
	verifier           verify.Verifier
	domainSearcher     *domainsearch.Searcher
	aiBudget           *aibudget.Budget
	sendPolicy         sendpolicy.Config
	smtpHost           string
	smtpPort           string
	smtpUser           string
	smtpPass           string
	fromName           string
	fromEmail          string
	replyTo            string
	dailySendLimit     int
	minSendIntervalSec int
	draftMinInterval   time.Duration
}

// NewHandler creates a new handler with dependencies
func NewHandler(db *store.DB, redisConn *redis.Client) *Handler {
	aiProvider, err := draft.NewProvider()
	if err != nil {
		log.Printf("Warning: AI provider not configured: %v", err)
	}

	// Crawl politeness settings were previously hardcoded here despite being documented,
	// configurable-sounding entries in .env.example — actually read them now.
	crawlUserAgent := os.Getenv("CRAWL_USER_AGENT")
	if crawlUserAgent == "" {
		crawlUserAgent = "Prospector/1.0"
	}
	crawlDelayMS := 1200
	if v := os.Getenv("CRAWL_DELAY_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			crawlDelayMS = n
		}
	}

	// Initialize seeders registry
	seedRegistry := seed.NewRegistry()
	seedRegistry.Register(seed.NewJobBoardSeeder(crawlUserAgent))
	seedRegistry.Register(seed.NewRemoteOKSeeder(crawlUserAgent))
	seedRegistry.Register(seed.NewWeWorkRemotelySeeder(crawlUserAgent))
	seedRegistry.Register(seed.NewRemotiveSeeder(crawlUserAgent))
	seedRegistry.Register(seed.NewHimalayasSeeder(crawlUserAgent))
	seedRegistry.Register(seed.NewManualSeeder("", crawlUserAgent))

	// Initialize enricher
	enricher := enrich.NewEnricher(redisConn, crawlDelayMS, crawlUserAgent)

	// Initialize scorer
	scorer := score.NewScorer()

	// Initialize domain verifier — see internal/verify for why a guessed domain never reaches
	// the enricher directly.
	verifier := verify.NewHeuristicVerifier(crawlUserAgent)

	// Search-based domain resolver for companies stuck at domain_status='failed' — every guessed
	// candidate from verify.CandidateDomains already exhausted. Enabled() reports false (and
	// every caller skips gracefully) when the two env vars aren't set — no API key is not an
	// error, same as an unconfigured AI provider.
	domainSearchDailyLimit := 90
	if v := os.Getenv("GOOGLE_SEARCH_DAILY_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			domainSearchDailyLimit = n
		}
	}
	domainSearcher := domainsearch.NewSearcher(redisConn, os.Getenv("GOOGLE_SEARCH_API_KEY"), os.Getenv("GOOGLE_SEARCH_CX"), domainSearchDailyLimit)

	// Get SMTP settings from env
	smtpHost := os.Getenv("SMTP_HOST")
	if smtpHost == "" {
		smtpHost = "smtp.gmail.com"
	}

	smtpPort := os.Getenv("SMTP_PORT")
	if smtpPort == "" {
		smtpPort = "587"
	}

	dailySendLimit := 20
	if limit := os.Getenv("DAILY_SEND_LIMIT"); limit != "" {
		if n, err := strconv.Atoi(limit); err == nil {
			dailySendLimit = n
		}
	}

	minSendIntervalSec := 90
	if interval := os.Getenv("MIN_SEND_INTERVAL_SECONDS"); interval != "" {
		if n, err := strconv.Atoi(interval); err == nil {
			minSendIntervalSec = n
		}
	}

	smtpUser := os.Getenv("SMTP_USER")

	// FROM_NAME/FROM_EMAIL/REPLY_TO are documented in .env.example but were never actually
	// read — sendSMTP used the raw SMTP username as the visible From with no Reply-To at
	// all. Fall back to the SMTP user so behavior is unchanged when they're unset.
	fromName := os.Getenv("FROM_NAME")
	fromEmail := os.Getenv("FROM_EMAIL")
	if fromEmail == "" {
		fromEmail = smtpUser
	}
	replyTo := os.Getenv("REPLY_TO")

	// Default pacing comes from the provider itself (Gemini: divided across however many keys
	// are pooled; Claude: none needed) — DRAFT_MIN_INTERVAL_SECONDS overrides it if set.
	draftMinInterval := 13 * time.Second
	if aiProvider != nil {
		draftMinInterval = aiProvider.MinInterval()
	}
	if interval := os.Getenv("DRAFT_MIN_INTERVAL_SECONDS"); interval != "" {
		if n, err := strconv.Atoi(interval); err == nil && n >= 0 {
			draftMinInterval = time.Duration(n) * time.Second
		}
	}

	// AI generation budget (review.md §6) — treats AI drafting as a scarce resource shared by
	// the manual "Draft Enriched" batch, the single-company button, and the autonomous worker.
	budgetCfg := aibudget.DefaultConfig()
	if v := os.Getenv("AI_ENABLED"); v != "" {
		budgetCfg.Enabled = v != "false" && v != "0"
	}
	if v := os.Getenv("AI_DRAFTS_PER_HOUR"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			budgetCfg.DraftsPerHour = n
		}
	}
	if v := os.Getenv("AI_DRAFTS_PER_DAY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			budgetCfg.DraftsPerDay = n
		}
	}
	if v := os.Getenv("AI_MIN_SCORE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			budgetCfg.MinScore = n
		}
	}
	if v := os.Getenv("AI_DRAFT_COOLDOWN_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			budgetCfg.CooldownSeconds = n
		}
	}
	aiBudget := aibudget.New(redisConn, budgetCfg)

	// Send policy (review.md's "Critical Workflow Change") — decides AUTO_SEND vs
	// REVIEW_REQUIRED vs BLOCKED per draft. Conservative defaults: a score in between
	// ReviewMinScore and AutoSendMinScore gets a human, not silence.
	policyCfg := sendpolicy.DefaultConfig()
	if v := os.Getenv("AUTO_SEND_ENABLED"); v != "" {
		policyCfg.Enabled = v != "false" && v != "0"
	}
	if v := os.Getenv("AUTO_SEND_MIN_SCORE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			policyCfg.AutoSendMinScore = n
		}
	}
	if v := os.Getenv("REVIEW_MIN_SCORE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			policyCfg.ReviewMinScore = n
		}
	}
	if v := os.Getenv("AUTO_SEND_REQUIRE_FRESH_EVIDENCE"); v != "" {
		policyCfg.RequireFreshEvidence = v != "false" && v != "0"
	}
	if v := os.Getenv("AUTO_SEND_MAX_EVIDENCE_AGE_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			policyCfg.MaxEvidenceAge = time.Duration(n) * time.Hour
		}
	}

	return &Handler{
		db:                 db,
		redis:              redisConn,
		aiProvider:         aiProvider,
		draftService:       draft.NewTemplateService(aiProvider, db),
		seedRegistry:       seedRegistry,
		enricher:           enricher,
		scorer:             scorer,
		verifier:           verifier,
		domainSearcher:     domainSearcher,
		aiBudget:           aiBudget,
		sendPolicy:         policyCfg,
		smtpHost:           smtpHost,
		smtpPort:           smtpPort,
		smtpUser:           smtpUser,
		smtpPass:           os.Getenv("SMTP_PASS"),
		fromName:           fromName,
		fromEmail:          fromEmail,
		replyTo:            replyTo,
		dailySendLimit:     dailySendLimit,
		minSendIntervalSec: minSendIntervalSec,
		draftMinInterval:   draftMinInterval,
	}
}

// Dependencies below are exposed so main.go can hand the exact same instances to
// internal/worker's Scheduler instead of constructing a second copy (and re-reading the same
// env vars a second time) — NewHandler stays the one place that builds them.

// SeedRegistry returns the seeder registry this handler uses.
func (h *Handler) SeedRegistry() *seed.Registry { return h.seedRegistry }

// Enricher returns the enricher this handler uses.
func (h *Handler) Enricher() *enrich.Enricher { return h.enricher }

// Scorer returns the scorer this handler uses.
func (h *Handler) Scorer() *score.Scorer { return h.scorer }

// Verifier returns the domain verifier this handler uses.
func (h *Handler) Verifier() verify.Verifier { return h.verifier }

// DomainSearcher returns the search-based domain resolver this handler uses.
func (h *Handler) DomainSearcher() *domainsearch.Searcher { return h.domainSearcher }

// DraftService returns the draft template service this handler uses.
func (h *Handler) DraftService() *draft.TemplateService { return h.draftService }

// AIBudget returns the AI generation budget this handler uses.
func (h *Handler) AIBudget() *aibudget.Budget { return h.aiBudget }

// SendPolicy returns the send-decision policy config this handler uses.
func (h *Handler) SendPolicy() sendpolicy.Config { return h.sendPolicy }

// ExecuteSend runs a draft through the one guarded send path — exported so the autonomous
// worker can send an AUTO_SEND-decided draft through the exact same executeSend everything
// else uses (review.md: "there must still be exactly ONE sending implementation"). Satisfies
// worker.Sender without worker needing to import this package.
func (h *Handler) ExecuteSend(ctx context.Context, companyID, draftID int64) error {
	return h.executeSend(ctx, companyID, draftID, false, "", false)
}

// HasAIProvider reports whether an AI provider is actually configured — drafting is a no-op,
// not an error, when it isn't.
func (h *Handler) HasAIProvider() bool { return h.aiProvider != nil }

// reviewPreviewItem is one row in the dashboard's "Ready for review" list — a draft plus the
// company it belongs to, since ListDraftsByState alone doesn't carry the company's name/score.
type reviewPreviewItem struct {
	Company  *store.Company
	Draft    *store.Draft
	Decision string
}

// activityView adds a precomputed IsWorker flag to an ActivityEntry — html/template has no
// strings.HasPrefix, and this is simpler than registering a FuncMap for one check.
type activityView struct {
	ActivityEntry
	IsWorker bool
}

// companyDraftView shadows Draft's *string fields with plain strings (empty when nil) — same
// reasoning as drafts.go's draftView: html/template can't compare a *string with eq/ne (requires
// a basic kind) or print one meaningfully (prints the address, not the value).
type companyDraftView struct {
	*store.Draft
	Subject        string
	Decision       string
	DecisionReason string
}

// Home renders the dashboard — an operational cockpit (review.md §38-40) now that discovery,
// research, scoring and drafting run continuously in the background rather than only on a
// button click.
func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	companies, err := h.db.ListCompanies(ctx, store.CompanyFilter{}, 1000, 0)
	if err != nil {
		log.Printf("Home: failed to list companies: %v", err)
		http.Error(w, "Failed to load companies", http.StatusInternalServerError)
		return
	}

	stats := struct {
		Total      int
		New        int
		Enriched   int
		Drafted    int
		Sent       int
		Agencies   int
		Incubators int
		Hiring     int
		SMEs       int
	}{}

	stats.Total = len(companies)
	for _, c := range companies {
		switch c.Status {
		case "new":
			stats.New++
		case "enriched":
			stats.Enriched++
		case "drafted":
			stats.Drafted++
		case "sent":
			stats.Sent++
		}
		if c.Segment != nil {
			switch *c.Segment {
			case "agency":
				stats.Agencies++
			case "incubator":
				stats.Incubators++
			case "hiring":
				stats.Hiring++
			case "sme":
				stats.SMEs++
			}
		}
	}

	needsManual, _ := h.db.ListDraftsByState(ctx, "needs_manual", 1000, 0)

	// "Today" is local midnight — an operator checking this over morning coffee wants to know
	// what happened since they last looked, not since UTC midnight.
	now := time.Now()
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	todayStats, err := h.db.GetDashboardStats(ctx, startOfToday, h.sendPolicy.ReviewMinScore)
	if err != nil {
		log.Printf("Home: failed to load dashboard stats: %v", err)
		todayStats = &store.DashboardStats{}
	}

	researchQueue, err := h.db.CountCompaniesForResearch(ctx)
	if err != nil {
		log.Printf("Home: failed to count research queue: %v", err)
	}

	hourUsed, hourLimit, dayUsed, dayLimit := h.aiBudget.Usage(ctx)
	nextAvailable, aiReady, aiPausedReason := h.aiBudget.NextAvailable(ctx)

	// Top prospects: highest-scored companies regardless of pipeline stage — informational,
	// not an action list.
	topProspects, err := h.db.ListCompanies(ctx, store.CompanyFilter{}, 6, 0)
	if err != nil {
		log.Printf("Home: failed to load top prospects: %v", err)
	}

	// Ready for review: a handful of pending, non-blocked drafts with their company attached —
	// review.md §9's exception queue, previewed here so the operator doesn't have to leave the
	// dashboard to see whether anything actually needs them. Sorted by company score, same as
	// /review's own queue — highest-value prospects first, not just whatever was drafted last.
	pendingDrafts, _ := h.db.ListDraftsByState(ctx, "pending", 200, 0)
	var reviewCandidates []reviewPreviewItem
	for _, d := range pendingDrafts {
		decision := ""
		if d.Decision != nil {
			decision = *d.Decision
		}
		if decision == "blocked" {
			continue
		}
		company, err := h.db.GetCompany(ctx, d.CompanyID)
		if err != nil {
			continue
		}
		reviewCandidates = append(reviewCandidates, reviewPreviewItem{Company: company, Draft: d, Decision: decision})
	}
	sort.Slice(reviewCandidates, func(i, j int) bool { return reviewCandidates[i].Company.Score > reviewCandidates[j].Company.Score })
	awaitingReview := len(reviewCandidates)
	reviewPreview := reviewCandidates
	if len(reviewPreview) > 5 {
		reviewPreview = reviewPreview[:5]
	}

	rawActivity := h.recentActivity(ctx)
	activityViews := make([]activityView, len(rawActivity))
	for i, a := range rawActivity {
		activityViews[i] = activityView{ActivityEntry: a, IsWorker: strings.HasPrefix(a.Action, "worker:")}
	}
	lastActive := ""
	if len(rawActivity) > 0 {
		lastActive = formatAgo(now.Sub(rawActivity[0].Timestamp))
	}

	// nextStep is a plain-language pointer to what to click next — still useful even with the
	// worker running, since it tells a first-time user (or one with AI disabled) what the
	// manual buttons below are for.
	nextStep := ""
	switch {
	case stats.Total == 0:
		nextStep = "Paste some companies into the CSV box below to get started, or wait for the next automatic discovery pass."
	case stats.New > 0:
		nextStep = fmt.Sprintf("%d new compan%s waiting — the research worker will pick these up automatically, or click \"Enrich New\" to do it now.", stats.New, plural(stats.New, "y", "ies"))
	case stats.Enriched > 0:
		nextStep = fmt.Sprintf("%d compan%s enriched and ready — the draft worker will get to the best of these within its AI budget, or click \"Draft Enriched\" to do it now.", stats.Enriched, plural(stats.Enriched, "y", "ies"))
	case awaitingReview > 0:
		nextStep = fmt.Sprintf("%d draft%s waiting for you — go to \"Review drafts\".", awaitingReview, plural(awaitingReview, "", "s"))
	default:
		nextStep = "Nothing pending right now. The worker keeps discovering and researching in the background."
	}

	w.Header().Set("Content-Type", "text/html")
	renderTemplate(w, "dashboard.html", map[string]interface{}{
		"stats":          stats,
		"today":          todayStats,
		"nextStep":       nextStep,
		"pendingReview":  awaitingReview,
		"needsManual":    len(needsManual),
		"activity":       activityViews,
		"lastActive":     lastActive,
		"researchQueue":  researchQueue,
		"aiEnabled":      h.aiBudget.Enabled(),
		"aiHasProvider":  h.HasAIProvider(),
		"aiHourUsed":     hourUsed,
		"aiHourLimit":    hourLimit,
		"aiDayUsed":      dayUsed,
		"aiDayLimit":     dayLimit,
		"aiHourPct":      gaugePct(hourUsed, hourLimit),
		"aiDayPct":       gaugePct(dayUsed, dayLimit),
		"aiReady":        aiReady,
		"aiPausedReason": aiPausedReason,
		"aiNextAvailable": func() string {
			if aiReady || nextAvailable.IsZero() {
				return ""
			}
			return nextAvailable.Format("15:04")
		}(),
		"autoSendEnabled":     h.sendPolicy.Enabled,
		"topProspects":        topProspects,
		"reviewPreview":       reviewPreview,
		"domainSearchEnabled": h.domainSearcher.Enabled(),
		"domainSearchPaused":  h.domainSearcher.IsPaused(ctx),
	})
}

// gaugePct is used/limit as a 0-100 percentage for the AI budget gauge bars, capped so a
// display glitch can't push a bar past its track when usage briefly exceeds the limit.
func gaugePct(used, limit int) int {
	if limit <= 0 {
		return 0
	}
	pct := used * 100 / limit
	if pct > 100 {
		pct = 100
	}
	return pct
}

// formatAgo renders a duration the way an operator glances at, not the way Go prints it —
// "3m ago" rather than "3m12.4s ago".
func formatAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// companiesPageSize is how many rows one /companies page shows — small enough that pagination
// actually matters once the list grows past a screenful, per the exact complaint that this list
// had no visible way to search, filter, or page through it as it grew.
const companiesPageSize = 50

// ListCompanies returns the companies list, filterable by status/segment/domain status/market/
// recency and searchable by name/domain, with real pagination — numbered page links and a
// result count, not just a query param nobody could discover.
func (h *Handler) ListCompanies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	f := store.CompanyFilter{
		Status:       r.URL.Query().Get("status"),
		Segment:      r.URL.Query().Get("segment"),
		DomainStatus: r.URL.Query().Get("domain_status"),
		Market:       r.URL.Query().Get("market"),
		CreatedSince: r.URL.Query().Get("created"),
		Search:       strings.TrimSpace(r.URL.Query().Get("q")),
	}
	pageStr := r.URL.Query().Get("page")
	page := 0
	if pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p - 1
		}
	}

	limit := companiesPageSize
	offset := page * limit

	companies, err := h.db.ListCompanies(ctx, f, limit, offset)
	if err != nil {
		log.Printf("ListCompanies: failed to list companies (%+v page=%d): %v", f, page, err)
		http.Error(w, "Failed to load companies", http.StatusInternalServerError)
		return
	}

	total, err := h.db.CountCompanies(ctx, f)
	if err != nil {
		log.Printf("ListCompanies: failed to count companies: %v", err)
	}
	totalPages := (total + limit - 1) / limit
	if totalPages < 1 {
		totalPages = 1
	}
	currentPage := page + 1

	urlFor := func(p int) string { return companiesURL(f, p) }

	prevURL, nextURL := "", ""
	if page > 0 {
		prevURL = urlFor(currentPage - 1)
	}
	if currentPage < totalPages {
		nextURL = urlFor(currentPage + 1)
	}
	// currentURL is where the inline "fix domain" form (per failed-domain row) redirects back
	// to — so correcting a domain from a filtered/paged view doesn't lose that filter/page.
	currentURL := urlFor(currentPage)

	w.Header().Set("Content-Type", "text/html")
	renderTemplate(w, "companies.html", map[string]interface{}{
		"companies":    companies,
		"status":       f.Status,
		"segment":      f.Segment,
		"domainStatus": f.DomainStatus,
		"market":       f.Market,
		"created":      f.CreatedSince,
		"query":        f.Search,
		"page":         currentPage,
		"totalPages":   totalPages,
		"total":        total,
		"hasPrev":      page > 0,
		"hasNext":      currentPage < totalPages,
		"prevURL":      prevURL,
		"nextURL":      nextURL,
		"currentURL":   currentURL,
		"pageLinks":    paginationWindow(currentPage, totalPages, urlFor),
	})
}

// paginationLink is one entry in the numbered pagination strip — either a real page number
// (Ellipsis false) or a "…" gap marker.
type paginationLink struct {
	Number   int
	URL      string
	Current  bool
	Ellipsis bool
}

// paginationWindow returns which page numbers to render as clickable links so a jump straight to
// "page 4" doesn't require clicking Next three times — always page 1 and the last page, plus a
// small window around the current page, with "…" markers for any gap. Keeps the strip short even
// once there are hundreds of pages.
func paginationWindow(current, total int, urlFor func(page int) string) []paginationLink {
	if total <= 1 {
		return nil
	}
	const window = 2

	add := func(links []paginationLink, n int) []paginationLink {
		return append(links, paginationLink{Number: n, URL: urlFor(n), Current: n == current})
	}
	addEllipsis := func(links []paginationLink) []paginationLink {
		return append(links, paginationLink{Ellipsis: true})
	}

	start := current - window
	if start < 1 {
		start = 1
	}
	end := current + window
	if end > total {
		end = total
	}

	var links []paginationLink
	if start > 1 {
		links = add(links, 1)
		if start > 2 {
			links = addEllipsis(links)
		}
	}
	for n := start; n <= end; n++ {
		links = add(links, n)
	}
	if end < total {
		if end < total-1 {
			links = addEllipsis(links)
		}
		links = add(links, total)
	}
	return links
}

// companiesURL builds a /companies link preserving the current filters with a different page
// number — used for pagination links, computed server-side so the template doesn't need to
// hand-build (and URL-escape) query strings itself.
func companiesURL(f store.CompanyFilter, page int) string {
	v := url.Values{}
	if f.Status != "" {
		v.Set("status", f.Status)
	}
	if f.Segment != "" {
		v.Set("segment", f.Segment)
	}
	if f.DomainStatus != "" {
		v.Set("domain_status", f.DomainStatus)
	}
	if f.Market != "" {
		v.Set("market", f.Market)
	}
	if f.CreatedSince != "" {
		v.Set("created", f.CreatedSince)
	}
	if f.Search != "" {
		v.Set("q", f.Search)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return "/companies"
	}
	return "/companies?" + v.Encode()
}

// CreateCompany creates a new company
func (h *Handler) CreateCompany(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	name := r.FormValue("name")
	domain := r.FormValue("domain")
	source := r.FormValue("source")
	sourceURL := r.FormValue("source_url")

	if name == "" || domain == "" {
		http.Error(w, "name and domain required", http.StatusBadRequest)
		return
	}

	// A human typed this domain directly into the form — trust it like a manual CSV row.
	_, err := h.db.CreateCompany(ctx, name, domain, source, sourceURL, "manual")
	if err != nil {
		http.Error(w, "Failed to create company", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/companies", http.StatusSeeOther)
}

// ViewCompany shows a single company with contacts and signals
func (h *Handler) ViewCompany(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	company, err := h.db.GetCompany(ctx, id)
	if err != nil {
		http.Error(w, "Company not found", http.StatusNotFound)
		return
	}

	contacts, _ := h.db.GetContactsByCompany(ctx, id)
	signals, _ := h.db.GetSignalsByCompany(ctx, id)

	// html/template's eq/ne require a "basic kind" and can't compare a *string directly, so this
	// is precomputed here rather than in the template — same reasoning as companyDraftView below.
	isApplicationMode := company.Segment != nil && *company.Segment == "application"

	// A draft in any state except "discarded" counts as "live" — CompanyDraft refuses to
	// generate a new one while that's true, so the page must show what's actually blocking it
	// (a blocked or needs_manual draft has real content/a real reason worth seeing, not just a
	// "Generate Draft" button that would fail if clicked) rather than only recognizing "pending".
	var currentDraft *companyDraftView
	if latest, err := h.db.GetLatestDraftForCompany(ctx, id); err == nil && latest != nil && latest.State != "discarded" {
		dv := companyDraftView{Draft: latest}
		if latest.Subject != nil {
			dv.Subject = *latest.Subject
		}
		if latest.Decision != nil {
			dv.Decision = *latest.Decision
		}
		if latest.DecisionReason != nil {
			dv.DecisionReason = *latest.DecisionReason
		}
		currentDraft = &dv
	}

	// sendpolicy's blocked branches each return exactly one reason (see policy.go), so this
	// prefix check is unambiguous — true only when the draft's sole block reason is the score
	// threshold, never suppression/no-contact/domain/placeholder, which "Send anyway" must never
	// bypass. Precomputed here, not in the template, for the same reason isApplicationMode is.
	isScoreBlockedDraft := currentDraft != nil && currentDraft.Decision == "blocked" &&
		strings.HasPrefix(currentDraft.DecisionReason, "score_below_review_threshold")

	var scoreBreakdown []score.Line
	if len(company.ScoreBreakdown) > 0 {
		if err := json.Unmarshal(company.ScoreBreakdown, &scoreBreakdown); err != nil {
			log.Printf("ViewCompany: failed to parse score breakdown for %d: %v", id, err)
		}
	}

	sentEmails, err := h.db.ListSentEmailsForCompany(ctx, id)
	if err != nil {
		log.Printf("ViewCompany: failed to load sent emails for %d: %v", id, err)
	}

	w.Header().Set("Content-Type", "text/html")
	renderTemplate(w, "company.html", map[string]interface{}{
		"company":             company,
		"contacts":            contacts,
		"signals":             signals,
		"currentDraft":        currentDraft,
		"isApplicationMode":   isApplicationMode,
		"isScoreBlockedDraft": isScoreBlockedDraft,
		"scoreBreakdown":      scoreBreakdown,
		"sentEmails":          sentEmails,
	})
}

// sendError carries an HTTP status alongside a user-facing refusal reason, so both the
// company-page send button and the /review screen's "Save & send" can show the right message.
type sendError struct {
	status int
	msg    string
}

func (e *sendError) Error() string { return e.msg }

// executeSend is the single path that actually sends mail. Every guardrail from spec section 8
// is enforced here — callers must not reimplement any of these checks.
// executeSend is the one guarded path every send — first contact, human-approved,
// AUTO_SEND, or a follow-up — actually goes through. isFollowup marks a follow-up to an
// existing thread: it's exempt from the 30-day same-domain rule (that rule exists to stop a
// *new* unsolicited contact, not a planned follow-up to a conversation already underway), and
// it skips creating a fresh send_log row / rescheduling follow-up dates, since the caller
// (ReviewFollowupSend) marks the existing thread's followup_N_sent_at instead of starting a
// new one.
func (h *Handler) executeSend(ctx context.Context, companyID, draftID int64, isFollowup bool, toOverride string, overrideScoreBlock bool) error {
	company, err := h.db.GetCompany(ctx, companyID)
	if err != nil {
		return &sendError{http.StatusNotFound, "Company not found"}
	}

	dft, err := h.db.GetDraft(ctx, draftID)
	if err != nil || dft.CompanyID != companyID {
		return &sendError{http.StatusNotFound, "Draft not found"}
	}

	if dft.State != "pending" {
		return &sendError{http.StatusConflict, "Draft is not pending — it may have already been sent"}
	}

	// Final safety net: the send policy already decided this draft shouldn't go out. Re-check
	// here so nothing — a stale form resubmit, a direct POST — can push a BLOCKED draft through
	// regardless of how it was decided. score_below_review_threshold is the one reason a human
	// can deliberately override (overrideScoreBlock, wired from the company/review pages' "Send
	// anyway" button) — it's a confidence judgment about a possibly-wrong automated score, not a
	// technical/compliance fact. Every other blocked reason (domain_unverified, no_contact,
	// suppressed, recent_contact_same_domain, placeholder_remaining) stays a hard stop no
	// override can bypass — those aren't judgment calls. sendpolicy.Evaluate's blocked branches
	// each return exactly one reason, so this prefix check is unambiguous.
	if dft.Decision != nil && *dft.Decision == string(sendpolicy.Blocked) {
		reason := ""
		if dft.DecisionReason != nil {
			reason = *dft.DecisionReason
		}
		isScoreBlock := strings.HasPrefix(reason, "score_below_review_threshold")
		if !overrideScoreBlock || !isScoreBlock {
			msg := "This draft was blocked by the send policy — see its decision reason"
			if isScoreBlock {
				msg = "This draft was blocked for a low score (" + reason + ") — use \"Send anyway\" to override, or fix the score first"
			}
			return &sendError{http.StatusForbidden, msg}
		}
	}

	if err := draft.ValidateTemplate(dft.Body); err != nil {
		return &sendError{http.StatusBadRequest, "Draft still contains an unresolved placeholder — edit it before sending"}
	}

	contacts, err := h.db.GetContactsByCompany(ctx, companyID)
	if err != nil {
		return &sendError{http.StatusInternalServerError, "Failed to load contacts"}
	}

	// A human-typed override (the review screen's editable "Send to" field, or a direct API
	// call) always wins — that's the fix for enrichment having found a dead email. Otherwise
	// fall back to auto-pick, skipping anything already marked bad.
	toEmail := strings.TrimSpace(toOverride)
	if toEmail == "" {
		for _, c := range contacts {
			if c.Kind == "email" && c.Status != "bad" {
				toEmail = c.Value
				break
			}
		}
	}
	if toEmail == "" {
		return &sendError{http.StatusBadRequest, "No email address found — add or fix one on the company page"}
	}

	if suppressed, _ := h.db.IsSuppressed(ctx, toEmail); suppressed {
		return &sendError{http.StatusForbidden, "This address asked not to be contacted again"}
	}
	if company.Domain != "" {
		if suppressed, _ := h.db.IsSuppressed(ctx, company.Domain); suppressed {
			return &sendError{http.StatusForbidden, "This domain asked not to be contacted again"}
		}

		if !isFollowup {
			since := time.Now().AddDate(0, 0, -30)
			recent, err := h.db.HasRecentSendToDomain(ctx, company.Domain, since)
			if err != nil {
				return &sendError{http.StatusInternalServerError, "Failed to check send history"}
			}
			if recent {
				return &sendError{http.StatusConflict, "This domain was already contacted within the last 30 days"}
			}
		}
	}

	if !h.checkMinInterval(ctx) {
		return &sendError{http.StatusTooManyRequests, "Sending too fast — wait a little before the next send"}
	}
	if !h.checkDailyLimit(ctx) {
		return &sendError{http.StatusTooManyRequests, "Daily send limit reached (" + strconv.Itoa(h.dailySendLimit) + " emails/day)"}
	}

	subject := ""
	if dft.Subject != nil {
		subject = *dft.Subject
	}
	h.logActivity(ctx, "worker:send", fmt.Sprintf("Sending to %s (%s)…", company.Name, toEmail), true)
	if err := h.sendSMTP(company.Name, toEmail, subject, dft.Body); err != nil {
		log.Printf("executeSend: SMTP error for %s (%s): %v", company.Name, toEmail, err)
		h.logActivity(ctx, "send", fmt.Sprintf("Failed to send to %s (%s): %v", company.Name, toEmail, err), false)
		return &sendError{http.StatusInternalServerError, "Failed to send email: " + err.Error()}
	}

	// Only now that the send actually happened do we burn a daily-limit slot and reset the
	// min-interval clock — a failed send above must not cost the sender a slot.
	h.recordSend(ctx)

	// A manually-typed address that wasn't already on file just proved it's deliverable enough
	// to accept the SMTP handoff — worth remembering (quality 3) so it wins auto-pick next time
	// instead of whatever dead address enrichment originally found. Skipped when the override
	// just matches an existing contact (e.g. picked from the quick-select list) — re-inserting it
	// would wipe that contact's person_name/role via the upsert's ON CONFLICT UPDATE.
	if toOverride != "" {
		alreadyKnown := false
		for _, c := range contacts {
			if c.Kind == "email" && c.Value == toEmail {
				alreadyKnown = true
				break
			}
		}
		if !alreadyKnown {
			if _, err := h.db.CreateContact(ctx, companyID, "email", toEmail, nil, nil, 3); err != nil {
				log.Printf("executeSend: failed to save corrected contact for company %d: %v", companyID, err)
			}
		}
	}

	if err := h.db.UpdateDraftState(ctx, draftID, "sent"); err != nil {
		log.Printf("executeSend: Failed to update draft state: %v", err)
	}

	// A follow-up continues an existing thread — it doesn't start a new one, so no new
	// send_log row and no company-status reset. The caller (ReviewFollowupSend) marks the
	// existing thread's followup_N_sent_at instead.
	if !isFollowup {
		followUp1 := time.Now().AddDate(0, 0, 3)
		followUp2 := time.Now().AddDate(0, 0, 8)
		if _, err := h.db.CreateSendLog(ctx, draftID, companyID, toEmail, "email", followUp1, followUp2); err != nil {
			log.Printf("executeSend: Failed to create send log: %v", err)
		}

		_ = h.db.UpdateCompanyStatus(ctx, companyID, "sent")
	}

	h.logActivity(ctx, "send", fmt.Sprintf("Sent to %s (%s)", company.Name, toEmail), true)
	log.Printf("executeSend: Successfully sent to %s (%s)", company.Name, toEmail)
	return nil
}

// SendDraft sends the draft via email with SMTP — the company-page send button.
func (h *Handler) SendDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	draftID, _ := strconv.ParseInt(r.FormValue("draft_id"), 10, 64)
	toEmail := strings.TrimSpace(r.FormValue("to_email"))
	overrideScoreBlock := r.FormValue("override_score_block") == "1"

	if err := h.executeSend(ctx, id, draftID, false, toEmail, overrideScoreBlock); err != nil {
		if se, ok := err.(*sendError); ok {
			http.Error(w, se.msg, se.status)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	http.Redirect(w, r, "/companies/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// buildEmailMessage constructs the raw RFC 5322 message sendSMTP hands to smtp.SendMail — a
// pure function (no network I/O) so the From/Reply-To header logic can be tested directly. The
// envelope sender (smtp.SendMail's separate "from" argument, not built here) must stay the raw
// SMTP account — Gmail rejects a mismatched envelope sender — but the visible From header can
// and should carry the configured display name, and Reply-To is a separate header entirely.
func buildEmailMessage(fromName, fromEmail, replyTo, smtpUser, toEmail, subject, body string) string {
	fromHeader := smtpUser
	if fromEmail != "" {
		if fromName != "" {
			fromHeader = fmt.Sprintf("%q <%s>", fromName, fromEmail)
		} else {
			fromHeader = fromEmail
		}
	}
	header := "From: " + fromHeader + "\n"
	header += "To: " + toEmail + "\n"
	if replyTo != "" {
		header += "Reply-To: " + replyTo + "\n"
	}
	header += "Subject: " + subject + "\n"
	header += "MIME-Version: 1.0\n"
	header += "Content-Type: text/plain; charset=utf-8\n"

	return header + "\n" + body
}

// sendSMTP sends email via SMTP
func (h *Handler) sendSMTP(toName, toEmail, subject, body string) error {
	if h.smtpUser == "" || h.smtpPass == "" {
		return fmt.Errorf("SMTP credentials not configured")
	}

	message := buildEmailMessage(h.fromName, h.fromEmail, h.replyTo, h.smtpUser, toEmail, subject, body)

	// Connect to SMTP server
	addr := h.smtpHost + ":" + h.smtpPort
	auth := smtp.PlainAuth("", h.smtpUser, h.smtpPass, h.smtpHost)

	if err := smtp.SendMail(addr, auth, h.smtpUser, []string{toEmail}, []byte(message)); err != nil {
		return fmt.Errorf("SMTP send failed: %w", err)
	}

	log.Printf("Email sent successfully to %s", toEmail)
	return nil
}

// Admin handlers

// AdminSeed runs a seeder by source. The "manual" source is special-cased: its CSV text comes
// from this request's form body, not from registry state (the registered instance always holds
// an empty CSV — it exists only so /companies list of sources works).
func (h *Handler) AdminSeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	source := chi.URLParam(r, "source")

	if source == "" {
		http.Error(w, "source parameter required", http.StatusBadRequest)
		return
	}

	if source == "manual" {
		csv := r.FormValue("csv")
		if strings.TrimSpace(csv) == "" {
			http.Error(w, "csv field required for manual import", http.StatusBadRequest)
			return
		}

		start := time.Now()
		manualSeeder := seed.NewManualSeeder(csv, "Prospector/1.0")
		added, err := manualSeeder.Seed(ctx, h.db, h.redis)
		if err != nil {
			h.logActivity(ctx, "seed:manual", "Failed: "+err.Error(), false)
			http.Error(w, "Seed failed: "+err.Error(), http.StatusInternalServerError)
			log.Printf("AdminSeed: manual import failed: %v", err)
			return
		}

		h.logActivity(ctx, "seed:manual", fmt.Sprintf("Added %d compan%s from pasted CSV", added, plural(added, "y", "ies")), true)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"source":"manual","added":%d,"duration":"%v"}`, added, time.Since(start))
		return
	}

	if !contains(h.seedRegistry.List(), source) {
		http.Error(w, "Unknown seeder: "+source, http.StatusBadRequest)
		log.Printf("AdminSeed: Unknown seeder %s", source)
		return
	}

	result, err := h.seedRegistry.Run(ctx, source, h.db, h.redis)
	if err != nil {
		h.logActivity(ctx, "seed:"+source, "Failed: "+err.Error(), false)
		http.Error(w, "Seed failed: "+err.Error(), http.StatusInternalServerError)
		log.Printf("AdminSeed: Seeder %s failed: %v", source, err)
		return
	}

	detail := fmt.Sprintf("Added %d compan%s in %v", result.Added, plural(result.Added, "y", "ies"), result.Duration.Round(time.Millisecond))
	if result.Added == 0 {
		detail += " — check server logs, this source's selectors may not match the live site"
	}
	h.logActivity(ctx, "seed:"+source, detail, result.Added > 0)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"source":"%s","added":%d,"duration":"%v"}`, result.Source, result.Added, result.Duration)
}

// usJobSourceNames are every seeder AdminSeedUSJobs runs together — one click instead of four,
// matching "apply to as many sources as possible" rather than making the operator trigger each
// US/international remote job board separately.
var usJobSourceNames = []string{"remoteok", "weworkremotely", "remotive", "himalayas"}

// AdminSeedUSJobs runs every US/international remote job board seeder in one go and reports a
// combined total — the individual sources are still reachable one at a time via
// /admin/seed/{source} (e.g. for retrying just one that failed), this is the convenience button.
func (h *Handler) AdminSeedUSJobs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	totalAdded := 0
	var failedSources []string
	for _, source := range usJobSourceNames {
		result, err := h.seedRegistry.Run(ctx, source, h.db, h.redis)
		if err != nil {
			log.Printf("AdminSeedUSJobs: %s failed: %v", source, err)
			failedSources = append(failedSources, source)
			continue
		}
		totalAdded += result.Added
	}

	detail := fmt.Sprintf("Added %d compan%s across %d US/international source%s", totalAdded, plural(totalAdded, "y", "ies"), len(usJobSourceNames), plural(len(usJobSourceNames), "", "s"))
	if len(failedSources) > 0 {
		detail += fmt.Sprintf(" — %s failed, check server logs", strings.Join(failedSources, ", "))
	}
	h.logActivity(ctx, "seed:us_jobs", detail, totalAdded > 0)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"added":%d,"failed":%d}`, totalAdded, len(failedSources))
}

// AdminEnrich enriches all new companies
func (h *Handler) AdminEnrich(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get all new companies
	companies, err := h.db.ListCompanies(ctx, store.CompanyFilter{Status: "new"}, 1000, 0)
	if err != nil {
		http.Error(w, "Failed to list companies: "+err.Error(), http.StatusInternalServerError)
		log.Printf("AdminEnrich: Failed to list companies: %v", err)
		return
	}

	if len(companies) == 0 {
		h.logActivity(ctx, "enrich", "No new companies to enrich", true)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"enriched":0,"empty":0,"errors":0}`)
		return
	}

	enriched := 0
	empty := 0
	errors := 0
	onCooldown := 0

	for _, company := range companies {
		if h.enricher.RecentlyAttempted(ctx, company.ID) {
			onCooldown++
			continue
		}

		found, err := h.enricher.EnrichCompany(ctx, company, h.db)
		if err != nil {
			log.Printf("AdminEnrich: Failed to enrich %s: %v", company.Name, err)
			errors++
			continue
		}
		if !found {
			empty++
			continue
		}

		if err := h.scorer.Score(ctx, company, h.db); err != nil {
			log.Printf("AdminEnrich: Failed to score %s: %v", company.Name, err)
			errors++
			continue
		}

		enriched++
	}

	detail := fmt.Sprintf("Enriched %d of %d compan%s", enriched, len(companies), plural(len(companies), "y", "ies"))
	if empty > 0 {
		detail += fmt.Sprintf(" — %d had nothing extractable (wrong/dead domain, or site blocked the crawler)", empty)
	}
	if onCooldown > 0 {
		detail += fmt.Sprintf(", %d skipped (found nothing last time, retry after a few hours)", onCooldown)
	}
	if errors > 0 {
		detail += fmt.Sprintf(", %d hard error%s", errors, plural(errors, "", "s"))
	}
	h.logActivity(ctx, "enrich", detail, enriched > 0 || errors == 0)

	log.Printf("AdminEnrich: Enriched %d companies (empty: %d, on cooldown: %d, errors: %d)", enriched, empty, onCooldown, errors)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"enriched":%d,"empty":%d,"on_cooldown":%d,"errors":%d}`, enriched, empty, onCooldown, errors)
}

// draftBatchSize is the cost guard from spec section 7: draft only the top-scoring N per run.
const draftBatchSize = 40

// AdminDraft generates drafts for the top-scoring eligible companies (review.md §7/§8: domain
// verified, a real email contact, score at or above AI_MIN_SCORE, no existing live draft —
// enforced deterministically in ListCompaniesForDrafting before any AI call is considered),
// stopping early if the AI budget runs out rather than failing the whole batch.
func (h *Handler) AdminDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if h.aiProvider == nil {
		h.logActivity(ctx, "draft", "Failed: no AI provider configured (set ANTHROPIC_API_KEY or GEMINI_API_KEY)", false)
		http.Error(w, "AI provider not configured", http.StatusInternalServerError)
		log.Printf("AdminDraft: AI provider not configured")
		return
	}

	companies, err := h.db.ListCompaniesForDrafting(ctx, h.aiBudget.MinScore(), draftBatchSize)
	if err != nil {
		http.Error(w, "Failed to list companies: "+err.Error(), http.StatusInternalServerError)
		log.Printf("AdminDraft: Failed to list companies: %v", err)
		return
	}

	if len(companies) == 0 {
		h.logActivity(ctx, "draft", "No enriched companies waiting to be drafted", true)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"drafted":0,"needs_manual":0,"errors":0}`)
		return
	}

	drafted := 0
	needsManual := 0
	autoSent := 0
	errors := 0
	budgetStopped := 0
	minInterval := h.draftMinInterval
	var lastCallAt time.Time

	for _, company := range companies {
		// A manual batch run doesn't check the cooldown (that's specifically to pace the
		// *autonomous* loop's self-initiated spending) but the hard hourly/daily caps still
		// apply — the same scarce quota either path would burn through.
		if allowed, reason := h.aiBudget.Allow(ctx, false); !allowed {
			log.Printf("AdminDraft: stopping early, %s", reason)
			budgetStopped = len(companies) - drafted - needsManual - errors
			break
		}

		// Pace requests to the AI provider — without this, a batch of drafts blows through a
		// free-tier per-minute quota in seconds and most of the batch just fails.
		if minInterval > 0 && !lastCallAt.IsZero() {
			if wait := minInterval - time.Since(lastCallAt); wait > 0 {
				time.Sleep(wait)
			}
		}
		lastCallAt = time.Now()

		gen, err := draft.GenerateAndSaveDraft(ctx, h.db, h.draftService, h.sendPolicy, company, h.redis)
		if gen != nil && gen.APICalled {
			h.aiBudget.Record(ctx)
		}
		if err != nil {
			log.Printf("AdminDraft: Failed to draft for %s: %v", company.Name, err)
			errors++
			continue
		}

		if gen.State == "needs_manual" {
			needsManual++
			continue
		}
		drafted++

		if gen.Decision == sendpolicy.AutoSend {
			if err := h.ExecuteSend(ctx, company.ID, gen.DraftID); err != nil {
				log.Printf("AdminDraft: auto-send failed for %s: %v — left pending for review", company.Name, err)
			} else {
				autoSent++
				h.logActivity(ctx, "auto_send", fmt.Sprintf("Auto-sent to %s (score %d)", company.Name, company.Score), true)
			}
		}
	}

	detail := fmt.Sprintf("Drafted %d, %d need%s manual attention", drafted, needsManual, plural(needsManual, "s", ""))
	if autoSent > 0 {
		detail += fmt.Sprintf(", %d auto-sent", autoSent)
	}
	if errors > 0 {
		detail += fmt.Sprintf(", %d error%s", errors, plural(errors, "", "s"))
	}
	if budgetStopped > 0 {
		detail += fmt.Sprintf(", %d left for later (AI budget)", budgetStopped)
	}
	h.logActivity(ctx, "draft", detail, drafted > 0 || (needsManual == 0 && errors == 0))

	log.Printf("AdminDraft: Generated %d drafts, %d needs_manual, %d auto-sent (errors: %d)", drafted, needsManual, autoSent, errors)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"drafted":%d,"needs_manual":%d,"auto_sent":%d,"errors":%d}`, drafted, needsManual, autoSent, errors)
}

// domainResolveBatchSize caps one manual trigger's batch — generous since it's human-initiated,
// but still bounded so a single click can't burn the entire daily search budget at once.
const domainResolveBatchSize = 20

// AdminResolveDomains manually triggers the same domain-resolution pass the autonomous worker
// runs on its own schedule — for companies stuck at domain_status='failed' after every guessed
// candidate (verify.CandidateDomains) already came up empty.
func (h *Handler) AdminResolveDomains(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if !h.domainSearcher.Enabled() {
		h.logActivity(ctx, "domain_resolve", "Failed: GOOGLE_SEARCH_API_KEY/GOOGLE_SEARCH_CX not configured", false)
		http.Error(w, "Domain search not configured — set GOOGLE_SEARCH_API_KEY and GOOGLE_SEARCH_CX", http.StatusInternalServerError)
		return
	}

	resolved, attempted, err := domainsearch.ResolveFailedDomains(ctx, h.db, h.verifier, h.domainSearcher, h.redis, domainResolveBatchSize)
	if err != nil {
		h.logActivity(ctx, "domain_resolve", "Failed: "+err.Error(), false)
		http.Error(w, "Domain resolution failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	detail := fmt.Sprintf("Resolved %d of %d attempted", resolved, attempted)
	if attempted == 0 {
		detail = "No failed-domain companies waiting, today's search budget is used up, or it's paused"
	}
	h.logActivity(ctx, "domain_resolve", detail, resolved > 0 || attempted == 0)

	log.Printf("AdminResolveDomains: resolved %d of %d attempted", resolved, attempted)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"resolved":%d,"attempted":%d}`, resolved, attempted)
}

// DomainSearchPause hits the runtime kill-switch for the search-based domain resolver — for
// when a mistake is noticed (a wrong domain getting accepted, quota burning faster than
// expected) and it needs to stop before the next scheduled tick, not after editing .env and
// restarting the process.
func (h *Handler) DomainSearchPause(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := h.domainSearcher.Pause(ctx); err != nil {
		http.Error(w, "Failed to pause: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.logActivity(ctx, "domain_resolve", "Paused domain search (manual)", false)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// DomainSearchResume clears the runtime kill-switch.
func (h *Handler) DomainSearchResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := h.domainSearcher.Resume(ctx); err != nil {
		http.Error(w, "Failed to resume: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.logActivity(ctx, "domain_resolve", "Resumed domain search (manual)", true)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// checkMinInterval reports whether MIN_SEND_INTERVAL_SECONDS has elapsed since the last send.
// It only peeks at send:last_at — recordSend is what advances it, and only after a real send.
func (h *Handler) checkMinInterval(ctx context.Context) bool {
	val, err := h.redis.Get(ctx, "send:last_at").Result()
	if err == redis.Nil {
		return true
	}
	if err != nil {
		log.Printf("Redis error checking send interval: %v", err)
		return false
	}

	lastUnix, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		log.Printf("Redis error parsing send:last_at: %v", err)
		return false
	}

	return time.Since(time.Unix(lastUnix, 0)) >= time.Duration(h.minSendIntervalSec)*time.Second
}

// checkDailyLimit reports whether today's send count is still under DAILY_SEND_LIMIT. It only
// peeks at the counter — recordSend is what increments it, and only after a real send, so a
// rejected send never costs the sender a slot.
func (h *Handler) checkDailyLimit(ctx context.Context) bool {
	key := "send:day:" + time.Now().Format("2006-01-02")

	val, err := h.redis.Get(ctx, key).Result()
	if err == redis.Nil {
		return true
	}
	if err != nil {
		log.Printf("Redis error checking daily limit: %v", err)
		return false
	}

	count, err := strconv.Atoi(val)
	if err != nil {
		log.Printf("Redis error parsing send:day counter: %v", err)
		return false
	}

	return count < h.dailySendLimit
}

// recordSend advances the daily counter and the min-interval clock. Call only after a send has
// actually succeeded.
func (h *Handler) recordSend(ctx context.Context) {
	key := "send:day:" + time.Now().Format("2006-01-02")

	count, err := h.redis.Incr(ctx, key).Result()
	if err != nil {
		log.Printf("Redis error recording daily send count: %v", err)
	} else if count == 1 {
		if err := h.redis.Expire(ctx, key, 24*time.Hour).Err(); err != nil {
			log.Printf("Redis expiry error: %v", err)
		}
	}

	if err := h.redis.Set(ctx, "send:last_at", time.Now().Unix(), 0).Err(); err != nil {
		log.Printf("Redis error setting send:last_at: %v", err)
	}
}

// contains checks if a string is in a slice
func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

// renderTemplate renders a template file and writes to response
func renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	tmpl, err := template.ParseFiles("internal/web/templates/" + name)
	if err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}

	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("Execution error: %v", err)
	}
}
