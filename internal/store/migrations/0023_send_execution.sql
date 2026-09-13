-- Phase 5 guarded send execution. Additive: legacy send_log, drafts, follow-up history,
-- suppression, contacts and company history are untouched and keep their original meaning.

-- Explicit human authorization, bound to one exact composition revision, its content hash and
-- the pinned recipient. Semantic review passing is not approval and cannot create this row.
CREATE TABLE send_approval (
 id bigserial PRIMARY KEY,
 draft_id bigint NOT NULL REFERENCES draft_composition(draft_id) ON DELETE RESTRICT,
 composition_revision integer NOT NULL,
 content_hash text NOT NULL CHECK (content_hash <> ''),
 input_hash text NOT NULL CHECK (input_hash <> ''),
 recipient_contact_id bigint NOT NULL REFERENCES contact(id) ON DELETE RESTRICT,
 recipient_email text NOT NULL CHECK (recipient_email <> ''),
 intent text NOT NULL CHECK (intent IN ('specific_job_application','speculative_job_enquiry','service_outreach')),
 approved_by text NOT NULL CHECK (approved_by <> ''),
 approved_at timestamptz NOT NULL DEFAULT now(),
 revoked_at timestamptz,
 revoked_reason text NOT NULL DEFAULT '',
 CHECK ((revoked_at IS NULL) = (revoked_reason = '')),
 FOREIGN KEY (draft_id, composition_revision) REFERENCES draft_composition_revision(draft_id, revision) ON DELETE RESTRICT
);
-- At most one live approval per draft. Revoking frees the slot for a re-approval.
CREATE UNIQUE INDEX send_approval_live ON send_approval(draft_id) WHERE revoked_at IS NULL;

-- An approval is a record of a decision already taken. Only revocation may change it.
CREATE FUNCTION protect_send_approval() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'send approvals are revoked, not deleted'; END IF;
 IF (NEW.id, NEW.draft_id, NEW.composition_revision, NEW.content_hash, NEW.input_hash,
     NEW.recipient_contact_id, NEW.recipient_email, NEW.intent, NEW.approved_by, NEW.approved_at)
    IS DISTINCT FROM
    (OLD.id, OLD.draft_id, OLD.composition_revision, OLD.content_hash, OLD.input_hash,
     OLD.recipient_contact_id, OLD.recipient_email, OLD.intent, OLD.approved_by, OLD.approved_at) THEN
  RAISE EXCEPTION 'an approval records a decision already taken; revoke it and approve again';
 END IF;
 IF OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at THEN
  RAISE EXCEPTION 'a revoked approval cannot be reinstated';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_send_approval BEFORE UPDATE OR DELETE ON send_approval
 FOR EACH ROW EXECUTE FUNCTION protect_send_approval();

-- A durable outbound conversation. Thread identity is explicit, never inferred from company id:
-- a company may have many opportunities historically while only one conversation is appropriate.
CREATE TABLE outbound_thread (
 id bigserial PRIMARY KEY,
 company_id bigint NOT NULL REFERENCES company(id) ON DELETE RESTRICT,
 plan_id bigint NOT NULL,
 plan_revision integer NOT NULL,
 intent text NOT NULL CHECK (intent IN ('specific_job_application','speculative_job_enquiry','service_outreach')),
 recipient_contact_id bigint NOT NULL REFERENCES contact(id) ON DELETE RESTRICT,
 recipient_email text NOT NULL CHECK (recipient_email <> ''),
 opportunity_id bigint REFERENCES opportunity(id) ON DELETE RESTRICT,
 root_draft_id bigint NOT NULL REFERENCES draft_composition(draft_id) ON DELETE RESTRICT,
 root_message_id text NOT NULL DEFAULT '',
 state text NOT NULL DEFAULT 'active'
  CHECK (state IN ('active','replied','closed','bounced','do_not_contact')),
 state_reason text NOT NULL DEFAULT '',
 followup_step integer NOT NULL DEFAULT 0 CHECK (followup_step >= 0),
 last_sent_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (plan_id, plan_revision) REFERENCES contact_plan_revision(plan_id, revision) ON DELETE RESTRICT
);
-- One thread per root draft, and at most one live conversation per recipient.
CREATE UNIQUE INDEX outbound_thread_root ON outbound_thread(root_draft_id);
CREATE UNIQUE INDEX outbound_thread_active_recipient ON outbound_thread(recipient_contact_id) WHERE state = 'active';
CREATE INDEX outbound_thread_company ON outbound_thread(company_id, updated_at DESC);

-- The thread's approved intent is frozen. A follow-up inherits it; changing purpose requires a
-- new contact plan and a new thread.
CREATE FUNCTION protect_outbound_thread() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'outbound threads are closed, not deleted'; END IF;
 IF (NEW.id, NEW.company_id, NEW.plan_id, NEW.plan_revision, NEW.intent, NEW.recipient_contact_id,
     NEW.recipient_email, NEW.opportunity_id, NEW.root_draft_id, NEW.created_at)
    IS DISTINCT FROM
    (OLD.id, OLD.company_id, OLD.plan_id, OLD.plan_revision, OLD.intent, OLD.recipient_contact_id,
     OLD.recipient_email, OLD.opportunity_id, OLD.root_draft_id, OLD.created_at) THEN
  RAISE EXCEPTION 'thread identity, recipient and intent are frozen; create a new plan and thread';
 END IF;
 IF NEW.followup_step < OLD.followup_step THEN
  RAISE EXCEPTION 'follow-up progress cannot move backwards';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_outbound_thread BEFORE UPDATE OR DELETE ON outbound_thread
 FOR EACH ROW EXECUTE FUNCTION protect_outbound_thread();

-- One durable, immutable row per transmission attempt. Written before the provider is contacted
-- so a crash between reservation and result leaves evidence rather than silence.
CREATE TABLE send_attempt (
 id bigserial PRIMARY KEY,
 idempotency_key text NOT NULL CHECK (idempotency_key <> ''),
 draft_id bigint NOT NULL REFERENCES draft_composition(draft_id) ON DELETE RESTRICT,
 composition_revision integer NOT NULL,
 approval_id bigint NOT NULL REFERENCES send_approval(id) ON DELETE RESTRICT,
 thread_id bigint REFERENCES outbound_thread(id) ON DELETE RESTRICT,
 company_id bigint NOT NULL REFERENCES company(id) ON DELETE RESTRICT,
 recipient_contact_id bigint NOT NULL REFERENCES contact(id) ON DELETE RESTRICT,
 recipient_email text NOT NULL CHECK (recipient_email <> ''),
 intent text NOT NULL CHECK (intent IN ('specific_job_application','speculative_job_enquiry','service_outreach')),
 kind text NOT NULL CHECK (kind IN ('first_contact','followup')),
 followup_step integer NOT NULL DEFAULT 0 CHECK (followup_step >= 0),
 -- The exact bytes that were or would have been transmitted.
 subject text NOT NULL,
 body text NOT NULL,
 content_hash text NOT NULL CHECK (content_hash <> ''),
 message_id text NOT NULL DEFAULT '',
 -- Pinned upstream revisions, so an attempt is auditable without reading anything mutable.
 profile_revision_id bigint NOT NULL REFERENCES candidate_profile_revision(id) ON DELETE RESTRICT,
 opportunity_id bigint REFERENCES opportunity(id) ON DELETE RESTRICT,
 opportunity_revision integer,
 plan_id bigint NOT NULL,
 plan_revision integer NOT NULL,
 policy_version text NOT NULL,
 initiator text NOT NULL CHECK (initiator <> ''),
 state text NOT NULL
  CHECK (state IN ('reserved','sending','accepted','rejected','transient_failed','uncertain','cancelled')),
 provider text NOT NULL DEFAULT '',
 provider_message_id text NOT NULL DEFAULT '',
 provider_response text NOT NULL DEFAULT '',
 provider_stage text NOT NULL DEFAULT '',
 error_category text NOT NULL DEFAULT '',
 reserved_at timestamptz NOT NULL DEFAULT now(),
 started_at timestamptz,
 settled_at timestamptz,
 FOREIGN KEY (draft_id, composition_revision) REFERENCES draft_composition_revision(draft_id, revision) ON DELETE RESTRICT,
 FOREIGN KEY (plan_id, plan_revision) REFERENCES contact_plan_revision(plan_id, revision) ON DELETE RESTRICT,
 CHECK ((opportunity_id IS NULL) = (opportunity_revision IS NULL)),
 CHECK ((kind = 'followup') = (followup_step > 0))
);

-- The duplicate guard. A key is held by any attempt that might have reached the provider, so a
-- second transmission cannot be reserved. transient_failed and cancelled release the key,
-- because those attempts definitely did not leave, which is what makes a bounded retry safe.
CREATE UNIQUE INDEX send_attempt_live_key ON send_attempt(idempotency_key)
 WHERE state IN ('reserved','sending','accepted','uncertain','rejected');
CREATE INDEX send_attempt_key_history ON send_attempt(idempotency_key, reserved_at DESC);
CREATE INDEX send_attempt_thread ON send_attempt(thread_id, reserved_at DESC);
CREATE INDEX send_attempt_recent ON send_attempt(settled_at DESC) WHERE state = 'accepted';

-- An attempt is a historical record. Only its own settlement fields may be filled in, once.
CREATE FUNCTION protect_send_attempt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'send attempts are the audit trail and are never deleted'; END IF;
 IF (NEW.id, NEW.idempotency_key, NEW.draft_id, NEW.composition_revision, NEW.approval_id,
     NEW.company_id, NEW.recipient_contact_id, NEW.recipient_email, NEW.intent, NEW.kind,
     NEW.followup_step, NEW.subject, NEW.body, NEW.content_hash, NEW.profile_revision_id,
     NEW.plan_id, NEW.plan_revision, NEW.initiator, NEW.reserved_at)
    IS DISTINCT FROM
    (OLD.id, OLD.idempotency_key, OLD.draft_id, OLD.composition_revision, OLD.approval_id,
     OLD.company_id, OLD.recipient_contact_id, OLD.recipient_email, OLD.intent, OLD.kind,
     OLD.followup_step, OLD.subject, OLD.body, OLD.content_hash, OLD.profile_revision_id,
     OLD.plan_id, OLD.plan_revision, OLD.initiator, OLD.reserved_at) THEN
  RAISE EXCEPTION 'what was sent, to whom and under which approval is immutable';
 END IF;
 -- A settled attempt never changes again, so an accepted send cannot be rewritten as failed.
 IF OLD.state NOT IN ('reserved','sending') AND NEW.state IS DISTINCT FROM OLD.state THEN
  RAISE EXCEPTION 'attempt % is already settled as %', OLD.id, OLD.state;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_send_attempt BEFORE UPDATE OR DELETE ON send_attempt
 FOR EACH ROW EXECUTE FUNCTION protect_send_attempt();

-- Bounce and reply state arrive from an operator or, later, a provider integration. There is no
-- inbox ingestion in this phase, so nothing infers a bounce from send acceptance alone.
CREATE TABLE thread_event (
 id bigserial PRIMARY KEY,
 thread_id bigint NOT NULL REFERENCES outbound_thread(id) ON DELETE RESTRICT,
 send_attempt_id bigint REFERENCES send_attempt(id) ON DELETE RESTRICT,
 kind text NOT NULL CHECK (kind IN ('replied','bounced_permanent','bounced_transient','closed','do_not_contact','reconciled','note')),
 detail text NOT NULL DEFAULT '',
 source text NOT NULL CHECK (source IN ('operator','provider')),
 occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX thread_event_thread ON thread_event(thread_id, occurred_at DESC);
CREATE FUNCTION protect_thread_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'thread events are an append-only record'; END $$;
CREATE TRIGGER protect_thread_event BEFORE UPDATE OR DELETE ON thread_event
 FOR EACH ROW EXECUTE FUNCTION protect_thread_event();
