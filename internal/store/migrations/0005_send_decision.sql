-- The AUTO_SEND / REVIEW_REQUIRED / BLOCKED policy engine (review.md "Critical Workflow
-- Change") needs to record, on every draft, what it decided and why — never a bare boolean.
ALTER TABLE draft ADD COLUMN decision text;              -- auto_send|review_required|blocked
ALTER TABLE draft ADD COLUMN decision_reason text;        -- comma-separated reason codes
ALTER TABLE draft ADD COLUMN decision_at timestamptz;
ALTER TABLE draft ADD COLUMN score_at_decision int;

CREATE INDEX ON draft (decision);
