-- Suppressing an opportunity that has expired or is no longer hiring.
--
-- Strictly additive: no existing table, row, index, trigger or constraint is modified. Nothing
-- here changes the send interlocks or the recipient rules. It fills a gap in the opposite
-- direction: opportunity.selection_status and opportunity.availability_status are already read by
-- the browse priority filter, the dashboard queue and contact-discovery eligibility, but no code
-- path ever wrote a suppression, and opportunity.expires_at was stored at ingest and never read.
-- A posting that closed stayed in every queue forever.

-- One suppression decision about one opportunity, kept rather than deleted.
--
-- Reinstating does not remove the row, it supersedes it. "This was marked expired in March and
-- put back in April" is a different and more useful fact than "this is not suppressed", and the
-- automatic sweep needs the history: a posting an operator deliberately put back must not be
-- suppressed again by the same automatic reasoning on the next tick.
CREATE TABLE opportunity_suppression (
 id bigserial PRIMARY KEY,
 opportunity_id bigint NOT NULL REFERENCES opportunity(id) ON DELETE RESTRICT,
 -- The revision the decision was about. A later revision is new information from the source,
 -- which is why a reinstated posting can legitimately be suppressed again later.
 opportunity_revision integer NOT NULL,
 -- What is true about the posting. This is the operator's or the sweep's conclusion.
 reason text NOT NULL CHECK (reason IN (
   'expired','filled','no_longer_hiring','withdrawn_by_source','not_pursuing','duplicate')),
 -- How it is known, kept separate from the conclusion because an operator saying a role is gone
 -- and a date having passed are different grades of evidence and should not be conflated when
 -- someone later asks why this was dropped.
 basis text NOT NULL CHECK (basis IN (
   -- A person decided.
   'operator',
   -- The source itself said closed.
   'source_closed',
   -- The expiry date the source published has passed.
   'expiry_date_passed',
   -- The posting stopped appearing in a feed that is still being read successfully.
   'absent_from_source')),
 note text NOT NULL DEFAULT '',
 decided_by text NOT NULL CHECK (decided_by <> ''),
 -- The facts the decision actually saw, so an automatic suppression can be re-read against its
 -- own evidence instead of being taken on trust.
 evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
 -- The selection status held before suppression, so reinstating restores exactly what was there
 -- rather than guessing at 'new'.
 restore_selection_status text NOT NULL DEFAULT 'new',
 decided_at timestamptz NOT NULL DEFAULT now(),
 -- Set when the suppression is lifted. Only an operator lifts one; the sweep never does.
 superseded_at timestamptz,
 superseded_by text NOT NULL DEFAULT '',
 supersede_note text NOT NULL DEFAULT '',
 CHECK (octet_length(evidence::text) <= 16384),
 CHECK ((superseded_at IS NULL) = (superseded_by = ''))
);

-- At most one live suppression per opportunity. The partial index is what makes "is this
-- suppressed right now" a single unambiguous row instead of a max() over history.
CREATE UNIQUE INDEX opportunity_suppression_current
 ON opportunity_suppression(opportunity_id) WHERE superseded_at IS NULL;

-- The sweep asks "has this basis already been reinstated for this opportunity", and the browse
-- filter joins on the live row.
CREATE INDEX opportunity_suppression_history ON opportunity_suppression(opportunity_id, basis);

-- The sweep scans for expiry and absence across the whole table on a slow tick.
CREATE INDEX opportunity_suppression_candidates
 ON opportunity(availability_status, selection_status, expires_at, last_seen_at);
