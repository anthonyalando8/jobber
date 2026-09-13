package store

import (
	"context"
	"encoding/json"
	"errors"
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
func (db *DB) AutomationCandidates(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Query(ctx, `
		SELECT c.draft_id FROM draft_composition c
		 WHERE c.status = 'reviewed'
		   AND NOT EXISTS(SELECT 1 FROM send_approval a WHERE a.draft_id = c.draft_id AND a.revoked_at IS NULL)
		   AND NOT EXISTS(SELECT 1 FROM send_attempt s WHERE s.draft_id = c.draft_id
		                   AND s.state IN ('reserved','sending','accepted','uncertain','rejected'))
		   AND NOT EXISTS(SELECT 1 FROM automation_claim k WHERE k.draft_id = c.draft_id AND k.expires_at > now())
		 ORDER BY c.updated_at ASC
		 LIMIT $1`, limit)
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
