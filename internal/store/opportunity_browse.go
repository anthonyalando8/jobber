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
	// Access narrows by whether someone applying from Kenya is eligible: "accessible" means open or
	// likely, otherwise one exact kenya_access value. Route and Cost select one exact value, except
	// Cost "not_paid" which hides listings with a stated payment requirement.
	Access, Route, Cost string
	// Tracking selects on the operator's recorded progress: "active" is anything saved or in
	// progress, "none" is untracked, otherwise one exact state.
	Tracking string
	// Contract keeps only contract, freelance and part-time work.
	Contract bool
}
type OpportunityListRow struct {
	Opportunity
	Fit, Selection, Availability, Description string
	MatchScore                                int
	// Suppressed and its reason, so a row that is only visible because the operator asked to see
	// suppressed postings says why it was dropped rather than looking like live work.
	Suppressed                          bool
	SuppressionReason, SuppressionBasis string
	// Access is the stored eligibility and application-route classification; empty fields mean
	// the listing has not been classified yet.
	Access opportunity.Access
	// Tracking is the operator's recorded progress, empty when untracked.
	Tracking string
	// Pursuit is the "best to pursue" score, see opportunity.PursuitScore.
	Pursuit int
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
const opportunityBrowseFrom = ` FROM opportunity o JOIN company c ON c.id=o.company_id ` + currentMatchJoin + `
 LEFT JOIN opportunity_access a ON a.opportunity_id=o.id LEFT JOIN opportunity_tracking tr ON tr.opportunity_id=o.id `

// pursuitScoreSQL is opportunity.PursuitScore in SQL, so the list can be ordered by it in the
// database. TestPursuitScoreSQLMatchesGo keeps the two in step.
const pursuitScoreSQL = `(CASE COALESCE(m.classification,'unassessed') WHEN 'strong_match' THEN 40 WHEN 'possible_match' THEN 25 WHEN 'unrelated' THEN 0 ELSE 12 END
 + CASE COALESCE(a.kenya_access,'unknown') WHEN 'open' THEN 25 WHEN 'likely' THEN 18 WHEN 'restricted' THEN 0 ELSE 10 END
 + CASE COALESCE(a.route,'unknown') WHEN 'direct_contact' THEN 20 WHEN 'employer_site' THEN 15 WHEN 'employer_ats' THEN 15 WHEN 'job_board' THEN 9 ELSE 5 END
 + CASE COALESCE(a.cost,'unknown') WHEN 'free' THEN 5 WHEN 'registration' THEN 1 WHEN 'paid' THEN -15 ELSE 3 END
 + CASE WHEN COALESCE(a.contract,false) THEN 5 ELSE 0 END
 + CASE WHEN now()-COALESCE(o.published_at,o.discovered_at) <= interval '7 days' THEN 5
        WHEN now()-COALESCE(o.published_at,o.discovered_at) <= interval '30 days' THEN 2
        WHEN now()-COALESCE(o.published_at,o.discovered_at) > interval '60 days' THEN -5 ELSE 0 END)`
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
 AND ($14='' OR $14='any' OR ($14='hidden' AND NOT ` + liveSuppression + `) OR ($14='only' AND ` + liveSuppression + `))
 AND ($15='' OR ($15='accessible' AND a.kenya_access IN ('open','likely')) OR a.kenya_access=$15)
 AND ($16='' OR a.route=$16)
 AND ($17='' OR ($17='not_paid' AND COALESCE(a.cost,'unknown')<>'paid') OR a.cost=$17)
 AND ($18='' OR ($18='active' AND tr.state IN ('saved','applied','interviewing','offer')) OR ($18='none' AND tr.state IS NULL) OR tr.state=$18)
 AND (NOT $19 OR a.contract) `

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
	return []any{opportunity.MatcherVersion, likeSearch(f.Query), f.Fit, f.Assessment, f.Source, f.CompanyID, f.Availability, f.Description, f.Selection, likeSearch(opportunity.NormalizeTitle(f.Title)), f.Priority, f.Recipient, f.ContactSearch, f.Suppressed,
		f.Access, f.Route, f.Cost, f.Tracking, f.Contract}
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
		"pursue": pursuitScoreSQL + " DESC,o.discovered_at DESC,o.id DESC",
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
 COALESCE((SELECT s.basis FROM opportunity_suppression s WHERE s.opportunity_id=o.id AND s.superseded_at IS NULL),''),
 COALESCE(a.eligibility,''),COALESCE(a.kenya_access,''),COALESCE(a.eligibility_evidence,''),COALESCE(a.route,''),COALESCE(a.route_url,''),COALESCE(a.cost,''),COALESCE(a.cost_evidence,''),COALESCE(a.contract,false),
 COALESCE(tr.state,''),`+pursuitScoreSQL+opportunityBrowseFrom+` JOIN opportunity_revision r ON r.opportunity_id=o.id AND r.revision=o.current_revision `+opportunityBrowseWhere+` ORDER BY `+order+` LIMIT $20 OFFSET $21`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpportunityListRow{}
	for rows.Next() {
		var item OpportunityListRow
		var raw []byte
		if err = rows.Scan(&item.ID, &item.CompanyID, &item.CompanyName, &item.Revision, &raw, &item.DiscoveredAt, &item.LastSeenAt, &item.UpdatedAt, &item.Fit, &item.MatchScore, &item.Selection, &item.Availability, &item.Description,
			&item.Suppressed, &item.SuppressionReason, &item.SuppressionBasis,
			&item.Access.Eligibility, &item.Access.KenyaAccess, &item.Access.EligibilityEvidence, &item.Access.Route, &item.Access.RouteURL, &item.Access.Cost, &item.Access.CostEvidence, &item.Access.Contract,
			&item.Tracking, &item.Pursuit); err != nil {
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
	// Accessible counts live listings open or likely open to an applicant in Kenya, Direct those
	// with a published email to apply to, and Tracked those the operator is actively pursuing.
	Accessible, Direct, Tracked int
}

func (db *DB) OpportunityFacets(ctx context.Context) (OpportunityFacets, error) {
	var f OpportunityFacets
	err := db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE m.classification='strong_match'),count(*) FILTER(WHERE m.classification='possible_match'),count(*) FILTER(WHERE m.classification='unrelated'),count(*) FILTER(WHERE m.id IS NULL),count(*) FILTER(WHERE o.selection_status='review_required'),count(*) FILTER(WHERE `+liveSuppression+`),COALESCE(array_agg(DISTINCT o.source) FILTER(WHERE o.source<>''),'{}'),
 count(*) FILTER(WHERE a.kenya_access IN ('open','likely') AND NOT `+liveSuppression+`),count(*) FILTER(WHERE a.route='direct_contact' AND NOT `+liveSuppression+`),
 count(*) FILTER(WHERE tr.state IN ('saved','applied','interviewing','offer'))`+opportunityBrowseFrom, opportunity.MatcherVersion).Scan(&f.Strong, &f.Possible, &f.Unrelated, &f.Unassessed, &f.Review, &f.Suppressed, &f.Sources, &f.Accessible, &f.Direct, &f.Tracked)
	return f, err
}
