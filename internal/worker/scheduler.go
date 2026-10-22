// Package worker is the autonomous daytime prospecting loop (docs/review.md §4-5): a
// lightweight in-process scheduler, not a distributed queue. Each loop reuses the exact same
// functions the HTTP admin routes already call (seed.Registry.Run, enricher.EnrichCompany,
// scorer.Score) — a ticker triggers them instead of an HTTP POST, nothing about the
// processing itself is duplicated.
package worker

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anthonyalando8/prospector/internal/activity"
	"github.com/anthonyalando8/prospector/internal/aibudget"
	"github.com/anthonyalando8/prospector/internal/contactsearch"
	"github.com/anthonyalando8/prospector/internal/domainsearch"
	"github.com/anthonyalando8/prospector/internal/draft"
	"github.com/anthonyalando8/prospector/internal/enrich"
	"github.com/anthonyalando8/prospector/internal/score"
	"github.com/anthonyalando8/prospector/internal/seed"
	"github.com/anthonyalando8/prospector/internal/sendpolicy"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/anthonyalando8/prospector/internal/verify"
	"github.com/redis/go-redis/v9"
)

// Sender is the one capability the draft loop needs beyond drafting itself — sending an
// AUTO_SEND-decided draft through the exact same guarded path everything else uses. Defined
// here rather than imported so this package doesn't need to depend on internal/web; *web.Handler
// satisfies this via its exported ExecuteSend.
type Sender interface {
	ExecuteSend(ctx context.Context, companyID, draftID int64) error
}

// researchBatchSize caps how many companies one research tick processes — keeps a tick short
// so shutdown never has to wait long, and spreads crawling out over the day rather than
// bursting through the whole backlog at once.
const researchBatchSize = 5

// verifyBatchSize caps how many domains one discovery tick's verification pass checks.
const verifyBatchSize = 10

// Config holds the tunables review.md §5 and §33 ask to be configurable, with the doc's own
// suggested defaults.
type Config struct {
	DiscoveryInterval     time.Duration // default 20m
	ResearchInterval      time.Duration // default 30s
	ResearchTTL           time.Duration // default 24h — how long before a researched company is eligible again
	DraftInterval         time.Duration // default 60s — how often the loop *checks*; aibudget decides whether it actually drafts
	DomainResolveInterval time.Duration // default 30m — how often the loop *checks*; domainsearch's own daily budget decides whether it actually spends a query
	// AutomationInterval is how often selective automation looks for a candidate. It is slow on
	// purpose: the automation cap is one or two sends a day, so polling faster would only mean
	// re-deciding the same refusals.
	AutomationInterval time.Duration // default 15m
	// ContactDiscoveryInterval is how often the loop *checks* for a company blocked on contact
	// data. It is deliberately the slowest loop here: each company's own cooldown is measured in
	// days, and the daily budget is small, so checking more often would only re-read a queue
	// whose answer has not changed.
	ContactDiscoveryInterval time.Duration // default 45m
	// SuppressionInterval is how often postings are checked for having expired or quietly
	// disappeared from their source. Slow on purpose: the signals it reads are a published
	// closing date and a two-week absence, neither of which changes between ticks.
	SuppressionInterval time.Duration // default 6h
}

// DefaultConfig returns review.md's suggested defaults.
func DefaultConfig() Config {
	return Config{
		AutomationInterval:       15 * time.Minute,
		DiscoveryInterval:        20 * time.Minute,
		ResearchInterval:         30 * time.Second,
		ResearchTTL:              24 * time.Hour,
		DraftInterval:            60 * time.Second,
		DomainResolveInterval:    30 * time.Minute,
		ContactDiscoveryInterval: 45 * time.Minute,
		SuppressionInterval:      6 * time.Hour,
	}
}

// Scheduler owns the autonomous loops. It holds the same dependencies web.Handler holds,
// constructed once in main.go and handed to both — not a second copy of the pipeline.
type Scheduler struct {
	runtime        RuntimeStatus
	db             *store.DB
	redis          *redis.Client
	enricher       *enrich.Enricher
	scorer         *score.Scorer
	seedRegistry   *seed.Registry
	verifier       verify.Verifier
	domainSearcher *domainsearch.Searcher
	draftService   *draft.TemplateService
	contactEngine  *contactsearch.Engine
	hasAI          bool
	budget         *aibudget.Budget
	policyCfg      sendpolicy.Config
	sender         Sender
	cfg            Config

	discoveryBusy        atomic.Bool
	researchBusy         atomic.Bool
	draftBusy            atomic.Bool
	domainResolveBusy    atomic.Bool
	contactDiscoveryBusy atomic.Bool
	suppressionBusy      atomic.Bool
}

// NewScheduler creates a scheduler. Nothing runs until Run is called.
func NewScheduler(db *store.DB, redisConn *redis.Client, enricher *enrich.Enricher, scorer *score.Scorer, seedRegistry *seed.Registry, verifier verify.Verifier, domainSearcher *domainsearch.Searcher, draftService *draft.TemplateService, contactEngine *contactsearch.Engine, hasAI bool, budget *aibudget.Budget, policyCfg sendpolicy.Config, sender Sender, cfg Config) *Scheduler {
	return &Scheduler{
		db:             db,
		redis:          redisConn,
		enricher:       enricher,
		scorer:         scorer,
		seedRegistry:   seedRegistry,
		verifier:       verifier,
		domainSearcher: domainSearcher,
		draftService:   draftService,
		contactEngine:  contactEngine,
		hasAI:          hasAI,
		budget:         budget,
		policyCfg:      policyCfg,
		sender:         sender,
		cfg:            cfg,
	}
}

// Run launches every loop as its own goroutine and returns immediately. Each loop ticks once
// right away (no waiting out the first interval on a cold start) and then on its own ticker
// until ctx is cancelled. wg lets the caller wait for every loop to actually finish its
// current tick before the process exits (graceful shutdown).
func (s *Scheduler) Run(ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(6)
	go s.loop(ctx, wg, "discovery", s.cfg.DiscoveryInterval, s.discoveryTick)
	go s.loop(ctx, wg, "research", s.cfg.ResearchInterval, s.researchTick)
	go s.loop(ctx, wg, "draft", s.cfg.DraftInterval, s.draftTick)
	go s.loop(ctx, wg, "domain-resolve", s.cfg.DomainResolveInterval, s.domainResolveTick)
	go s.loop(ctx, wg, contactDiscoveryLoop, s.cfg.ContactDiscoveryInterval, s.contactDiscoveryTick)
	go s.loop(ctx, wg, suppressionLoop, s.cfg.SuppressionInterval, s.suppressionTick)
}

// loop runs tick immediately, then again every interval, until ctx is cancelled. A busy tick
// is never allowed to overlap the next — if a discovery pass is still running when its next
// interval fires, that tick is skipped rather than stacked.
func (s *Scheduler) loop(ctx context.Context, wg *sync.WaitGroup, name string, interval time.Duration, tick func(context.Context)) {
	defer wg.Done()
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	s.updateStatus(name, func(state *LoopStatus) { state.AliveAt = time.Now() })
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.updateStatus(name, func(state *LoopStatus) { state.AliveAt = time.Now() })
			}
		}
	}()
	run := func() {
		s.updateStatus(name, func(state *LoopStatus) { state.Busy = true; state.StartedAt = time.Now() })
		// One bad record must not take down the web server and every other loop with it: a
		// panicking tick is logged and the loop carries on at its next interval.
		func() {
			defer func() {
				if p := recover(); p != nil {
					log.Printf("worker: %s tick panicked: %v\n%s", name, p, debug.Stack())
				}
			}()
			tick(ctx)
		}()
		s.updateStatus(name, func(state *LoopStatus) { state.Busy = false; state.CompletedAt = time.Now() })
	}

	run()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("worker: %s loop stopping", name)
			return
		case <-ticker.C:
			run()
		}
	}
}

// discoveryTick seeds from the job board and, right after, runs a small verification pass
// over any domain that's still unknown/candidate — review.md's DISCOVER -> VERIFY sequence,
// folded into one loop since both are cheap and infrequent.
func (s *Scheduler) discoveryTick(ctx context.Context) {
	if !s.discoveryBusy.CompareAndSwap(false, true) {
		return // previous discovery tick still running
	}
	defer s.discoveryBusy.Store(false)

	result, err := s.seedRegistry.Run(ctx, "jobboard", s.db, s.redis)
	if err != nil {
		s.recordFailure("discovery")
		activity.Log(ctx, s.redis, "worker:discover", "Discovery cycle failed; inspect server logs", false)
	} else {
		activity.Log(ctx, s.redis, "worker:discover", fmt.Sprintf("discovered %d compan%s from jobboard", result.Added, pluralY(result.Added)), true)
	}

	s.verifyTick(ctx)
}

// verifyTick checks any company whose domain hasn't been confirmed yet — the gate that stops
// a guessed domain (review.md §10) from ever reaching the research loop.
func (s *Scheduler) verifyTick(ctx context.Context) {
	companies, err := s.db.ListCompaniesForVerification(ctx, verifyBatchSize)
	if err != nil {
		log.Printf("worker: verify: failed to list companies: %v", err)
		return
	}
	if len(companies) == 0 {
		return
	}

	verified, failed, corrected := 0, 0, 0
	for _, c := range companies {
		status, confidence, err := s.verifier.Verify(ctx, verify.Company{Name: c.Name, Domain: c.Domain})
		if err != nil {
			log.Printf("worker: verify: %s: %v", c.Name, err)
			continue
		}

		// The stored guess (extractDomain's first candidate) is often simply wrong for a
		// well-known company — try the rest of the ranked candidate list before giving up on
		// this company entirely, rather than stopping at the one guess that happened to get
		// stored at seed time.
		finalDomain := c.Domain
		if status != verify.StatusVerified {
			// Ordered by where the company was discovered: trying .co.ke first for a listing
			// from an international remote board wastes the attempt on a host that does not
			// exist, which is how 180 companies ended up unresearched.
			for _, cand := range verify.CandidateDomainsForMarket(c.Name, verify.MarketForSource(c.Source)) {
				if cand == c.Domain {
					continue // already just tried this one
				}
				time.Sleep(500 * time.Millisecond) // don't burst unrelated hosts back-to-back
				altStatus, altConfidence, altErr := s.verifier.Verify(ctx, verify.Company{Name: c.Name, Domain: cand})
				if altErr != nil {
					continue
				}
				if altStatus == verify.StatusVerified {
					status, confidence, finalDomain = altStatus, altConfidence, cand
					break
				}
			}
		}

		if finalDomain != c.Domain {
			if err := s.db.UpdateCompanyDomain(ctx, c.ID, finalDomain); err != nil {
				log.Printf("worker: verify: failed to correct domain for %s: %v", c.Name, err)
			} else {
				corrected++
			}
		}

		if err := s.db.UpdateCompanyDomainStatus(ctx, c.ID, string(status), confidence); err != nil {
			log.Printf("worker: verify: failed to update %s: %v", c.Name, err)
			continue
		}
		if status == verify.StatusVerified {
			verified++
		} else {
			failed++
		}
	}

	detail := fmt.Sprintf("checked %d domain%s: %d verified, %d failed", len(companies), pluralS(len(companies)), verified, failed)
	if corrected > 0 {
		detail += fmt.Sprintf(" (%d found on a different guess than the first)", corrected)
	}
	activity.Log(ctx, s.redis, "worker:verify", detail, true)
}

// researchTick enriches a small batch of companies with a trusted domain, scoring each one
// immediately after — the same two calls AdminEnrich already makes, just ticker-triggered.
func (s *Scheduler) researchTick(ctx context.Context) {
	if !s.researchBusy.CompareAndSwap(false, true) {
		return // previous research tick still running
	}
	defer s.researchBusy.Store(false)

	companies, err := s.db.ListCompaniesForResearch(ctx, researchBatchSize)
	if err != nil {
		s.recordFailure("research")
		activity.Log(ctx, s.redis, "worker:research", "Unable to load research queue", false)
		log.Printf("worker: research: failed to list companies: %v", err)
		return
	}
	if len(companies) == 0 {
		return
	}

	researched := 0
	attempted, failures, skipped := 0, 0, 0
	for _, c := range companies {
		if s.enricher.RecentlyAttempted(ctx, c.ID) {
			skipped++
			continue
		}

		attempted++
		s.updateStatus("research", func(state *LoopStatus) { state.Company = c.Name })
		found, err := s.enricher.EnrichCompany(ctx, c, s.db)
		if err != nil {
			failures++
			s.recordFailure("research")
			activity.Log(ctx, s.redis, "worker:research", "Research operation failed for "+c.Name, false)
			log.Printf("worker: research: failed to enrich %s: %v", c.Name, err)
			continue
		}
		// Whether or not anything was found, this company was just attempted — don't let the
		// research loop hammer it again every 30s. TTL applies equally to a dead site (cooldown
		// already covers that path too) and a live one that simply had nothing new.
		if err := s.db.MarkResearched(ctx, c.ID, s.cfg.ResearchTTL); err != nil {
			failures++
			s.recordFailure("research")
			activity.Log(ctx, s.redis, "worker:research", "Research operation failed for "+c.Name, false)
			log.Printf("worker: research: failed to schedule next research for %s: %v", c.Name, err)
		}
		if !found {
			continue
		}

		if err := s.scorer.Score(ctx, c, s.db); err != nil {
			failures++
			s.recordFailure("research")
			activity.Log(ctx, s.redis, "worker:research", "Research operation failed for "+c.Name, false)
			log.Printf("worker: research: failed to score %s: %v", c.Name, err)
			continue
		}
		researched++
	}

	activity.Log(ctx, s.redis, "worker:research",
		fmt.Sprintf("Attempted %d companies; %d yielded evidence; %d errors; %d in cooldown", attempted, researched, failures, skipped),
		failures == 0)
}

// draftTick is the autonomous side of review.md §7/§8: only the strongest, deterministically
// eligible prospect is offered to the AI, and only when the budget allows it — checkCooldown
// is true here specifically because this is the system spending quota on its own initiative,
// not a human explicitly asking for one company's draft. Running out of budget is silent and
// non-fatal: research and scoring keep going regardless (review.md §6 — "the system must
// remain productive even when AI quota is exhausted").
func (s *Scheduler) draftTick(ctx context.Context) {
	if s.draftService == nil || !s.hasAI {
		return
	}
	if !s.draftBusy.CompareAndSwap(false, true) {
		return
	}
	defer s.draftBusy.Store(false)

	if allowed, _ := s.budget.Allow(ctx, true); !allowed {
		return // quiet — logging "budget exhausted" every tick would just be noise
	}

	// One at a time: the cooldown between autonomous draft calls (AI_DRAFT_COOLDOWN_SECONDS)
	// is the real pacing mechanism here, not the batch size — review.md §45, quality over
	// volume. The next tick re-checks Allow, so nothing is lost by only taking the single best
	// candidate each time.
	companies, err := s.db.ListCompaniesForDrafting(ctx, s.budget.MinScore(), 1)
	if err != nil {
		log.Printf("worker: draft: failed to list companies: %v", err)
		return
	}
	if len(companies) == 0 {
		return
	}

	company := companies[0]
	gen, err := draft.GenerateAndSaveDraft(ctx, s.db, s.draftService, s.policyCfg, company, s.redis)
	if gen != nil && gen.APICalled {
		s.budget.Record(ctx)
	}
	if err != nil {
		log.Printf("worker: draft: failed for %s: %v", company.Name, err)
		activity.Log(ctx, s.redis, "worker:draft", fmt.Sprintf("failed to draft %s: %v", company.Name, err), false)
		return
	}

	activity.Log(ctx, s.redis, "worker:draft", fmt.Sprintf("drafted %s (score %d, %s)", company.Name, company.Score, gen.State), true)

	// The send policy already decided this one is confident enough to go out unattended —
	// review.md's "Critical Workflow Change": AUTO_SEND is the normal path, not the exception.
	// executeSend (via Sender) remains the one real technical guard either way; if it refuses
	// for a reason the policy couldn't see (daily cap hit since, a race on suppression), the
	// draft just stays pending and a human picks it up in /review like any other.
	if gen.Decision == sendpolicy.AutoSend {
		if err := s.sender.ExecuteSend(ctx, company.ID, gen.DraftID); err != nil {
			log.Printf("worker: draft: auto-send failed for %s: %v — left pending for review", company.Name, err)
			activity.Log(ctx, s.redis, "worker:auto_send", fmt.Sprintf("auto-send failed for %s: %v", company.Name, err), false)
		} else {
			activity.Log(ctx, s.redis, "worker:auto_send", fmt.Sprintf("auto-sent to %s (score %d)", company.Name, company.Score), true)
		}
	}
}

// domainResolveBatchSize caps how many companies one tick's search budget goes toward — a
// generous batch is harmless since domainsearch.Allow() stops the loop early once today's real
// query budget (Google's free tier) runs out, regardless of this number.
const domainResolveBatchSize = 5

// domainResolveTick asks internal/domainsearch to find a real domain for companies stuck at
// domain_status='failed' — every guessed candidate from verify.CandidateDomains already
// exhausted during the normal verify loop. A no-op when GOOGLE_SEARCH_API_KEY/GOOGLE_SEARCH_CX
// aren't configured, the same way draftTick is a no-op without an AI provider.
func (s *Scheduler) domainResolveTick(ctx context.Context) {
	if s.domainSearcher == nil || !s.domainSearcher.Enabled() {
		return
	}
	if !s.domainResolveBusy.CompareAndSwap(false, true) {
		return
	}
	defer s.domainResolveBusy.Store(false)

	resolved, attempted, err := domainsearch.ResolveFailedDomains(ctx, s.db, s.verifier, s.domainSearcher, s.redis, domainResolveBatchSize)
	if err != nil {
		log.Printf("worker: domain-resolve: %v", err)
		return
	}
	if attempted == 0 {
		return // nothing waiting, or today's budget already spent — quiet, not worth logging every tick
	}

	activity.Log(ctx, s.redis, "worker:domain_resolve", fmt.Sprintf("resolved %d of %d attempted", resolved, attempted), resolved > 0)
}

// contactDiscoveryBatchSize caps how many companies one tick considers. A generous batch is
// harmless because the budget stops the run long before this number does; what actually paces
// this loop is the daily company allowance and each company's own multi-day cooldown.
const contactDiscoveryBatchSize = 10

// contactDiscoveryLoop is the loop's name in the status report.
const contactDiscoveryLoop = "contact-discovery"

// contactDiscoveryTick looks for a relevant opportunity with nowhere acceptable to send, and
// searches the public web for an application route or a hiring contact.
//
// It runs alongside discovery and research and is entirely independent of sending: automation
// being off does not stop it, for the same reason research does not stop. It finds contact data;
// it creates no plan, approves nothing and sends nothing, so there is no version of this loop
// running that could put a message in front of anyone.
func (s *Scheduler) contactDiscoveryTick(ctx context.Context) {
	if s.contactEngine == nil {
		return
	}
	if !s.contactDiscoveryBusy.CompareAndSwap(false, true) {
		return // the previous tick is still working; never stack two searches
	}
	defer s.contactDiscoveryBusy.Store(false)

	batch, err := s.contactEngine.RunBatch(ctx, contactDiscoveryBatchSize)
	if err != nil {
		s.recordFailure(contactDiscoveryLoop)
		log.Printf("worker: contact-discovery: %v", err)
		activity.Log(ctx, s.redis, "worker:contact_discovery", "Contact discovery cycle failed; inspect server logs", false)
		return
	}
	if batch.Examined == 0 {
		return // nothing eligible, or every eligible company is in cooldown — not worth logging
	}
	detail := fmt.Sprintf("examined %d compan%s: %d searched, %d gained an acceptable destination, %d skipped, %d failed",
		batch.Examined, pluralY(batch.Examined), batch.Searched, batch.Resolved, batch.Skipped, batch.Failed)
	if batch.StopEarly != "" {
		detail += " (stopped early: " + batch.StopEarly + ")"
	}
	activity.Log(ctx, s.redis, "worker:contact_discovery", detail, batch.Failed == 0)
}

// suppressionLoop is the loop's name in the status report.
const suppressionLoop = "suppression"

// suppressionTick takes postings out of the working queues once they are demonstrably no longer
// live: the source marked them closed, the published closing date has passed, or they stopped
// appearing in a feed that is still being read successfully.
//
// Without this loop the expiry date every adapter stores was never read by anything, and a
// posting taken down last month stayed in the priority list, the dashboard queue and contact
// discovery indefinitely. It writes no messages and contacts no one — it only stops work being
// offered on vacancies that have ended.
func (s *Scheduler) suppressionTick(ctx context.Context) {
	if !s.suppressionBusy.CompareAndSwap(false, true) {
		return // the previous sweep is still working
	}
	defer s.suppressionBusy.Store(false)

	result, err := s.db.SweepOpportunitySuppressions(ctx, store.DefaultSuppressionPolicy())
	if err != nil {
		s.recordFailure(suppressionLoop)
		log.Printf("worker: suppression: %v", err)
		activity.Log(ctx, s.redis, "worker:suppression", "Opportunity suppression sweep failed; inspect server logs", false)
		return
	}
	if len(result.Errors) > 0 {
		s.recordFailure(suppressionLoop)
		log.Printf("worker: suppression: %d of %d decisions failed, first: %v",
			len(result.Errors), len(result.Errors)+result.Total(), result.Errors[0])
	}
	if result.Total() == 0 {
		return // nothing has expired since the last sweep — not worth logging
	}
	counts := result.ByBasis()
	detail := fmt.Sprintf("suppressed %d opportunit%s: %d marked closed by the source, %d past their closing date, %d gone from a live feed",
		result.Total(), pluralY(result.Total()), counts[store.BasisSourceClosed],
		counts[store.BasisExpiryPassed], counts[store.BasisAbsentFromSource])
	activity.Log(ctx, s.redis, "worker:suppression", detail, len(result.Errors) == 0)
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
