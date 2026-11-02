-- Checks on discovered contact addresses, and operator acceptance of an address that needed
-- review. Additive: no existing candidate, contact or evidence row is rewritten or deleted.

-- One row per check run on a candidate's address. Append-only: a re-check adds a row rather than
-- overwriting what an earlier check concluded, so the history of "why was this accepted" stays.
--
-- A check never establishes that a mailbox exists. The deterministic half reads the address and
-- the domain's mail servers; the AI half, when it runs, gives an opinion on plausibility. Neither
-- sends anything to the address.
CREATE TABLE contact_address_check (
 id bigserial PRIMARY KEY,
 candidate_id bigint NOT NULL REFERENCES contact_candidate(id) ON DELETE RESTRICT,
 email text NOT NULL CHECK (email <> ''),
 check_version text NOT NULL CHECK (check_version <> ''),
 -- will_not_receive: a hard fault (bad syntax, no mail servers for the domain, no-reply).
 -- doubtful: it can receive mail but something does not fit (another domain, free mail, an
 --           unfamiliar format). looks_deliverable: nothing found against it.
 verdict text NOT NULL CHECK (verdict IN ('looks_deliverable','doubtful','will_not_receive')),
 mail_servers text NOT NULL CHECK (mail_servers IN ('mx','address_only','none','lookup_failed','not_checked')),
 checks jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (octet_length(checks::text) <= 16384),
 -- The AI opinion, when one was asked for. 'not_run' when it was not.
 ai_verdict text NOT NULL DEFAULT 'not_run' CHECK (ai_verdict IN ('not_run','plausible','doubtful','implausible','failed')),
 ai_reasons jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (octet_length(ai_reasons::text) <= 16384),
 ai_model text NOT NULL DEFAULT '',
 checked_by text NOT NULL CHECK (checked_by <> ''),
 checked_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX contact_address_check_candidate ON contact_address_check(candidate_id, checked_at DESC);

-- Who accepted a candidate that needed review. Empty for one promoted automatically because the
-- employer published it.
ALTER TABLE contact_candidate ADD COLUMN accepted_by text NOT NULL DEFAULT '';
ALTER TABLE contact_candidate ADD COLUMN accepted_at timestamptz;

-- An inferred address still never becomes a contact on its own. It may now become one when a
-- person accepted it, and only then.
ALTER TABLE contact_candidate DROP CONSTRAINT contact_candidate_check1;
ALTER TABLE contact_candidate ADD CONSTRAINT contact_candidate_inferred_needs_person CHECK (
 NOT (email_provenance = 'inferred_pattern' AND status = 'promoted' AND accepted_by = ''));
