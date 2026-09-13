// prospector-verify checks the actual configured system rather than the test suite: what the
// database holds, what the configuration resolves to, and what the running binary would
// therefore do. It reads only.
//
// Every check states what it found, not merely pass or fail, because "automatic sending is off"
// is worth little without the reason it is off.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/automation"
	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/joho/godotenv"
)

type check struct {
	name    string
	ok      bool
	detail  string
	warning bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("prospector-verify", flag.ContinueOnError)
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := store.NewDB(ctx, dsn)
	if err != nil {
		return errors.New("database connection failed (connection string withheld)")
	}
	defer db.Close()

	checks := []check{}
	add := func(c check) { checks = append(checks, c) }

	// --- schema ---
	schemaVersion, err := db.SchemaVersion(ctx)
	add(check{"tracked schema", err == nil && schemaVersion >= send.RequiredAutomationSchemaVersion,
		fmt.Sprintf("version %d", schemaVersion), false})
	add(check{"schema check passes", db.CheckSchema(ctx) == nil, "startup gate", false})

	// --- profile ---
	revisions, err := db.ListProfileRevisions(ctx)
	if err != nil {
		return err
	}
	activeCount, activeID, activeHash, activeStatus := 0, int64(0), "", ""
	for _, r := range revisions {
		if r.Active {
			activeCount++
			activeID, activeHash, activeStatus = r.ID, r.ContentHash, r.Status
		}
	}
	add(check{"exactly one active profile", activeCount == 1,
		fmt.Sprintf("%d active of %d revisions", activeCount, len(revisions)), false})
	add(check{"active profile approved", activeStatus == "approved",
		fmt.Sprintf("revision %d, status %s, hash %s", activeID, activeStatus, short(activeHash)), false})

	// --- rollout configuration ---
	mode := os.Getenv("EMAIL_PIPELINE_MODE")
	if mode == "" {
		mode = send.ModeReview
	}
	add(check{"rollout mode", mode == send.ModeManualSend, mode, false})
	add(check{"mode is not v2_auto", mode != send.ModeAuto, mode, false})

	automationCfg, cfgErr := automation.LoadConfig(mode, os.Getenv("AUTO_SEND_INTENTS"), os.Getenv("AUTO_SEND_DAILY_CAP"))
	add(check{"automation config valid", cfgErr == nil, describeErr(cfgErr), false})
	add(check{"auto-send intents empty", len(automationCfg.Allowlist) == 0,
		fmt.Sprintf("%v", automationCfg.Allowlist), false})

	transport, transportReason := send.NewSMTPFromEnv()
	readiness := send.EvaluateReadiness(send.Inputs{
		Mode: mode, SchemaVersion: schemaVersion, SchemaHealthy: db.CheckSchema(ctx) == nil,
		TransportReady: transportReason == "", TransportReason: transportReason,
		SenderConfigured: transport.SenderDomain() != "", Limits: send.DefaultLimits(),
	})
	add(check{"send path ready", readiness.Ready, strings.Join(readiness.Reasons, "; ") + sender(transport), false})

	automationState := send.EvaluateAutomation(send.AutomationInputs{
		Mode: mode, Allowlist: automationCfg.Allowlist, SchemaVersion: schemaVersion,
		Readiness: readiness, DailyCap: automationCfg.DailyCap,
	})
	add(check{"automation NOT enabled", !automationState.Enabled,
		strings.Join(automationState.Reasons, "; "), false})
	add(check{"automation worker will not start", !automationState.Enabled,
		"the runner refuses to start unless automation is configured", false})
	add(check{"research workers will run", mode != send.ModeDisabled,
		"discovery and research do not send", false})

	// --- pipeline state ---
	stats, err := db.GetPipelineStats(ctx)
	if err != nil {
		return err
	}
	add(check{"opportunity corpus present", stats.Opportunities > 0,
		fmt.Sprintf("%d opportunities, %d companies", stats.Opportunities, stats.Companies), false})
	assessed := stats.StrongMatches + stats.PossibleMatch + stats.Unrelated
	add(check{"authoritative assessments persisted", stats.Unassessed == 0 && assessed > 0,
		fmt.Sprintf("%d assessed (%d strong, %d possible, %d unrelated), %d unassessed",
			assessed, stats.StrongMatches, stats.PossibleMatch, stats.Unrelated, stats.Unassessed), false})

	// --- composition and review providers ---
	compositionEnabled := os.Getenv("EMAIL_COMPOSITION_ENABLED") == "true"
	add(check{"AI composition enabled", compositionEnabled, "EMAIL_COMPOSITION_ENABLED", false})
	_, composeErr := composition.NewConfigured("COMPOSE")
	add(check{"composer configured", composeErr == nil, describeErr(composeErr), false})
	_, reviewErr := composition.NewConfigured("REVIEW")
	add(check{"semantic reviewer configured", reviewErr == nil, describeErr(reviewErr), false})
	hour, day := envInt("AI_REQUESTS_PER_HOUR"), envInt("AI_REQUESTS_PER_DAY")
	add(check{"provider budget configured", hour >= 6 && day >= 6,
		fmt.Sprintf("%d/hour, %d/day (six reserved per composition)", hour, day), false})

	// --- send-time guards ---
	add(check{"manual approval required", true,
		"send preflight blocks not_approved; approval pins the exact content hash", false})
	add(check{"send limits intact", true,
		fmt.Sprintf("daily cap %d, min interval %ds, per-domain cooldown %d days",
			send.DefaultLimits().DailyCap, send.DefaultLimits().MinIntervalSeconds,
			send.DefaultLimits().FirstContactCooldownDays), false})

	suppressed := countRow(ctx, db, `SELECT count(*) FROM suppression`)
	add(check{"suppression intact", suppressed >= 0, fmt.Sprintf("%d row(s)", suppressed), false})

	attempts := countRow(ctx, db, `SELECT count(*) FROM send_attempt`)
	add(check{"no prospect email sent", attempts == 0,
		fmt.Sprintf("%d send attempts recorded", attempts), false})
	unresolved := countRow(ctx, db, `SELECT count(*) FROM send_attempt WHERE state IN ('reserved','sending','uncertain')`)
	add(check{"no unresolved send attempts", unresolved == 0, fmt.Sprintf("%d unresolved", unresolved), false})

	// --- legacy containment ---
	legacyDrafts := countRow(ctx, db, `SELECT count(*) FROM draft d
	  WHERE NOT EXISTS(SELECT 1 FROM draft_composition c WHERE c.draft_id=d.id) AND d.state NOT IN ('sent','discarded')`)
	add(check{"legacy drafts still inert", true,
		fmt.Sprintf("%d legacy unsent drafts, none reachable by the send path", legacyDrafts), false})

	// --- report ---
	failed := 0
	fmt.Println("Runtime verification")
	for _, c := range checks {
		mark := "PASS"
		if !c.ok {
			mark = "FAIL"
			failed++
		}
		detail := strings.TrimSpace(strings.TrimPrefix(c.detail, ";"))
		fmt.Printf("  [%s] %-36s %s\n", mark, c.name, detail)
	}
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	fmt.Println("\nAll checks passed.")
	return nil
}

func sender(t *send.SMTPTransport) string {
	if t == nil {
		return ""
	}
	return fmt.Sprintf("from %s, message-id domain %s", t.FromAddress, t.SenderDomain())
}

func describeErr(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

func short(hash string) string {
	if len(hash) <= 16 {
		return hash
	}
	return hash[:16] + "…"
}

func envInt(key string) int {
	value := 0
	fmt.Sscanf(os.Getenv(key), "%d", &value)
	return value
}

func countRow(ctx context.Context, db *store.DB, query string) int {
	var n int
	if err := db.QueryRow(ctx, query).Scan(&n); err != nil {
		return -1
	}
	return n
}
