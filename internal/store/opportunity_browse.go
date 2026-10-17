package store

import (
	"context"
	"encoding/json"
	"github.com/anthonyalando8/prospector/internal/opportunity"
)

type OpportunityListFilter struct {
	Query, Fit, Assessment, Source, Availability, Description, Selection, Title, Sort string
	// Recipient and ContactSearch answer the question this browse view could not previously
	// answer: which relevant opportunities are actually blocked on having nowhere to send.
	// "acceptable" means an identified recruiting or engineering mailbox, or a published
	// application route; "missing" is the state the contact-discovery worker exists to fix.
	Recipient, ContactSearch string
	// Suppressed selects on whether a live suppression stands: "hidden" leaves suppressed
	// postings out, "only" shows just them, "any" or empty shows everything. Empty is "any" so
	// that a zero filter still means the whole workspace; the browse page asks for "hidden".
	Suppressed string
	CompanyID  int64
	Priority   bool
}
type OpportunityListRow struct {
	Opportunity
	Fit, Selection, Availability, Description string
	MatchScore                                int
	// Suppressed and its reason, so a row that is only visible because the operator asked to see
	// suppressed postings says why it was dropped rather than looking like live work.
	Suppressed                          bool
	SuppressionReason, SuppressionBasis string
}

// SuppressionLabel is the suppression reason in words, for the list badge. The table shows a slug
// nowhere else, and "no_longer_hiring" in a cell reads as a database value rather than a decision.
func (r OpportunityListRow) SuppressionLabel() string {
	if !r.Suppressed {
		return ""
	}
	return OpportunitySuppression{Reason: r.SuppressionReason}.ReasonLabel()
}

// All browse predicates are fixed SQL. Only named, server-owned sort expressions vary.
const currentMatchJoin = ` LEFT JOIN opportunity_match m ON m.opportunity_id=o.id AND m.opportunity_revision=o.current_revision
 AND m.profile_revision_id=(SELECT active_revision_id FROM candidate_profile WHERE id=1) AND m.matcher_version=$1 `
const opportunityBrowseFrom = ` FROM opportunity o JOIN company c ON c.id=o.company_id ` + currentMatchJoin
const opportunityBrowseWhere = ` WHERE
 ($2='' OR o.title ILIKE $2 OR c.name ILIKE $2 OR o.location ILIKE $2 OR o.source ILIKE $2 OR o.source_key ILIKE $2 OR o.external_id ILIKE $2)
 AND ($3='' OR m.classification=$3)
 AND ($4='' OR ($4='assessed' AND m.id IS NOT NULL) OR ($4='unassessed' AND m.id IS NULL) OR ($4='review_required' AND o.selection_status='review_required'))
 AND ($5='' OR o.source=$5) AND ($6=0 OR o.company_id=$6)
 AND ($7='' OR o.availability_status=$7) AND ($8='' OR o.description_status=$8)
 AND ($9='' OR o.selection_status=$9) AND ($10='' OR o.normalized_title ILIKE $10)
 AND (NOT $11 OR (o.availability_status<>'closed' AND o.selection_status<>'ignored' AND (o.selection_status='review_required' OR m.classification IN ('strong_match','possible_match'))))
 AND ($12='' OR ($12='acceptable' AND ` + acceptableDestination + `)
              OR ($12='missing' AND NOT ` + acceptableDestination + ` AND NOT ` + genericOnly + `)
              OR ($12='generic' AND ` + genericOnly + `))
 AND ($13='' OR EXISTS(SELECT 1 FROM contact_discovery_task t WHERE t.company_id=o.company_id AND t.state=$13))
 AND ($14='' OR $14='any' OR ($14='hidden' AND NOT ` + liveSuppression + `) OR ($14='only' AND ` + liveSuppression + `)) `

// liveSuppression is "a suppression stands against this posting right now" — the row the partial
// unique index guarantees is at most one. A posting is suppressed because it expired, was filled
// or was taken down, so the browse page hides these by default: leaving them in the list is the
// bug this filter exists to fix.
const liveSuppression = `EXISTS(SELECT 1 FROM opportunity_suppression s
   WHERE s.opportunity_id=o.id AND s.superseded_at IS NULL)`

// acceptableDestination is the SQL form of "there is somewhere acceptable to send this".
//
// It deliberately mirrors internal/plan's rules rather than inventing a looser version for the
// sake of a fast query: an identified recruiting or engineering mailbox that is not a machine
// endpoint, or an application route the employer published — whether the job board recorded it or
// contact discovery read it off the employer's own careers page, since those are the same fact
// found in two places. The Go rules remain authoritative — this is a browse filter, and the detail
// pages recompute the real answer — but a filter that disagreed with the policy would send an
// operator to the wrong list.
const acceptableDestination = `(
  COALESCE(o.metadata->>'apply_url','') <> ''
  OR EXISTS(SELECT 1 FROM contact_candidate cc WHERE cc.company_id=o.company_id
              AND cc.kind='application_url' AND cc.status <> 'rejected' AND cc.employer_owned_evidence)
  OR EXISTS(SELECT 1 FROM contact ct WHERE ct.company_id=o.company_id AND ct.kind='email'
              AND ct.status='active' AND ct.value NOT LIKE '%@%sentry.io'
              AND split_part(ct.value,'@',1) NOT IN ('noreply','no-reply','donotreply','postmaster','bounce')
              AND (ct.person_name IS NOT NULL AND ct.person_name <> ''
                   OR split_part(ct.value,'@',1) IN ('careers','jobs','recruiting','recruitment','talent','hiring','hr','engineering','dev','tech','cto')))
)`

// genericOnly is "there are addresses, but every one of them is a generic mailbox" — a distinct
// state from having nothing, because it calls for finding a named person rather than finding an
// address.
const genericOnly = `(
  NOT ` + acceptableDestination + `
  AND EXISTS(SELECT 1 FROM contact ct WHERE ct.company_id=o.company_id AND ct.kind='email' AND ct.status='active')
)`

func likeSearch(value string) string {
	if value == "" {
		return ""
	}
	return "%" + value + "%"
}
func (f OpportunityListFilter) args() []any {
	return []any{opportunity.MatcherVersion, likeSearch(f.Query), f.Fit, f.Assessment, f.Source, f.CompanyID, f.Availability, f.Description, f.Selection, likeSearch(opportunity.NormalizeTitle(f.Title)), f.Priority, f.Recipient, f.ContactSearch, f.Suppressed}
}
func (db *DB) CountOpportunityList(ctx context.Context, f OpportunityListFilter) (int, error) {
	var total int
	err := db.QueryRow(ctx, `SELECT count(*)`+opportunityBrowseFrom+opportunityBrowseWhere, f.args()...).Scan(&total)
	return total, err
}
func (db *DB) ListOpportunityPage(ctx context.Context, f OpportunityListFilter, limit, offset int) ([]OpportunityListRow, error) {
	if limit < 1 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	classOrder := `CASE m.classification WHEN 'strong_match' THEN 0 WHEN 'possible_match' THEN 1 WHEN 'unrelated' THEN 2 ELSE 3 END`
	sorts := map[string]string{
		"updated": "o.updated_at DESC,o.id DESC", "newest": "o.discovered_at DESC,o.id DESC",
		"best":     classOrder + ",COALESCE(m.score,0) DESC,o.updated_at DESC,o.id DESC",
		"priority": "CASE WHEN o.selection_status='review_required' THEN 0 WHEN m.classification='strong_match' THEN 1 ELSE 2 END,o.updated_at DESC,o.id DESC",
		"company":  "lower(c.name),o.updated_at DESC,o.id DESC", "title": "o.normalized_title,o.updated_at DESC,o.id DESC",
	}
	order, ok := sorts[f.Sort]
	if !ok {
		order = sorts["updated"]
	}
	// For a search without an explicit sort, title hits precede company then location/source.
	if f.Query != "" && f.Sort == "" {
		order = `CASE WHEN o.title ILIKE $2 THEN 0 WHEN c.name ILIKE $2 THEN 1 ELSE 2 END,` + order
	}
	args := append(f.args(), limit, offset)
	rows, err := db.Query(ctx, `SELECT o.id,o.company_id,c.name,o.current_revision,r.document,o.discovered_at,o.last_seen_at,o.updated_at,COALESCE(m.classification,'unassessed'),COALESCE(m.score,0),o.selection_status,o.availability_status,o.description_status,
 `+liveSuppression+`,
 COALESCE((SELECT s.reason FROM opportunity_suppression s WHERE s.opportunity_id=o.id AND s.superseded_at IS NULL),''),
 COALESCE((SELECT s.basis FROM opportunity_suppression s WHERE s.opportunity_id=o.id AND s.superseded_at IS NULL),'')`+opportunityBrowseFrom+` JOIN opportunity_revision r ON r.opportunity_id=o.id AND r.revision=o.current_revision `+opportunityBrowseWhere+` ORDER BY `+order+` LIMIT $15 OFFSET $16`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpportunityListRow{}
	for rows.Next() {
		var item OpportunityListRow
		var raw []byte
		if err = rows.Scan(&item.ID, &item.CompanyID, &item.CompanyName, &item.Revision, &raw, &item.DiscoveredAt, &item.LastSeenAt, &item.UpdatedAt, &item.Fit, &item.MatchScore, &item.Selection, &item.Availability, &item.Description,
			&item.Suppressed, &item.SuppressionReason, &item.SuppressionBasis); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &item.Facts); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type OpportunityFacets struct {
	Strong, Possible, Unrelated, Unassessed, Review int
	// Suppressed is how many postings are currently held back as expired, filled or taken down.
	// The browse page hides them, so it has to be able to say how many it is hiding.
	Suppressed int
	Sources    []string
}

func (db *DB) OpportunityFacets(ctx context.Context) (OpportunityFacets, error) {
	var f OpportunityFacets
	err := db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE m.classification='strong_match'),count(*) FILTER(WHERE m.classification='possible_match'),count(*) FILTER(WHERE m.classification='unrelated'),count(*) FILTER(WHERE m.id IS NULL),count(*) FILTER(WHERE o.selection_status='review_required'),count(*) FILTER(WHERE `+liveSuppression+`),COALESCE(array_agg(DISTINCT o.source) FILTER(WHERE o.source<>''),'{}')`+opportunityBrowseFrom, opportunity.MatcherVersion).Scan(&f.Strong, &f.Possible, &f.Unrelated, &f.Unassessed, &f.Review, &f.Suppressed, &f.Sources)
	return f, err
}
