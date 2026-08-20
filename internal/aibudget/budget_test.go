package aibudget

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// testRedis connects to REDIS_URL for a real integration test (matches how this project's
// other infra-backed tests are gated) and uses a dedicated key prefix + FlushDB-free cleanup
// via explicit key deletion, so it never touches real application data.
func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL not set — skipping aibudget integration test")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("invalid REDIS_URL: %v", err)
	}
	client := redis.NewClient(opts)
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skipf("Redis unreachable at %s: %v", url, err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		client.Del(ctx, "ai:last_call_at")
		now := time.Now()
		client.Del(ctx, "ai:hour:"+now.Format("2006-01-02T15"))
		client.Del(ctx, "ai:day:"+now.Format("2006-01-02"))
		client.Close()
	})
	return client
}

func TestBudget_AllowsUntilHourlyLimit(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()
	cfg := Config{Enabled: true, DraftsPerHour: 3, DraftsPerDay: 100, MinScore: 0, CooldownSeconds: 0}
	b := New(client, cfg)

	for i := 0; i < 3; i++ {
		allowed, reason := b.Allow(ctx, false)
		if !allowed {
			t.Fatalf("Allow() call %d = false (%s), want true — under the hourly limit of %d", i+1, reason, cfg.DraftsPerHour)
		}
		b.Record(ctx)
	}

	allowed, reason := b.Allow(ctx, false)
	if allowed {
		t.Error("Allow() after hitting the hourly limit = true, want false")
	}
	if reason == "" {
		t.Error("Allow() refusal has no reason — review.md requires knowing WHY, never a bare boolean")
	}
}

func TestBudget_DailyLimitIndependentOfHourly(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()
	cfg := Config{Enabled: true, DraftsPerHour: 100, DraftsPerDay: 2, MinScore: 0, CooldownSeconds: 0}
	b := New(client, cfg)

	b.Record(ctx)
	b.Record(ctx)

	allowed, _ := b.Allow(ctx, false)
	if allowed {
		t.Error("Allow() after hitting the daily limit = true, want false, even though the hourly limit wasn't reached")
	}
}

func TestBudget_DisabledNeverAllows(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()
	cfg := DefaultConfig()
	cfg.Enabled = false
	b := New(client, cfg)

	if allowed, _ := b.Allow(ctx, false); allowed {
		t.Error("Allow() with AI_ENABLED=false = true, want false")
	}
}

func TestBudget_CooldownOnlyAppliesWhenRequested(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()
	cfg := Config{Enabled: true, DraftsPerHour: 100, DraftsPerDay: 100, MinScore: 0, CooldownSeconds: 3600}
	b := New(client, cfg)

	b.Record(ctx) // sets the cooldown clock

	// A manual/human-triggered call (checkCooldown=false) must not be blocked by the
	// autonomous-loop pacing cooldown — only the hard caps apply to it.
	if allowed, reason := b.Allow(ctx, false); !allowed {
		t.Errorf("Allow(checkCooldown=false) right after a call = false (%s), want true — cooldown shouldn't gate a manual send", reason)
	}

	// The autonomous worker (checkCooldown=true) must respect it.
	if allowed, _ := b.Allow(ctx, true); allowed {
		t.Error("Allow(checkCooldown=true) right after a call = true, want false — the autonomous loop must respect its own cooldown")
	}
}

func TestBudget_UsageReflectsRecordedCalls(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()
	cfg := DefaultConfig()
	b := New(client, cfg)

	b.Record(ctx)
	b.Record(ctx)

	hourUsed, _, dayUsed, _ := b.Usage(ctx)
	if hourUsed != 2 || dayUsed != 2 {
		t.Errorf("Usage() = hour=%d day=%d, want both 2 after two Record() calls", hourUsed, dayUsed)
	}
}
