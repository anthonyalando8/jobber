-- Domain verification state machine (review.md §10). A seeded domain is never trusted for
-- enrichment until it's verified — closes the "Acme Ltd -> acme-ltd.com" guessing gap.
ALTER TABLE company ADD COLUMN domain_status text NOT NULL DEFAULT 'unknown';
    -- unknown|candidate|verified|failed|manual
ALTER TABLE company ADD COLUMN domain_confidence real;

CREATE INDEX ON company (domain_status);

-- Companies seeded before this migration existed either came from a human-typed CSV/form row
-- (source = 'manual' — trust it directly, same as a fresh manual import would be) or a
-- guessed job-board domain. For non-manual sources, anything already enriched or further
-- along clearly had a working domain, so grandfather those in as manual too; everything else
-- (still status='new', never-verified) starts at unknown and goes through the verifier.
UPDATE company SET domain_status = 'manual'
  WHERE domain_status = 'unknown' AND (source = 'manual' OR status NOT IN ('new'));
