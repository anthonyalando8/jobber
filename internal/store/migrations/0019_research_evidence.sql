-- Research evidence provenance and grouping. Additive only: every legacy signal row keeps its
-- original id/company_id/kind/detail/url/created_at and is labelled a legacy detection rather
-- than rewritten into a quotation it never was.
ALTER TABLE signal ADD COLUMN fact_key text NOT NULL DEFAULT '';
ALTER TABLE signal ADD COLUMN fact_group text NOT NULL DEFAULT '';
ALTER TABLE signal ADD COLUMN excerpt text NOT NULL DEFAULT '';
ALTER TABLE signal ADD COLUMN extraction_method text NOT NULL DEFAULT 'legacy_detection';
ALTER TABLE signal ADD COLUMN confidence text NOT NULL DEFAULT 'unknown' CHECK (confidence IN ('low','medium','high','unknown'));
ALTER TABLE signal ADD COLUMN first_seen_at timestamptz;
ALTER TABLE signal ADD COLUMN last_seen_at timestamptz;
ALTER TABLE signal ADD COLUMN observations integer NOT NULL DEFAULT 1 CHECK (observations >= 1);
ALTER TABLE signal ADD COLUMN revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1);
ALTER TABLE signal ADD CONSTRAINT signal_excerpt_bounded CHECK (octet_length(excerpt) <= 4096);

-- Only new canonical evidence is deduplicated. Preserved legacy duplicates keep fact_key='' and
-- are deliberately outside this index so historical citations survive untouched.
CREATE UNIQUE INDEX signal_canonical_fact ON signal(company_id, fact_key) WHERE fact_key <> '';
CREATE INDEX signal_fact_group_idx ON signal(company_id, fact_group) WHERE fact_group <> '';

CREATE FUNCTION protect_signal_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id <> OLD.id OR NEW.company_id <> OLD.company_id OR NEW.created_at <> OLD.created_at THEN
  RAISE EXCEPTION 'signal identity is immutable';
 END IF;
 IF OLD.extraction_method = 'legacy_detection'
    AND (NEW.detail IS DISTINCT FROM OLD.detail OR NEW.kind IS DISTINCT FROM OLD.kind
         OR NEW.excerpt IS DISTINCT FROM OLD.excerpt OR NEW.extraction_method <> OLD.extraction_method) THEN
  RAISE EXCEPTION 'legacy detections are historical evidence and cannot be rewritten as quotations';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_signal_history BEFORE UPDATE ON signal FOR EACH ROW EXECUTE FUNCTION protect_signal_history();

-- Business segment describes the business. It is not, and can never become, an email intent.
-- The legacy company.segment column is preserved unchanged as historical reference.
ALTER TABLE company ADD COLUMN business_segment text NOT NULL DEFAULT 'unknown'
 CHECK (business_segment IN ('agency','enterprise','incubator','sme','unknown'));
