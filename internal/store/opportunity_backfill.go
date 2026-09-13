package store

import (
	"context"
	"net/url"
	"strings"

	"github.com/anthonyalando8/prospector/internal/opportunity"
)

type BackfillResult struct {
	Processed      int
	Converted      int
	ReviewRequired int
}

// Recover only the explicit legacy "title at employer" prefix. Never use the
// legacy role matcher, title suffix guesses, or candidate identity as source facts.
func legacyTitle(detail, company string) string {
	marker := " at " + company
	index := strings.Index(strings.ToLower(detail), strings.ToLower(marker))
	if index <= 0 || index > 200 {
		return ""
	}
	title := strings.TrimSpace(detail[:index])
	if strings.ContainsAny(title, "\r\n") {
		return ""
	}
	suffix := detail[index+len(marker):]
	if suffix != "" && !strings.HasPrefix(suffix, ".") && !strings.HasPrefix(suffix, " ") {
		return ""
	}
	return title
}

// Each batch commits its mapping alongside the opportunity. The ledger is the
// resume cursor; reruns do not reinterpret previously processed history.
func (db *DB) BackfillLegacyOpportunities(ctx context.Context, limit int) (BackfillResult, error) {
	result := BackfillResult{}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(73201918)`); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT s.id,s.company_id,c.name,c.source,s.detail,COALESCE(s.url,'') FROM signal s JOIN company c ON c.id=s.company_id LEFT JOIN legacy_opportunity_backfill b ON b.signal_id=s.id WHERE s.kind='hiring_backend' AND b.signal_id IS NULL ORDER BY s.id LIMIT $1`, limit)
	if err != nil {
		return result, err
	}
	type legacy struct {
		id, companyID                int64
		company, source, detail, url string
	}
	batch := []legacy{}
	for rows.Next() {
		var item legacy
		if err = rows.Scan(&item.id, &item.companyID, &item.company, &item.source, &item.detail, &item.url); err != nil {
			break
		}
		batch = append(batch, item)
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	if rows.Err() != nil {
		return result, rows.Err()
	}
	for _, item := range batch {
		source := item.source
		u, parseErr := url.Parse(item.url)
		if parseErr == nil {
			host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
			if host == "brightermonday.co.ke" || host == "myjobmag.co.ke" {
				source = host
			}
		}
		d := opportunity.DiscoveredOpportunity{Source: source, CompanyID: item.companyID, CompanyName: item.company, URL: item.url, Title: legacyTitle(item.detail, item.company), Description: item.detail, DescriptionStatus: "partial", Metadata: map[string]string{"extraction": "legacy_signal_prefix"}}
		if d.Source == "" {
			d.Source = "legacy_unknown"
		}
		d, err = opportunity.Normalize(d)
		if err != nil {
			return result, err
		}
		var ingested IngestionResult
		if d.Title == "" || d.CanonicalURL == "" || parseErr != nil || u.Path == "" || u.Path == "/" || u.Path == "/jobs" || u.Path == "/jobs/" {
			ingested, err = ingestionReview(ctx, tx, d, "legacy_evidence_insufficient")
		} else {
			ingested, err = ingestOpportunity(ctx, tx, d)
		}
		if err != nil {
			return result, err
		}
		status, reason := "converted", "explicit_legacy_title_and_url"
		var id, revision any
		if ingested.ID == 0 {
			status = "review_required"
			reason = ingested.ReviewReason
			result.ReviewRequired++
		} else {
			id = ingested.ID
			revision = ingested.Revision
			result.Converted++
		}
		if _, err = tx.Exec(ctx, `INSERT INTO legacy_opportunity_backfill(signal_id,opportunity_id,opportunity_revision,status,reason) VALUES($1,$2,$3,$4,$5)`, item.id, id, revision, status, reason); err != nil {
			return result, err
		}
		result.Processed++
	}
	return result, tx.Commit(ctx)
}
