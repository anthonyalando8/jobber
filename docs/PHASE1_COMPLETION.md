# Phase 1 completion report

Date: 2026-09-12

Scope: migration/preflight foundation only. Profile, opportunity, intent matching,
composition, and semantic-review implementation have not started. No outreach copy
or candidate facts were changed.

## Implemented

- Separate `prospector-migrate` command for read-only inspection, backup/restore
  verification, explicit legacy baseline, tracked application, and schema checks.
- Embedded migrations with stable LF-normalized checksums, PostgreSQL advisory
  serialization, and atomic migration/history transactions.
- Refusal of incomplete/untracked populated schemas, history/checksum mismatch,
  future versions, missing required columns/types/keys, and stale backup proof.
- Honest legacy baseline: one observed-structure record through 0016, with no
  fabricated migration checksums or replay of historical copy migrations.
- Startup schema check before Redis/HTTP/workers; shared migration command in
  Makefile and Windows setup.
- Temporary construction interlock: the new binary cannot initialize an AI
  provider, run background workers, or send through the guarded path. Automatic
  sending is unavailable even if the old environment enables it. Rollout mode
  defaults to `v2_review`; construction refuses `v2_auto`.

## Live preflight and data impact

The configured application database had all expected legacy columns and keys but
no migration history. A first verified backup was rejected at baseline because
the company ID sequence changed afterward. Inspection found the old Prospector
instance listening on configured port 8081. It was stopped before a fresh backup;
the new application has not been started.

The final backup was restored into a randomly named, isolated scratch database.
Table columns, constraints, indexes, row counts, sorted data fingerprints and
sequence values matched the source. The owned restore database was removed.

The sole live schema change was creation of `schema_migrations` and insertion of
one `baseline` record with a NULL checksum and the observed inventory. No old
template SQL was replayed. Post-baseline comparison confirmed all original row
fingerprints and sequence values unchanged:

| Table | Rows preserved |
| --- | ---: |
| company | 417 |
| contact | 771 |
| draft | 107 |
| send_log | 33 |
| signal | 1,697 |
| page_cache | 1,120 |
| suppression | 1 |
| template | 10 |

Fourteen exact duplicate signal groups were observed and preserved. Deduplication
and new scoring are later-phase work. Original IDs, template edits, message bodies,
suppression and follow-up state are retained. No AI request or email was sent by
this implementation.

Local artifacts (ignored by Git; the dump contains private application data):

- `.local/phase1-quiescent.dump`: final source backup.
- `.local/phase1-quiescent.dump.verified.json`: archive hash and restore proof.
- `.local/phase1-after.json`: post-baseline inventory.

## Verification

- `go test ./... -count=1`, with a sandbox-writable temporary Go cache.
- Opt-in migration tests ran against randomly named `prospector_test_*` databases
  on the configured server, never against application tables. Tests cover fresh
  install, repeat apply, customized-template preservation, incomplete baseline
  refusal, missing uniqueness, atomic rollback, and concurrent migration runners.
- CLI backup/restore comparison passed on the actual source data.
- CLI `check` passed against the baselined live database.
- Post-baseline original-table/sequence comparison passed.
- `git diff --check` passed.

The original Go failure was a sandbox access problem, not missing Go standard
library files or a required dependency upgrade. Tests passed with a temporary
cache and also passed outside the sandbox using the normal cache. No global Go
configuration, toolchain installation, dependency version, or user `go.mod` edit
was changed. Optional pre-existing DB/Redis tests still require their own opt-in
environment; this report does not count skipped tests as executed integration tests.

## Files changed

| Files | Purpose |
| --- | --- |
| `internal/store/migrate.go`, `migrate_test.go` | Embedded runner, inventory/fingerprints, baseline, schema gate, unit/integration tests. |
| `cmd/prospector-migrate/main.go`, `main_test.go` | CLI, PostgreSQL tool invocation, private backup verification manifest, restore checks. |
| `internal/store/db.go`, `cmd/prospector/main.go` | Startup schema check and paused worker startup. |
| `internal/web/handler.go`, `handler_test.go` | Construction interlock for AI/sending; test refusal before database access. |
| `Makefile`, `setup.bat` | One migration path across platforms; explicit inspection and proof arguments. |
| `.env.example`, `.gitignore`, `README.md` | Review-only settings, ignored backup location, Phase 1 operational notice. |
| `docs/PHASE1_MIGRATIONS.md`, this report | Operator commands, data impact, verification and limits. |
| `docs/EMAIL_PIPELINE_IMPLEMENTATION_PLAN.md` | Record approved implementation constraints and phase links. |

Pre-existing modifications to `go.mod`, the untracked Docker Compose file, and the
investigation document were preserved.

## Deviations and decisions

1. No toolchain replacement or cache deletion was necessary. A writable temporary
   cache solves sandbox testing; unsandboxed verification established the normal
   toolchain works.
2. Added a temporary compile-time construction interlock rather than trusting an
   environment switch alone. V2 profile/plan/review checks do not exist yet, so
   leaving the old send path active would violate the rollout constraint. Later
   phases replace this with the completed v2 gates before enabling review-only use.
3. Database creation is explicit: setup commands apply to the database configured
   by `DATABASE_URL`, rather than creating a hard-coded `prospector` database. The
   backup verifier creates/removes only its own randomly named scratch database.
4. No legacy repair migration was needed for this database. Incomplete schemas are
   refused with structural findings; repairs remain explicit, evidence-driven work.

Phase 2 is next: approved-profile storage/UI and first-class opportunity ingestion,
using synthetic candidate fixtures until real facts are approved. The two additional
payments-as-backend-proof and partial-fit acceptance cases are recorded in the
approved plan; they will be implemented with the matching/composition phases.
