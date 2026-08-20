// Package sendpolicy is the "Critical Workflow Change" decision engine from review.md: every
// generated draft gets exactly one of AUTO_SEND / REVIEW_REQUIRED / BLOCKED, with structured
// reasons — never a bare boolean, so the history always explains why a message went out
// unattended, was flagged, or was refused. This package only decides; it never sends anything
// itself, and it deliberately doesn't duplicate executeSend's hard guards (suppression, the
// 30-day rule, daily/interval caps) — those remain the one real technical gate. What lives
// here is judgment about draft *quality and confidence*: is this specific message good enough
// that a human watching wouldn't have added anything.
package sendpolicy

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/anthonyalando8/prospector/internal/tonecheck"
)

// Decision is one of exactly three outcomes — review.md §1: "the system needs to know WHY a
// message was automatically sent, flagged, or blocked," which a plain approved=true/false
// can't express.
type Decision string

const (
	AutoSend       Decision = "auto_send"
	ReviewRequired Decision = "review_required"
	Blocked        Decision = "blocked"
)

// Config is every threshold review.md's "Critical Workflow Change" §6 asks to be
// configurable, with the doc's own suggested defaults. The gap between ReviewMinScore (65)
// and AutoSendMinScore (80) is deliberate headroom, not an oversight — a score in that band is
// good enough to bother a human with, not good enough to trust unattended.
type Config struct {
	Enabled              bool
	AutoSendMinScore     int
	ReviewMinScore       int
	RequireFreshEvidence bool
	MaxEvidenceAge       time.Duration
}

// DefaultConfig returns review.md's suggested defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:              true,
		AutoSendMinScore:     80,
		ReviewMinScore:       65,
		RequireFreshEvidence: true,
		MaxEvidenceAge:       72 * time.Hour,
	}
}

// Input is everything Evaluate needs about one freshly-created pending draft. Signals should
// be every signal on the company (Evaluate resolves which ones the draft actually cited),
// Suppressed and RecentSameDomain are cheap lookups the caller already has easy access to via
// store.DB, kept out of this package to avoid it reaching back into the database itself.
type Input struct {
	Company          *store.Company
	Contacts         []*store.Contact
	Signals          []*store.Signal
	Draft            *store.Draft
	Suppressed       bool
	RecentSameDomain bool
}

// Result is the decision plus the structured reasons behind it (review.md §7/§8).
type Result struct {
	Decision Decision
	Reasons  []string
}

func (r Result) ReasonString() string { return strings.Join(r.Reasons, ",") }

// Evaluate decides what to do with one drafted email. BLOCKED checks run first and return
// immediately — those are hard stops nothing should override. Everything after is a
// confidence judgment: AUTO_SEND only when every quality signal passes at once, REVIEW_REQUIRED
// the moment any one of them is merely uncertain rather than wrong. review.md §11: "if
// uncertain, REVIEW_REQUIRED, not AUTO_SEND."
func Evaluate(cfg Config, in Input) Result {
	if !cfg.Enabled {
		return Result{Decision: ReviewRequired, Reasons: []string{"auto_send_disabled"}}
	}

	// --- Hard stops: something is actually wrong, not just uncertain. ---

	if in.Company.DomainStatus != "verified" && in.Company.DomainStatus != "manual" {
		return Result{Decision: Blocked, Reasons: []string{"domain_unverified"}}
	}

	hasEmail := false
	for _, c := range in.Contacts {
		if c.Kind == "email" {
			hasEmail = true
			break
		}
	}
	if !hasEmail {
		return Result{Decision: Blocked, Reasons: []string{"no_contact"}}
	}

	if in.Suppressed {
		return Result{Decision: Blocked, Reasons: []string{"suppressed"}}
	}

	if in.RecentSameDomain {
		return Result{Decision: Blocked, Reasons: []string{"recent_contact_same_domain"}}
	}

	// Mirrors draft.ValidateTemplate — kept as an inline check rather than importing the draft
	// package, which would create draft -> sendpolicy -> draft. executeSend re-checks this
	// again at send time regardless; this is defense in depth, not the only check.
	if strings.Contains(in.Draft.Body, "[") || strings.Contains(in.Draft.Body, "{{") {
		return Result{Decision: Blocked, Reasons: []string{"placeholder_remaining"}}
	}

	score := in.Company.Score
	if score < cfg.ReviewMinScore {
		return Result{Decision: Blocked, Reasons: []string{fmt.Sprintf("score_below_review_threshold(%d<%d)", score, cfg.ReviewMinScore)}}
	}

	// --- Confidence judgment: any single uncertainty downgrades to REVIEW_REQUIRED. ---

	var reasons []string

	toneWarnings := tonecheck.Warnings(in.Draft.Body)
	if len(toneWarnings) > 0 {
		reasons = append(reasons, "ai_tone_warning:"+strings.Join(toneWarnings, ";"))
	}

	evidenceUsed := ""
	if in.Draft.EvidenceUsed != nil {
		evidenceUsed = *in.Draft.EvidenceUsed
	}
	if cfg.RequireFreshEvidence && !evidenceFresh(evidenceUsed, in.Signals, cfg.MaxEvidenceAge) {
		reasons = append(reasons, "stale_or_unresolved_evidence")
	}

	if score < cfg.AutoSendMinScore {
		reasons = append(reasons, fmt.Sprintf("score_below_auto_send_threshold(%d<%d)", score, cfg.AutoSendMinScore))
	}

	if !in.Draft.EditedByHuman && len(reasons) == 0 {
		return Result{Decision: AutoSend, Reasons: []string{fmt.Sprintf("score_%d_meets_auto_send_threshold", score)}}
	}
	if in.Draft.EditedByHuman {
		// A human already touched this draft — a hand-edited message should always get a
		// human's eyes on the final send too, not slip into auto-send on a technicality.
		reasons = append(reasons, "human_edited")
	}

	return Result{Decision: ReviewRequired, Reasons: reasons}
}

// evidenceFresh reports whether every signal id the draft cited (draft.EvidenceUsed, a
// comma-separated list) still resolves to a real signal on the company and was captured
// within maxAge. An id that no longer resolves (deleted/re-enriched away) counts as not fresh
// — better to ask a human than assume.
func evidenceFresh(evidenceUsed string, signals []*store.Signal, maxAge time.Duration) bool {
	byID := make(map[int64]*store.Signal, len(signals))
	for _, s := range signals {
		byID[s.ID] = s
	}

	ids := strings.Split(evidenceUsed, ",")
	found := false
	for _, idStr := range ids {
		idStr = strings.TrimSpace(idStr)
		if idStr == "" {
			continue
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return false
		}
		sig, ok := byID[id]
		if !ok {
			return false
		}
		if time.Since(sig.CreatedAt) > maxAge {
			return false
		}
		found = true
	}
	return found
}
