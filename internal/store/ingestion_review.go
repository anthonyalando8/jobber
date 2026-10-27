package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/jackc/pgx/v5"
)

// HeldListing is one listing a source published that ingestion could not store as-is, with the
// reason and the listing exactly as it arrived.
type HeldListing struct {
	ID           int64
	Source       string
	Reason       string
	Discovery    opportunity.DiscoveredOpportunity
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	Observations int
	ResolvedAt   *time.Time
}

// HeldListingFilter selects held listings. Recent limits to listings the source published again in
// the last seven days, which is what "still happening" means: a listing held back before a fix and
// never seen since stops counting once the fix works.
type HeldListingFilter struct {
	Source, Reason string
	Recent         bool
	// Resolved includes listings an operator has already resolved.
	Resolved bool
}

const heldWhere = ` WHERE ($1='' OR source=$1) AND ($2='' OR reason=$2)
 AND (NOT $3 OR last_seen_at > now()-interval '7 days') AND ($4 OR resolved_at IS NULL)`

// CountHeldListings counts held listings matching f.
func (db *DB) CountHeldListings(ctx context.Context, f HeldListingFilter) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM ingestion_review`+heldWhere, f.Source, f.Reason, f.Recent, f.Resolved).Scan(&n)
	return n, err
}

// ListHeldListings returns held listings, most recently seen first.
func (db *DB) ListHeldListings(ctx context.Context, f HeldListingFilter, limit, offset int) ([]HeldListing, error) {
	rows, err := db.Query(ctx, `SELECT id,source,reason,discovery,first_seen_at,last_seen_at,observations,resolved_at FROM ingestion_review`+heldWhere+
		` ORDER BY last_seen_at DESC, id DESC LIMIT $5 OFFSET $6`, f.Source, f.Reason, f.Recent, f.Resolved, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HeldListing{}
	for rows.Next() {
		var h HeldListing
		var raw []byte
		if err = rows.Scan(&h.ID, &h.Source, &h.Reason, &raw, &h.FirstSeenAt, &h.LastSeenAt, &h.Observations, &h.ResolvedAt); err != nil {
			return nil, err
		}
		// A row that does not decode still shows its source and reason.
		_ = json.Unmarshal(raw, &h.Discovery)
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetHeldListing returns one held listing.
func (db *DB) GetHeldListing(ctx context.Context, id int64) (*HeldListing, error) {
	var h HeldListing
	var raw []byte
	err := db.QueryRow(ctx, `SELECT id,source,reason,discovery,first_seen_at,last_seen_at,observations,resolved_at FROM ingestion_review WHERE id=$1`, id).
		Scan(&h.ID, &h.Source, &h.Reason, &raw, &h.FirstSeenAt, &h.LastSeenAt, &h.Observations, &h.ResolvedAt)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &h.Discovery); err != nil {
		return nil, fmt.Errorf("held listing %d does not decode: %w", id, err)
	}
	return &h, nil
}

// CompanyCandidates are the existing companies a held listing's employer name or domain matched:
// the ones the resolver could not choose between.
func (db *DB) CompanyCandidates(ctx context.Context, d opportunity.DiscoveredOpportunity) ([]Company, error) {
	rows, err := db.Query(ctx, `SELECT id,name,domain,source FROM company
 WHERE lower(regexp_replace(trim(name),'\s+',' ','g'))=$1 OR ($2<>'' AND lower(domain)=lower($2)) ORDER BY id LIMIT 10`,
		normalizeCompany(d.CompanyName), d.DomainCandidate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Company{}
	for rows.Next() {
		var c Company
		if err = rows.Scan(&c.ID, &c.Name, &c.Domain, &c.Source); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ErrAliasConflict means the employer name is already attached to a different company.
var ErrAliasConflict = errors.New("this employer name is already attached to a different company")

// AttachHeldListing records the operator's decision that a held listing's employer is companyID,
// stores the listing under that company and marks the held record resolved. The decision is kept as
// a name alias, so the same source's later runs attach the same employer name without holding it
// back again. No company is merged or renamed.
func (db *DB) AttachHeldListing(ctx context.Context, heldID, companyID int64, decidedBy string) (IngestionResult, error) {
	held, err := db.GetHeldListing(ctx, heldID)
	if err != nil {
		return IngestionResult{}, err
	}
	key := normalizeCompany(held.Discovery.CompanyName)
	if key == "" {
		return IngestionResult{}, fmt.Errorf("this listing names no employer, so there is nothing to attach")
	}
	var existing int64
	err = db.QueryRow(ctx, `SELECT company_id FROM company_alias WHERE normalized_name=$1`, key).Scan(&existing)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err = db.Exec(ctx, `INSERT INTO company_alias(normalized_name,company_id,decided_by) VALUES($1,$2,$3)`, key, companyID, decidedBy); err != nil {
			return IngestionResult{}, err
		}
	case err != nil:
		return IngestionResult{}, err
	case existing != companyID:
		return IngestionResult{}, ErrAliasConflict
	}
	result, err := db.IngestOpportunity(ctx, held.Discovery)
	if err != nil {
		return result, err
	}
	if result.ReviewReason != "" {
		return result, fmt.Errorf("the listing is still held back: %s", result.ReviewReason)
	}
	if err = db.SaveOpportunityAccess(ctx, result.ID, result.Revision, opportunity.ClassifyAccess(held.Discovery)); err != nil {
		return result, err
	}
	_, err = db.Exec(ctx, `UPDATE ingestion_review SET resolved_at=now() WHERE id=$1`, heldID)
	return result, err
}
