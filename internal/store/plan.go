package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/jackc/pgx/v5"
)

// ContactPlan is the persisted decision about why a recipient is being contacted. Selecting a
// plan authorizes future draft generation only. It never authorizes sending.
type ContactPlan struct {
	ID        int64  `json:"id"`
	CompanyID int64  `json:"company_id"`
	Intent    string `json:"intent"`
	// OpportunityID is the applied-for vacancy, set only for a specific application, always
	// with the exact revision applied to.
	OpportunityID       *int64 `json:"opportunity_id,omitempty"`
	OpportunityRevision *int   `json:"opportunity_revision,omitempty"`
	// ProvenanceOpportunityID is the listing that led us here. It is never the role claimed.
	ProvenanceOpportunityID *int64              `json:"provenance_opportunity_id,omitempty"`
	MatchClassification     string              `json:"match_classification"`
	TargetFamilies          []plan.TargetFamily `json:"target_families"`
	RecipientContactID      int64               `json:"recipient_contact_id"`
	RecipientEmail          string              `json:"recipient_email"`
	RecipientName           string              `json:"recipient_name"`
	RecipientRelevance      string              `json:"recipient_relevance"`
	RecipientReason         string              `json:"recipient_reason"`
	ProfileRevisionID       int64               `json:"profile_revision_id"`
	PlanKey                 string              `json:"plan_key"`
	Revision                int                 `json:"revision"`
	SelectionOrigin         string              `json:"selection_origin"`
	Status                  string              `json:"status"`
	RequiresReview          bool                `json:"requires_review"`
	Reasons                 explain.Reasons     `json:"reasons"`
	Selection               plan.Selection      `json:"selection"`
	CreatedAt               time.Time           `json:"created_at"`
	UpdatedAt               time.Time           `json:"updated_at"`
	// OpportunityTitle and ProvenanceTitle are read-side conveniences for the UI.
	OpportunityTitle string `json:"opportunity_title,omitempty"`
	ProvenanceTitle  string `json:"provenance_title,omitempty"`
}

// PlanKey identifies the campaign: company plus vacancy-or-none plus intent. One live plan may
// exist per key; changing the recipient is a revision of that plan, not a second campaign.
func PlanKey(companyID int64, opportunityID *int64, intent string) string {
	vacancy := "none"
	if opportunityID != nil {
		vacancy = fmt.Sprintf("%d", *opportunityID)
	}
	return fmt.Sprintf("%d|%s|%s", companyID, vacancy, intent)
}

// CreateContactPlan records a plan and its immutable first revision in one transaction. The
// unrelated-role invariant is additionally enforced by a database CHECK, so a caller bug cannot
// turn an unrelated role into an application.
func (db *DB) CreateContactPlan(ctx context.Context, p ContactPlan) (*ContactPlan, error) {
	if !plan.Intent(p.Intent).Valid() {
		return nil, fmt.Errorf("invalid intent %q", p.Intent)
	}
	p.PlanKey = PlanKey(p.CompanyID, p.OpportunityID, p.Intent)
	p.Revision = 1
	families, err := json.Marshal(p.TargetFamilies)
	if err != nil {
		return nil, err
	}
	reasons, err := json.Marshal(p.Reasons)
	if err != nil {
		return nil, err
	}
	selection, err := json.Marshal(p.Selection)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx,
		`INSERT INTO contact_plan (company_id, intent, opportunity_id, opportunity_revision,
		        provenance_opportunity_id, match_classification, target_families,
		        recipient_contact_id, recipient_email, recipient_name, recipient_relevance,
		        recipient_reason, profile_revision_id, plan_key, revision, selection_origin,
		        status, requires_review, reasons, selection)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,1,$15,$16,$17,$18,$19)
		 RETURNING id, created_at, updated_at`,
		p.CompanyID, p.Intent, p.OpportunityID, p.OpportunityRevision, p.ProvenanceOpportunityID,
		p.MatchClassification, families, p.RecipientContactID, p.RecipientEmail, p.RecipientName,
		p.RecipientRelevance, p.RecipientReason, p.ProfileRevisionID, p.PlanKey,
		p.SelectionOrigin, p.Status, p.RequiresReview, reasons, selection).
		Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err = insertPlanRevision(ctx, tx, p); err != nil {
		return nil, err
	}
	return &p, tx.Commit(ctx)
}

// insertPlanRevision freezes the recipient and the whole plan for this revision. Later sending
// must read this snapshot rather than reselecting a current best contact.
func insertPlanRevision(ctx context.Context, tx pgx.Tx, p ContactPlan) error {
	snapshot, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO contact_plan_revision (plan_id, revision, snapshot) VALUES ($1,$2,$3)`,
		p.ID, p.Revision, snapshot)
	return err
}

// UpdatePlanRecipient changes who is contacted. It increments the plan revision and writes a
// new frozen snapshot, so an already-reviewed plan can never quietly point at someone else.
func (db *DB) UpdatePlanRecipient(ctx context.Context, planID, contactID int64, email, name, relevance, reason string) (*ContactPlan, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var companyID int64
	if err = tx.QueryRow(ctx, `SELECT company_id FROM contact_plan WHERE id = $1 FOR UPDATE`, planID).Scan(&companyID); err != nil {
		return nil, err
	}
	var contactCompany int64
	if err = tx.QueryRow(ctx, `SELECT company_id FROM contact WHERE id = $1`, contactID).Scan(&contactCompany); err != nil {
		return nil, err
	}
	if contactCompany != companyID {
		return nil, fmt.Errorf("contact %d does not belong to the plan's company", contactID)
	}
	_, err = tx.Exec(ctx,
		`UPDATE contact_plan SET recipient_contact_id = $2, recipient_email = $3, recipient_name = $4,
		        recipient_relevance = $5, recipient_reason = $6, revision = revision + 1,
		        requires_review = true, updated_at = now()
		 WHERE id = $1`, planID, contactID, email, name, relevance, reason)
	if err != nil {
		return nil, err
	}
	updated, err := readPlan(ctx, tx, planID)
	if err != nil {
		return nil, err
	}
	if err = insertPlanRevision(ctx, tx, *updated); err != nil {
		return nil, err
	}
	return updated, tx.Commit(ctx)
}

// SetContactPlanStatus pauses, closes or reselects a plan. Plans are closed, never deleted.
func (db *DB) SetContactPlanStatus(ctx context.Context, planID int64, status string) error {
	switch status {
	case "suggested", "selected", "paused", "closed":
	default:
		return fmt.Errorf("invalid plan status %q", status)
	}
	_, err := db.Exec(ctx, `UPDATE contact_plan SET status = $2, updated_at = now() WHERE id = $1`, planID, status)
	return err
}

const planColumns = `id, company_id, intent, opportunity_id, opportunity_revision,
 provenance_opportunity_id, match_classification, target_families, recipient_contact_id,
 recipient_email, recipient_name, recipient_relevance, recipient_reason, profile_revision_id,
 plan_key, revision, selection_origin, status, requires_review, reasons, selection,
 created_at, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPlan(row rowScanner) (*ContactPlan, error) {
	var p ContactPlan
	var families, reasons, selection []byte
	err := row.Scan(&p.ID, &p.CompanyID, &p.Intent, &p.OpportunityID, &p.OpportunityRevision,
		&p.ProvenanceOpportunityID, &p.MatchClassification, &families, &p.RecipientContactID,
		&p.RecipientEmail, &p.RecipientName, &p.RecipientRelevance, &p.RecipientReason,
		&p.ProfileRevisionID, &p.PlanKey, &p.Revision, &p.SelectionOrigin, &p.Status,
		&p.RequiresReview, &reasons, &selection, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(families, &p.TargetFamilies); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(reasons, &p.Reasons); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(selection, &p.Selection); err != nil {
		return nil, err
	}
	return &p, nil
}

func readPlan(ctx context.Context, tx pgx.Tx, id int64) (*ContactPlan, error) {
	return scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM contact_plan WHERE id = $1`, id))
}

func (db *DB) GetContactPlan(ctx context.Context, id int64) (*ContactPlan, error) {
	return scanPlan(db.QueryRow(ctx, `SELECT `+planColumns+` FROM contact_plan WHERE id = $1`, id))
}

// ListContactPlansForCompany returns every plan, including paused and closed ones, so
// alternative purposes at one company stay visible.
func (db *DB) ListContactPlansForCompany(ctx context.Context, companyID int64) ([]*ContactPlan, error) {
	rows, err := db.Query(ctx, `SELECT `+planColumns+` FROM contact_plan WHERE company_id = $1
	 ORDER BY (status IN ('selected','suggested')) DESC, updated_at DESC`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := []*ContactPlan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, p := range plans {
		if p.OpportunityID != nil {
			_ = db.QueryRow(ctx, `SELECT title FROM opportunity WHERE id = $1`, *p.OpportunityID).Scan(&p.OpportunityTitle)
		}
		if p.ProvenanceOpportunityID != nil {
			_ = db.QueryRow(ctx, `SELECT title FROM opportunity WHERE id = $1`, *p.ProvenanceOpportunityID).Scan(&p.ProvenanceTitle)
		}
	}
	return plans, nil
}

// ContactPlanRevisions returns the immutable snapshots of a plan, newest first.
func (db *DB) ContactPlanRevisions(ctx context.Context, planID int64) ([]json.RawMessage, error) {
	rows, err := db.Query(ctx, `SELECT snapshot FROM contact_plan_revision WHERE plan_id = $1 ORDER BY revision DESC`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

// CountContactPlans reports how many plans exist per intent, for the completion report.
func (db *DB) CountContactPlans(ctx context.Context) (map[string]int, error) {
	rows, err := db.Query(ctx, `SELECT intent, count(*) FROM contact_plan GROUP BY intent ORDER BY intent`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var intent string
		var n int
		if err = rows.Scan(&intent, &n); err != nil {
			return nil, err
		}
		counts[intent] = n
	}
	return counts, rows.Err()
}
