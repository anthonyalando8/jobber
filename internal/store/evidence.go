package store

import (
	"context"

	"github.com/anthonyalando8/prospector/internal/evidence"
)

// EvidenceInput is one observation of a company fact, carrying where it came from rather than
// only what a keyword matcher concluded.
type EvidenceInput struct {
	Kind             string
	Detail           string
	Excerpt          string
	SourceURL        string
	ExtractionMethod string
	Confidence       string
}

// UpsertEvidence records a canonical research fact. Observing the same fact again refreshes its
// observation state; it never inserts a second row and never adds independent confidence. The
// preserved legacy duplicates keep an empty fact_key and are deliberately outside this path.
func (db *DB) UpsertEvidence(ctx context.Context, companyID int64, in EvidenceInput) (int64, bool, error) {
	if in.ExtractionMethod == "" || in.ExtractionMethod == evidence.LegacyExtraction {
		in.ExtractionMethod = "site_text_v1"
	}
	if in.Confidence == "" {
		in.Confidence = "unknown"
	}
	if len(in.Excerpt) > evidence.MaxExcerptBytes {
		in.Excerpt = in.Excerpt[:evidence.MaxExcerptBytes]
	}
	key := evidence.FactKey(in.Kind, in.Detail)
	group := evidence.GroupKey(in.Kind, in.Detail)
	var url *string
	if in.SourceURL != "" {
		url = &in.SourceURL
	}
	var id int64
	var inserted bool
	err := db.QueryRow(ctx,
		`INSERT INTO signal (company_id, kind, detail, url, fact_key, fact_group, excerpt,
		                     extraction_method, confidence, first_seen_at, last_seen_at, observations)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, now(), now(), 1)
		 ON CONFLICT (company_id, fact_key) WHERE fact_key <> '' DO UPDATE
		   SET last_seen_at = now(),
		       observations = signal.observations + 1,
		       url = COALESCE(EXCLUDED.url, signal.url)
		 RETURNING id, (xmax = 0)`,
		companyID, in.Kind, in.Detail, url, key, group, in.Excerpt,
		in.ExtractionMethod, in.Confidence).Scan(&id, &inserted)
	return id, inserted, err
}

// ListEvidence returns every stored fact for a company, legacy detections included and clearly
// labelled as such.
func (db *DB) ListEvidence(ctx context.Context, companyID int64) ([]evidence.Fact, error) {
	rows, err := db.Query(ctx,
		`SELECT id, company_id, kind, detail, COALESCE(url,''), fact_key, fact_group, excerpt,
		        extraction_method, confidence, observations,
		        COALESCE(first_seen_at, created_at), COALESCE(last_seen_at, created_at), created_at
		 FROM signal WHERE company_id = $1 ORDER BY id`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	facts := []evidence.Fact{}
	for rows.Next() {
		var f evidence.Fact
		if err = rows.Scan(&f.SignalID, &f.CompanyID, &f.Kind, &f.Detail, &f.SourceURL,
			&f.FactKey, &f.FactGroup, &f.Excerpt, &f.ExtractionMethod, &f.Confidence,
			&f.Observations, &f.FirstSeen, &f.LastSeen, &f.CreatedAt); err != nil {
			return nil, err
		}
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

// EvidenceGroups collapses a company's stored facts into distinct fact groups, which is the
// only form company relevance and proof selection are allowed to count.
func (db *DB) EvidenceGroups(ctx context.Context, companyID int64) ([]evidence.Group, error) {
	facts, err := db.ListEvidence(ctx, companyID)
	if err != nil {
		return nil, err
	}
	return evidence.GroupFacts(facts), nil
}
