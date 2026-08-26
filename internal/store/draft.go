package store

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Draft represents a generated outreach message
type Draft struct {
	ID            int64
	CompanyID     int64
	ContactID     *int64
	Channel       string
	Subject       *string
	Body          string
	EvidenceUsed  *string
	State         string
	EditedByHuman bool
	// Decision/DecisionReason/DecisionAt/ScoreAtDecision are the AUTO_SEND/REVIEW_REQUIRED/
	// BLOCKED outcome from internal/sendpolicy (review.md's "Critical Workflow Change" §8) —
	// nil/empty for a draft created before that policy engine existed, or one that never got
	// past needs_manual (there's no send decision to make on a draft that was never sendable).
	Decision        *string
	DecisionReason  *string
	DecisionAt      *time.Time
	ScoreAtDecision *int
	CreatedAt       time.Time
}

// CreateDraft inserts a new draft in the given state (e.g. "pending" or "needs_manual" — see
// spec section 7's rejection rules).
func (db *DB) CreateDraft(ctx context.Context, companyID int64, contactID *int64, channel string, subject, body, evidenceUsed, state string) (*Draft, error) {
	var id int64
	var createdAt time.Time

	err := db.QueryRow(ctx,
		`INSERT INTO draft (company_id, contact_id, channel, subject, body, evidence_used, state)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, created_at`,
		companyID, contactID, channel, subject, body, evidenceUsed, state).Scan(&id, &createdAt)

	if err != nil {
		return nil, err
	}

	return &Draft{
		ID:        id,
		CompanyID: companyID,
		ContactID: contactID,
		Channel:   channel,
		Subject:   &subject,
		Body:      body,
		State:     state,
		CreatedAt: createdAt,
	}, nil
}

// GetDraft retrieves a draft by ID
func (db *DB) GetDraft(ctx context.Context, id int64) (*Draft, error) {
	var draft Draft

	err := db.QueryRow(ctx,
		`SELECT id, company_id, contact_id, channel, subject, body, evidence_used, state, edited_by_human,
		        decision, decision_reason, decision_at, score_at_decision, created_at
		 FROM draft WHERE id = $1`, id).Scan(
		&draft.ID, &draft.CompanyID, &draft.ContactID, &draft.Channel,
		&draft.Subject, &draft.Body, &draft.EvidenceUsed, &draft.State,
		&draft.EditedByHuman, &draft.Decision, &draft.DecisionReason, &draft.DecisionAt, &draft.ScoreAtDecision,
		&draft.CreatedAt)

	if err != nil {
		return nil, err
	}

	return &draft, nil
}

// GetLatestDraftForCompany gets the most recent draft for a company
func (db *DB) GetLatestDraftForCompany(ctx context.Context, companyID int64) (*Draft, error) {
	var draft Draft

	err := db.QueryRow(ctx,
		`SELECT id, company_id, contact_id, channel, subject, body, evidence_used, state, edited_by_human,
		        decision, decision_reason, decision_at, score_at_decision, created_at
		 FROM draft WHERE company_id = $1 ORDER BY created_at DESC LIMIT 1`, companyID).Scan(
		&draft.ID, &draft.CompanyID, &draft.ContactID, &draft.Channel,
		&draft.Subject, &draft.Body, &draft.EvidenceUsed, &draft.State,
		&draft.EditedByHuman, &draft.Decision, &draft.DecisionReason, &draft.DecisionAt, &draft.ScoreAtDecision,
		&draft.CreatedAt)

	if err != nil {
		return nil, err
	}

	return &draft, nil
}

// UpdateDraftState updates the state of a draft
func (db *DB) UpdateDraftState(ctx context.Context, id int64, state string) error {
	_, err := db.Exec(ctx,
		`UPDATE draft SET state = $1 WHERE id = $2`, state, id)
	return err
}

// UpdateDraftBody updates the draft body (human edits)
func (db *DB) UpdateDraftBody(ctx context.Context, id int64, body string) error {
	_, err := db.Exec(ctx,
		`UPDATE draft SET body = $1, edited_by_human = true WHERE id = $2`, body, id)
	return err
}

// UpdateDraftDecision records the internal/sendpolicy outcome for a draft — review.md §8: the
// history should always make it obvious why a message was auto-sent, flagged, or refused.
func (db *DB) UpdateDraftDecision(ctx context.Context, id int64, decision, reason string, scoreAtDecision int) error {
	_, err := db.Exec(ctx,
		`UPDATE draft SET decision = $1, decision_reason = $2, decision_at = now(), score_at_decision = $3 WHERE id = $4`,
		decision, reason, scoreAtDecision, id)
	return err
}

// NextPendingDraft returns the oldest pending draft belonging to the highest-scoring company,
// excluding any draft ids the caller has already passed over this session (see the /review
// skip flow) and any draft the send policy decided is BLOCKED — those aren't meant for the
// normal review queue (review.md: "blocked items should remain visible in logs/history", not
// offered for a human to send). Returns nil, nil when there's nothing left to show.
func (db *DB) NextPendingDraft(ctx context.Context, excludeIDs []int64) (*Draft, error) {
	var d Draft
	err := db.QueryRow(ctx,
		`SELECT d.id, d.company_id, d.contact_id, d.channel, d.subject, d.body, d.evidence_used, d.state, d.edited_by_human,
		        d.decision, d.decision_reason, d.decision_at, d.score_at_decision, d.created_at
		 FROM draft d
		 JOIN company c ON c.id = d.company_id
		 WHERE d.state = 'pending' AND NOT (d.id = ANY($1)) AND (d.decision IS NULL OR d.decision != 'blocked')
		 ORDER BY c.score DESC, d.created_at ASC
		 LIMIT 1`,
		excludeIDs).Scan(&d.ID, &d.CompanyID, &d.ContactID, &d.Channel, &d.Subject, &d.Body,
		&d.EvidenceUsed, &d.State, &d.EditedByHuman, &d.Decision, &d.DecisionReason, &d.DecisionAt, &d.ScoreAtDecision,
		&d.CreatedAt)

	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return &d, nil
}

// ReviewQueueItem is one row in the /review sidebar list — just enough to render a clickable
// row (company name/domain/score, draft id, decision) without pulling the full Company/Draft
// objects for every row in the queue.
type ReviewQueueItem struct {
	DraftID       int64
	CompanyID     int64
	CompanyName   string
	CompanyDomain string
	CompanyScore  int
	Decision      *string
}

// ListReviewQueue returns every reviewable (pending, non-BLOCKED) draft, highest company score
// first — the full queue for /review's "jump to a specific draft" list, as opposed to
// NextPendingDraft's single next-one-up pick for the skip-through flow.
func (db *DB) ListReviewQueue(ctx context.Context, limit int) ([]ReviewQueueItem, error) {
	rows, err := db.Query(ctx,
		`SELECT d.id, c.id, c.name, c.domain, c.score, d.decision
		 FROM draft d
		 JOIN company c ON c.id = d.company_id
		 WHERE d.state = 'pending' AND (d.decision IS NULL OR d.decision != 'blocked')
		 ORDER BY c.score DESC, d.created_at ASC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ReviewQueueItem
	for rows.Next() {
		var it ReviewQueueItem
		if err := rows.Scan(&it.DraftID, &it.CompanyID, &it.CompanyName, &it.CompanyDomain, &it.CompanyScore, &it.Decision); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// DraftListItem is one row of the /drafts screen — every draft regardless of state, unlike
// ListReviewQueue which only surfaces the pending-and-not-blocked subset. This is the direct
// answer to "a draft already exists for this company but I can't find it anywhere" — a blocked
// or needs_manual draft has real content (or a real rejection reason) worth seeing, not just a
// dashboard count with nowhere to click through to.
type DraftListItem struct {
	DraftID        int64
	CompanyID      int64
	CompanyName    string
	CompanyDomain  string
	CompanyScore   int
	Channel        string
	Subject        *string
	Body           string
	State          string
	Decision       *string
	DecisionReason *string
	CreatedAt      time.Time
}

// DraftFilter is every dimension /drafts can filter by.
type DraftFilter struct {
	State string // "" = any: pending|needs_manual|sent|approved|discarded
	// CreatedSince is one of "5m", "1h", "today", "7d", or "" for no filter.
	CreatedSince string
	// Search matches company name or domain, case-insensitive substring.
	Search string
}

func draftFilterClause(f DraftFilter, args []interface{}) (string, []interface{}) {
	clause := " WHERE 1=1"
	argNum := len(args) + 1

	if f.State != "" {
		clause += ` AND d.state = $` + strconv.Itoa(argNum)
		args = append(args, f.State)
		argNum++
	}
	switch f.CreatedSince {
	case "5m":
		clause += ` AND d.created_at >= now() - interval '5 minutes'`
	case "1h":
		clause += ` AND d.created_at >= now() - interval '1 hour'`
	case "today":
		clause += ` AND d.created_at >= date_trunc('day', now())`
	case "7d":
		clause += ` AND d.created_at >= now() - interval '7 days'`
	}
	if f.Search != "" {
		clause += ` AND (c.name ILIKE $` + strconv.Itoa(argNum) + ` OR c.domain ILIKE $` + strconv.Itoa(argNum) + `)`
		args = append(args, "%"+f.Search+"%")
		argNum++
	}
	return clause, args
}

// ListDrafts returns every draft matching f, newest first, with the company already joined in.
func (db *DB) ListDrafts(ctx context.Context, f DraftFilter, limit, offset int) ([]*DraftListItem, error) {
	where, args := draftFilterClause(f, nil)
	query := `SELECT d.id, c.id, c.name, c.domain, c.score, d.channel, d.subject, d.body, d.state, d.decision, d.decision_reason, d.created_at
	           FROM draft d JOIN company c ON c.id = d.company_id` + where

	argNum := len(args) + 1
	query += ` ORDER BY d.created_at DESC LIMIT $` + strconv.Itoa(argNum) + ` OFFSET $` + strconv.Itoa(argNum+1)
	args = append(args, limit, offset)

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*DraftListItem
	for rows.Next() {
		var it DraftListItem
		if err := rows.Scan(&it.DraftID, &it.CompanyID, &it.CompanyName, &it.CompanyDomain, &it.CompanyScore,
			&it.Channel, &it.Subject, &it.Body, &it.State, &it.Decision, &it.DecisionReason, &it.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, &it)
	}
	return items, rows.Err()
}

// CountDrafts returns how many drafts match f — the total behind /drafts' pagination.
func (db *DB) CountDrafts(ctx context.Context, f DraftFilter) (int, error) {
	where, args := draftFilterClause(f, nil)
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM draft d JOIN company c ON c.id = d.company_id`+where, args...).Scan(&n)
	return n, err
}

// DiscardDraft marks a draft discarded — the one thing that frees a company up for a fresh
// GenerateAndSaveDraft attempt (CompanyDraft/ListCompaniesForDrafting both refuse to draft a
// company that already has any non-discarded draft). Used for a needs_manual draft that's worth
// retrying (e.g. after fixing a missing contact), or a blocked one where the block reason no
// longer applies (domain since verified, contact since added).
func (db *DB) DiscardDraft(ctx context.Context, id int64) error {
	_, err := db.Exec(ctx, `UPDATE draft SET state = 'discarded' WHERE id = $1`, id)
	return err
}

// ListDraftsByState retrieves drafts in a given state
func (db *DB) ListDraftsByState(ctx context.Context, state string, limit, offset int) ([]*Draft, error) {
	rows, err := db.Query(ctx,
		`SELECT id, company_id, contact_id, channel, subject, body, evidence_used, state, edited_by_human,
		        decision, decision_reason, decision_at, score_at_decision, created_at
		 FROM draft WHERE state = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		state, limit, offset)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var drafts []*Draft
	for rows.Next() {
		var draft Draft
		err := rows.Scan(
			&draft.ID, &draft.CompanyID, &draft.ContactID, &draft.Channel,
			&draft.Subject, &draft.Body, &draft.EvidenceUsed, &draft.State,
			&draft.EditedByHuman, &draft.Decision, &draft.DecisionReason, &draft.DecisionAt, &draft.ScoreAtDecision,
			&draft.CreatedAt)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, &draft)
	}

	return drafts, rows.Err()
}
