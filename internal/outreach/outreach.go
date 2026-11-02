// Package outreach is the step between "a strong reason to write exists" and "an email is ready
// for the sending policy": it records a contact plan and composes a reviewed draft for an
// opportunity worth applying to, without anyone advancing it by hand.
//
// It is deliberately narrow. It only writes job applications for strong matches with a confirmed
// employer domain and a usable recipient; everything else (a possible match, a generic mailbox, a
// plan that needs review, business prospecting) is left for a person, exactly as before. It never
// sends anything: what it writes goes to the sending policy like any other draft, and it only runs
// when the operator has switched automatic drafting on.
package outreach

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/anthonyalando8/prospector/internal/assess"
	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/draft"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
)

// DefaultDailyDrafts bounds how many drafts the worker starts per day (OUTREACH_DAILY_DRAFTS).
// Each draft costs up to six AI requests (compose and review, with two repairs), drawn from the
// same AI request budget as manual drafting.
const DefaultDailyDrafts = 3

// Result is what one tick did.
type Result struct {
	Considered, Drafted, Skipped int
	Off, CapReached              bool
	SkipReasons                  map[string]int
}

// Worker holds what one tick needs.
type Worker struct {
	DB     *store.DB
	Redis  *redis.Client
	Assess *assess.Service
	// Composer and Reviewer are the configured providers; nil means drafting is unavailable.
	Composer                         composition.Composer
	Reviewer                         composition.Reviewer
	HourLimit, DayLimit, DailyDrafts int
}

// FromEnvironment builds the worker from the same settings manual composition uses.
func FromEnvironment(db *store.DB, rdb *redis.Client) *Worker {
	w := &Worker{DB: db, Redis: rdb, Assess: assess.New(db), DailyDrafts: DefaultDailyDrafts}
	w.HourLimit, _ = strconv.Atoi(os.Getenv("AI_REQUESTS_PER_HOUR"))
	w.DayLimit, _ = strconv.Atoi(os.Getenv("AI_REQUESTS_PER_DAY"))
	if n, err := strconv.Atoi(os.Getenv("OUTREACH_DAILY_DRAFTS")); err == nil && n >= 0 {
		w.DailyDrafts = n
	}
	if os.Getenv("EMAIL_COMPOSITION_ENABLED") == "true" {
		if c, err := composition.NewConfigured("COMPOSE"); err == nil {
			w.Composer = c
		}
		if r, err := composition.NewConfigured("REVIEW"); err == nil {
			w.Reviewer = r
		}
	}
	return w
}

// skipFor remembers, per opportunity revision, that it was looked at and left for a person, so the
// same handful of postings does not crowd every tick. A changed revision is looked at afresh.
const skipFor = 24 * time.Hour

// Tick drafts at most one email: drafting is the expensive step, and one per tick spreads the AI
// budget over the day. It returns without doing anything when automatic drafting is off.
func (w *Worker) Tick(ctx context.Context) (Result, error) {
	res := Result{SkipReasons: map[string]int{}}
	if !w.DB.SettingOn(ctx, store.SettingAutoCompose) {
		res.Off = true
		return res, nil
	}
	if w.Composer == nil || w.Reviewer == nil {
		return res, errors.New("automatic drafting is on but the compose/review providers are not configured")
	}
	if w.DailyDrafts <= 0 {
		res.CapReached = true
		return res, nil
	}
	today := time.Now()
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
	if n, err := w.DB.AutoComposedSince(ctx, today); err != nil {
		return res, err
	} else if n >= w.DailyDrafts {
		res.CapReached = true
		return res, nil
	}
	candidates, err := w.DB.OutreachCandidates(ctx, 50)
	if err != nil {
		return res, err
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		key := fmt.Sprintf("outreach:skip:%d:%d", c.OpportunityID, c.Revision)
		if w.Redis != nil {
			if n, err := w.Redis.Exists(ctx, key).Result(); err == nil && n > 0 {
				continue
			}
		}
		res.Considered++
		reason, err := w.draftOne(ctx, c)
		if err != nil {
			log.Printf("outreach: opportunity %d: %v", c.OpportunityID, err)
			reason = "error"
		}
		if reason == "" {
			res.Drafted++
			return res, nil
		}
		res.Skipped++
		res.SkipReasons[reason]++
		if w.Redis != nil {
			w.Redis.Set(ctx, key, reason, skipFor)
		}
	}
	return res, nil
}

// draftOne returns "" when it started a draft, or why it left the opportunity for a person.
func (w *Worker) draftOne(ctx context.Context, c store.OutreachCandidate) (string, error) {
	proposal, err := w.Assess.Propose(ctx, c.CompanyID, c.OpportunityID, plan.SpecificJobApplication)
	if err != nil {
		return "", err
	}
	if !proposal.View.Profile.Active {
		return "profile_not_active", nil
	}
	if proposal.Decision.Outcome != plan.OutcomeSuggested || proposal.Decision.RequiresReview {
		return "plan_needs_review", nil
	}
	recipient := bestRecipient(proposal.Recipients)
	if recipient == nil {
		// No usable recipient: the posting stays available for applying through its own route
		// and for contact discovery.
		return "no_usable_recipient", nil
	}
	created, err := w.ownPlan(ctx, c)
	if err != nil {
		return "", err
	}
	if created == nil {
		if created, err = w.Assess.CreatePlan(ctx, assess.CreatePlanRequest{CompanyID: c.CompanyID, OpportunityID: c.OpportunityID,
			Intent: plan.SpecificJobApplication, ContactID: recipient.Assessment.Contact.ID, Origin: "suggested"}); err != nil {
			return "plan_refused", err
		}
	}
	snapshot, err := w.DB.BuildCompositionSnapshot(ctx, created.ID)
	if err != nil {
		return "snapshot_failed", err
	}
	draftID, err := w.DB.QueueComposition(ctx, snapshot)
	var exists *store.CompositionExistsError
	if errors.As(err, &exists) {
		return "already_drafted", nil
	}
	if err != nil {
		return "queue_failed", err
	}
	owner, err := w.DB.ClaimComposition(ctx, draftID, 0, 6, w.HourLimit, w.DayLimit)
	if err != nil {
		// The AI request budget is used up: release the admission without spending anything.
		_ = w.DB.CancelQueuedComposition(ctx, draftID)
		return "ai_budget_unavailable", nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 7*time.Minute)
	defer cancel()
	if err := draft.RunComposition(runCtx, w.DB, draftID, owner, snapshot, w.Composer, w.Reviewer, nil); err != nil {
		return "", fmt.Errorf("draft %d: %w (its attempts are recorded)", draftID, err)
	}
	log.Printf("outreach: drafted %d for opportunity %d (plan %d)", draftID, c.OpportunityID, created.ID)
	return "", nil
}

// ownPlan returns the plan this worker already recorded for the posting, when an earlier draft
// never completed, so a retry continues it instead of being refused as a second plan.
func (w *Worker) ownPlan(ctx context.Context, c store.OutreachCandidate) (*store.ContactPlan, error) {
	plans, err := w.DB.ListContactPlansForCompany(ctx, c.CompanyID)
	if err != nil {
		return nil, err
	}
	for _, p := range plans {
		if p.SelectionOrigin == "suggested" && p.Status == "selected" && p.OpportunityID != nil && *p.OpportunityID == c.OpportunityID {
			return p, nil
		}
	}
	return nil, nil
}

// bestRecipient is the first preferred, published or acceptable non-generic recipient, in the
// ranking's own order. A generic mailbox or an unverified address is never chosen by the worker.
func bestRecipient(options []plan.RecipientOption) *plan.RecipientOption {
	for i := range options {
		o := options[i]
		if o.SourcePublished || o.Relevance == plan.RecipientPreferred || o.Relevance == plan.RecipientAcceptable {
			return &options[i]
		}
	}
	return nil
}
