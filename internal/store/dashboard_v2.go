package store

import "context"

// PipelineStats is the v2 operating picture: what has been discovered, what the matcher
// concluded, what is waiting for a person, and what the legacy pipeline left behind. It is
// deliberately operational — every number here corresponds to something the operator can act
// on or must account for, rather than to an interesting-looking trend.
type PipelineStats struct {
	Companies      int `json:"companies"`
	Opportunities  int `json:"opportunities"`
	StrongMatches  int `json:"strong_matches"`
	PossibleMatch  int `json:"possible_matches"`
	Unrelated      int `json:"unrelated"`
	MatchReview    int `json:"opportunities_needing_review"`
	Unassessed     int `json:"opportunities_unassessed"`
	PlansSelected  int `json:"contact_plans_selected"`
	PlansSuggested int `json:"contact_plans_suggested"`

	CompositionsQueued   int `json:"compositions_queued"`
	CompositionsRunning  int `json:"compositions_running"`
	CompositionsReview   int `json:"compositions_review_required"`
	CompositionsReviewed int `json:"compositions_reviewed"`
	CompositionsFailed   int `json:"compositions_failed"`
	CompositionsStale    int `json:"compositions_stale"`

	// NeedsReconciliation counts attempts that are stuck in flight or ended uncertain. Both hold
	// their idempotency key and block any further send of that message, and neither resolves on
	// its own — so they are surfaced rather than left to be discovered when a send is refused.
	NeedsReconciliation int `json:"attempts_needing_reconciliation"`

	// Automation counters. Eligible/blocked are what the policy concluded most recently for
	// each draft; blocked is never hidden, because the blocked cases are the ones that say
	// whether automation is being held back by the data or by the policy.
	AutoEligible       int `json:"automation_eligible"`
	AutoBlocked        int `json:"automation_blocked"`
	AutoSentToday      int `json:"automatic_sends_today"`
	ManualSentToday    int `json:"manual_sends_today"`
	ManualEditExcluded int `json:"excluded_by_manual_edit"`

	Approvals     int `json:"live_approvals"`
	SendAttempts  int `json:"send_attempts"`
	ThreadsActive int `json:"threads_active"`

	LegacyUnsentDrafts     int `json:"legacy_unsent_drafts"`
	LegacyThreads          int `json:"legacy_threads"`
	LegacyUnclassified     int `json:"legacy_threads_unclassified"`
	ProfileRevisions       int `json:"profile_revisions"`
	ProfileActive          bool
	ProfileActiveRevision  int64
	OpportunitiesThisWeek  int `json:"opportunities_this_week"`
	CompositionAttempts24h int `json:"composition_attempts_24h"`
}

// GetPipelineStats runs the v2 dashboard counts. Each is a separate explicit query so a reader
// can check what any displayed number actually means; this dataset is small enough that the
// cost is irrelevant next to the clarity.
func (db *DB) GetPipelineStats(ctx context.Context) (*PipelineStats, error) {
	s := &PipelineStats{}
	// Staleness is computed per draft from its snapshot, which is too expensive to express as
	// one aggregate; it is filled in by the caller from the compositions it lists.
	counts := []struct {
		target *int
		query  string
	}{
		{&s.Companies, `SELECT count(*) FROM company`},
		{&s.Opportunities, `SELECT count(*) FROM opportunity`},
		{&s.OpportunitiesThisWeek, `SELECT count(*) FROM opportunity WHERE discovered_at > now() - interval '7 days'`},
		{&s.StrongMatches, `SELECT count(*) FROM opportunity o JOIN opportunity_match m
		   ON m.opportunity_id=o.id AND m.opportunity_revision=o.current_revision
		 WHERE m.classification='strong_match'`},
		{&s.PossibleMatch, `SELECT count(*) FROM opportunity o JOIN opportunity_match m
		   ON m.opportunity_id=o.id AND m.opportunity_revision=o.current_revision
		 WHERE m.classification='possible_match'`},
		{&s.Unrelated, `SELECT count(*) FROM opportunity o JOIN opportunity_match m
		   ON m.opportunity_id=o.id AND m.opportunity_revision=o.current_revision
		 WHERE m.classification='unrelated'`},
		{&s.MatchReview, `SELECT count(*) FROM opportunity WHERE selection_status='review_required'`},
		// An opportunity with no assessment for its current revision is not "unrelated"; it is
		// simply unanswered, and showing it as anything else would overstate what is known.
		{&s.Unassessed, `SELECT count(*) FROM opportunity o WHERE NOT EXISTS(
		   SELECT 1 FROM opportunity_match m WHERE m.opportunity_id=o.id AND m.opportunity_revision=o.current_revision)`},
		{&s.PlansSelected, `SELECT count(*) FROM contact_plan WHERE status='selected'`},
		{&s.PlansSuggested, `SELECT count(*) FROM contact_plan WHERE status='suggested'`},
		{&s.CompositionsQueued, `SELECT count(*) FROM draft_composition WHERE status='queued'`},
		{&s.CompositionsRunning, `SELECT count(*) FROM draft_composition WHERE status='running'`},
		{&s.CompositionsReview, `SELECT count(*) FROM draft_composition WHERE status='review_required'`},
		{&s.CompositionsReviewed, `SELECT count(*) FROM draft_composition WHERE status='reviewed'`},
		{&s.CompositionsFailed, `SELECT count(*) FROM draft_composition WHERE status='failed'`},
		{&s.CompositionAttempts24h, `SELECT count(*) FROM composition_call WHERE started_at > now() - interval '24 hours'`},
		{&s.NeedsReconciliation, `SELECT count(*) FROM send_attempt WHERE state IN ('uncertain','sending','reserved')`},
		// The latest decision per draft, so a draft that was re-evaluated is counted once.
		{&s.AutoEligible, `SELECT count(*) FROM (
		   SELECT DISTINCT ON (draft_id) outcome FROM automation_decision ORDER BY draft_id, evaluated_at DESC, id DESC
		 ) latest WHERE outcome='eligible_for_auto'`},
		{&s.AutoBlocked, `SELECT count(*) FROM (
		   SELECT DISTINCT ON (draft_id) outcome FROM automation_decision ORDER BY draft_id, evaluated_at DESC, id DESC
		 ) latest WHERE outcome='review_required'`},
		{&s.AutoSentToday, `SELECT count(DISTINCT a.id) FROM send_attempt a
		   JOIN automation_decision d ON d.send_attempt_id=a.id
		  WHERE a.reserved_at::date = now()::date AND a.state IN ('reserved','sending','accepted','uncertain')`},
		{&s.ManualSentToday, `SELECT count(*) FROM send_attempt a
		  WHERE a.reserved_at::date = now()::date AND a.initiator NOT LIKE 'automation:%'
		    AND a.state IN ('reserved','sending','accepted','uncertain')`},
		{&s.ManualEditExcluded, `SELECT count(*) FROM automation_decision, jsonb_array_elements(blockers) AS b
		  WHERE b->>'code' = 'human_edited_revision'`},
		{&s.Approvals, `SELECT count(*) FROM send_approval WHERE revoked_at IS NULL`},
		{&s.SendAttempts, `SELECT count(*) FROM send_attempt`},
		{&s.ThreadsActive, `SELECT count(*) FROM outbound_thread WHERE state='active'`},
		{&s.LegacyUnsentDrafts, `SELECT count(*) FROM draft d
		 WHERE NOT EXISTS(SELECT 1 FROM draft_composition c WHERE c.draft_id=d.id)
		   AND d.state NOT IN ('sent','discarded')`},
		{&s.LegacyThreads, `SELECT count(*) FROM send_log`},
		{&s.LegacyUnclassified, `SELECT count(*) FROM send_log s WHERE NOT EXISTS(
		   SELECT 1 FROM legacy_thread_intent i WHERE i.send_log_id=s.id AND i.superseded_at IS NULL)`},
		{&s.ProfileRevisions, `SELECT count(*) FROM candidate_profile_revision`},
	}
	for _, c := range counts {
		if err := db.QueryRow(ctx, c.query).Scan(c.target); err != nil {
			return nil, err
		}
	}

	// The active profile is the fact that decides whether any of this can become authoritative
	// at all, so it is reported as a state rather than a count.
	var activeRevision *int64
	if err := db.QueryRow(ctx, `SELECT active_revision_id FROM candidate_profile LIMIT 1`).Scan(&activeRevision); err == nil && activeRevision != nil {
		s.ProfileActive, s.ProfileActiveRevision = true, *activeRevision
	}
	return s, nil
}
