-- How an approval was given. Additive: every existing approval keeps its meaning and becomes
-- basis 'reviewed' (approved after the AI review passed). 'manual_override' is a person approving
-- an email the AI review did not pass, after reading its findings; the findings stay on record.
ALTER TABLE send_approval ADD COLUMN basis text NOT NULL DEFAULT 'reviewed'
 CHECK (basis IN ('reviewed','manual_override'));
ALTER TABLE send_approval ADD COLUMN override_note text NOT NULL DEFAULT '';
ALTER TABLE send_approval ADD CONSTRAINT send_approval_override_note
 CHECK (basis = 'manual_override' OR override_note = '');

-- The basis and note are part of the decision, so they are as immutable as the rest of it.
CREATE OR REPLACE FUNCTION protect_send_approval() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'send approvals are revoked, not deleted'; END IF;
 IF (NEW.id, NEW.draft_id, NEW.composition_revision, NEW.content_hash, NEW.input_hash,
     NEW.recipient_contact_id, NEW.recipient_email, NEW.intent, NEW.approved_by, NEW.approved_at,
     NEW.basis, NEW.override_note)
    IS DISTINCT FROM
    (OLD.id, OLD.draft_id, OLD.composition_revision, OLD.content_hash, OLD.input_hash,
     OLD.recipient_contact_id, OLD.recipient_email, OLD.intent, OLD.approved_by, OLD.approved_at,
     OLD.basis, OLD.override_note) THEN
  RAISE EXCEPTION 'an approval records a decision already taken; revoke it and approve again';
 END IF;
 IF OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at THEN
  RAISE EXCEPTION 'a revoked approval cannot be reinstated';
 END IF;
 RETURN NEW;
END $$;
