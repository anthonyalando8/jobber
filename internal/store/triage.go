package store

import (
	"context"
	"encoding/json"

	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/opportunity"
)

// TriageCandidate is one unrelated opportunity triage has not yet decided under its current
// inputs.
type TriageCandidate struct {
	OpportunityID     int64
	Revision          int
	Title             string
	Description       string
	DescriptionStatus string
	Source            string
	ProfileRevisionID int64
	Match             opportunity.MatchResult
}

// triageCandidateWhere selects current, unsuppressed, unrelated assessments that have no triage
// row for exactly these inputs. $1 matcher version, $2 triage version, $3 whether rows recorded as
// "would suppress" while suppression was off should be revisited.
const triageCandidateWhere = `
 FROM opportunity o
 JOIN opportunity_match m ON m.opportunity_id=o.id AND m.opportunity_revision=o.current_revision AND m.matcher_version=$1
  AND m.profile_revision_id=(SELECT active_revision_id FROM candidate_profile WHERE id=1)
 LEFT JOIN opportunity_triage t ON t.opportunity_id=o.id
 WHERE m.classification='unrelated' AND NOT ` + liveSuppression + `
   AND (t.opportunity_id IS NULL
     OR t.opportunity_revision<>o.current_revision OR t.matcher_version<>$1
     OR t.profile_revision_id<>m.profile_revision_id OR t.triage_version<>$2
     OR (t.outcome='failed' AND t.attempts<3) -- a failing step is retried at most three times per input set
     OR ($3 AND t.outcome='confirmed_unrelated' AND t.confidence='high'))`

// TriageCandidates returns up to limit opportunities awaiting triage, newest first.
func (db *DB) TriageCandidates(ctx context.Context, triageVersion string, revisitPending bool, limit int) ([]TriageCandidate, error) {
	rows, err := db.Query(ctx, `SELECT o.id,o.current_revision,o.title,o.description,o.description_status,o.source,m.profile_revision_id,m.result`+
		triageCandidateWhere+` ORDER BY o.discovered_at DESC, o.id DESC LIMIT $4`,
		opportunity.MatcherVersion, triageVersion, revisitPending, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TriageCandidate{}
	for rows.Next() {
		var c TriageCandidate
		var raw []byte
		if err = rows.Scan(&c.OpportunityID, &c.Revision, &c.Title, &c.Description, &c.DescriptionStatus, &c.Source, &c.ProfileRevisionID, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &c.Match); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountTriageCandidates is how many opportunities are waiting for triage.
func (db *DB) CountTriageCandidates(ctx context.Context, triageVersion string, revisitPending bool) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*)`+triageCandidateWhere, opportunity.MatcherVersion, triageVersion, revisitPending).Scan(&n)
	return n, err
}

// TriageRecord is one triage conclusion for the exact inputs in c.
type TriageRecord struct {
	Outcome, Confidence, DecidedBy, Error string
	Reasons                               explain.Reasons
}

// RecordTriage stores the conclusion for these inputs, replacing any older conclusion for the
// opportunity. A failure for the same inputs counts up attempts, so a step that keeps failing is
// retried a bounded number of times and then left for a person.
func (db *DB) RecordTriage(ctx context.Context, c TriageCandidate, triageVersion string, r TriageRecord) error {
	if r.Reasons == nil {
		r.Reasons = explain.Reasons{}
	}
	raw, err := json.Marshal(r.Reasons)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO opportunity_triage(opportunity_id,opportunity_revision,matcher_version,profile_revision_id,
		  triage_version,outcome,confidence,reasons,attempts,last_error,decided_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,1,$9,$10)
		ON CONFLICT (opportunity_id) DO UPDATE SET opportunity_revision=EXCLUDED.opportunity_revision,
		  matcher_version=EXCLUDED.matcher_version, profile_revision_id=EXCLUDED.profile_revision_id,
		  triage_version=EXCLUDED.triage_version, outcome=EXCLUDED.outcome, confidence=EXCLUDED.confidence,
		  reasons=EXCLUDED.reasons, last_error=EXCLUDED.last_error, decided_by=EXCLUDED.decided_by, triaged_at=now(),
		  attempts=CASE WHEN EXCLUDED.outcome='failed' AND opportunity_triage.outcome='failed'
		    AND opportunity_triage.opportunity_revision=EXCLUDED.opportunity_revision
		    AND opportunity_triage.triage_version=EXCLUDED.triage_version
		    THEN opportunity_triage.attempts+1 ELSE 1 END`,
		c.OpportunityID, c.Revision, opportunity.MatcherVersion, c.ProfileRevisionID, triageVersion,
		r.Outcome, r.Confidence, raw, r.Error, r.DecidedBy)
	return err
}

// TriageSuppressionReversed reports whether a person restored a triage suppression of this
// revision. Triage never suppresses the same revision again after that.
func (db *DB) TriageSuppressionReversed(ctx context.Context, opportunityID int64, revision int) (bool, error) {
	var reversed bool
	err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM opportunity_suppression WHERE opportunity_id=$1
		AND opportunity_revision=$2 AND basis=$3 AND superseded_at IS NOT NULL)`, opportunityID, revision, BasisTriage).Scan(&reversed)
	return reversed, err
}

// MarkTriageKept records a person's decision to keep a triaged opportunity visible.
func (db *DB) MarkTriageKept(ctx context.Context, opportunityID int64, by string) error {
	_, err := db.Exec(ctx, `UPDATE opportunity_triage SET outcome='kept', decided_by=$2, triaged_at=now() WHERE opportunity_id=$1`, opportunityID, by)
	return err
}

// TriageCounts is what the dashboard and Sources page show about triage.
type TriageCounts struct {
	Awaiting, Suppressed, Confirmed, ManualReview, Kept, Failed int
	// PendingSuppression are high-confidence unrelated postings recorded while suppression is
	// off: what triage would hide once suppression is turned on.
	PendingSuppression int
	// Recovered are triaged postings whose current assessment is no longer unrelated, after a
	// matcher or profile change.
	Recovered int
}

// TriageStats reads the counts.
func (db *DB) TriageStats(ctx context.Context, triageVersion string, revisitPending bool) (TriageCounts, error) {
	var c TriageCounts
	var err error
	if c.Awaiting, err = db.CountTriageCandidates(ctx, triageVersion, revisitPending); err != nil {
		return c, err
	}
	err = db.QueryRow(ctx, `SELECT
	  count(*) FILTER (WHERE t.outcome='suppressed' AND `+liveSuppressionFor("t.opportunity_id")+`),
	  count(*) FILTER (WHERE t.outcome='confirmed_unrelated' AND t.confidence<>'high'),
	  count(*) FILTER (WHERE t.outcome='manual_review'),
	  count(*) FILTER (WHERE t.outcome='kept'),
	  count(*) FILTER (WHERE t.outcome='failed'),
	  count(*) FILTER (WHERE t.outcome='confirmed_unrelated' AND t.confidence='high'),
	  count(*) FILTER (WHERE EXISTS(SELECT 1 FROM opportunity o JOIN opportunity_match m ON m.opportunity_id=o.id
	      AND m.opportunity_revision=o.current_revision AND m.matcher_version=$1
	      AND m.profile_revision_id=(SELECT active_revision_id FROM candidate_profile WHERE id=1)
	    WHERE o.id=t.opportunity_id AND m.classification<>'unrelated'))
	 FROM opportunity_triage t`, opportunity.MatcherVersion).Scan(&c.Suppressed, &c.Confirmed, &c.ManualReview, &c.Kept, &c.Failed, &c.PendingSuppression, &c.Recovered)
	return c, err
}

func liveSuppressionFor(column string) string {
	return `EXISTS(SELECT 1 FROM opportunity_suppression s WHERE s.opportunity_id=` + column + ` AND s.superseded_at IS NULL)`
}
