package store

import (
	"context"
	"github.com/anthonyalando8/prospector/internal/opportunity"
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

// usSourceNames are every seeder pulling US/international remote job postings — duplicated here
// (rather than imported from internal/score, which would create an import cycle back into
// store) since it's just a fixed list of source strings, not shared logic.
var usSourceNames = []string{"remoteok", "weworkremotely", "remotive", "himalayas"}

// CompanyFilter is every dimension /companies can filter by. Grouped into a struct rather than a
// growing list of positional string args — six filter dimensions as positional strings is where
// that stops being readable (was already at four before Market/CreatedSince).
type CompanyFilter struct {
	Status       string
	Segment      string
	DomainStatus string
	// Market is "us" (the four US/international job-board sources) or "kenya" (everything
	// else) — filtered by source, not segment, deliberately: a freshly-seeded company has no
	// segment yet (that's only assigned once scored), so a segment-based filter would hide
	// brand-new US-sourced companies until the research loop got to them.
	Market string
	// CreatedSince is one of "5m", "1h", "today", "7d", or "" for no filter.
	CreatedSince string
	// Search matches name or domain, case-insensitive substring.
	Search                                        string
	Source, Research, HasOpportunities, HasStrong string
}

// companyFilterClause builds the shared WHERE clause for ListCompanies/CountCompanies, so the
// two never drift apart — a list and its "how many pages" count must agree on what "matches"
// means.
func companyFilterClause(f CompanyFilter, args []interface{}) (string, []interface{}) {
	clause := " WHERE 1=1"
	argNum := len(args) + 1

	if f.Status != "" {
		clause += ` AND status = $` + strconv.Itoa(argNum)
		args = append(args, f.Status)
		argNum++
	}
	if f.Segment != "" {
		clause += ` AND segment = $` + strconv.Itoa(argNum)
		args = append(args, f.Segment)
		argNum++
	}
	if f.DomainStatus != "" {
		clause += ` AND domain_status = $` + strconv.Itoa(argNum)
		args = append(args, f.DomainStatus)
		argNum++
	}
	switch f.Market {
	case "us":
		clause += ` AND source = ANY($` + strconv.Itoa(argNum) + `)`
		args = append(args, usSourceNames)
		argNum++
	case "kenya":
		clause += ` AND NOT (source = ANY($` + strconv.Itoa(argNum) + `))`
		args = append(args, usSourceNames)
		argNum++
	}
	switch f.CreatedSince {
	case "5m":
		clause += ` AND created_at >= now() - interval '5 minutes'`
	case "1h":
		clause += ` AND created_at >= now() - interval '1 hour'`
	case "today":
		clause += ` AND created_at >= date_trunc('day', now())`
	case "7d":
		clause += ` AND created_at >= now() - interval '7 days'`
	}
	if f.Search != "" {
		clause += ` AND (name ILIKE $` + strconv.Itoa(argNum) + ` OR domain ILIKE $` + strconv.Itoa(argNum) + `)`
		args = append(args, "%"+f.Search+"%")
		argNum++
	}
	if f.Source != "" {
		clause += ` AND source=$` + strconv.Itoa(argNum)
		args = append(args, f.Source)
		argNum++
	}
	switch f.Research {
	case "queue":
		clause += ` AND ` + researchQueueWhere
	case "researched":
		clause += ` AND last_researched_at IS NOT NULL`
	case "unresearched":
		clause += ` AND last_researched_at IS NULL`
	case "recent":
		clause += ` AND last_researched_at>now()-interval '24 hours'`
	}
	if f.HasOpportunities == "yes" {
		clause += ` AND EXISTS(SELECT 1 FROM opportunity o WHERE o.company_id=company.id)`
	}
	if f.HasOpportunities == "no" {
		clause += ` AND NOT EXISTS(SELECT 1 FROM opportunity o WHERE o.company_id=company.id)`
	}
	if f.HasStrong != "" {
		match := `EXISTS(SELECT 1 FROM opportunity o JOIN opportunity_match m ON m.opportunity_id=o.id AND m.opportunity_revision=o.current_revision JOIN candidate_profile p ON p.active_revision_id=m.profile_revision_id WHERE o.company_id=company.id AND m.classification='strong_match' AND m.matcher_version=$` + strconv.Itoa(argNum) + `)`
		if f.HasStrong == "no" {
			match = "NOT " + match
		}
		clause += " AND " + match
		args = append(args, opportunity.MatcherVersion)
		argNum++
	}
	return clause, args
}

// ListCompanies retrieves companies matching f — search matches against name or domain, since
// with the list growing past what fits on one screen, "find the company I'm thinking of" needs
// to be an actual query, not scrolling.
func (db *DB) ListCompanies(ctx context.Context, f CompanyFilter, limit, offset int) ([]*Company, error) {
	where, args := companyFilterClause(f, nil)
	query := `SELECT id, name, domain, source, source_url, segment, score, status, location, notes,
		        last_researched_at, next_research_at, domain_status, domain_confidence, created_at, updated_at
		     FROM company` + where

	argNum := len(args) + 1
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

// CountCompanies returns how many companies match f — the total behind "page 2 of N" / "137
// results".
func (db *DB) CountCompanies(ctx context.Context, f CompanyFilter) (int, error) {
	where, args := companyFilterClause(f, nil)
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM company`+where, args...).Scan(&n)
	return n, err
}

// ListCompaniesForDomainResolution returns companies stuck at domain_status='failed' — every
// guessed candidate (verify.CandidateDomains) already exhausted during the normal verify loop.
// Ordered by score so a limited daily search-API budget (internal/domainsearch) goes to the best
// prospects first, not whichever failed most recently.
func (db *DB) ListCompaniesForDomainResolution(ctx context.Context, limit int) ([]*Company, error) {
	rows, err := db.Query(ctx,
		`SELECT id, name, domain, source, source_url, segment, score, status, location, notes,
		        last_researched_at, next_research_at, domain_status, domain_confidence, created_at, updated_at
		 FROM company
		 WHERE domain_status = 'failed'
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

// SetCompanySegment force-sets a company's segment — the one path that puts it there other than
// score.Scorer's rule-based classifySegment, used for "application" mode (company.html's manual
// override to draft as a genuine job candidate instead of a contract pitch — see
// internal/score/scorer.go, which treats "application" as sticky against re-scoring).
func (db *DB) SetCompanySegment(ctx context.Context, id int64, segment string) error {
	_, err := db.Exec(ctx,
		`UPDATE company SET segment = $1, updated_at = now() WHERE id = $2`,
		segment, id)
	return err
}

// ClearCompanySegment resets a company back to no segment at all, so the next score pass
// re-derives it from scratch via classifySegment's ordinary rules — the undo for
// SetCompanySegment's "application" override.
func (db *DB) ClearCompanySegment(ctx context.Context, id int64) error {
	_, err := db.Exec(ctx,
		`UPDATE company SET segment = NULL, updated_at = now() WHERE id = $1`,
		id)
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

// UpdateCompanyNotes overwrites company notes outright — kept for the rare case that needs to
// replace the whole field; AppendCompanyNote below is what the UI actually uses day to day.
func (db *DB) UpdateCompanyNotes(ctx context.Context, id int64, notes string) error {
	_, err := db.Exec(ctx,
		`UPDATE company SET notes = $1, updated_at = now() WHERE id = $2`,
		notes, id)
	return err
}

// AppendCompanyNote prepends a timestamped entry to the company's notes — a running log of what
// actually happened (a reply's content, a call outcome, an objection) rather than a status enum
// that can't say anything beyond its own label. Newest entry first, so the most recent context is
// what a human sees at a glance. Built as a single UPDATE (not read-then-write) so two notes
// added around the same time can't race and clobber each other.
func (db *DB) AppendCompanyNote(ctx context.Context, id int64, note string) error {
	entry := time.Now().Format("2006-01-02 15:04") + " — " + note
	_, err := db.Exec(ctx,
		`UPDATE company
		 SET notes = CASE WHEN notes IS NULL OR notes = '' THEN $1 ELSE $1 || E'\n\n' || notes END,
		     updated_at = now()
		 WHERE id = $2`,
		entry, id)
	return err
}
