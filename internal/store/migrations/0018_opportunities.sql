CREATE TABLE opportunity (
 id bigserial PRIMARY KEY, company_id bigint NOT NULL REFERENCES company(id) ON DELETE RESTRICT,
 source text NOT NULL, source_key text NOT NULL, external_id text NOT NULL DEFAULT '',
 url text NOT NULL DEFAULT '', canonical_url text NOT NULL DEFAULT '',
 title text NOT NULL DEFAULT '', normalized_title text NOT NULL DEFAULT '', description text NOT NULL DEFAULT '',
 location text NOT NULL DEFAULT '', employment_type text NOT NULL DEFAULT '',
 remote_status text NOT NULL CHECK(remote_status IN ('remote','hybrid','onsite','unknown')),
 published_at timestamptz, expires_at timestamptz,
 availability_status text NOT NULL CHECK(availability_status IN ('open','closed','unknown')),
 description_status text NOT NULL CHECK(description_status IN ('complete','partial','unavailable')),
 selection_status text NOT NULL DEFAULT 'new' CHECK(selection_status IN ('new','selected','review_required','ignored')),
 current_revision integer NOT NULL DEFAULT 1, content_hash text NOT NULL, metadata jsonb NOT NULL DEFAULT '{}',
 discovered_at timestamptz NOT NULL DEFAULT now(), last_seen_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source,source_key), CHECK(octet_length(description)<=65536), CHECK(octet_length(metadata::text)<=131072)
);
CREATE UNIQUE INDEX opportunity_external_id_unique ON opportunity(source,external_id) WHERE external_id<>'';
CREATE INDEX opportunity_company_idx ON opportunity(company_id,updated_at DESC);
CREATE INDEX opportunity_url_idx ON opportunity(source,canonical_url) WHERE canonical_url<>'';
CREATE TABLE opportunity_revision (
 opportunity_id bigint NOT NULL REFERENCES opportunity(id) ON DELETE RESTRICT, revision integer NOT NULL,
 document jsonb NOT NULL, content_hash text NOT NULL, captured_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(opportunity_id,revision)
);
ALTER TABLE opportunity ADD CONSTRAINT opportunity_current_revision_fk FOREIGN KEY(id,current_revision) REFERENCES opportunity_revision(opportunity_id,revision) DEFERRABLE INITIALLY DEFERRED;
CREATE FUNCTION protect_opportunity_revision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'opportunity revisions are immutable'; END $$;
CREATE TRIGGER immutable_opportunity_revision BEFORE UPDATE OR DELETE ON opportunity_revision FOR EACH ROW EXECUTE FUNCTION protect_opportunity_revision();
CREATE TABLE ingestion_review (
 id bigserial PRIMARY KEY, diagnostic_key text NOT NULL UNIQUE, source text NOT NULL,
 reason text NOT NULL, discovery jsonb NOT NULL, first_seen_at timestamptz NOT NULL DEFAULT now(),
 last_seen_at timestamptz NOT NULL DEFAULT now(), observations integer NOT NULL DEFAULT 1,
 resolved_at timestamptz
);
CREATE TABLE legacy_opportunity_backfill (
 signal_id bigint PRIMARY KEY REFERENCES signal(id) ON DELETE RESTRICT,
 opportunity_id bigint, opportunity_revision integer,
 status text NOT NULL CHECK(status IN ('converted','review_required')), reason text NOT NULL,
 processed_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(opportunity_id,opportunity_revision) REFERENCES opportunity_revision(opportunity_id,revision) ON DELETE RESTRICT
);
