package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Suppressing an opportunity is how a posting that has expired, been filled or been taken down
// leaves the working queues without being deleted. It is deliberately not a delete: the posting
// was real, it may have been assessed, planned against or written about, and a closed vacancy at
// a company is itself a fact worth keeping.
//
// The live flag is opportunity.selection_status='ignored', because that is what the browse
// priority filter, the dashboard queue and contact-discovery eligibility already read. Writing
// the decision therefore takes effect across the whole system without changing those queries.
// What this adds is the record of why, by whom and on what evidence, and a way back.

// Suppression reasons: what is true about the posting.
const (
	SuppressExpired           = "expired"
	SuppressFilled            = "filled"
	SuppressNoLongerHiring    = "no_longer_hiring"
	SuppressWithdrawnBySource = "withdrawn_by_source"
	SuppressNotPursuing       = "not_pursuing"
	SuppressDuplicate         = "duplicate"
)

// Suppression bases: how it is known. Kept separate from the reason, because a person deciding a
// role is gone and a published date having passed are different grades of evidence, and someone
// asking later why this was dropped needs to be able to tell them apart.
const (
	BasisOperator         = "operator"
	BasisSourceClosed     = "source_closed"
	BasisExpiryPassed     = "expiry_date_passed"
	BasisAbsentFromSource = "absent_from_source"
)

// SuppressionReasons are the conclusions that may be recorded, in the order the UI offers them.
var SuppressionReasons = []string{
	SuppressExpired, SuppressFilled, SuppressNoLongerHiring,
	SuppressWithdrawnBySource, SuppressNotPursuing, SuppressDuplicate,
}

// ErrNotSuppressed is returned when reinstating something that is not currently suppressed. It is
// a distinct error so the UI can say so rather than report a failure.
var ErrNotSuppressed = errors.New("opportunity is not currently suppressed")

// ErrAlreadySuppressed is returned when a suppression already stands. Suppressing twice would
// lose the first decision's reason and author, so it is refused rather than overwritten.
var ErrAlreadySuppressed = errors.New("opportunity is already suppressed")

// OpportunitySuppression is one recorded decision, live or superseded.
type OpportunitySuppression struct {
	ID                     int64
	OpportunityID          int64
	OpportunityRevision    int
	Reason, Basis, Note    string
	DecidedBy              string
	Evidence               map[string]any
	RestoreSelectionStatus string
	DecidedAt              time.Time
	SupersededAt           *time.Time
	SupersededBy           string
	SupersedeNote          string
}

// Live reports whether this decision is the one currently in force.
func (s OpportunitySuppression) Live() bool { return s.SupersededAt == nil }

// Automatic reports whether the sweep made this decision rather than a person.
func (s OpportunitySuppression) Automatic() bool { return s.Basis != BasisOperator }

func validSuppression(reason, basis string) error {
	known := false
	for _, candidate := range SuppressionReasons {
		known = known || candidate == reason
	}
	if !known {
		return fmt.Errorf("unknown suppression reason %q", reason)
	}
	switch basis {
	case BasisOperator, BasisSourceClosed, BasisExpiryPassed, BasisAbsentFromSource:
		return nil
	}
	return fmt.Errorf("unknown suppression basis %q", basis)
}

// SuppressOpportunity records a suppression and takes the opportunity out of the working queues
// in one transaction, so a decision can never be recorded without taking effect, or take effect
// without being recorded.
func (db *DB) SuppressOpportunity(ctx context.Context, opportunityID int64, reason, basis, note, decidedBy string, evidence map[string]any) (OpportunitySuppression, error) {
	if err := validSuppression(reason, basis); err != nil {
		return OpportunitySuppression{}, err
	}
	if decidedBy == "" {
		return OpportunitySuppression{}, errors.New("a suppression must record who decided it")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return OpportunitySuppression{}, err
	}
	defer tx.Rollback(ctx)
	record, err := suppressOpportunity(ctx, tx, opportunityID, reason, basis, note, decidedBy, evidence)
	if err != nil {
		return record, err
	}
	return record, tx.Commit(ctx)
}

func suppressOpportunity(ctx context.Context, tx pgx.Tx, opportunityID int64, reason, basis, note, decidedBy string, evidence map[string]any) (OpportunitySuppression, error) {
	// Lock the opportunity row, so an operator and the sweep cannot both write a first
	// suppression and have one of them silently lose its reason.
	var revision int
	var selection string
	err := tx.QueryRow(ctx,
		`SELECT current_revision, selection_status FROM opportunity WHERE id=$1 FOR UPDATE`,
		opportunityID).Scan(&revision, &selection)
	if errors.Is(err, pgx.ErrNoRows) {
		return OpportunitySuppression{}, fmt.Errorf("opportunity %d does not exist", opportunityID)
	}
	if err != nil {
		return OpportunitySuppression{}, err
	}
	var live bool
	if err = tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM opportunity_suppression WHERE opportunity_id=$1 AND superseded_at IS NULL)`,
		opportunityID).Scan(&live); err != nil {
		return OpportunitySuppression{}, err
	}
	if live {
		return OpportunitySuppression{}, ErrAlreadySuppressed
	}
	if evidence == nil {
		evidence = map[string]any{}
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return OpportunitySuppression{}, err
	}
	record := OpportunitySuppression{
		OpportunityID: opportunityID, OpportunityRevision: revision, Reason: reason,
		Basis: basis, Note: note, DecidedBy: decidedBy, Evidence: evidence,
		RestoreSelectionStatus: selection,
	}
	if err = tx.QueryRow(ctx,
		`INSERT INTO opportunity_suppression
		   (opportunity_id,opportunity_revision,reason,basis,note,decided_by,evidence,restore_selection_status)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,decided_at`,
		opportunityID, revision, reason, basis, note, decidedBy, raw, selection,
	).Scan(&record.ID, &record.DecidedAt); err != nil {
		return OpportunitySuppression{}, err
	}
	// The live flag every existing queue already consults.
	if _, err = tx.Exec(ctx,
		`UPDATE opportunity SET selection_status='ignored', updated_at=now() WHERE id=$1`,
		opportunityID); err != nil {
		return OpportunitySuppression{}, err
	}
	return record, nil
}

// ReinstateOpportunity lifts a suppression, restoring the selection status the opportunity held
// before it. The row is superseded rather than deleted, so the history of having been dropped and
// put back survives — and the sweep reads that history to avoid re-suppressing on reasoning a
// person has already overruled.
func (db *DB) ReinstateOpportunity(ctx context.Context, opportunityID int64, reinstatedBy, note string) (OpportunitySuppression, error) {
	if reinstatedBy == "" {
		return OpportunitySuppression{}, errors.New("a reinstatement must record who decided it")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return OpportunitySuppression{}, err
	}
	defer tx.Rollback(ctx)

	var record OpportunitySuppression
	var raw []byte
	err = tx.QueryRow(ctx,
		`UPDATE opportunity_suppression
		    SET superseded_at=now(), superseded_by=$2, supersede_note=$3
		  WHERE opportunity_id=$1 AND superseded_at IS NULL
		  RETURNING id,opportunity_revision,reason,basis,note,decided_by,evidence,
		            restore_selection_status,decided_at,superseded_at,superseded_by,supersede_note`,
		opportunityID, reinstatedBy, note,
	).Scan(&record.ID, &record.OpportunityRevision, &record.Reason, &record.Basis, &record.Note,
		&record.DecidedBy, &raw, &record.RestoreSelectionStatus, &record.DecidedAt,
		&record.SupersededAt, &record.SupersededBy, &record.SupersedeNote)
	if errors.Is(err, pgx.ErrNoRows) {
		return OpportunitySuppression{}, ErrNotSuppressed
	}
	if err != nil {
		return OpportunitySuppression{}, err
	}
	record.OpportunityID = opportunityID
	if err = json.Unmarshal(raw, &record.Evidence); err != nil {
		return OpportunitySuppression{}, err
	}
	// Restore what was there, not a guess: a posting that was 'selected' before someone marked it
	// filled by mistake comes back selected.
	restore := record.RestoreSelectionStatus
	if restore == "" || restore == "ignored" {
		restore = "new"
	}
	if _, err = tx.Exec(ctx,
		`UPDATE opportunity SET selection_status=$2, updated_at=now() WHERE id=$1`,
		opportunityID, restore); err != nil {
		return OpportunitySuppression{}, err
	}
	return record, tx.Commit(ctx)
}

// OpportunitySuppressionHistory returns every decision about one opportunity, newest first. The
// one in force, if any, is the row whose Live reports true.
func (db *DB) OpportunitySuppressionHistory(ctx context.Context, opportunityID int64) ([]OpportunitySuppression, error) {
	rows, err := db.Query(ctx,
		`SELECT id,opportunity_id,opportunity_revision,reason,basis,note,decided_by,evidence,
		        restore_selection_status,decided_at,superseded_at,superseded_by,supersede_note
		   FROM opportunity_suppression WHERE opportunity_id=$1
		  ORDER BY decided_at DESC, id DESC`, opportunityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpportunitySuppression{}
	for rows.Next() {
		var record OpportunitySuppression
		var raw []byte
		if err = rows.Scan(&record.ID, &record.OpportunityID, &record.OpportunityRevision,
			&record.Reason, &record.Basis, &record.Note, &record.DecidedBy, &raw,
			&record.RestoreSelectionStatus, &record.DecidedAt, &record.SupersededAt,
			&record.SupersededBy, &record.SupersedeNote); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &record.Evidence); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// CurrentOpportunitySuppression returns the suppression in force, or nil when there is none.
func (db *DB) CurrentOpportunitySuppression(ctx context.Context, opportunityID int64) (*OpportunitySuppression, error) {
	history, err := db.OpportunitySuppressionHistory(ctx, opportunityID)
	if err != nil {
		return nil, err
	}
	for i := range history {
		if history[i].Live() {
			return &history[i], nil
		}
	}
	return nil, nil
}

// SuppressionChoice is one reason offered in the UI, with the short explanation that keeps the
// vocabulary from being guessed at. "Expired" and "no longer hiring" are not the same claim, and
// a list of bare slugs invites an operator to pick whichever sounds closest.
type SuppressionChoice struct{ Value, Label, Help string }

// SuppressionReasonChoices are the manual reasons, in the order the form offers them.
func SuppressionReasonChoices() []SuppressionChoice {
	return []SuppressionChoice{
		{SuppressExpired, "Expired", "The advertised closing date has passed."},
		{SuppressFilled, "Filled", "The employer says the role is taken."},
		{SuppressNoLongerHiring, "No longer hiring", "The posting is gone or the employer has stopped recruiting for it."},
		{SuppressWithdrawnBySource, "Withdrawn by source", "The job board or the employer took the posting down."},
		{SuppressNotPursuing, "Not pursuing", "Still open, but a deliberate decision not to apply."},
		{SuppressDuplicate, "Duplicate", "The same vacancy is already tracked under another posting."},
	}
}

// ReasonLabel is the conclusion in words, for a page rather than a column.
func (s OpportunitySuppression) ReasonLabel() string {
	for _, choice := range SuppressionReasonChoices() {
		if choice.Value == s.Reason {
			return choice.Label
		}
	}
	return s.Reason
}

// BasisLabel says how the decision was reached, in the terms that matter when judging whether to
// trust it: who or what concluded this, and from what.
func (s OpportunitySuppression) BasisLabel() string {
	switch s.Basis {
	case BasisOperator:
		return "decided by an operator"
	case BasisSourceClosed:
		return "the source marked this posting closed"
	case BasisExpiryPassed:
		return "the published closing date has passed"
	case BasisAbsentFromSource:
		return "the posting stopped appearing in a feed that is still being read"
	}
	return s.Basis
}
