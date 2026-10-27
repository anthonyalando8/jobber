package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrAlreadyReserved reports that another attempt already holds this message's idempotency key.
// The caller must resolve to that attempt rather than transmitting again.
var ErrAlreadyReserved = errors.New("another attempt already holds this send identity")

// SendApproval is the stored human authorization.
type SendApproval struct {
	ID                  int64
	DraftID             int64
	CompositionRevision int
	ContentHash         string
	InputHash           string
	RecipientContactID  int64
	RecipientEmail      string
	Intent              string
	ApprovedBy          string
	ApprovedAt          time.Time
	RevokedAt           *time.Time
	RevokedReason       string
	// Basis is ApprovalReviewed (approved after the AI review passed) or ApprovalManualOverride
	// (a person approved despite AI feedback). OverrideNote is the person's reason, if given.
	Basis        string
	OverrideNote string
}

const (
	ApprovalReviewed       = "reviewed"
	ApprovalManualOverride = "manual_override"
)

// ToPolicy converts the stored approval into the shape the preflight rules read.
func (a *SendApproval) ToPolicy() *send.Approval {
	if a == nil {
		return nil
	}
	return &send.Approval{
		ID: a.ID, CompositionRevision: a.CompositionRevision, ContentHash: a.ContentHash,
		InputHash: a.InputHash, RecipientContactID: a.RecipientContactID,
		RecipientEmail: a.RecipientEmail, Intent: plan.Intent(a.Intent),
		ApprovedBy: a.ApprovedBy, ApprovedAt: a.ApprovedAt, Revoked: a.RevokedAt != nil,
		ManualOverride: a.Basis == ApprovalManualOverride,
	}
}

// ApproveForSend records explicit human authorization for one exact revision. It refuses
// anything that is not a reviewed composition, so approval can never precede review.
func (db *DB) ApproveForSend(ctx context.Context, draftID int64, revision int, contentHash, approvedBy string) (*SendApproval, error) {
	if strings.TrimSpace(approvedBy) == "" {
		return nil, fmt.Errorf("an approval must record who gave it")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var status string
	var current int
	var rawSnapshot []byte
	var inputHash string
	err = tx.QueryRow(ctx, `SELECT status,current_revision,snapshot,input_hash FROM draft_composition WHERE draft_id=$1 FOR UPDATE`, draftID).
		Scan(&status, &current, &rawSnapshot, &inputHash)
	if err != nil {
		return nil, err
	}
	if status != "reviewed" {
		return nil, fmt.Errorf("only a reviewed composition can be approved; this one is %s", status)
	}
	if revision != current {
		return nil, fmt.Errorf("revision %d is not the current revision %d", revision, current)
	}
	var storedHash string
	if err = tx.QueryRow(ctx, `SELECT content_hash FROM draft_composition_revision WHERE draft_id=$1 AND revision=$2`, draftID, revision).Scan(&storedHash); err != nil {
		return nil, err
	}
	if contentHash != storedHash {
		return nil, fmt.Errorf("the content changed since it was displayed; reload and review before approving")
	}
	snapshot, err := decodeSnapshot(rawSnapshot)
	if err != nil {
		return nil, err
	}

	a := &SendApproval{
		DraftID: draftID, CompositionRevision: revision, ContentHash: storedHash, InputHash: inputHash,
		RecipientContactID: snapshot.Recipient.ContactID, RecipientEmail: snapshot.Recipient.Address,
		Intent: string(snapshot.Intent), ApprovedBy: approvedBy, Basis: ApprovalReviewed,
	}
	err = tx.QueryRow(ctx,
		`INSERT INTO send_approval(draft_id,composition_revision,content_hash,input_hash,
		   recipient_contact_id,recipient_email,intent,approved_by)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,approved_at`,
		a.DraftID, a.CompositionRevision, a.ContentHash, a.InputHash,
		a.RecipientContactID, a.RecipientEmail, a.Intent, a.ApprovedBy).Scan(&a.ID, &a.ApprovedAt)
	if err != nil {
		return nil, err
	}
	return a, tx.Commit(ctx)
}

// ErrNotManuallyApprovable explains why a person cannot approve this email over the AI review.
type ErrNotManuallyApprovable struct{ Reason string }

func (e *ErrNotManuallyApprovable) Error() string { return e.Reason }

// ManualApprovalBlockers are the deterministic checks that even a person's approval cannot
// override, for the email as it stands: critical factual problems (a false attachment, an
// unconfigured link, a technology outside the plan, the wrong title...). They are recomputed
// from the text with the current rules rather than read from history.
func ManualApprovalBlockers(s composition.Snapshot, e composition.Email) []composition.Finding {
	out := []composition.Finding{}
	for _, f := range composition.Validate(s, e) {
		if f.Severity == "block" {
			out = append(out, f)
		}
	}
	return out
}

// ApproveManually records a person's approval of an email the AI review did not pass, after they
// have read its findings. It is the same authorization as ApproveForSend, bound to the same exact
// revision, content and recipient, but recorded as a manual override so it is never mistaken for
// an AI pass; the review status and findings are left exactly as they are. It refuses while a
// critical deterministic check fails, because those are facts, not editorial opinions.
func (db *DB) ApproveManually(ctx context.Context, draftID int64, revision int, contentHash, approvedBy, note string) (*SendApproval, error) {
	if strings.TrimSpace(approvedBy) == "" {
		return nil, fmt.Errorf("an approval must record who gave it")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var status string
	var current int
	var rawSnapshot []byte
	var inputHash string
	err = tx.QueryRow(ctx, `SELECT CASE WHEN status='running' AND lease_until<now() THEN 'failed' ELSE status END,current_revision,snapshot,input_hash FROM draft_composition WHERE draft_id=$1 FOR UPDATE`, draftID).
		Scan(&status, &current, &rawSnapshot, &inputHash)
	if err != nil {
		return nil, err
	}
	switch status {
	case "review_required":
	case "reviewed":
		return nil, &ErrNotManuallyApprovable{"the AI review passed this email; use the normal approval"}
	default:
		return nil, &ErrNotManuallyApprovable{"this email is " + status + "; wait for it to finish, or save an edit, before approving"}
	}
	if revision != current {
		return nil, &ErrNotManuallyApprovable{fmt.Sprintf("revision %d is not the current revision %d; reload before approving", revision, current)}
	}
	var storedHash string
	var rawAttempt []byte
	if err = tx.QueryRow(ctx, `SELECT content_hash,attempt FROM draft_composition_revision WHERE draft_id=$1 AND revision=$2`, draftID, revision).Scan(&storedHash, &rawAttempt); err != nil {
		return nil, err
	}
	if contentHash != storedHash {
		return nil, &ErrNotManuallyApprovable{"the content changed since it was displayed; reload and read it before approving"}
	}
	snapshot, err := decodeSnapshot(rawSnapshot)
	if err != nil {
		return nil, err
	}
	var attempt composition.Attempt
	if err = json.Unmarshal(rawAttempt, &attempt); err != nil {
		return nil, err
	}
	if strings.TrimSpace(attempt.Email.Subject) == "" || strings.TrimSpace(attempt.Email.Body) == "" {
		return nil, &ErrNotManuallyApprovable{"this revision has no complete email to approve"}
	}
	if blockers := ManualApprovalBlockers(snapshot, attempt.Email); len(blockers) > 0 {
		codes := []string{}
		for _, f := range blockers {
			codes = append(codes, f.Code)
		}
		return nil, &ErrNotManuallyApprovable{"fix the critical checks first: " + strings.Join(codes, ", ")}
	}
	a := &SendApproval{
		DraftID: draftID, CompositionRevision: revision, ContentHash: storedHash, InputHash: inputHash,
		RecipientContactID: snapshot.Recipient.ContactID, RecipientEmail: snapshot.Recipient.Address,
		Intent: string(snapshot.Intent), ApprovedBy: approvedBy, Basis: ApprovalManualOverride,
		OverrideNote: strings.TrimSpace(note),
	}
	err = tx.QueryRow(ctx,
		`INSERT INTO send_approval(draft_id,composition_revision,content_hash,input_hash,
		   recipient_contact_id,recipient_email,intent,approved_by,basis,override_note)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id,approved_at`,
		a.DraftID, a.CompositionRevision, a.ContentHash, a.InputHash,
		a.RecipientContactID, a.RecipientEmail, a.Intent, a.ApprovedBy, a.Basis, a.OverrideNote).Scan(&a.ID, &a.ApprovedAt)
	if err != nil {
		return nil, err
	}
	return a, tx.Commit(ctx)
}

// RevokeApproval withdraws authorization. Editing or regenerating a draft should call this, so a
// stale approval can never authorize new content.
func (db *DB) RevokeApproval(ctx context.Context, draftID int64, reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "revoked"
	}
	_, err := db.Exec(ctx,
		`UPDATE send_approval SET revoked_at=now(),revoked_reason=$2 WHERE draft_id=$1 AND revoked_at IS NULL`,
		draftID, reason)
	return err
}

// LiveApproval returns the current approval, or nil.
func (db *DB) LiveApproval(ctx context.Context, draftID int64) (*SendApproval, error) {
	a := &SendApproval{}
	err := db.QueryRow(ctx,
		`SELECT id,draft_id,composition_revision,content_hash,input_hash,recipient_contact_id,
		        recipient_email,intent,approved_by,approved_at,revoked_at,revoked_reason,basis,override_note
		 FROM send_approval WHERE draft_id=$1 AND revoked_at IS NULL`, draftID).
		Scan(&a.ID, &a.DraftID, &a.CompositionRevision, &a.ContentHash, &a.InputHash,
			&a.RecipientContactID, &a.RecipientEmail, &a.Intent, &a.ApprovedBy, &a.ApprovedAt,
			&a.RevokedAt, &a.RevokedReason, &a.Basis, &a.OverrideNote)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// SendAttempt is one durable transmission attempt.
type SendAttempt struct {
	ID                  int64
	IdempotencyKey      string
	DraftID             int64
	CompositionRevision int
	ApprovalID          int64
	ThreadID            *int64
	CompanyID           int64
	RecipientContactID  int64
	RecipientEmail      string
	Intent              string
	Kind                string
	FollowupStep        int
	Subject, Body       string
	ContentHash         string
	MessageID           string
	ProfileRevisionID   int64
	OpportunityID       *int64
	OpportunityRevision *int
	PlanID              int64
	PlanRevision        int
	PolicyVersion       string
	Initiator           string
	State               string
	Provider            string
	ProviderMessageID   string
	ProviderResponse    string
	ProviderStage       string
	ErrorCategory       string
	ReservedAt          time.Time
	StartedAt           *time.Time
	SettledAt           *time.Time
}

const sendAttemptColumns = `id,idempotency_key,draft_id,composition_revision,approval_id,thread_id,
 company_id,recipient_contact_id,recipient_email,intent,kind,followup_step,subject,body,content_hash,
 message_id,profile_revision_id,opportunity_id,opportunity_revision,plan_id,plan_revision,
 policy_version,initiator,state,provider,provider_message_id,provider_response,provider_stage,
 error_category,reserved_at,started_at,settled_at`

func scanAttempt(row rowScanner) (*SendAttempt, error) {
	a := &SendAttempt{}
	err := row.Scan(&a.ID, &a.IdempotencyKey, &a.DraftID, &a.CompositionRevision, &a.ApprovalID,
		&a.ThreadID, &a.CompanyID, &a.RecipientContactID, &a.RecipientEmail, &a.Intent, &a.Kind,
		&a.FollowupStep, &a.Subject, &a.Body, &a.ContentHash, &a.MessageID, &a.ProfileRevisionID,
		&a.OpportunityID, &a.OpportunityRevision, &a.PlanID, &a.PlanRevision, &a.PolicyVersion,
		&a.Initiator, &a.State, &a.Provider, &a.ProviderMessageID, &a.ProviderResponse,
		&a.ProviderStage, &a.ErrorCategory, &a.ReservedAt, &a.StartedAt, &a.SettledAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// ReserveSend writes the attempt before anything is transmitted. The partial unique index on the
// idempotency key is what makes two concurrent requests resolve to one transmission: the loser
// gets ErrAlreadyReserved and must attach to the winner rather than contacting the provider.
func (db *DB) ReserveSend(ctx context.Context, a SendAttempt) (*SendAttempt, error) {
	if a.IdempotencyKey == "" || a.ContentHash == "" || a.Initiator == "" {
		return nil, fmt.Errorf("a reservation needs its identity, content hash and initiator")
	}
	a.State = send.StateReserved
	a.PolicyVersion = send.PolicyVersion
	row := db.QueryRow(ctx,
		`INSERT INTO send_attempt(idempotency_key,draft_id,composition_revision,approval_id,thread_id,
		   company_id,recipient_contact_id,recipient_email,intent,kind,followup_step,subject,body,
		   content_hash,message_id,profile_revision_id,opportunity_id,opportunity_revision,plan_id,
		   plan_revision,policy_version,initiator,state)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
		 RETURNING `+sendAttemptColumns,
		a.IdempotencyKey, a.DraftID, a.CompositionRevision, a.ApprovalID, a.ThreadID,
		a.CompanyID, a.RecipientContactID, a.RecipientEmail, a.Intent, a.Kind, a.FollowupStep,
		a.Subject, a.Body, a.ContentHash, a.MessageID, a.ProfileRevisionID, a.OpportunityID,
		a.OpportunityRevision, a.PlanID, a.PlanRevision, a.PolicyVersion, a.Initiator, a.State)
	reserved, err := scanAttempt(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "send_attempt_live_key") {
			return nil, ErrAlreadyReserved
		}
		return nil, err
	}
	return reserved, nil
}

// MarkSending records that transmission has begun, so a crash mid-call is visible as an attempt
// that reached the provider rather than as a clean reservation.
func (db *DB) MarkSending(ctx context.Context, id int64) error {
	tag, err := db.Exec(ctx, `UPDATE send_attempt SET state='sending',started_at=now() WHERE id=$1 AND state='reserved'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("attempt %d is not reserved", id)
	}
	return nil
}

// SettleSend writes the provider's verdict. The trigger refuses to change an already-settled
// attempt, so an accepted send can never be rewritten as a failure.
func (db *DB) SettleSend(ctx context.Context, id int64, result send.Result) (*SendAttempt, error) {
	state := result.State()
	row := db.QueryRow(ctx,
		`UPDATE send_attempt SET state=$2,provider=$3,provider_message_id=$4,provider_response=$5,
		   provider_stage=$6,error_category=$7,settled_at=now()
		 WHERE id=$1 AND state IN ('reserved','sending') RETURNING `+sendAttemptColumns,
		id, state, result.Provider, result.ProviderMessageID, result.Response, result.Stage, result.ErrorCategory)
	return scanAttempt(row)
}

// CancelReservation releases a reservation that never reached the provider.
func (db *DB) CancelReservation(ctx context.Context, id int64, reason string) error {
	_, err := db.Exec(ctx,
		`UPDATE send_attempt SET state='cancelled',error_category=$2,settled_at=now() WHERE id=$1 AND state='reserved'`,
		id, reason)
	return err
}

// AttemptsForKey returns every attempt against one send identity, oldest first.
func (db *DB) AttemptsForKey(ctx context.Context, key string) ([]*SendAttempt, error) {
	rows, err := db.Query(ctx, `SELECT `+sendAttemptColumns+` FROM send_attempt WHERE idempotency_key=$1 ORDER BY reserved_at`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SendAttempt{}
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AttemptsForDraft lists the audit trail shown on the draft page.
func (db *DB) AttemptsForDraft(ctx context.Context, draftID int64) ([]*SendAttempt, error) {
	rows, err := db.Query(ctx, `SELECT `+sendAttemptColumns+` FROM send_attempt WHERE draft_id=$1 ORDER BY reserved_at DESC`, draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SendAttempt{}
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (db *DB) GetSendAttempt(ctx context.Context, id int64) (*SendAttempt, error) {
	return scanAttempt(db.QueryRow(ctx, `SELECT `+sendAttemptColumns+` FROM send_attempt WHERE id=$1`, id))
}

// SendVolume reports what the daily cap and minimum interval need to know.
func (db *DB) SendVolume(ctx context.Context) (sentToday int, lastSendAt time.Time, err error) {
	var last *time.Time
	err = db.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE settled_at > now() - interval '24 hours'), max(settled_at)
		 FROM send_attempt WHERE state='accepted'`).Scan(&sentToday, &last)
	if err != nil {
		return 0, time.Time{}, err
	}
	if last != nil {
		lastSendAt = *last
	}
	return sentToday, lastSendAt, nil
}

// DomainSendHistory reports live conversations and the most recent accepted first contact at a
// domain, which is what the per-domain controls are built on.
func (db *DB) DomainSendHistory(ctx context.Context, domain string) (activeThreads int, lastFirstContact time.Time, err error) {
	if strings.TrimSpace(domain) == "" {
		return 0, time.Time{}, nil
	}
	var last *time.Time
	err = db.QueryRow(ctx,
		`SELECT
		   (SELECT count(*) FROM outbound_thread t JOIN company c ON c.id=t.company_id
		     WHERE lower(c.domain)=lower($1) AND t.state='active'),
		   (SELECT max(a.settled_at) FROM send_attempt a JOIN company c ON c.id=a.company_id
		     WHERE lower(c.domain)=lower($1) AND a.state='accepted' AND a.kind='first_contact')`,
		domain).Scan(&activeThreads, &last)
	if err != nil {
		return 0, time.Time{}, err
	}
	if last != nil {
		lastFirstContact = *last
	}
	return activeThreads, lastFirstContact, nil
}

func decodeSnapshot(raw []byte) (composition.Snapshot, error) {
	var s composition.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	return s, nil
}
