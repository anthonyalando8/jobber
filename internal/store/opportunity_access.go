package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/jackc/pgx/v5"
)

// SaveOpportunityAccess stores the access classification of one listing revision.
func (db *DB) SaveOpportunityAccess(ctx context.Context, opportunityID int64, revision int, a opportunity.Access) error {
	_, err := db.Exec(ctx, `INSERT INTO opportunity_access(opportunity_id,opportunity_revision,classifier_version,eligibility,kenya_access,eligibility_evidence,route,route_url,cost,cost_evidence,contract,classified_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
 ON CONFLICT(opportunity_id) DO UPDATE SET opportunity_revision=EXCLUDED.opportunity_revision,classifier_version=EXCLUDED.classifier_version,
 eligibility=EXCLUDED.eligibility,kenya_access=EXCLUDED.kenya_access,eligibility_evidence=EXCLUDED.eligibility_evidence,route=EXCLUDED.route,
 route_url=EXCLUDED.route_url,cost=EXCLUDED.cost,cost_evidence=EXCLUDED.cost_evidence,contract=EXCLUDED.contract,classified_at=now()`,
		opportunityID, revision, opportunity.AccessClassifierVersion, a.Eligibility, a.KenyaAccess, a.EligibilityEvidence, a.Route, a.RouteURL, a.Cost, a.CostEvidence, a.Contract)
	return err
}

// GetOpportunityAccess returns the stored classification, or nil when none exists yet.
func (db *DB) GetOpportunityAccess(ctx context.Context, opportunityID int64) (*opportunity.Access, error) {
	var a opportunity.Access
	err := db.QueryRow(ctx, `SELECT eligibility,kenya_access,eligibility_evidence,route,route_url,cost,cost_evidence,contract FROM opportunity_access WHERE opportunity_id=$1`, opportunityID).
		Scan(&a.Eligibility, &a.KenyaAccess, &a.EligibilityEvidence, &a.Route, &a.RouteURL, &a.Cost, &a.CostEvidence, &a.Contract)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}

// RefreshOpportunityAccess classifies listings that have no classification, or whose
// classification predates the current listing revision or classifier rules. It returns how many
// rows it classified. Bounded per call so a large backlog is worked off over several ticks.
func (db *DB) RefreshOpportunityAccess(ctx context.Context, limit int) (int, error) {
	rows, err := db.Query(ctx, `SELECT o.id,o.current_revision,r.document FROM opportunity o
 JOIN opportunity_revision r ON r.opportunity_id=o.id AND r.revision=o.current_revision
 LEFT JOIN opportunity_access a ON a.opportunity_id=o.id
 WHERE a.opportunity_id IS NULL OR a.opportunity_revision<>o.current_revision OR a.classifier_version<>$1
 ORDER BY o.id DESC LIMIT $2`, opportunity.AccessClassifierVersion, limit)
	if err != nil {
		return 0, err
	}
	type pending struct {
		id       int64
		revision int
		facts    opportunity.DiscoveredOpportunity
	}
	work := []pending{}
	for rows.Next() {
		var p pending
		var raw []byte
		if err = rows.Scan(&p.id, &p.revision, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		if err = json.Unmarshal(raw, &p.facts); err != nil {
			rows.Close()
			return 0, fmt.Errorf("opportunity %d revision document: %w", p.id, err)
		}
		work = append(work, p)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	for _, p := range work {
		if err = db.SaveOpportunityAccess(ctx, p.id, p.revision, opportunity.ClassifyAccess(p.facts)); err != nil {
			return 0, err
		}
	}
	return len(work), nil
}

// TrackingStates are the progress states an operator can record, in pipeline order.
var TrackingStates = []string{"saved", "applied", "interviewing", "offer", "rejected", "not_interested"}

// ValidTrackingState reports whether s is a recordable state.
func ValidTrackingState(s string) bool {
	for _, state := range TrackingStates {
		if s == state {
			return true
		}
	}
	return false
}

// OpportunityTracking is the operator's recorded progress on one listing.
type OpportunityTracking struct {
	State     string
	Note      string
	AppliedAt *time.Time
	UpdatedAt time.Time
}

// SetOpportunityTracking records progress. The first move to "applied" (or beyond) stamps
// applied_at, which is kept if the state later changes.
func (db *DB) SetOpportunityTracking(ctx context.Context, opportunityID int64, state, note string) error {
	if !ValidTrackingState(state) {
		return fmt.Errorf("unknown tracking state %q", state)
	}
	note = strings.TrimSpace(note)
	if len(note) > 4000 {
		note = note[:4000]
	}
	applied := state == "applied" || state == "interviewing" || state == "offer"
	_, err := db.Exec(ctx, `INSERT INTO opportunity_tracking(opportunity_id,state,note,applied_at,updated_at)
 VALUES($1,$2,$3,CASE WHEN $4 THEN now() END,now())
 ON CONFLICT(opportunity_id) DO UPDATE SET state=EXCLUDED.state,
 note=CASE WHEN EXCLUDED.note='' THEN opportunity_tracking.note ELSE EXCLUDED.note END,
 applied_at=COALESCE(opportunity_tracking.applied_at,EXCLUDED.applied_at),updated_at=now()`, opportunityID, state, note, applied)
	return err
}

// ClearOpportunityTracking forgets the recorded progress on a listing.
func (db *DB) ClearOpportunityTracking(ctx context.Context, opportunityID int64) error {
	_, err := db.Exec(ctx, `DELETE FROM opportunity_tracking WHERE opportunity_id=$1`, opportunityID)
	return err
}

// GetOpportunityTracking returns recorded progress, or nil when none exists.
func (db *DB) GetOpportunityTracking(ctx context.Context, opportunityID int64) (*OpportunityTracking, error) {
	var t OpportunityTracking
	err := db.QueryRow(ctx, `SELECT state,note,applied_at,updated_at FROM opportunity_tracking WHERE opportunity_id=$1`, opportunityID).Scan(&t.State, &t.Note, &t.AppliedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &t, err
}

// ATSLink is a link into an employer's applicant tracking board, with the employer it was
// published for.
type ATSLink struct{ URL, Company string }

// ATSLinks returns every distinct link into a Greenhouse, Lever or Ashby board that a stored
// listing or a discovered contact candidate has published.
func (db *DB) ATSLinks(ctx context.Context) ([]ATSLink, error) {
	rows, err := db.Query(ctx, `SELECT DISTINCT ON (link) link, company FROM (
   SELECT o.metadata->>'apply_url' AS link, c.name AS company FROM opportunity o JOIN company c ON c.id=o.company_id
   UNION ALL SELECT o.url, c.name FROM opportunity o JOIN company c ON c.id=o.company_id
   UNION ALL SELECT cc.application_url, c.name FROM contact_candidate cc JOIN company c ON c.id=cc.company_id
 ) links
 WHERE link ~* '^https?://((boards|job-boards)(\.eu)?\.greenhouse\.io|jobs\.(eu\.)?lever\.co|jobs\.ashbyhq\.com)/'
 ORDER BY link LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ATSLink{}
	for rows.Next() {
		var l ATSLink
		if err = rows.Scan(&l.URL, &l.Company); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListUnassessedOpportunityIDs returns live listings with no fit assessment against the active
// profile and current matcher, newest first. Discovery uses it so a new listing is assessed when it
// arrives instead of waiting for someone to press "rerun".
func (db *DB) ListUnassessedOpportunityIDs(ctx context.Context, limit int) ([]int64, error) {
	rows, err := db.Query(ctx, `SELECT o.id`+opportunityBrowseFrom+` WHERE m.id IS NULL AND NOT `+liveSuppression+`
 AND (SELECT active_revision_id FROM candidate_profile WHERE id=1) IS NOT NULL
 ORDER BY EXISTS(SELECT 1 FROM contact_plan cp WHERE cp.opportunity_id=o.id AND cp.status IN ('suggested','selected')) DESC,
   o.discovered_at DESC, o.id DESC LIMIT $2`, opportunity.MatcherVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SourceCount summarises the stored listings from one opportunity source.
type SourceCount struct {
	Source                      string
	Total, LastWeek, Accessible int
	Latest                      *time.Time
}

// SourceCounts reports, per opportunity source, how many live listings are stored, how many were
// discovered in the last seven days and how many are open or likely open to an applicant in Kenya.
// This is what answers "is this source actually producing anything" from the UI.
func (db *DB) SourceCounts(ctx context.Context) ([]SourceCount, error) {
	rows, err := db.Query(ctx, `SELECT o.source,count(*),count(*) FILTER(WHERE o.discovered_at > now()-interval '7 days'),
 count(*) FILTER(WHERE a.kenya_access IN ('open','likely')),max(o.discovered_at)
 FROM opportunity o LEFT JOIN opportunity_access a ON a.opportunity_id=o.id
 WHERE NOT EXISTS(SELECT 1 FROM opportunity_suppression s WHERE s.opportunity_id=o.id AND s.superseded_at IS NULL)
 GROUP BY o.source ORDER BY count(*) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceCount{}
	for rows.Next() {
		var c SourceCount
		if err = rows.Scan(&c.Source, &c.Total, &c.LastWeek, &c.Accessible, &c.Latest); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// IngestionReviewCount is how many distinct listings a source could not store and why, from the
// ingestion_review diagnostics table — listings with no employer named, for example.
type IngestionReviewCount struct {
	Source, Reason string
	Count          int
}

// IngestionReviewCounts summarises held-back listings per source and reason.
func (db *DB) IngestionReviewCounts(ctx context.Context) ([]IngestionReviewCount, error) {
	// Only listings the source published again in the last week and nobody has resolved: a listing
	// held back before a fix, and never seen since, no longer describes a live problem.
	rows, err := db.Query(ctx, `SELECT source,reason,count(*) FROM ingestion_review
 WHERE resolved_at IS NULL AND last_seen_at > now()-interval '7 days'
 GROUP BY source,reason ORDER BY count(*) DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IngestionReviewCount{}
	for rows.Next() {
		var c IngestionReviewCount
		if err = rows.Scan(&c.Source, &c.Reason, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListCompaniesNeedingRelevance returns companies whose relevance assessment is missing, belongs to
// another profile revision or assessor version, or predates their latest research. These are the
// companies whose "is there a reason to contact them" answer is out of date.
func (db *DB) ListCompaniesNeedingRelevance(ctx context.Context, assessorVersion string, limit int) ([]int64, error) {
	rows, err := db.Query(ctx, `SELECT c.id FROM company c LEFT JOIN company_relevance r ON r.company_id=c.id
 WHERE (SELECT active_revision_id FROM candidate_profile WHERE id=1) IS NOT NULL
   AND (c.last_researched_at IS NOT NULL OR EXISTS(SELECT 1 FROM opportunity o WHERE o.company_id=c.id))
   AND (r.company_id IS NULL OR r.assessor_version<>$1
        OR r.profile_revision_id<>(SELECT active_revision_id FROM candidate_profile WHERE id=1)
        OR (c.last_researched_at IS NOT NULL AND c.last_researched_at > r.assessed_at))
 ORDER BY c.last_researched_at DESC NULLS LAST, c.id DESC LIMIT $2`, assessorVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
