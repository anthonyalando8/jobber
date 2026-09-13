package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func TestMigrationHistoryRejectsDrift(t *testing.T) {
	ms, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	history := []MigrationRecord{}
	for _, m := range ms {
		sum := m.Checksum
		history = append(history, MigrationRecord{m.Version, m.Name, &sum, "applied"})
	}
	if got, err := verifyHistory(history, ms); err != nil || got != len(ms) {
		t.Fatalf("valid history: %d %v", got, err)
	}
	bad := "changed"
	history[0].Checksum = &bad
	if _, err := verifyHistory(history, ms); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	if _, err := verifyHistory([]MigrationRecord{{Version: 17, Kind: "baseline"}}, ms); err == nil {
		t.Fatal("future version accepted")
	}
	if _, err := verifyHistory([]MigrationRecord{{Version: 16, Name: "observed", Kind: "baseline"}}, ms); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyHistory([]MigrationRecord{{Version: 16, Kind: "baseline", Checksum: &bad}}, ms); err == nil {
		t.Fatal("baseline with fabricated checksum accepted")
	}
}

func TestMigrationOwnsLegacyTransaction(t *testing.T) {
	ms, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	body := migrationBody(ms[0])
	if strings.Contains(body, "\nBEGIN;") || strings.Contains(body, "\nCOMMIT;") {
		t.Fatal("nested transaction wrapper retained")
	}
	if !strings.Contains(body, "INSERT INTO template") {
		t.Fatal("template data stripped")
	}
}

func TestLegacyGapsRejectMissingOrWrongTypes(t *testing.T) {
	tables := map[string]TableInventory{}
	for table, spec := range requiredColumns {
		var cols []ColumnInfo
		for _, field := range strings.Fields(spec) {
			p := strings.SplitN(field, ":", 2)
			cols = append(cols, ColumnInfo{Name: p[0], Type: strings.ReplaceAll(p[1], "_", " ")})
		}
		tables[table] = TableInventory{Columns: cols}
	}
	if gaps := legacyGaps(tables); len(gaps) != 0 {
		t.Fatal(gaps)
	}
	table := tables["draft"]
	table.Columns[0].Type = "text"
	tables["draft"] = table
	if gaps := legacyGaps(tables); len(gaps) != 1 {
		t.Fatal(gaps)
	}
}

// This opt-in test connects to the supplied server only to create/drop a unique
// scratch database. It never migrates or writes the configured application DB.
func migrationTestDB(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("MIGRATION_TEST_DATABASE_URL")
	if path := os.Getenv("MIGRATION_TEST_ENV_FILE"); path != "" {
		vars, err := godotenv.Read(path)
		if err != nil {
			t.Fatal("cannot read test connection environment")
		}
		dsn = vars["DATABASE_URL"]
	}
	if dsn == "" {
		t.Skip("set MIGRATION_TEST_DATABASE_URL or MIGRATION_TEST_ENV_FILE for isolated database tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("test admin connection failed (DSN withheld)")
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	name := "prospector_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE template0"); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("scratch cleanup %s: %v", name, err)
		}
	})
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test config")
	}
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal("scratch connection failed")
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func TestMigrationsFreshApplyBaselineAndPreserveData(t *testing.T) {
	conn := migrationTestDB(t)
	ctx := context.Background()
	if err := CheckSchema(ctx, conn); err == nil {
		t.Fatal("empty schema accepted at startup")
	}
	if err := ApplyMigrations(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE template SET body='operator custom copy' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	before, err := InspectSchema(ctx, conn, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, conn); err != nil {
		t.Fatal(err)
	}
	after, err := InspectSchema(ctx, conn, true)
	if err != nil {
		t.Fatal(err)
	}
	if before.Tables["template"].DataHash != after.Tables["template"].DataHash {
		t.Fatal("repeat apply changed template content")
	}
	if _, err := conn.Exec(ctx, `DROP TABLE schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, conn); err == nil {
		t.Fatal("untracked existing schema replayed")
	}
	if err := BaselineLegacy(ctx, conn); err == nil {
		t.Fatal("v2 schema with lost history must not be labeled legacy")
	}
	conn = migrationTestDB(t)
	ms, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms[:16] {
		if _, err := conn.Exec(ctx, migrationBody(m)); err != nil {
			t.Fatal(err)
		}
	}
	before, err = InspectSchema(ctx, conn, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := BaselineLegacy(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(ctx, conn); err == nil {
		t.Fatal("pending v2 migrations accepted")
	}
	after, err = InspectSchema(ctx, conn, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.History) != 1 || after.History[0].Kind != "baseline" || after.History[0].Checksum != nil {
		t.Fatal("baseline falsifies execution history")
	}
	for _, name := range applicationTables {
		if before.Tables[name].DataHash != after.Tables[name].DataHash {
			t.Fatalf("baseline changed %s", name)
		}
	}
	if _, err := conn.Exec(ctx, `ALTER TABLE draft DROP COLUMN decision`); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchema(ctx, conn); err == nil {
		t.Fatal("schema drift accepted")
	}
}

func TestIncompleteLegacyBaselineRefused(t *testing.T) {
	conn := migrationTestDB(t)
	ctx := context.Background()
	ms, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, migrationBody(ms[0])); err != nil {
		t.Fatal(err)
	}
	if err := BaselineLegacy(ctx, conn); err == nil {
		t.Fatal("partial schema baselined")
	}
	var ledger *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations')::text`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if ledger != nil {
		t.Fatal("refused baseline created ledger")
	}
}

func TestBaselineRejectsMissingUniqueness(t *testing.T) {
	conn := migrationTestDB(t)
	ctx := context.Background()
	if err := ApplyMigrations(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `DROP TABLE schema_migrations; ALTER TABLE company DROP CONSTRAINT company_domain_key`); err != nil {
		t.Fatal(err)
	}
	if err := BaselineLegacy(ctx, conn); err == nil {
		t.Fatal("baseline accepted missing domain uniqueness")
	}
}

func TestConcurrentMigrationRunnersSerialize(t *testing.T) {
	conn := migrationTestDB(t)
	ctx := context.Background()
	other, err := pgx.ConnectConfig(ctx, conn.Config().Copy())
	if err != nil {
		t.Fatal("second scratch connection failed")
	}
	defer other.Close(ctx)
	results := make(chan error, 2)
	go func() { results <- ApplyMigrations(ctx, conn) }()
	go func() { results <- ApplyMigrations(ctx, other) }()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	ms, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if n != len(ms) {
		t.Fatalf("got %d ledger entries", n)
	}
}

func TestFailedMigrationRollsBackDDLAndHistory(t *testing.T) {
	conn := migrationTestDB(t)
	ctx := context.Background()
	if _, err := conn.Exec(ctx, ledgerSQL+`; CREATE FUNCTION reject_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test ledger failure'; END $$; CREATE TRIGGER reject_record BEFORE INSERT ON schema_migrations FOR EACH ROW EXECUTE FUNCTION reject_migration();`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, conn); err == nil {
		t.Fatal("expected transaction failure")
	}
	var company *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.company')::text`).Scan(&company); err != nil {
		t.Fatal(err)
	}
	if company != nil {
		t.Fatal("DDL committed without ledger record")
	}
}
