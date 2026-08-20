package web

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/anthonyalando8/prospector/internal/activity"
)

// ActivityEntry is kept as an alias so existing template/handler references don't need to
// change — the actual storage now lives in internal/activity, shared with internal/worker.
type ActivityEntry = activity.Entry

// logActivity records one pipeline action so the dashboard can show it.
func (h *Handler) logActivity(ctx context.Context, action, detail string, success bool) {
	activity.Log(ctx, h.redis, action, detail, success)
}

// recentActivity returns the most recent entries, newest first.
func (h *Handler) recentActivity(ctx context.Context) []ActivityEntry {
	return activity.Recent(ctx, h.redis)
}

// ActivityStream is a Server-Sent Events endpoint: it pushes each new activity.Entry to the
// dashboard the moment it's logged, so "what's happening right now" no longer means tailing
// server logs. One goroutine per connection, fed by Redis pub/sub — nothing buffered beyond
// whatever's already in flight on the channel.
func (h *Handler) ActivityStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	sub := activity.Subscribe(ctx, h.redis)
	defer sub.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // in case this ever sits behind nginx
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := sub.Channel()

	// Idle proxies/load balancers tend to kill a connection that's gone quiet — a periodic
	// comment line keeps this one visibly alive without meaning anything to the client's
	// onmessage handler (SSE comments start with ':' and are ignored by EventSource).
	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg.Payload)
			flusher.Flush()
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}
