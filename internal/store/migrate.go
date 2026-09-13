package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationLock int64 = 739208144

type Migration struct {
	Version             int
	Name, SQL, Checksum string
}

func Migrations() ([]Migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var result []Migration
	for _, entry := range entries {
		name := entry.Name()
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		// Normalize checkout line endings, not SQL semantics.
		sql := strings.ReplaceAll(string(body), "\r\n", "\n")
		hash := sha256.Sum256([]byte(sql))
		result = append(result, Migration{v, name, sql, hex.EncodeToString(hash[:])})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Version < result[j].Version })
	for i, m := range result {
		if m.Version != i+1 {
			return nil, fmt.Errorf("migration sequence must be contiguous at %s", m.Name)
		}
	}
	return result, nil
}

type ColumnInfo struct {
	Name, Type string
	Nullable   bool
}
type TableInventory struct {
	Columns     []ColumnInfo
	Constraints []string
	Indexes     []string
	Rows        int64
	DataHash    string
	// LegacyDataHash fingerprints only the original pre-v2 columns of an application table.
	// An additive migration changes DataHash simply by adding a column, so this is what
	// actually proves that no original value in any original row was altered.
	LegacyDataHash string
}
type SchemaReport struct {
	CapturedAt            time.Time
	ServerVersion         string
	Tables                map[string]TableInventory
	History               []MigrationRecord
	Missing               []string
	DuplicateSignalGroups int64
	Sequences             map[string]string
}
type MigrationRecord struct {
	Version  int
	Name     string
	Checksum *string
	Kind     string
}

var applicationTables = []string{"company", "contact", "signal", "page_cache", "draft", "send_log", "suppression", "template"}

// Required legacy columns through 0016. Copy-only migrations cannot be inferred
// from these milestones; the baseline is explicitly not an execution record.
var requiredColumns = map[string]string{
	"company":     "id:bigint name:text domain:text source:text source_url:text segment:text score:integer status:text location:text notes:text created_at:timestamp_with_time_zone updated_at:timestamp_with_time_zone last_researched_at:timestamp_with_time_zone next_research_at:timestamp_with_time_zone domain_status:text domain_confidence:real score_breakdown:jsonb",
	"contact":     "id:bigint company_id:bigint kind:text value:text person_name:text role:text quality:integer status:text bad_reason:text bad_at:timestamp_with_time_zone",
	"signal":      "id:bigint company_id:bigint kind:text detail:text url:text created_at:timestamp_with_time_zone",
	"page_cache":  "url:text status:integer body:text fetched_at:timestamp_with_time_zone",
	"draft":       "id:bigint company_id:bigint contact_id:bigint channel:text subject:text body:text evidence_used:text state:text edited_by_human:boolean created_at:timestamp_with_time_zone decision:text decision_reason:text decision_at:timestamp_with_time_zone score_at_decision:integer",
	"send_log":    "id:bigint draft_id:bigint company_id:bigint to_value:text channel:text sent_at:timestamp_with_time_zone provider_response:text followup_1_due:date followup_2_due:date replied_at:timestamp_with_time_zone followup_1_sent_at:timestamp_with_time_zone followup_2_sent_at:timestamp_with_time_zone bounced_at:timestamp_with_time_zone bounce_reason:text",
	"suppression": "value:text reason:text created_at:timestamp_with_time_zone",
	"template":    "id:bigint segment:text channel:text role_filter:text subject:text body:text display_only:boolean created_at:timestamp_with_time_zone",
}

// LegacyColumns lists an application table's original pre-v2 columns in a fixed order, or nil
// for a table that has no legacy definition to preserve.
func LegacyColumns(table string) []string {
	spec, ok := requiredColumns[table]
	if !ok {
		return nil
	}
	fields := strings.Fields(spec)
	columns := make([]string, 0, len(fields))
	for _, field := range fields {
		columns = append(columns, strings.SplitN(field, ":", 2)[0])
	}
	return columns
}

// PreservationDiff reports how a table changed between two inventories. Added columns are an
// expected result of an additive migration; anything else is a preservation failure.
func PreservationDiff(before, after TableInventory) (added []string, problems []string) {
	if before.Rows != after.Rows {
		problems = append(problems, fmt.Sprintf("row count %d became %d", before.Rows, after.Rows))
	}
	if before.LegacyDataHash == "" || after.LegacyDataHash == "" {
		problems = append(problems, "original-column fingerprint unavailable")
	} else if before.LegacyDataHash != after.LegacyDataHash {
		problems = append(problems, "original column values changed")
	}
	existing := map[string]ColumnInfo{}
	for _, column := range before.Columns {
		existing[column.Name] = column
	}
	seen := map[string]bool{}
	for _, column := range after.Columns {
		seen[column.Name] = true
		original, ok := existing[column.Name]
		if !ok {
			added = append(added, column.Name)
			continue
		}
		if original.Type != column.Type || original.Nullable != column.Nullable {
			problems = append(problems, "column "+column.Name+" was retyped")
		}
	}
	for name := range existing {
		if !seen[name] {
			problems = append(problems, "column "+name+" was removed")
		}
	}
	sort.Strings(added)
	sort.Strings(problems)
	return added, problems
}

func legacyGaps(tables map[string]TableInventory) []string {
	var gaps []string
	for table, spec := range requiredColumns {
		actual, ok := tables[table]
		if !ok {
			gaps = append(gaps, "missing table: "+table)
			continue
		}
		for _, field := range strings.Fields(spec) {
			parts := strings.SplitN(field, ":", 2)
			found := false
			for _, col := range actual.Columns {
				if col.Name == parts[0] && col.Type == strings.ReplaceAll(parts[1], "_", " ") {
					found = true
				}
			}
			if !found {
				gaps = append(gaps, "missing or incompatible column: "+table+"."+parts[0])
			}
		}
	}
	sort.Strings(gaps)
	return gaps
}

func legacyConstraintGaps(tables map[string]TableInventory) []string {
	var gaps []string
	for _, name := range applicationTables {
		table, ok := tables[name]
		if !ok {
			continue
		}
		pk := "PRIMARY KEY (id)"
		if name == "page_cache" {
			pk = "PRIMARY KEY (url)"
		}
		if name == "suppression" {
			pk = "PRIMARY KEY (value)"
		}
		wanted := []string{pk}
		if name == "company" {
			wanted = append(wanted, "UNIQUE (domain)")
		}
		if name == "contact" {
			wanted = append(wanted, "UNIQUE (company_id, kind, value)")
		}
		if name == "contact" || name == "signal" || name == "draft" || name == "send_log" {
			wanted = append(wanted, "FOREIGN KEY (company_id) REFERENCES company(id)")
		}
		if name == "draft" {
			wanted = append(wanted, "FOREIGN KEY (contact_id) REFERENCES contact(id)")
		}
		if name == "send_log" {
			wanted = append(wanted, "FOREIGN KEY (draft_id) REFERENCES draft(id)")
		}
		for _, want := range wanted {
			found := false
			for _, got := range table.Constraints {
				if strings.HasPrefix(strings.ReplaceAll(got, "public.", ""), want) {
					found = true
				}
			}
			if !found {
				gaps = append(gaps, "missing constraint: "+name+" "+want)
			}
		}
	}
	sort.Strings(gaps)
	return gaps
}

// InspectSchema uses a read-only repeatable-read snapshot. Data is represented
// by sorted row digests, never by email bodies, contacts, or template prose.
func InspectSchema(ctx context.Context, conn *pgx.Conn, includeData bool) (*SchemaReport, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	report := &SchemaReport{CapturedAt: time.Now().UTC(), Tables: map[string]TableInventory{}}
	if err := tx.QueryRow(ctx, "SHOW server_version").Scan(&report.ServerVersion); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT table_name, column_name, data_type, is_nullable = 'YES' FROM information_schema.columns WHERE table_schema = 'public' ORDER BY table_name, ordinal_position`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		var col ColumnInfo
		if err := rows.Scan(&name, &col.Name, &col.Type, &col.Nullable); err != nil {
			rows.Close()
			return nil, err
		}
		t := report.Tables[name]
		t.Columns = append(t.Columns, col)
		report.Tables[name] = t
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for name, table := range report.Tables {
		if name == "schema_migrations" {
			continue
		}
		rows, err := tx.Query(ctx, `SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname='public' AND t.relname=$1 ORDER BY 1`, name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				return nil, err
			}
			table.Constraints = append(table.Constraints, s)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname='public' AND tablename=$1 ORDER BY indexname`, name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				return nil, err
			}
			table.Indexes = append(table.Indexes, s)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		rows.Close()
		if includeData {
			// Sorting row digests is independent of physical row/column order after restore.
			query := `SELECT md5(to_jsonb(t)::text) FROM ` + pgx.Identifier{"public", name}.Sanitize() + ` t ORDER BY 1`
			rows, err := tx.Query(ctx, query)
			if err != nil {
				return nil, err
			}
			h := sha256.New()
			for rows.Next() {
				var digest string
				if err := rows.Scan(&digest); err != nil {
					rows.Close()
					return nil, err
				}
				h.Write([]byte(digest + "\n"))
				table.Rows++
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			rows.Close()
			table.DataHash = hex.EncodeToString(h.Sum(nil))
			if legacy := LegacyColumns(name); len(legacy) > 0 {
				idents := make([]string, 0, len(legacy))
				for _, column := range legacy {
					idents = append(idents, "t."+pgx.Identifier{column}.Sanitize())
				}
				rows, err := tx.Query(ctx, `SELECT md5(to_jsonb(row(`+strings.Join(idents, ",")+`))::text) FROM `+
					pgx.Identifier{"public", name}.Sanitize()+` t ORDER BY 1`)
				if err != nil {
					return nil, err
				}
				lh := sha256.New()
				for rows.Next() {
					var digest string
					if err := rows.Scan(&digest); err != nil {
						rows.Close()
						return nil, err
					}
					lh.Write([]byte(digest + "\n"))
				}
				if err := rows.Err(); err != nil {
					rows.Close()
					return nil, err
				}
				rows.Close()
				table.LegacyDataHash = hex.EncodeToString(lh.Sum(nil))
			}
		}
		report.Tables[name] = table
	}
	if _, ok := report.Tables["schema_migrations"]; ok {
		rows, err := tx.Query(ctx, `SELECT version,name,checksum,kind FROM schema_migrations ORDER BY version`)
		if err != nil {
			return nil, fmt.Errorf("unsupported migration ledger: %w", err)
		}
		for rows.Next() {
			var rec MigrationRecord
			if err := rows.Scan(&rec.Version, &rec.Name, &rec.Checksum, &rec.Kind); err != nil {
				rows.Close()
				return nil, err
			}
			report.History = append(report.History, rec)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		rows.Close()
	}
	report.Missing = legacyGaps(report.Tables)
	report.Missing = append(report.Missing, legacyConstraintGaps(report.Tables)...)
	if includeData {
		report.Sequences = map[string]string{}
		seqRows, err := tx.Query(ctx, `SELECT sequencename FROM pg_sequences WHERE schemaname='public' ORDER BY sequencename`)
		if err != nil {
			return nil, err
		}
		var names []string
		for seqRows.Next() {
			var name string
			if err := seqRows.Scan(&name); err != nil {
				seqRows.Close()
				return nil, err
			}
			names = append(names, name)
		}
		if err := seqRows.Err(); err != nil {
			return nil, err
		}
		seqRows.Close()
		for _, name := range names {
			var value int64
			var called bool
			if err := tx.QueryRow(ctx, `SELECT last_value,is_called FROM `+pgx.Identifier{"public", name}.Sanitize()).Scan(&value, &called); err != nil {
				return nil, err
			}
			report.Sequences[name] = fmt.Sprintf("%d/%t", value, called)
		}
	}
	if includeData {
		if _, ok := report.Tables["signal"]; ok {
			err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT company_id,kind,detail,url FROM signal GROUP BY company_id,kind,detail,url HAVING count(*)>1) x`).Scan(&report.DuplicateSignalGroups)
			if err != nil {
				return nil, err
			}
		}
	}
	return report, tx.Commit(ctx)
}

func verifyHistory(history []MigrationRecord, migrations []Migration) (int, error) {
	version := 0
	for i, rec := range history {
		if rec.Version > len(migrations) || rec.Version < 1 {
			return 0, fmt.Errorf("database migration version %d is incompatible with this binary", rec.Version)
		}
		if rec.Kind == "baseline" {
			if i != 0 || rec.Version != 16 || rec.Checksum != nil {
				return 0, fmt.Errorf("invalid legacy baseline record")
			}
		} else if rec.Kind == "applied" {
			m := migrations[rec.Version-1]
			if rec.Version != version+1 || rec.Checksum == nil || *rec.Checksum != m.Checksum || rec.Name != m.Name {
				return 0, fmt.Errorf("migration history/checksum mismatch at version %d", rec.Version)
			}
		} else {
			return 0, fmt.Errorf("unknown migration record kind %q", rec.Kind)
		}
		version = rec.Version
	}
	return version, nil
}

func CheckSchema(ctx context.Context, conn *pgx.Conn) error {
	migrations, err := Migrations()
	if err != nil {
		return err
	}
	report, err := InspectSchema(ctx, conn, false)
	if err != nil {
		return err
	}
	version, err := verifyHistory(report.History, migrations)
	if err != nil {
		return err
	}
	if version != len(migrations) {
		return fmt.Errorf("schema is untracked or pending migrations: run prospector-migrate inspect, then explicit baseline/apply after backup verification")
	}
	if len(report.Missing) > 0 {
		return fmt.Errorf("schema drift: %s", strings.Join(report.Missing, "; "))
	}
	return checkV2Schema(ctx, conn, version, report)
}

const ledgerSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
 version integer PRIMARY KEY, name text NOT NULL, checksum text,
 kind text NOT NULL CHECK (kind IN ('baseline','applied')),
 applied_at timestamptz NOT NULL DEFAULT now(), inventory jsonb,
 CHECK ((kind='baseline' AND checksum IS NULL) OR (kind='applied' AND checksum IS NOT NULL))
)`

// ApplyMigrations refuses an untracked populated database. Legacy copy migrations
// must never be replayed over operator-edited templates.
func ApplyMigrations(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx, `SET search_path TO public`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLock)
	report, err := InspectSchema(ctx, conn, false)
	if err != nil {
		return err
	}
	if _, tracked := report.Tables["schema_migrations"]; !tracked && len(report.Tables) > 0 {
		return fmt.Errorf("untracked nonempty schema: inspect and explicitly baseline; no migrations applied")
	}
	migrations, err := Migrations()
	if err != nil {
		return err
	}
	version, err := verifyHistory(report.History, migrations)
	if err != nil {
		return err
	}
	if version >= 16 && len(report.Missing) > 0 {
		return fmt.Errorf("schema drift requires explicit repair: %s", strings.Join(report.Missing, "; "))
	}
	if err := checkV2Schema(ctx, conn, version, report); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, ledgerSQL); err != nil {
		return err
	}
	for _, m := range migrations {
		if m.Version <= version {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		// 0001 has its own outer transaction. Strip only these exact standalone
		// wrappers; the runner owns the transaction including the history write.
		sql := migrationBody(m)
		if _, err = tx.Exec(ctx, sql); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version,name,checksum,kind) VALUES($1,$2,$3,'applied')`, m.Version, m.Name, m.Checksum)
		}
		if err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s failed: %w", m.Name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return CheckSchema(ctx, conn)
}

func migrationBody(m Migration) string {
	if m.Version != 1 {
		return m.SQL
	}
	lines := strings.Split(m.SQL, "\n")
	var result []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "BEGIN;" || strings.TrimSpace(line) == "COMMIT;" {
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// BaselineLegacy records observed structure/data at 0016 without asserting that
// any copy migration ran. It changes only the new ledger, never application data.
func BaselineLegacy(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx, `SET search_path TO public`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLock)
	report, err := InspectSchema(ctx, conn, true)
	if err != nil {
		return err
	}
	if len(report.History) > 0 {
		return fmt.Errorf("database already tracked; baseline refused")
	}
	for _, name := range []string{"candidate_profile", "candidate_profile_revision", "opportunity", "opportunity_revision", "ingestion_review", "legacy_opportunity_backfill"} {
		if _, exists := report.Tables[name]; exists {
			return fmt.Errorf("v2 table %s exists without migration history; explicit recovery required", name)
		}
	}
	if len(report.Missing) > 0 {
		return fmt.Errorf("incomplete legacy schema; explicit repair required: %s", strings.Join(report.Missing, "; "))
	}
	doc, err := json.Marshal(report)
	if err != nil {
		return err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, ledgerSQL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version,name,kind,inventory) VALUES(16,'legacy_structure_observed_through_0016','baseline',$1)`, doc); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
