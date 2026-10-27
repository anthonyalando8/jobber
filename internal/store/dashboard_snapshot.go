package store

import (
	"context"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"time"
)

const researchQueueWhere = `status='new' AND domain_status IN ('verified','manual') AND (next_research_at IS NULL OR next_research_at<=now())`

type DashboardCounts struct {
	ProfileActive                                                                        bool
	Companies, Opportunities, Strong, Possible, Unrelated, Unassessed, OpportunityReview int
	CompositionReview, ApprovalPending, Queued, Running, Failed                          int
	Sent, Replies, Bounces, Reconciliation, ResearchQueue, Unresearched, LegacyDrafts    int
	HourUsed, DayUsed, Reserved, ProviderFailures                                        int
	// Suppressed counts postings under a live suppression; Opportunities and the match counts
	// exclude them.
	Suppressed int
	// Contact discovery. StrongMissingDestination is the number that names the bottleneck
	// directly: strong matches with nowhere acceptable to send an application.
	StrongMissingDestination, ContactSearchPending, ContactSearchToday            int
	ContactSearchNoResult, ContactSearchFailed, ContactSearchIdentityBlocked      int
	RecruitingFound, EngineeringFound, ApplicationRoutesFound, PeopleWithoutEmail int
	LastResearch                                                                  *time.Time
}

// Bounded round trips, no per-card/per-row reads or composition snapshot revalidation.
// Match counts use only assessments of current opportunity revisions.
func (db *DB) DashboardCounts(ctx context.Context) (*DashboardCounts, error) {
	c := &DashboardCounts{}
	// Opportunity counts leave out postings under a live suppression (expired, filled, taken
	// down), exactly as the Opportunities list does by default, so each card's number is the
	// number of rows the list it links to shows. Suppressed postings are counted on their own.
	err := db.QueryRow(ctx, `WITH live AS (
 SELECT o.* FROM opportunity o WHERE NOT `+liveSuppression+`
 ), matches AS (
 SELECT o.id,m.classification FROM live o JOIN opportunity_match m ON m.opportunity_id=o.id AND m.opportunity_revision=o.current_revision
 JOIN candidate_profile p ON p.active_revision_id=m.profile_revision_id WHERE m.matcher_version=$1
 ), compositions AS (
 SELECT c.*,d.state AS draft_state FROM draft_composition c JOIN draft d ON d.id=c.draft_id
 ) SELECT
 (SELECT count(*) FROM company),
 (SELECT count(*) FROM live),
 (SELECT count(*) FROM opportunity o WHERE `+liveSuppression+`),
 (SELECT count(*) FROM matches WHERE classification='strong_match'),
 (SELECT count(*) FROM matches WHERE classification='possible_match'),
 (SELECT count(*) FROM matches WHERE classification='unrelated'),
 (SELECT count(*) FROM live o WHERE NOT EXISTS(SELECT 1 FROM matches m WHERE m.id=o.id)),
 (SELECT count(*) FROM live WHERE selection_status='review_required'),
 (SELECT count(*) FROM compositions WHERE status='review_required' AND draft_state NOT IN ('sent','discarded')),
 (SELECT count(*) FROM compositions c WHERE status='reviewed' AND draft_state NOT IN ('sent','discarded') AND NOT EXISTS(SELECT 1 FROM send_approval a WHERE a.draft_id=c.draft_id AND a.composition_revision=c.current_revision AND a.revoked_at IS NULL)),
 (SELECT count(*) FROM compositions WHERE status='queued'),
 (SELECT count(*) FROM compositions WHERE status='running' AND lease_until>=now()),
 (SELECT count(*) FROM compositions WHERE status='failed' OR (status='running' AND lease_until<now())),
 (SELECT count(*) FROM send_attempt WHERE state='accepted'),
 (SELECT count(DISTINCT thread_id) FROM thread_event WHERE kind='replied'),
 (SELECT count(DISTINCT thread_id) FROM thread_event WHERE kind IN ('bounced_permanent','bounced_transient')),
 (SELECT count(*) FROM send_attempt WHERE state IN ('reserved','sending','uncertain')),
 (SELECT count(*) FROM company WHERE `+researchQueueWhere+`),
 (SELECT count(*) FROM company WHERE last_researched_at IS NULL),
 (SELECT count(*) FROM draft d WHERE state NOT IN ('sent','discarded') AND NOT EXISTS(SELECT 1 FROM draft_composition c WHERE c.draft_id=d.id)),
 (SELECT count(*) FROM composition_call WHERE started_at>now()-interval '1 hour'),
 (SELECT count(*) FROM composition_call WHERE started_at>now()-interval '24 hours'),
 (SELECT COALESCE(sum(remaining),0) FROM composition_reservation WHERE expires_at>now()),
 (SELECT count(*) FROM draft_composition_revision WHERE created_at>now()-interval '24 hours' AND (COALESCE(attempt->'compose_metadata'->>'error_category','')<>'' OR COALESCE(attempt->'review_metadata'->>'error_category','')<>'')),
 (SELECT count(DISTINCT o.company_id) FROM live o JOIN matches m ON m.id=o.id
   WHERE m.classification='strong_match' AND o.availability_status<>'closed' AND o.selection_status<>'ignored'
     AND NOT `+acceptableDestination+`),
 (SELECT count(*) FROM contact_discovery_task WHERE state='pending'),
 (SELECT count(*) FROM contact_discovery_task WHERE last_searched_at::date=now()::date),
 (SELECT count(*) FROM contact_discovery_task WHERE state='no_result'),
 (SELECT count(*) FROM contact_discovery_task WHERE state='failed'),
 (SELECT count(*) FROM contact_discovery_task WHERE state='identity_blocked'),
 (SELECT count(*) FROM contact_candidate WHERE recipient_function='recruiting' AND status IN ('new','review_required','promoted')),
 (SELECT count(*) FROM contact_candidate WHERE recipient_function IN ('engineering','leadership') AND status IN ('new','review_required','promoted')),
 (SELECT count(*) FROM contact_candidate WHERE kind IN ('application_url','application_email') AND status<>'rejected'),
 (SELECT count(*) FROM contact_candidate WHERE kind='person' AND email='' AND status NOT IN ('rejected','superseded')),
 (SELECT max(last_researched_at) FROM company),
 (SELECT EXISTS(SELECT 1 FROM candidate_profile WHERE active_revision_id IS NOT NULL))
 `, opportunity.MatcherVersion).Scan(&c.Companies, &c.Opportunities, &c.Suppressed, &c.Strong, &c.Possible, &c.Unrelated, &c.Unassessed, &c.OpportunityReview,
		&c.CompositionReview, &c.ApprovalPending, &c.Queued, &c.Running, &c.Failed, &c.Sent, &c.Replies, &c.Bounces, &c.Reconciliation,
		&c.ResearchQueue, &c.Unresearched, &c.LegacyDrafts, &c.HourUsed, &c.DayUsed, &c.Reserved, &c.ProviderFailures,
		&c.StrongMissingDestination, &c.ContactSearchPending, &c.ContactSearchToday, &c.ContactSearchNoResult,
		&c.ContactSearchFailed, &c.ContactSearchIdentityBlocked, &c.RecruitingFound, &c.EngineeringFound,
		&c.ApplicationRoutesFound, &c.PeopleWithoutEmail, &c.LastResearch, &c.ProfileActive)
	return c, err
}

type DashboardOpportunity struct {
	ID                               int64
	Company, Title, Match, Attention string
}

func (db *DB) DashboardOpportunities(ctx context.Context) ([]DashboardOpportunity, error) {
	rows, err := db.Query(ctx, `SELECT o.id,c.name,o.normalized_title,COALESCE(m.classification,'unassessed'),o.selection_status
 FROM opportunity o JOIN company c ON c.id=o.company_id
 LEFT JOIN opportunity_match m ON m.opportunity_id=o.id AND m.opportunity_revision=o.current_revision
 AND m.profile_revision_id=(SELECT active_revision_id FROM candidate_profile LIMIT 1) AND m.matcher_version=$1
 WHERE o.availability_status<>'closed' AND o.selection_status<>'ignored' AND (o.selection_status='review_required' OR m.classification IN ('strong_match','possible_match'))
 AND NOT `+liveSuppression+`
 ORDER BY CASE WHEN o.selection_status='review_required' THEN 0 WHEN m.classification='strong_match' THEN 1 ELSE 2 END,o.updated_at DESC,o.id DESC LIMIT 5`, opportunity.MatcherVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []DashboardOpportunity{}
	for rows.Next() {
		var o DashboardOpportunity
		if err := rows.Scan(&o.ID, &o.Company, &o.Title, &o.Match, &o.Attention); err != nil {
			return nil, err
		}
		result = append(result, o)
	}
	return result, rows.Err()
}

// Only append-only v2 records are surfaced as events; current mutable state is never
// represented as a fictional event. Details intentionally exclude provider output/credentials.
type DashboardEvent struct {
	Action, Detail, Href string
	Timestamp            time.Time
	Success              bool
}

func (db *DB) DashboardEvents(ctx context.Context) ([]DashboardEvent, error) {
	rows, err := db.Query(ctx, `SELECT action,detail,at,success,href FROM (
 (SELECT 'Opportunity discovered' AS action,'Opportunity #'||id AS detail,discovered_at AS at,true AS success,'/opportunities/'||id AS href FROM opportunity ORDER BY discovered_at DESC LIMIT 6)
 UNION ALL
 (SELECT CASE WHEN COALESCE(attempt->'compose_metadata'->>'error_category','')<>'' OR COALESCE(attempt->'review_metadata'->>'error_category','')<>'' THEN 'Provider request failed' ELSE 'Composition attempt recorded' END,'Draft #'||draft_id,created_at,COALESCE(attempt->>'failure','')='','/compositions/'||draft_id FROM draft_composition_revision ORDER BY created_at DESC LIMIT 6)
 UNION ALL
 (SELECT 'Draft approved','Draft #'||draft_id,approved_at,true,'/compositions/'||draft_id FROM send_approval ORDER BY approved_at DESC LIMIT 6)
 UNION ALL
 (SELECT 'Message accepted','Draft #'||draft_id,settled_at,true,'/compositions/'||draft_id FROM send_attempt WHERE state='accepted' ORDER BY settled_at DESC LIMIT 6)
 UNION ALL
 (SELECT CASE kind WHEN 'replied' THEN 'Reply recorded' WHEN 'bounced_permanent' THEN 'Permanent bounce' WHEN 'bounced_transient' THEN 'Transient bounce' ELSE 'Conversation updated' END,'Conversation #'||thread_id,occurred_at,kind NOT IN ('bounced_permanent','bounced_transient'),'/threads/'||thread_id FROM thread_event ORDER BY occurred_at DESC LIMIT 6)
 ) events ORDER BY at DESC LIMIT 8`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []DashboardEvent{}
	for rows.Next() {
		var e DashboardEvent
		if err := rows.Scan(&e.Action, &e.Detail, &e.Timestamp, &e.Success, &e.Href); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
