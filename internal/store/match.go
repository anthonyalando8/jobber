package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/relevance"
	"github.com/jackc/pgx/v5"
)

// SaveOpportunityMatch persists a deterministic assessment against exact revisions. A
// profile_required result is never stored: without an active approved profile there is no
// authoritative fit to record.
func (db *DB) SaveOpportunityMatch(ctx context.Context, m opportunity.MatchResult) error {
	if m.Classification == opportunity.ClassificationProfileRequired {
		return fmt.Errorf("refusing to store a match without an active approved profile")
	}
	if m.ProfileRevisionID == 0 {
		return fmt.Errorf("match must cite the approved profile revision it used")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx,
		`INSERT INTO opportunity_match (opportunity_id, opportunity_revision, profile_revision_id,
		        matcher_version, classification, score, family_confidence, result)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (opportunity_id, opportunity_revision, profile_revision_id, matcher_version)
		 DO UPDATE SET classification = EXCLUDED.classification, score = EXCLUDED.score,
		               family_confidence = EXCLUDED.family_confidence, result = EXCLUDED.result,
		               assessed_at = now()`,
		m.OpportunityID, m.OpportunityRevision, m.ProfileRevisionID, m.MatcherVersion,
		m.Classification, m.Score, m.FamilyConfidence, raw)
	return err
}

// GetCurrentOpportunityMatch returns the assessment matching the opportunity's current revision,
// the active approved profile revision and this binary's matcher version. Anything else is
// stale by definition and is never returned as current.
func (db *DB) GetCurrentOpportunityMatch(ctx context.Context, opportunityID int64) (*opportunity.MatchResult, error) {
	var raw []byte
	err := db.QueryRow(ctx,
		`SELECT m.result FROM opportunity_match m
		 JOIN opportunity o ON o.id = m.opportunity_id AND o.current_revision = m.opportunity_revision
		 JOIN candidate_profile p ON p.active_revision_id = m.profile_revision_id
		 WHERE m.opportunity_id = $1 AND m.matcher_version = $2`, opportunityID, opportunity.MatcherVersion).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m opportunity.MatchResult
	if err = json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// StoredMatch is an assessment as stored, with the staleness of its pinned inputs resolved
// against what is current now.
type StoredMatch struct {
	Result    opportunity.MatchResult
	Stale     bool
	StaleWhy  string
	Assessed  string
	Available bool
}

// LatestMatchesForCompany returns the newest stored assessment per opportunity for a company,
// each flagged with whether it still describes current inputs.
func (db *DB) LatestMatchesForCompany(ctx context.Context, companyID int64) (map[int64]StoredMatch, error) {
	rows, err := db.Query(ctx,
		`SELECT DISTINCT ON (m.opportunity_id)
		        m.opportunity_id, m.result, m.opportunity_revision, m.profile_revision_id,
		        m.matcher_version, o.current_revision, COALESCE(p.active_revision_id, 0),
		        to_char(m.assessed_at, 'YYYY-MM-DD HH24:MI')
		 FROM opportunity_match m
		 JOIN opportunity o ON o.id = m.opportunity_id
		 CROSS JOIN candidate_profile p
		 WHERE o.company_id = $1 AND p.id = 1
		 ORDER BY m.opportunity_id, m.assessed_at DESC`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]StoredMatch{}
	for rows.Next() {
		var id int64
		var raw []byte
		var storedRevision, currentRevision int
		var storedProfile, activeProfile int64
		var version, assessed string
		if err = rows.Scan(&id, &raw, &storedRevision, &storedProfile, &version,
			&currentRevision, &activeProfile, &assessed); err != nil {
			return nil, err
		}
		var m opportunity.MatchResult
		if err = json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		stored := StoredMatch{Result: m, Assessed: assessed, Available: true}
		switch {
		case storedRevision != currentRevision:
			stored.Stale, stored.StaleWhy = true, fmt.Sprintf(
				"assessed against source revision %d; the opportunity is now at revision %d", storedRevision, currentRevision)
		case activeProfile == 0:
			stored.Stale, stored.StaleWhy = true, "no candidate profile revision is currently active"
		case storedProfile != activeProfile:
			stored.Stale, stored.StaleWhy = true, fmt.Sprintf(
				"assessed against profile revision %d; revision %d is now active", storedProfile, activeProfile)
		case version != opportunity.MatcherVersion:
			stored.Stale, stored.StaleWhy = true, fmt.Sprintf(
				"assessed by matcher %s; this binary uses %s", version, opportunity.MatcherVersion)
		}
		out[id] = stored
	}
	return out, rows.Err()
}

// LatestMatchForOpportunity returns the newest stored assessment with its staleness resolved.
func (db *DB) LatestMatchForOpportunity(ctx context.Context, opportunityID int64) (*StoredMatch, error) {
	var companyID int64
	if err := db.QueryRow(ctx, `SELECT company_id FROM opportunity WHERE id = $1`, opportunityID).Scan(&companyID); err != nil {
		return nil, err
	}
	all, err := db.LatestMatchesForCompany(ctx, companyID)
	if err != nil {
		return nil, err
	}
	if m, ok := all[opportunityID]; ok {
		return &m, nil
	}
	return nil, nil
}

// MatchClassificationCounts reports the stored classification breakdown, counting the newest
// assessment per opportunity.
func (db *DB) MatchClassificationCounts(ctx context.Context) (map[string]int, error) {
	rows, err := db.Query(ctx,
		`SELECT classification, count(*) FROM (
		   SELECT DISTINCT ON (opportunity_id) opportunity_id, classification
		   FROM opportunity_match ORDER BY opportunity_id, assessed_at DESC
		 ) latest GROUP BY classification ORDER BY classification`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var classification string
		var n int
		if err = rows.Scan(&classification, &n); err != nil {
			return nil, err
		}
		counts[classification] = n
	}
	return counts, rows.Err()
}

// SaveCompanyRelevance stores the capped, explainable company-relevance assessment.
func (db *DB) SaveCompanyRelevance(ctx context.Context, r relevance.Result) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var profileRevision *int64
	if r.ProfileRevisionID != 0 {
		profileRevision = &r.ProfileRevisionID
	}
	_, err = db.Exec(ctx,
		`INSERT INTO company_relevance (company_id, profile_revision_id, assessor_version, score,
		        relevant, payment_need_evidenced, result)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (company_id) DO UPDATE
		   SET profile_revision_id = EXCLUDED.profile_revision_id,
		       assessor_version = EXCLUDED.assessor_version, score = EXCLUDED.score,
		       relevant = EXCLUDED.relevant, payment_need_evidenced = EXCLUDED.payment_need_evidenced,
		       result = EXCLUDED.result, assessed_at = now()`,
		r.CompanyID, profileRevision, r.AssessorVersion, r.Score, r.Relevant, r.PaymentNeedEvidenced, raw)
	return err
}

// GetCompanyRelevance returns the stored assessment, or nil when none has been computed.
func (db *DB) GetCompanyRelevance(ctx context.Context, companyID int64) (*relevance.Result, error) {
	var raw []byte
	err := db.QueryRow(ctx, `SELECT result FROM company_relevance WHERE company_id = $1`, companyID).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r relevance.Result
	if err = json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
