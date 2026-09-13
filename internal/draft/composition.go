package draft

import (
	"context"
	"fmt"
	"time"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/store"
)

// RunComposition is invoked only for a user-requested run with an owned database
// reservation. It is never wired to the autonomous scheduler or to SMTP.
func RunComposition(ctx context.Context, db *store.DB, id int64, owner string, s composition.Snapshot, composer composition.Composer, reviewer composition.Reviewer, edit *composition.Email) error {
	status := "failed"
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = db.FinishComposition(cleanup, id, owner, status)
	}()
	engine := composition.Engine{Composer: composer, Reviewer: reviewer,
		BeforeCall: func(ctx context.Context, stage string) error {
			stale, err := db.CompositionStaleReasons(ctx, s)
			if err != nil {
				return err
			}
			if len(stale) > 0 {
				return fmt.Errorf("composition inputs changed: %v", stale)
			}
			return db.ConsumeCompositionCall(ctx, id, owner, stage)
		},
		Record: func(ctx context.Context, a composition.Attempt) error {
			return db.RecordCompositionAttempt(ctx, id, owner, a)
		},
	}
	result, err := engine.Run(ctx, s, edit)
	if err != nil {
		return err
	}
	stale, err := db.CompositionStaleReasons(ctx, s)
	if err != nil {
		return err
	}
	status = result.Status
	if len(stale) > 0 {
		status = "review_required"
	}
	return nil
}
