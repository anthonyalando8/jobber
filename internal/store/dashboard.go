package store

import (
	"context"
	"time"
)

// DashboardStats is the "TODAY" strip review.md §38 asks for — everything the operator should
// see at a glance without digging into individual screens.
type DashboardStats struct {
	Discovered     int // companies created since `since`
	Researched     int // companies researched since `since`
	HighValue      int // companies at/above the review-worthy score threshold, touched since `since`
	Drafted        int // drafts created since `since`
	AwaitingReview int // pending drafts, excluding ones the send policy blocked
	Sent           int // sends since `since`
	FollowupsDue   int
	Replies        int // replies received since `since`
}

// GetDashboardStats runs the small set of aggregate counts the dashboard needs. since is
// normally "start of today" in the operator's local time — a handful of cheap COUNT queries,
// not worth a single mega-query given how small this dataset is.
func (db *DB) GetDashboardStats(ctx context.Context, since time.Time, highValueMinScore int) (*DashboardStats, error) {
	s := &DashboardStats{}

	if err := db.QueryRow(ctx, `SELECT count(*) FROM company WHERE created_at >= $1`, since).Scan(&s.Discovered); err != nil {
		return nil, err
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM company WHERE last_researched_at >= $1`, since).Scan(&s.Researched); err != nil {
		return nil, err
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM company WHERE score >= $1 AND updated_at >= $2`, highValueMinScore, since).Scan(&s.HighValue); err != nil {
		return nil, err
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM draft WHERE created_at >= $1`, since).Scan(&s.Drafted); err != nil {
		return nil, err
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM draft WHERE state = 'pending' AND (decision IS NULL OR decision != 'blocked')`).Scan(&s.AwaitingReview); err != nil {
		return nil, err
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM send_log WHERE sent_at >= $1`, since).Scan(&s.Sent); err != nil {
		return nil, err
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM send_log WHERE replied_at IS NULL AND ((followup_1_due IS NOT NULL AND followup_1_due <= now()) OR (followup_2_due IS NOT NULL AND followup_2_due <= now()))`).Scan(&s.FollowupsDue); err != nil {
		return nil, err
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM send_log WHERE replied_at >= $1`, since).Scan(&s.Replies); err != nil {
		return nil, err
	}

	return s, nil
}

// CountCompaniesForResearch mirrors ListCompaniesForResearch's eligibility rule as a plain
// count — the "N companies waiting" the research-queue panel shows (review.md §40) without
// pulling every row back just to len() it.
func (db *DB) CountCompaniesForResearch(ctx context.Context) (int, error) {
	var n int
	err := db.QueryRow(ctx,
		`SELECT count(*) FROM company
		 WHERE status = 'new'
		   AND domain_status IN ('verified', 'manual')
		   AND (next_research_at IS NULL OR next_research_at <= now())`).Scan(&n)
	return n, err
}
