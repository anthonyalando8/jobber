-- Explainable scoring (review.md §15) — "82/100, +15 hiring backend, +10 Django evidence...".
-- Store the line items that produced a score, not just the final number.
ALTER TABLE company ADD COLUMN score_breakdown jsonb;
