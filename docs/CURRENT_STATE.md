# Prospector — current state (2026-08-19)

This is a status snapshot for review, not a spec. The build spec is
[`PROSPECTOR_SPEC.md`](PROSPECTOR_SPEC.md) — this doc says what actually exists in the
repo today, where it diverges from that spec, and what's still thin. Written to be shared
for feedback on what to build next.

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
