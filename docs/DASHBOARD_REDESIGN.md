# Prospector dashboard redesign

Implemented inside the existing Go/server-rendered application. No frontend framework or new frontend dependency was added.

## What was wrong

The old home handler mixed legacy and v2 workflows, loaded up to 1,000 companies to calculate some totals, fetched a company for each pending draft, and gave many internal counters equal prominence. Its review navigation count came from up to 200 pending, non-blocked drafts, not the v2 composition or opportunity review queue. The observed 29 therefore did not mean 29 current v2 drafts awaiting approval.

The worker-running label was unconditional. Activity SSE refreshed the feed, but company, opportunity, review, provider, and budget panels stayed at their initial values. Incoming SSE entries received an unconditional "just now" label, which did not age. Internal action names and rollout details dominated the interface.

Two separate legacy dependencies caused the AI discrepancies:

- `Handler.aiProvider` is initialized only when `LegacySendEnabled` is true. That constant is permanently false. Checking `HasAIProvider()` on the dashboard therefore reported no provider even while v2 composition could initialize Gemini successfully.
- The old dashboard read `Handler.aiBudget`, the retired Redis drafting budget configured by `AI_DRAFTS_PER_HOUR/DAY`, with defaults of 5/30. V2 compose/review admission uses `AI_REQUESTS_PER_HOUR/DAY` and PostgreSQL calls/reservations. The dashboard was showing the wrong budget, not the effective v2 limits of 18/60.

## Information architecture and visual changes

The shared left sidebar uses actual existing routes: Dashboard, Profile, Opportunities, Companies, Compositions, and Conversations. Research status points to the real dashboard research panel. Historical drafts, conversations, and templates form a separate group. There is no invented Settings route, fake search, or decorative Messages link. The active page uses `aria-current="page"`.

The dashboard has a time-aware greeting, an existing `POST /admin/enrich` primary action, a concise operational banner, two rows of five metrics, and a three-column operational area. That area contains the provider, recent activity, opportunities to review, next steps, quick actions, and worker status. Detailed rollout/readiness information remains in an expandable section.

Light surfaces, restrained blue accents, subtle borders, moderate corner radii, compact cards, and generous gutters follow the reference. Green, amber, red, and blue have textual status labels as well as color. There are focus outlines, skip links, semantic headings, labeled progress elements, and real links/buttons. Navigation becomes a horizontally scrollable row on narrow screens; metrics and operational panels wrap.

The shared server-rendered shell is applied centrally because existing page handlers pass different data shapes to full-page templates. It buffers a successful page render before attaching the common sidebar and page wrapper; it does not require rewriting those workflows. Dashboard styling replaces the old dashboard stylesheet instead of retaining its old layout and layering overrides over it.

## Live updates and performance

`GET /dashboard/snapshot` returns one server-rendered HTML partial containing all dashboard state. The browser replaces that partial every **20 seconds**, with one immediate request after load. Hidden tabs skip polling and refresh when made visible. A refresh button uses the same path.

There is one polling timer and one request in flight at most. Requests have a 10-second client timeout and an 8-second endpoint context. The old activity SSE endpoint remains available for compatibility but the dashboard does not subscribe to it. No card has an independent polling loop.

A process-local, mutex-protected **10-second snapshot cache** coalesces simultaneous tabs and requests. Under normal 20-second polling, each poll obtains a new snapshot; a manual refresh during the cache window can reuse that snapshot. Persisted counts are gathered in one aggregate SQL statement. Two bounded queries obtain five attention items and recent recorded v2 events. Redis supplies the existing bounded activity history and a reachability check. Schema checks are cached for a minute. Worker status comes from memory, without database polling.

There are no company-per-draft lookups, per-card queries, full composition snapshot revalidations, or per-row opportunity reads. Match aggregates use the current opportunity revision, active approved profile revision, and current matcher version. Thus multiple historical assessments cannot inflate displayed totals.

If fetching a new snapshot fails, the browser keeps the previous content and explicitly labels it stale/unavailable. Initial database failures return 503 rather than presenting zero counts as a healthy empty workspace. Refresh preserves an open details panel and restores focused workflow links.

## Data sources and metric definitions

| Display | Authoritative state |
| --- | --- |
| Companies / opportunities | Full counts in `company` / `opportunity`, without an application-side row cap |
| Strong / possible / unrelated | `opportunity_match` pinned to the current opportunity revision, active profile, and current matcher version |
| Not assessed | Opportunities without an assessment satisfying those same current-input requirements |
| Opportunity selection review | `opportunity.selection_status = 'review_required'` |
| Composition semantic review | Unsent, undiscarded `draft_composition.status = 'review_required'` |
| Human approval pending | Unsent, undiscarded reviewed compositions without a live approval for their current composition revision |
| Drafts needing attention | Composition semantic review plus human approval pending; opportunity review is separate |
| Queued / running / failed | Composition state; expired running leases are displayed as failed, without rewriting records |
| Sent | Accepted v2 `send_attempt` records, not reservations or uncertain sends |
| Replies / bounces | Distinct v2 conversations with recorded reply/bounce `thread_event` records |
| Unresolved sends | Reserved, sending, or uncertain v2 attempts; these include in-flight work, not just failed sends |
| Research queue | The scheduler SQL eligibility: new companies, verified/manual domains, and due research scheduling |
| Never researched / last research | Null `company.last_researched_at` count / its latest persisted timestamp |
| Legacy drafts | Unsent, undiscarded drafts with no associated v2 composition |
| AI usage | `composition_call` over rolling one-hour and 24-hour windows, exactly matching admission |
| AI reservations | Remaining unexpired `composition_reservation` capacity, displayed separately from usage |
| Request failures | Persisted compose/review error metadata on composition revisions in the past 24 hours |

Research queue eligibility is not a promise that every company will run immediately: the existing Redis attempt cooldown can defer an eligible company. The research cycle event explicitly reports cooldown skips.

Opportunities to review prioritizes selection-review items, then strong and possible matches. It excludes closed and ignored opportunities, joins their actual company and role, and links directly to the opportunity workflow. It never substitutes arbitrary high-scoring companies. Reviewed drafts can still have stale inputs or other preflight blockers; this dashboard deliberately does not claim that review status alone makes a message sendable.

Next steps follow actual state: activate a missing profile, resolve semantic reviews, review drafts for human approval, resolve opportunity selections, explore strong matches, and research queued companies. Quick actions open existing opportunity, company/contact-plan, and composition workflows. No empty send or legacy follow-up action is presented as current v2 work.

## Provider and budget fixes

`Handler.compositionRuntime` is initialized by `loadCompositionRuntime()` using the existing `composition.NewConfigured("COMPOSE"/"REVIEW")` factories. The manual compose and edit/review handlers now use those same initialized provider instances and the same effective limits. The dashboard reads an allowlisted view of those instances: friendly provider name, model, initialized/usable state, usage, quotas, and failures. It never renders provider structs or credentials.

Both stage routes are shown because composition and review may use different providers/models. Missing credentials, unsupported providers, or missing explicit models result in failed initialization and an unavailable dashboard state. A disabled manual-composition flag or insufficient generation quota is also explicit. "Initialized" means initialization passed; it does **not** assert that credentials were remotely authenticated or that the next request will succeed. Remote availability is checked by actual work, and persisted request failures surface separately. No paid provider probe is performed by page loads or polling.

The initialized runtime reads `AI_REQUESTS_PER_HOUR/DAY` once and passes those same values to PostgreSQL admission. Provider and quota environment changes require an application restart, as with other startup configuration. No UI defaults are duplicated. The existing fail-closed behavior is preserved: absent/invalid quotas remain zero and manual generation requires limits of at least six to reserve its maximum compose/review attempts. No legacy 5/30 defaults are borrowed. The legacy Redis drafting budget remains confined to compatibility dependencies; disabled legacy drafting and send-policy behavior are unchanged.

Known provider, SMTP, and domain-search credentials are redacted from existing activity details before rendering. New persisted activity summaries exclude raw provider output and error bodies.

## Worker status and trustworthy health

Scheduler loops now expose a synchronized runtime view: heartbeat, working/waiting state, cycle start/completion, current or last attempted research company, error count since startup, and last failure time. Heartbeats update every **10 seconds** even during a long batch. The dashboard flags a heartbeat older than **35 seconds**, a missing runtime source, and recent worker errors. Discovery, research, and domain resolution remain independent from automatic sending.

Research instrumentation records real failures and reports attempted companies, companies yielding evidence, errors, and cooldown skips. This replaces a misleading success-only cycle summary whose "researched" counter actually measured companies yielding evidence. These are observation changes only: they do not change scheduling, retries, matching, or eligibility.

The health banner combines successful database snapshot reads, the existing schema checker, Redis reachability, worker heartbeats/recent errors, initialized v2 AI routes, failed/expired compositions, and unresolved send attempts. Manual-send readiness problems are surfaced when manual transmission is the selected mode. Deliberately disabled rollout is labeled as stopped; ordinary manual mode does not imply stopped research.

## Recent activity and timestamps

The dashboard merges the existing Redis activity history with persisted opportunity discovery, composition attempt/provider failure, approval, accepted-send, and conversation events. It sorts actual event timestamps and shows six recent entries. An empty history stays empty. Mutable current states are not invented as historical events.

Activity ages are recomputed for each snapshot, with exact RFC3339 timestamps in `time` attributes/tooltips. Worker completion labels also retain exact timestamps. No activity automatically claims a worker is alive; liveness comes from the scheduler heartbeat.

## Validation and browser inspection

Passed:

- `go test ./...` across the repository. The normal suite skips opt-in database integration tests when their connection environment is absent.
- The dashboard database integration test was separately run with `MIGRATION_TEST_ENV_FILE` against a unique scratch database created and cleaned up by the existing test harness. It verifies empty sources, live count changes, all three match classifications, exclusion of historical matcher/profile assessments, legacy separation, semantic-review versus approval counts, actual request consumption, and persisted failures. The application database is not modified by these fixtures.
- Dashboard tests verify initialized Gemini while the legacy provider remains disabled, shared compose/review instances, 18/60 effective quotas, usage, missing quota/provider states, credential redaction, empty activity, time-aware greetings, manual/automation-off presentation, worker liveness and overdue heartbeats, and 503 on database failure.
- Worker tests verify actual running/completed/unstarted loop state.

Headless Edge rendered the real server-side handlers against the configured database and provider configuration. Desktop inspection used **1672 x 1040**, with additional checks at **1024 x 1000** and **390 x 844**. Screenshots were captured and inspected. The browser confirmed ten metric cards, initialized Google Gemini, the configured models and 18/60 quotas, and the active Companies navigation state. Long company names, long activity entries, zero values, and nine-digit counts were exercised only through temporary browser DOM fixtures; those values are never in application templates or the database.

The live check changed a displayed count temporarily, waited for polling, and observed the real count return while the browser document stayed identical. Exactly **one** snapshot request occurred during that interval. A simulated 503 preserved the prior cards and displayed the unavailable/stale label. All checked widths avoided document-level horizontal overflow.

The verification server was a **read-only preview**: it did not start a duplicate scheduler or disturb the already-running application workers. Consequently the captured preview correctly shows "Research worker runtime unavailable" and an amber health state. The normal application entry point wires `scheduler.Status` into the handler; its liveness transformation and loop instrumentation are covered by tests. A full production-process restart with the new code was not performed during browser verification. The existing process must be restarted to serve the redesign and register the new heartbeat source.

Desktop evidence: [dashboard screenshot](assets/dashboard-desktop.png). Additional local inspection artifacts: `.local/dashboard-mobile.png`, `.local/dashboard-stress.png`, and `.local/dashboard-browser.cjs`.

## Files changed for this redesign

- `cmd/prospector/main.go`: snapshot route, worker-status wiring, correct `/static/` prefix stripping.
- `internal/web/handler.go`: shared runtime dependency, snapshot home handler, buffered shared-shell rendering; removes old capped/N+1 dashboard assembly.
- `internal/web/composition.go`: consumes shared initialized providers, enabled state, and effective request quotas.
- `internal/composition/provider.go`: updates initialization documentation to reflect the shared v2 runtime.
- `internal/web/dashboard_runtime.go`: initialized v2 provider/configuration source and safe provider labels.
- `internal/web/dashboard.go`: dashboard view, snapshot assembly/cache, health, actions, timestamps, activity redaction, partial endpoint.
- `internal/store/dashboard_snapshot.go`: aggregate counts, bounded attention query, recorded v2 event query.
- `internal/worker/status.go`, `internal/worker/scheduler.go`: synchronized heartbeat/cycle/company/failure observations and truthful activity summaries.
- `internal/web/shell.go`, `internal/web/templates/sidebar.html`: shared navigation and page shell.
- `internal/web/templates/dashboard.html`, `internal/web/static/dashboard.css`, `dashboard.js`, `shell.css`: responsive layout and one lightweight polling client.
- `internal/web/dashboard_test.go`, `phase6_test.go`, `phase7_test.go`, `internal/store/dashboard_snapshot_test.go`, `internal/worker/status_test.go`: semantic regression coverage.
- This document and `docs/assets/dashboard-desktop.png`.

Unrelated existing workspace changes were preserved.

## Deviations from the reference

There is no search box, Settings route, tip carousel, fabricated weekly trend, or invented provider-availability badge. The research panel replaces the reference tip card because worker state is operationally useful. The provider card includes both stages and reservations, so it needs more information than the mockup. Actual v2 attention counts differ from the reference's legacy-derived review number. Historical follow-ups and unclassified threads are kept in historical workflows instead of becoming urgent current-v2 counters.

## Explicit answers

1. **Why did the old dashboard say no provider?** It checked the permanently disabled legacy provider instead of the v2 compose/review provider routes.
2. **Authoritative provider source?** Initialized `Handler.compositionRuntime` provider instances, shared with the actual manual composition/review handlers.
3. **Authoritative hourly/daily limits?** That same initialized v2 runtime's `AI_REQUESTS_PER_HOUR/DAY` values, passed directly to PostgreSQL request admission; usage comes from calls and reservations.
4. **Live without full-page refresh?** Yes, one server-rendered snapshot partial is replaced in place.
5. **Update frequency?** Every 20 seconds while visible, immediately after load/on returning to the tab, and on manual refresh. Cache coalescing lasts 10 seconds; worker heartbeats are 10 seconds and schema checks at most once a minute.
6. **Multiple independent DB polling loops?** No. One browser timer/endpoint, one in-flight request, and one shared snapshot cache; no per-card polling.
7. **All metrics real?** Yes, from persisted records or the running scheduler, with explicit unavailable states.
8. **Estimated/fabricated trends?** None. There are no trend numbers; budget percentages derive from actual usage and configured limits.
9. **What does Needs review mean?** Labels distinguish opportunity selection review, composition semantic review, and reviewed drafts awaiting explicit human approval. The primary draft-attention count is the latter two combined. Legacy drafts are separate.
10. **Research visibly independent from sends?** Yes, it has its own runtime status and panel and continues in manual mode regardless of automation being off.
11. **Automation still off?** Yes. The configured runtime reports automation off, and the redesign does not modify activation or send configuration.
12. **Backend safety/business rules changed?** No. Only dashboard reads, shared initialization of existing composition configuration, rendering, and worker observability changed. Matching thresholds, profile facts, approval semantics, send policy, suppression, idempotency, SMTP behavior, contact policy, rollout mode, and automation eligibility are unchanged. Manual sending remains approval-gated.
