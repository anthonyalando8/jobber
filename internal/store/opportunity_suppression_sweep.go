package store

import (
	"context"
	"errors"
	"time"

	"github.com/anthonyalando8/prospector/internal/opportunity"
)

// SuppressionSweepActor is recorded as the author of an automatic suppression. It is deliberately
// not a person's name: a decision nobody made should not look like one somebody made.
const SuppressionSweepActor = "opportunity-suppression-sweep"

// SuppressionPolicy tunes the automatic sweep. The defaults are conservative on purpose — a
// wrongly suppressed opportunity is a missed application, and the cost of leaving a dead posting
// in the list for another week is only noise.
type SuppressionPolicy struct {
	// AbsenceGrace is how long a posting must have been missing from a feed that is still being
	// read successfully before absence is treated as "the posting was taken down".
	AbsenceGrace time.Duration
	// SourceFreshness is how recently a source must have produced a sighting for its silence
	// about one posting to mean anything. Without this, a source that broke or was removed from
	// the seed registry would suppress every opportunity it ever found.
	SourceFreshness time.Duration
	// Batch caps one sweep, so a tick stays short and a first run over a large backlog spreads
	// across several ticks rather than holding a transaction open for all of them.
	Batch int
	// EnumerableSources are the sources whose absence rule may be applied at all: those whose
	// single read returns their complete current listing. Empty means the declared default in
	// internal/opportunity. A source that is read as a keyword search or a truncated feed must
	// never appear here — see opportunity.EnumerableSources for why.
	EnumerableSources []string
}

// DefaultSuppressionPolicy returns the conservative defaults.
func DefaultSuppressionPolicy() SuppressionPolicy {
	return SuppressionPolicy{AbsenceGrace: 14 * 24 * time.Hour, SourceFreshness: 48 * time.Hour,
		Batch: 200, EnumerableSources: opportunity.EnumerableSources}
}

func (p SuppressionPolicy) withDefaults() SuppressionPolicy {
	defaults := DefaultSuppressionPolicy()
	if p.AbsenceGrace <= 0 {
		p.AbsenceGrace = defaults.AbsenceGrace
	}
	if p.SourceFreshness <= 0 {
		p.SourceFreshness = defaults.SourceFreshness
	}
	if p.Batch <= 0 || p.Batch > 2000 {
		p.Batch = defaults.Batch
	}
	if len(p.EnumerableSources) == 0 {
		p.EnumerableSources = opportunity.EnumerableSources
	}
	return p
}

// SuppressionCandidate is one opportunity the sweep concluded is no longer live, with the facts
// it reached that conclusion from.
type SuppressionCandidate struct {
	OpportunityID      int64
	Title, CompanyName string
	Reason, Basis      string
	Evidence           map[string]any
	Source             string
	ExpiresAt          *time.Time
	LastSeenAt         time.Time
	SourceLatestSeenAt time.Time
	AvailabilityStatus string
}

// SuppressionSweepResult reports what one sweep did.
type SuppressionSweepResult struct {
	Suppressed []SuppressionCandidate
	// Skipped counts candidates that could not be suppressed — almost always because a decision
	// was written concurrently. They are not failures and the next sweep sees them again.
	Skipped int
	Errors  []error
}

// Total is how many opportunities this sweep took out of the working queues.
func (r SuppressionSweepResult) Total() int { return len(r.Suppressed) }

// ByBasis counts the suppressions this sweep made for each basis, for a one-line log that says
// what kind of staleness was found.
func (r SuppressionSweepResult) ByBasis() map[string]int {
	counts := map[string]int{}
	for _, candidate := range r.Suppressed {
		counts[candidate.Basis]++
	}
	return counts
}

// suppressionCandidateQuery finds postings that are no longer live, and says on what basis.
//
// The three bases are ordered by how directly the source states the fact. A feed that says
// "closed" has stated it; a published expiry date that has passed is the source's own deadline;
// absence from a feed is an inference, and the weakest of the three, which is why it carries
// three separate guards: the source must be one that enumerates its whole listing, it must have
// been read recently, and the posting must have been missing for the whole grace period.
//
// The enumeration guard is not a nicety. Most sources here are read as keyword searches or
// truncated feeds, where a posting disappears because newer ones pushed it out rather than
// because it ended — see opportunity.EnumerableSources.
//
// Two exclusions matter as much as the predicates:
//
//   - A posting already suppressed, or already out of the queues as 'ignored', is left alone. The
//     sweep adds decisions, it never revises one.
//   - A posting whose suppression on this same basis was lifted by a person is never suppressed
//     on that basis again. Otherwise reinstating an automatically suppressed opportunity would
//     last exactly until the next tick, and the operator could not win.
//
// A later revision is new information from the source, so it does not fall under that second
// exclusion: the sweep compares the lifted decision's revision with the current one.
const suppressionCandidateQuery = `
WITH source_health AS (
  SELECT source, max(last_seen_at) AS latest FROM opportunity GROUP BY source
)
SELECT o.id, o.title, c.name, o.source, o.expires_at, o.last_seen_at, h.latest, o.availability_status,
       CASE
         WHEN o.availability_status = 'closed' THEN 'source_closed'
         WHEN o.expires_at IS NOT NULL AND o.expires_at < now() THEN 'expiry_date_passed'
         ELSE 'absent_from_source'
       END AS basis
  FROM opportunity o
  JOIN company c ON c.id = o.company_id
  JOIN source_health h ON h.source = o.source
 WHERE o.selection_status <> 'ignored'
   AND NOT EXISTS (SELECT 1 FROM opportunity_suppression s
                    WHERE s.opportunity_id = o.id AND s.superseded_at IS NULL)
   AND (
     o.availability_status = 'closed'
     OR (o.expires_at IS NOT NULL AND o.expires_at < now())
     OR (o.source = ANY($4::text[])
         AND h.latest > now() - make_interval(secs => $1)
         AND o.last_seen_at < h.latest - make_interval(secs => $2))
   )
   AND NOT EXISTS (
     SELECT 1 FROM opportunity_suppression s
      WHERE s.opportunity_id = o.id
        AND s.superseded_at IS NOT NULL
        AND s.superseded_by <> ''
        AND s.opportunity_revision >= o.current_revision
        AND s.basis = CASE
              WHEN o.availability_status = 'closed' THEN 'source_closed'
              WHEN o.expires_at IS NOT NULL AND o.expires_at < now() THEN 'expiry_date_passed'
              ELSE 'absent_from_source'
            END
   )
 ORDER BY o.last_seen_at, o.id
 LIMIT $3`

// reasonForBasis is the conclusion each basis supports. They are not interchangeable: a feed
// saying closed is the employer withdrawing the posting, a passed expiry date is expiry, and a
// posting that quietly stopped appearing is the one we can only describe as no longer advertised.
func reasonForBasis(basis string) string {
	switch basis {
	case BasisSourceClosed:
		return SuppressWithdrawnBySource
	case BasisExpiryPassed:
		return SuppressExpired
	default:
		return SuppressNoLongerHiring
	}
}

// OpportunitySuppressionCandidates reports what the sweep would suppress, without suppressing
// anything. The worker uses the sweep; this exists so the decision can be inspected first.
func (db *DB) OpportunitySuppressionCandidates(ctx context.Context, policy SuppressionPolicy) ([]SuppressionCandidate, error) {
	policy = policy.withDefaults()
	// Durations are passed as seconds into make_interval rather than as Go duration strings for
	// Postgres to parse: "336h0m0s" happens to be accepted, but a filter whose grace period was
	// silently misread would suppress live postings, and that is not a thing to leave to luck.
	rows, err := db.Query(ctx, suppressionCandidateQuery,
		policy.SourceFreshness.Seconds(), policy.AbsenceGrace.Seconds(), policy.Batch,
		policy.EnumerableSources)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SuppressionCandidate{}
	for rows.Next() {
		var c SuppressionCandidate
		if err = rows.Scan(&c.OpportunityID, &c.Title, &c.CompanyName, &c.Source, &c.ExpiresAt,
			&c.LastSeenAt, &c.SourceLatestSeenAt, &c.AvailabilityStatus, &c.Basis); err != nil {
			return nil, err
		}
		c.Reason = reasonForBasis(c.Basis)
		c.Evidence = map[string]any{
			"source":                c.Source,
			"availability_status":   c.AvailabilityStatus,
			"last_seen_at":          c.LastSeenAt,
			"source_latest_seen_at": c.SourceLatestSeenAt,
			"absence_grace":         policy.AbsenceGrace.String(),
			"source_freshness":      policy.SourceFreshness.String(),
			"observed_at":           time.Now().UTC(),
		}
		if c.ExpiresAt != nil {
			c.Evidence["expires_at"] = *c.ExpiresAt
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SweepOpportunitySuppressions suppresses every posting that is demonstrably no longer live.
//
// Each decision is written in its own transaction, so one failure does not discard the rest and
// nothing holds a long transaction over a whole backlog. A candidate that was suppressed by
// someone else in the meantime is counted as skipped, not failed.
func (db *DB) SweepOpportunitySuppressions(ctx context.Context, policy SuppressionPolicy) (SuppressionSweepResult, error) {
	var result SuppressionSweepResult
	candidates, err := db.OpportunitySuppressionCandidates(ctx, policy)
	if err != nil {
		return result, err
	}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		_, err := db.SuppressOpportunity(ctx, candidate.OpportunityID, candidate.Reason,
			candidate.Basis, "", SuppressionSweepActor, candidate.Evidence)
		switch {
		case errors.Is(err, ErrAlreadySuppressed):
			result.Skipped++
		case err != nil:
			result.Errors = append(result.Errors, err)
		default:
			result.Suppressed = append(result.Suppressed, candidate)
		}
	}
	return result, nil
}
