-- Deterministic candidate/opportunity fit assessments. An assessment is pinned to an exact
-- opportunity revision, an exact approved profile revision and an exact matcher version, so a
-- newer revision of either can never be read as a current assessment.
CREATE TABLE opportunity_match (
 id bigserial PRIMARY KEY,
 opportunity_id bigint NOT NULL,
 opportunity_revision integer NOT NULL,
 profile_revision_id bigint NOT NULL REFERENCES candidate_profile_revision(id) ON DELETE RESTRICT,
 matcher_version text NOT NULL,
 classification text NOT NULL CHECK (classification IN ('strong_match','possible_match','unrelated')),
 score integer NOT NULL CHECK (score BETWEEN 0 AND 100),
 family_confidence text NOT NULL CHECK (family_confidence IN ('clear','ambiguous','none')),
 result jsonb NOT NULL,
 assessed_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (opportunity_id, opportunity_revision, profile_revision_id, matcher_version),
 FOREIGN KEY (opportunity_id, opportunity_revision) REFERENCES opportunity_revision(opportunity_id, revision) ON DELETE RESTRICT,
 CHECK (octet_length(result::text) <= 262144),
 -- A title alone can never reach strong_match: classification and family confidence must agree.
 CHECK (NOT (classification = 'strong_match' AND family_confidence <> 'clear')),
 CHECK (NOT (classification = 'unrelated' AND family_confidence = 'clear'))
);
CREATE INDEX opportunity_match_current_idx ON opportunity_match(opportunity_id, assessed_at DESC);
CREATE INDEX opportunity_match_classification_idx ON opportunity_match(classification, assessed_at DESC);

-- Company relevance answers "is there a truthful engineering reason to contact this company",
-- never "is this job suitable". Categories are capped and evidence is counted per fact group.
CREATE TABLE company_relevance (
 company_id bigint PRIMARY KEY REFERENCES company(id) ON DELETE RESTRICT,
 profile_revision_id bigint REFERENCES candidate_profile_revision(id) ON DELETE RESTRICT,
 assessor_version text NOT NULL,
 score integer NOT NULL CHECK (score BETWEEN 0 AND 100),
 relevant boolean NOT NULL,
 payment_need_evidenced boolean NOT NULL DEFAULT false,
 result jsonb NOT NULL,
 assessed_at timestamptz NOT NULL DEFAULT now(),
 CHECK (octet_length(result::text) <= 262144)
);
