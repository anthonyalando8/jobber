# Phase 1: tracked migrations and preflight

The migration command is separate from the web server. It loads `.env` with
`godotenv`, uses `DATABASE_URL`, and never initializes Redis, AI, SMTP, or workers.
Do not print that variable. Keep backups and reports in the ignored `.local/`
directory: the archive contains personal contact/message data.

## Existing database

Stop all old Prospector processes before backup or baseline. Disabling automatic
sending alone does not stop discovery/research writes. The new Phase 1 binary has
a temporary code-level construction interlock that disables workers, AI providers,
and the unified send path regardless of legacy auto-send settings. Default rollout
mode is `v2_review`; `v2_auto` is refused during construction. Do not restart the
old binary against the new ledger.

```powershell
New-Item -ItemType Directory -Force .local | Out-Null
go run ./cmd/prospector-migrate -command inspect -out .local/preflight.json
go run ./cmd/prospector-migrate -command backup-verify -pg-bin 'C:\Program Files\PostgreSQL\18\bin' -out .local/backup.dump
go run ./cmd/prospector-migrate -command baseline -proof .local/backup.dump.verified.json
go run ./cmd/prospector-migrate -command check
```

Choose a new archive filename for each attempt. `backup-verify` runs `pg_dump`,
creates a random `prospector_verify_*` database on the same PostgreSQL server,
restores there using `pg_restore`, and compares table columns, constraints, indexes,
row counts, sorted content digests, and sequence values. It then drops only that
owned scratch database. This requires permission to create databases. It never
restores over the application database. A source change during verification or
before baseline invalidates the proof; repeat after stopping the writer.

The manifest contains hashes and schema metadata, not contact addresses or email
bodies. The archive hash is checked before baseline/apply. The manifest expires
after 24 hours. It is an operator-local verification artifact, not a signed
attestation or replacement for keeping the source stopped.

`baseline` accepts only the complete legacy structure through migration 0016,
including expected keys and foreign keys. It writes one `schema_migrations` row
with kind `baseline`, NULL checksum, and the observed inventory. It does **not**
claim every old SQL file ran, and it does not execute template updates. Missing
columns/types/constraints require an explicit reviewed repair; the command refuses
instead of trying to reconstruct edited templates. All original IDs and rows stay.

## Fresh database and later migrations

Create an empty database matching `DATABASE_URL` first, then:

```text
go run ./cmd/prospector-migrate -command apply
```

For an already-populated tracked database, stop the app, make a new verified backup,
and supply its manifest:

```text
go run ./cmd/prospector-migrate -command apply -proof .local/backup.dump.verified.json
```

The runner embeds SQL files, normalizes checkout line endings for checksums, takes
a PostgreSQL advisory lock, and commits each migration with its ledger entry in one
transaction. Migration 0001's original outer transaction is handled by the runner.
Unknown/future versions, missing versions, changed checksums, and detected legacy
structural drift are refused. Server startup checks schema before starting any
workers or HTTP handlers. New v2 schema checks must extend the manifest as each
later phase adds tables; Phase 1 does not pretend those tables already exist.

`make migrate PROOF=...` and `setup.bat migrate ...` invoke this same runner. Neither
replays raw SQL over an existing database. `make db` / `setup.bat db` now apply to an
already-created database; they no longer silently create a hard-coded database.
`setup.bat inspect` is read-only. No automatic database migration occurs at startup.

## Tests

```powershell
# Sandbox-local cache when the system Go cache is inaccessible:
$env:GOCACHE = Join-Path $env:TEMP 'prospector-go-cache'
go test ./...

# Opt-in integration tests: read connection settings but ONLY create/use random
# prospector_test_* databases. Never run migrations against the application DB.
$env:MIGRATION_TEST_ENV_FILE = Join-Path (Get-Location) '.env'
go test ./internal/store -run 'TestMigration|TestIncomplete|TestFailed|TestBaseline' -v -count=1
```

Alternatively set `MIGRATION_TEST_DATABASE_URL` to a test administrative connection.
Credentials are not logged. Existing store/Redis tests retain their own opt-in
environment requirements; a green default test run can include skipped integration
tests. The migration tests explicitly exercise fresh install, repeat apply, legacy
baseline preservation, incomplete-schema refusal, missing-key refusal, and atomic
rollback when the ledger write fails.

## Rollback and remaining work

Phase 1 adds only the ledger to the live database. Do not drop or replay existing
tables/templates. Keep the verified archive. Later phases will add profile,
opportunity, contact-plan, and review tables through new migrations. The temporary
construction interlock is removed only when all callers enforce v2 invariants;
automatic-send intents remain empty until reviewed offline evaluation is complete.
