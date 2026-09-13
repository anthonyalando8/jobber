-- A contact plan is the explicit, persisted record of why a particular recipient is being
-- contacted. It authorizes future draft generation only; it never authorizes sending.
CREATE TABLE contact_plan (
 id bigserial PRIMARY KEY,
 company_id bigint NOT NULL REFERENCES company(id) ON DELETE RESTRICT,
 intent text NOT NULL CHECK (intent IN ('specific_job_application','speculative_job_enquiry','service_outreach')),
 -- The applied-for vacancy. Only a specific application pins one, and it pins an exact revision.
 opportunity_id bigint REFERENCES opportunity(id) ON DELETE RESTRICT,
 opportunity_revision integer,
 -- Provenance only: the listing that led us to the company. Never the role being claimed.
 provenance_opportunity_id bigint REFERENCES opportunity(id) ON DELETE RESTRICT,
 match_classification text NOT NULL CHECK (match_classification IN ('strong_match','possible_match','unrelated','profile_required','not_assessed')),
 target_families jsonb NOT NULL DEFAULT '[]',
 recipient_contact_id bigint NOT NULL REFERENCES contact(id) ON DELETE RESTRICT,
 recipient_email text NOT NULL CHECK (recipient_email <> ''),
 recipient_name text NOT NULL DEFAULT '',
 recipient_relevance text NOT NULL CHECK (recipient_relevance IN ('preferred','acceptable','review_required')),
 recipient_reason text NOT NULL,
 profile_revision_id bigint NOT NULL REFERENCES candidate_profile_revision(id) ON DELETE RESTRICT,
 plan_key text NOT NULL,
 revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
 selection_origin text NOT NULL CHECK (selection_origin IN ('manual','suggested')),
 status text NOT NULL DEFAULT 'selected' CHECK (status IN ('suggested','selected','paused','closed')),
 requires_review boolean NOT NULL DEFAULT true,
 reasons jsonb NOT NULL DEFAULT '[]',
 selection jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 -- Exactly the specific-application intent pins a vacancy revision; the other two never do.
 CHECK ((intent = 'specific_job_application') = (opportunity_id IS NOT NULL AND opportunity_revision IS NOT NULL)),
 -- The unrelated-role invariant, enforced by the database rather than by any caller: no score,
 -- manual override, company segment or legacy status can make an unrelated role an application.
 CHECK (NOT (intent = 'specific_job_application' AND match_classification IN ('unrelated','profile_required','not_assessed'))),
 -- A speculative enquiry's provenance listing must not double as the applied-for vacancy.
 CHECK (provenance_opportunity_id IS NULL OR opportunity_id IS NULL OR provenance_opportunity_id <> opportunity_id),
 CHECK (octet_length(selection::text) <= 262144),
 FOREIGN KEY (opportunity_id, opportunity_revision) REFERENCES opportunity_revision(opportunity_id, revision) ON DELETE RESTRICT
);
-- One live plan per company + vacancy-or-none + intent. Alternative plans stay visible.
CREATE UNIQUE INDEX contact_plan_active_key ON contact_plan(plan_key) WHERE status IN ('suggested','selected');
CREATE INDEX contact_plan_company_idx ON contact_plan(company_id, updated_at DESC);

-- Immutable snapshot per plan revision. The frozen recipient lives here, so a later send path
-- cannot silently substitute the company current best contact.
CREATE TABLE contact_plan_revision (
 plan_id bigint NOT NULL REFERENCES contact_plan(id) ON DELETE RESTRICT,
 revision integer NOT NULL,
 snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (plan_id, revision),
 CHECK (octet_length(snapshot::text) <= 262144)
);
CREATE FUNCTION protect_contact_plan_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'contact plan revisions are immutable'; END $$;
CREATE TRIGGER immutable_contact_plan_revision BEFORE UPDATE OR DELETE ON contact_plan_revision
 FOR EACH ROW EXECUTE FUNCTION protect_contact_plan_revision();

-- Changing who is contacted, the intent, or the pinned vacancy is a new plan revision, never a
-- silent in-place edit of an already-reviewed plan.
CREATE FUNCTION protect_contact_plan() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'contact plans are closed, not deleted'; END IF;
 IF NEW.id <> OLD.id OR NEW.company_id <> OLD.company_id OR NEW.created_at <> OLD.created_at THEN
  RAISE EXCEPTION 'contact plan identity is immutable';
 END IF;
 IF (NEW.recipient_contact_id, NEW.recipient_email, NEW.intent, NEW.match_classification,
     COALESCE(NEW.opportunity_id, 0), COALESCE(NEW.opportunity_revision, 0), NEW.profile_revision_id)
    IS DISTINCT FROM
    (OLD.recipient_contact_id, OLD.recipient_email, OLD.intent, OLD.match_classification,
     COALESCE(OLD.opportunity_id, 0), COALESCE(OLD.opportunity_revision, 0), OLD.profile_revision_id)
    AND NEW.revision <= OLD.revision THEN
  RAISE EXCEPTION 'changing plan intent, pinned vacancy or recipient requires a new plan revision';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_contact_plan BEFORE UPDATE OR DELETE ON contact_plan
 FOR EACH ROW EXECUTE FUNCTION protect_contact_plan();
