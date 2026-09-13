# Prospector

A personal outreach and job-opportunity research tool built with Go, PostgreSQL,
Redis, chi and server-rendered HTML. Its candidate positioning is **Full-Stack
Software Developer**, with **Backend Engineering** as the primary strength.

## Current status: Phase 3

Candidate profiles, first-class opportunities, deterministic fit matching, company
relevance, recipient selection and explicit contact plans are implemented. The
application requires **tracked database schema 21**.

- `/profile`: create profile drafts, inspect revision history, explicitly approve
  and activate verified facts.
- `/opportunities`, `/opportunities/{id}`: inspect advertised jobs and their fit
  assessment — classification, matched requirements with source excerpts, unmet and
  unknown requirements, and the reasons behind both.
- `/companies/{id}/plan`: the contact-plan workspace. Choose which vacancy is being
  pursued, why the company is relevant, which of the three intents applies, who is
  being contacted, and exactly which approved facts a future email may use.
- Existing company, contact, signal, draft, sent-message, suppression and follow-up
  history is preserved.
- `/compositions/{id}`: the complete email, both validation layers, the approval
  state and the exact send eligibility. A send button appears only when every
  preflight check passes; when one fails, the reasons appear instead.
- `/threads`, `/threads/{id}`: conversations opened by an accepted send, their
  frozen intent, and the delivery facts recorded against them.
- Existing company scores and legacy segments take no part in any v2 fit or intent
  decision.
- **Background workers and automatic sending remain disabled.** No environment
  setting enables them. The legacy send, follow-up and resend routes are
  permanently closed and return 410 Gone.

Sending is possible only as a deliberate act: a reviewed draft must be explicitly
approved by a person, for exactly the content shown, and is then transmitted once
through a single guarded path. Reviewed is not approved, and approved is not sent.

See the [Phase 5 completion report](docs/PHASE5_COMPLETION.md) for the sending
architecture, its guarantees and its limits, the
[Phase 4 completion report](docs/PHASE4_COMPLETION.md) for composition and review,
and the [Phase 3 completion report](docs/PHASE3_COMPLETION.md) for matching, intent
and contact plans.
The configured database is migrated to schema 25. It holds three candidate-profile
revisions and **none of them is active**, so matching correctly answers
`profile_required` and no contact plan, composition or send can yet become
authoritative. Activation is a deliberate act on the profile page; no migration,
startup path or test performs it.

## Setup

### Requirements

- Go 1.25 or later, matching `go.mod`.
- PostgreSQL and an existing database named by `DATABASE_URL`.
- Redis; application startup checks its connection even while workers are paused.
- `pg_dump` and `pg_restore` for existing-database backup verification. Phase 2 was
  verified on PostgreSQL 18.

AI keys and SMTP credentials are not required for the current profile/opportunity
workflow. Keep existing credentials private; configuration fields are documented
in [.env.example](.env.example).

### New installation

From the repository root, create `.env` only if it does not already exist. In
PowerShell:

```powershell
if (-not (Test-Path .env)) { Copy-Item .env.example .env }
go mod download
```

Configure the database and Redis connections. Use your own connection values:

```dotenv
DATABASE_URL=postgres://user:password@localhost:5432/prospector?sslmode=disable
REDIS_URL=redis://localhost:6379/0
HTTP_ADDR=:8080
EMAIL_PIPELINE_MODE=v2_review
AUTO_SEND_ENABLED=false
AUTO_SEND_INTENTS=
```

Create the PostgreSQL database first using your database administration tool.
For a **new, empty database**, apply the embedded migrations and start the app:

```powershell
go run ./cmd/prospector-migrate -command apply
go run ./cmd/prospector
```

The HTTP address comes from `HTTP_ADDR`, defaulting to `:8080`. Run from the
repository root so the application can locate its HTML templates.

`make run` and `setup.bat run` also start the application. `make db` and
`setup.bat db` apply migrations; **they do not create the database**.

### Existing database

Do not replay SQL files manually. The migration command tracks applied versions
and checksums, and startup refuses an untracked, incompatible or outdated schema.

```powershell
go run ./cmd/prospector-migrate -command inspect
go run ./cmd/prospector-migrate -command check
```

Stop the application before migration. Existing-database changes require a fresh
backup that has been restored and verified in an isolated scratch database.
Untracked legacy databases also need an explicit structural baseline; edited
legacy template migrations must not be replayed.

Follow [Phase 1 migration operations](docs/PHASE1_MIGRATIONS.md) and the
[Phase 2 operations and limitations](docs/PHASE2_COMPLETION.md#operations-and-limitations)
for the appropriate baseline, backup, migration and backfill sequence. The
migration CLI supports:

| Command | Purpose |
| --- | --- |
| `inspect` | Read-only schema/data inventory; optional `-out` report. |
| `check` | Verify schema version and required invariants. |
| `backup-verify` | Create a backup and prove restoration in an owned scratch database. |
| `baseline` | Explicitly record a verified legacy baseline; requires `-proof`. |
| `apply` | Apply pending tracked migrations; populated databases require `-proof`. |
| `backfill-opportunities` | Batched, resumable legacy hiring-signal conversion; requires `-proof`. |

`backup-verify -rehearse-phase2` rehearses every pending migration on the restored
copy and proves each original table keeps its row count and its original column
values, reporting any columns the migration adds.

`backup-verify -rehearse-phase2` also rehearses migrations and backfill on the
restored scratch copy. Use `-pg-bin` if PostgreSQL tools are not on `PATH`.
Archives and local reports belong in the ignored `.local/` directory. Backups
contain private application data. Do not roll back to an old sending binary that
does not understand the construction interlock.

## Current workflow

### 1. Approve candidate facts

Open `/profile`, edit the JSON document and save a new draft revision. Review the
saved content, then explicitly approve and activate that revision. Saving alone
does not change the active profile. Approved document content is immutable;
subsequent edits create another revision.

Only the high-level identity and desired engineering role families are seeded.
Technologies, project responsibilities, outcomes, resources, availability and
other optional facts remain empty until supplied and approved. Old email copy is
not candidate evidence. Profile activation does not enable generation or sending.

### 2. Import and inspect opportunities

The five job adapters share one PostgreSQL ingestion path:

| Adapter | Source scope |
| --- | --- |
| `jobboard` | BrighterMonday Kenya and MyJobMag Kenya, within configured engineering searches. |
| `remoteok` | Bounded RemoteOK feed. |
| `weworkremotely` | Existing backend, full-stack, DevOps and programming category feeds. |
| `remotive` | Software-development feed. |
| `himalayas` | Bounded Himalayas feed. |

Manual CSV company import is also available. Clutch, GoodFirms, Fuzu, Google SERP
and Play Store adapters are not registered sources.

Discovery is manually triggered while workers are paused. Job adapters return the
number of newly created **opportunities**, which may belong to existing companies.
Manual CSV import continues to create companies separately.

PostgreSQL owns opportunity identity: source external ID first, canonical URL
second. Missing identity or ambiguous employer associations become ingestion-review
cases. Redis discovery state cannot suppress a new job for an existing company.
Different source IDs and different sources remain distinct.

Original advertised titles are preserved, including nontechnical titles. Missing
titles stay unknown. Phase 2 stores what was advertised; it does not decide whether
the candidate should apply. Meaningful source changes create immutable revisions;
identical observations refresh timestamps without creating another revision.

Descriptions retain useful paragraph structure, with a 64 KiB UTF-8 limit and a
2 MiB HTTP response limit. Failed detail fetches retain partial listings. Inspect
source quality, timestamps, conditions and revision history on the opportunity
page. Weak legacy records remain unresolved instead of receiving guessed titles.

### 3. Assess fit, relevance and intent

Nothing here writes or sends a message. This stage decides what a future email would
be allowed to say and why the recipient is being contacted.

**Matching requires an active approved profile.** Without one, matching answers
`profile_required`, stores nothing, and no contact plan can be created. Discovery,
opportunity storage, inspection and company research all continue regardless. No
candidate skill, technology, capability or project is ever inferred from legacy
templates or code.

Fit is deterministic and explainable, matcher version `fit-v1`. The advertised title
is the first gate: a role designation naming a non-engineering business function or a
non-software discipline is `unrelated`, and engineering vocabulary elsewhere in the
description cannot change that. Requirements are extracted conservatively from the
stored revision, each keeping its verbatim source line, and resolved against approved
technologies, capabilities and projects. Classifications are `strong_match`,
`possible_match` and `unrelated`; the numeric score is a ranking aid that decides
nothing.

**An `unrelated` role can never authorize a specific job application.** No score,
manual override, company segment, checkout signal or legacy application status changes
this — the Go layer, the service layer and a database CHECK constraint each enforce it
independently. An unrelated listing may still be recorded as the *provenance* of a
speculative enquiry, but never as the role being sought.

Company relevance is separate from job fit and answers only whether there is a truthful
engineering reason to contact a company. It counts distinct fact groups, so the same
fact observed on three pages counts once. Checkout presence, payment acceptance and
generic technology detection contribute nothing and can never evidence a payment
problem.

A contact plan records the company, the intent, the pinned vacancy revision where
applicable, the target engineering families, the frozen recipient and the bounded set
of approved capabilities and proof a future composer may use — together with the claims
it may never make. Selecting a plan authorizes future draft generation only; it does
not authorize sending.

### 4. Retain research and history

Company/contact management and manual research remain available. Legacy research
signals can be keyword-derived detections; they are not necessarily verbatim
source quotations or evidence of a recipient problem. Existing draft/review and
template pages remain legacy tools pending later phases.

Redis supports existing research throttling/cache, activity events and legacy
budget/counter infrastructure. Its job-list abstraction is not the active v2
workflow; Phase 2 opportunity persistence does not depend on Redis deduplication.

## Main routes

| Route | Current purpose |
| --- | --- |
| `GET /` | Dashboard and navigation. |
| `GET /profile` | Profile editor, saved revisions and approval status. |
| `POST /profile/drafts` | Save a new profile draft. |
| `POST /profile/activate` | Explicitly approve and activate the displayed revision. |
| `GET /opportunities` | Opportunity list and filters. |
| `GET /opportunities/{id}` | Source facts plus fit classification, matched and unmet requirements, and reasons. |
| `POST /opportunities/{id}/assess` | Recompute this vacancy's fit against the active approved profile. |
| `GET /companies/{id}/plan` | Contact-plan workspace: fit, relevance, intent, recipient and allowed proof. |
| `POST /companies/{id}/assess` | Recompute every vacancy and the company relevance. |
| `POST /companies/{id}/plans` | Record an explicit contact plan. |
| `POST /plans/{id}/recipient`, `POST /plans/{id}/status` | Change a plan's frozen recipient (new revision) or pause/close it. |
| `GET /companies`, `GET /companies/{id}` | Company/contact/research views. |
| `GET /drafts`, `GET /review`, `GET /templates` | Existing legacy draft and template tools. |
| `GET /sent` | Existing sent history. |
| `POST /admin/seed/{source}` | Manually run a registered source. |
| `POST /admin/seed-us-jobs` | Manually run the remote job adapters. |
| `POST /admin/enrich`, `POST /companies/{id}/enrich` | Manual company research. |

Generation and send routes still exist for compatibility but cannot generate or
send during construction. `/followups` also refuses preparation. The full route
registration is in [cmd/prospector/main.go](cmd/prospector/main.go).

## Project structure

```text
cmd/prospector/          HTTP application; worker startup currently interlocked
cmd/prospector-migrate/  Inspection, backup verification, migrations and backfill
internal/profile/       Typed candidate facts and reference validation
internal/opportunity/   Source opportunity model, identity, requirements and fit matching
internal/evidence/      Research facts, grouping, and what a detection cannot establish
internal/relevance/     Capped company relevance and contact-function classification
internal/plan/          Intents, recipient ranking and bounded capability/proof selection
internal/assess/        Phase 3 orchestration over store plus the pure rule packages
internal/explain/       The shared reason type behind every Phase 3 decision
internal/store/         PostgreSQL queries, revisions, migrations and constraints
internal/seed/          Source adapters and shared opportunity ingestion
internal/enrich/        Existing website research and contact/signal extraction
internal/score/         Legacy company scoring; v2 fit matching not implemented
internal/draft/         Legacy providers/templates; generation interlocked
internal/sendpolicy/    Existing send-policy rules
internal/aibudget/      Existing AI budget infrastructure
internal/web/           HTTP handlers, guarded delivery and HTML templates
internal/worker/        Existing scheduler; loops disabled during construction
internal/queue/         Redis connection and queue helpers
```

The tool remains a single-user application without authentication or a frontend
framework. No generalized CRM, separate AI service or distributed workflow engine
is introduced by the v2 design.

## Testing

Run the Go suite:

```powershell
go test ./... -count=1
```

`make test` and `setup.bat unit-test` also run Go tests. `setup.bat test` only checks
Go and builds the application; it is not the full test suite.

To opt into isolated migration/profile/opportunity/adapter PostgreSQL tests:

```powershell
$env:MIGRATION_TEST_ENV_FILE = Join-Path (Get-Location) '.env'
go test ./... -count=1
```

These tests use the configured server to create randomly named scratch databases;
the connection user needs permission to create/drop them. Fixture connections
assert their scratch database destination before writes. Other legacy DB/Redis
tests have separate opt-in requirements and may skip.

If the sandbox cannot write the normal Go build cache, set a writable cache for
that PowerShell session:

```powershell
$env:GOCACHE = Join-Path $env:TEMP 'prospector-go-cache'
```

Source tests use synthetic HTTP fixtures. Profile tests use synthetic candidate
facts; pipeline construction tests verify that AI and delivery remain disabled.
See the [completion report](docs/PHASE2_COMPLETION.md#verification) for executed
checks, preservation evidence and race-detector limitations.

## Design and implementation history

- [Email generation findings](docs/EMAIL_GENERATION_FINDINGS.md)
- [Approved implementation design](docs/EMAIL_PIPELINE_IMPLEMENTATION_PLAN.md)
- [Phase 1 completion](docs/PHASE1_COMPLETION.md)
- [Phase 2 completion](docs/PHASE2_COMPLETION.md)
- [Phase 3 completion](docs/PHASE3_COMPLETION.md)

- [Phase 4 completion](docs/PHASE4_COMPLETION.md)
- [Phase 5 completion](docs/PHASE5_COMPLETION.md)
- [Phase 6 completion](docs/PHASE6_COMPLETION.md)
- [Phase 7 completion](docs/PHASE7_COMPLETION.md)
- [Production activation](docs/PRODUCTION_ACTIVATION.md)

Start with [current state](docs/CURRENT_STATE.md): it states the schema version, the rollout
mode, the active profile state and the known limitations on one page.

**Status: ACTIVE — MANUAL MODE.** Candidate profile revision 3 is approved and active, 273
opportunities are assessed, and the system runs in **`v2_manual_send`**: a reviewed draft can be
sent after explicit human approval of that exact content.

Selective automation is implemented and **off**: only specific job applications can ever be
automated, `AUTO_SEND_INTENTS` is empty, and enabling an intent is a deliberate configuration
change. See [production activation](docs/PRODUCTION_ACTIVATION.md). Discovery, matching, contact plans, composition, validation
and review all work; sending requires a person to approve one exact email, and automatic sending
is disabled with no setting that enables it. Everything the pre-v2 pipeline produced is preserved
and inert: legacy drafts cannot be sent, legacy templates cannot generate anything, and legacy
sent messages keep their original text with unknown metadata reported as unknown.

Phase 7 — selective automation — begins only after Phase 6 review. The system can now explain deterministically
which opportunity is being pursued, how the approved profile does and does not fit it,
why the company is relevant, what the intent is and who is being contacted; express
that plan as a complete email that two independent layers have checked; and transmit
it exactly once, after a person approves that exact text, with every attempt
permanently recorded. What it still cannot do, by design, is decide on its own that an
email should go out.
