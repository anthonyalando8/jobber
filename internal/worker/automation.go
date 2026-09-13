package worker

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/anthonyalando8/prospector/internal/automation"
	"github.com/anthonyalando8/prospector/internal/dispatch"
	"github.com/anthonyalando8/prospector/internal/store"
)

// AutomationRunner polls for drafts that selective automation might send. It is a polling loop
// over the existing database, not a new queue: the candidate set is a narrow SQL query over
// reviewed v2 compositions, and exclusion between workers is a durable claim row.
//
// The loop does not decide anything. It finds candidates and hands each to the automation
// policy, which refuses by default. A loop that ran every second with an empty allowlist would
// still send nothing.
type AutomationRunner struct {
	db         *store.DB
	dispatcher *dispatch.Service
	config     automation.Config
	interval   time.Duration
	// budget reports whether AI request capacity remains. Automation shares the same budget as
	// manual work rather than having an allowance of its own.
	budget func(context.Context) (bool, string)
	owner  string
}

func NewAutomationRunner(db *store.DB, dispatcher *dispatch.Service, config automation.Config,
	interval time.Duration, owner string, budget func(context.Context) (bool, string)) *AutomationRunner {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if owner == "" {
		owner = "automation-worker"
	}
	return &AutomationRunner{db: db, dispatcher: dispatcher, config: config,
		interval: interval, owner: owner, budget: budget}
}

// Run starts the loop. It refuses to start at all unless selective automation is configured, so
// an unconfigured deployment has no automation code running in it rather than a loop that
// happens to always decline.
func (r *AutomationRunner) Run(ctx context.Context, wg *sync.WaitGroup) bool {
	state := r.dispatcher.Automation()
	if !state.Enabled {
		log.Printf("automation: not started (%v)", state.Reasons)
		return false
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("automation: polling every %v for %v; every message is decided individually",
			r.interval, state.Allowlist)
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Print("automation: loop stopping")
				return
			case <-ticker.C:
				r.Tick(ctx)
			}
		}
	}()
	return true
}

// Tick considers one batch of candidates. Deliberately small: this exists for controlled
// observation, and the automation cap will stop it long before the batch does.
func (r *AutomationRunner) Tick(ctx context.Context) {
	// Unresolved delivery state halts automation entirely. The system does not know what it has
	// already sent, so it must not decide what to send next.
	if unresolved, err := r.db.UnresolvedSendAttempts(ctx); err != nil {
		log.Printf("automation: cannot read send state; skipping this tick")
		return
	} else if unresolved > 0 {
		log.Printf("automation: halted, %d send attempt(s) unresolved. A person must establish what happened.", unresolved)
		return
	}

	available, reason := true, ""
	if r.budget != nil {
		available, reason = r.budget(ctx)
	}

	candidates, err := r.db.AutomationCandidates(ctx, 10)
	if err != nil {
		log.Print("automation: cannot list candidates")
		return
	}
	cfg := dispatch.AutomationConfig{Config: r.config, BudgetAvailable: available, BudgetReason: reason}
	for _, draftID := range candidates {
		select {
		case <-ctx.Done():
			return
		default:
		}
		out, err := r.dispatcher.AutoSend(ctx, draftID, r.owner, cfg)
		switch {
		case err == store.ErrNotClaimed:
			continue
		case err != nil:
			log.Printf("automation: draft %d could not be processed", draftID)
			continue
		case out.Withdrawn != "":
			log.Printf("automation: draft %d was withdrawn before transmission (%s)", draftID, out.Withdrawn)
		case out.Sent != nil && out.Sent.Decision.Allowed:
			log.Printf("automation: draft %d sent automatically", draftID)
			// One automatic send per tick. The cap is checked per message anyway; stopping here
			// means a misconfiguration cannot empty the queue in a single pass.
			return
		default:
			log.Printf("automation: draft %d stays in review (%v)", draftID, out.Decision.Blockers.Codes())
		}
	}
}
