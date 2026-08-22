package domainsearch

import (
	"context"
	"fmt"
	"log"

	"github.com/anthonyalando8/prospector/internal/activity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/anthonyalando8/prospector/internal/verify"
	"github.com/redis/go-redis/v9"
)

// ResolveFailedDomains is the one implementation both the autonomous worker tick and the manual
// admin trigger call — same reasoning as executeSend/GenerateAndSaveDraft elsewhere in this
// codebase: one real implementation, multiple triggers, so they can't drift apart. For each
// company still stuck at domain_status='failed', it asks Searcher for candidate domains and
// re-runs the existing verify.Verifier against each one — a search hit is only ever a proposal
// until it passes the same "does the company's name actually appear on this page" check every
// guessed candidate already has to.
func ResolveFailedDomains(ctx context.Context, db *store.DB, verifier verify.Verifier, searcher *Searcher, redisConn *redis.Client, limit int) (resolved, attempted int, err error) {
	if !searcher.Enabled() || searcher.IsPaused(ctx) {
		return 0, 0, nil
	}

	companies, err := db.ListCompaniesForDomainResolution(ctx, limit)
	if err != nil {
		return 0, 0, err
	}

	for _, c := range companies {
		if allowed, reason := searcher.Allow(ctx); !allowed {
			log.Printf("domainsearch: stopping early, %s", reason)
			break
		}
		if searcher.RecentlyAttempted(ctx, c.ID) {
			continue
		}

		attempted++
		candidates, serr := searcher.Search(ctx, c.Name)
		searcher.Record(ctx) // a real API call was spent either way
		if serr != nil {
			log.Printf("domainsearch: search failed for %s: %v", c.Name, serr)
			continue
		}

		verifiedDomain := ""
		var confidence float64
		for _, cand := range candidates {
			status, conf, verr := verifier.Verify(ctx, verify.Company{Name: c.Name, Domain: cand})
			if verr != nil {
				continue
			}
			if status == verify.StatusVerified {
				verifiedDomain, confidence = cand, conf
				break
			}
		}

		if verifiedDomain != "" {
			if err := db.UpdateCompanyDomain(ctx, c.ID, verifiedDomain); err != nil {
				log.Printf("domainsearch: failed to update domain for %s: %v", c.Name, err)
				continue
			}
			if err := db.UpdateCompanyDomainStatus(ctx, c.ID, string(verify.StatusVerified), confidence); err != nil {
				log.Printf("domainsearch: failed to update domain status for %s: %v", c.Name, err)
			}
			activity.Log(ctx, redisConn, "worker:domain_resolve", fmt.Sprintf("Resolved %s -> %s", c.Name, verifiedDomain), true)
			resolved++
		} else {
			searcher.MarkAttempted(ctx, c.ID)
			activity.Log(ctx, redisConn, "worker:domain_resolve", fmt.Sprintf("No verifiable domain found for %s", c.Name), false)
		}
	}

	return resolved, attempted, nil
}
