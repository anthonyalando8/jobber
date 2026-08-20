-- Research freshness scheduling for the autonomous worker (review.md §11). Additive only.
ALTER TABLE company ADD COLUMN last_researched_at timestamptz;
ALTER TABLE company ADD COLUMN next_research_at timestamptz;

-- The research loop polls "which companies are due", frequently — index the gating columns.
CREATE INDEX ON company (status, next_research_at);
