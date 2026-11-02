package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"time"

	"github.com/anthonyalando8/prospector/internal/automation"
	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/jackc/pgx/v5"
)

// ErrNotClaimed means another worker holds the automation claim for this draft.
var ErrNotClaimed = errors.New("automation candidate is claimed by another worker")

// AutomationDecision is one persisted answer about one exact composition revision.
type AutomationDecision struct {
	ID                  int64
	DraftID             int64
	CompositionRevision int
	ContentHash         string
	Outcome             string
	PolicyVersion       string
	Intent              string
	ProfileRevisionID   *int64
	OpportunityID       *int64
	OpportunityRevision *int
	PlanID              *int64
	PlanRevision        *int
	RecipientContactID  *int64
	RecipientEmail      string
	SemanticStatus      string
	SemanticHash        string
	RolloutMode         string
	Allowlist           []string
	DailyCap            int
	Reasons             explain.Reasons
	Blockers            explain.Reasons
	SendAttemptID       *int64
	EvaluatedAt         time.Time
}

const automationDecisionColumns = `id,draft_id,composition_revision,content_hash,outcome,policy_version,
 intent,profile_revision_id,opportunity_id,opportunity_revision,plan_id,plan_revision,
 recipient_contact_id,recipient_email,semantic_status,semantic_hash,rollout_mode,allowlist,
 daily_cap,reasons,blockers,send_attempt_id,evaluated_at`

// RecordAutomationDecision persists what the policy concluded, eligible or not. Recording the
// refusals is the point as much as recording the approvals: the question "why did this stay in
// review" has to be answerable months later, and only a stored decision can answer it.
func (db *DB) RecordAutomationDecision(ctx context.Context, d AutomationDecision) (*AutomationDecision, error) {
	reasons, err := json.Marshal(nonNilReasons(d.Reasons))
	if err != nil {
		return nil, err
	}
	blockers, err := json.Marshal(nonNilReasons(d.Blockers))
	if err != nil {
		return nil, err
	}
	allowlist := d.Allowlist
	if allowlist == nil {
		allowlist = []string{}
	}
	rawAllowlist, err := json.Marshal(allowlist)
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(ctx,
		`INSERT INTO automation_decision(draft_id,composition_revision,content_hash,outcome,policy_version,
		   intent,profile_revision_id,opportunity_id,opportunity_revision,plan_id,plan_revision,
		   recipient_contact_id,recipient_email,semantic_status,semantic_hash,rollout_mode,allowlist,
		   daily_cap,reasons,blockers)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		 RETURNING `+automationDecisionColumns,
		d.DraftID, d.CompositionRevision, d.ContentHash, d.Outcome, d.PolicyVersion, d.Intent,
		d.ProfileRevisionID, d.OpportunityID, d.OpportunityRevision, d.PlanID, d.PlanRevision,
		d.RecipientContactID, d.RecipientEmail, d.SemanticStatus, d.SemanticHash, d.RolloutMode,
		rawAllowlist, d.DailyCap, reasons, blockers)
	return scanAutomationDecision(row)
}

// LinkAutomationDecisionToAttempt records that this decision led to that transmission. It is the
// one permitted mutation, and it cannot overwrite an existing link.
func (db *DB) LinkAutomationDecisionToAttempt(ctx context.Context, decisionID, attemptID int64) error {
	_, err := db.Exec(ctx,
		`UPDATE automation_decision SET send_attempt_id=$2 WHERE id=$1 AND send_attempt_id IS NULL`,
		decisionID, attemptID)
	return err
}

// AutomationDecisionsForDraft returns every decision ever recorded for a draft, newest first.
func (db *DB) AutomationDecisionsForDraft(ctx context.Context, draftID int64) ([]*AutomationDecision, error) {
	rows, err := db.Query(ctx,
		`SELECT `+automationDecisionColumns+` FROM automation_decision WHERE draft_id=$1 ORDER BY evaluated_at DESC, id DESC`,
		draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectAutomationDecisions(rows)
}

// RecentAutomationDecisions powers the dashboard: what the policy has been concluding lately.
func (db *DB) RecentAutomationDecisions(ctx context.Context, limit int) ([]*AutomationDecision, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(ctx,
		`SELECT `+automationDecisionColumns+` FROM automation_decision ORDER BY evaluated_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectAutomationDecisions(rows)
}

// AutomationBlockerCounts aggregates what most commonly prevents eligibility. This is the number
// that tells an operator whether the obstacle is the data or the policy.
func (db *DB) AutomationBlockerCounts(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := db.Query(ctx,
		`SELECT b->>'code', count(*) FROM automation_decision, jsonb_array_elements(blockers) AS b
		  WHERE evaluated_at >= $1 GROUP BY 1 ORDER BY 2 DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var code string
		var count int
		if err := rows.Scan(&code, &count); err != nil {
			return nil, err
		}
		counts[code] = count
	}
	return counts, rows.Err()
}

// AutoSentToday counts automatic sends since local midnight. It is deliberately derived from
// send attempts linked to an eligible decision rather than from a counter, so a crash cannot
// lose the count and let the cap be exceeded.
func (db *DB) AutoSentToday(ctx context.Context, since time.Time) (int, error) {
	var count int
	err := db.QueryRow(ctx,
		`SELECT count(DISTINCT a.id) FROM send_attempt a
		   JOIN automation_decision d ON d.send_attempt_id = a.id
		  WHERE a.reserved_at >= $1 AND a.state IN ('reserved','sending','accepted','uncertain')`,
		since).Scan(&count)
	return count, err
}

// UnresolvedSendAttempts counts attempts whose outcome the system does not know. Automation must
// halt while any exists: continuing means acting without knowing what has already been done.
func (db *DB) UnresolvedSendAttempts(ctx context.Context) (int, error) {
	var count int
	err := db.QueryRow(ctx,
		`SELECT count(*) FROM send_attempt WHERE state IN ('reserved','sending','uncertain')`).Scan(&count)
	return count, err
}

// UnresolvedAttemptsForDraft narrows that to one draft, which is what blocks that specific
// logical message even when the system as a whole is clear.
func (db *DB) UnresolvedAttemptsForDraft(ctx context.Context, draftID int64) (int, error) {
	var count int
	err := db.QueryRow(ctx,
		`SELECT count(*) FROM send_attempt WHERE draft_id=$1 AND state IN ('reserved','sending','uncertain')`,
		draftID).Scan(&count)
	return count, err
}

// ---------------------------------------------------------------- claims

// ClaimAutomationCandidate takes a durable, expiring claim over one draft. Two workers cannot
// hold the same draft, and an abandoned claim expires rather than blocking the draft for good.
func (db *DB) ClaimAutomationCandidate(ctx context.Context, draftID int64, owner string, lease time.Duration) error {
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	tag, err := db.Exec(ctx,
		`INSERT INTO automation_claim(draft_id,owner,expires_at) VALUES($1,$2,now()+$3::interval)
		 ON CONFLICT (draft_id) DO UPDATE SET owner=EXCLUDED.owner, claimed_at=now(), expires_at=EXCLUDED.expires_at
		 WHERE automation_claim.expires_at < now()`,
		draftID, owner, lease.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotClaimed
	}
	return nil
}

// ReleaseAutomationClaim gives the draft back, but only to the worker that holds it.
func (db *DB) ReleaseAutomationClaim(ctx context.Context, draftID int64, owner string) error {
	_, err := db.Exec(ctx, `DELETE FROM automation_claim WHERE draft_id=$1 AND owner=$2`, draftID, owner)
	return err
}

// AutomationCandidates lists drafts that could plausibly be automated, before the policy runs.
// It is deliberately narrow: reviewed v2 compositions with no live approval and no settled send,
// and nothing else. It never walks company status or any legacy queue.
//
// since is when the operator switched automatic sending on: a revision written before it is
// historical and never a candidate. A revision already decided BLOCKED is not reconsidered (its
// content cannot change without a new revision), and one decided REVIEW_REQUIRED within the last
// hour waits, so an unchanged draft is not re-decided every tick.
func (db *DB) AutomationCandidates(ctx context.Context, since time.Time, limit int) ([]int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Query(ctx, `
		SELECT c.draft_id FROM draft_composition c
		 JOIN draft_composition_revision r ON r.draft_id = c.draft_id AND r.revision = c.current_revision
		 JOIN draft d ON d.id = c.draft_id
		 WHERE c.status = 'reviewed' AND d.state NOT IN ('sent','discarded')
		   AND r.created_at >= $2
		   AND NOT EXISTS(SELECT 1 FROM send_approval a WHERE a.draft_id = c.draft_id AND a.revoked_at IS NULL)
		   AND NOT EXISTS(SELECT 1 FROM send_attempt s WHERE s.draft_id = c.draft_id
		                   AND s.state IN ('reserved','sending','accepted','uncertain','rejected'))
		   AND NOT EXISTS(SELECT 1 FROM automation_claim k WHERE k.draft_id = c.draft_id AND k.expires_at > now())
		   AND NOT EXISTS(SELECT 1 FROM automation_decision x WHERE x.draft_id = c.draft_id
		                   AND x.composition_revision = c.current_revision
		                   AND (x.outcome = 'blocked' OR (x.outcome = 'review_required' AND x.evaluated_at > now() - interval '1 hour')))
		 ORDER BY c.updated_at ASC
		 LIMIT $1`, limit, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---------------------------------------------------------------- scanning

func scanAutomationDecision(row pgx.Row) (*AutomationDecision, error) {
	var d AutomationDecision
	var allowlist, reasons, blockers []byte
	if err := row.Scan(&d.ID, &d.DraftID, &d.CompositionRevision, &d.ContentHash, &d.Outcome,
		&d.PolicyVersion, &d.Intent, &d.ProfileRevisionID, &d.OpportunityID, &d.OpportunityRevision,
		&d.PlanID, &d.PlanRevision, &d.RecipientContactID, &d.RecipientEmail, &d.SemanticStatus,
		&d.SemanticHash, &d.RolloutMode, &allowlist, &d.DailyCap, &reasons, &blockers,
		&d.SendAttemptID, &d.EvaluatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(allowlist, &d.Allowlist); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(reasons, &d.Reasons); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(blockers, &d.Blockers); err != nil {
		return nil, err
	}
	return &d, nil
}

func collectAutomationDecisions(rows pgx.Rows) ([]*AutomationDecision, error) {
	out := []*AutomationDecision{}
	for rows.Next() {
		d, err := scanAutomationDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func nonNilReasons(r explain.Reasons) explain.Reasons {
	if r == nil {
		return explain.Reasons{}
	}
	return r
}

// AutomationDecisionFrom converts a policy decision into its storable form, copying the pinned
// facts the policy saw so the row is readable without re-deriving anything.
func AutomationDecisionFrom(d automation.Decision, draftID int64, revision int, hash string) AutomationDecision {
	return AutomationDecision{
		DraftID: draftID, CompositionRevision: revision, ContentHash: hash,
		Outcome: d.Outcome, PolicyVersion: d.PolicyVersion, Intent: d.Intent,
		Reasons: d.Reasons, Blockers: d.Blockers, EvaluatedAt: d.EvaluatedAt,
	}
}

// AutomationFacts are the extra facts the auto-v2 policy reads about one draft.
type AutomationFacts struct {
	RevisionCreatedAt     time.Time
	OpportunitySuppressed bool
	TransientFailures     int
	LastFailureAt         time.Time
}

// AutomationFactsFor reads them in one round trip.
func (db *DB) AutomationFactsFor(ctx context.Context, draftID int64, revision int, opportunityID int64) (AutomationFacts, error) {
	var f AutomationFacts
	var last *time.Time
	err := db.QueryRow(ctx, `SELECT
	   COALESCE((SELECT created_at FROM draft_composition_revision WHERE draft_id=$1 AND revision=$2), 'epoch'::timestamptz),
	   $3>0 AND EXISTS(SELECT 1 FROM opportunity_suppression WHERE opportunity_id=$3 AND superseded_at IS NULL),
	   (SELECT count(*) FROM send_attempt WHERE draft_id=$1 AND state='transient_failed'),
	   (SELECT max(COALESCE(settled_at, reserved_at)) FROM send_attempt WHERE draft_id=$1 AND state='transient_failed')`,
		draftID, revision, opportunityID).Scan(&f.RevisionCreatedAt, &f.OpportunitySuppressed, &f.TransientFailures, &last)
	if last != nil {
		f.LastFailureAt = *last
	}
	return f, err
}

// OutreachCandidate is an opportunity the outreach worker may draft an application for.
type OutreachCandidate struct {
	OpportunityID, CompanyID int64
	Revision                 int
}

// OutreachCandidates lists strong-match opportunities that have a strong reason to write and
// nothing in the way of it: open, not dismissed or suppressed, at a company whose domain is
// confirmed, with no live contact plan and no conversation ever opened with that company. It does
// not decide anything; the worker still asks the plan rules and the recipient ranking.
func (db *DB) OutreachCandidates(ctx context.Context, limit int) ([]OutreachCandidate, error) {
	rows, err := db.Query(ctx, `SELECT o.id,o.company_id,o.current_revision
		 FROM opportunity o JOIN company c ON c.id=o.company_id
		 JOIN opportunity_match m ON m.opportunity_id=o.id AND m.opportunity_revision=o.current_revision AND m.matcher_version=$1
		  AND m.profile_revision_id=(SELECT active_revision_id FROM candidate_profile WHERE id=1)
		 WHERE m.classification='strong_match' AND o.availability_status<>'closed' AND o.selection_status<>'ignored'
		   AND NOT `+liveSuppression+`
		   AND c.domain_status IN ('verified','manual')
		   -- A live plan blocks the company, except the worker's own plan for this posting whose
		   -- draft never completed (a restart mid-draft): that one is retried, not abandoned.
		   AND NOT EXISTS(SELECT 1 FROM contact_plan p WHERE p.company_id=o.company_id AND p.status IN ('suggested','selected')
		     AND (p.selection_origin<>'suggested' OR p.opportunity_id IS DISTINCT FROM o.id
		       OR EXISTS(SELECT 1 FROM draft_composition dc JOIN draft d ON d.id=dc.draft_id WHERE dc.plan_id=p.id
		         AND d.state<>'discarded' AND NOT (dc.status='failed' OR (dc.status IN ('queued','running') AND COALESCE(dc.lease_until,dc.created_at+interval '10 minutes')<now())))))
		   AND NOT EXISTS(SELECT 1 FROM outbound_thread t WHERE t.company_id=o.company_id)
		 ORDER BY o.discovered_at DESC, o.id DESC LIMIT $2`, opportunity.MatcherVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OutreachCandidate{}
	for rows.Next() {
		var c OutreachCandidate
		if err = rows.Scan(&c.OpportunityID, &c.CompanyID, &c.Revision); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AutoComposedSince counts drafts the outreach worker started (from plans it suggested) since t.
func (db *DB) AutoComposedSince(ctx context.Context, t time.Time) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM draft_composition c JOIN contact_plan p ON p.id=c.plan_id
		 WHERE p.selection_origin='suggested' AND c.created_at >= $1`, t).Scan(&n)
	return n, err
}

// BlockedDraftsSince counts distinct drafts whose latest decision since t was BLOCKED.
func (db *DB) BlockedDraftsSince(ctx context.Context, t time.Time) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM (
	   SELECT DISTINCT ON (draft_id) draft_id, outcome FROM automation_decision
	    WHERE evaluated_at >= $1 ORDER BY draft_id, evaluated_at DESC) latest WHERE outcome='blocked'`, t).Scan(&n)
	return n, err
}
