package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// PageCache is one previously-fetched page — review.md §13: the page_cache table existed in
// the schema from the start but nothing ever wrote to it; enrichment cached pages in an
// in-process map instead, so the cache didn't survive a restart and gave no benefit across
// the HTTP-triggered enrich path and the worker's research loop hitting the same URL.
type PageCache struct {
	URL       string
	Status    int
	Body      string
	FetchedAt time.Time
}

// GetPageCache returns a cached fetch, or (nil, nil) on a plain cache miss — that's the
// ordinary case, not an error.
func (db *DB) GetPageCache(ctx context.Context, url string) (*PageCache, error) {
	var pc PageCache
	err := db.QueryRow(ctx,
		`SELECT url, status, body, fetched_at FROM page_cache WHERE url = $1`, url).
		Scan(&pc.URL, &pc.Status, &pc.Body, &pc.FetchedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &pc, nil
}

// UpsertPageCache records the outcome of a page fetch — a real HTTP status and body, not a
// network-level failure (timeout, DNS, connection refused), which has no status to cache and
// should just be retried next time rather than remembered as a fixed failure.
func (db *DB) UpsertPageCache(ctx context.Context, url string, status int, body string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO page_cache (url, status, body, fetched_at) VALUES ($1, $2, $3, now())
		 ON CONFLICT (url) DO UPDATE SET status = $2, body = $3, fetched_at = now()`,
		url, status, body)
	return err
}
