-- An operator's decision that an employer name, as one source spells it, is a company already in
-- the workspace.
--
-- Strictly additive. Ingestion holds a listing back as "ambiguous_company_identity" when its
-- employer name and its domain point at different companies, which is how the same employer
-- spelled two ways ("Acme" and "Acme Ltd") presents. The resolver never guesses which is right.
-- An alias records the operator's answer once, so later runs of the same source attach the
-- listing to that company instead of holding it back again. Companies are never merged.
CREATE TABLE company_alias (
 normalized_name text PRIMARY KEY CHECK (normalized_name <> '' AND octet_length(normalized_name) <= 512),
 company_id bigint NOT NULL REFERENCES company(id) ON DELETE CASCADE,
 decided_by text NOT NULL CHECK (decided_by <> ''),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX company_alias_company ON company_alias(company_id);
