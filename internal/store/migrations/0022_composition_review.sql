-- Additive Phase 4 extension. No legacy draft, sent record or template is rewritten.
CREATE TABLE draft_composition (
 draft_id bigint PRIMARY KEY REFERENCES draft(id) ON DELETE RESTRICT,
 plan_id bigint NOT NULL, plan_revision integer NOT NULL,
 profile_revision_id bigint NOT NULL REFERENCES candidate_profile_revision(id) ON DELETE RESTRICT,
 snapshot jsonb NOT NULL, input_hash text NOT NULL,
 status text NOT NULL CHECK(status IN ('queued','running','review_required','reviewed','failed')),
 current_revision integer NOT NULL DEFAULT 0,
 lease_token text NOT NULL DEFAULT '', lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(plan_id,plan_revision) REFERENCES contact_plan_revision(plan_id,revision) ON DELETE RESTRICT,
 CHECK(octet_length(snapshot::text)<=524288)
);
CREATE UNIQUE INDEX composition_one_running_plan ON draft_composition(plan_id) WHERE status IN ('queued','running');
CREATE TABLE draft_composition_revision (
 draft_id bigint NOT NULL REFERENCES draft_composition(draft_id) ON DELETE RESTRICT,
 revision integer NOT NULL, content_hash text NOT NULL, attempt jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(draft_id,revision),
 CHECK(octet_length(attempt::text)<=262144)
);
CREATE TABLE composition_reservation (
 draft_id bigint PRIMARY KEY REFERENCES draft_composition(draft_id) ON DELETE RESTRICT,
 owner text NOT NULL, remaining integer NOT NULL CHECK(remaining>=0), expires_at timestamptz NOT NULL
);
CREATE TABLE composition_call (
 id bigserial PRIMARY KEY, draft_id bigint NOT NULL REFERENCES draft_composition(draft_id) ON DELETE RESTRICT,
 stage text NOT NULL CHECK(stage IN ('compose','review')), started_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX composition_call_time ON composition_call(started_at);
CREATE FUNCTION protect_composition_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'composition snapshots cannot be deleted'; END IF;
 IF (NEW.draft_id,NEW.plan_id,NEW.plan_revision,NEW.profile_revision_id,NEW.snapshot,NEW.input_hash,NEW.created_at)
 IS DISTINCT FROM (OLD.draft_id,OLD.plan_id,OLD.plan_revision,OLD.profile_revision_id,OLD.snapshot,OLD.input_hash,OLD.created_at)
 THEN RAISE EXCEPTION 'composition inputs are immutable; regenerate a new draft'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER immutable_composition_snapshot BEFORE UPDATE OR DELETE ON draft_composition FOR EACH ROW EXECUTE FUNCTION protect_composition_snapshot();
CREATE FUNCTION protect_composition_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'composition revisions cannot be rewritten'; END $$;
CREATE TRIGGER immutable_composition_revision BEFORE UPDATE OR DELETE ON draft_composition_revision FOR EACH ROW EXECUTE FUNCTION protect_composition_revision();
CREATE FUNCTION protect_composed_draft() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM draft_composition WHERE draft_id=OLD.id) THEN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'composed drafts retain history'; END IF;
  IF (NEW.subject,NEW.body,NEW.contact_id,NEW.company_id) IS DISTINCT FROM (OLD.subject,OLD.body,OLD.contact_id,OLD.company_id)
   AND COALESCE(current_setting('prospector.composition_write',true),'')<>'on'
   THEN RAISE EXCEPTION 'use versioned composition editing and full review'; END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_composed_draft BEFORE UPDATE OR DELETE ON draft FOR EACH ROW EXECUTE FUNCTION protect_composed_draft();
