package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Contact-discovery queue states. These mirror the CHECK constraint in migration 0026; the
// constants exist so callers name a state rather than spelling it, and a typo fails to compile
// instead of failing at write time.
const (
	DiscoveryPending         = "pending"
	DiscoveryClaimed         = "claimed"
	DiscoverySearching       = "searching"
	DiscoveryCompleted       = "completed"
	DiscoveryPartial         = "partial"
	DiscoveryNoResult        = "no_result"
	DiscoveryReviewRequired  = "review_required"
	DiscoveryIdentityBlocked = "identity_blocked"
	DiscoveryFailed          = "failed"
)

// Discovery priorities, ascending. These encode the order section 2 of the brief asks for: the
// highest-value work is a strong match with nowhere to send an application.
const (
	PriorityStrongMatch      = 1
	PrioritySelectedPlan     = 2
	PrioritySelectedPossible = 3
)

// ContactDiscoveryTask is one company's durable place in the contact-discovery queue.
type ContactDiscoveryTask struct {
	CompanyID         int64
	State             string
	Priority          int
	EligibilityReason string
	Intent            string
	OpportunityID     *int64
	PlanID            *int64
	Attempts          int
	LastSearchedAt    *time.Time
	NextEligibleAt    time.Time
	ResultSummary     []byte
	FailureReason     string
	ClaimedBy         string
	ClaimExpiresAt    *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Summary decodes the stored result summary, or an empty map when there is none.
func (t *ContactDiscoveryTask) Summary() map[string]any {
	out := map[string]any{}
	if len(t.ResultSummary) > 0 {
		_ = json.Unmarshal(t.ResultSummary, &out)
	}
	return out
}

// DiscoveryCandidateInput is one company eligible for contact discovery, with the reason it
// became eligible and at what priority.
type DiscoveryCandidateInput struct {
	CompanyID         int64
	CompanyName       string
	Domain            string
	DomainStatus      string
	Priority          int
	EligibilityReason string
	Intent            string
	OpportunityID     int64
	PlanID            int64
}

// contactDiscoveryEligibility is the population contact discovery may consider, and nothing
// wider. Three things make a company eligible: a live strong match, an open contact plan, or a
// possible match a person explicitly selected. An opportunity nobody wants never queues a search
// merely because its employer publishes no address — spending the budget on those is exactly how
// a bottleneck stays a bottleneck.
//
// Whether the company actually lacks an acceptable destination is deliberately not decided here.
// That question is answered in Go by the same classifiers and the same recipient rules the send
// path uses, because expressing it a second time in SQL would be a second opinion that could
// disagree with the first.
const contactDiscoveryEligibility = `
WITH live_match AS (
  SELECT o.company_id, m.classification, o.id AS opportunity_id, o.selection_status
    FROM opportunity o
    JOIN opportunity_match m
      ON m.opportunity_id = o.id AND m.opportunity_revision = o.current_revision
   WHERE o.availability_status <> 'closed' AND o.selection_status <> 'ignored'
),
strong AS (
  SELECT company_id, min(opportunity_id) AS opportunity_id
    FROM live_match WHERE classification = 'strong_match' GROUP BY company_id
),
chosen_possible AS (
  SELECT company_id, min(opportunity_id) AS opportunity_id
    FROM live_match WHERE classification = 'possible_match' AND selection_status = 'selected'
   GROUP BY company_id
),
open_plan AS (
  SELECT DISTINCT ON (company_id) company_id, id AS plan_id, intent, opportunity_id
    FROM contact_plan WHERE status IN ('suggested','selected')
   ORDER BY company_id, updated_at DESC, id DESC
)
SELECT c.id, c.name, c.domain, c.domain_status,
  CASE WHEN s.company_id IS NOT NULL THEN 1
       WHEN p.company_id IS NOT NULL THEN 2
       ELSE 3 END AS priority,
  CASE WHEN s.company_id IS NOT NULL THEN 'a strong match at this company has no acceptable destination'
       WHEN p.company_id IS NOT NULL THEN 'an open contact plan at this company has no acceptable destination'
       ELSE 'a possible match was selected for contact planning and has no acceptable destination' END AS reason,
  COALESCE(p.intent, '') AS intent,
  COALESCE(s.opportunity_id, p.opportunity_id, cp.opportunity_id, 0) AS opportunity_id,
  COALESCE(p.plan_id, 0) AS plan_id
 FROM company c
 LEFT JOIN strong s ON s.company_id = c.id
 LEFT JOIN chosen_possible cp ON cp.company_id = c.id
 LEFT JOIN open_plan p ON p.company_id = c.id
 LEFT JOIN contact_discovery_task t ON t.company_id = c.id
 WHERE (s.company_id IS NOT NULL OR cp.company_id IS NOT NULL OR p.company_id IS NOT NULL)
   AND (t.company_id IS NULL
        OR (t.claimed_by = '' AND t.next_eligible_at <= now())
        OR (t.claimed_by <> '' AND t.claim_expires_at < now()))
 ORDER BY priority, COALESCE(t.last_searched_at, to_timestamp(0)), c.id
 LIMIT $1`

// ListCompaniesForContactDiscovery returns the companies whose contact situation is worth
// investigating, best first and respecting each company's cooldown.
func (db *DB) ListCompaniesForContactDiscovery(ctx context.Context, limit int) ([]DiscoveryCandidateInput, error) {
	if limit < 1 || limit > 200 {
		limit = 20
	}
	rows, err := db.Query(ctx, contactDiscoveryEligibility, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiscoveryCandidateInput{}
	for rows.Next() {
		var item DiscoveryCandidateInput
		if err := rows.Scan(&item.CompanyID, &item.CompanyName, &item.Domain, &item.DomainStatus,
			&item.Priority, &item.EligibilityReason, &item.Intent, &item.OpportunityID, &item.PlanID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// EnqueueContactDiscovery records or refreshes a company's queue entry. It is idempotent, and it
// never resets a cooldown by itself — asking twice does not buy an extra search.
func (db *DB) EnqueueContactDiscovery(ctx context.Context, in DiscoveryCandidateInput) (*ContactDiscoveryTask, error) {
	opportunity := nullableID(in.OpportunityID)
	plan := nullableID(in.PlanID)
	priority := in.Priority
	if priority < 1 {
		priority = PrioritySelectedPossible
	}
	_, err := db.Exec(ctx, `
		INSERT INTO contact_discovery_task
		  (company_id, state, priority, eligibility_reason, intent, opportunity_id, plan_id)
		VALUES ($1,'pending',$2,$3,$4,$5,$6)
		ON CONFLICT (company_id) DO UPDATE SET
		  priority = LEAST(contact_discovery_task.priority, EXCLUDED.priority),
		  eligibility_reason = EXCLUDED.eligibility_reason,
		  intent = EXCLUDED.intent,
		  opportunity_id = COALESCE(EXCLUDED.opportunity_id, contact_discovery_task.opportunity_id),
		  plan_id = COALESCE(EXCLUDED.plan_id, contact_discovery_task.plan_id),
		  updated_at = now()`,
		in.CompanyID, priority, in.EligibilityReason, in.Intent, opportunity, plan)
	if err != nil {
		return nil, err
	}
	return db.GetContactDiscoveryTask(ctx, in.CompanyID)
}

// RequestContactDiscovery is the manual "Find contact" action. It is the same queue entry the
// worker reads — there is deliberately no separate manual code path — and its one extra effect is
// to clear the cooldown, because an operator asking explicitly is new information that the
// automatic schedule did not have.
func (db *DB) RequestContactDiscovery(ctx context.Context, in DiscoveryCandidateInput) (*ContactDiscoveryTask, error) {
	if _, err := db.EnqueueContactDiscovery(ctx, in); err != nil {
		return nil, err
	}
	_, err := db.Exec(ctx, `
		UPDATE contact_discovery_task
		   SET state = CASE WHEN claimed_by <> '' AND claim_expires_at > now() THEN state ELSE 'pending' END,
		       next_eligible_at = now(), failure_reason = '', updated_at = now()
		 WHERE company_id = $1`, in.CompanyID)
	if err != nil {
		return nil, err
	}
	return db.GetContactDiscoveryTask(ctx, in.CompanyID)
}

// GetContactDiscoveryTask reads one company's queue entry, or nil when it has never been queued.
func (db *DB) GetContactDiscoveryTask(ctx context.Context, companyID int64) (*ContactDiscoveryTask, error) {
	var t ContactDiscoveryTask
	err := db.QueryRow(ctx, `
		SELECT company_id, state, priority, eligibility_reason, intent, opportunity_id, plan_id,
		       attempts, last_searched_at, next_eligible_at, result_summary, failure_reason,
		       claimed_by, claim_expires_at, created_at, updated_at
		  FROM contact_discovery_task WHERE company_id = $1`, companyID).Scan(
		&t.CompanyID, &t.State, &t.Priority, &t.EligibilityReason, &t.Intent, &t.OpportunityID, &t.PlanID,
		&t.Attempts, &t.LastSearchedAt, &t.NextEligibleAt, &t.ResultSummary, &t.FailureReason,
		&t.ClaimedBy, &t.ClaimExpiresAt, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ClaimContactDiscovery takes exclusive ownership of one company's search for a bounded time.
// It returns false when somebody else holds a live claim, which is the ordinary outcome when the
// worker and a manual request arrive together — one of them proceeds and the other waits, rather
// than both searching the same company.
func (db *DB) ClaimContactDiscovery(ctx context.Context, companyID int64, owner string, lease time.Duration) (bool, error) {
	if strings.TrimSpace(owner) == "" {
		return false, fmt.Errorf("a contact-discovery claim needs an owner")
	}
	if lease <= 0 {
		lease = 10 * time.Minute
	}
	tag, err := db.Exec(ctx, `
		UPDATE contact_discovery_task
		   SET state = 'claimed', claimed_by = $2, claim_expires_at = now() + $3::interval,
		       attempts = attempts + 1, updated_at = now()
		 WHERE company_id = $1
		   AND (claimed_by = '' OR claim_expires_at < now())`,
		companyID, owner, lease.String())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MarkContactDiscoverySearching records that the claim holder has started work.
func (db *DB) MarkContactDiscoverySearching(ctx context.Context, companyID int64, owner string) error {
	_, err := db.Exec(ctx, `
		UPDATE contact_discovery_task SET state='searching', updated_at=now()
		 WHERE company_id=$1 AND claimed_by=$2`, companyID, owner)
	return err
}

// ContactDiscoveryOutcome is how a search ended and when the company may be searched again.
type ContactDiscoveryOutcome struct {
	State         string
	Summary       any
	FailureReason string
	// Cooldown is how long before this company is eligible again. It is supplied by the caller
	// rather than derived here, because how long to wait depends on why the search ended, and
	// that reasoning belongs with the pipeline that knows.
	Cooldown time.Duration
}

// CompleteContactDiscovery releases the claim and records the outcome and the next eligible time.
func (db *DB) CompleteContactDiscovery(ctx context.Context, companyID int64, outcome ContactDiscoveryOutcome) error {
	summary := []byte("{}")
	if outcome.Summary != nil {
		encoded, err := json.Marshal(outcome.Summary)
		if err != nil {
			return err
		}
		summary = encoded
	}
	cooldown := outcome.Cooldown
	if cooldown <= 0 {
		cooldown = 24 * time.Hour
	}
	_, err := db.Exec(ctx, `
		UPDATE contact_discovery_task
		   SET state=$2, result_summary=$3, failure_reason=$4,
		       last_searched_at=now(), next_eligible_at=now() + $5::interval,
		       claimed_by='', claim_expires_at=NULL, updated_at=now()
		 WHERE company_id=$1`,
		companyID, outcome.State, summary, outcome.FailureReason, cooldown.String())
	return err
}

// ReleaseContactDiscoveryClaim hands a claim back without recording an outcome — for a worker
// shutting down mid-search, where the honest record is that nothing was concluded.
func (db *DB) ReleaseContactDiscoveryClaim(ctx context.Context, companyID int64, owner string) error {
	_, err := db.Exec(ctx, `
		UPDATE contact_discovery_task SET state='pending', claimed_by='', claim_expires_at=NULL, updated_at=now()
		 WHERE company_id=$1 AND claimed_by=$2`, companyID, owner)
	return err
}

// ContactCandidate is one stored discovery candidate with its classification and provenance.
type ContactCandidateRow struct {
	ID                  int64
	CompanyID           int64
	IdentityKey         string
	IdentityBasis       string
	Kind                string
	PersonName          string
	RoleTitle           string
	EmployerClaimed     string
	Email               string
	EmailProvenance     string
	Verification        string
	ApplicationURL      string
	ATSProvider         string
	RecipientFunction   string
	FunctionEvidence    string
	EndpointPurpose     string
	EndpointContactable bool
	Confidence          string
	BestSourceTrust     int
	EmployerOwned       bool
	Status              string
	RejectedReason      string
	PromotedContactID   *int64
	DiscoveredAt        time.Time
	UpdatedAt           time.Time
	Evidence            []ContactCandidateEvidenceRow
}

// ContactCandidateEvidenceRow is one stored observation behind a candidate.
type ContactCandidateEvidenceRow struct {
	ID         int64
	SourceType string
	SourceURL  string
	Excerpt    string
	Method     string
	Provider   string
	Trust      int
	Employer   bool
	ObservedAt time.Time
}

// SaveContactCandidateInput is one candidate and every observation supporting it.
type SaveContactCandidateInput struct {
	CompanyID           int64
	IdentityKey         string
	IdentityBasis       string
	Kind                string
	PersonName          string
	RoleTitle           string
	EmployerClaimed     string
	Email               string
	EmailProvenance     string
	Verification        string
	ApplicationURL      string
	ATSProvider         string
	RecipientFunction   string
	FunctionEvidence    string
	EndpointPurpose     string
	EndpointContactable bool
	Confidence          string
	BestSourceTrust     int
	EmployerOwned       bool
	Status              string
	RejectedReason      string
	Evidence            []SaveContactEvidenceInput
}

// SaveContactEvidenceInput is one observation.
type SaveContactEvidenceInput struct {
	SourceType  string
	SourceTrust int
	SourceURL   string
	Excerpt     string
	ExcerptHash string
	Method      string
	Provider    string
	Employer    bool
	ObservedAt  time.Time
}

// SaveContactCandidate stores a candidate and its observations, merging with any existing
// candidate that shares its identity.
//
// Merging is additive on both sides. The candidate keeps the strongest source trust it has ever
// had, gains a name or a role it did not have, and keeps every distinct observation — so one
// recruiter found on a team page, in a job posting and in a search result is one candidate with
// three pieces of provenance rather than three people. A fact already recorded is never rewritten
// by a weaker one: a stored name is not replaced, and a stored address cannot be replaced at all,
// because the database refuses it.
func (db *DB) SaveContactCandidate(ctx context.Context, in SaveContactCandidateInput) (int64, error) {
	if strings.TrimSpace(in.IdentityKey) == "" {
		return 0, fmt.Errorf("a contact candidate needs a deduplication identity")
	}
	if len(in.Evidence) == 0 {
		return 0, fmt.Errorf("a contact candidate needs at least one piece of evidence")
	}
	status := in.Status
	if status == "" {
		status = "new"
	}
	// Unstated is the weakest reading of each of these, never the strongest. Saying nothing about
	// how well a lead is supported is not the same as saying it is well supported, and an address
	// with no stated provenance is one nobody has been shown to have published.
	if in.Confidence == "" {
		in.Confidence = "low"
	}
	if in.EmailProvenance == "" {
		in.EmailProvenance = "none"
	}
	if in.Verification == "" {
		in.Verification = "unverified"
	}
	// Everything here was scraped from somebody else's page, and a page served as Latin-1 puts
	// bytes in it that are not UTF-8 characters at all. Postgres refuses such a byte outright, so
	// without this one non-breaking space in one excerpt would abort the whole company's search.
	// Sanitizing at the boundary means every caller is covered, not only the careful ones.
	in.PersonName = validText(in.PersonName)
	in.RoleTitle = validText(in.RoleTitle)
	in.EmployerClaimed = validText(in.EmployerClaimed)
	in.FunctionEvidence = validText(in.FunctionEvidence)
	in.RejectedReason = validText(in.RejectedReason)
	in.ApplicationURL = validText(in.ApplicationURL)
	var id int64
	err := db.QueryRow(ctx, `
		INSERT INTO contact_candidate
		  (company_id, identity_key, identity_basis, kind, person_name, role_title, employer_claimed,
		   email, email_provenance, verification, application_url, ats_provider,
		   recipient_function, function_evidence, endpoint_purpose, endpoint_contactable,
		   confidence, best_source_trust, employer_owned_evidence, status, rejected_reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		ON CONFLICT (company_id, identity_key) DO UPDATE SET
		  -- A name or role we did not have is new information; one we already have is not
		  -- overwritten by a later, possibly weaker reading of the same person.
		  person_name = CASE WHEN contact_candidate.person_name = '' THEN EXCLUDED.person_name ELSE contact_candidate.person_name END,
		  role_title = CASE WHEN contact_candidate.role_title = '' THEN EXCLUDED.role_title ELSE contact_candidate.role_title END,
		  employer_claimed = CASE WHEN contact_candidate.employer_claimed = '' THEN EXCLUDED.employer_claimed ELSE contact_candidate.employer_claimed END,
		  application_url = CASE WHEN contact_candidate.application_url = '' THEN EXCLUDED.application_url ELSE contact_candidate.application_url END,
		  ats_provider = CASE WHEN contact_candidate.ats_provider = '' THEN EXCLUDED.ats_provider ELSE contact_candidate.ats_provider END,
		  recipient_function = EXCLUDED.recipient_function,
		  function_evidence = EXCLUDED.function_evidence,
		  endpoint_purpose = EXCLUDED.endpoint_purpose,
		  endpoint_contactable = EXCLUDED.endpoint_contactable,
		  best_source_trust = GREATEST(contact_candidate.best_source_trust, EXCLUDED.best_source_trust),
		  employer_owned_evidence = contact_candidate.employer_owned_evidence OR EXCLUDED.employer_owned_evidence,
		  confidence = EXCLUDED.confidence,
		  -- A promoted or rejected candidate keeps the state a person or a policy put it in;
		  -- rediscovering it does not quietly reopen that decision.
		  status = CASE WHEN contact_candidate.status IN ('promoted','rejected') THEN contact_candidate.status ELSE EXCLUDED.status END,
		  updated_at = now()
		RETURNING id`,
		in.CompanyID, in.IdentityKey, in.IdentityBasis, in.Kind, in.PersonName, in.RoleTitle, in.EmployerClaimed,
		in.Email, in.EmailProvenance, in.Verification, in.ApplicationURL, in.ATSProvider,
		in.RecipientFunction, in.FunctionEvidence, in.EndpointPurpose, in.EndpointContactable,
		in.Confidence, in.BestSourceTrust, in.EmployerOwned, status, in.RejectedReason).Scan(&id)
	if err != nil {
		return 0, err
	}

	for _, evidence := range in.Evidence {
		evidence.Excerpt = validText(evidence.Excerpt)
		evidence.SourceURL = validText(evidence.SourceURL)
		if strings.TrimSpace(evidence.Excerpt) == "" {
			continue // an excerpt that survives sanitizing as nothing supports nothing
		}
		observed := evidence.ObservedAt
		if observed.IsZero() {
			observed = time.Now().UTC()
		}
		_, err := db.Exec(ctx, `
			INSERT INTO contact_candidate_evidence
			  (candidate_id, source_type, source_trust, source_url, excerpt, excerpt_hash,
			   discovery_method, provider, employer_owned, observed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (candidate_id, source_url, excerpt_hash)
			  DO UPDATE SET observed_at = EXCLUDED.observed_at`,
			id, evidence.SourceType, evidence.SourceTrust, evidence.SourceURL, evidence.Excerpt,
			evidence.ExcerptHash, evidence.Method, evidence.Provider, evidence.Employer, observed)
		if err != nil {
			return 0, err
		}
	}
	return id, nil
}

// SupersedeNamedCandidate marks a person-only candidate as superseded once the same person has
// been found again with a published address. The weaker record is kept rather than deleted: it
// is how the person was first found, and deleting it would lose that.
func (db *DB) SupersedeNamedCandidate(ctx context.Context, companyID int64, normalizedName string, keepID int64) error {
	if strings.TrimSpace(normalizedName) == "" {
		return nil
	}
	_, err := db.Exec(ctx, `
		UPDATE contact_candidate
		   SET status='superseded',
		       rejected_reason='The same person was found again with a published address; that candidate supersedes this one.',
		       updated_at=now()
		 WHERE company_id=$1 AND id <> $2 AND identity_basis='person_name'
		   AND status NOT IN ('promoted','rejected','superseded')
		   AND identity_key LIKE 'person:' || $3 || '@%'`,
		companyID, keepID, normalizedName)
	return err
}

// PromoteContactCandidate writes a candidate into the operational contact table and links the
// two, so the contact the send path sees always names the candidate and therefore the evidence
// it came from. An inferred address cannot reach here: the database refuses it.
func (db *DB) PromoteContactCandidate(ctx context.Context, candidateID int64, quality int) (*Contact, error) {
	var companyID int64
	var email, personName, roleTitle, provenance string
	err := db.QueryRow(ctx, `
		SELECT company_id, email, person_name, role_title, email_provenance
		  FROM contact_candidate WHERE id=$1`, candidateID).
		Scan(&companyID, &email, &personName, &roleTitle, &provenance)
	if err != nil {
		return nil, err
	}
	if provenance == "inferred_pattern" {
		return nil, fmt.Errorf("an inferred address is never promoted to a contact")
	}
	if strings.TrimSpace(email) == "" {
		return nil, fmt.Errorf("candidate %d has no address to promote", candidateID)
	}
	var name, role *string
	if personName = strings.TrimSpace(personName); personName != "" {
		name = &personName
	}
	if roleTitle = strings.TrimSpace(roleTitle); roleTitle != "" {
		role = &roleTitle
	}
	contact, err := db.CreateContact(ctx, companyID, "email", email, name, role, quality)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(ctx, `
		UPDATE contact_candidate SET status='promoted', promoted_contact_id=$2, updated_at=now()
		 WHERE id=$1`, candidateID, contact.ID); err != nil {
		return nil, err
	}
	return contact, nil
}

// SetContactCandidateStatus records a non-promotion outcome and why.
func (db *DB) SetContactCandidateStatus(ctx context.Context, candidateID int64, status, reason string) error {
	_, err := db.Exec(ctx, `
		UPDATE contact_candidate SET status=$2, rejected_reason=$3, updated_at=now()
		 WHERE id=$1 AND status NOT IN ('promoted')`, candidateID, status, reason)
	return err
}

// ListContactCandidates returns every candidate for a company with all of its evidence, strongest
// first. Provenance is returned with the candidate rather than behind a second request, because a
// candidate without its provenance is exactly the thing this pipeline is not allowed to produce.
func (db *DB) ListContactCandidates(ctx context.Context, companyID int64) ([]ContactCandidateRow, error) {
	rows, err := db.Query(ctx, `
		SELECT id, company_id, identity_key, identity_basis, kind, person_name, role_title,
		       employer_claimed, email, email_provenance, verification, application_url, ats_provider,
		       recipient_function, function_evidence, endpoint_purpose, endpoint_contactable,
		       confidence, best_source_trust, employer_owned_evidence, status, rejected_reason,
		       promoted_contact_id, discovered_at, updated_at
		  FROM contact_candidate
		 WHERE company_id=$1
		 ORDER BY CASE status WHEN 'promoted' THEN 0 WHEN 'review_required' THEN 1 WHEN 'new' THEN 2 ELSE 3 END,
		          best_source_trust DESC, id`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContactCandidateRow{}
	index := map[int64]int{}
	for rows.Next() {
		var c ContactCandidateRow
		if err := rows.Scan(&c.ID, &c.CompanyID, &c.IdentityKey, &c.IdentityBasis, &c.Kind,
			&c.PersonName, &c.RoleTitle, &c.EmployerClaimed, &c.Email, &c.EmailProvenance,
			&c.Verification, &c.ApplicationURL, &c.ATSProvider, &c.RecipientFunction,
			&c.FunctionEvidence, &c.EndpointPurpose, &c.EndpointContactable, &c.Confidence,
			&c.BestSourceTrust, &c.EmployerOwned, &c.Status, &c.RejectedReason,
			&c.PromotedContactID, &c.DiscoveredAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		index[c.ID] = len(out)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	ids := make([]int64, 0, len(out))
	for _, c := range out {
		ids = append(ids, c.ID)
	}
	evidenceRows, err := db.Query(ctx, `
		SELECT id, candidate_id, source_type, source_trust, source_url, excerpt,
		       discovery_method, provider, employer_owned, observed_at
		  FROM contact_candidate_evidence
		 WHERE candidate_id = ANY($1)
		 ORDER BY source_trust DESC, observed_at DESC, id`, ids)
	if err != nil {
		return nil, err
	}
	defer evidenceRows.Close()
	for evidenceRows.Next() {
		var candidateID int64
		var e ContactCandidateEvidenceRow
		if err := evidenceRows.Scan(&e.ID, &candidateID, &e.SourceType, &e.Trust, &e.SourceURL,
			&e.Excerpt, &e.Method, &e.Provider, &e.Employer, &e.ObservedAt); err != nil {
			return nil, err
		}
		if position, ok := index[candidateID]; ok {
			out[position].Evidence = append(out[position].Evidence, e)
		}
	}
	return out, evidenceRows.Err()
}

// ContactDiscoveryStats is the operating picture for the dashboard. Every number here is
// something an operator can act on: what is blocked, what is queued, and what searching has
// actually produced.
type ContactDiscoveryStats struct {
	StrongMatchCompanies int `json:"strong_match_companies"`
	QueuedPending        int `json:"contact_searches_pending"`
	InFlight             int `json:"contact_searches_in_flight"`
	CompletedToday       int `json:"contact_searches_completed_today"`
	NoResult             int `json:"contact_searches_no_result"`
	Failed               int `json:"contact_searches_failed"`
	IdentityBlocked      int `json:"contact_searches_identity_blocked"`
	RecruitingFound      int `json:"recruiting_contacts_found"`
	EngineeringFound     int `json:"engineering_contacts_found"`
	ApplicationRoutes    int `json:"published_application_routes_found"`
	PeopleWithoutEmail   int `json:"people_found_without_email"`
	InferredAwaiting     int `json:"inferred_addresses_awaiting_review"`
	PromotedContacts     int `json:"candidates_promoted_to_contacts"`
}

// ContactDiscoveryCounts runs the dashboard counts. Each is a separate explicit query for the
// same reason the rest of the dashboard is: a reader must be able to check what a number means.
func (db *DB) ContactDiscoveryCounts(ctx context.Context) (*ContactDiscoveryStats, error) {
	s := &ContactDiscoveryStats{}
	counts := []struct {
		target *int
		query  string
	}{
		{&s.StrongMatchCompanies, `SELECT count(DISTINCT o.company_id) FROM opportunity o
		   JOIN opportunity_match m ON m.opportunity_id=o.id AND m.opportunity_revision=o.current_revision
		  WHERE m.classification='strong_match' AND o.availability_status <> 'closed' AND o.selection_status <> 'ignored'`},
		{&s.QueuedPending, `SELECT count(*) FROM contact_discovery_task WHERE state='pending'`},
		{&s.InFlight, `SELECT count(*) FROM contact_discovery_task WHERE state IN ('claimed','searching')`},
		{&s.CompletedToday, `SELECT count(*) FROM contact_discovery_task
		  WHERE last_searched_at::date = now()::date AND state IN ('completed','partial')`},
		{&s.NoResult, `SELECT count(*) FROM contact_discovery_task WHERE state='no_result'`},
		{&s.Failed, `SELECT count(*) FROM contact_discovery_task WHERE state='failed'`},
		{&s.IdentityBlocked, `SELECT count(*) FROM contact_discovery_task WHERE state='identity_blocked'`},
		{&s.RecruitingFound, `SELECT count(*) FROM contact_candidate
		  WHERE recipient_function='recruiting' AND status IN ('new','review_required','promoted')`},
		{&s.EngineeringFound, `SELECT count(*) FROM contact_candidate
		  WHERE recipient_function IN ('engineering','leadership') AND status IN ('new','review_required','promoted')`},
		{&s.ApplicationRoutes, `SELECT count(*) FROM contact_candidate
		  WHERE kind IN ('application_url','application_email') AND status <> 'rejected'`},
		{&s.PeopleWithoutEmail, `SELECT count(*) FROM contact_candidate
		  WHERE kind='person' AND email='' AND status NOT IN ('rejected','superseded')`},
		{&s.InferredAwaiting, `SELECT count(*) FROM contact_candidate
		  WHERE email_provenance='inferred_pattern' AND status NOT IN ('rejected','superseded')`},
		{&s.PromotedContacts, `SELECT count(*) FROM contact_candidate WHERE status='promoted'`},
	}
	for _, c := range counts {
		if err := db.QueryRow(ctx, c.query).Scan(c.target); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// ContactDiscoveryStateByCompany returns the discovery state for a set of companies, for list
// views that need to show it without a query per row.
func (db *DB) ContactDiscoveryStateByCompany(ctx context.Context, companyIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(companyIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `SELECT company_id, state FROM contact_discovery_task WHERE company_id = ANY($1)`, companyIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, err
		}
		out[id] = state
	}
	return out, rows.Err()
}

// validText replaces any byte sequence that is not valid UTF-8, so a scraped fragment can be
// stored as text. The replacement character is deliberate: it records that the source contained
// something unreadable rather than pretending the text was always clean.
func validText(text string) string {
	if utf8.ValidString(text) {
		return text
	}
	return strings.ToValidUTF8(text, "\uFFFD")
}

func nullableID(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}

// IdentityBlockedCompanies returns up to limit companies whose contact search is refused until
// their employer identity (in practice, their domain) is corrected, most recently blocked first.
func (db *DB) IdentityBlockedCompanies(ctx context.Context, limit int) ([]int64, error) {
	rows, err := db.Query(ctx, `SELECT company_id FROM contact_discovery_task
		 WHERE state='identity_blocked' ORDER BY updated_at DESC, company_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RequeueBlockedContactDiscovery puts a company whose search was refused for identity back in the
// queue, for when a person has just corrected or confirmed its domain. The block was a judgement
// about the old domain, so it no longer stands; the next search re-checks identity against the new
// one and blocks again if it still does not hold. Other states are left alone.
func (db *DB) RequeueBlockedContactDiscovery(ctx context.Context, companyID int64) error {
	_, err := db.Exec(ctx, `UPDATE contact_discovery_task
		   SET state='pending', next_eligible_at=now(), failure_reason='', updated_at=now()
		 WHERE company_id=$1 AND state='identity_blocked'`, companyID)
	return err
}
