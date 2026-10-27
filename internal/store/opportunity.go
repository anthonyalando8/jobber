package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/jackc/pgx/v5"
)

type IngestionResult struct {
	ID           int64
	Revision     int
	Created      bool
	ReviewReason string
}
type Opportunity struct {
	ID           int64
	CompanyID    int64
	CompanyName  string
	Revision     int
	Facts        opportunity.DiscoveredOpportunity
	DiscoveredAt time.Time
	LastSeenAt   time.Time
	UpdatedAt    time.Time
}

// PostgreSQL owns identity. The short transaction lock is intentionally simple
// for this personal tool and also serializes concurrent source imports.
func (db *DB) IngestOpportunity(ctx context.Context, d opportunity.DiscoveredOpportunity) (IngestionResult, error) {
	d, err := opportunity.Normalize(d)
	if err != nil {
		return IngestionResult{}, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return IngestionResult{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(73201918)`); err != nil {
		return IngestionResult{}, err
	}
	result, err := ingestOpportunity(ctx, tx, d)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func ingestionReview(ctx context.Context, tx pgx.Tx, d opportunity.DiscoveredOpportunity, reason string) (IngestionResult, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return IngestionResult{}, err
	}
	// The diagnostic key ignores which search query found the listing, as the content hash does:
	// otherwise one held card found by ten searches was recorded, and counted, ten times.
	keyed := d
	if _, ok := d.Metadata["listing_url"]; ok {
		keyed.Metadata = map[string]string{}
		for k, v := range d.Metadata {
			if k != "listing_url" {
				keyed.Metadata[k] = v
			}
		}
	}
	keyRaw, err := json.Marshal(keyed)
	if err != nil {
		return IngestionResult{}, err
	}
	sum := sha256.Sum256(append([]byte(reason), keyRaw...))
	_, err = tx.Exec(ctx, `INSERT INTO ingestion_review(diagnostic_key,source,reason,discovery) VALUES($1,$2,$3,$4) ON CONFLICT(diagnostic_key) DO UPDATE SET last_seen_at=now(),observations=ingestion_review.observations+1`, hex.EncodeToString(sum[:]), d.Source, reason, raw)
	return IngestionResult{ReviewReason: reason}, err
}

func ingestOpportunity(ctx context.Context, tx pgx.Tx, d opportunity.DiscoveredOpportunity) (IngestionResult, error) {
	if d.Identity() == "" {
		return ingestionReview(ctx, tx, d, "missing_source_identity")
	}
	var id, companyID int64
	var revision int
	var hash string
	// A different nonempty external ID is a different vacancy even at the same URL.
	rows, err := tx.Query(ctx, `SELECT id,company_id,current_revision,content_hash FROM opportunity WHERE source=$1 AND (($2<>'' AND external_id=$2) OR ($3<>'' AND canonical_url=$3 AND ($2='' OR external_id=''))) ORDER BY CASE WHEN external_id=$2 AND $2<>'' THEN 0 ELSE 1 END,id`, d.Source, d.ExternalID, d.CanonicalURL)
	if err != nil {
		return IngestionResult{}, err
	}
	count := 0
	for rows.Next() {
		count++
		if count == 1 {
			err = rows.Scan(&id, &companyID, &revision, &hash)
		}
	}
	rows.Close()
	if err != nil {
		return IngestionResult{}, err
	}
	if rows.Err() != nil {
		return IngestionResult{}, rows.Err()
	}
	if count > 1 {
		return ingestionReview(ctx, tx, d, "ambiguous_source_identity")
	}
	if id != 0 {
		var name string
		if err = tx.QueryRow(ctx, `SELECT name FROM company WHERE id=$1`, companyID).Scan(&name); err != nil {
			return IngestionResult{}, err
		}
		if d.CompanyID != 0 && d.CompanyID != companyID {
			return ingestionReview(ctx, tx, d, "source_employer_changed")
		}
		if normalizeCompany(name) != normalizeCompany(d.CompanyName) {
			aliased, err := aliasCompany(ctx, tx, d.CompanyName)
			if err != nil {
				return IngestionResult{}, err
			}
			if aliased != companyID {
				return ingestionReview(ctx, tx, d, "source_employer_changed")
			}
		}
	} else {
		var reason string
		companyID, reason, err = resolveOpportunityCompany(ctx, tx, d)
		if err != nil {
			return IngestionResult{}, err
		}
		if reason != "" {
			return ingestionReview(ctx, tx, d, reason)
		}
	}
	d.CompanyID = companyID
	newHash := d.Hash()
	raw, err := json.Marshal(d)
	if err != nil {
		return IngestionResult{}, err
	}
	meta, err := json.Marshal(d.Metadata)
	if err != nil {
		return IngestionResult{}, err
	}
	if d.Metadata == nil {
		meta = []byte(`{}`)
	}
	created := id == 0
	if created {
		revision = 1
		err = tx.QueryRow(ctx, `INSERT INTO opportunity(company_id,source,source_key,external_id,url,canonical_url,title,normalized_title,description,location,employment_type,remote_status,published_at,expires_at,availability_status,description_status,content_hash,metadata) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) RETURNING id`, companyID, d.Source, d.Identity(), d.ExternalID, d.URL, d.CanonicalURL, d.Title, d.NormalizedTitle, d.Description, d.Location, d.EmploymentType, d.RemoteStatus, d.PublishedAt, d.ExpiresAt, d.Availability, d.DescriptionStatus, newHash, meta).Scan(&id)
	} else if hash != newHash {
		revision++
		_, err = tx.Exec(ctx, `UPDATE opportunity SET title=$2,normalized_title=$3,description=$4,location=$5,employment_type=$6,remote_status=$7,published_at=$8,expires_at=$9,availability_status=$10,description_status=$11,content_hash=$12,metadata=$13,current_revision=$14,canonical_url=$15,updated_at=now() WHERE id=$1`, id, d.Title, d.NormalizedTitle, d.Description, d.Location, d.EmploymentType, d.RemoteStatus, d.PublishedAt, d.ExpiresAt, d.Availability, d.DescriptionStatus, newHash, meta, revision, d.CanonicalURL)
	}
	if err != nil {
		return IngestionResult{}, err
	}
	if created || hash != newHash {
		if _, err = tx.Exec(ctx, `INSERT INTO opportunity_revision(opportunity_id,revision,document,content_hash) VALUES($1,$2,$3,$4)`, id, revision, raw, newHash); err != nil {
			return IngestionResult{}, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE opportunity SET last_seen_at=now(),url=$2,external_id=CASE WHEN $3='' THEN external_id ELSE $3 END WHERE id=$1`, id, d.URL, d.ExternalID)
	return IngestionResult{ID: id, Revision: revision, Created: created}, err
}

func normalizeCompany(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
func resolveOpportunityCompany(ctx context.Context, tx pgx.Tx, d opportunity.DiscoveredOpportunity) (int64, string, error) {
	if d.CompanyID != 0 {
		var name string
		err := tx.QueryRow(ctx, `SELECT name FROM company WHERE id=$1`, d.CompanyID).Scan(&name)
		if err != nil {
			return 0, "", err
		}
		if normalizeCompany(name) != normalizeCompany(d.CompanyName) {
			return 0, "company_association_mismatch", nil
		}
		return d.CompanyID, "", nil
	}
	if d.CompanyName == "" {
		return 0, "missing_employer", nil
	}
	// An operator already decided which company this employer name belongs to.
	if aliased, err := aliasCompany(ctx, tx, d.CompanyName); err != nil || aliased != 0 {
		return aliased, "", err
	}
	rows, err := tx.Query(ctx, `SELECT id,name,domain FROM company WHERE lower(regexp_replace(trim(name),'\s+',' ','g'))=$1 OR ($2<>'' AND lower(domain)=lower($2))`, normalizeCompany(d.CompanyName), d.DomainCandidate)
	if err != nil {
		return 0, "", err
	}
	var id int64
	count := 0
	collision := false
	for rows.Next() {
		var candidate int64
		var name, domain string
		if err = rows.Scan(&candidate, &name, &domain); err != nil {
			break
		}
		count++
		id = candidate
		if normalizeCompany(name) != normalizeCompany(d.CompanyName) {
			collision = true
		}
	}
	rows.Close()
	if err != nil {
		return 0, "", err
	}
	if rows.Err() != nil {
		return 0, "", rows.Err()
	}
	if count > 1 || collision {
		return 0, "ambiguous_company_identity", nil
	}
	if count == 1 {
		return id, "", nil
	}
	domain := strings.ToLower(strings.TrimSpace(d.DomainCandidate))
	if domain == "" || strings.ContainsAny(domain, " /:@\\") || !strings.Contains(domain, ".") {
		return 0, "missing_or_invalid_domain_candidate", nil
	}
	err = tx.QueryRow(ctx, `INSERT INTO company(name,domain,source,source_url,domain_status) VALUES($1,$2,$3,$4,'candidate') RETURNING id`, d.CompanyName, domain, d.Source, d.URL).Scan(&id)
	return id, "", err
}

func (db *DB) ListOpportunities(ctx context.Context, source string, companyID int64) ([]Opportunity, error) {
	return db.ListOpportunitiesFiltered(ctx, source, companyID, "", "", 200, 0)
}

// CountOpportunitiesFiltered reports how many rows the same filter matches in total, so the UI
// can show a real count and a real number of pages rather than "at least this many".
func (db *DB) CountOpportunitiesFiltered(ctx context.Context, source string, companyID int64, title, availability string) (int, error) {
	var total int
	err := db.QueryRow(ctx, `SELECT count(*) FROM opportunity o WHERE ($1='' OR o.source=$1) AND ($2=0 OR o.company_id=$2) AND ($3='' OR o.normalized_title ILIKE '%' || $3 || '%') AND ($4='' OR o.availability_status=$4)`,
		source, companyID, opportunity.NormalizeTitle(title), availability).Scan(&total)
	return total, err
}

// ListOpportunitiesFiltered returns one page of matching opportunities. The caller supplies the
// window; nothing here silently truncates, because a list that quietly stops at an invisible
// limit hides rows an operator has no way of knowing exist.
func (db *DB) ListOpportunitiesFiltered(ctx context.Context, source string, companyID int64, title, availability string, limit, offset int) ([]Opportunity, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	// Ordering includes the id so a page boundary is stable when two rows share updated_at;
	// without it the same row can appear on two pages or on none.
	rows, err := db.Query(ctx, `SELECT o.id,o.company_id,c.name,o.current_revision,r.document,o.discovered_at,o.last_seen_at,o.updated_at FROM opportunity o JOIN company c ON c.id=o.company_id JOIN opportunity_revision r ON r.opportunity_id=o.id AND r.revision=o.current_revision WHERE ($1='' OR o.source=$1) AND ($2=0 OR o.company_id=$2) AND ($3='' OR o.normalized_title ILIKE '%' || $3 || '%') AND ($4='' OR o.availability_status=$4) ORDER BY o.updated_at DESC,o.id DESC LIMIT $5 OFFSET $6`, source, companyID, opportunity.NormalizeTitle(title), availability, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Opportunity{}
	for rows.Next() {
		var o Opportunity
		var raw []byte
		if err = rows.Scan(&o.ID, &o.CompanyID, &o.CompanyName, &o.Revision, &raw, &o.DiscoveredAt, &o.LastSeenAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &o.Facts); err != nil {
			return nil, err
		}
		result = append(result, o)
	}
	return result, rows.Err()
}

func (db *DB) GetOpportunity(ctx context.Context, id int64) (*Opportunity, error) {
	var o Opportunity
	var raw []byte
	err := db.QueryRow(ctx, `SELECT o.id,o.company_id,c.name,o.current_revision,r.document,o.discovered_at,o.last_seen_at,o.updated_at FROM opportunity o JOIN company c ON c.id=o.company_id JOIN opportunity_revision r ON r.opportunity_id=o.id AND r.revision=o.current_revision WHERE o.id=$1`, id).Scan(&o.ID, &o.CompanyID, &o.CompanyName, &o.Revision, &raw, &o.DiscoveredAt, &o.LastSeenAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &o.Facts); err != nil {
		return nil, err
	}
	return &o, nil
}

func (db *DB) OpportunityHistory(ctx context.Context, id int64) ([]json.RawMessage, error) {
	rows, err := db.Query(ctx, `SELECT document FROM opportunity_revision WHERE opportunity_id=$1 ORDER BY revision DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []json.RawMessage
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		result = append(result, raw)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("opportunity not found")
	}
	return result, nil
}

// AllOpportunityIDs returns every stored opportunity id, oldest first. The list endpoints cap
// their result for the UI; analysis that must cover the whole corpus — a matching rehearsal, a
// backfill — needs the complete set and says so explicitly.
func (db *DB) AllOpportunityIDs(ctx context.Context) ([]int64, error) {
	rows, err := db.Query(ctx, `SELECT id FROM opportunity ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// aliasCompany returns the company an operator has attached this employer name to, or 0.
func aliasCompany(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, companyName string) (int64, error) {
	key := normalizeCompany(companyName)
	if key == "" {
		return 0, nil
	}
	var id int64
	err := q.QueryRow(ctx, `SELECT company_id FROM company_alias WHERE normalized_name=$1`, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}
