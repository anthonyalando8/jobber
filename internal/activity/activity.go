// Package activity is the small Redis-backed "what just happened" log shown on the
// dashboard. Shared between internal/web (HTTP-triggered actions) and internal/worker
// (ticker-triggered actions) so both feed the same list instead of each keeping its own.
package activity

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	activityKey     = "activity:log"
	activityChannel = "activity:events"
	activityMaxKept = 40
)

// Entry is one row of the dashboard's activity feed.
type Entry struct {
	Action    string    `json:"action"`
	Detail    string    `json:"detail"`
	Success   bool      `json:"success"`
	Timestamp time.Time `json:"timestamp"`
}

// Log records one action so the dashboard can show it. Best-effort — a Redis hiccup here must
// never break the action it's logging.
func Log(ctx context.Context, client *redis.Client, action, detail string, success bool) {
	entry := Entry{Action: action, Detail: detail, Success: success, Timestamp: time.Now()}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if err := client.LPush(ctx, activityKey, data).Err(); err != nil {
		log.Printf("activity.Log: redis error: %v", err)
		return
	}
	client.LTrim(ctx, activityKey, 0, activityMaxKept-1)

	// Best-effort fan-out to anyone watching the live dashboard feed. Publish has no persistence
	// of its own — a moment with zero subscribers just means no one sees this one in real time,
	// which is fine, since the list above remains the source of truth on page load/refresh.
	if err := client.Publish(ctx, activityChannel, data).Err(); err != nil {
		log.Printf("activity.Log: publish error: %v", err)
	}
}

// Recent returns the most recent entries, newest first.
func Recent(ctx context.Context, client *redis.Client) []Entry {
	raw, err := client.LRange(ctx, activityKey, 0, activityMaxKept-1).Result()
	if err != nil {
		return nil
	}

	entries := make([]Entry, 0, len(raw))
	for _, r := range raw {
		var e Entry
		if err := json.Unmarshal([]byte(r), &e); err == nil {
			entries = append(entries, e)
		}
	}
	return entries
}

// Subscribe opens a Redis pub/sub subscription to the live activity channel — one entry per
// Log call, as it happens. The caller owns the returned subscription and must Close() it (e.g.
// when the watching HTTP client disconnects).
func Subscribe(ctx context.Context, client *redis.Client) *redis.PubSub {
	return client.Subscribe(ctx, activityChannel)
}
