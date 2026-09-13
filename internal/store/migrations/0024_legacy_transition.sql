-- Phase 6 legacy transition. Strictly additive. No legacy draft, template, signal, send_log
-- row or company segment is rewritten, reclassified or deleted by this migration.
--
-- The only thing Phase 6 needs to record that did not exist before is a person's explicit
-- statement about what an old sent thread was actually for. Everything else the transition
-- needs is already derivable: a draft belongs to v2 if and only if it has a draft_composition
-- row, which is a foreign key rather than a label that could disagree with reality.

-- One classification event. Intent is never inferred from company segment, the old application
-- flag, template wording or payment keywords: a row here exists only because a person said so,
-- and 'unknown' is a legitimate, preferred answer when they cannot tell.
CREATE TABLE legacy_thread_intent (
 id bigserial PRIMARY KEY,
 send_log_id bigint NOT NULL REFERENCES send_log(id) ON DELETE RESTRICT,
 intent text NOT NULL CHECK (intent IN ('specific_job_application','speculative_job_enquiry','service_outreach','unknown')),
 -- Pinned only where the person could actually establish them. All three stay NULL for an
 -- 'unknown' classification, and none of them is reconstructed from current state.
 opportunity_id bigint REFERENCES opportunity(id) ON DELETE RESTRICT,
 recipient_contact_id bigint REFERENCES contact(id) ON DELETE RESTRICT,
 target_families jsonb NOT NULL DEFAULT '[]'::jsonb,
 -- What the person actually used to decide. Required, so a classification can be audited
 -- rather than taken on trust.
 basis text NOT NULL CHECK (basis <> ''),
 classified_by text NOT NULL CHECK (classified_by <> ''),
 classified_at timestamptz NOT NULL DEFAULT now(),
 superseded_at timestamptz,
 CHECK (intent <> 'unknown' OR (opportunity_id IS NULL AND recipient_contact_id IS NULL)),
 CHECK (octet_length(target_families::text) <= 4096)
);

-- At most one current classification per thread; correcting one supersedes it rather than
-- overwriting what was previously believed.
CREATE UNIQUE INDEX legacy_thread_intent_current ON legacy_thread_intent(send_log_id) WHERE superseded_at IS NULL;
CREATE INDEX legacy_thread_intent_lookup ON legacy_thread_intent(send_log_id, classified_at DESC);

-- A classification is a statement someone made at a point in time. It can be superseded, never
-- edited or erased.
CREATE FUNCTION protect_legacy_thread_intent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'a legacy classification is superseded, not deleted'; END IF;
 IF (NEW.id, NEW.send_log_id, NEW.intent, NEW.opportunity_id, NEW.recipient_contact_id,
     NEW.target_families, NEW.basis, NEW.classified_by, NEW.classified_at)
    IS DISTINCT FROM
    (OLD.id, OLD.send_log_id, OLD.intent, OLD.opportunity_id, OLD.recipient_contact_id,
     OLD.target_families, OLD.basis, OLD.classified_by, OLD.classified_at) THEN
  RAISE EXCEPTION 'a legacy classification records what someone concluded; supersede it instead';
 END IF;
 IF OLD.superseded_at IS NOT NULL AND NEW.superseded_at IS DISTINCT FROM OLD.superseded_at THEN
  RAISE EXCEPTION 'a superseded classification cannot be reinstated';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_legacy_thread_intent BEFORE UPDATE OR DELETE ON legacy_thread_intent
 FOR EACH ROW EXECUTE FUNCTION protect_legacy_thread_intent();
