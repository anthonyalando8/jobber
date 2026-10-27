package seed

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/redis/go-redis/v9"
)

// Seeder is the interface for all seed adapters
type Seeder interface {
	Name() string
	Seed(ctx context.Context, db *store.DB, redis *redis.Client) (int, error)
}

// SeedResult tracks seed operation results
type SeedResult struct {
	Source    string
	Added     int
	Skipped   int
	Errors    int
	Duration  time.Duration
	Timestamp time.Time
}

// Registry holds all available seeders
type Registry struct {
	seeders map[string]Seeder
	mu      sync.RWMutex
}

// NewRegistry creates a seed adapter registry
func NewRegistry() *Registry {
	return &Registry{
		seeders: make(map[string]Seeder),
	}
}

// Register adds a seeder to the registry
func (r *Registry) Register(seeder Seeder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seeders[seeder.Name()] = seeder
}

// List returns all available seeders
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.seeders))
	for name := range r.seeders {
		names = append(names, name)
	}
	return names
}

// Run executes a seeder by name
func (r *Registry) Run(ctx context.Context, name string, db *store.DB, redis *redis.Client) (*SeedResult, error) {
	r.mu.RLock()
	seeder, ok := r.seeders[name]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown seeder: %s", name)
	}

	start := time.Now()
	log.Printf("Seeding from %s...", name)

	added, err := seeder.Seed(ctx, db, redis)
	recordStatus(ctx, redis, name, added, time.Since(start), err)
	if err != nil {
		log.Printf("Seeding from %s failed: %v", name, err)
		return &SeedResult{
			Source:    name,
			Added:     0,
			Errors:    1,
			Duration:  time.Since(start),
			Timestamp: time.Now(),
		}, err
	}

	result := &SeedResult{
		Source:    name,
		Added:     added,
		Duration:  time.Since(start),
		Timestamp: time.Now(),
	}

	log.Printf("Seeded %d new opportunities from %s in %v", added, name, result.Duration)
	return result, nil
}

// BaseSeeder provides common functionality for all seeders
type BaseSeeder struct {
	client    *http.Client
	userAgent string
	// retryDelay is the pause before the single retry of a listing fetch.
	retryDelay time.Duration
}

// NewBaseSeeder creates a base seeder. The timeout is per request; a whole source run is bounded
// separately by the caller's context. Ten seconds was too tight for full RSS feeds and the larger
// JSON APIs from a European VPS, and a timeout there aborted the whole source.
func NewBaseSeeder(userAgent string) *BaseSeeder {
	return &BaseSeeder{
		client: &http.Client{
			Timeout: 25 * time.Second,
		},
		userAgent:  userAgent,
		retryDelay: 2 * time.Second,
	}
}

// Fetch makes an HTTP request with proper headers
func (bs *BaseSeeder) Fetch(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", bs.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := bs.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, url)
	}

	return resp, nil
}

// CheckDuplicate checks if domain already exists
func (bs *BaseSeeder) CheckDuplicate(ctx context.Context, redis *redis.Client, domain string) (bool, error) {
	key := "seen:domain:" + domain
	exists, err := redis.Exists(ctx, key).Result()
	if err != nil {
		log.Printf("Redis check error: %v", err)
		return false, nil // Allow continue on Redis error
	}
	return exists > 0, nil
}

// MarkSeen marks a domain as processed
func (bs *BaseSeeder) MarkSeen(ctx context.Context, redis *redis.Client, domain string) error {
	key := "seen:domain:" + domain
	// Keep in cache for 7 days
	return redis.Set(ctx, key, time.Now().Unix(), 7*24*time.Hour).Err()
}

// CreateCompanyIfNew creates the company if it isn't a duplicate and returns the created row so
// callers can attach signals/scores to the right company_id. domainStatus should be "manual"
// when a human supplied the domain directly (trust it) or "candidate" when it was guessed —
// see internal/verify for how a candidate domain gets promoted to verified or failed.
func (bs *BaseSeeder) CreateCompanyIfNew(ctx context.Context, db *store.DB, redis *redis.Client, name, domain, source, sourceURL, domainStatus string) (company *store.Company, created bool, err error) {
	// Check Redis cache first (fast)
	isDup, _ := bs.CheckDuplicate(ctx, redis, domain)
	if isDup {
		return nil, false, nil
	}

	// Create company
	company, err = db.CreateCompany(ctx, name, domain, source, sourceURL, domainStatus)
	if err != nil {
		// Likely duplicate key error - that's OK
		log.Printf("Company create failed (likely duplicate): %v", err)
		return nil, false, nil
	}

	// Mark in cache
	_ = bs.MarkSeen(ctx, redis, domain)
	return company, true, nil
}
