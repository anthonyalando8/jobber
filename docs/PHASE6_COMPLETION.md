# Phase 6 completion: legacy transition and review-only pilot

Status: implemented, tested, and migrated on the configured database. The system is in
**`v2_review`**. **No email has been sent, no candidate profile was activated, and no historical
record was altered** — all seven content digests over historical outbound text are byte-identical
before and after the phase. Awaiting review before Phase 7.

The Phase 6 success criterion was: *the repository is fully transitioned from the legacy email
pipeline into a review-only v2 operating state: historical data remains truthful and immutable,
legacy unsent material cannot bypass v2, real opportunities/plans/drafts can be inspected through
the new workflow, structured review reasons are operationally useful, and automation remains off
while the system is prepared for an explicit Phase 7 decision.* Section 24 answers the fifteen
questions; section 23 states where the criterion is met and where it is limited.

One thing to read first, because it shapes everything else: **the live database has no active
candidate profile**, so matching answers `profile_required` and nothing can become authoritative
there. That is the state the brief anticipated in §9, and it was not changed. The real-data
pilot (§10) was therefore run as a *rehearsal* that computes what the pipeline would conclude,
writing nothing — plus a walk through the live workflow showing it correctly refusing.

---

## 1. Rollout mode

`v2_review`, and it is the default when nothing is configured (`TestReviewIsTheDefaultRolloutMode`).

| Fact | State | Enforced by |
| --- | --- | --- |
| Rollout mode | `v2_review` | `send.EvaluateReadiness`; preflight blocks with `rollout_mode_forbids_sending` |
| Automatic sending | Disabled | `send.AutomaticSendingEnabled()` returns `false` — a function body, not a setting |
| Auto-send intents | Empty | `AUTO_SEND_INTENTS` unset; parsed and displayed so a stray value is visible |
| Scheduler | Not started | `cmd/prospector/main.go` gates worker startup on `AutomaticSendingEnabled()` |
| Manual approved sending | Implemented, not enabled in this mode | Phase 5 `v2_manual_send` remains available for an explicit later decision |

The allowlist parser exists specifically so that "empty" is a *checked* fact rather than an
assumption. `TestReviewOnlyRolloutHasNoAutomation` sets `AUTO_SEND_INTENTS` to a real intent and
asserts that automatic sending still cannot happen — nothing consults the allowlist to send, so a
value set by accident surfaces on the dashboard instead of taking effect.

---

## 2. Legacy unsent drafts

**62 on the live database.** Readable, permanently unsendable.

The original design proposed a `pipeline_version = legacy` column. What was built is stronger and
was already in place: **a draft belongs to v2 if and only if it has a `draft_composition` row.**
That is a foreign key rather than a label that could disagree with reality, and it is the same
fact the send path reads — `dispatch.Service.Send` loads a composition, so a legacy draft has
nothing for it to load. There is no route that accepts one.

`/legacy/drafts` lists them with their original subject and body intact, and every entry carries
one wording:

> This draft was created by the legacy pipeline and does not carry the candidate profile,
> opportunity, recipient and review context that v2 requires. It is kept exactly as written, for
> reference. To contact this company now, create or select a contact plan and generate a new
> composition, which is then validated and reviewed before it can be approved.

`TestLegacyDraftPageOffersNoSendPath` asserts the rendered page contains no `/send`, `/approval`
or `/compositions/{id}` control at all — absent, not disabled. No legacy body was rewritten;
`TestListLegacyUnsentDraftsPreservesText` pins that, including unresolved `{{company}}`
placeholders, which are part of the historical record.

---

## 3. Legacy sent history

**34 sent legacy drafts, 33 `send_log` rows, 2 with follow-ups already sent.** Untouched.

Nothing rewrites a subject or body, assigns an invented intent, claims the current profile was
used, or attributes reconstructed evidence to an old decision. The legacy thread page states it
plainly for every message:

> Profile revision, opportunity, selected evidence and review result: **unknown**. None of these
> was recorded when this message was sent, and none is reconstructed here.

The one discrepancy worth naming: there are 34 sent drafts but 33 `send_log` rows. One sent draft
has no send-log entry. That is a pre-existing legacy inconsistency; it was left exactly as found,
because inventing the missing row is precisely what §16 forbids.

---

## 4. Legacy thread classification

Migration `0024_legacy_transition.sql` adds one table, `legacy_thread_intent`. It is the only
thing Phase 6 needed to record that did not already exist.

A classification exists **only because a person stated it**. The four storable answers are the
three real intents plus `unknown`. `TestLegacyIntentVocabulary` asserts that the old segment
strings — `application`, `hiring`, `hiring_us`, `agency`, `enterprise`, `sme` — can never be
stored as an intent, which is the concrete form of "do not infer intent from company segment or
the old application flag".

Design choices worth flagging:

- **A basis is required.** `basis text NOT NULL CHECK (basis <> '')`. A classification that cannot
  be audited is refused. The UI asks "What are you basing that on?"
- **`unknown` pins nothing.** The CHECK constraint forbids an opportunity or recipient alongside
  it, and the store nils them defensively. An unknown answer that still pinned a recipient would
  be exactly the reconstructed metadata §16 prohibits.
- **Corrections supersede, never overwrite.** A partial unique index keeps one current answer per
  thread; a trigger refuses every edit and every delete. What was believed, and when, survives.

**33 of 33 threads remain unclassified.** Nothing was classified on the operator's behalf: the
tool is built, the judgements are theirs.

---

## 5. Follow-up behaviour on legacy threads

An unclassified or explicitly-unknown thread is not follow-up ready — `LegacyThread.Classified()`
returns false for both, verified in `TestUnclassifiedLegacyThreadIsNotFollowupReady`. A classified
thread becomes eligible for an ordinary v2 follow-up: a new composition, both validation layers,
a fresh review and a fresh approval. No old text and no old approval is carried across, and no
generic payments follow-up copy exists anywhere in the v2 path.

Automatic follow-up sending remains impossible (§1).

---

## 6. Legacy templates

**10, read-only.** `/templates` is now split:

- **V2** — states that the text actually in use comes from versioned prompts in
  `internal/composition/prompts`, changed in the repository and reviewed like code, deliberately
  not edited live against a running database. It also shows the rollout mode and automation state.
- **Legacy** — the 10 old templates, rendered as `<pre>` blocks. No form, no textarea, no save.

`POST /templates/{id}/update` returns **410 Gone**. `TestLegacyTemplatesAreReadOnly` asserts the
page contains no edit form and no textarea while still showing the original copy, and
`TestLegacyTemplateEditRouteIsClosed` asserts the route refuses with an explanation. The
payments-heavy copy stays inert no matter that it remains in the database.

---

## 7. Old routes and controls

| Route | Before | Now |
| --- | --- | --- |
| `/companies/{id}/send` | legacy send | 410 Gone (Phase 5) |
| `/review/send` | legacy send | 410 via `executeSend` |
| `/followups/send` | generic follow-up | 410 Gone |
| `/sent/{id}/resend` | clone and resend | 410 Gone |
| `/companies/{id}/generate-draft` | template fill | **410 Gone**, pointing at the contact plan |
| `/admin/draft` | batch template fill | **410 Gone** |
| `/templates/{id}/update` | live copy editing | **410 Gone** |
| `/companies/{id}/apply-mode` | set segment = `application` | **redirect to `/companies/{id}/plan`, writes nothing** |

The two generation routes previously failed with `500 AI provider not configured` — technically a
refusal, but one that reads as a broken deployment. They now explain what they were and where to
go instead.

`apply-mode` deserves its own note. It used to set a company-wide flag that decided which template
the next draft was built from — a company-level setting standing in for what an individual message
is for. v2 separates the two: business segment says nothing about intent, and intent belongs to a
contact plan alongside the opportunity, recipient and approved proof. So the control now writes
nothing and sends the operator to the plan, where the choice is made explicitly against a real
opportunity. `TestApplyModeCannotSetV2Intent` reads the handler source and fails if it performs
*any* write, and `TestRouterWiresNoLegacyGenerationOrSendHandler` reads the route table and fails
if a legacy generation or template-edit handler is ever wired back in.

---

## 8. Review-only real-data exercise

This is the main pilot. A new command, `cmd/prospector-rehearse`, answers one question without
changing anything: *if a given profile revision were active, what would the deterministic pipeline
conclude about the opportunities already stored?* It issues no INSERT, UPDATE or DELETE, never
activates a profile, never calls an AI provider and never contacts a mail server.

**All 273 stored opportunities** were assessed against profile revision 3 (the prepared, inactive
one). Not a capped sample — the UI's list endpoint stops at 200, so a dedicated full-corpus query
was added rather than quietly misreporting a third of the data.

### Classification

| | Count | Share |
| --- | --- | --- |
| `strong_match` | 6 | 2% |
| `possible_match` | 94 | 34% |
| `unrelated` | 173 | 63% |

Stored descriptions: 262 complete, 11 partial.

### What blocks a stronger classification

| Blocker | Occurrences |
| --- | --- |
| `unresolved_mandatory_requirement` | 273 |
| `unrelated_application_role` | 173 |
| `family_not_clear` | 55 |
| `insufficient_supported_capabilities` | 39 |
| `insufficient_requirements` | 24 |
| `unresolved_mandatory_condition` | 23 |
| `known_preference_conflict` | 1 |

### The finding worth your attention

A Backend Engineer role at Grafana Labs scores **100** and is still classified
**`possible_match`** — which looks like a bug until you read the blockers:

```
unresolved_mandatory_requirement: Mandatory requirement 1 years of experience is not supported…
unresolved_mandatory_requirement: Mandatory requirement Terraform is not supported…
unresolved_mandatory_requirement: Mandatory requirement Node.js is not supported…
```

This is the classification gate correctly overriding the score, exactly as the approved design
requires. It is worth stating because it means **score is not the operative quantity** — a
high-scoring role with one unmet mandatory requirement is review-required, by design.

I initially suspected a systemic problem: your approved profile deliberately makes no
years-of-experience claim, so every posting stating one would permanently block `strong_match`.
Measured, that concern is smaller than it looked — **exactly 2 opportunities** are blocked *solely*
by an experience requirement. Elsewhere the experience gap co-occurs with real technology gaps.
It remains a standing policy question for Phase 7 (§22), not a matching defect, and I have not
touched the matcher or your profile.

### The live workflow, walked

The same companies were then taken through `assess.Service` exactly as the web UI does. Every one
returns the same answer:

```
Grafana Labs (company 7116) — relevance 0, stored assessment: false
  intent outcome: blocked, requires review: true
  BLOCKER: profile_required: No approved candidate profile revision is active.
           No authoritative contact plan can become ready.
```

That is the correct behaviour and it confirms §9: the review-only workflow is operable and
refuses, rather than silently producing something.

Recipient ranking still runs and is worth reading:

| Company | Top recipient | Verdict |
| --- | --- | --- |
| Samsara | `77585ea9…@o212792.ingest.sentry.io` | `review_required` — no stored role, no recognizable function |
| Cosuno | `info@cosuno.de` | `review_required` — generic mailbox, no identified person |
| Sticker Mule | `help@stickermule.com` | `review_required` — generic mailbox |

The first is a Sentry ingest address that legacy enrichment scraped from a page and stored as a
contact. It is correctly refused rather than used — but it is also a live data-quality problem
worth a cleanup pass before any real outreach.

**Coverage against §8's list:** strong match ✓ (6 found), possible/partial ✓ (94), unrelated ✓
(173), weak-evidence company ✓ (relevance 0 examples), generic mailbox ✓ (three above), legacy
draft/company ✓ (§2). Speculative-enquiry and service-outreach candidates could not be exercised
end to end, because intent resolution is blocked upstream by `profile_required` — a genuine gap,
recorded in §23.

---

## 9. Candidate profile state

**Three revisions stored. None active.**

| Revision | Status | Active |
| --- | --- | --- |
| 3 | draft | no |
| 2 | draft | no |
| 1 | approved | no |

Nothing was activated. No migration, startup path or test activates a profile, and the rehearsal
command explicitly does not. The dashboard now states the consequence in plain words rather than
showing zeros that look like an empty pipeline:

> **No approved candidate profile is active** (3 revisions stored). Matching answers
> `profile_required`, so no assessment, contact plan, composition or send can become authoritative.

Activating a revision is your decision and belongs on the profile page.

---

## 10. Representative generated drafts

**None were generated, and no provider quota was spent.**

§10 conditions this on an approved profile permitting real v2 composition. None is active, so real
composition is not permitted and generating drafts would have required activating a profile — which
§9 forbids doing as part of this phase. Composition, both validation layers and the semantic
reviewer remain covered by the Phase 4 evaluation and by the synthetic end-to-end tests.

---

## 11. Budget and quota visibility

The dashboard shows composition attempts in the last 24 hours (from `composition_call`, the real
accounting table), compositions queued / running / review-required / reviewed / **failed**, and
the actual configured AI request budget with its pause reason — never a capacity inferred from the
number of API keys.

Provider exhaustion leaves a draft visibly `failed` or `review_required` with its attempts
recorded. Nothing disappears and nothing restarts on its own: `QueueComposition` marks an
abandoned run failed rather than re-running it, and retry is an explicit act.

---

## 12. Dashboard

A v2 panel now sits above the legacy strip, because the v2 pipeline is what the system runs on and
the legacy counts below it describe data, not a live pipeline. It shows: rollout mode, automatic
sending state, the auto-send allowlist, send-path readiness reasons, the profile blocker,
companies, opportunities, strong / possible / unrelated / unassessed / needing-review, plans
selected, compositions queued / composing / needing review / reviewed / failed, provider calls in
24h, live approvals, send attempts, **attempts needing reconciliation**, active threads, and the
two legacy counts linking to their pages.

`unassessed` is deliberately its own number. An opportunity with no assessment for its current
revision is not "unrelated" — it is unanswered, and showing it as anything else would overstate
what is known. On the live database all 273 are unassessed.

Navigation gained Compositions, Conversations and Legacy; "Templates" became "Message text".

---

## 13–14. Documentation

`docs/CURRENT_STATE.md` was rewritten. It opens with a state table (schema 24, phases complete,
rollout mode, automation state, profile state, live counts, emails sent), then a diagram of how
the pipeline actually works, then legacy-data handling, then where an operator works, then the
known limitations, then the next phase. The pre-v2 description is retained below a divider marked
as history rather than deleted.

`README.md` now points at current state first and describes `v2_review` honestly. The
implementation plan's status line reads Phases 1–6 on schema 24. The historical investigation and
design documents are untouched.

---

## 15. Legacy inventory

`prospector-migrate -command legacy-inventory` is a new read-only census. Live output:

| Category | Count | Disposition |
| --- | --- | --- |
| `legacy_unsent_drafts` | 62 | stale, blocked from v2 |
| `legacy_discarded_drafts` | 11 | preserved |
| `legacy_sent_drafts` | 34 | read-only |
| `legacy_send_log_threads` | 33 | preserved |
| `legacy_threads_unclassified` | 33 | needs explicit classification |
| `legacy_threads_classified_unknown` | 0 | needs explicit classification |
| `legacy_threads_classified` | 0 | migrated reference retained |
| `legacy_followups_already_sent` | 2 | preserved |
| `legacy_templates` | 10 | read-only |
| `legacy_detection_signals` | 1697 | migrated reference retained |
| `companies_with_legacy_segment` | 172 | read-only |
| `suppression_rows` | 1 | preserved |
| `v2_compositions` | 0 | actionable through v2 |
| `v2_send_attempts` | 0 | preserved |

There is deliberately no "deleted" disposition — `TestLegacyInventoryNeverReportsDeletion` asserts
none of them implies data loss. Nothing was deleted to reduce a count.

---

## 16. No fabricated metadata

Nothing assigns a profile revision, opportunity revision, evidence set, selected capabilities,
semantic review or intent to an old sent message. Where a fact was never recorded it reads
`unknown`, in the data model and in the UI. The one classification a person *can* supply is
recorded as their statement, attributed, with its basis, and superseded rather than edited.

---

## 17. Preserved historical data

The inventory computes md5 digests over historical outbound content. Captured immediately after
migration 0024 and again after all Phase 6 work:

| Digest | Value | Changed? |
| --- | --- | --- |
| `sent_draft_bodies` | `845286cd34342492c604fbec5346cbc4` | no |
| `sent_draft_subject` | `ebf0329ec12145f60df641dfd6a58578` | no |
| `send_log_rows` | `b906fe6b37fd79e77f962abcb91a7a44` | no |
| `template_bodies` | `5c2c9dd446dba32e57cda4aa009f42d6` | no |
| `suppression_rows` | `27fef57aa0ba87cf43910b0e6e7688ba` | no |
| `contact_rows` | `2d78df86738bf6c80a4800d6061169c3` | no |
| `company_rows` | `c056f68cb573018c7cbb6da6368d3096` | no |

All seven identical. `TestPreservationDigestsDetectChange` proves the digests are not vacuous: it
adds a classification and asserts the sent-content digest does **not** move, then rewrites a sent
body and asserts it **does**.

Row counts before and after migration 0024: one new table, zero removed tables, zero row-count
changes in any pre-existing table.

---

## 18. Workflow UX

The route from company to reviewed draft is: `/companies/{id}` → `/companies/{id}/plan` (inspect
fit, choose intent and recipient) → generate → `/compositions/{id}` (intent, recipient, subject and
body, both validation layers, stale state, approval state, send eligibility). No internal ID needs
typing anywhere; every step is a link or a form on the previous page. Confusing legacy controls are
removed or relabelled; no wholesale redesign was attempted.

---

## 19. Review backlog quality

Review reasons are specific and actionable, each carrying a **code**, a **sentence** and an
**action**. They are deliberately not collapsed into "needs review", because Phase 7 has to decide
which categories could ever be automated and that decision needs the category.

| Source | Example codes |
| --- | --- |
| Staleness | `stale_profile_changed`, `stale_opportunity_changed`, `stale_recipient_changed` — one per changed input |
| Composition state | `composition_queued`, `composition_running`, `composition_failed` |
| Deterministic | the validator's own codes, e.g. `unapproved_technology` |
| Semantic | the reviewer's own codes, e.g. `fit_uncertain` |
| Review availability | `review_unavailable`, `review_incomplete` |
| Legacy | `legacy_draft` |
| Matching | `family_not_clear`, `insufficient_requirements`, `unresolved_mandatory_requirement`, `known_preference_conflict`, … |
| Recipient | `recipient_function_unknown`, generic-mailbox findings |

`TestReviewReasonsAreSpecificAndActionable` asserts distinct codes survive, that each has text and
an action, and that informational findings are *not* promoted into review reasons.
`TestMissingSemanticReviewIsItsOwnReason` asserts a missing or incomplete review is its own
category and never a pass.

---

## 20. Stale-state behaviour

`CompositionStaleReasons` covers policy, plan, profile, recipient, company, opportunity, matcher,
match, selection and evidence. Each surfaces as its own reason
(`TestStaleDraftsAreIdentifiablePerInput`), and staleness blocks sending with `stale_inputs`
(Phase 5 tests J, K, L). Non-material changes — a refreshed discovery timestamp, an unrelated
company note — do not stale a draft, because comparison is by material content hash and revision,
not by `updated_at`.

Verification against real stored data is necessarily partial: with no active profile there are no
live v2 drafts to stale. The rules are proven on real schema with synthetic drafts.

---

## 21. Phase 5 limitations carried forward

Unchanged and still true:

1. **No automatic asynchronous bounce ingestion.** No IMAP, no webhook. SMTP-time rejections are
   caught during the conversation; a bounce arriving later by email suppresses an address only
   once someone records it.
2. **SMTP only.** The `Transport` interface is provider-agnostic but unproven against a second
   provider.
3. **No real SMTP smoke test.** The conversation has only met a scripted in-process server.
4. **Stuck `sending` / `uncertain` attempts have no reconciliation tooling.**

Two items were addressed as operational hardening:

**Sender domain — fixed.** `senderDomain()` was a placeholder returning `prospector.local`
regardless of input, and it was derived from the *company's* domain. A Message-ID stamped with the
recipient's domain claims to originate from a host we do not control, which breaks threading and
reads as forgery to a receiving server. `Transport` now exposes `SenderDomain()`, `SMTPTransport`
derives it from the configured `FROM_EMAIL`, an unusable address yields an empty domain, and send
readiness now requires a usable sender domain rather than a merely non-empty from address.
`TestSenderDomainComesFromConfiguredFromAddress` and an assertion in `TestApprovedDraftSendsOnce`
pin it.

**Stuck attempts — surfaced, not redesigned.** The dashboard now counts attempts in `reserved`,
`sending` or `uncertain` and explains that each holds its idempotency key and blocks further sends
of that message. I did **not** build the reconciliation workflow: releasing a held key requires a
state transition that the Phase 5 trigger deliberately forbids, so it would mean changing the
send-path state machine and the live-key index you just approved. That deserves its own design and
review rather than being slipped into a transition phase.

---

## 22. Phase 7 readiness analysis

**No intent is enabled, and none is currently eligible.** One gate blocks all three today: no
active profile. Beyond that, per intent:

### `specific_job_application`

*Closest to eligible.* Deterministic gates already do most of the work: `strong_match` requires a
clear desired family from the title, a complete description with sufficient recognized
requirements, at least two approved capabilities supported by actual requirement excerpts, and no
unresolved mandatory requirement or known preference conflict.

Before it could be considered: an active approved profile; measured composer/reviewer quality
against real strong matches (none exist yet — 6 candidates identified, none composed); a decision
on the years-of-experience question (§8) since a posting stating one currently blocks
`strong_match`; a confirmed non-generic recipient with an identified function; clean semantic
review on the exact content hash with no manual edit; all inputs current; domain and suppression
limits clear; and a measured false-positive rate for `stock_opening`-class findings over a real
sample.

At today's distribution that is **6 of 273 opportunities**, before any recipient-quality filter.

### `speculative_job_enquiry`

*Not close.* It depends on company relevance rather than a matched vacancy, and relevance on the
live data is weak — the walked examples scored 0 and 10. It also needs confirmed engagement
preferences, an identified engineering or hiring contact rather than a generic mailbox, and a
truthful company-connection reason that is not derived from keyword detection. The legacy signal
corpus (1697 rows, largely `legacy_detection`) cannot support this; it would need fresh evidence
with real provenance.

Additional risk: a speculative enquiry has no advertised title to anchor it, so the failure mode is
a fluent generic message. Human review is exactly the control that catches that.

### `service_outreach`

*Furthest.* It requires evidence of a genuine engineering need, which is the hardest thing to
establish truthfully and the place the old pipeline failed worst — inferring a payments problem
from the presence of a checkout. `checkout_present` and `paybill_listed` contribute nothing, by
design. Until there is real, quoted evidence of a stated need, this should stay human-reviewed
indefinitely rather than pending on a checklist.

### Remaining review-required by design

Possible matches, unknown or generic recipients, and **all follow-ups** — regardless of intent.

### Minimum bar for any intent

Active approved profile · `strong_match` where a match applies · recipient with an identified
function and a real address · clean deterministic validation · complete clean semantic review bound
to the exact content hash · no manual edit after review · all inputs current (no stale reason) ·
within daily, interval and per-domain limits · address and domain not suppressed · a measured
false-positive rate from a real sample, not a synthetic one.

---

## 23. Tests, migration and live-data impact

**26 new tests** (19 web, 7 store). Full suite green across 21 packages.

```
ok  internal/web       (19 new)    ok  internal/store   (7 new)
ok  internal/dispatch            ok  internal/send
ok  internal/composition         ok  internal/assess      … 21 packages, all ok
```

Acceptance coverage against §23 of the brief:

| | Case | Test | Result |
| --- | --- | --- | --- |
| A | Legacy unsent draft | `TestLegacyDraftPageOffersNoSendPath`, `TestListLegacyUnsentDraftsPreservesText`, `TestLegacyDraftIdentification` | stale/legacy, no send control exists, text unchanged |
| B | Legacy sent message | `TestLegacyThreadPageReportsUnknownMetadataHonestly`, preservation digests | content unchanged, metadata reads unknown |
| C | Legacy template | `TestLegacyTemplatesAreReadOnly`, `TestLegacyTemplateEditRouteIsClosed` | visible, read-only, cannot drive v2, route 410 |
| D | Old application mode | `TestApplyModeCannotSetV2Intent` | writes nothing, redirects to the contact plan |
| E | Unrelated opportunity | matcher gate + live rehearsal (173 unrelated) | `unrelated_application_role` blocks a specific application |
| F | Possible match | live rehearsal (94) with explicit blockers | review-required with named reasons |
| G | Strong match | live rehearsal (6) | proceeds to plan/composition/review, never auto-send |
| H | Provider review unavailable | `TestMissingSemanticReviewIsItsOwnReason` | `review_unavailable` / `review_incomplete`, never a pass |
| I | Stale input | `TestStaleDraftsAreIdentifiablePerInput` | one identifiable reason per changed input |
| J | Repeated evidence | evidence grouping (Phase 3) | fact groups counted once |
| K | Payments project on generic backend role | selection rules (Phase 3/4) | backend proof without a payment pitch |
| L | Review-only rollout | `TestReviewOnlyRolloutHasNoAutomation`, `TestReviewIsTheDefaultRolloutMode` | automatic sending impossible even with the allowlist set |
| M | Legacy send route | `TestLegacySendAndFollowupRemainClosed`, `TestRouterWiresNoLegacyGenerationOrSendHandler` | closed, and cannot be re-wired unnoticed |
| N | Automatic follow-up | `TestLegacySendAndFollowupRemainClosed` | unavailable |
| O | Historical preservation | `TestPreservationDigestsDetectChange` + live digests | unchanged, and the check is proven non-vacuous |

Supporting: `TestLegacyThreadClassificationLifecycle`, `TestLegacyClassificationIsImmutable`,
`TestLegacyInventoryCountsRealRows`, `TestPipelineStatsRunAgainstRealSchema`,
`TestDashboardStatesRolloutAndProfileBlocker`, `TestDashboardClassificationNamesMatchTheMatcher`
(guards against a status string drifting and silently reading zero),
`TestSenderDomainComesFromConfiguredFromAddress`.

Every mutating store test runs against its own randomly named scratch database and asserts
`current_database()` before writing.

**Live migration.** `0024_legacy_transition.sql` applied following the documented procedure:
`inspect` → `backup-verify` (dump, restore into an isolated scratch database, compare schema, row
counts, digests, sequences) → `apply -proof` → `check` → `legacy-inventory`. One new table, zero
removed tables, zero row-count changes in any pre-existing table, all seven content digests
identical.

No email was sent. No profile was activated. No AI provider was called.

---

## 24. Explicit answers

**1. Can a legacy unsent draft send?**
No. A draft is v2 only if it has a `draft_composition` row, which is what the send path loads; a
legacy draft has nothing to load and no route accepts one. The page offers no send, approve or
composition control at all.

**2. Was any historical sent content modified?**
No. All seven preservation digests are byte-identical before and after the phase, and the check is
proven to detect a change.

**3. Can an old template affect v2 generation?**
No. v2 composition reads versioned prompt files; the template table feeds nothing. The page has no
edit control and the update route returns 410.

**4. Can old company segment or application state choose a v2 intent?**
No. Intent lives on a contact plan and is chosen explicitly. `apply-mode` writes nothing and
redirects. The old segment strings cannot even be stored as a legacy classification.

**5. Is `v2_review` the active/default rollout?**
Yes, and it is the default when nothing is configured.

**6. Are auto-send intents empty?**
Yes. Parsed and displayed so a stray value is visible; a value set by accident still cannot enable
sending.

**7. Can any automatic first-contact path send?**
No. `AutomaticSendingEnabled()` returns false unconditionally and the scheduler is gated on it.

**8. Can any automatic follow-up path send?**
No. Follow-up policy is enforced but nothing schedules or sends one; every follow-up needs
composition, review, approval and a person.

**9. Are review-required reasons specific and actionable?**
Yes — code, sentence and action, kept distinct rather than collapsed, with staleness reported one
reason per changed input. This is the information Phase 7's decision depends on.

**10. Are stale v2 drafts clearly identifiable?**
Yes, per changed input, on the draft page and as a blocker at send time.

**11. Can the review-only workflow be operated on representative real data?**
Partly, and the limit is the profile. All 273 real opportunities were assessed in a rehearsal
producing 6 strong, 94 possible, 173 unrelated with full reasons; the live workflow was walked and
correctly refuses with `profile_required`. Composition and review could not be exercised on real
data because no profile is active.

**12. What is the active candidate-profile state?**
None active. Three revisions (1 approved, 2 draft), all inactive. Nothing was activated.

**13. What legacy records still require explicit classification?**
All 33 legacy sent threads. The tool is built; none was classified on your behalf.

**14. Are Phase 5 send-history and suppression records preserved?**
Yes. `send_attempt`, `outbound_thread` and `thread_event` are untouched and empty; the single
suppression row is intact and enforced at send time.

**15. Which intents are candidates for Phase 7, under what gates?**
None today — no active profile. Ranked by distance: `specific_job_application` is closest and
would need the gates in §22 plus a measured false-positive rate on real strong matches;
`speculative_job_enquiry` needs fresh evidence with real provenance and identified contacts;
`service_outreach` should stay human-reviewed until quoted evidence of a stated need exists.
Possible matches, unknown recipients and all follow-ups remain review-required by design.

---

## 25. Deviations and known limits

1. **No `pipeline_version` column.** The design proposed one; the implementation uses the presence
   of a `draft_composition` row, which is a foreign key rather than a label and is what the send
   path actually reads. Strictly stronger, but a deviation from the written plan.
2. **No representative generated drafts** (§10), because no profile is active. Conditional in the
   brief, but the pilot is thinner for it.
3. **Speculative-enquiry and service-outreach intents were not exercised end to end** on real data,
   for the same reason.
4. **33 legacy threads remain unclassified.** Classifying them is a series of human judgements.
5. **No reconciliation tooling** for stuck or uncertain attempts (§21). Surfaced, not solved.
6. **One sent draft has no `send_log` row** (34 vs 33). Pre-existing; left as found.
7. **A Sentry ingest address is stored as a contact** for Samsara. Correctly refused by recipient
   ranking, but the contact data would benefit from a cleanup pass.
8. **`/opportunities` still lists at most 200 rows** with no pagination control, so 73 of 273 are
   not reachable from that page. Pre-existing; the rehearsal uses a full-corpus query instead.
9. **Legacy handler functions still exist** in the source (`CompanyDraft`, `AdminDraft`,
   `TemplateUpdate`) though no route wires them. A test guards against re-wiring.

---

## 26. What I would check first in review

1. **§8's rehearsal numbers**, and whether 6 strong matches from 273 matches your expectation. If
   it seems low, the blocker table says exactly why, and the years-of-experience question is the
   first thing to settle.
2. **The `apply-mode` change.** It silently stops writing a segment some workflow may rely on.
3. **Whether `unknown` is the right default** for the 33 legacy threads, or whether you would
   rather classify them before Phase 7 so follow-ups become possible.
