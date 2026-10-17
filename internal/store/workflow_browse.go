package store

import (
	"context"
	"time"
)

type CompositionListFilter struct {
	Query, Status, Attention string
	CompanyID                int64
}

const compositionAttentionPredicate = `d.state NOT IN ('sent','discarded') AND (h.status='review_required' OR (h.status='reviewed' AND NOT EXISTS(SELECT 1 FROM send_approval a WHERE a.draft_id=h.draft_id AND a.composition_revision=h.current_revision AND a.revoked_at IS NULL)))`
const compositionBrowseFrom = ` FROM draft_composition h JOIN draft d ON d.id=h.draft_id JOIN company c ON c.id=d.company_id `
const compositionBrowseWhere = ` WHERE ($1='' OR c.name ILIKE $1 OR COALESCE(d.subject,'') ILIKE $1) AND ($2='' OR CASE WHEN h.status='running' AND h.lease_until<now() THEN 'failed' ELSE h.status END=$2)
 AND ($3='' OR ($3='required' AND (` + compositionAttentionPredicate + `)) OR ($3='semantic_review' AND h.status='review_required' AND d.state NOT IN ('sent','discarded')) OR ($3='approval_pending' AND h.status='reviewed' AND d.state NOT IN ('sent','discarded') AND NOT EXISTS(SELECT 1 FROM send_approval a WHERE a.draft_id=h.draft_id AND a.composition_revision=h.current_revision AND a.revoked_at IS NULL))) AND ($4=0 OR d.company_id=$4) `

func (f CompositionListFilter) args() []any {
	return []any{likeSearch(f.Query), f.Status, f.Attention, f.CompanyID}
}
func (db *DB) CountCompositionList(ctx context.Context, f CompositionListFilter) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*)`+compositionBrowseFrom+compositionBrowseWhere, f.args()...).Scan(&n)
	return n, err
}
func (db *DB) ListCompositionPage(ctx context.Context, f CompositionListFilter, limit, offset int) ([]CompositionSummary, error) {
	args := append(f.args(), limit, offset)
	rows, err := db.Query(ctx, `SELECT d.id,c.name,h.snapshot->>'intent',COALESCE(d.subject,''),CASE WHEN h.status='running' AND h.lease_until<now() THEN 'failed' ELSE h.status END`+compositionBrowseFrom+compositionBrowseWhere+` ORDER BY h.updated_at DESC,d.id DESC LIMIT $5 OFFSET $6`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CompositionSummary{}
	for rows.Next() {
		var s CompositionSummary
		if err = rows.Scan(&s.ID, &s.Company, &s.Intent, &s.Subject, &s.Status); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type ThreadListFilter struct {
	Query, State, Event string
	CompanyID           int64
}

const threadBrowseFrom = ` FROM outbound_thread t JOIN company c ON c.id=t.company_id `
const threadBrowseWhere = ` WHERE ($1='' OR c.name ILIKE $1 OR t.recipient_email ILIKE $1) AND ($2='' OR t.state=$2) AND ($3='' OR EXISTS(SELECT 1 FROM thread_event e WHERE e.thread_id=t.id AND e.kind=$3)) AND ($4=0 OR t.company_id=$4) `

func (f ThreadListFilter) args() []any {
	return []any{likeSearch(f.Query), f.State, f.Event, f.CompanyID}
}
func (db *DB) CountThreadList(ctx context.Context, f ThreadListFilter) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*)`+threadBrowseFrom+threadBrowseWhere, f.args()...).Scan(&n)
	return n, err
}
func (db *DB) ListThreadPage(ctx context.Context, f ThreadListFilter, limit, offset int) ([]*OutboundThread, error) {
	rows, err := db.Query(ctx, `SELECT `+threadColumnsFor("t")+`,c.name`+threadBrowseFrom+threadBrowseWhere+` ORDER BY t.updated_at DESC,t.id DESC LIMIT $5 OFFSET $6`, append(f.args(), limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OutboundThread{}
	for rows.Next() {
		t := &OutboundThread{}
		if err = rows.Scan(&t.ID, &t.CompanyID, &t.PlanID, &t.PlanRevision, &t.Intent, &t.RecipientContactID, &t.RecipientEmail, &t.OpportunityID, &t.RootDraftID, &t.RootMessageID, &t.State, &t.StateReason, &t.FollowupStep, &t.LastSentAt, &t.CreatedAt, &t.UpdatedAt, &t.CompanyName); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type MessageListFilter struct {
	State     string
	CompanyID int64
}
type MessageSummary struct {
	ID, DraftID, CompanyID  int64
	Company, Subject, State string
	ThreadID                *int64
	SettledAt               *time.Time
}

const messageBrowseFrom = ` FROM send_attempt a JOIN company c ON c.id=a.company_id `
const messageBrowseWhere = ` WHERE ($1='' OR a.state=$1) AND ($2=0 OR a.company_id=$2) `

func (db *DB) CountMessageList(ctx context.Context, f MessageListFilter) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*)`+messageBrowseFrom+messageBrowseWhere, f.State, f.CompanyID).Scan(&n)
	return n, err
}
func (db *DB) ListMessagePage(ctx context.Context, f MessageListFilter, limit, offset int) ([]MessageSummary, error) {
	rows, err := db.Query(ctx, `SELECT a.id,a.draft_id,a.company_id,c.name,a.subject,a.state,a.thread_id,a.settled_at`+messageBrowseFrom+messageBrowseWhere+` ORDER BY a.reserved_at DESC,a.id DESC LIMIT $3 OFFSET $4`, f.State, f.CompanyID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MessageSummary{}
	for rows.Next() {
		var s MessageSummary
		if err = rows.Scan(&s.ID, &s.DraftID, &s.CompanyID, &s.Company, &s.Subject, &s.State, &s.ThreadID, &s.SettledAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
