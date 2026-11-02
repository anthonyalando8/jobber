-- Opportunity triage and autonomous-sending controls. Additive: existing rows keep their
-- meaning, existing values stay valid, nothing is rewritten or deleted.

-- Triage may take an opportunity out of the working list as "not relevant". The decision is an
-- ordinary suppression record, so it is visible, attributable and reversible like any other.
ALTER TABLE opportunity_suppression DROP CONSTRAINT opportunity_suppression_reason_check;
ALTER TABLE opportunity_suppression ADD CONSTRAINT opportunity_suppression_reason_check CHECK (reason IN (
  'expired','filled','no_longer_hiring','withdrawn_by_source','not_pursuing','duplicate','not_relevant'));
ALTER TABLE opportunity_suppression DROP CONSTRAINT opportunity_suppression_basis_check;
ALTER TABLE opportunity_suppression ADD CONSTRAINT opportunity_suppression_basis_check CHECK (basis IN (
  'operator','source_closed','expiry_date_passed','absent_from_source',
  -- The deterministic triage rules decided, from the stored assessment and description.
  'triage_rules'));

-- One triage conclusion per opportunity, pinned to the exact inputs it read. When any input
-- changes (a new revision of the posting, a new matcher, a new profile revision or new triage
-- rules) the row no longer matches and the opportunity is triaged again; otherwise it is never
-- re-processed. The assessment itself is never rewritten: triage only reads it.
CREATE TABLE opportunity_triage (
 opportunity_id bigint PRIMARY KEY REFERENCES opportunity(id) ON DELETE RESTRICT,
 opportunity_revision integer NOT NULL,
 matcher_version text NOT NULL CHECK (matcher_version <> ''),
 profile_revision_id bigint NOT NULL REFERENCES candidate_profile_revision(id) ON DELETE RESTRICT,
 triage_version text NOT NULL CHECK (triage_version <> ''),
 -- suppressed: high-confidence unrelated, taken out of the list (reversibly).
 -- confirmed_unrelated: medium confidence, kept visible as unrelated.
 -- manual_review: contradictory evidence, waiting for a person.
 -- kept: a person looked at it and chose to keep it visible.
 -- failed: the triage step itself failed; retried, then left for a person.
 outcome text NOT NULL CHECK (outcome IN ('suppressed','confirmed_unrelated','manual_review','kept','failed')),
 confidence text NOT NULL CHECK (confidence IN ('high','medium','low','none')),
 reasons jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (octet_length(reasons::text) <= 16384),
 attempts integer NOT NULL DEFAULT 1 CHECK (attempts >= 0),
 last_error text NOT NULL DEFAULT '',
 decided_by text NOT NULL CHECK (decided_by <> ''),
 triaged_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX opportunity_triage_outcome ON opportunity_triage(outcome, triaged_at DESC);

-- Durable operator controls that change behaviour without a restart or a code change: pausing
-- automatic sending, turning automatic drafting on, pausing triage. One row per key.
CREATE TABLE operator_setting (
 key text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_.]*$'),
 value text NOT NULL,
 updated_by text NOT NULL CHECK (updated_by <> ''),
 updated_at timestamptz NOT NULL DEFAULT now()
);

-- The sending policy now has three answers: eligible for automatic sending, needs a person, or
-- must not be sent at all.
ALTER TABLE automation_decision DROP CONSTRAINT automation_decision_outcome_check;
ALTER TABLE automation_decision ADD CONSTRAINT automation_decision_outcome_check
 CHECK (outcome IN ('eligible_for_auto','review_required','blocked'));
