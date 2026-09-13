package draft

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/activity"
	"github.com/anthonyalando8/prospector/internal/score"
	"github.com/anthonyalando8/prospector/internal/sendpolicy"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
)

// isNoiseSignalForTechnicalAudience reports whether a signal is about a company's own marketing
// site — its tech stack, or whether it looks freshly updated — as opposed to a genuine business
// signal (a real payment complaint, a paybill number, the job ad text itself).
func isNoiseSignalForTechnicalAudience(kind string) bool {
	return strings.HasPrefix(kind, "tech:") || kind == "checkout_present" || kind == "active_site" || kind == "stale_site"
}

// relevantSignalsForSegment narrows what grounds the model's observation/hypothesis once a
// company has already demonstrated it's the wrong audience for "your own site looks off" as a
// hook.
//
// hiring/enterprise: a company actively hiring a backend developer has already demonstrated
// engineering awareness — citing a generic "we detected React/WordPress on your marketing site"
// signal as if it were evidence of a gap produces a non-sequitur pitch (a real example: a
// company hiring a Backend Developer got drafted a hypothesis about "managing transactions
// across a split WordPress and React setup", which doesn't follow from anything). Only filtered
// once a hiring_backend signal is actually present — otherwise there's nothing to prefer it over.
//
// agency: a company that itself builds websites, apps, or software for clients. Its own site
// running WordPress, or looking a year or two stale, is never a useful hook — at best it's
// irrelevant (that's not what the pitch is about), at worst it reads as "you don't know your own
// site is outdated?", which is exactly the kind of email that loses a technical reader (a real
// sent example: "Your website has not been updated since 2022 and is currently running on
// WordPress" — to a company that sells web design as a service). Filtered unconditionally for
// this segment, not gated behind a specific signal — the segment itself is the evidence a
// capability-focused pitch is what's needed, not a critique. See offers_tech_services
// (internal/enrich) for the signal meant to replace it as grounding when present; when nothing
// else is left, GenerateDraft's zero-signals cost guard correctly falls back to needs_manual
// rather than force a weak or condescending observation.
//
// Every other segment (incubator/sme) is untouched — those tech-stack/freshness signals are
// often the only grounding available at all for a genuinely non-technical business, where "your
// site hasn't been touched since 2022" is a legitimate, relatable pain point rather than an
// insult.
func relevantSignalsForSegment(segment string, signals []*store.Signal) []*store.Signal {
	if segment == "agency" {
		var filtered []*store.Signal
		for _, s := range signals {
			if !isNoiseSignalForTechnicalAudience(s.Kind) {
				filtered = append(filtered, s)
			}
		}
		return filtered
	}

	if segment != "hiring" && segment != "enterprise" && segment != "hiring_us" && segment != "application" {
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
		if !isNoiseSignalForTechnicalAudience(s.Kind) {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// The capability lines servicePitch chooses between — hand-written, reviewed sentences, not
// AI-generated text (the spec's rule that only observation/hypothesis come from the model still
// holds; this only deterministically picks among a few fixed options). Written as connected
// prose deliberately, not a comma-spliced feature list ("STK push, verified callbacks, no double
// charges, automatic reconciliation") — that reads like a spec sheet dropped into a sentence, the
// single most common "this was clearly AI-written" tell across every template before this pass.
// The "US" variants exist because M-Pesa is a Kenya-specific payment rail — naming it to a
// US/international remote employer is meaningless at best, confusing at worst; the underlying
// skill (payments integration, third-party API work) is the same, just not branded to a rail
// their market doesn't use.
const (
	servicePitchPayments   = "getting payments and M-Pesa integrations to actually work, with callbacks verified and nobody charged twice"
	servicePitchBackend    = "a Go API your frontend or app team can actually build against, auth and deployment included"
	servicePitchGeneral    = "payments and M-Pesa work, an API your team can build a frontend against, or picking up a backend piece mid-project"
	servicePitchPaymentsUS = "getting payment integrations to actually work, with callbacks verified and nobody charged twice"
	servicePitchGeneralUS  = "payments work, an API your team can build a frontend against, or picking up a backend piece mid-project"
)

// paymentRoleKeywords catches a hiring_backend job ad that's explicitly about payments or
// finance work (e.g. "Checkoff Product Scheme Manager") — a far stronger, more specific signal
// for leading with the M-Pesa/payments pitch than "checkout functionality detected on the
// marketing site" ever is, and one the generic paybill_listed/payment_complaint site signals
// wouldn't catch since it comes from the job ad text, not the company's own site.
var paymentRoleKeywords = []string{
	"payment", "payroll", "finance", "billing", "reconciliation", "mpesa", "m-pesa",
	"checkoff", "wallet", "collections", "treasury",
}

// servicePitch picks which capability to lead the "here's what I can do for you" line with,
// based on real signals — instead of every email defaulting to M-Pesa/payments regardless of
// whether that's actually the fit (a company hiring a generic "Backend Developer" with no
// payments angle at all was getting the same M-Pesa-first pitch as one explicitly hiring for a
// payments/payroll role). Falls back to the original balanced phrasing when nothing points
// clearly one way. market == "us" swaps the M-Pesa-branded payments wording for generic
// "payment integrations" — see the servicePitch* constants above.
func servicePitch(signals []*store.Signal, market string) string {
	hasPaymentSignal := false
	hasBackendGapSignal := false

	for _, s := range signals {
		switch {
		case s.Kind == "paybill_listed" || s.Kind == "payment_complaint" || s.Kind == "checkout_present":
			hasPaymentSignal = true
		case s.Kind == "hiring_backend" && containsAny(strings.ToLower(s.Detail), paymentRoleKeywords):
			hasPaymentSignal = true
		case s.Kind == "offers_tech_services" || strings.HasPrefix(s.Kind, "tech:"):
			hasBackendGapSignal = true
		}
	}

	isUS := market == "us"
	switch {
	case hasPaymentSignal && isUS:
		return servicePitchPaymentsUS
	case hasPaymentSignal:
		return servicePitchPayments
	case hasBackendGapSignal:
		return servicePitchBackend
	case isUS:
		return servicePitchGeneralUS
	default:
		return servicePitchGeneral
	}
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
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
	segment := "sme"
	if company.Segment != nil && *company.Segment != "" {
		segment = *company.Segment
	}

	// CAN-SPAM requires a valid physical postal address in every commercial email sent to a US
	// recipient — this is a hard legal requirement, not a style choice, and unlike the Kenya
	// templates there's no existing line that covers it. Keyed off the company's source, not its
	// segment: "application" mode (a human's manual override, see classifySegment) can put a
	// US-sourced company on a different segment without changing which market it's actually in,
	// and the guard must still apply. Refuse outright rather than send a non-compliant email;
	// this check must run before any AI call is made (same cost-guard philosophy as the
	// zero-signals check below — never spend a call on a draft that can't be sent regardless of
	// what it says).
	isUSSource := score.UsJobSources[company.Source]
	mailingAddress := os.Getenv("US_MAILING_ADDRESS")
	if isUSSource && mailingAddress == "" {
		reason := "US_MAILING_ADDRESS not configured — required by CAN-SPAM before any US-market email can send"
		activity.Log(ctx, redisClient, "worker:draft", fmt.Sprintf("Skipped %s: %s", company.Name, reason), false)
		d, err := db.CreateDraft(ctx, company.ID, nil, "email", "", reason, "", "needs_manual")
		if err != nil {
			return nil, err
		}
		return &Generated{State: "needs_manual", DraftID: d.ID}, nil
	}

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

	role := ""
	if segment == "hiring" || segment == "enterprise" || segment == "hiring_us" || segment == "application" {
		role = ""
		for _, s := range signals {
			if s.Kind == "hiring_backend" {
				role = ExtractRole(s.Detail)
				break
			}
		}
	}

	market := ""
	if isUSSource {
		market = "us"
	}

	result, err := ts.GenerateDraft(ctx, DraftInput{
		Company:        company,
		FirstName:      firstName,
		Role:           role,
		Channel:        "email",
		Signals:        relevantSignalsForSegment(segment, signals),
		ServicePitch:   servicePitch(signals, market),
		MailingAddress: mailingAddress,
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
