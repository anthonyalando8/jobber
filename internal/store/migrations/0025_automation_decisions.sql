-- Phase 7 selective automation. Strictly additive: no existing table, row, index or trigger is
-- modified. The Phase 5 send-state invariants are untouched — automation is a decision layer
-- above the send path, not a change to it.

-- One recorded automation decision about one exact composition revision. Every decision is
-- persisted, eligible or not, because the question that has to be answerable afterwards is not
-- only "why did this send" but equally "why did this one stay in review".
CREATE TABLE automation_decision (
 id bigserial PRIMARY KEY,
 draft_id bigint NOT NULL REFERENCES draft_composition(draft_id) ON DELETE RESTRICT,
 composition_revision integer NOT NULL,
 -- The exact bytes the decision was about. A later revision is a different question and gets
 -- its own row rather than reusing this answer.
 content_hash text NOT NULL CHECK (content_hash <> ''),
 outcome text NOT NULL CHECK (outcome IN ('eligible_for_auto','review_required')),
 policy_version text NOT NULL CHECK (policy_version <> ''),
 intent text NOT NULL CHECK (intent IN ('specific_job_application','speculative_job_enquiry','service_outreach')),
 -- Pinned inputs, so a decision can be re-read against what it actually saw.
 profile_revision_id bigint REFERENCES candidate_profile_revision(id) ON DELETE RESTRICT,
 opportunity_id bigint REFERENCES opportunity(id) ON DELETE RESTRICT,
 opportunity_revision integer,
 plan_id bigint,
 plan_revision integer,
 recipient_contact_id bigint REFERENCES contact(id) ON DELETE RESTRICT,
 recipient_email text NOT NULL DEFAULT '',
 -- The semantic review this decision relied on: whether it completed, whether it passed, and
 -- the content hash it was performed against.
 semantic_status text NOT NULL DEFAULT '',
 semantic_hash text NOT NULL DEFAULT '',
 -- The rollout configuration in force at evaluation time.
 rollout_mode text NOT NULL DEFAULT '',
 allowlist jsonb NOT NULL DEFAULT '[]'::jsonb,
 daily_cap integer NOT NULL DEFAULT 0,
 -- Why. Both lists are always stored: the reasons that supported eligibility and every blocker
 -- that stood against it.
 reasons jsonb NOT NULL DEFAULT '[]'::jsonb,
 blockers jsonb NOT NULL DEFAULT '[]'::jsonb,
 -- Set only when this decision actually led to a transmission, linking the two audit trails.
 send_attempt_id bigint REFERENCES send_attempt(id) ON DELETE RESTRICT,
 evaluated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (draft_id, composition_revision) REFERENCES draft_composition_revision(draft_id, revision) ON DELETE RESTRICT,
 CHECK (octet_length(reasons::text) <= 65536),
 CHECK (octet_length(blockers::text) <= 65536),
 CHECK (octet_length(allowlist::text) <= 4096)
);

CREATE INDEX automation_decision_draft ON automation_decision(draft_id, evaluated_at DESC);
CREATE INDEX automation_decision_recent ON automation_decision(evaluated_at DESC);
CREATE INDEX automation_decision_outcome ON automation_decision(outcome, evaluated_at DESC);

-- A decision is a record of what the policy concluded at a moment, under a stated policy
-- version. It is never edited: re-evaluating writes a new row, which is what makes the history
-- of "this became eligible only after the opportunity was re-crawled" readable at all. The one
-- permitted mutation is attaching the send attempt that resulted from it.
CREATE FUNCTION protect_automation_decision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'automation decisions are the audit trail and are never deleted'; END IF;
 IF (NEW.id, NEW.draft_id, NEW.composition_revision, NEW.content_hash, NEW.outcome,
     NEW.policy_version, NEW.intent, NEW.profile_revision_id, NEW.opportunity_id,
     NEW.opportunity_revision, NEW.plan_id, NEW.plan_revision, NEW.recipient_contact_id,
     NEW.recipient_email, NEW.semantic_status, NEW.semantic_hash, NEW.rollout_mode,
     NEW.allowlist, NEW.daily_cap, NEW.reasons, NEW.blockers, NEW.evaluated_at)
    IS DISTINCT FROM
    (OLD.id, OLD.draft_id, OLD.composition_revision, OLD.content_hash, OLD.outcome,
     OLD.policy_version, OLD.intent, OLD.profile_revision_id, OLD.opportunity_id,
     OLD.opportunity_revision, OLD.plan_id, OLD.plan_revision, OLD.recipient_contact_id,
     OLD.recipient_email, OLD.semantic_status, OLD.semantic_hash, OLD.rollout_mode,
     OLD.allowlist, OLD.daily_cap, OLD.reasons, OLD.blockers, OLD.evaluated_at) THEN
  RAISE EXCEPTION 'an automation decision records what the policy concluded; evaluate again instead';
 END IF;
 IF OLD.send_attempt_id IS NOT NULL AND NEW.send_attempt_id IS DISTINCT FROM OLD.send_attempt_id THEN
  RAISE EXCEPTION 'an automation decision is already linked to send attempt %', OLD.send_attempt_id;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_automation_decision BEFORE UPDATE OR DELETE ON automation_decision
 FOR EACH ROW EXECUTE FUNCTION protect_automation_decision();

-- A durable claim over one automation candidate. Two workers must never evaluate and send the
-- same draft concurrently; the primary key is the draft, so the claim is the draft.
CREATE TABLE automation_claim (
 draft_id bigint PRIMARY KEY REFERENCES draft_composition(draft_id) ON DELETE RESTRICT,
 owner text NOT NULL CHECK (owner <> ''),
 claimed_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL
);
CREATE INDEX automation_claim_expiry ON automation_claim(expires_at);
