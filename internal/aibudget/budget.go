// Package aibudget treats AI draft generation as the scarce resource review.md §6 says it is
// (a Gemini free-tier key, or any rate/cost-limited provider): an hourly cap, a daily cap, and
// — for the autonomous worker specifically — a cooldown between self-initiated calls, so the
// system spends quota deliberately rather than burning through it the moment enough companies
// are enriched. Exhausting the budget is never an error: callers are expected to skip drafting
// and keep doing everything else (research, scoring) rather than fail.
package aibudget

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Config is every threshold review.md §6 asks to be configurable, with the doc's own suggested
// defaults.
type Config struct {
	Enabled         bool
	DraftsPerHour   int
	DraftsPerDay    int
	MinScore        int
	CooldownSeconds int
}

// DefaultConfig returns review.md's suggested defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:         true,
		DraftsPerHour:   5,
		DraftsPerDay:    30,
		MinScore:        70,
		CooldownSeconds: 600,
	}
}

// Budget enforces Config against Redis-backed counters — the same INCR+EXPIRE pattern the
// existing send:day:{date}/send:last_at guardrails already use, just for AI calls instead of
// sends.
type Budget struct {
	redis *redis.Client
	cfg   Config
}

// New creates a Budget.
func New(redisConn *redis.Client, cfg Config) *Budget {
	return &Budget{redis: redisConn, cfg: cfg}
}

// Enabled reports whether AI drafting is turned on at all (AI_ENABLED) — distinct from
// sendpolicy.Config.Enabled, which gates auto-*sending*, not drafting.
func (b *Budget) Enabled() bool { return b.cfg.Enabled }

// MinScore is the score floor below which a candidate shouldn't be offered to the AI at all —
// review.md §8: "score < threshold -> no AI call." Exposed so callers can filter their
// candidate query with it before ever reaching Allow.
func (b *Budget) MinScore() int { return b.cfg.MinScore }

// Allow reports whether one more AI draft call is currently permitted, and if not, a
// human-readable reason why. checkCooldown should be true only for the autonomous worker loop
// deciding on its own initiative to spend a call — a human explicitly clicking "Generate
// Draft" for one company, or running a manual batch, has already exercised that judgment and
// only needs the hard hourly/daily resource caps enforced, not the pacing cooldown meant to
// keep the *unattended* loop from spending quota faster than review.md §45's "quality beats
// volume" principle wants.
func (b *Budget) Allow(ctx context.Context, checkCooldown bool) (bool, string) {
	if !b.cfg.Enabled {
		return false, "AI drafting disabled (AI_ENABLED=false)"
	}

	hourUsed, dayUsed := b.usage(ctx)
	if hourUsed >= b.cfg.DraftsPerHour {
		return false, fmt.Sprintf("hourly AI budget used (%d/%d)", hourUsed, b.cfg.DraftsPerHour)
	}
	if dayUsed >= b.cfg.DraftsPerDay {
		return false, fmt.Sprintf("daily AI budget used (%d/%d)", dayUsed, b.cfg.DraftsPerDay)
	}

	if checkCooldown && b.cfg.CooldownSeconds > 0 {
		val, err := b.redis.Get(ctx, lastCallKey).Result()
		if err == nil {
			if lastUnix, perr := strconv.ParseInt(val, 10, 64); perr == nil {
				since := time.Since(time.Unix(lastUnix, 0))
				if wait := time.Duration(b.cfg.CooldownSeconds)*time.Second - since; wait > 0 {
					return false, fmt.Sprintf("cooldown active, next draft in %v", wait.Round(time.Second))
				}
			}
		}
	}

	return true, ""
}

const lastCallKey = "ai:last_call_at"

// Record marks that one AI call was just made — increments the hourly/daily counters and
// resets the cooldown clock. Call this only after a real call to the provider, the same way
// the send-side recordSend only fires after an actual SMTP send succeeds.
func (b *Budget) Record(ctx context.Context) {
	now := time.Now()
	hourKey := "ai:hour:" + now.Format("2006-01-02T15")
	dayKey := "ai:day:" + now.Format("2006-01-02")

	if err := b.redis.Incr(ctx, hourKey).Err(); err == nil {
		b.redis.Expire(ctx, hourKey, time.Hour)
	}
	if err := b.redis.Incr(ctx, dayKey).Err(); err == nil {
		b.redis.Expire(ctx, dayKey, 24*time.Hour)
	}
	b.redis.Set(ctx, lastCallKey, now.Unix(), time.Duration(b.cfg.CooldownSeconds)*time.Second+time.Minute)
}

// Usage returns (hourUsed, hourLimit, dayUsed, dayLimit) for display — the dashboard's "AI
// USAGE: today 7/30, this hour 2/5" review.md §39 asks for.
func (b *Budget) Usage(ctx context.Context) (hourUsed, hourLimit, dayUsed, dayLimit int) {
	used1, used2 := b.usage(ctx)
	return used1, b.cfg.DraftsPerHour, used2, b.cfg.DraftsPerDay
}

// NextAvailable reports when the next AI draft call will be permitted, and why it isn't right
// now — review.md §39's "Next available: 10:42". ok is true when a call is allowed immediately
// (the returned time is zero and meaningless in that case).
func (b *Budget) NextAvailable(ctx context.Context) (t time.Time, ok bool, reason string) {
	if !b.cfg.Enabled {
		return time.Time{}, false, "AI drafting disabled"
	}

	hourUsed, dayUsed := b.usage(ctx)
	now := time.Now()

	if dayUsed >= b.cfg.DraftsPerDay {
		tomorrow := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
		return tomorrow, false, "daily budget used"
	}
	if hourUsed >= b.cfg.DraftsPerHour {
		nextHour := now.Truncate(time.Hour).Add(time.Hour)
		return nextHour, false, "hourly budget used"
	}

	if b.cfg.CooldownSeconds > 0 {
		if val, err := b.redis.Get(ctx, lastCallKey).Result(); err == nil {
			if lastUnix, perr := strconv.ParseInt(val, 10, 64); perr == nil {
				next := time.Unix(lastUnix, 0).Add(time.Duration(b.cfg.CooldownSeconds) * time.Second)
				if next.After(now) {
					return next, false, "cooldown"
				}
			}
		}
	}

	return time.Time{}, true, ""
}

func (b *Budget) usage(ctx context.Context) (hourUsed, dayUsed int) {
	now := time.Now()
	hourKey := "ai:hour:" + now.Format("2006-01-02T15")
	dayKey := "ai:day:" + now.Format("2006-01-02")

	if v, err := b.redis.Get(ctx, hourKey).Result(); err == nil {
		hourUsed, _ = strconv.Atoi(v)
	}
	if v, err := b.redis.Get(ctx, dayKey).Result(); err == nil {
		dayUsed, _ = strconv.Atoi(v)
	}
	return hourUsed, dayUsed
}
