# Prospector — current state

**Read this first.** It is the fastest way to understand the repository without reading every
phase report.

| | |
| --- | --- |
| Status | **ACTIVE — MANUAL MODE** |
| Tracked schema | **25** (`prospector-migrate -command check`) |
| Phases complete | 1–7, all approved. In production use. |
| Rollout mode | **`v2_manual_send`** — the full pipeline, with every send requiring explicit human approval of that exact content. |
| Selective automation | **Implemented, off.** `AUTO_SEND_INTENTS` is empty and the mode is not `v2_auto`, so no automation loop runs. |
| Auto-send intents | **Empty.** Enabling one is a deliberate configuration change, never a side effect. |
| Automation cap | 1 automatic send per day if ever enabled (`AUTO_SEND_DAILY_CAP`, refuses above 5). |
| Active candidate profile | **Revision 3**, approved and active, hash `7138cbad…`. |
| Authoritative assessments | 273 stored: **6 strong, 94 possible, 173 unrelated**. |
| Opportunities stored | 273 (262 complete descriptions, 11 partial) |
| Companies | 596 · contacts 771 · suppression rows 1 |
| v2 compositions / approvals / send attempts / threads | 0 / 0 / 0 / 0 |
| Legacy unsent drafts | 62 — readable, permanently unsendable |
| Legacy sent threads | 33 — immutable; 33 still unclassified |
| Legacy templates | 10 — read-only history, inert |
| Telemetry contacts marked unusable | 19 (Sentry ingest keys; marked, never deleted) |
| Emails sent by v2 | **None.** |

## How the pipeline works now

```
discovery ──▶ opportunity (+ revisions)
                  │
     approved candidate profile ──▶ deterministic match ──▶ classification + explicit reasons
                  │                                             │
     company evidence ──▶ relevance ─────────────────────────────┤
                                                                ▼
                                          intent + recipient + bounded proof = contact plan
                                                                ▼
                                   composition snapshot ──▶ AI composer ──▶ deterministic validators
                                                                ▼
                                                    independent semantic reviewer
                                                                ▼
                                              human approval of that exact content
                                                                ▼
                                    dispatch.Service.Send — the one path to a mail server
```

Selective automation sits **above** that chain, never beside it. When configured, it asks the
automation policy about a draft that has already been through every stage above; if the policy
says yes it creates the same approval record a person would, re-checks everything immediately
before transmission, and then uses the same `dispatch.Service.Send`. It can skip nothing.

Each arrow is a gate, and every gate explains itself with a code and a sentence rather than a
boolean. Nothing advances on its own: composition is requested, approval is given by a person,
and sending is a separate deliberate act.

**Candidate profile.** A singleton head plus immutable JSONB revisions. Only an *active,
approved* revision is authoritative. Activation is manual and is never performed by a migration,
a startup path or a test.

**Contact plans.** One plan records company, intent, the pinned opportunity revision where
applicable, target families, the frozen recipient, and the bounded set of approved capabilities
and proof a composer may use — plus the claims it may never make. Selecting a plan authorizes
generation, never sending.

**The three intents** are `specific_job_application`, `speculative_job_enquiry` and
`service_outreach`. Intent is chosen explicitly, frozen into the composition, into the approval,
into the idempotency key and into the conversation thread. A follow-up cannot change it.

**Review is two independent layers.** Deterministic validators check what can be checked by
rule; a separate semantic reviewer reads the finished email against the same sources. Findings
are the union of both — a clean semantic review never erases a deterministic finding.

**Sending** runs through `dispatch.Service.Send` and nothing else. It reserves a durable attempt
before contacting a provider, transmits exactly the approved bytes once, and records what
happened permanently. See [Phase 5 completion](PHASE5_COMPLETION.md) for the guarantees.

## Legacy data

The pre-v2 pipeline's output is preserved exactly and is inert.

- **Unsent legacy drafts** (`/legacy/drafts`) carry no profile, opportunity, recipient or review
  context. They are readable and cannot be approved or sent — a draft belongs to v2 if and only
  if it has a `draft_composition` row, which is what the send path reads.
- **Sent legacy messages** keep their exact subject and body. Intent, profile revision, evidence
  and review result were never recorded and are reported as unknown, never reconstructed.
- **Legacy threads** (`/legacy/threads`) acquire an intent only when a person states one, with
  the basis they used. `unknown` is a valid answer and means no v2 follow-up is offered.
- **Legacy templates** (`/templates`) are read-only history. v2 text comes from versioned prompts
  in `internal/composition/prompts`.
- **Legacy routes** — send, generate, batch draft, resend, follow-up send, template edit — return
  **410 Gone** with an explanation of the v2 route that replaces them. `/companies/{id}/apply-mode`
  redirects to the contact plan and writes nothing.

`prospector-migrate -command legacy-inventory` prints the full census plus content digests over
historical outbound text, so a transition can be proven not to have altered it.

## Where an operator works

`/` dashboard (v2 panel first) · `/profile` · `/opportunities` · `/companies/{id}/plan` ·
`/compositions/{id}` (the review and approval screen) · `/threads` · `/legacy/drafts` ·
`/legacy/threads` · `/templates`.

## Selective automation

Only **specific job applications** can ever be automated, and that is a property of the policy
rather than of configuration: speculative enquiries and service outreach are refused at
configuration time, because their correctness depends on company-relevance evidence the stored
data cannot support. **All follow-ups stay human-reviewed.**

Automation requires, per message: an active approved profile, a `strong_match` with a clear
engineering family and a complete description, no unresolved mandatory requirement or preference
conflict, a recipient that is a real human mailbox with a recruiting or engineering function (or
an employer-published application address), a current non-stale composition, clean deterministic
validation, a complete and clean semantic review with no findings at all, no hand editing, no
manual approval, no suppression, no open conversation with that company, no unresolved send
attempt anywhere, AI budget available, and the automation cap not reached. Every decision —
eligible or not — is persisted in `automation_decision` with its reasons and the policy version.

`AUTO_SEND_INTENTS=` (empty) means nothing is sent automatically. `all` and wildcards are
refused. An unknown or non-automatable value stops startup.

## Known operational limitations

1. No approved profile is active, so nothing can become authoritative. This is deliberate and is
   the single largest gate.
2. Bounce and reply detection is manual — no IMAP, no webhook. SMTP-time rejections are caught
   during the conversation; an asynchronous bounce suppresses an address only once recorded.
3. No follow-up scheduling exists. Follow-up policy is enforced, but nothing decides when one is due.
4. One transport (SMTP). The interface is provider-agnostic but unproven against a second provider.
5. No real SMTP smoke test has been run; the conversation has only met a scripted in-process server.
6. An attempt stuck in `sending` or `uncertain` holds its idempotency key and has no reconciliation
   tooling. It fails safe — it blocks, and now also halts automation entirely. The dashboard
   surfaces the count; see [the reconciliation design](RECONCILIATION_DESIGN.md) for the proposed
   fix and the operator procedure in the meantime.
7. No real strong-match draft has been composed under the real profile, so composer and reviewer
   behaviour on real strong matches is unmeasured. That measurement is a precondition for
   enabling any intent.

## Production activation

See [PRODUCTION_ACTIVATION.md](PRODUCTION_ACTIVATION.md) for what was activated and verified.
The system is in real use for manual sending: discovery and research run in the background,
composition is requested explicitly, and no email leaves without a person approving that exact
content. Automatic sending is implemented and switched off.

## Activation readiness

[ACTIVATION_READINESS.md](ACTIVATION_READINESS.md) is the evidence-based assessment of whether the
system is ready for real use. In short: manual use is ready pending profile activation; automation
is `NOT_READY`; and the real bottleneck is contact data — 180 of 212 internationally-sourced
companies carry a guessed `.co.ke` domain, and the job adapters discard the employer and
application URLs the boards publish. The SMTP smoke test passed (accepted by Gmail over TLS 1.3).

## Enabling automation

Automation stays off until real manual use provides the evidence for turning it on. What remains:

1. Compose and inspect a small real strong-match sample, and measure wrong-role rate,
   unsupported claims, payment leakage, reviewer false positives and manual-edit frequency.
2. Use manual sending for real, long enough to trust the output.
3. Only then set `EMAIL_PIPELINE_MODE=v2_auto` and
   `AUTO_SEND_INTENTS=specific_job_application`, leaving `AUTO_SEND_DAILY_CAP` at 1.

Each step is a deliberate decision. None happens as a side effect of deployment.

## Phase reports

[Phase 1 operations](PHASE1_MIGRATIONS.md) · [Phase 1](PHASE1_COMPLETION.md) ·
[Phase 2](PHASE2_COMPLETION.md) · [Phase 3](PHASE3_COMPLETION.md) ·
[Phase 3.5 calibration](PHASE3_5_CALIBRATION.md) · [Phase 4](PHASE4_COMPLETION.md) ·
[Phase 5](PHASE5_COMPLETION.md) · [Phase 6](PHASE6_COMPLETION.md) ·
[Phase 7](PHASE7_COMPLETION.md) · [Production activation](PRODUCTION_ACTIVATION.md) ·
[Activation readiness](ACTIVATION_READINESS.md) ·
[Reconciliation design](RECONCILIATION_DESIGN.md) ·
[Approved design](EMAIL_PIPELINE_IMPLEMENTATION_PLAN.md) ·
[Original investigation](EMAIL_GENERATION_FINDINGS.md)

---

# Historical snapshot (2026-08-19)

The text below describes the original pre-v2 pipeline. It is retained as history and no longer
describes how the system works.

## What it is

A one-person tool: find Kenyan companies likely to need backend/payments contract work,
gather real evidence about each one, draft a first-contact message per company for human
review, and send approved ones one at a time from a personal Gmail account. Single Go
binary, Postgres + Redis, server-rendered HTML, no login, no multi-user.

The five-stage pipeline (seed → enrich → score → draft → review/send) is implemented and
runnable end to end. The gaps below are mostly about *reliability and completeness* of
each stage, not missing stages.

## Pipeline, stage by stage

### Seed

Two working sources, both scraped synchronously when you click a button on the dashboard:

- **Job boards** — brightermonday.co.ke (backend/golang/django searches) and
  myjobmag.co.ke, via CSS selectors checked against live markup. A hiring-ad company is
  the strongest signal this project has, so this is the source worth trusting most.
- **Manual CSV paste** — `name,domain,segment,notes` rows, tolerant of a header row and
  malformed trailing lines.

clutch.co, goodfirms.co, and fuzu.com were tried and dropped — all three return 403 to a
plain HTTP client (Cloudflare/bot detection), not fixable short of a headless browser.
The spec also names `google` and `playstore` seed adapters; neither exists yet.

**Weak spot:** the job-board seeder doesn't discover a real company domain — it guesses
one by slugging the display name (`"Acme Ltd"` → `acme-ltd.com`). That's frequently wrong,
which cascades into enrichment fetching the wrong (or a nonexistent) site.

### Enrich

Fully implemented, synchronous (a batch button or a per-company button, no background
worker). For each company it fetches up to 7 pages (home, /about, /contact, /careers,
/pricing, /team, /portfolio) and extracts: emails (ranked named > role-based > info@),
Kenyan phone numbers, a superset of the spec's tech-stack keywords, checkout-language
presence, paybill/till mentions, and a crude freshness signal from literal year strings in
the page text.

**Gaps against spec:**
- No robots.txt handling at all.
- Per-host rate limiting is decorative — a `crawl:host:{host}` Redis key gets set but is
  never checked; the real throttle is a flat sleep before every fetch, not a per-host one.
- Page cache is an in-process map, not the `page_cache` Postgres table the schema defines
  — that table exists and is empty; nothing writes to it. Cache doesn't survive a restart.

A 6-hour cooldown key prevents repeatedly re-crawling a site that yielded nothing on the
last attempt — a practical addition not in the spec.

### Score

Implemented, runs automatically right after a successful enrich. The actual rule set is
quite different from the spec's point values — it uses a per-segment base score
(hiring=10, agency=8, incubator=6, sme=4) plus per-signal additions, capped at 100. None
of the spec's `+2 named contact`, `+2 agency-with-no-backend-mention`, `+1 phone found`,
`−3 no contact` rules exist in code.

**Gap:** a company with contacts but zero signals is skipped by the scorer entirely and
can sit at `score=0, segment=NULL` indefinitely — nothing surfaces this as stuck.

Segment classification also has dead branches: it checks for `source=clutch`/`google` and
signal kinds like `incubator_mention`/`sme_indicator` that no seeder or enricher actually
ever produces. Effectively, segment mostly falls through to a same default (`agency`) for
anything the job-board/manual seeders didn't already tag explicitly.

### Draft

The best-matched-to-spec part of the system. The model (Claude, with Gemini as a full
fallback provider — multi-key pooled, not in the original spec) is only allowed to return
two short fields, an observation and a hypothesis, both required to cite real signal ids;
everything else is literal template text pulled from a `template` Postgres table (one row
per segment × channel — an operationalized version of `OUTREACH_TEMPLATES.md` the spec
only described in prose). Rejection into `needs_manual` is thorough: INSUFFICIENT
response, empty citations, a citation that doesn't exist on the company, word-count
overruns, or a number in the observation that isn't verbatim in the signals.

Batches the top 40 scored `enriched` companies per run (spec's own cost-guard default),
paced between calls, and never re-drafts a company that already has a live draft.

### Review & send

Two separate UI paths exist, which is worth resolving:

- **`/review`** — the spec's intended screen. One pending draft at a time queued by
  company score, editable body, Save & Send / Save / Skip / Needs-manual, keyboard
  shortcuts. This is the only place a draft actually gets sent from the queue flow.
- **`/companies/{id}/draft`** — a second, separate single-draft screen with a canned
  "objection handling" sidebar. Its Approve button only flips `state=approved` — nothing
  downstream ever reads that state, so this path is currently a dead end. (This is also
  the template that had the `{{` parse bug fixed earlier today.)

Sending itself is a single guarded code path (`executeSend`, shared by both `/review/send`
and the company-page send button) — matches the spec's "one guarded path, no send-all"
requirement. It checks, in order: no leftover placeholder brackets, a real email contact
exists, sender/domain isn't suppressed, no send to the same domain in the last 30 days
(a real SQL check, not just hidden in the UI), minimum interval since last send, and the
daily cap — then sends plain-text SMTP and logs `send_log` with both follow-up dates.

**Real gap:** `sendSMTP` never sets a `Reply-To` header, and `From:` is the raw SMTP user,
not a formatted `"Name <email>"`. `FROM_NAME`, `FROM_EMAIL`, and `REPLY_TO` are all defined
in `.env.example` and the spec but never read anywhere in code. The spec's acceptance
check #2 ("correct reply-to") would fail today. There's also no day-over-day send ramp
(10/15/20) — only a flat daily cap.

## Screens that exist

Dashboard (pipeline buttons, stats, recent-activity feed — the activity log itself is a
Redis-backed addition not in the spec), companies list (status filter only; a segment
filter and search box are in the markup but not wired up), company detail (contacts,
signals, notes, enrich/draft/skip buttons, an outcome form, and a suppress form — the
suppress route exists but the spec never actually defines it), the two draft-review
screens above, and follow-ups (due-today list with mailto links).

## Data model

Matches the spec table-for-table (`company`, `contact`, `signal`, `page_cache`, `draft`,
`send_log`, `suppression`) plus one addition — `template` — that operationalizes the
outreach copy. `page_cache` is the one table that's defined but structurally unused.

## Redis

Of the spec's five documented keys, three work as designed (`send:day:*`,
`send:last_at`, `seen:domain:*`); `crawl:host:*` is written but not enforced; and
`queue:enrich`/`queue:draft` are dead code — `queue.go` has working `Enqueue`/`Dequeue`
helpers that nothing in the app calls. There's no background worker anywhere in the
binary; every pipeline stage is triggered synchronously by an HTTP POST, which is why the
admin routes carry a 10-minute timeout instead of the normal 30s.

Two keys not in the spec: an enrich-cooldown key and the activity-log list.

## No tests

Zero `_test.go` files. Consistent with the spec's explicit non-goal, but worth naming: the
spec's three acceptance checks (§10) are manual-only, nothing automated verifies them.

## Open questions for review

Things worth a second opinion on, roughly in order of how much they affect whether this
tool is trustworthy to run unattended-ish:

1. **Domain guessing in the job-board seeder** is probably the highest-leverage fix — a
   wrong domain poisons everything downstream (enrich fetches garbage, score never fires,
   draft never happens). Worth a real domain-lookup step (e.g. a search-engine query or a
   `company-name → website` heuristic with verification) before falling back to slugging.
2. **Reply-To/From-name wiring** is a small, concrete fix that's currently silently broken
   relative to both the spec and `.env.example`'s stated intent.
3. **Two competing draft-review UIs** — worth deciding whether `/companies/{id}/draft` +
   "Approve" should be wired into the send path, merged into `/review`, or removed.
4. **Per-host crawl throttling** is currently cosmetic (key written, never checked) — low
   risk today at this volume, but worth fixing before scaling up seeding.
5. **Scorer's dead branches** (segment/signal kinds nothing ever produces, companies with
   contacts-but-no-signals going unscored) suggest the scoring rules could use a pass once
   there's more real signal data to tune against.
6. Whether `google`/`playstore` seed adapters (in spec, not built) are still worth building,
   or whether job-board + manual is enough given clutch/goodfirms/fuzu turned out to be
   dead ends.

Feedback and additional feature suggestions welcome — this doc reflects what's actually
in the repo as of today, not what's aspirational.
