package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/plan"
)

// LegacyIntentUnknown is a first-class answer, not a failure to answer. An old thread whose
// purpose cannot be established stays unknown, and no v2 follow-up is offered for it.
const LegacyIntentUnknown = "unknown"

// LegacyThread is one pre-v2 sent message, with whatever a person has since concluded about it.
// Every field that was never recorded at the time stays empty rather than being reconstructed.
type LegacyThread struct {
	SendLogID     int64
	DraftID       *int64
	CompanyID     int64
	CompanyName   string
	ToValue       string
	SentAt        time.Time
	Subject       string
	Body          string
	RepliedAt     *time.Time
	BouncedAt     *time.Time
	BounceReason  string
	Followup1Sent *time.Time
	Followup2Sent *time.Time
	// Classification is nil until a person has said what this thread was for.
	Classification *LegacyClassification
}

// Classified reports whether a v2 follow-up could even be considered for this thread.
func (t LegacyThread) Classified() bool {
	return t.Classification != nil && t.Classification.Intent != LegacyIntentUnknown
}

// LegacyClassification is a person's statement about an old thread, with the basis they used.
type LegacyClassification struct {
	ID                 int64
	SendLogID          int64
	Intent             string
	OpportunityID      *int64
	RecipientContactID *int64
	TargetFamilies     []plan.TargetFamily
	Basis              string
	ClassifiedBy       string
	ClassifiedAt       time.Time
}

// ValidLegacyIntent accepts the three real intents plus unknown. Nothing else is storable, and
// nothing infers one of these from company segment, the old application flag or message text.
func ValidLegacyIntent(intent string) bool {
	switch intent {
	case string(plan.SpecificJobApplication), string(plan.SpeculativeJobEnquiry),
		string(plan.ServiceOutreach), LegacyIntentUnknown:
		return true
	}
	return false
}

// ClassifyLegacyThread records what a person concluded. A previous classification is superseded
// rather than overwritten, so the record shows what was believed and when.
func (db *DB) ClassifyLegacyThread(ctx context.Context, c LegacyClassification) (*LegacyClassification, error) {
	if !ValidLegacyIntent(c.Intent) {
		return nil, fmt.Errorf("unknown legacy intent %q", c.Intent)
	}
	if strings.TrimSpace(c.Basis) == "" {
		return nil, fmt.Errorf("a classification must record what it was based on")
	}
	if strings.TrimSpace(c.ClassifiedBy) == "" {
		return nil, fmt.Errorf("a classification must record who made it")
	}
	// An unknown answer pins nothing. Allowing an opportunity or recipient alongside it would
	// be exactly the reconstructed metadata this phase is meant to avoid.
	if c.Intent == LegacyIntentUnknown {
		c.OpportunityID, c.RecipientContactID, c.TargetFamilies = nil, nil, nil
	}
	families := c.TargetFamilies
	if families == nil {
		families = []plan.TargetFamily{}
	}
	raw, err := json.Marshal(families)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx,
		`UPDATE legacy_thread_intent SET superseded_at=now() WHERE send_log_id=$1 AND superseded_at IS NULL`,
		c.SendLogID); err != nil {
		return nil, err
	}
	var stored LegacyClassification
	var familiesRaw []byte
	err = tx.QueryRow(ctx,
		`INSERT INTO legacy_thread_intent(send_log_id,intent,opportunity_id,recipient_contact_id,target_families,basis,classified_by)
		 VALUES($1,$2,$3,$4,$5,$6,$7)
		 RETURNING id,send_log_id,intent,opportunity_id,recipient_contact_id,target_families,basis,classified_by,classified_at`,
		c.SendLogID, c.Intent, c.OpportunityID, c.RecipientContactID, raw, c.Basis, c.ClassifiedBy).
		Scan(&stored.ID, &stored.SendLogID, &stored.Intent, &stored.OpportunityID,
			&stored.RecipientContactID, &familiesRaw, &stored.Basis, &stored.ClassifiedBy, &stored.ClassifiedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(familiesRaw, &stored.TargetFamilies); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &stored, nil
}

// LegacyClassificationHistory returns every classification ever recorded for a thread, newest
// first, including superseded ones.
func (db *DB) LegacyClassificationHistory(ctx context.Context, sendLogID int64) ([]LegacyClassification, error) {
	rows, err := db.Query(ctx,
		`SELECT id,send_log_id,intent,opportunity_id,recipient_contact_id,target_families,basis,classified_by,classified_at
		   FROM legacy_thread_intent WHERE send_log_id=$1 ORDER BY classified_at DESC, id DESC`, sendLogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := []LegacyClassification{}
	for rows.Next() {
		var c LegacyClassification
		var familiesRaw []byte
		if err := rows.Scan(&c.ID, &c.SendLogID, &c.Intent, &c.OpportunityID, &c.RecipientContactID,
			&familiesRaw, &c.Basis, &c.ClassifiedBy, &c.ClassifiedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(familiesRaw, &c.TargetFamilies); err != nil {
			return nil, err
		}
		history = append(history, c)
	}
	return history, rows.Err()
}

// ListLegacyThreads returns pre-v2 sent messages with their current classification. The legacy
// subject and body are read straight from the original draft row and are never rewritten.
func (db *DB) ListLegacyThreads(ctx context.Context, onlyUnclassified bool, limit int) ([]LegacyThread, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	filter := ""
	if onlyUnclassified {
		filter = ` AND i.id IS NULL`
	}
	rows, err := db.Query(ctx, `
		SELECT s.id, s.draft_id, s.company_id, co.name, s.to_value, s.sent_at,
		       COALESCE(d.subject,''), COALESCE(d.body,''),
		       s.replied_at, s.bounced_at, COALESCE(s.bounce_reason,''),
		       s.followup_1_sent_at, s.followup_2_sent_at,
		       i.id, i.intent, i.opportunity_id, i.recipient_contact_id, i.target_families,
		       i.basis, i.classified_by, i.classified_at
		  FROM send_log s
		  JOIN company co ON co.id = s.company_id
		  LEFT JOIN draft d ON d.id = s.draft_id
		  LEFT JOIN legacy_thread_intent i ON i.send_log_id = s.id AND i.superseded_at IS NULL
		 WHERE NOT EXISTS(SELECT 1 FROM draft_composition c WHERE c.draft_id = s.draft_id)`+filter+`
		 ORDER BY s.sent_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	threads := []LegacyThread{}
	for rows.Next() {
		var t LegacyThread
		var classID *int64
		var intent, basis, classifiedBy *string
		var opportunityID, contactID *int64
		var familiesRaw []byte
		var classifiedAt *time.Time
		if err := rows.Scan(&t.SendLogID, &t.DraftID, &t.CompanyID, &t.CompanyName, &t.ToValue, &t.SentAt,
			&t.Subject, &t.Body, &t.RepliedAt, &t.BouncedAt, &t.BounceReason,
			&t.Followup1Sent, &t.Followup2Sent,
			&classID, &intent, &opportunityID, &contactID, &familiesRaw,
			&basis, &classifiedBy, &classifiedAt); err != nil {
			return nil, err
		}
		if classID != nil && intent != nil {
			c := &LegacyClassification{ID: *classID, SendLogID: t.SendLogID, Intent: *intent,
				OpportunityID: opportunityID, RecipientContactID: contactID}
			if basis != nil {
				c.Basis = *basis
			}
			if classifiedBy != nil {
				c.ClassifiedBy = *classifiedBy
			}
			if classifiedAt != nil {
				c.ClassifiedAt = *classifiedAt
			}
			if len(familiesRaw) > 0 {
				if err := json.Unmarshal(familiesRaw, &c.TargetFamilies); err != nil {
					return nil, err
				}
			}
			t.Classification = c
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

// LegacyDraft is a pre-v2 draft that was never sent. It carries no snapshot, no profile
// revision, no opportunity and no review, which is precisely why it cannot enter the v2 path.
type LegacyDraft struct {
	ID          int64
	CompanyID   int64
	CompanyName string
	Subject     string
	Body        string
	State       string
	Decision    string
	CreatedAt   time.Time
}

// ListLegacyUnsentDrafts returns drafts with no v2 composition that were never sent. They exist
// to be read, and to be replaced by a deliberate v2 composition from a current contact plan.
func (db *DB) ListLegacyUnsentDrafts(ctx context.Context, limit int) ([]LegacyDraft, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := db.Query(ctx, `
		SELECT d.id, d.company_id, c.name, COALESCE(d.subject,''), d.body, d.state, COALESCE(d.decision,'')
		     , d.created_at
		  FROM draft d JOIN company c ON c.id = d.company_id
		 WHERE NOT EXISTS(SELECT 1 FROM draft_composition x WHERE x.draft_id = d.id)
		   AND d.state NOT IN ('sent','discarded')
		 ORDER BY d.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	drafts := []LegacyDraft{}
	for rows.Next() {
		var d LegacyDraft
		if err := rows.Scan(&d.ID, &d.CompanyID, &d.CompanyName, &d.Subject, &d.Body,
			&d.State, &d.Decision, &d.CreatedAt); err != nil {
			return nil, err
		}
		drafts = append(drafts, d)
	}
	return drafts, rows.Err()
}

// IsLegacyDraft reports whether a draft id belongs to the pre-v2 pipeline. This is the single
// definition used by the UI and by the guard on the v2 routes: a draft is v2 if and only if it
// has a composition row.
func (db *DB) IsLegacyDraft(ctx context.Context, draftID int64) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM draft WHERE id=$1) AND NOT EXISTS(SELECT 1 FROM draft_composition WHERE draft_id=$1)`,
		draftID).Scan(&exists)
	return exists, err
}
