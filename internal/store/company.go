package store

import (
	"context"
	"strconv"
	"time"
)

// Company represents a prospect company
type Company struct {
	ID               int64
	Name             string
	Domain           string
	Source           string
	SourceURL        *string
	Segment          *string
	Score            int
	Status           string
	Location         *string
	Notes            *string
	LastResearchedAt *time.Time
	NextResearchAt   *time.Time
	DomainStatus     string // unknown|candidate|verified|failed|manual
	DomainConfidence *float64
	ScoreBreakdown   []byte // raw JSON — the explainable line items behind Score (review.md §15)
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// CreateCompany inserts a new company. domainStatus should be "manual" for a human-entered
// domain (trusted immediately) or "candidate" for a guessed one that still needs
// verification before it's safe to enrich — see internal/verify.
func (db *DB) CreateCompany(ctx context.Context, name, domain, source, sourceURL, domainStatus string) (*Company, error) {
	var id int64
	var createdAt, updatedAt time.Time

	err := db.QueryRow(ctx,
		`INSERT INTO company (name, domain, source, source_url, domain_status)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, created_at, updated_at`,
		name, domain, source, sourceURL, domainStatus).Scan(&id, &createdAt, &updatedAt)

	if err != nil {
		return nil, err
	}

	return &Company{
		ID:           id,
		Name:         name,
		Domain:       domain,
		Source:       source,
		SourceURL:    &sourceURL,
		Status:       "new",
		Score:        0,
		DomainStatus: domainStatus,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}, nil
}

// GetCompany retrieves a company by ID
func (db *DB) GetCompany(ctx context.Context, id int64) (*Company, error) {
	var company Company

	err := db.QueryRow(ctx,
		`SELECT id, name, domain, source, source_url, segment, score, status, location, notes,
		        last_researched_at, next_research_at, domain_status, domain_confidence, score_breakdown, created_at, updated_at
		 FROM company WHERE id = $1`, id).Scan(
		&company.ID, &company.Name, &company.Domain, &company.Source, &company.SourceURL,
		&company.Segment, &company.Score, &company.Status, &company.Location, &company.Notes,
		&company.LastResearchedAt, &company.NextResearchAt, &company.DomainStatus, &company.DomainConfidence, &company.ScoreBreakdown,
		&company.CreatedAt, &company.UpdatedAt)

	if err != nil {
		return nil, err
	}

	return &company, nil
}

// ListCompanies retrieves companies with optional filtering
func (db *DB) ListCompanies(ctx context.Context, status, segment string, limit, offset int) ([]*Company, error) {
	query := `SELECT id, name, domain, source, source_url, segment, score, status, location, notes,
		        last_researched_at, next_research_at, domain_status, domain_confidence, created_at, updated_at
		     FROM company WHERE 1=1`
	args := []interface{}{}
	argNum := 1

	if status != "" {
		query += ` AND status = $` + strconv.Itoa(argNum)
		args = append(args, status)
		argNum++
	}

	if segment != "" {
		query += ` AND segment = $` + strconv.Itoa(argNum)
		args = append(args, segment)
		argNum++
	}

	query += ` ORDER BY score DESC, created_at DESC LIMIT $` + strconv.Itoa(argNum) + ` OFFSET $` + strconv.Itoa(argNum+1)
	args = append(args, limit, offset)

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var companies []*Company
	for rows.Next() {
		var company Company
		err := rows.Scan(
			&company.ID, &company.Name, &company.Domain, &company.Source, &company.SourceURL,
			&company.Segment, &company.Score, &company.Status, &company.Location, &company.Notes,
			&company.LastResearchedAt, &company.NextResearchAt, &company.DomainStatus, &company.DomainConfidence,
			&company.CreatedAt, &company.UpdatedAt)
		if err != nil {
			return nil, err
		}
		companies = append(companies, &company)
	}

	return companies, rows.Err()
}

// ListCompaniesForResearch returns companies eligible for the research (enrichment) worker
// loop: a status of "new" (never successfully enriched), a domain trusted enough to crawl
// (verified by internal/verify, or manually entered), and either never researched or past
// its next_research_at freshness deadline (review.md §11).
func (db *DB) ListCompaniesForResearch(ctx context.Context, limit int) ([]*Company, error) {
	rows, err := db.Query(ctx,
		`SELECT id, name, domain, source, source_url, segment, score, status, location, notes,
		        last_researched_at, next_research_at, domain_status, domain_confidence, created_at, updated_at
		 FROM company
		 WHERE status = 'new'
		   AND domain_status IN ('verified', 'manual')
		   AND (next_research_at IS NULL OR next_research_at <= now())
		 ORDER BY score DESC, created_at ASC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var companies []*Company
	for rows.Next() {
		var company Company
		if err := rows.Scan(
			&company.ID, &company.Name, &company.Domain, &company.Source, &company.SourceURL,
			&company.Segment, &company.Score, &company.Status, &company.Location, &company.Notes,
			&company.LastResearchedAt, &company.NextResearchAt, &company.DomainStatus, &company.DomainConfidence,
			&company.CreatedAt, &company.UpdatedAt); err != nil {
			return nil, err
		}
		companies = append(companies, &company)
	}
	return companies, rows.Err()
}

// MarkResearched stamps a research attempt and schedules the next one ttl in the future, so
// the research loop doesn't re-crawl the same company on every tick.
func (db *DB) MarkResearched(ctx context.Context, id int64, ttl time.Duration) error {
	_, err := db.Exec(ctx,
		`UPDATE company SET last_researched_at = now(), next_research_at = now() + $1, updated_at = now() WHERE id = $2`,
		ttl, id)
	return err
}

// ListCompaniesForVerification returns companies whose domain hasn't been checked yet —
// newly seeded (unknown) or guessed-and-unverified (candidate) — for the verify worker loop.
func (db *DB) ListCompaniesForVerification(ctx context.Context, limit int) ([]*Company, error) {
	rows, err := db.Query(ctx,
		`SELECT id, name, domain, source, source_url, segment, score, status, location, notes,
		        last_researched_at, next_research_at, domain_status, domain_confidence, created_at, updated_at
		 FROM company
		 WHERE domain_status IN ('unknown', 'candidate')
		 ORDER BY created_at ASC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var companies []*Company
	for rows.Next() {
		var company Company
		if err := rows.Scan(
			&company.ID, &company.Name, &company.Domain, &company.Source, &company.SourceURL,
			&company.Segment, &company.Score, &company.Status, &company.Location, &company.Notes,
			&company.LastResearchedAt, &company.NextResearchAt, &company.DomainStatus, &company.DomainConfidence,
			&company.CreatedAt, &company.UpdatedAt); err != nil {
			return nil, err
		}
		companies = append(companies, &company)
	}
	return companies, rows.Err()
}

// ListCompaniesForDrafting returns companies deterministically eligible for an AI draft call —
// review.md §7/§8's candidate-queue checks done in SQL, before an AI call is ever considered:
// enriched, a domain trustworthy enough to have actually been crawled, a real email contact to
// send to (drafting a company with no way to reach it wastes the call), a score at or above
// minScore, and no existing live (non-discarded) draft already. Ordered by score first — the
// system should spend its limited AI budget on the best opportunities first (review.md §7).
func (db *DB) ListCompaniesForDrafting(ctx context.Context, minScore, limit int) ([]*Company, error) {
	rows, err := db.Query(ctx,
		`SELECT c.id, c.name, c.domain, c.source, c.source_url, c.segment, c.score, c.status, c.location, c.notes,
		        c.last_researched_at, c.next_research_at, c.domain_status, c.domain_confidence, c.created_at, c.updated_at
		 FROM company c
		 WHERE c.status = 'enriched'
		   AND c.domain_status IN ('verified', 'manual')
		   AND c.score >= $1
		   AND EXISTS (SELECT 1 FROM contact ct WHERE ct.company_id = c.id AND ct.kind = 'email')
		   AND NOT EXISTS (SELECT 1 FROM draft d WHERE d.company_id = c.id AND d.state != 'discarded')
		 ORDER BY c.score DESC, c.last_researched_at ASC NULLS LAST
		 LIMIT $2`, minScore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var companies []*Company
	for rows.Next() {
		var company Company
		if err := rows.Scan(
			&company.ID, &company.Name, &company.Domain, &company.Source, &company.SourceURL,
			&company.Segment, &company.Score, &company.Status, &company.Location, &company.Notes,
			&company.LastResearchedAt, &company.NextResearchAt, &company.DomainStatus, &company.DomainConfidence,
			&company.CreatedAt, &company.UpdatedAt); err != nil {
			return nil, err
		}
		companies = append(companies, &company)
	}
	return companies, rows.Err()
}

// UpdateCompanyDomainStatus records the outcome of a domain-verification attempt.
func (db *DB) UpdateCompanyDomainStatus(ctx context.Context, id int64, status string, confidence float64) error {
	_, err := db.Exec(ctx,
		`UPDATE company SET domain_status = $1, domain_confidence = $2, updated_at = now() WHERE id = $3`,
		status, confidence, id)
	return err
}

// UpdateCompanyDomain replaces the company's domain — used when the verify loop's initial
// guess didn't resolve but a later candidate from verify.CandidateDomains did, so the correct
// domain is what actually gets enriched, not the failed first guess.
func (db *DB) UpdateCompanyDomain(ctx context.Context, id int64, domain string) error {
	_, err := db.Exec(ctx,
		`UPDATE company SET domain = $1, updated_at = now() WHERE id = $2`,
		domain, id)
	return err
}

// UpdateCompanyStatus updates the status of a company
func (db *DB) UpdateCompanyStatus(ctx context.Context, id int64, status string) error {
	_, err := db.Exec(ctx,
		`UPDATE company SET status = $1, updated_at = now() WHERE id = $2`,
		status, id)
	return err
}

// UpdateCompanyScore updates the score and segment. breakdownJSON is the explainable line
// items that produced the score (review.md §15) — pass nil to leave it untouched, which
// internal/seed's segment-only tagging (no real scoring pass) relies on.
func (db *DB) UpdateCompanyScore(ctx context.Context, id int64, score int, segment string, breakdownJSON []byte) error {
	if breakdownJSON == nil {
		_, err := db.Exec(ctx,
			`UPDATE company SET score = $1, segment = $2, updated_at = now() WHERE id = $3`,
			score, segment, id)
		return err
	}
	_, err := db.Exec(ctx,
		`UPDATE company SET score = $1, segment = $2, score_breakdown = $3, updated_at = now() WHERE id = $4`,
		score, segment, breakdownJSON, id)
	return err
}

// UpdateCompanyNotes updates company notes
func (db *DB) UpdateCompanyNotes(ctx context.Context, id int64, notes string) error {
	_, err := db.Exec(ctx,
		`UPDATE company SET notes = $1, updated_at = now() WHERE id = $2`,
		notes, id)
	return err
}
