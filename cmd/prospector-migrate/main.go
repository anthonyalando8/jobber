// prospector-migrate is intentionally separate from the web/worker binary.
// Inspect and backup never start workers or initialize AI/SMTP clients.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("prospector-migrate", flag.ContinueOnError)
	command := flags.String("command", "inspect", "inspect | check | profile | activate-profile | legacy-inventory | contact-endpoints | backup-verify | baseline | apply | backfill-opportunities")
	out := flags.String("out", "", "report JSON path (inspect) or new backup archive path (backup-verify)")
	proof := flags.String("proof", "", "verified backup manifest required for baseline or existing-database apply")
	pgBin := flags.String("pg-bin", "", "directory containing pg_dump and pg_restore")
	apply := flags.Bool("apply", false, "for contact-endpoints: record the findings instead of only reporting them")
	revision := flags.Int64("revision", 0, "for activate-profile: the candidate profile revision id being approved")
	hash := flags.String("hash", "", "for activate-profile: the content hash of the revision you reviewed")
	rehearse := flags.Bool("rehearse-phase2", false, "after backup restore verification, rehearse pending migrations and legacy backfill only in the owned scratch database")
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return errors.New("database connection failed (connection string withheld)")
	}
	defer conn.Close(context.Background())
	switch *command {
	case "backfill-opportunities":
		if err := store.CheckSchema(ctx, conn); err != nil {
			return err
		}
		if err := verifyProof(ctx, conn, *proof); err != nil {
			return err
		}
		db, err := store.NewDB(ctx, dsn)
		if err != nil {
			return errors.New("backfill connection failed (connection string withheld)")
		}
		defer db.Close()
		total := store.BackfillResult{}
		for {
			batch, err := db.BackfillLegacyOpportunities(ctx, 100)
			if err != nil {
				return err
			}
			total.Processed += batch.Processed
			total.Converted += batch.Converted
			total.ReviewRequired += batch.ReviewRequired
			if batch.Processed == 0 {
				break
			}
		}
		if *out != "" {
			if err := writeJSON(*out, total); err != nil {
				return err
			}
		}
		fmt.Printf("Backfill processed %d signals: %d converted, %d require review. Original rows unchanged.\n", total.Processed, total.Converted, total.ReviewRequired)
		return nil
	case "inspect":
		report, err := store.InspectSchema(ctx, conn, true)
		if err != nil {
			return err
		}
		if *out != "" {
			if err := writeJSON(*out, report); err != nil {
				return err
			}
		}
		fmt.Printf("Inspected %d tables; %d missing/incompatible legacy columns; %d history records; %d duplicate signal groups.\n", len(report.Tables), len(report.Missing), len(report.History), report.DuplicateSignalGroups)
		for _, gap := range report.Missing {
			fmt.Println(gap)
		}
		return nil
	case "check":
		return store.CheckSchema(ctx, conn)
	case "contact-endpoints":
		// Scan stored contacts for addresses that are machine endpoints rather than mailboxes.
		// Read-only unless -apply is given, and even then it marks rather than deletes.
		db, err := store.NewDB(ctx, dsn)
		if err != nil {
			return errors.New("connection failed (connection string withheld)")
		}
		defer db.Close()
		found, err := db.MarkNonContactEndpoints(ctx, !*apply)
		if err != nil {
			return err
		}
		byPurpose := map[string]int{}
		for _, e := range found {
			byPurpose[e.Purpose]++
			fmt.Printf("  contact %d  %-46s %-12s %s\n", e.ContactID, e.Address, e.Purpose, e.CompanyName)
		}
		verb := "would be marked unusable"
		if *apply {
			verb = "marked unusable"
		}
		fmt.Printf("%d contact(s) %s. Nothing was deleted.\n", len(found), verb)
		for _, purpose := range sortedKeys(countsAsStrings(byPurpose)) {
			fmt.Printf("  %-14s %d\n", purpose, byPurpose[purpose])
		}
		if !*apply {
			fmt.Println("Re-run with -apply to record these.")
		}
		if *out != "" {
			if err := writeJSON(*out, found); err != nil {
				return err
			}
		}
		return nil
	case "activate-profile":
		// The same call the /profile page makes, with the same guards: an explicit revision, the
		// content hash the operator actually read, and the active revision they observed. It
		// cannot activate a revision whose contents have changed since they were displayed, and
		// it modifies nothing about the document itself.
		if *revision <= 0 {
			return errors.New("-revision is required: name the revision id you are approving")
		}
		if strings.TrimSpace(*hash) == "" {
			return errors.New("-hash is required: paste the content hash you reviewed")
		}
		db, err := store.NewDB(ctx, dsn)
		if err != nil {
			return errors.New("connection failed (connection string withheld)")
		}
		defer db.Close()
		before, err := db.ListProfileRevisions(ctx)
		if err != nil {
			return err
		}
		var observedActive int64
		for _, r := range before {
			if r.Active {
				observedActive = r.ID
			}
		}
		if err := db.ActivateProfile(ctx, *revision, *hash, observedActive); err != nil {
			return err
		}
		after, err := db.ListProfileRevisions(ctx)
		if err != nil {
			return err
		}
		fmt.Println("Profile activation result:")
		activeCount := 0
		for _, r := range after {
			marker := " "
			if r.Active {
				marker = "*"
				activeCount++
			}
			fmt.Printf("  %s revision id %d (revision %d) status=%-9s active=%v hash=%s\n",
				marker, r.ID, r.Revision, r.Status, r.Active, r.ContentHash)
		}
		if activeCount != 1 {
			return fmt.Errorf("expected exactly one active revision, found %d", activeCount)
		}
		return nil
	case "profile":
		// Present the stored candidate profile exactly as written, with the differences from the
		// last approved revision. It reads only: reviewing a profile must never be capable of
		// changing one, and activation is a separate deliberate act on the profile page.
		db, err := store.NewDB(ctx, dsn)
		if err != nil {
			return errors.New("connection failed (connection string withheld)")
		}
		defer db.Close()
		comparison, err := db.CompareProfileRevisions(ctx)
		if err != nil {
			return err
		}
		printProfile(comparison)
		if *out != "" {
			if err := writeJSON(*out, comparison); err != nil {
				return err
			}
		}
		return nil
	case "legacy-inventory":
		// Read-only census of what the pre-v2 pipeline left behind, plus content digests over
		// historical outbound text. Nothing here writes, so it is safe to run at any time and
		// to compare across a transition.
		inventory, err := store.LegacyInventoryReport(ctx, conn)
		if err != nil {
			return err
		}
		if *out != "" {
			if err := writeJSON(*out, inventory); err != nil {
				return err
			}
		}
		fmt.Printf("Legacy inventory at schema %d:\n", inventory.SchemaVersion)
		for _, category := range inventory.Categories {
			fmt.Printf("  %-36s %6d  %s\n", category.Name, category.Count, category.Disposition)
		}
		fmt.Println("Preservation digests:")
		for _, name := range sortedKeys(inventory.Preservation) {
			fmt.Printf("  %-20s %s\n", name, inventory.Preservation[name])
		}
		return nil
	case "backup-verify":
		if *out == "" {
			return errors.New("-out is required for a new backup archive")
		}
		return backupVerify(ctx, conn, dsn, *pgBin, *out, *rehearse)
	case "baseline":
		if err := verifyProof(ctx, conn, *proof); err != nil {
			return err
		}
		if err := store.BaselineLegacy(ctx, conn); err != nil {
			return err
		}
		fmt.Println("Recorded explicit legacy baseline; existing application rows and templates unchanged.")
		return nil
	case "apply":
		report, err := store.InspectSchema(ctx, conn, false)
		if err != nil {
			return err
		}
		if len(report.Tables) > 0 {
			if err := verifyProof(ctx, conn, *proof); err != nil {
				return err
			}
		}
		if err := store.ApplyMigrations(ctx, conn); err != nil {
			return err
		}
		fmt.Println("Schema is current.")
		return nil
	default:
		return fmt.Errorf("unknown command %q", *command)
	}
}

type backupProof struct {
	VerifiedAt     time.Time
	Archive        string
	ArchiveSHA256  string
	Source         *store.SchemaReport
	RestoreMatched bool
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

func sameData(a, b *store.SchemaReport) bool {
	if !reflect.DeepEqual(a.Sequences, b.Sequences) {
		return false
	}
	for name, at := range a.Tables {
		if name == "schema_migrations" {
			continue
		}
		bt, ok := b.Tables[name]
		if !ok || at.Rows != bt.Rows || at.DataHash != bt.DataHash || !reflect.DeepEqual(at.Columns, bt.Columns) || !reflect.DeepEqual(at.Constraints, bt.Constraints) || !reflect.DeepEqual(at.Indexes, bt.Indexes) {
			return false
		}
	}
	for name := range b.Tables {
		if name != "schema_migrations" {
			if _, ok := a.Tables[name]; !ok {
				return false
			}
		}
	}
	return true
}

func verifyProof(ctx context.Context, conn *pgx.Conn, path string) error {
	if path == "" {
		return errors.New("verified backup manifest required: run backup-verify while application is stopped, then supply -proof")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var proof backupProof
	if err := json.Unmarshal(data, &proof); err != nil {
		return err
	}
	if !proof.RestoreMatched || proof.Source == nil || time.Since(proof.VerifiedAt) > 24*time.Hour {
		return errors.New("backup proof missing, invalid, or older than 24 hours")
	}
	hash, err := fileHash(proof.Archive)
	if err != nil {
		return err
	}
	if hash != proof.ArchiveSHA256 {
		return errors.New("backup archive hash differs from verified proof")
	}
	report, err := store.InspectSchema(ctx, conn, true)
	if err != nil {
		return err
	}
	if !sameData(proof.Source, report) {
		return errors.New("database changed since verified backup; stop application and create a fresh verified backup")
	}
	return nil
}

func pgCommand(ctx context.Context, bin, tool, dsn string, args ...string) error {
	if bin != "" {
		tool = filepath.Join(bin, tool)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid database tool configuration")
	}
	if filepath.Base(tool) == "pg_restore" {
		args = append([]string{"--dbname=" + cfg.Database}, args...)
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	// Connection credentials are passed in the child environment, not command
	// arguments, process listings, or log output.
	for _, e := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(e), "PG") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	sslMode := "disable"
	if cfg.TLSConfig != nil {
		sslMode = "require"
		if !cfg.TLSConfig.InsecureSkipVerify {
			sslMode = "verify-full"
		}
		if len(cfg.Fallbacks) > 0 {
			sslMode = "prefer"
		}
	}
	cmd.Env = append(cmd.Env, "PGDATABASE="+cfg.Database, "PGHOST="+cfg.Host, "PGPORT="+strconv.Itoa(int(cfg.Port)), "PGUSER="+cfg.User, "PGPASSWORD="+cfg.Password, "PGSSLMODE="+sslMode, "PGCONNECT_TIMEOUT=10")
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		for key, env := range map[string]string{"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY", "options": "PGOPTIONS"} {
			if v := u.Query().Get(key); v != "" {
				cmd.Env = append(cmd.Env, env+"="+v)
			}
		}
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed (database diagnostics withheld): %w", filepath.Base(tool), err)
	}
	return nil
}

func backupVerify(ctx context.Context, source *pgx.Conn, dsn, bin, out string, rehearse bool) (resultErr error) {
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		return errors.New("backup output already exists or cannot be inspected; choose a new path")
	}
	before, err := store.InspectSchema(ctx, source, true)
	if err != nil {
		return err
	}
	if err := pgCommand(ctx, bin, "pg_dump", dsn, "--format=custom", "--no-owner", "--no-privileges", "--file="+out); err != nil {
		return err
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return err
	}
	scratch := "prospector_verify_" + hex.EncodeToString(bytes)
	if _, err := source.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{scratch}.Sanitize()+" TEMPLATE template0"); err != nil {
		return fmt.Errorf("backup saved; could not create isolated restore database: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := source.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{scratch}.Sanitize()+" WITH (FORCE)")
		if err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove owned scratch database %s manually: %w", scratch, err))
		}
	}()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	// Build a libpq URI from parsed configuration while retaining query settings.
	scratchDSN, err := databaseDSN(dsn, scratch)
	if err != nil {
		return err
	}
	if err := pgCommand(ctx, bin, "pg_restore", scratchDSN, "--no-owner", "--no-privileges", "--exit-on-error", abs); err != nil {
		return err
	}
	cfg.Database = scratch
	restored, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return errors.New("could not connect to restored scratch database")
	}
	defer restored.Close(ctx)
	var actual string
	if err := restored.QueryRow(ctx, `SELECT current_database()`).Scan(&actual); err != nil || actual != scratch {
		return errors.New("restore destination assertion failed")
	}
	after, err := store.InspectSchema(ctx, restored, true)
	if err != nil {
		return err
	}
	current, err := store.InspectSchema(ctx, source, true)
	if err != nil {
		return err
	}
	if !sameData(before, after) || !sameData(before, current) {
		return errors.New("restore/source fingerprint mismatch; no verified manifest issued")
	}
	if rehearse {
		if err := store.ApplyMigrations(ctx, restored); err != nil {
			return fmt.Errorf("scratch migration rehearsal: %w", err)
		}
		db, err := store.NewDB(ctx, scratchDSN)
		if err != nil {
			return errors.New("scratch backfill connection failed")
		}
		defer db.Close()
		if err := db.QueryRow(ctx, `SELECT current_database()`).Scan(&actual); err != nil || actual != scratch {
			return errors.New("scratch backfill destination assertion failed")
		}
		total := store.BackfillResult{}
		for {
			batch, err := db.BackfillLegacyOpportunities(ctx, 100)
			if err != nil {
				return err
			}
			total.Processed += batch.Processed
			total.Converted += batch.Converted
			total.ReviewRequired += batch.ReviewRequired
			if batch.Processed == 0 {
				break
			}
		}
		migrated, err := store.InspectSchema(ctx, restored, true)
		if err != nil {
			return err
		}
		// An additive migration may add columns to an application table. What it may never do is
		// change an original value, drop or retype an original column, or add or lose a row.
		additions := map[string][]string{}
		for _, name := range []string{"company", "contact", "signal", "page_cache", "draft", "send_log", "suppression", "template"} {
			added, problems := store.PreservationDiff(before.Tables[name], migrated.Tables[name])
			if len(problems) > 0 {
				return fmt.Errorf("scratch rehearsal changed original table %s: %s", name, strings.Join(problems, "; "))
			}
			if len(added) > 0 {
				additions[name] = added
				fmt.Printf("Rehearsal added columns to %s: %s (all %d original rows preserved unchanged)\n",
					name, strings.Join(added, ", "), migrated.Tables[name].Rows)
			}
		}
		for name, value := range before.Sequences {
			if migrated.Sequences[name] != value {
				return fmt.Errorf("scratch rehearsal changed original sequence %s", name)
			}
		}
		if err := writeJSON(out+".rehearsal.json", struct {
			Backfill     store.BackfillResult
			AddedColumns map[string][]string
			After        *store.SchemaReport
		}{total, additions, migrated}); err != nil {
			return err
		}
		fmt.Printf("Scratch migration/backfill rehearsal preserved every original table and sequence: %d converted signals, %d review cases.\n", total.Converted, total.ReviewRequired)
	}
	hash, err := fileHash(abs)
	if err != nil {
		return err
	}
	proof := backupProof{time.Now().UTC(), abs, hash, before, true}
	if err := writeJSON(out+".verified.json", proof); err != nil {
		return err
	}
	fmt.Printf("Backup restored in isolated database; schema, row counts, and data fingerprints match. Proof: %s.verified.json\n", out)
	return nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func databaseDSN(dsn, database string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", errors.New("invalid database URI")
		}
		u.Path = "/" + database
		u.RawPath = ""
		q := u.Query()
		q.Del("dbname")
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	// Keyword DSNs support a final dbname override; the generated database name
	// contains only a constant prefix and random hexadecimal characters.
	if _, err := pgx.ParseConfig(dsn); err != nil {
		return "", errors.New("invalid database configuration")
	}
	return dsn + " dbname=" + database, nil
}

// sortedKeys keeps report output stable so two runs can be compared directly.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// countsAsStrings adapts a count map for the shared key sorter.
func countsAsStrings(counts map[string]int) map[string]string {
	out := make(map[string]string, len(counts))
	for k := range counts {
		out[k] = ""
	}
	return out
}

// printProfile renders a stored profile revision verbatim. Every value comes from the stored
// document; nothing is inferred, summarized or filled in.
func printProfile(c *store.ProfileComparison) {
	latest := c.Latest
	fmt.Printf("Latest candidate profile revision\n")
	fmt.Printf("  revision id    %d\n", latest.ID)
	fmt.Printf("  revision no    %d\n", latest.Revision)
	fmt.Printf("  content hash   %s\n", latest.ContentHash)
	fmt.Printf("  status         %s\n", latest.Status)
	fmt.Printf("  active         %v\n", latest.Active)
	fmt.Printf("  created        %s\n", latest.CreatedAt.Format(time.RFC3339))

	d := latest.Document
	fmt.Printf("\nIdentity\n  %s\n", d.Identity)
	fmt.Printf("  primary strength:   %s\n", d.PrimaryStrength)
	fmt.Printf("  secondary position: %s\n", d.SecondaryPosition)

	fmt.Printf("\nDesired roles (%d)\n", len(d.DesiredRoles))
	for _, r := range d.DesiredRoles {
		fmt.Printf("  %-16s %s%s\n", r.ID, r.Name, aliasList(r.Aliases))
	}
	fmt.Printf("\nTechnologies (%d)\n", len(d.Technologies))
	for _, t := range d.Technologies {
		fmt.Printf("  %-16s %s%s\n", t.ID, t.Name, aliasList(t.Aliases))
	}
	fmt.Printf("\nCapabilities (%d)\n", len(d.Capabilities))
	for _, cap := range d.Capabilities {
		fmt.Printf("  %-16s %s\n      %s\n", cap.ID, cap.Name, cap.Description)
		if len(cap.TechnologyIDs) > 0 {
			fmt.Printf("      technologies: %s\n", strings.Join(cap.TechnologyIDs, ", "))
		}
		if len(cap.ProjectIDs) > 0 {
			fmt.Printf("      projects: %s\n", strings.Join(cap.ProjectIDs, ", "))
		}
	}
	fmt.Printf("\nProjects (%d)\n", len(d.Projects))
	for _, project := range d.Projects {
		fmt.Printf("  %-16s %s\n      %s\n", project.ID, project.Name, project.Description)
		for _, r := range project.Responsibilities {
			fmt.Printf("      responsibility: %s\n", r.Text)
		}
		for _, o := range project.Outcomes {
			fmt.Printf("      outcome: %s\n", o.Text)
		}
		if len(project.TechnologyIDs) > 0 {
			fmt.Printf("      technologies: %s\n", strings.Join(project.TechnologyIDs, ", "))
		}
	}
	if len(d.Domains) > 0 {
		fmt.Printf("\nDomains (%d)\n", len(d.Domains))
		for _, f := range d.Domains {
			fmt.Printf("  %-16s %s\n", f.ID, f.Text)
		}
	}
	if len(d.Specializations) > 0 {
		fmt.Printf("\nSpecializations (%d)\n", len(d.Specializations))
		for _, f := range d.Specializations {
			fmt.Printf("  %-16s %s\n", f.ID, f.Text)
		}
	}
	fmt.Printf("\nEngagement preferences\n")
	if d.Engagement == nil {
		fmt.Println("  (none stated — unknown preferences prevent unattended intent selection)")
	} else {
		e := d.Engagement
		fmt.Printf("  employment types: %s\n", orNoneList(e.EmploymentTypes))
		if e.ServiceWork != nil {
			fmt.Printf("  service work:     %v\n", *e.ServiceWork)
		} else {
			fmt.Println("  service work:     (not stated)")
		}
		if e.Remote != nil {
			fmt.Printf("  remote:           %v\n", *e.Remote)
		} else {
			fmt.Println("  remote:           (not stated)")
		}
		fmt.Printf("  locations:        %s\n", orNoneList(e.Locations))
		fmt.Printf("  availability:     %s\n", orNotStated(e.Availability))
		fmt.Printf("  pricing:          %s\n", orNotStated(e.Pricing))
	}
	fmt.Printf("\nResources (%d)\n", len(d.Resources))
	for _, r := range d.Resources {
		target := r.URL
		if target == "" {
			if r.AvailableOnRequest {
				target = "(available on request — no public link)"
			} else {
				target = "(no link, not marked available on request)"
			}
		}
		fmt.Printf("  %-16s [%s] %s\n      %s\n", r.ID, r.Kind, r.Label, target)
	}
	fmt.Printf("\nSignature\n")
	fmt.Printf("  name:       %s\n", orNotStated(d.Signature.Name))
	fmt.Printf("  descriptor: %s\n", orNotStated(d.Signature.Descriptor))
	fmt.Printf("  location:   %s\n", orNotStated(d.Signature.Location))
	fmt.Printf("  phone:      %s\n", orNotStated(d.Signature.Phone))
	fmt.Printf("  resources:  %s\n", orNoneList(d.Signature.ResourceIDs))

	fmt.Printf("\nDifference from the last approved revision\n")
	if c.Approved == nil {
		fmt.Println("  No other approved revision exists to compare against.")
		return
	}
	fmt.Printf("  comparing against revision id %d (revision %d, hash %s)\n",
		c.Approved.ID, c.Approved.Revision, c.Approved.ContentHash)
	if len(c.Differences) == 0 {
		fmt.Println("  No differences: the documents are identical.")
		return
	}
	for _, diff := range c.Differences {
		fmt.Printf("\n  %s\n", diff.Section)
		for _, added := range diff.Added {
			fmt.Printf("    + %s\n", added)
		}
		for _, removed := range diff.Removed {
			fmt.Printf("    - %s\n", removed)
		}
		for _, changed := range diff.Changed {
			fmt.Printf("    ~ %s\n", changed)
		}
	}
}

func aliasList(aliases []string) string {
	if len(aliases) == 0 {
		return ""
	}
	return "  (aliases: " + strings.Join(aliases, ", ") + ")"
}

func orNoneList(values []string) string {
	if len(values) == 0 {
		return "(none stated)"
	}
	return strings.Join(values, ", ")
}

func orNotStated(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(not stated)"
	}
	return value
}
