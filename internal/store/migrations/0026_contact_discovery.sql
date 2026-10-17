-- Second-stage contact discovery. Strictly additive: no existing table, row, index, trigger or
-- constraint is modified. Nothing here relaxes the Phase 3 recipient rules or the Phase 5/7 send
-- interlocks — this layer only supplies contact facts with their provenance, and the existing
-- recipient policy remains the only thing that decides whether a fact is a usable recipient.

-- One durable queue entry per company. The primary key is the company, because "search for a
-- recipient at this employer" is a question about the employer, not about one vacancy: two
-- strong matches at the same company are one search, not two.
CREATE TABLE contact_discovery_task (
 company_id bigint PRIMARY KEY REFERENCES company(id) ON DELETE RESTRICT,
 state text NOT NULL DEFAULT 'pending' CHECK (state IN (
   -- Waiting to be picked up, or picked up and in flight.
   'pending','claimed','searching',
   -- Terminal-for-now outcomes. completed found at least one destination-grade candidate;
   -- partial found people or weak evidence but no acceptable destination; no_result found
   -- nothing at all; review_required found something only a human should judge.
   'completed','partial','no_result','review_required',
   -- Refusals. identity_blocked is the company-identity gate: we will not go looking for the
   -- employees of a company we cannot confidently identify. failed is a search/fetch failure.
   'identity_blocked','failed')),
 -- Why this company was queued at all, and at what priority. Priority is ascending: 1 is the
 -- highest-value work (a strong match with no acceptable destination).
 priority integer NOT NULL DEFAULT 100 CHECK (priority >= 1),
 eligibility_reason text NOT NULL DEFAULT '',
 -- The purpose the search is serving, when a plan or a selected opportunity establishes one.
 -- Empty means no authoritative intent exists, and the searcher must not invent one.
 intent text NOT NULL DEFAULT '' CHECK (intent IN ('','specific_job_application','speculative_job_enquiry','service_outreach')),
 opportunity_id bigint REFERENCES opportunity(id) ON DELETE RESTRICT,
 plan_id bigint REFERENCES contact_plan(id) ON DELETE RESTRICT,
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 last_searched_at timestamptz,
 -- The cooldown. A company searched thoroughly with no useful result is not asked again on the
 -- next worker tick; this column is what makes that true across restarts.
 next_eligible_at timestamptz NOT NULL DEFAULT now(),
 result_summary jsonb NOT NULL DEFAULT '{}'::jsonb,
 failure_reason text NOT NULL DEFAULT '',
 -- A durable claim, so two workers (or a worker and a manual "Find contact") cannot search the
 -- same company at once.
 claimed_by text NOT NULL DEFAULT '',
 claim_expires_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK (octet_length(result_summary::text) <= 65536),
 CHECK ((state IN ('claimed','searching')) = (claimed_by <> ''))
);
CREATE INDEX contact_discovery_task_queue ON contact_discovery_task(state, priority, next_eligible_at);
CREATE INDEX contact_discovery_task_claim ON contact_discovery_task(claim_expires_at) WHERE claimed_by <> '';

-- One discovered candidate: a person, a published application route, or a mailbox. This is
-- deliberately not the contact table. A candidate can be a person with no email at all, an email
-- whose owner is unknown, or an address that was inferred rather than published — none of which
-- the contact table can represent, and none of which should sit in the table the send path reads.
CREATE TABLE contact_candidate (
 id bigserial PRIMARY KEY,
 company_id bigint NOT NULL REFERENCES company(id) ON DELETE RESTRICT,
 -- The deduplication key. A normalized email is the strongest identity; a source-published
 -- profile URL is next; a normalized person name plus company is the weakest and is only used
 -- when nothing better exists. Two evidence rows that resolve to this key are one candidate.
 identity_key text NOT NULL CHECK (identity_key <> ''),
 identity_basis text NOT NULL CHECK (identity_basis IN ('email','profile_url','person_name','route_url')),
 kind text NOT NULL CHECK (kind IN (
   'application_email',  -- an address the employer published as where to apply
   'application_url',    -- an apply form or ATS route; a destination that needs no email
   'person',             -- a named human with a role, who may or may not have a known address
   'mailbox')),          -- an address with no identified person
 person_name text NOT NULL DEFAULT '',
 role_title text NOT NULL DEFAULT '',
 -- The employer named by the source, kept verbatim so a mismatch against the company we think
 -- we are researching stays visible rather than being silently normalized away.
 employer_claimed text NOT NULL DEFAULT '',
 email text NOT NULL DEFAULT '',
 -- Where the address came from, kept separate from who the person is. Finding a name never
 -- establishes an address, so these two facts are never merged into one column.
 email_provenance text NOT NULL DEFAULT 'none' CHECK (email_provenance IN (
   'none','source_published','employer_published','third_party_published','inferred_pattern')),
 -- The verification state, which is about evidence rather than about SMTP. An address is never
 -- called verified because a domain resolves.
 verification text NOT NULL DEFAULT 'unverified' CHECK (verification IN (
   'published','source_confirmed','inferred','unverified','bad','generic','not_human_mailbox')),
 application_url text NOT NULL DEFAULT '',
 ats_provider text NOT NULL DEFAULT '',
 -- The existing classifiers' verdicts, stored so the operator sees the same answer the send
 -- path would reach. These are written from internal/relevance; this table defines no second
 -- opinion about what a valid recruiter or engineering contact is.
 recipient_function text NOT NULL DEFAULT '',
 function_evidence text NOT NULL DEFAULT '',
 endpoint_purpose text NOT NULL DEFAULT '',
 endpoint_contactable boolean NOT NULL DEFAULT false,
 confidence text NOT NULL DEFAULT 'low' CHECK (confidence IN ('high','medium','low')),
 -- The trust of the best evidence behind this candidate, so "a search snippet said so" can
 -- never be mistaken for "the employer published it".
 best_source_trust integer NOT NULL DEFAULT 0 CHECK (best_source_trust BETWEEN 0 AND 100),
 employer_owned_evidence boolean NOT NULL DEFAULT false,
 status text NOT NULL DEFAULT 'new' CHECK (status IN ('new','promoted','review_required','rejected','superseded')),
 rejected_reason text NOT NULL DEFAULT '',
 promoted_contact_id bigint REFERENCES contact(id) ON DELETE RESTRICT,
 discovered_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (company_id, identity_key),
 -- An inferred address is never a published one, and is never promoted to a contact row. Both
 -- halves are enforced here rather than trusted to the caller, because the whole point of
 -- separating inference from publication is that no later code path can confuse them.
 CHECK (NOT (email_provenance = 'inferred_pattern' AND verification IN ('published','source_confirmed'))),
 CHECK (NOT (email_provenance = 'inferred_pattern' AND status = 'promoted')),
 -- A candidate promoted into the contact table must actually carry an address and name the row
 -- it became.
 CHECK ((status = 'promoted') = (promoted_contact_id IS NOT NULL)),
 CHECK (status <> 'promoted' OR email <> '')
);
CREATE INDEX contact_candidate_company ON contact_candidate(company_id, status, best_source_trust DESC);
CREATE INDEX contact_candidate_function ON contact_candidate(recipient_function, status);

-- Every observation that supports a candidate, kept individually. The same recruiter found on a
-- team page, a job posting and a search snippet is one candidate with three evidence rows — not
-- three people, and not one fact that has quietly lost the weaker sources it also rests on.
CREATE TABLE contact_candidate_evidence (
 id bigserial PRIMARY KEY,
 candidate_id bigint NOT NULL REFERENCES contact_candidate(id) ON DELETE RESTRICT,
 -- Trust order, strongest first. The numeric trust is stored alongside the name so a later
 -- change to the ranking cannot silently rewrite what a stored observation was worth when it
 -- was made.
 source_type text NOT NULL CHECK (source_type IN (
   'job_posting','employer_careers_page','employer_team_page','employer_page',
   'public_profile','third_party','search_snippet','source_metadata')),
 source_trust integer NOT NULL CHECK (source_trust BETWEEN 0 AND 100),
 source_url text NOT NULL DEFAULT '',
 -- The exact words that support the claim. A candidate with no excerpt is a claim with no
 -- source, which this pipeline does not create.
 excerpt text NOT NULL CHECK (excerpt <> ''),
 excerpt_hash text NOT NULL CHECK (excerpt_hash <> ''),
 discovery_method text NOT NULL CHECK (discovery_method <> ''),
 provider text NOT NULL DEFAULT '',
 employer_owned boolean NOT NULL DEFAULT false,
 observed_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (candidate_id, source_url, excerpt_hash),
 CHECK (length(excerpt) <= 2000)
);
CREATE INDEX contact_candidate_evidence_candidate ON contact_candidate_evidence(candidate_id, source_trust DESC);

-- Evidence is an observation that was made. Correcting it means observing again, so the row is
-- append-only; the same source seen twice refreshes observed_at through the unique key instead
-- of inserting a second row that would read as independent confirmation.
CREATE FUNCTION protect_contact_candidate_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'discovered evidence is the provenance record and is never deleted'; END IF;
 IF (NEW.id, NEW.candidate_id, NEW.source_type, NEW.source_trust, NEW.source_url,
     NEW.excerpt, NEW.excerpt_hash, NEW.discovery_method, NEW.provider, NEW.employer_owned)
    IS DISTINCT FROM
    (OLD.id, OLD.candidate_id, OLD.source_type, OLD.source_trust, OLD.source_url,
     OLD.excerpt, OLD.excerpt_hash, OLD.discovery_method, OLD.provider, OLD.employer_owned) THEN
  RAISE EXCEPTION 'discovered evidence records what a source said; record a new observation instead';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_contact_candidate_evidence BEFORE UPDATE OR DELETE ON contact_candidate_evidence
 FOR EACH ROW EXECUTE FUNCTION protect_contact_candidate_evidence();

-- A candidate's identity never changes. Reclassification, promotion and rejection are ordinary
-- updates; becoming a different person or a different address is not.
CREATE FUNCTION protect_contact_candidate() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'contact candidates are rejected, not deleted'; END IF;
 IF (NEW.id, NEW.company_id, NEW.identity_key, NEW.identity_basis, NEW.discovered_at)
    IS DISTINCT FROM (OLD.id, OLD.company_id, OLD.identity_key, OLD.identity_basis, OLD.discovered_at) THEN
  RAISE EXCEPTION 'contact candidate identity is immutable';
 END IF;
 -- An address that was published cannot later be recorded as inferred, and an inferred one
 -- cannot be promoted to published without new evidence creating a new candidate.
 IF OLD.email <> '' AND NEW.email <> OLD.email THEN
  RAISE EXCEPTION 'a candidate address is evidence; record a new candidate rather than rewriting this one';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_contact_candidate BEFORE UPDATE OR DELETE ON contact_candidate
 FOR EACH ROW EXECUTE FUNCTION protect_contact_candidate();
