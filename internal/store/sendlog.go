package store

import (
	"context"
	"time"
)

// SendLog tracks a single outreach send and its follow-up schedule. SentAt records when SMTP
// accepted the handoff — not proof of delivery; BouncedAt/BounceReason record the asynchronous
// bounce notification (Gmail, etc. reject a dead mailbox minutes later, not during the SMTP
// conversation) that only a human, reading their own inbox, can notice and record.
type SendLog struct {
	ID               int64
	DraftID          int64
	CompanyID        int64
	ToValue          string
	Channel          string
	SentAt           time.Time
	ProviderResponse *string
	FollowUp1Due     *time.Time
	FollowUp2Due     *time.Time
	FollowUp1SentAt  *time.Time
	FollowUp2SentAt  *time.Time
	RepliedAt        *time.Time
	BouncedAt        *time.Time
	BounceReason     *string
}

// CreateSendLog records a send with its two follow-up due dates (+3d, +8d per spec).
func (db *DB) CreateSendLog(ctx context.Context, draftID, companyID int64, toValue, channel string, followUp1Due, followUp2Due time.Time) (*SendLog, error) {
	var id int64
	var sentAt time.Time

	err := db.QueryRow(ctx,
		`INSERT INTO send_log (draft_id, company_id, to_value, channel, followup_1_due, followup_2_due)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, sent_at`,
		draftID, companyID, toValue, channel, followUp1Due, followUp2Due).Scan(&id, &sentAt)

	if err != nil {
		return nil, err
	}

	return &SendLog{
		ID:           id,
		DraftID:      draftID,
		CompanyID:    companyID,
		ToValue:      toValue,
		Channel:      channel,
		SentAt:       sentAt,
		FollowUp1Due: &followUp1Due,
		FollowUp2Due: &followUp2Due,
	}, nil
}

// GetSendLog retrieves a send log entry
func (db *DB) GetSendLog(ctx context.Context, id int64) (*SendLog, error) {
	var log SendLog

	err := db.QueryRow(ctx,
		`SELECT id, draft_id, company_id, to_value, channel, sent_at, provider_response,
		        followup_1_due, followup_2_due, followup_1_sent_at, followup_2_sent_at, replied_at,
		        bounced_at, bounce_reason
		 FROM send_log WHERE id = $1`,
		id).Scan(&log.ID, &log.DraftID, &log.CompanyID, &log.ToValue, &log.Channel, &log.SentAt,
		&log.ProviderResponse, &log.FollowUp1Due, &log.FollowUp2Due, &log.FollowUp1SentAt, &log.FollowUp2SentAt, &log.RepliedAt,
		&log.BouncedAt, &log.BounceReason)

	if err != nil {
		return nil, err
	}

	return &log, nil
}

// ListSendLogs retrieves send logs for a company
func (db *DB) ListSendLogs(ctx context.Context, companyID int64) ([]*SendLog, error) {
	rows, err := db.Query(ctx,
		`SELECT id, draft_id, company_id, to_value, channel, sent_at, provider_response,
		        followup_1_due, followup_2_due, followup_1_sent_at, followup_2_sent_at, replied_at,
		        bounced_at, bounce_reason
		 FROM send_log WHERE company_id = $1 ORDER BY sent_at DESC`,
		companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []*SendLog
	for rows.Next() {
		var log SendLog
		if err := rows.Scan(&log.ID, &log.DraftID, &log.CompanyID, &log.ToValue, &log.Channel, &log.SentAt,
			&log.ProviderResponse, &log.FollowUp1Due, &log.FollowUp2Due, &log.FollowUp1SentAt, &log.FollowUp2SentAt, &log.RepliedAt,
			&log.BouncedAt, &log.BounceReason); err != nil {
			return nil, err
		}
		logs = append(logs, &log)
	}

	return logs, rows.Err()
}

// ListFollowupsDue returns send_log rows with at least one follow-up that's due, not yet sent,
// and the company hasn't already replied. Checking followup_N_sent_at individually (rather than
// just the due date) is what makes a follow-up actually drop off this list once it's been sent
// — previously nothing ever cleared "due", so a sent follow-up would show as due forever.
func (db *DB) ListFollowupsDue(ctx context.Context, onOrBefore time.Time) ([]*SendLog, error) {
	rows, err := db.Query(ctx,
		`SELECT id, draft_id, company_id, to_value, channel, sent_at, provider_response,
		        followup_1_due, followup_2_due, followup_1_sent_at, followup_2_sent_at, replied_at,
		        bounced_at, bounce_reason
		 FROM send_log
		 WHERE replied_at IS NULL
		   AND bounced_at IS NULL
		   AND ((followup_1_due IS NOT NULL AND followup_1_due <= $1 AND followup_1_sent_at IS NULL)
		        OR (followup_2_due IS NOT NULL AND followup_2_due <= $1 AND followup_2_sent_at IS NULL))
		 ORDER BY sent_at ASC`,
		onOrBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []*SendLog
	for rows.Next() {
		var log SendLog
		if err := rows.Scan(&log.ID, &log.DraftID, &log.CompanyID, &log.ToValue, &log.Channel, &log.SentAt,
			&log.ProviderResponse, &log.FollowUp1Due, &log.FollowUp2Due, &log.FollowUp1SentAt, &log.FollowUp2SentAt, &log.RepliedAt,
			&log.BouncedAt, &log.BounceReason); err != nil {
			return nil, err
		}
		logs = append(logs, &log)
	}

	return logs, rows.Err()
}

// MarkFollowup1Sent / MarkFollowup2Sent record that a follow-up actually went out — the only
// thing that makes ListFollowupsDue stop offering it again.
func (db *DB) MarkFollowup1Sent(ctx context.Context, id int64) error {
	_, err := db.Exec(ctx, `UPDATE send_log SET followup_1_sent_at = now() WHERE id = $1`, id)
	return err
}

func (db *DB) MarkFollowup2Sent(ctx context.Context, id int64) error {
	_, err := db.Exec(ctx, `UPDATE send_log SET followup_2_sent_at = now() WHERE id = $1`, id)
	return err
}

// MarkReplied records that a send_log's company replied.
func (db *DB) MarkReplied(ctx context.Context, id int64) error {
	_, err := db.Exec(ctx,
		`UPDATE send_log SET replied_at = now() WHERE id = $1`, id)
	return err
}

// MarkRepliedForCompany marks the most recent not-yet-replied send for a company as replied —
// used when a human records an outcome (review.md §32) on the company page rather than from a
// specific send_log row. A no-op (not an error) if there's no matching row, since a human might
// record an outcome for a company that was never actually sent to via this tool.
func (db *DB) MarkRepliedForCompany(ctx context.Context, companyID int64) error {
	_, err := db.Exec(ctx,
		`UPDATE send_log SET replied_at = now()
		 WHERE id = (
		   SELECT id FROM send_log WHERE company_id = $1 AND replied_at IS NULL
		   ORDER BY sent_at DESC LIMIT 1
		 )`, companyID)
	return err
}

// SentEmailView is one row of the /sent screen — a send_log entry joined with the company and
// the exact draft that went out, so "what did I actually send, and to whom" is one click from
// the dashboard instead of cross-referencing company and draft tables by hand.
type SentEmailView struct {
	SendLogID    int64
	CompanyID    int64
	CompanyName  string
	Domain       string
	ToValue      string
	Subject      *string
	Body         string
	SentAt       time.Time
	RepliedAt    *time.Time
	BouncedAt    *time.Time
	BounceReason *string
}

// ListSentEmails returns sent emails newest-first, with the company and draft content already
// joined in — the dashboard's "Sent" count links here.
func (db *DB) ListSentEmails(ctx context.Context, limit, offset int) ([]*SentEmailView, error) {
	rows, err := db.Query(ctx,
		`SELECT sl.id, sl.company_id, c.name, c.domain, sl.to_value, d.subject, d.body, sl.sent_at, sl.replied_at,
		        sl.bounced_at, sl.bounce_reason
		 FROM send_log sl
		 JOIN company c ON c.id = sl.company_id
		 JOIN draft d ON d.id = sl.draft_id
		 ORDER BY sl.sent_at DESC
		 LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var views []*SentEmailView
	for rows.Next() {
		var v SentEmailView
		if err := rows.Scan(&v.SendLogID, &v.CompanyID, &v.CompanyName, &v.Domain, &v.ToValue, &v.Subject, &v.Body, &v.SentAt, &v.RepliedAt,
			&v.BouncedAt, &v.BounceReason); err != nil {
			return nil, err
		}
		views = append(views, &v)
	}
	return views, rows.Err()
}

// CountSentEmails is the total behind ListSentEmails' pagination.
func (db *DB) CountSentEmails(ctx context.Context) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM send_log`).Scan(&n)
	return n, err
}

// ListSentEmailsForCompany returns one company's send history with the actual draft content
// joined in, newest first — the company page's "what did I actually send this company" answer,
// same shape as ListSentEmails but scoped to one company instead of paginated globally.
func (db *DB) ListSentEmailsForCompany(ctx context.Context, companyID int64) ([]*SentEmailView, error) {
	rows, err := db.Query(ctx,
		`SELECT sl.id, sl.company_id, c.name, c.domain, sl.to_value, d.subject, d.body, sl.sent_at, sl.replied_at,
		        sl.bounced_at, sl.bounce_reason
		 FROM send_log sl
		 JOIN company c ON c.id = sl.company_id
		 JOIN draft d ON d.id = sl.draft_id
		 WHERE sl.company_id = $1
		 ORDER BY sl.sent_at DESC`,
		companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var views []*SentEmailView
	for rows.Next() {
		var v SentEmailView
		if err := rows.Scan(&v.SendLogID, &v.CompanyID, &v.CompanyName, &v.Domain, &v.ToValue, &v.Subject, &v.Body, &v.SentAt, &v.RepliedAt,
			&v.BouncedAt, &v.BounceReason); err != nil {
			return nil, err
		}
		views = append(views, &v)
	}
	return views, rows.Err()
}

// MarkBounced records that a sent email actually bounced — a human noticing the NDR in their own
// inbox, since Gmail (and most providers) reject a dead mailbox asynchronously, not during the
// SMTP conversation this tool can see. Excluded from HasRecentSendToDomain below, so a bounce
// doesn't block a corrected resend the way a real successful send would.
func (db *DB) MarkBounced(ctx context.Context, sendLogID int64, reason string) error {
	_, err := db.Exec(ctx,
		`UPDATE send_log SET bounced_at = now(), bounce_reason = $2 WHERE id = $1`,
		sendLogID, reason)
	return err
}

// HasRecentSendToDomain reports whether the domain was successfully sent to within the given
// window — the spec's "never contact the same domain twice within 30 days" rule, enforced
// against send_log rather than trusted to the UI alone. A row marked bounced is excluded: it's
// evidence nothing was actually delivered, so it must not block a corrected resend.
func (db *DB) HasRecentSendToDomain(ctx context.Context, domain string, since time.Time) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM send_log sl
			JOIN company c ON c.id = sl.company_id
			WHERE c.domain = $1 AND sl.sent_at > $2 AND sl.bounced_at IS NULL
		 )`,
		domain, since).Scan(&exists)
	return exists, err
}
