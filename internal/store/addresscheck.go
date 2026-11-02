package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AddressCheckSubject is one discovered address waiting for a person, with what a check needs
// to judge it: the company it is meant to belong to, how it was found, and the other addresses
// the same company is already known to use.
type AddressCheckSubject struct {
	CandidateID   int64
	CompanyID     int64
	CompanyName   string
	CompanyDomain string
	Email         string
	PersonName    string
	RoleTitle     string
	Provenance    string
	Function      string
	// Evidence holds the strongest excerpts behind the candidate, verbatim.
	Evidence []string
	// KnownAddresses are addresses at this company that the employer or the job source published.
	KnownAddresses []string
}

// AddressCheckItem is one finding of a check.
type AddressCheckItem struct {
	Code   string `json:"code"`
	Result string `json:"result"` // pass | warn | fail
	Text   string `json:"text"`
}

// AddressCheck is one stored check of a candidate's address.
type AddressCheck struct {
	ID          int64
	CandidateID int64
	Email       string
	Version     string
	Verdict     string
	MailServers string
	Checks      []AddressCheckItem
	AIVerdict   string
	AIReasons   []string
	AIModel     string
	CheckedBy   string
	CheckedAt   time.Time
}

// Address check verdicts, as stored.
const (
	AddressLooksDeliverable = "looks_deliverable"
	AddressDoubtful         = "doubtful"
	AddressWillNotReceive   = "will_not_receive"
	AIAddressNotRun         = "not_run"
)

// AddressCheckSubjects lists review-required candidates with an address that have no check of
// this version yet. A candidate is checked once per version; a person can ask again by hand.
func (db *DB) AddressCheckSubjects(ctx context.Context, version string, limit int) ([]AddressCheckSubject, error) {
	return db.addressSubjects(ctx, `
		  AND NOT EXISTS (SELECT 1 FROM contact_address_check k
		                   WHERE k.candidate_id=c.id AND k.check_version=$1 AND k.email=c.email)`, version, limit)
}

// AIAddressSubjects lists review-required candidates whose latest check of this version found no
// hard fault and has no AI opinion yet. Each address gets at most one AI opinion per version.
func (db *DB) AIAddressSubjects(ctx context.Context, version string, limit int) ([]AddressCheckSubject, error) {
	return db.addressSubjects(ctx, `
		  AND EXISTS (SELECT 1 FROM contact_address_check k
		               WHERE k.candidate_id=c.id AND k.check_version=$1 AND k.email=c.email
		                 AND k.verdict <> 'will_not_receive')
		  AND NOT EXISTS (SELECT 1 FROM contact_address_check k
		                   WHERE k.candidate_id=c.id AND k.check_version=$1 AND k.email=c.email
		                     AND k.ai_verdict <> 'not_run')`, version, limit)
}

// AddressCheckSubject loads one candidate for a check a person asked for. Any status with an
// address qualifies, so a person can look again at one they already decided.
func (db *DB) AddressCheckSubject(ctx context.Context, candidateID int64) (*AddressCheckSubject, error) {
	rows, err := db.querySubjects(ctx, `c.id=$1 AND c.email <> ''`, []any{candidateID}, 1)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, pgx.ErrNoRows
	}
	return &rows[0], nil
}

func (db *DB) addressSubjects(ctx context.Context, extra, version string, limit int) ([]AddressCheckSubject, error) {
	return db.querySubjects(ctx, `c.status='review_required' AND c.email <> ''`+extra, []any{version}, limit)
}

func (db *DB) querySubjects(ctx context.Context, where string, args []any, limit int) ([]AddressCheckSubject, error) {
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := db.Query(ctx, fmt.Sprintf(`
		SELECT c.id, c.company_id, co.name, COALESCE(co.domain,''), c.email, c.person_name, c.role_title,
		       c.email_provenance, c.recipient_function
		  FROM contact_candidate c JOIN company co ON co.id=c.company_id
		 WHERE %s
		 ORDER BY c.best_source_trust DESC, c.id
		 LIMIT $%d`, where, len(args)), args...)
	if err != nil {
		return nil, err
	}
	out := []AddressCheckSubject{}
	for rows.Next() {
		var s AddressCheckSubject
		if err := rows.Scan(&s.CandidateID, &s.CompanyID, &s.CompanyName, &s.CompanyDomain, &s.Email,
			&s.PersonName, &s.RoleTitle, &s.Provenance, &s.Function); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := db.fillSubject(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (db *DB) fillSubject(ctx context.Context, s *AddressCheckSubject) error {
	rows, err := db.Query(ctx, `
		SELECT excerpt FROM contact_candidate_evidence
		 WHERE candidate_id=$1 ORDER BY source_trust DESC, observed_at DESC LIMIT 3`, s.CandidateID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var excerpt string
		if err := rows.Scan(&excerpt); err != nil {
			rows.Close()
			return err
		}
		s.Evidence = append(s.Evidence, excerpt)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Published addresses only: an inferred one is a guess, and a guess is no evidence of format.
	rows, err = db.Query(ctx, `
		SELECT DISTINCT lower(email) FROM contact_candidate
		 WHERE company_id=$1 AND id<>$2 AND email<>'' AND email_provenance IN ('source_published','employer_published')
		 UNION
		SELECT DISTINCT lower(value) FROM contact
		 WHERE company_id=$1 AND kind='email' AND COALESCE(status,'active')='active'
		 LIMIT 20`, s.CompanyID, s.CandidateID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			return err
		}
		if !strings.EqualFold(address, s.Email) {
			s.KnownAddresses = append(s.KnownAddresses, address)
		}
	}
	return rows.Err()
}

// RecordAddressCheck stores one check. Rows are never updated: a later check is a new row.
func (db *DB) RecordAddressCheck(ctx context.Context, c AddressCheck) (int64, error) {
	if c.AIVerdict == "" {
		c.AIVerdict = AIAddressNotRun
	}
	if c.Checks == nil {
		c.Checks = []AddressCheckItem{}
	}
	if c.AIReasons == nil {
		c.AIReasons = []string{}
	}
	checks, err := json.Marshal(c.Checks)
	if err != nil {
		return 0, err
	}
	reasons, err := json.Marshal(c.AIReasons)
	if err != nil {
		return 0, err
	}
	var id int64
	err = db.QueryRow(ctx, `
		INSERT INTO contact_address_check
		  (candidate_id, email, check_version, verdict, mail_servers, checks, ai_verdict, ai_reasons, ai_model, checked_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		c.CandidateID, c.Email, c.Version, c.Verdict, c.MailServers, checks, c.AIVerdict, reasons,
		c.AIModel, c.CheckedBy).Scan(&id)
	return id, err
}

// LatestAddressChecks returns the newest check of each candidate's current address.
func (db *DB) LatestAddressChecks(ctx context.Context, candidateIDs []int64) (map[int64]AddressCheck, error) {
	out := map[int64]AddressCheck{}
	if len(candidateIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `
		SELECT DISTINCT ON (k.candidate_id) k.id, k.candidate_id, k.email, k.check_version, k.verdict,
		       k.mail_servers, k.checks, k.ai_verdict, k.ai_reasons, k.ai_model, k.checked_by, k.checked_at
		  FROM contact_address_check k JOIN contact_candidate c ON c.id=k.candidate_id AND c.email=k.email
		 WHERE k.candidate_id = ANY($1)
		 ORDER BY k.candidate_id, k.checked_at DESC, k.id DESC`, candidateIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c AddressCheck
		var checks, reasons []byte
		if err := rows.Scan(&c.ID, &c.CandidateID, &c.Email, &c.Version, &c.Verdict, &c.MailServers,
			&checks, &c.AIVerdict, &reasons, &c.AIModel, &c.CheckedBy, &c.CheckedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(checks, &c.Checks)
		_ = json.Unmarshal(reasons, &c.AIReasons)
		out[c.CandidateID] = c
	}
	return out, rows.Err()
}

// AIAddressChecksSince counts AI opinions given since a time, for the daily cap.
func (db *DB) AIAddressChecksSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM contact_address_check WHERE ai_verdict <> 'not_run' AND checked_at >= $1`, since).Scan(&n)
	return n, err
}

// AIRequestsLastHourAndDay counts every AI request the system made: email composition and review
// calls plus address opinions. Address checks use it to stay inside the shared AI budget.
func (db *DB) AIRequestsLastHourAndDay(ctx context.Context) (hour, day int, err error) {
	err = db.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM composition_call WHERE started_at > now()-interval '1 hour')
		     + (SELECT count(*) FROM contact_address_check WHERE ai_verdict <> 'not_run' AND checked_at > now()-interval '1 hour'),
		       (SELECT count(*) FROM composition_call WHERE started_at > now()-interval '24 hours')
		     + (SELECT count(*) FROM contact_address_check WHERE ai_verdict <> 'not_run' AND checked_at > now()-interval '24 hours')
		     + (SELECT COALESCE(sum(remaining),0) FROM composition_reservation WHERE expires_at > now())`).Scan(&hour, &day)
	return hour, day, err
}

// AcceptContactCandidate is a person accepting an address that needed review. The candidate
// becomes a contact the send path can use, and records who accepted it. This is the only way an
// inferred address becomes a contact; no check, deterministic or AI, can do it on its own.
func (db *DB) AcceptContactCandidate(ctx context.Context, candidateID int64, by string) (*Contact, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return nil, fmt.Errorf("acceptance needs the person who accepted")
	}
	var companyID int64
	var email, personName, roleTitle, status, function string
	if err := db.QueryRow(ctx, `
		SELECT company_id, email, person_name, role_title, status, recipient_function
		  FROM contact_candidate WHERE id=$1`, candidateID).
		Scan(&companyID, &email, &personName, &roleTitle, &status, &function); err != nil {
		return nil, err
	}
	if status != "review_required" {
		return nil, fmt.Errorf("only an address waiting for review can be accepted (this one is %s)", status)
	}
	if strings.TrimSpace(email) == "" {
		return nil, fmt.Errorf("candidate %d has no address to accept", candidateID)
	}
	quality := 2
	var name, role *string
	if personName = strings.TrimSpace(personName); personName != "" {
		name, quality = &personName, 3
	} else if function == "generic" {
		quality = 1
	}
	if roleTitle = strings.TrimSpace(roleTitle); roleTitle != "" {
		role = &roleTitle
	}
	contact, err := db.CreateContact(ctx, companyID, "email", email, name, role, quality)
	if err != nil {
		return nil, err
	}
	tag, err := db.Exec(ctx, `
		UPDATE contact_candidate
		   SET status='promoted', promoted_contact_id=$2, accepted_by=$3, accepted_at=now(),
		       rejected_reason='', updated_at=now()
		 WHERE id=$1 AND status='review_required'`, candidateID, contact.ID, by)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, fmt.Errorf("candidate %d was decided by someone else first", candidateID)
	}
	return contact, nil
}

// RejectContactCandidate is a person rejecting an address that needed review. The candidate and
// its evidence are kept, so rediscovering it does not reopen the decision.
func (db *DB) RejectContactCandidate(ctx context.Context, candidateID int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "Rejected by the operator after review."
	}
	tag, err := db.Exec(ctx, `
		UPDATE contact_candidate SET status='rejected', rejected_reason=$2, updated_at=now()
		 WHERE id=$1 AND status='review_required'`, candidateID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("only an address waiting for review can be rejected")
	}
	return nil
}

// AddressReviewCounts is the dashboard's picture of addresses waiting for a person.
type AddressReviewCounts struct {
	Waiting, Unchecked, LooksDeliverable, Doubtful, WillNotReceive int
}

// AddressReviewStats counts review-required addresses by their latest check.
func (db *DB) AddressReviewStats(ctx context.Context) (AddressReviewCounts, error) {
	var c AddressReviewCounts
	err := db.QueryRow(ctx, `
		WITH latest AS (
		  SELECT DISTINCT ON (k.candidate_id) k.candidate_id, k.verdict
		    FROM contact_address_check k JOIN contact_candidate c ON c.id=k.candidate_id AND c.email=k.email
		   ORDER BY k.candidate_id, k.checked_at DESC, k.id DESC)
		SELECT count(*),
		       count(*) FILTER (WHERE l.verdict IS NULL),
		       count(*) FILTER (WHERE l.verdict='looks_deliverable'),
		       count(*) FILTER (WHERE l.verdict='doubtful'),
		       count(*) FILTER (WHERE l.verdict='will_not_receive')
		  FROM contact_candidate c LEFT JOIN latest l ON l.candidate_id=c.id
		 WHERE c.status='review_required' AND c.email <> ''`).
		Scan(&c.Waiting, &c.Unchecked, &c.LooksDeliverable, &c.Doubtful, &c.WillNotReceive)
	return c, err
}

// AddressReviewCompany is a company with addresses waiting for a person.
type AddressReviewCompany struct {
	CompanyID int64
	Name      string
	Waiting   int
}

// AddressReviewCompanies lists companies with waiting addresses, most first.
func (db *DB) AddressReviewCompanies(ctx context.Context, limit int) ([]AddressReviewCompany, error) {
	rows, err := db.Query(ctx, `
		SELECT co.id, co.name, count(*)
		  FROM contact_candidate c JOIN company co ON co.id=c.company_id
		 WHERE c.status='review_required' AND c.email <> ''
		 GROUP BY co.id, co.name
		 ORDER BY count(*) DESC, co.name
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AddressReviewCompany{}
	for rows.Next() {
		var c AddressReviewCompany
		if err := rows.Scan(&c.CompanyID, &c.Name, &c.Waiting); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ContactFromInferredAddress reports whether a contact began as an inferred address that a person
// accepted. Unreadable counts as inferred: automation must fail closed.
func (db *DB) ContactFromInferredAddress(ctx context.Context, contactID int64) bool {
	var inferred bool
	err := db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM contact_candidate
		                WHERE promoted_contact_id=$1 AND email_provenance='inferred_pattern')`, contactID).Scan(&inferred)
	return err != nil || inferred
}
