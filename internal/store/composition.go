package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/jackc/pgx/v5"
)

type ComposedDraft struct {
	ID           int64
	Snapshot     composition.Snapshot
	Status       string
	Revision     int
	Attempts     []composition.Attempt
	StaleReasons []string
}

// CompositionExistsError refuses a second email for the same plan, or for the same vacancy and
// intent, while the first is still live. Composing again would spend provider calls on a duplicate
// and could send two applications for one job.
type CompositionExistsError struct {
	DraftID int64
	Sent    bool
}

func (e *CompositionExistsError) Error() string {
	if e.Sent {
		return fmt.Sprintf("an email for this plan was already sent (draft %d)", e.DraftID)
	}
	return fmt.Sprintf("draft %d already exists for this plan; discard it before composing again", e.DraftID)
}

// ErrCompositionNotDiscardable means the draft has a send attempt or a run in progress.
var ErrCompositionNotDiscardable = errors.New("this draft has a send attempt or a run in progress, so it cannot be discarded")

// DiscardComposition retires a composed draft so the plan can be composed again. A draft that has
// any send attempt is part of the send record and is never discarded; any approval is revoked so
// a discarded draft can never be sent.
func (db *DB) DiscardComposition(ctx context.Context, draftID int64, reason string) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var blocked bool
	err = tx.QueryRow(ctx, `SELECT d.state='sent' OR EXISTS(SELECT 1 FROM send_attempt a WHERE a.draft_id=d.id)
		   OR (c.status IN ('queued','running') AND COALESCE(c.lease_until,c.created_at+interval '10 minutes')>=now())
		 FROM draft d JOIN draft_composition c ON c.draft_id=d.id WHERE d.id=$1 FOR UPDATE OF d`, draftID).Scan(&blocked)
	if err != nil {
		return err
	}
	if blocked {
		return ErrCompositionNotDiscardable
	}
	if _, err = tx.Exec(ctx, `UPDATE draft SET state='discarded' WHERE id=$1`, draftID); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		reason = "draft discarded"
	}
	if _, err = tx.Exec(ctx, `UPDATE send_approval SET revoked_at=now(),revoked_reason=$2 WHERE draft_id=$1 AND revoked_at IS NULL`, draftID, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CancelQueuedComposition releases a failed admission without spending a request.
// It cannot cancel a claimed run or alter an existing message revision.
func (db *DB) CancelQueuedComposition(ctx context.Context, id int64) error {
	_, err := db.Exec(ctx, `UPDATE draft_composition SET status='failed',updated_at=now() WHERE draft_id=$1 AND status='queued' AND current_revision=0`, id)
	return err
}

func (db *DB) QueueComposition(ctx context.Context, s composition.Snapshot) (int64, error) {
	if err := s.Validate(); err != nil {
		return 0, err
	}
	reasons, err := db.CompositionStaleReasons(ctx, s)
	if err != nil {
		return 0, err
	}
	if len(reasons) > 0 {
		return 0, fmt.Errorf("stale composition inputs: %v", reasons)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return 0, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// One live email per plan, and per vacancy and intent: serialise admissions for the company,
	// then refuse if one already exists. Recomposing means discarding the existing one first.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(4711, $1::int)`, s.CompanyID); err != nil {
		return 0, err
	}
	var existing int64
	var existingState string
	err = tx.QueryRow(ctx, `SELECT c.draft_id, d.state FROM draft_composition c JOIN draft d ON d.id=c.draft_id
		 WHERE d.state<>'discarded'
		   AND NOT (c.status='failed' OR (c.status IN ('queued','running') AND COALESCE(c.lease_until,c.created_at+interval '10 minutes')<now()))
		   AND (c.plan_id=$1 OR ($2>0 AND c.snapshot->>'opportunity_id'=$2::text AND c.snapshot->>'intent'=$3))
		 ORDER BY c.draft_id DESC LIMIT 1`, s.PlanID, s.OpportunityID, string(s.Intent)).Scan(&existing, &existingState)
	if err == nil {
		return 0, &CompositionExistsError{DraftID: existing, Sent: existingState == "sent"}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	// An abandoned run is visible as failed, never automatically regenerated.
	if _, err = tx.Exec(ctx, `UPDATE draft_composition SET status='failed',updated_at=now() WHERE plan_id=$1 AND status IN ('queued','running') AND COALESCE(lease_until,created_at+interval '10 minutes')<now()`, s.PlanID); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO draft(company_id,contact_id,channel,subject,body,state,decision,decision_reason) VALUES($1,$2,'email','','','needs_manual','review_required','Phase 4 composition; sending disabled') RETURNING id`, s.CompanyID, s.Recipient.ContactID).Scan(&id)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO draft_composition(draft_id,plan_id,plan_revision,profile_revision_id,snapshot,input_hash,status) VALUES($1,$2,$3,$4,$5,$6,'queued')`, id, s.PlanID, s.PlanRevision, s.ProfileRevisionID, raw, s.InputHash)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (db *DB) GetComposition(ctx context.Context, id int64) (*ComposedDraft, error) {
	d := &ComposedDraft{ID: id, Attempts: []composition.Attempt{}}
	var raw []byte
	err := db.QueryRow(ctx, `SELECT snapshot,CASE WHEN status='running' AND lease_until<now() THEN 'failed' ELSE status END,current_revision FROM draft_composition WHERE draft_id=$1`, id).Scan(&raw, &d.Status, &d.Revision)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &d.Snapshot); err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT attempt FROM draft_composition_revision WHERE draft_id=$1 ORDER BY revision`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		var a composition.Attempt
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &a); err != nil {
			break
		}
		d.Attempts = append(d.Attempts, a)
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	d.StaleReasons, err = db.CompositionStaleReasons(ctx, d.Snapshot)
	if err != nil {
		return nil, err
	}
	// Provider routing and prompts are material inputs, never inferred from the
	// current company segment. Synthetic test providers are not live configuration.
	seenStages := map[string]bool{}
	for i := len(d.Attempts) - 1; i >= 0; i-- {
		a := d.Attempts[i]
		for stage, meta := range map[string]composition.Metadata{"compose": a.ComposeMetadata, "review": a.ReviewMetadata} {
			if seenStages[stage] {
				continue
			}
			if meta.Provider != "claude" && meta.Provider != "gemini" {
				continue
			}
			seenStages[stage] = true
			if meta.PromptHash != composition.CurrentPromptHash(stage, string(d.Snapshot.Intent)) {
				d.StaleReasons = append(d.StaleReasons, stage+"_prompt_changed")
			}
			current, configErr := composition.NewConfigured(strings.ToUpper(stage))
			if configErr != nil || current.Provider != meta.Provider || current.Model != meta.Model {
				d.StaleReasons = append(d.StaleReasons, stage+"_routing_changed")
			}
		}
		if len(seenStages) == 2 {
			break
		}
	}
	return d, err
}

type CompositionSummary struct {
	ID                               int64
	Company, Intent, Subject, Status string
}

func (db *DB) ListCompositions(ctx context.Context) ([]CompositionSummary, error) {
	rows, err := db.Query(ctx, `SELECT d.id,c.name,h.snapshot->>'intent',COALESCE(d.subject,''),CASE WHEN h.status='running' AND h.lease_until<now() THEN 'failed' ELSE h.status END FROM draft_composition h JOIN draft d ON d.id=h.draft_id JOIN company c ON c.id=d.company_id ORDER BY d.id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CompositionSummary{}
	for rows.Next() {
		var item CompositionSummary
		if err := rows.Scan(&item.ID, &item.Company, &item.Intent, &item.Subject, &item.Status); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ClaimComposition atomically owns the run and reserves all six possible calls
// (or one review for an edit). Unused capacity is released at completion/expiry.
func (db *DB) ClaimComposition(ctx context.Context, id int64, expectedRevision, calls, hourLimit, dayLimit int) (string, error) {
	if calls <= 0 || hourLimit < calls || dayLimit < calls {
		return "", fmt.Errorf("explicit request budget must cover this bounded run")
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes)
	tx, err := db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(73200422)`); err != nil {
		return "", err
	}
	var hour, day, reserved int
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE started_at>now()-interval '1 hour'),count(*) FROM composition_call WHERE started_at>now()-interval '24 hours'`).Scan(&hour, &day); err != nil {
		return "", err
	}
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(remaining),0) FROM composition_reservation WHERE expires_at>now()`).Scan(&reserved); err != nil {
		return "", err
	}
	if hour+reserved+calls > hourLimit || day+reserved+calls > dayLimit {
		return "", fmt.Errorf("composition request budget exhausted")
	}
	tag, err := tx.Exec(ctx, `UPDATE draft_composition SET status='running',lease_token=$2,lease_until=now()+interval '8 minutes',updated_at=now() WHERE draft_id=$1 AND current_revision=$3 AND status IN ('queued','reviewed','review_required','failed')`, id, token, expectedRevision)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", fmt.Errorf("draft changed or another run owns it")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO composition_reservation(draft_id,owner,remaining,expires_at) VALUES($1,$2,$3,now()+interval '8 minutes') ON CONFLICT(draft_id) DO UPDATE SET owner=$2,remaining=$3,expires_at=now()+interval '8 minutes'`, id, token, calls); err != nil {
		return "", err
	}
	return token, tx.Commit(ctx)
}

func (db *DB) ConsumeCompositionCall(ctx context.Context, id int64, owner, stage string) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE composition_reservation SET remaining=remaining-1 WHERE draft_id=$1 AND owner=$2 AND remaining>0 AND expires_at>now()`, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("request reservation unavailable")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO composition_call(draft_id,stage) VALUES($1,$2)`, id, stage); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (db *DB) RecordCompositionAttempt(ctx context.Context, id int64, owner string, a composition.Attempt) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var revision int
	if err = tx.QueryRow(ctx, `UPDATE draft_composition SET current_revision=current_revision+1,updated_at=now() WHERE draft_id=$1 AND lease_token=$2 AND status='running' AND lease_until>now() RETURNING current_revision`, id, owner).Scan(&revision); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO draft_composition_revision(draft_id,revision,content_hash,attempt) VALUES($1,$2,$3,$4)`, id, revision, composition.Hash(a.Email), raw); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('prospector.composition_write','on',true)`); err != nil {
		return err
	}
	if a.Email.Subject != "" && a.Email.Body != "" {
		_, err = tx.Exec(ctx, `UPDATE draft SET subject=$2,body=$3,edited_by_human=$4,decision='review_required',decision_reason='Full review history is in the Phase 4 draft workspace; sending disabled' WHERE id=$1`, id, a.Email.Subject, a.Email.Body, a.Source == "human")
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (db *DB) FinishComposition(ctx context.Context, id int64, owner, status string) error {
	if status != "reviewed" && status != "review_required" && status != "failed" {
		return fmt.Errorf("invalid completion state")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE draft_composition SET status=$3,lease_token='',lease_until=NULL,updated_at=now() WHERE draft_id=$1 AND lease_token=$2`, id, owner, status); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM composition_reservation WHERE draft_id=$1 AND owner=$2`, id, owner); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
