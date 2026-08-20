package draft

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/activity"
	"github.com/anthonyalando8/prospector/internal/sendpolicy"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
)

// relevantSignalsForSegment narrows what grounds the model's observation/hypothesis for
// hiring/enterprise companies. A company actively hiring a backend developer has already
// demonstrated engineering awareness — citing a generic "we detected React/WordPress on your
// marketing site" signal as if it were evidence of a gap produces a non-sequitur pitch ("you
// may need backend help" to a company visibly building a backend team; a real example: a
// company hiring a Backend Developer got drafted a hypothesis about "managing transactions
// across a split WordPress and React setup", which doesn't follow from anything). The
// hiring_backend signal — almost always the richest, most specific evidence available, since
// it's the actual job ad text — and genuinely business-relevant signals (a real payment
// complaint, a paybill number) still ground the draft; only generic tech-stack/freshness
// signals detected off the marketing site are cut, and only once a hiring signal makes them
// redundant. Companies without a hiring_backend signal are untouched — for agency/incubator/sme
// segments those tech-stack signals are often the only grounding available at all.
func relevantSignalsForSegment(segment string, signals []*store.Signal) []*store.Signal {
	if segment != "hiring" && segment != "enterprise" {
		return signals
	}

	hasHiringSignal := false
	for _, s := range signals {
		if s.Kind == "hiring_backend" {
			hasHiringSignal = true
			break
		}
	}
	if !hasHiringSignal {
		return signals
	}

	var filtered []*store.Signal
	for _, s := range signals {
		if strings.HasPrefix(s.Kind, "tech:") || s.Kind == "checkout_present" || s.Kind == "active_site" || s.Kind == "stale_site" {
			continue
		}
		filtered = append(filtered, s)
	}
	return filtered
}

// Generated is the outcome of one GenerateAndSaveDraft call — a struct rather than a growing
// list of return values now that both the draft state and the send-policy decision need to be
// reported back to the caller.
type Generated struct {
	State     string // pending|needs_manual
	DraftID   int64
	Decision  sendpolicy.Decision // "" when State != "pending" — there's no send decision to make on a draft that isn't sendable
	Reasons   []string
	APICalled bool // false only when the pre-call cost guard (no signals) skipped the AI call entirely
}

// GenerateAndSaveDraft runs one company through the section 7 contract, persists the result as
// a draft row, and — for a real "pending" draft — evaluates internal/sendpolicy so the caller
// knows immediately whether this one is safe to auto-send, needs a human, or is blocked. This
// is the one place a draft actually gets created, shared by the HTTP admin batch, the
// single-company "Generate Draft" button, and the autonomous worker's draft loop, so none of
// them can drift from the others' needs_manual or policy handling.
func GenerateAndSaveDraft(ctx context.Context, db *store.DB, ts *TemplateService, policyCfg sendpolicy.Config, company *store.Company, redisClient *redis.Client) (*Generated, error) {
	activity.Log(ctx, redisClient, "worker:draft", fmt.Sprintf("Drafting for %s…", company.Name), true)

	signals, _ := db.GetSignalsByCompany(ctx, company.ID)
	contacts, _ := db.GetContactsByCompany(ctx, company.ID)

	firstName := "there"
	for _, c := range contacts {
		if c.PersonName != nil && *c.PersonName != "" {
			firstName = *c.PersonName
			break
		}
	}

	segment := "sme"
	if company.Segment != nil && *company.Segment != "" {
		segment = *company.Segment
	}

	role := ""
	if segment == "hiring" || segment == "enterprise" {
		role = "Backend Developer"
		for _, s := range signals {
			if s.Kind == "hiring_backend" {
				role = ExtractRole(s.Detail)
				break
			}
		}
	}

	result, err := ts.GenerateDraft(ctx, DraftInput{
		Company:   company,
		FirstName: firstName,
		Role:      role,
		Channel:   "email",
		Signals:   relevantSignalsForSegment(segment, signals),
	})
	if err != nil {
		return nil, err
	}

	if result.State == "needs_manual" {
		log.Printf("GenerateAndSaveDraft: %s needs manual — %s", company.Name, result.Reason)
		d, err := db.CreateDraft(ctx, company.ID, nil, result.Channel, "", result.Reason, "", "needs_manual")
		if err != nil {
			return nil, err
		}
		return &Generated{State: "needs_manual", DraftID: d.ID, APICalled: result.APICalled}, nil
	}

	d, err := db.CreateDraft(ctx, company.ID, nil, result.Channel, result.Subject, result.Body, result.EvidenceUsed, "pending")
	if err != nil {
		return nil, err
	}

	policyResult := evaluatePolicy(ctx, db, policyCfg, company, contacts, signals, d)
	if err := db.UpdateDraftDecision(ctx, d.ID, string(policyResult.Decision), policyResult.ReasonString(), company.Score); err != nil {
		log.Printf("GenerateAndSaveDraft: failed to record policy decision for draft %d: %v", d.ID, err)
	}

	return &Generated{
		State:     "pending",
		DraftID:   d.ID,
		Decision:  policyResult.Decision,
		Reasons:   policyResult.Reasons,
		APICalled: result.APICalled,
	}, nil
}

// evaluatePolicy resolves the cheap read-only facts sendpolicy.Evaluate needs (suppression,
// recent-contact-to-domain) and runs it. Kept here rather than inside sendpolicy itself so that
// package stays free of any store.DB dependency beyond the plain data types it already takes.
func evaluatePolicy(ctx context.Context, db *store.DB, cfg sendpolicy.Config, company *store.Company, contacts []*store.Contact, signals []*store.Signal, d *store.Draft) sendpolicy.Result {
	suppressed := false
	for _, c := range contacts {
		if c.Kind == "email" {
			if s, _ := db.IsSuppressed(ctx, c.Value); s {
				suppressed = true
			}
			break
		}
	}
	if !suppressed && company.Domain != "" {
		if s, _ := db.IsSuppressed(ctx, company.Domain); s {
			suppressed = true
		}
	}

	recentSameDomain := false
	if company.Domain != "" {
		if recent, _ := db.HasRecentSendToDomain(ctx, company.Domain, time.Now().AddDate(0, 0, -30)); recent {
			recentSameDomain = true
		}
	}

	return sendpolicy.Evaluate(cfg, sendpolicy.Input{
		Company:          company,
		Contacts:         contacts,
		Signals:          signals,
		Draft:            d,
		Suppressed:       suppressed,
		RecentSameDomain: recentSameDomain,
	})
}
