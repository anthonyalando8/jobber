// prospector-assess runs the authoritative fit assessment over the stored opportunity corpus
// through the ordinary production path — the same assess.Service the web UI uses, persisting
// through the same SaveOpportunityMatch.
//
// It is not a rehearsal: it requires an active approved profile and it writes stored assessments.
// Without an active profile every opportunity answers profile_required and nothing is stored,
// which is the correct behaviour rather than an error.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/anthonyalando8/prospector/internal/assess"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/joho/godotenv"
)

// Run is the record of one authoritative pass.
type Run struct {
	StartedAt         time.Time      `json:"started_at"`
	FinishedAt        time.Time      `json:"finished_at"`
	ProfileRevisionID int64          `json:"profile_revision_id"`
	ProfileHash       string         `json:"profile_content_hash"`
	Assessed          int            `json:"assessed"`
	Stored            int            `json:"stored"`
	Classification    map[string]int `json:"classification_counts"`
	Blockers          map[string]int `json:"blocker_counts"`
	Failures          []string       `json:"failures,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("prospector-assess", flag.ContinueOnError)
	out := flags.String("out", "", "write the run record as JSON here")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return errors.New("could not load environment file")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	db, err := store.NewDB(ctx, dsn)
	if err != nil {
		return errors.New("database connection failed (connection string withheld)")
	}
	defer db.Close()

	service := assess.New(db)
	state, err := service.Profile(ctx)
	if err != nil {
		return err
	}
	if state.Document == nil {
		return errors.New("no approved candidate profile revision is active; nothing can be assessed authoritatively")
	}

	ids, err := db.AllOpportunityIDs(ctx)
	if err != nil {
		return err
	}
	// The hash is read from the stored revision rather than recomputed, so the run record names
	// exactly the approved bytes the assessments were made against.
	profileHash := ""
	if revisions, err := db.ListProfileRevisions(ctx); err == nil {
		for _, r := range revisions {
			if r.ID == state.RevisionID {
				profileHash = r.ContentHash
			}
		}
	}
	record := Run{StartedAt: time.Now().UTC(), ProfileRevisionID: state.RevisionID,
		ProfileHash: profileHash, Classification: map[string]int{}, Blockers: map[string]int{}}

	for _, id := range ids {
		assessment, err := service.AssessOpportunity(ctx, id)
		if err != nil {
			record.Failures = append(record.Failures, fmt.Sprintf("opportunity %d: %v", id, err))
			continue
		}
		record.Assessed++
		result := assessment.Match
		record.Classification[result.Classification]++
		if result.Classification != opportunity.ClassificationProfileRequired {
			record.Stored++
		}
		for _, blocker := range result.Blockers {
			record.Blockers[blocker.Code]++
		}
	}
	record.FinishedAt = time.Now().UTC()

	fmt.Printf("Authoritative assessment against profile revision %d (%s)\n",
		record.ProfileRevisionID, shortHash(record.ProfileHash))
	fmt.Printf("  assessed %d opportunities, stored %d assessments in %s\n\n",
		record.Assessed, record.Stored, record.FinishedAt.Sub(record.StartedAt).Round(time.Millisecond))
	fmt.Println("Classification:")
	for _, class := range sortedKeys(record.Classification) {
		fmt.Printf("  %-18s %5d\n", class, record.Classification[class])
	}
	fmt.Println("\nBlockers:")
	for _, code := range sortedKeys(record.Blockers) {
		fmt.Printf("  %-38s %5d\n", code, record.Blockers[code])
	}
	for _, failure := range record.Failures {
		fmt.Printf("  FAILED %s\n", failure)
	}
	if *out != "" {
		data, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out, append(data, '\n'), 0o600); err != nil {
			return err
		}
	}
	if len(record.Failures) > 0 {
		return fmt.Errorf("%d opportunities could not be assessed", len(record.Failures))
	}
	return nil
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func shortHash(hash string) string {
	if len(hash) <= 16 {
		return hash
	}
	return hash[:16] + "…"
}
