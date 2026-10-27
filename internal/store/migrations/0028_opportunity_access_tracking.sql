-- How a listing can be pursued, and how far the operator has pursued it.
--
-- Strictly additive: two new tables keyed by opportunity, no existing table, row, index or
-- constraint is modified, and nothing here touches the send interlocks or recipient rules.
--
-- opportunity_access holds a classification derived from the stored listing (who may apply, and
-- how one applies). It lives beside the opportunity rather than in opportunity.metadata because
-- metadata is part of the listing's content hash: storing a derived label there would mint a new
-- source revision, and invalidate every fit assessment, each time the classifier improved.
-- classifier_version and opportunity_revision say exactly which rules and which listing text a
-- row reflects, so a stale row is recomputed rather than trusted.
CREATE TABLE opportunity_access (
 opportunity_id bigint PRIMARY KEY REFERENCES opportunity(id) ON DELETE CASCADE,
 opportunity_revision integer NOT NULL,
 classifier_version integer NOT NULL,
 eligibility text NOT NULL CHECK (eligibility IN ('worldwide','region_restricted','country_specific','unclear')),
 kenya_access text NOT NULL CHECK (kenya_access IN ('open','likely','restricted','unknown')),
 eligibility_evidence text NOT NULL DEFAULT '',
 route text NOT NULL CHECK (route IN ('direct_contact','employer_site','employer_ats','job_board','unknown')),
 route_url text NOT NULL DEFAULT '',
 cost text NOT NULL CHECK (cost IN ('free','registration','paid','unknown')),
 cost_evidence text NOT NULL DEFAULT '',
 contract boolean NOT NULL DEFAULT false,
 classified_at timestamptz NOT NULL DEFAULT now(),
 CHECK (octet_length(eligibility_evidence) <= 1024 AND octet_length(cost_evidence) <= 1024 AND octet_length(route_url) <= 4096)
);
CREATE INDEX opportunity_access_filters ON opportunity_access(kenya_access, route, cost);

-- The operator's own progress on a listing: saved, applied, and what came of it. One row per
-- opportunity; the company page keeps the outreach outcome, this keeps the application.
CREATE TABLE opportunity_tracking (
 opportunity_id bigint PRIMARY KEY REFERENCES opportunity(id) ON DELETE CASCADE,
 state text NOT NULL CHECK (state IN ('saved','applied','interviewing','offer','rejected','not_interested')),
 note text NOT NULL DEFAULT '' CHECK (octet_length(note) <= 4000),
 applied_at timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX opportunity_tracking_state ON opportunity_tracking(state, updated_at DESC);
