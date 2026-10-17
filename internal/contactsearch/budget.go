package contactsearch

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Budget bounds what contact discovery may spend in a day, counted separately from every other
// consumer of search and AI capacity.
//
// It is its own budget on purpose. Contact discovery is now the bottleneck, and a bottleneck
// sharing a pool with domain resolution and drafting would either starve them or be starved by
// them depending on tick order — which is not a decision anybody would choose deliberately.
// The defaults are conservative: this pipeline is meant to work carefully on the few companies
// that matter, not to maximize coverage.
type Budget struct {
	redis *redis.Client
	cfg   BudgetConfig
}

// BudgetConfig is the daily allowance. Every field is configurable; the defaults assume the free
// tier of a search API shared with domain resolution.
type BudgetConfig struct {
	// CompaniesPerDay caps how many distinct companies are examined in a day.
	CompaniesPerDay int
	// SearchesPerDay caps search-engine requests.
	SearchesPerDay int
	// FetchesPerDay caps public pages fetched.
	FetchesPerDay int
	// AICallsPerDay caps AI extraction calls. It defaults to zero: deterministic extraction is
	// sufficient, so AI is opt-in rather than opt-out.
	AICallsPerDay int
}

// DefaultBudgetConfig returns the conservative defaults.
func DefaultBudgetConfig() BudgetConfig {
	return BudgetConfig{CompaniesPerDay: 10, SearchesPerDay: 30, FetchesPerDay: 60, AICallsPerDay: 0}
}

// Counter names. Spend counters bound work; outcome counters explain what the spend bought, and
// are what the dashboard reads.
const (
	CounterCompanies = "companies"
	CounterSearches  = "searches"
	CounterFetches   = "fetches"
	CounterAICalls   = "ai_calls"

	CounterContactsFound     = "contacts_found"
	CounterRoutesFound       = "routes_found"
	CounterRecruitersFound   = "recruiters_found"
	CounterEngineeringFound  = "engineering_found"
	CounterPeopleWithoutMail = "people_without_email"
	CounterNoResult          = "no_result"
	CounterFailures          = "failures"
	CounterIdentityBlocked   = "identity_blocked"
)

// NewBudget creates a budget. A nil Redis client yields a budget that allows everything and
// records nothing, so tests and one-off tooling do not need a running Redis to exercise the
// pipeline's logic.
func NewBudget(redisConn *redis.Client, cfg BudgetConfig) *Budget {
	defaults := DefaultBudgetConfig()
	if cfg.CompaniesPerDay <= 0 {
		cfg.CompaniesPerDay = defaults.CompaniesPerDay
	}
	if cfg.SearchesPerDay <= 0 {
		cfg.SearchesPerDay = defaults.SearchesPerDay
	}
	if cfg.FetchesPerDay <= 0 {
		cfg.FetchesPerDay = defaults.FetchesPerDay
	}
	if cfg.AICallsPerDay < 0 {
		cfg.AICallsPerDay = 0
	}
	return &Budget{redis: redisConn, cfg: cfg}
}

// Config returns the configured limits.
func (b *Budget) Config() BudgetConfig { return b.cfg }

func dayKey(counter string) string {
	return "contactsearch:day:" + time.Now().UTC().Format("2006-01-02") + ":" + counter
}

const pausedKey = "contactsearch:paused"

// IsPaused reports the runtime kill switch. Like domain search's, it takes effect on the next
// company rather than the next restart: noticing mid-run that this is doing the wrong thing and
// having to edit a file and restart is not a usable stop button.
func (b *Budget) IsPaused(ctx context.Context) bool {
	if b == nil || b.redis == nil {
		return false
	}
	exists, err := b.redis.Exists(ctx, pausedKey).Result()
	if err != nil {
		return false // fail open on a Redis hiccup, matching the rest of this codebase
	}
	return exists > 0
}

// Pause stops contact discovery until Resume is called. No TTL: a deliberate stop stays stopped.
func (b *Budget) Pause(ctx context.Context) error {
	if b == nil || b.redis == nil {
		return nil
	}
	return b.redis.Set(ctx, pausedKey, 1, 0).Err()
}

// Resume clears the kill switch.
func (b *Budget) Resume(ctx context.Context) error {
	if b == nil || b.redis == nil {
		return nil
	}
	return b.redis.Del(ctx, pausedKey).Err()
}

// Used reports today's count for one counter.
func (b *Budget) Used(ctx context.Context, counter string) int {
	if b == nil || b.redis == nil {
		return 0
	}
	value, err := b.redis.Get(ctx, dayKey(counter)).Result()
	if err != nil {
		return 0
	}
	used, _ := strconv.Atoi(value)
	return used
}

// Record adds to a counter. Spend counters are recorded when the spend happens — after a search
// request is issued, successful or not, because the provider counted it either way.
func (b *Budget) Record(ctx context.Context, counter string, n int) {
	if b == nil || b.redis == nil || n == 0 {
		return
	}
	key := dayKey(counter)
	total, err := b.redis.IncrBy(ctx, key, int64(n)).Result()
	if err == nil && total == int64(n) {
		b.redis.Expire(ctx, key, 48*time.Hour)
	}
}

// Limit returns the configured cap for a spend counter, or zero for a counter that is not capped.
func (b *Budget) Limit(counter string) int {
	switch counter {
	case CounterCompanies:
		return b.cfg.CompaniesPerDay
	case CounterSearches:
		return b.cfg.SearchesPerDay
	case CounterFetches:
		return b.cfg.FetchesPerDay
	case CounterAICalls:
		return b.cfg.AICallsPerDay
	}
	return 0
}

// Allow reports whether one more unit of a spend counter is permitted, and if not, why. The
// reason is returned rather than logged so it can be shown to the operator on the page where
// they are wondering why nothing happened.
func (b *Budget) Allow(ctx context.Context, counter string) (bool, string) {
	if b == nil {
		return true, ""
	}
	if b.IsPaused(ctx) {
		return false, "contact discovery is paused"
	}
	limit := b.Limit(counter)
	if limit <= 0 {
		if counter == CounterAICalls {
			return false, "AI extraction is not enabled for contact discovery"
		}
		return true, ""
	}
	if used := b.Used(ctx, counter); used >= limit {
		return false, fmt.Sprintf("today's contact-discovery %s budget is spent (%d/%d)", counter, used, limit)
	}
	return true, ""
}

// Usage is today's spend and outcomes, for the dashboard and the manual action page.
type Usage struct {
	Companies, CompaniesLimit int
	Searches, SearchesLimit   int
	Fetches, FetchesLimit     int
	AICalls, AICallsLimit     int

	ContactsFound     int
	RoutesFound       int
	RecruitersFound   int
	EngineeringFound  int
	PeopleWithoutMail int
	NoResult          int
	Failures          int
	IdentityBlocked   int
	Paused            bool
}

// Usage reads every counter for today.
func (b *Budget) Usage(ctx context.Context) Usage {
	if b == nil {
		return Usage{}
	}
	return Usage{
		Companies: b.Used(ctx, CounterCompanies), CompaniesLimit: b.cfg.CompaniesPerDay,
		Searches: b.Used(ctx, CounterSearches), SearchesLimit: b.cfg.SearchesPerDay,
		Fetches: b.Used(ctx, CounterFetches), FetchesLimit: b.cfg.FetchesPerDay,
		AICalls: b.Used(ctx, CounterAICalls), AICallsLimit: b.cfg.AICallsPerDay,
		ContactsFound:     b.Used(ctx, CounterContactsFound),
		RoutesFound:       b.Used(ctx, CounterRoutesFound),
		RecruitersFound:   b.Used(ctx, CounterRecruitersFound),
		EngineeringFound:  b.Used(ctx, CounterEngineeringFound),
		PeopleWithoutMail: b.Used(ctx, CounterPeopleWithoutMail),
		NoResult:          b.Used(ctx, CounterNoResult),
		Failures:          b.Used(ctx, CounterFailures),
		IdentityBlocked:   b.Used(ctx, CounterIdentityBlocked),
		Paused:            b.IsPaused(ctx),
	}
}
