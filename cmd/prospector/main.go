package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/anthonyalando8/prospector/internal/automation"
	"github.com/anthonyalando8/prospector/internal/queue"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/anthonyalando8/prospector/internal/web"
	"github.com/anthonyalando8/prospector/internal/worker"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
)

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Printf("Warning: Error loading .env file: %v", err)
	}
	pipelineMode := os.Getenv("EMAIL_PIPELINE_MODE")
	if pipelineMode == "" {
		pipelineMode = "v2_review"
	}
	if !send.ValidMode(pipelineMode) {
		log.Fatalf("EMAIL_PIPELINE_MODE must be one of %s, %s, %s or %s",
			send.ModeDisabled, send.ModeReview, send.ModeManualSend, send.ModeAuto)
	}
	// The automation configuration is validated here, before anything starts. An unknown intent,
	// a wildcard, or an implausible cap stops startup rather than being ignored: a configuration
	// that does not mean what it says is the wrong thing to run, and the failure mode of
	// misreading it is unattended email.
	automationCfg, err := automation.LoadConfig(pipelineMode,
		os.Getenv("AUTO_SEND_INTENTS"), os.Getenv("AUTO_SEND_DAILY_CAP"))
	if err != nil {
		log.Fatalf("Automation configuration is invalid: %v", err)
	}
	if len(automationCfg.Allowlist) == 0 {
		log.Printf("Email pipeline mode: %s; AUTO_SEND_INTENTS is empty, so no message can be sent automatically", pipelineMode)
	} else {
		log.Printf("Email pipeline mode: %s; selective automation is configured for %v with a cap of %d per day. "+
			"Each message must still pass the automation policy and the send preflight individually",
			pipelineMode, automationCfg.Allowlist, automationCfg.DailyCap)
	}

	// Initialize database
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL not set")
	}

	db, err := store.NewDB(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()
	if err := db.CheckSchema(context.Background()); err != nil {
		log.Fatalf("Schema check failed before worker startup: %v", err)
	}

	// Initialize Redis
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		log.Fatal("REDIS_URL not set")
	}

	redisConn, err := queue.NewRedisPool(redisURL)
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	defer redisConn.Close()

	// redis.NewClient dials lazily, so a down Redis wouldn't otherwise surface until the first
	// request touches it — deep inside a handler, as a confusing stream of dial errors. Ping now
	// so a misconfigured/stopped Redis fails loudly at startup instead.
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := redisConn.Ping(pingCtx).Err(); err != nil {
		cancel()
		log.Fatalf("Redis unreachable at %s: %v", redisURL, err)
	}
	cancel()

	// Initialize web handler
	handler := web.NewHandler(db, redisConn)

	// Set up router
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Static files
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("./internal/web/static"))))

	// Live activity feed: Server-Sent Events, deliberately outside the timeout groups below —
	// this connection is meant to stay open for as long as the dashboard tab is, not fail after
	// a fixed duration.
	r.Get("/events/activity", handler.ActivityStream)

	// Ordinary page routes get a tight timeout — these should always be fast.
	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(30 * time.Second))

		r.Get("/", handler.Home)
		r.Get("/dashboard/snapshot", handler.DashboardSnapshot)
		r.Get("/profile", handler.Profile)
		r.Post("/plans/{id}/compose", handler.CompositionGenerate)
		r.Get("/compositions/{id}", handler.CompositionView)
		r.Get("/compositions", handler.Compositions)
		r.Post("/compositions/{id}/edit", handler.CompositionEdit)
		// Phase 5: approval and sending. CompositionSend is the only send route in the
		// application; every other historical send/resend/follow-up route is permanently closed.
		r.Post("/compositions/{id}/approval", handler.CompositionApprove)
		r.Post("/compositions/{id}/approval/revoke", handler.CompositionRevoke)
		r.Post("/compositions/{id}/send", handler.CompositionSend)
		r.Get("/threads", handler.Threads)
		r.Get("/threads/{id}", handler.ThreadView)
		r.Post("/threads/{id}/events", handler.ThreadEventRecord)
		r.Post("/profile/drafts", handler.ProfileSave)
		r.Post("/profile/activate", handler.ProfileActivate)
		r.Get("/opportunities", handler.Opportunities)
		r.Get("/opportunities/{id}", handler.Opportunity)
		// Phase 3: deterministic fit, company relevance, intent, recipient and allowed proof.
		// Every one of these is a POST because it writes an assessment or a plan; none of them
		// generates or sends a message.
		r.Post("/opportunities/{id}/assess", handler.OpportunityAssess)
		r.Get("/companies/{id}/plan", handler.CompanyPlan)
		r.Post("/companies/{id}/assess", handler.CompanyAssess)
		r.Post("/companies/{id}/plans", handler.CompanyPlanCreate)
		r.Post("/plans/{id}/recipient", handler.PlanRecipient)
		r.Post("/plans/{id}/status", handler.PlanStatus)
		r.Get("/sent", handler.Sent)
		r.Post("/sent/{sendLogID}/resend", handler.SentResend)
		r.Get("/companies", handler.ListCompanies)
		r.Post("/companies", handler.CreateCompany)
		r.Get("/companies/{id}", handler.ViewCompany)
		r.Post("/companies/{id}/send", handler.SendDraft)
		r.Post("/companies/{id}/status", handler.CompanyStatus)
		r.Post("/companies/{id}/score", handler.CompanyScoreOverride)
		r.Post("/companies/{id}/notes", handler.CompanyAddNote)
		r.Post("/companies/{id}/suppress", handler.Suppress)
		r.Post("/companies/{id}/domain/verify", handler.CompanyVerifyDomain)
		r.Post("/companies/{id}/domain/update", handler.CompanyUpdateDomain)
		r.Post("/companies/{id}/generate-draft", handler.LegacyGenerateClosed)
		r.Post("/companies/{id}/apply-mode", handler.CompanyApplyMode)
		r.Post("/companies/{id}/segment/reset", handler.CompanySegmentReset)
		r.Post("/companies/{id}/contacts", handler.ContactAdd)
		r.Post("/contacts/{contactID}/update", handler.ContactUpdate)
		r.Post("/contacts/{contactID}/delete", handler.ContactDelete)
		r.Post("/contacts/{contactID}/mark-bad", handler.ContactMarkBad)
		r.Post("/contacts/{contactID}/reactivate", handler.ContactReactivate)

		// Review — the main screen, one pending draft at a time
		r.Get("/review", handler.Review)
		r.Post("/review/send", handler.ReviewSend)
		r.Post("/review/save", handler.ReviewSave)
		r.Post("/review/skip", handler.ReviewSkip)
		r.Post("/review/needs-manual", handler.ReviewNeedsManual)

		r.Get("/followups", handler.Followups)
		r.Post("/followups/send", handler.ReviewFollowupSend)

		r.Get("/templates", handler.Templates)
		r.Post("/templates/{id}/update", handler.LegacyTemplateEditClosed)

		// Phase 6: the legacy pipeline's remains, visible and explained rather than hidden.
		// Nothing here can generate, approve or send; classification is the one write.
		r.Get("/legacy/drafts", handler.LegacyDrafts)
		r.Get("/legacy/threads", handler.LegacyThreads)
		r.Post("/legacy/threads/{id}/classify", handler.LegacyThreadClassify)
		r.Get("/drafts", handler.Drafts)
		r.Post("/drafts/{id}/discard", handler.DraftDiscard)
	})

	// Admin routes run a synchronous batch (crawl every new company, one at a time, with a
	// polite per-page delay) — this can legitimately take minutes for a large batch. A 30s
	// timeout here doesn't actually stop the handler (it keeps running with a cancelled
	// context), it just breaks every downstream call still using that context — including
	// Redis writes — after the deadline. Give this group a timeout long enough that it never
	// gets hit in practice, until this becomes a real background job (see queue package).
	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(10 * time.Minute))

		r.Post("/admin/seed/{source}", handler.AdminSeed)
		r.Post("/admin/seed-us-jobs", handler.AdminSeedUSJobs)
		r.Post("/admin/enrich", handler.AdminEnrich)
		r.Post("/admin/draft", handler.LegacyGenerateClosed)
		r.Post("/admin/resolve-domains", handler.AdminResolveDomains)
		r.Post("/admin/domain-search/pause", handler.DomainSearchPause)
		r.Post("/admin/domain-search/resume", handler.DomainSearchResume)
		// Single-company enrich can, worst case, take up to 7 sequential page fetches at 10s
		// each — long enough to want the same generous timeout as the batch version.
		r.Post("/companies/{id}/enrich", handler.CompanyEnrich)
	})

	// Autonomous worker: the same seed/enrich/score dependencies the HTTP admin routes use,
	// ticker-triggered instead of click-triggered (docs/review.md — "autonomous daytime
	// prospecting"). See internal/worker for the loops themselves.
	workerCfg := worker.DefaultConfig()
	if v := os.Getenv("DISCOVERY_INTERVAL_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workerCfg.DiscoveryInterval = time.Duration(n) * time.Minute
		}
	}
	if v := os.Getenv("RESEARCH_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workerCfg.ResearchInterval = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("RESEARCH_TTL_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workerCfg.ResearchTTL = time.Duration(n) * time.Hour
		}
	}
	if v := os.Getenv("DRAFT_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workerCfg.DraftInterval = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("DOMAIN_RESOLVE_INTERVAL_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workerCfg.DomainResolveInterval = time.Duration(n) * time.Minute
		}
	}

	// handler already parsed AUTO_SEND_* into SendPolicy() — reused here rather than re-reading
	// the same env vars a second time, same reasoning as Enricher()/Scorer()/etc above.
	scheduler := worker.NewScheduler(db, redisConn, handler.Enricher(), handler.Scorer(), handler.SeedRegistry(), handler.Verifier(), handler.DomainSearcher(),
		handler.DraftService(), handler.HasAIProvider(), handler.AIBudget(), handler.SendPolicy(), handler, workerCfg)

	handler.SetWorkerStatus(scheduler.Status)
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	var workerWg sync.WaitGroup
	// Two separate decisions, deliberately not one.
	//
	// Discovery, research, scoring and domain resolution find and describe companies. None of
	// them sends anything, so they run in every mode except disabled: an operator working
	// manually still needs fresh opportunities, and gating research on send automation would
	// mean the corpus silently stopped growing.
	//
	// Sending automatically is the separate decision, and it requires the automation policy to
	// be configured at all. Even then the loop only asks the policy about already-reviewed
	// drafts, and the policy refuses by default.
	automationState := handler.AutomationState()
	if pipelineMode != send.ModeDisabled {
		scheduler.Run(workerCtx, &workerWg)
		log.Printf("Research workers running in %s. They discover and describe; they never send.", pipelineMode)
	} else {
		log.Print("Rollout mode is disabled: no background work runs at all")
	}
	if automationState.Enabled {
		runner := worker.NewAutomationRunner(db, handler.Dispatcher(), automationCfg,
			workerCfg.AutomationInterval, "automation-worker", handler.AIBudgetAvailable)
		runner.Run(workerCtx, &workerWg)
	} else {
		log.Printf("Automatic sending is off: %v", automationState.Reasons)
	}
	log.Printf("Worker configuration (research running=%v, automatic sending=%v): discovery every %v, research every %v, research TTL %v, draft check every %v (AI enabled=%v)",
		pipelineMode != send.ModeDisabled, automationState.Enabled,
		workerCfg.DiscoveryInterval, workerCfg.ResearchInterval, workerCfg.ResearchTTL, workerCfg.DraftInterval, handler.HasAIProvider())

	// Start server
	httpAddr := os.Getenv("HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = ":8080"
	}

	srv := &http.Server{Addr: httpAddr, Handler: r}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Starting Prospector on %s", httpAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
		close(serverErr)
	}()

	// Graceful shutdown: on Ctrl-C/SIGTERM, stop taking new HTTP requests, let the worker
	// finish whatever tick it's mid-way through, then close the shared connections. A long-
	// running personal tool that gets hard-killed mid-crawl every time it's stopped is exactly
	// the kind of thing that leaves a half-written record behind — this avoids that.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("Server error: %v", err)
		}
	case sig := <-sigCh:
		log.Printf("Received %v, shutting down gracefully...", sig)

		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		}
		cancelShutdown()

		cancelWorker()
		workerWg.Wait()

		log.Println("Shutdown complete")
	}
}
