# Phase 2 completion report

Date: 2026-09-12  
Status: implemented and verified; awaiting review before Phase 3.

Phase 2 establishes candidate facts and advertised opportunities. It does not
implement fit classifications, contact plans, intent selection, capability/proof
selection, composition, semantic review, or v2 sending. No email copy, legacy
draft, or candidate achievement was rewritten.

## Implemented behavior

### Candidate profile

`/profile` displays saved revisions, approval status, active status, and exact
document content. A JSON editor saves a new immutable draft revision. A separate
confirmation approves and activates the displayed revision. Approval checks its
stored hash and the active revision observed by the page, preventing a stale tab
from silently replacing a newly activated profile.

The singleton head can reference only an approved revision. Database triggers
protect revision content, including previously saved drafts; editing always
creates another revision. Typed decoding rejects unknown fields, oversized
documents, duplicate item IDs and broken technology/project/capability/resource/
role references. Empty optional sections are valid. `ActiveProfile` returns only
the active approved document, never the latest unapproved draft.

The live database contains **one approved but inactive seed revision**. These are
its only facts:

- Identity: **Full-Stack Software Developer**.
- Primary strength: **Backend Engineering**.
- Secondary positioning: **Full-Stack Development**.
- Desired families: Backend Engineering, Full-Stack Development, Software Engineering.
- Title aliases for those families: Backend Engineer, Backend Developer, Full Stack
  Developer, Full-Stack Developer, Fullstack Developer, Software Engineer, Software Developer.

Technologies, capabilities, projects, responsibilities, outcomes, domains,
specializations, resources, engagement preferences and signature facts remain
empty. No revision was activated on the live database. Synthetic facts were used
only in isolated profile tests.

### Opportunity storage and ingestion

All five adapters call `seed.IngestOpportunity`, which delegates identity,
company resolution, persistence and revision creation to one PostgreSQL
transaction. Redis is not consulted for opportunity identity or company existence.
Manual CSV company import retains its existing separate behavior.

Source external ID is preferred; a canonical URL is the fallback. A later external
ID can enrich a URL-identified opportunity without changing its original key.
Different nonempty IDs remain distinct even with the same title/URL. Different
sources remain separate. Missing identity and ambiguous company associations are
stored in `ingestion_review`, not converted into guessed vacancies.

Existing company rows are reused without update. Conflicting domain/name matches
are review cases; there is no company merge operation. A short transaction-level
advisory lock serializes ingestion, with SQL uniqueness as the final identity
constraint. This intentionally favors a simple personal-tool implementation.

Original titles remain separate from normalized searchable titles. Missing titles
stay empty. No new canonical `hiring_backend` signals are created by job adapters.
Source content changes create immutable revisions; identical observations update
`last_seen_at`. Tracking parameters and the search query used to rediscover a job
do not create content revisions.

Descriptions retain paragraph structure, remove obvious script/navigation noise,
and are bounded to 64 KiB of valid UTF-8. HTTP responses are bounded to 2 MiB;
metadata is bounded to 64 KiB at the Go boundary. Truncated or failed-detail
descriptions are explicitly partial. Unknown source dates/conditions stay unknown.

`/opportunities` filters by source, company, title and availability and shows up to 200 recently changed
rows. Detail pages show original title, company, source URL, source conditions,
description quality/full text, observation timestamps and immutable snapshots. Dashboard navigation links
to the profile and opportunity pages. Selection and fit controls belong to Phase 3.

### Legacy backfill

The backfill recovers only an explicit `title at employer` prefix with a usable
source URL. It does not use the old role matcher or a candidate-derived title.
Recovered descriptions remain partial. Weak records remain unconverted with a
review reason. Each processed signal gets an auditable mapping/result in
`legacy_opportunity_backfill`; each batch commits the mapping and opportunity
together. The ledger makes reruns idempotent and interrupted runs resumable.

## Schema and live-data impact

Applied through the existing tracked migration runner:

| Migration | Additions |
| --- | --- |
| `0017_candidate_profile.sql` | Singleton profile head, immutable revision table, approval/head constraints and triggers; one inactive minimal seed revision. |
| `0018_opportunities.sql` | Opportunity/current revision, immutable source revisions, ingestion-review diagnostics and legacy backfill ledger; keys/indexes and historical foreign keys. |

The live migration history now has the original honest baseline through 0016 plus
two checksummed applied records. No legacy SQL or template-copy migration was
replayed. Startup also checks v2 columns, historical keys, enabled invariant
triggers and the external-ID uniqueness index. A v2 database with lost history
cannot be mislabeled as a legacy baseline.

Both migrations were tested separately on scratch schemas, then rehearsed with
backfill on a restored copy of the actual application backup before live apply.
Expected additions were reported before applying them. A second backup/restore
verification preceded live backfill.

| New live table | Rows |
| --- | ---: |
| `candidate_profile` | 1 |
| `candidate_profile_revision` | 1, approved but inactive |
| `opportunity` | 38 |
| `opportunity_revision` | 38 |
| `ingestion_review` | 354 |
| `legacy_opportunity_backfill` | 392: 38 converted, 354 review-required |

All 392 legacy hiring signals have a backfill result. No live source crawl was
performed; the 38 live opportunities all came from recoverable legacy evidence.
The 354 unresolved records need source research or explicit correction later;
they were not silently assigned engineering titles.

Original data preservation was checked against `.local/phase1-after.json`:

| Original table | Rows preserved |
| --- | ---: |
| company | 417 |
| contact | 771 |
| draft | 107 |
| send_log | 33 |
| signal | 1,697 |
| page_cache | 1,120 |
| suppression | 1 |
| template | 10 |

Every original table's sorted row fingerprint, count, columns, constraints and
indexes matched. Every original sequence value matched. Original IDs, sent/draft
bodies, customized templates, suppression and follow-up history remain unchanged.
The 14 previously observed duplicate signal groups remain preserved.

### Test connection incident and recovery

One early adapter test incorrectly used `pgx.Config.ConnString()` after changing
`Config.Database`. That method retained the original DSN. Its pool therefore
inserted a synthetic company into the application database (ID 7023), then failed
because the application database had not yet received the opportunity table.
No opportunity, candidate fact, AI request or email was created by that failed test.

The test-created row was removed only after checking its exact ID/name/domain,
original row count, absence of dependent signals/contacts/drafts/send logs and
sequence state. The company sequence was restored from 7023 to the verified
7022/true baseline. This corrective sequence write was a deviation from the
requested no-sequence-change execution; migrations and backfill themselves do
not reset sequences. All eight original fingerprints and every sequence were
verified against Phase 1 immediately after recovery and again after Phase 2.

The adapter test now explicitly builds a scratch DSN and asserts
`current_database()` before any fixture write. Store test pools and restore/
rehearsal connections also assert their intended scratch destinations. The incident
and recovery were reported during implementation; it is not counted as an
isolated test success.

## Verification

Passed:

- `go test ./... -count=1` with a writable temporary Go cache and opt-in isolated
  PostgreSQL tests enabled through `MIGRATION_TEST_ENV_FILE`.
- Targeted store/seed/web/profile/opportunity tests after subsequent implementation
  changes; adapter tests rerun after adding the simulated already-seen Redis case.
- Fresh/repeat migration, baseline refusal/recovery boundaries, atomic migration
  rollback, concurrent migration runners and per-migration preservation tests.
- Actual backup restore, migration/backfill rehearsal and original-data comparison.
- Live tracked schema check and complete backfill-ledger check.
- `git diff --check` passed.
- Read-only HTTP-handler smoke checks: `/profile`, `/opportunities`, and an actual
  opportunity detail returned 200, without starting a server or workers.
- Construction tests: provider stays nil even with automatic-send configuration;
  guarded send, follow-up preview/preparation/send and resend refuse before storage.
- Source HTML is escaped on opportunity inspection; profile activation requires
  explicit confirmation.

Regression coverage includes:

| Case | Evidence |
| --- | --- |
| Existing employer receives additional jobs; Marketing/Finance/Backend coexist | PostgreSQL multi-opportunity tests and all-adapter fixtures. |
| Repeated source identity refreshes observation without revision | Store timestamp assertion and repeated adapter revision assertions. |
| Changed description creates revision; snapshots cannot be edited | Store revision and direct mutation-refusal tests. |
| Same title with distinct external IDs; same vacancy across sources | Identity/enrichment and cross-source tests. |
| Missing title/identity; nontechnical listing in engineering search | Unknown-title, diagnostic and all-adapter fixtures. |
| Full-stack spelling normalization and long UTF-8 content | Pure opportunity normalization/boundary tests. |
| Failed detail fetch retains partial card; oversized response refused | Synthetic HTTP transport fixtures. |
| Redis says company already seen | Hook would return seen; all adapters still persist jobs and make zero Redis queries. |
| Concurrent import of one job | Four simultaneous ingestion transactions yield one identity. |
| Approval, new draft after approval, inactive unapproved skills, invalid references | Profile storage and pure validation tests. |
| Weak legacy evidence stays unresolved; recoverable evidence maps once | Batched/resumed backfill tests with original signal fingerprint comparison. |
| Disabled invariant trigger cannot pass startup | Schema-gate regression. |

Existing optional tests requiring their own database/Redis environment may still
skip; they are not reported as executed integrations. Race-detector tests were not
run: this environment has `CGO_ENABLED=0` and no `gcc` command. Concurrent database
tests did run. No toolchain, global Go configuration, or dependency upgrade was
introduced. Real job-board availability/markup was not probed; adapters were tested
with local synthetic HTTP fixtures.

## Files changed in Phase 2

| Files | Purpose |
| --- | --- |
| `internal/profile/profile.go`, `profile_test.go` | Candidate document and strict validation. |
| `internal/opportunity/opportunity.go`, `opportunity_test.go` | Source model, normalization, identity/hash and UTF-8 bounds. |
| `internal/store/profile.go`, `opportunity.go`, `opportunity_backfill.go`, `phase2_test.go` | Profile approval, transactional job storage, backfill, integration regressions. |
| `internal/store/migrations/0017_candidate_profile.sql`, `0018_opportunities.sql` | Additive schema. |
| `internal/store/schema_v2.go`, `migrate.go`, `migrate_test.go` | Version-aware startup checks and honest baseline recovery. |
| `cmd/prospector-migrate/main.go` | Restored-data rehearsal and audited backfill commands. |
| `internal/seed/ingest.go`, `ingest_test.go` | Shared ingestion/fetch/description boundary and source fixtures. |
| `internal/seed/adapters.go`, `remoteok.go`, `remotive.go`, `himalayas.go`, `weworkremotely.go` | All source adapters persist first-class opportunities. |
| Removed `internal/seed/usjobs.go`, `usjobs_test.go` | Obsolete title-filter and 300-byte signal contract; replaced by actual ingestion regressions. |
| `internal/web/profile.go`, `opportunities.go`, `phase2_test.go`; new profile/opportunity templates | Approval and source-inspection UI. |
| `internal/web/followups.go`, `resend.go`; `cmd/prospector/main.go`; dashboard navigation | Construction guards and route wiring. |
| `internal/draft/role.go`, `role_test.go`, `generate.go` | Remove invented missing-role fallback from dormant legacy code. |
| `README.md`, `docs/CURRENT_STATE.md`, implementation-plan status, this report | Current operations and completion evidence. |

Pre-existing Phase 1 work, `go.mod` changes and the untracked Docker Compose file
were preserved. No `.env` setting was rewritten or credential printed.

## Operations and limitations

The configured database is already at schema 18 and backfilled. Do not replay
legacy migrations or run the old sending binary. Keep the construction interlock
and `v2_review`; no automatic-send intents are enabled by this implementation.
The application was not left running.

For another existing installation, stop its application and use the tracked tools:

```powershell
go run ./cmd/prospector-migrate -command backup-verify -pg-bin 'C:\Program Files\PostgreSQL\18\bin' -out .local/pre-v2.dump -rehearse-phase2
go run ./cmd/prospector-migrate -command apply -proof .local/pre-v2.dump.verified.json
go run ./cmd/prospector-migrate -command backup-verify -pg-bin 'C:\Program Files\PostgreSQL\18\bin' -out .local/pre-backfill.dump
go run ./cmd/prospector-migrate -command backfill-opportunities -proof .local/pre-backfill.dump.verified.json -out .local/backfill.json
go run ./cmd/prospector-migrate -command check
```

An untracked legacy database first needs the explicit Phase 1 inspection/baseline
procedure. A failed/interrupted backfill resumes from its ledger after a fresh
verified backup; do not restore over subsequent user changes merely to rerun it.
Migration failures roll back the failing migration; already applied additive
migrations remain tracked. Rollback must retain the construction-aware binary or
use the verified backup under maintenance, never reactivate old sending code.

Private ignored artifacts include `.local/phase2-preflight.dump`, its verification
and rehearsal reports, `.local/phase2-before-backfill.dump` and its verification
report, `.local/phase2-backfill.json`, `.local/phase2-after-test-cleanup.json`, and
`.local/phase2-after.json`. Backup archives contain private application data.

Source limits are explicit: 200 entries per remote feed and ten cards per Kenyan
search page within the existing search scope. A feed exceeding the response limit
fails with an error. Kenyan detail selectors can miss changed markup; such records
retain partial card text. Himalayas uses a supplied full description when present,
otherwise marks its excerpt partial. Cross-source duplicates are retained.
Backfill deliberately does not reconstruct missing historical job descriptions.

The practical UI uses JSON for profile editing and snapshots, rather than a visual
profile builder. Fit/intent selection and diagnostic-resolution workflows remain
Phase 3 work. Versioned candidate/opportunity inputs are ready for later drafts;
v2 draft staleness enforcement cannot be claimed before the plan/draft pipeline
exists. Legacy generation and sending remain interlocked in the meantime.

## Explicit answers

- **Every job source uses shared opportunity ingestion:** yes, including both Kenyan sites.
- **An existing company can receive new jobs:** yes, without rewriting its company row.
- **Multiple opportunities per company coexist:** yes.
- **Missing title becomes “Backend Developer”:** no; both legacy fallback locations were removed.
- **Redis discovery state can discard a new valid opportunity:** no; this path never consults it.
- **Additional profile facts activated:** no; no live profile revision was activated at all.
- **Construction interlock intact:** yes; no AI request or email was sent, and no workers were re-enabled.

Phase 3 has not started. The payments-as-backend-proof and partial-fit composition/
matching fixtures remain recorded requirements for the later phases that own them.
