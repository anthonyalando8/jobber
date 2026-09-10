package score

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/anthonyalando8/prospector/internal/store"
)

// Scorer assigns segments and scores to companies
type Scorer struct{}

// NewScorer creates a scorer
func NewScorer() *Scorer {
	return &Scorer{}
}

// Line is one explainable component of a score — review.md §15: "the UI should never have to
// make the user trust an unexplained number." Points can be negative (a penalty).
type Line struct {
	Label  string `json:"label"`
	Points int    `json:"points"`
}

// Score analyzes a company and assigns segment + score + a stored breakdown.
//
// A company can reach here (called after EnrichCompany returns found=true) with contacts but
// zero signals — that used to make this function return early with no DB write at all, leaving
// the company stuck at score=0/segment=NULL forever with no way to tell "not yet scored" from
// "scored low". Segment classification and the base-by-segment score don't actually need any
// signals, so there's no reason to skip writing those; a company with no signals just ends up
// with only the base line in its breakdown, which is itself useful information (it explains
// exactly why the score is low: nothing was found beyond a contact).
func (s *Scorer) Score(ctx context.Context, company *store.Company, db *store.DB) error {
	signals, err := db.GetSignalsByCompany(ctx, company.ID)
	if err != nil {
		return err
	}

	segment := s.classifySegment(company, signals)
	total, breakdown := s.calculateScore(signals, segment)

	breakdownJSON, err := json.Marshal(breakdown)
	if err != nil {
		return err
	}

	log.Printf("Scored %s: segment=%s, score=%d", company.Name, segment, total)
	return db.UpdateCompanyScore(ctx, company.ID, total, segment, breakdownJSON)
}

// enterpriseKeywords flag a company name as belonging to a large, institutional organization —
// a bank, insurer, university, hospital, or government body has its own HR department and
// procurement process. The "I can cover you on contract while you hire" pitch that works on a
// five-person startup reads as naive there; these companies still get pitched (this is a
// segment, not a filter — see "enterprise" in classifySegment), just with a narrower,
// consultant-style angle instead. Deliberately conservative/short: a false negative just means
// a bank gets the ordinary "hiring" pitch, not a big deal; a false positive on a small business
// whose name happens to contain one of these words is more visible, so kept to strong,
// low-ambiguity institutional words only.
var enterpriseKeywords = []string{
	"bank", "insurance", "sacco", "university", "hospital", "ministry",
	"county government", "authority", "holdings", "corporation", "plc",
	"airlines", "telecom", "microfinance",
}

func isLikelyEnterprise(name string) bool {
	lower := strings.ToLower(name)
	for _, kw := range enterpriseKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// UsJobSources are every seeder pulling US/international remote job postings — a distinct
// segment from the Kenyan "hiring" pitch, since the message can't lean on M-Pesa/local-market
// framing there. See internal/draft/generate.go's servicePitch, its CAN-SPAM mailing-address
// guard, and the "hiring_us" template — all three key off this same source list, not off
// whatever segment ends up assigned, since "application" mode (a human's manual override) can
// put a US-sourced company on a different segment without changing which market it's in.
var UsJobSources = map[string]bool{
	"remoteok": true, "weworkremotely": true, "remotive": true, "himalayas": true,
}

// classifySegment determines the company segment
func (s *Scorer) classifySegment(company *store.Company, signals []*store.Signal) string {
	// "application" is a human's deliberate choice (company.html's segment override, for a
	// company too large for the "let me cover this while you hire" contract pitch to make sense)
	// — never something this rule-based classifier assigns on its own, so once set it must stick
	// regardless of what the hiring-signal rules below would otherwise pick.
	if company.Segment != nil && *company.Segment == "application" {
		return "application"
	}

	// Rule-based segmentation

	isHiringSignal := company.Source == "jobboard" || UsJobSources[company.Source]
	for _, sig := range signals {
		if sig.Kind == "hiring_backend" {
			isHiringSignal = true
			break
		}
	}
	if isHiringSignal {
		if UsJobSources[company.Source] {
			return "hiring_us"
		}
		if isLikelyEnterprise(company.Name) {
			return "enterprise"
		}
		return "hiring"
	}

	if company.Source == "clutch" || company.Source == "google" {
		return "agency"
	}

	// Check signals
	for _, sig := range signals {
		switch sig.Kind {
		case "incubator_mention", "startup_program":
			return "incubator"
		case "retail_business", "sme_indicator":
			return "sme"
		}
	}

	// Default to unknown
	if company.Segment != nil && *company.Segment != "" {
		return *company.Segment
	}

	return "agency" // Safe default for cold outreach
}

// segmentBaseLabel and segmentBasePoints together are the "base score by segment" line —
// kept as a lookup so calculateScore can both compute the number and label it for the
// breakdown in one place, rather than duplicating the segment list twice.
var segmentBase = map[string]int{
	"hiring":     10,
	"hiring_us":  10, // same weight as "hiring" — a different pitch (no M-Pesa framing), not a lower priority
	"enterprise": 10, // same as "hiring" — an enterprise gets a different pitch, not a lower priority
	"agency":     8,
	"incubator":  6,
	"sme":        4,
}

// signalPoints maps a signal kind to its score contribution — the boost/penalty list from
// calculateScore, now also the source of each breakdown line's label.
var signalPoints = map[string]struct {
	Points int
	Label  string
}{
	"hiring_backend":       {10, "actively hiring a backend role"},
	"tech:flutter":         {5, "Flutter app detected"},
	"tech:react":           {5, "React app detected"},
	"checkout_present":     {3, "checkout/payment functionality on site"},
	"paybill_listed":       {7, "M-Pesa paybill/till listed"},
	"payment_complaint":    {7, "payment complaint found"},
	"active_site":          {2, "recently updated site"},
	"stale_site":           {-3, "site looks outdated"},
	"offers_tech_services": {4, "markets web/software development services — a potential subcontract partner"},
}

// calculateScore ranks the company and returns the explainable line items that produced the
// total, capped at 100 — matching the numbers the previous implementation used, just now with
// a label attached to each one instead of a bare total.
func (s *Scorer) calculateScore(signals []*store.Signal, segment string) (int, []Line) {
	base, ok := segmentBase[segment]
	if !ok {
		base = 2 // unrecognized/"unknown" segment — same fallback the old switch's default used
	}

	total := base
	breakdown := []Line{{Label: "segment: " + segment, Points: base}}

	for _, sig := range signals {
		sp, ok := signalPoints[sig.Kind]
		if !ok {
			continue
		}
		total += sp.Points
		breakdown = append(breakdown, Line{Label: sp.Label, Points: sp.Points})
	}

	if total > 100 {
		total = 100
	}

	return total, breakdown
}

// ScoreBatch scores multiple companies
func (s *Scorer) ScoreBatch(ctx context.Context, db *store.DB, companies []*store.Company) error {
	for _, company := range companies {
		if err := s.Score(ctx, company, db); err != nil {
			log.Printf("Scoring failed for %s: %v", company.Name, err)
		}
	}
	return nil
}
