package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/jackc/pgx/v5"
)

// OutboundThread is a durable conversation. Its intent, recipient and root draft are frozen; a
// different purpose requires a new contact plan and a new thread rather than a change here.
type OutboundThread struct {
	ID                 int64
	CompanyID          int64
	CompanyName        string
	PlanID             int64
	PlanRevision       int
	Intent             string
	RecipientContactID int64
	RecipientEmail     string
	OpportunityID      *int64
	RootDraftID        int64
	RootMessageID      string
	State              string
	StateReason        string
	FollowupStep       int
	LastSentAt         *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

const threadColumns = `id,company_id,plan_id,plan_revision,intent,recipient_contact_id,recipient_email,
 opportunity_id,root_draft_id,root_message_id,state,state_reason,followup_step,last_sent_at,created_at,updated_at`

// threadColumnsFor qualifies the same list with a table alias. Listing threads joins company,
// which has its own id, created_at and updated_at, so the unqualified list is ambiguous there —
// PostgreSQL rejects the query outright rather than guessing.
func threadColumnsFor(alias string) string {
	columns := strings.Split(strings.ReplaceAll(threadColumns, "\n", ""), ",")
	for i, column := range columns {
		columns[i] = alias + "." + strings.TrimSpace(column)
	}
	return strings.Join(columns, ",")
}

func scanThread(row rowScanner) (*OutboundThread, error) {
	t := &OutboundThread{}
	err := row.Scan(&t.ID, &t.CompanyID, &t.PlanID, &t.PlanRevision, &t.Intent, &t.RecipientContactID,
		&t.RecipientEmail, &t.OpportunityID, &t.RootDraftID, &t.RootMessageID, &t.State,
		&t.StateReason, &t.FollowupStep, &t.LastSentAt, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// OpenThread records the conversation started by an accepted first contact. It is called only
// after acceptance, so an unsent draft never creates a thread.
func (db *DB) OpenThread(ctx context.Context, a *SendAttempt) (*OutboundThread, error) {
	if a.State != send.StateAccepted {
		return nil, fmt.Errorf("a thread opens only from an accepted send")
	}
	row := db.QueryRow(ctx,
		`INSERT INTO outbound_thread(company_id,plan_id,plan_revision,intent,recipient_contact_id,
		   recipient_email,opportunity_id,root_draft_id,root_message_id,state,followup_step,last_sent_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'active',0,now())
		 ON CONFLICT (root_draft_id) DO UPDATE SET updated_at=now()
		 RETURNING `+threadColumns,
		a.CompanyID, a.PlanID, a.PlanRevision, a.Intent, a.RecipientContactID,
		a.RecipientEmail, a.OpportunityID, a.DraftID, a.MessageID)
	return scanThread(row)
}

// RecordThreadSend advances a thread after an accepted follow-up.
func (db *DB) RecordThreadSend(ctx context.Context, threadID int64, step int) error {
	tag, err := db.Exec(ctx,
		`UPDATE outbound_thread SET followup_step=GREATEST(followup_step,$2),last_sent_at=now(),updated_at=now()
		 WHERE id=$1 AND state='active'`, threadID, step)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("thread %d is not active", threadID)
	}
	return nil
}

func (db *DB) GetThread(ctx context.Context, id int64) (*OutboundThread, error) {
	return scanThread(db.QueryRow(ctx, `SELECT `+threadColumns+` FROM outbound_thread WHERE id=$1`, id))
}

// ActiveThreadForContact returns the live conversation with a recipient, or nil.
func (db *DB) ActiveThreadForContact(ctx context.Context, contactID int64) (*OutboundThread, error) {
	t, err := scanThread(db.QueryRow(ctx,
		`SELECT `+threadColumns+` FROM outbound_thread WHERE recipient_contact_id=$1 AND state='active'`, contactID))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return t, err
}

// ThreadForRootDraft finds the conversation a draft started.
func (db *DB) ThreadForRootDraft(ctx context.Context, draftID int64) (*OutboundThread, error) {
	t, err := scanThread(db.QueryRow(ctx,
		`SELECT `+threadColumns+` FROM outbound_thread WHERE root_draft_id=$1`, draftID))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (db *DB) ListThreads(ctx context.Context, limit int) ([]*OutboundThread, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.Query(ctx,
		`SELECT `+threadColumnsFor("t")+`,c.name FROM outbound_thread t JOIN company c ON c.id=t.company_id
		 ORDER BY t.updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OutboundThread{}
	for rows.Next() {
		t := &OutboundThread{}
		if err := rows.Scan(&t.ID, &t.CompanyID, &t.PlanID, &t.PlanRevision, &t.Intent,
			&t.RecipientContactID, &t.RecipientEmail, &t.OpportunityID, &t.RootDraftID,
			&t.RootMessageID, &t.State, &t.StateReason, &t.FollowupStep, &t.LastSentAt,
			&t.CreatedAt, &t.UpdatedAt, &t.CompanyName); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ThreadEvent is an append-only fact about a conversation. There is no inbox integration in this
// phase, so replies and bounces arrive from an operator unless a provider supplies them.
type ThreadEvent struct {
	ID            int64
	ThreadID      int64
	SendAttemptID *int64
	Kind          string
	Detail        string
	Source        string
	OccurredAt    time.Time
}

// RecordThreadEvent appends a fact and applies its consequence to the thread state. A permanent
// bounce also marks the contact bad and suppresses the address, so no later send or follow-up
// can reach it.
func (db *DB) RecordThreadEvent(ctx context.Context, e ThreadEvent) error {
	switch e.Kind {
	case "replied", "bounced_permanent", "bounced_transient", "closed", "do_not_contact", "reconciled", "note":
	default:
		return fmt.Errorf("unknown thread event %q", e.Kind)
	}
	if e.Source != "operator" && e.Source != "provider" {
		return fmt.Errorf("a thread event must say where it came from")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var contactID int64
	var email string
	if err = tx.QueryRow(ctx, `SELECT recipient_contact_id,recipient_email FROM outbound_thread WHERE id=$1 FOR UPDATE`, e.ThreadID).
		Scan(&contactID, &email); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO thread_event(thread_id,send_attempt_id,kind,detail,source) VALUES($1,$2,$3,$4,$5)`,
		e.ThreadID, e.SendAttemptID, e.Kind, e.Detail, e.Source); err != nil {
		return err
	}

	state := ""
	switch e.Kind {
	case "replied":
		state = send.ThreadReplied
	case "bounced_permanent":
		state = send.ThreadBounced
	case "closed":
		state = send.ThreadClosed
	case "do_not_contact":
		state = send.ThreadDoNotContact
	}
	if state != "" {
		if _, err = tx.Exec(ctx,
			`UPDATE outbound_thread SET state=$2,state_reason=$3,updated_at=now() WHERE id=$1 AND state='active'`,
			e.ThreadID, state, e.Detail); err != nil {
			return err
		}
	}
	// A permanent bounce is the one event that must also stop every future message to this
	// address, not merely this conversation.
	if e.Kind == "bounced_permanent" {
		if _, err = tx.Exec(ctx,
			`UPDATE contact SET status='bad',bad_reason=$2,bad_at=now() WHERE id=$1`,
			contactID, "permanent bounce: "+e.Detail); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx,
			`INSERT INTO suppression(value,reason) VALUES($1,$2) ON CONFLICT(value) DO NOTHING`,
			email, "permanent bounce"); err != nil {
			return err
		}
	}
	if e.Kind == "do_not_contact" {
		if _, err = tx.Exec(ctx,
			`INSERT INTO suppression(value,reason) VALUES($1,$2) ON CONFLICT(value) DO NOTHING`,
			email, "do not contact"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (db *DB) ThreadEvents(ctx context.Context, threadID int64) ([]ThreadEvent, error) {
	rows, err := db.Query(ctx,
		`SELECT id,thread_id,send_attempt_id,kind,detail,source,occurred_at FROM thread_event
		 WHERE thread_id=$1 ORDER BY occurred_at DESC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ThreadEvent{}
	for rows.Next() {
		var e ThreadEvent
		if err := rows.Scan(&e.ID, &e.ThreadID, &e.SendAttemptID, &e.Kind, &e.Detail, &e.Source, &e.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
