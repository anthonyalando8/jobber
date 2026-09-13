# Phase 7 completion: selective automation

Status: implemented, tested, and migrated on the configured database. **Automation is off.**
`AUTO_SEND_INTENTS` is empty, the rollout mode is `v2_review`, no automation loop is running, and
no email has been sent. Awaiting review.

The Phase 7 success criterion was: *the system can explain and enforce exactly which
fully-reviewed v2 messages would be safe enough for selective automation, while possible fits,
uncertain recipients, service/speculative outreach and follow-ups remain human-reviewed; no
automation is enabled merely because implementation is complete.* Section 20 answers the eighteen
questions.

**The number that matters most:** measured against all 273 stored opportunities, **zero would
currently qualify for automation** — and not marginally. Six reach `strong_match`; of those, four
have no contact on file and two have only a generic mailbox. Enabling automation today would send
nothing. Section 14 has the full breakdown.

---

## 1. Architecture: a decision layer, not a second pipeline

`internal/automation` is a pure policy package. Like the send preflight it is a function of an
explicit snapshot — it reads no database, calls nothing, and returns every reason.

```
reviewed composition ─▶ automation.Evaluate ─▶ eligible_for_auto | review_required
                              │                        │
                              │                        └─▶ persisted either way
                              ▼
                     approve (actor: automation:auto-v1)
                              ▼
                     RE-EVALUATE against fresh state
                              ▼
                     dispatch.Service.Send ─▶ send.Evaluate ─▶ reserve ─▶ transmit
```

The automation layer can skip nothing. It does not compose, does not choose a recipient, does not
select evidence, and cannot transmit: it calls the same `dispatch.Service.Send` a person does,
which runs the full Phase 5 preflight and reserves the idempotency key before contacting a
provider. Every Phase 1–6 invariant named in §1 of the brief is still enforced by the code that
always enforced it.

The authorization is the **same `send_approval` row** a person creates — pinning the same content
hash, recipient and intent — with `approved_by` set to `automation:auto-v1`. The actor differs and
differs visibly; the pinning does not.

---

## 2. Automation policy

`automation.Evaluate` returns `eligible_for_auto` or `review_required` with explicit reasons. The
default is refusal: eligibility has to be argued for item by item, and one unsatisfied condition
ends it. Every blocker is reported, not the first, so the record explains the whole distance
between a draft and eligibility.

Eligibility for a specific job application requires all of:

| Group | Conditions |
| --- | --- |
| Policy | intent is automatable at all |
| Configuration | mode is `v2_auto`; intent is allowlisted; send path ready |
| Profile | an approved profile revision is active |
| Match | `strong_match`; clear engineering family; complete description; no matcher blocker (unresolved mandatory requirement, eligibility condition, preference conflict); ≥2 capabilities evidenced by requirement excerpts |
| Plan | plan selected; no stale input; pinned opportunity revision still current; vacancy not closed |
| Review | composition `reviewed`; no deterministic finding; semantic review complete and passed; **no semantic finding of any severity above info**; content hash present |
| Provenance | revision not hand-edited |
| Recipient | pinned; not bad; a real human mailbox; recruiting or engineering function, or an employer-published application address |
| Suppression | address and domain clear |
| Volume | no open conversation with the company; automation cap configured and not reached |
| Delivery state | no unresolved send attempt |
| Approval | no existing manual approval |
| Budget | AI request budget available |

Nothing derives eligibility from a numerical score, a company score, a legacy segment, the absence
of findings alone, or the existence of an email address.

**The matcher's own blockers are carried through rather than re-implemented** — an
`unresolved_mandatory_requirement` becomes `match_unresolved_mandatory_requirement`. One source of
truth, and the operator sees the same wording in both places rather than two rules that can drift.

---

## 3. Per-intent behaviour

| Intent | Automatable | Why |
| --- | --- | --- |
| `specific_job_application` | **Yes, if every gate passes** | Anchored to an advertised role the matcher verified against approved profile facts. "Is this truthful and appropriate" reduces to checks already enforced. |
| `speculative_job_enquiry` | **Never** | No advertised role to anchor it. Correctness depends on company-relevance evidence with real provenance and an identified recipient; the live corpus supports neither. |
| `service_outreach` | **Never** | It asserts a company has an engineering need. Inventing that from weak signals was the previous pipeline's worst failure, and no automated check establishes it. |
| follow-ups (any intent) | **Never** | Reply state may be incomplete, asynchronous bounces are still manual, and intent continuity matters. The runner only ever requests `KindFirstContact`. |

This is a property of the **policy**, not of configuration. `IntentCapable` returns false for the
other two, and `ParseAllowlist` **refuses them at startup** — so naming one is a loud
configuration error rather than a silent per-message no-op. A test asserts the runner source
contains no reference to follow-up sending.

---

## 4. Years of experience (§4)

Unchanged, deliberately. Neither the profile nor the matcher was touched.

A posting with an explicit mandatory years-of-experience requirement that approved profile facts
do not support produces `unresolved_mandatory_requirement`, which prevents `strong_match`, which
prevents automation. Nothing infers experience from project age, repository dates, employment
history or number of systems built. If years-of-experience facts are ever approved, that belongs
in a new profile revision and flows through normally.

---

## 5. Allowlist semantics (§8)

`AUTO_SEND_INTENTS` is a comma-separated list of exact intent names.

- Empty (the default) → no automatic sends, ever.
- `all`, `*`, `any` → **refused**. No wildcard behaviour exists.
- An unknown value → **refused**, stopping startup.
- A valid but non-automatable intent → **refused**, with the reason.
- Duplicates collapse; case and whitespace are normalized.

Being in the allowlist is **necessary but never sufficient**. `TestConfigurationAloneCannotAuthorize`
sets a maximally permissive configuration against a `possible_match` and asserts refusal.

`AUTO_SEND_DAILY_CAP` defaults to 1 and refuses values above 5 — this is for controlled
observation, and a cap of 50 is far likelier to be a mistake than an intention.

---

## 6. `AutomaticSendingEnabled` and rollout mode (§9, §10)

The unconditional `false` is gone, replaced by `send.EvaluateAutomation`, which computes whether
selective automation is **configured** — meaning "the policy may be consulted", never "eligible
drafts get sent". It requires all of: mode `v2_auto`, a non-empty valid allowlist, schema ≥ 25,
a ready send path, and a configured cap. It reports why not.

Modes are now `disabled` → `v2_review` → `v2_manual_send` → `v2_auto`, each a superset of the
last. The send preflight permits transmission in the two sending modes; entering `v2_auto` makes
speculative, service and follow-up messages no more automatic than before, because the policy
refuses them regardless.

No configuration bypasses per-message eligibility. The automation worker refuses to start at all
unless automation is configured, so an unconfigured deployment runs no automation code rather than
a loop that always declines.

---

## 7. Automation decision record (§11)

Migration `0025_automation_decisions.sql` adds `automation_decision` and `automation_claim`.

Every evaluation is persisted, **eligible or not** — recording the refusals is as much the point
as recording the approvals, because "why did this stay in review" has to be answerable months
later. Each row stores: outcome, policy version, intent, content hash, profile revision,
opportunity id and revision, plan id and revision, recipient contact and address, semantic status
and hash, the rollout mode, the allowlist and cap in force, the full reasons and blockers, and the
send attempt it produced if any.

A decision is immutable. The `protect_automation_decision` trigger refuses every edit and every
delete; the single permitted mutation is attaching the resulting `send_attempt_id`, once.
Re-evaluating writes a **new row**, which is what makes "this became eligible only after the
opportunity was re-crawled" readable.

---

## 8. Re-evaluation immediately before sending (§12)

`AutoSend` evaluates, authorizes, then **re-evaluates against freshly read state** before
transmission. This is not belt-and-braces: between the snapshot and the reservation the recipient
can be marked bad, the vacancy can close, the profile can change, an address can be suppressed, or
another send can consume the cap.

If the re-check fails, the authorization is revoked, the refusal is recorded as its own decision,
and nothing is transmitted. If the send preflight then refuses anyway — it is the final authority
— the authorization is revoked too, so an abandoned automatic send never leaves a live approval
behind. `TestRecipientGoingBadBeforeSendStopsAutomation` asserts exactly that, including that no
approval remains.

---

## 9. Automation queue and claims (§13)

`AutomationCandidates` is deliberately narrow: reviewed v2 compositions with no live approval, no
blocking send attempt, and no held claim. It never walks company status or any legacy queue.

`automation_claim` is a durable, expiring claim keyed by draft, so two workers cannot process the
same draft; an abandoned claim expires rather than blocking the draft for good. The loop lives in
`internal/worker/automation.go`, polling every 15 minutes by default — slow on purpose, since the
cap is one send a day and polling faster would only re-decide the same refusals.

The loop **halts entirely** when any send attempt is unresolved, and sends at most one message per
tick so a misconfiguration cannot empty the queue in a single pass.

---

## 10. AI budget (§14)

Automation shares the existing budget rather than having an allowance of its own:
`BudgetAvailable` comes from `aibudget.NextAvailable`, and an exhausted budget produces
`budget_unavailable` carrying the real reason. Capacity is never inferred from the number of API
keys. The dashboard shows the configured budget, compositions queued/running/review-required/
reviewed/**failed**, and provider calls in the last 24 hours, from the real accounting table.

---

## 11. Conservative volume and per-company safety (§15, §16)

The automation cap is separate from the manual `DAILY_SEND_LIMIT` (20) and defaults to **1 per
day**. It is counted from send attempts linked to an eligible decision rather than from a counter,
so a crash cannot lose the count and let the cap be exceeded.

Multiple strong opportunities at one company cannot produce multiple automatic first contacts:
`domain_already_in_conversation` blocks a second one once any thread is open. The other
opportunities stay visible and remain manually actionable.

---

## 12. Manual edits and recipient quality (§17, §18, §19)

**A hand-edited revision is permanently outside automation.** Automation is justified by measured
composer and reviewer behaviour; hand-written text has no such measurement behind it. A later
regenerated, untouched revision qualifies on its own.

**Recipient quality** is two independent questions. `ClassifyContact` asks who an address reaches;
new `ClassifyEndpoint` asks whether it is a mailbox for humans at all — telemetry ingestion,
no-reply, ticketing and alerting endpoints, and long opaque machine keys. Automation requires
both: a contactable endpoint *and* a recruiting or engineering function (or an employer-published
application address). Even a source-published address is refused if it is not a human mailbox.

**Contact cleanup.** Phase 6 found one Sentry ingest address. The structural scan found
**nineteen** across Zeraki, Savannah Informatics, Samsara, BM Security and Centerwill — Sentry and
`sentry.wixpress.com` ingest keys scraped from pages and stored as contacts. All nineteen were
marked `bad` with the reason that identified them. **None was deleted**: the row is part of what
research found, and removing it only means the next crawl rediscovers it with no memory of the
judgement. `prospector-migrate -command contact-endpoints` reports; `-apply` records; a re-run now
reports zero.

---

## 13. Opportunities pagination (§20)

`/opportunities` silently stopped at 200 rows, so 73 of 273 were unreachable by ordinary
navigation. It now has real pagination: a true total count, 50 per page, page links that **carry
the active filters**, and ordering by last change with the id as a tiebreak so a row cannot fall
between two pages or appear on both.

---

## 14. Real-data automation rehearsal (§23)

`prospector-rehearse` now funnels the whole corpus through the automation gates decidable from
stored facts. It writes nothing, activates nothing and calls no AI.

| Gate | Surviving |
| --- | --- |
| Opportunities considered | **273** |
| Reach `strong_match` | **6** |
| Survive recipient quality | **0** |
| Survive all deterministic gates | **0** |
| Would still need composition and review | **0** |

What blocks the rest:

| Reason | Count |
| --- | --- |
| `not_a_strong_match` | 267 |
| `no_contact_on_file` | 4 |
| `recipient_generic_mailbox` | 2 |

So of the six strong matches, four companies have no contact at all and two have only a generic
mailbox. **The binding constraint is contact data quality, not the policy.** Loosening the policy
would not produce a single additional candidate; finding one identified engineering or recruiting
contact at one of those six companies would produce one.

This is also a live check on the earlier years-of-experience question: it is not what is keeping
the number at zero.

---

## 15. Measured real-draft requirement (§24)

**Not performed, and it is a precondition rather than an oversight.** It requires an active
approved profile, and none is active — activating one is explicitly not part of this phase (§22).
No real strong-match draft has been composed, so wrong-role rate, unsupported-claim rate, payment
leakage, reviewer false positives and manual-edit frequency are **unmeasured on real strong
matches**.

Composer and reviewer behaviour is covered by the Phase 4 evaluation and by adversarial fixtures,
but that is not the same evidence. Until this sample exists and is clean, no intent should be
enabled — which is also moot today, since zero candidates exist.

---

## 16. Unresolved attempts (§21)

Not implemented; designed and written down. `docs/RECONCILIATION_DESIGN.md` specifies a
`send_reconciliation` record plus a narrow `reconciled_not_delivered` marker that lets the
duplicate-guard index respect an established non-delivery — without ever rewriting an attempt's
state, content, recipient or timestamps.

It was not implemented because it changes the Phase 5 send-state invariants and the live-key index
predicate, both of which were reviewed and approved separately. That deserves its own review
rather than being slipped into an automation phase.

Meanwhile the document states the operator recovery procedure, and **automation halts entirely
whenever any attempt is unresolved** — `unresolved_send_attempt` in the policy, plus a hard stop
in the worker loop before it looks at any candidate.

---

## 17. Dashboard (§26)

The v2 panel now shows: rollout mode, whether automation is configured **and why not**, the
allowlist, the automation cap, auto-eligible and **auto-blocked** counts, automatic sends today,
manual sends today, drafts excluded by hand-editing, attempts needing reconciliation, and an
aggregated breakdown of what is blocking automation.

Blocked cases are never hidden — they are the evidence for whether automation is held back by the
data or by the policy, which on live data it plainly is by the data.

---

## 18. Tests and results

**53 new tests** (28 policy, 12 dispatch integration, 9 web, 4 endpoint classification). Full
suite green across 22 packages.

Acceptance coverage against §27:

| | Case | Test | Result |
| --- | --- | --- | --- |
| A | Possible match | `TestPossibleMatchIsNeverEligible` | `not_a_strong_match` |
| B | Unrelated role | `TestUnrelatedRoleIsNeverEligible` | matcher blocker carried through |
| C | Strong + generic mailbox | `TestRecipientQualityGates` | `recipient_generic_mailbox` |
| D | Strong + identified contact | `TestFullyCleanApplicationIsEligible`, `TestEligibleApplicationSendsAutomatically` | eligible; sends exactly once |
| E | Strong + manual edit | `TestHumanEditedRevisionIsNeverEligible`, `TestHandEditedRevisionIsNotAutomated` | `human_edited_revision` |
| F | Strong + semantic warning | `TestSemanticFindingsEndAutomation` | any severity above info blocks |
| G | Stale opportunity | `TestStaleAndChangedOpportunityEndAutomation` | stale, moved and closed all block |
| H | Suppressed address | `TestSuppressionEndsAutomation`, `TestSuppressionBeforeSendStopsAutomation` | blocked, nothing transmitted |
| I | Unresolved send attempt | `TestUnresolvedAttemptHaltsAutomation` | `unresolved_send_attempt` |
| J | Speculative enquiry | `TestSpeculativeAndServiceAreNeverAutomatable` | refused even when allowlisted |
| K | Service outreach | same | refused even when allowlisted |
| L | Follow-up | `TestAutomationNeverSendsFollowups` | runner cannot request one |
| M | Two strong jobs, one company | `TestOneAutomaticFirstContactPerCompany` | one thread only |
| N | Unknown `AUTO_SEND_INTENTS` | `TestAllowlistParsing`, `TestUnknownAutoSendIntentIsAConfigurationError` | configuration error, stops startup |
| O | Empty allowlist | `TestEmptyAllowlistBlocksEverything`, `TestEmptyAllowlistSendsNothing` | zero candidates, zero provider calls |
| P | Cap reached | `TestAutomationCapBlocksFurtherSends` | blocked; a zero cap is "unconfigured", not "unlimited" |
| Q | Concurrent workers | `TestConcurrentAutomationWorkersClaimOnce` | at most one transmission |
| R | Recipient goes bad before send | `TestRecipientGoingBadBeforeSendStopsAutomation` | blocked; no approval left behind |
| S | Opportunity closes | `TestStaleAndChangedOpportunityEndAutomation` | `opportunity_closed` |
| T | Profile revision changes | `TestProfileMustBeActive` | `profile_not_active` |
| U | Sentry/telemetry contact | `TestSentryIngestAddressIsNotContactable`, `TestRecipientQualityGates` | `recipient_not_a_human_mailbox` |
| V | Manually approved draft | `TestManuallyApprovedDraftStaysManual`, `TestManuallyApprovedDraftIsNotAutomated` | stays manual; approval untouched |
| W | Decision audit | `TestAutomationDecisionIsPersistedWithReasons`, `TestAutomationDecisionIsImmutable` | full record; immutable |
| X | Unresolved uncertain delivery | `TestUnresolvedAttemptHaltsAutomation` + worker hard stop | automation halts |

Two findings the tests caught during development, both real:

1. **The snapshot stores a raw role string, not a classification.** The policy was comparing it
   against `relevance.Function*` constants, so every contact fell through to "inappropriate" —
   safe, but wrong for the wrong reason, and it would have masked genuine recipient bugs. The
   function is now derived from the live contact exactly as the recipient ranker derives it.
2. **The send preflight hardcoded manual-send mode**, so an automation-authorized message was
   refused with `rollout_mode_forbids_sending`. Both sending modes now reach the gate; who
   authorized the message is recorded in the approval, which is where it belongs.

`TestAllBlockersAreReported` asserts a broken draft reports all six of its problems rather than
the first, and `TestFullyCleanApplicationIsEligible` is the control that keeps every "not
eligible" assertion meaningful.

---

## 19. Migration and live-data impact

`0025_automation_decisions.sql` applied via the documented procedure: `inspect` →
`backup-verify` → `apply -proof` → `check` → `inspect`.

- New tables: `automation_decision`, `automation_claim`. Removed: none.
- **Row-count changes in pre-existing tables: none.**
- All seven preservation digests over historical outbound content unchanged.
- Ledger: 16 (baseline), 17–25 applied.

One deliberate live-data change, requested by §19: **19 contacts had their status set to `bad`**
with the reason that identified them as telemetry endpoints. Addresses, ids and every other field
are untouched, nothing was deleted, and `ReactivateContact` reverses it. This does not appear in
the preservation digests because those cover address and identity, not status — stated here rather
than left for a reader to notice.

No email was sent, no profile was activated, no AI provider was called.

---

## 20. Explicit answers

**1. Which intents are technically capable of automatic sending?**
Only `specific_job_application`. Speculative enquiry and service outreach are refused by the
policy itself, and by `ParseAllowlist` at startup. Follow-ups of every intent are refused.

**2. Which intents are actually enabled by default?**
None. `AUTO_SEND_INTENTS` is empty and the mode is `v2_review`.

**3. Can a possible match auto-send?**
No. `not_a_strong_match`. A possible match is precisely the case a person has to judge.

**4. Can a generic/unknown recipient auto-send?**
No. Generic mailbox, unknown function, inappropriate function and non-human endpoints each block.
Only a recruiting or engineering contact, or an employer-published application address that is
also a human mailbox, is accepted.

**5. Can a manually edited draft auto-send?**
No, permanently. A later regenerated untouched revision qualifies on its own.

**6. Can a stale draft auto-send?**
No. Any stale reason blocks, as does a superseded opportunity revision or a closed vacancy.

**7. Can speculative enquiry auto-send?**
No — regardless of the allowlist, which will not even accept it.

**8. Can service outreach auto-send?**
No, on the same terms.

**9. Can any follow-up auto-send?**
No. The runner only ever requests first contact, and a test enforces it.

**10. Can a payment-related project cause automation where payment need is not evidenced?**
No. Automation adds no content decision: proof selection is the Phase 3 selector's, bounded by the
approved envelope, and checkout presence, payment acceptance and generic technology detection
contribute nothing. Automation only ever refuses what review already accepted.

**11. Can multiple opportunities at one company create multiple automatic first contacts?**
No. `domain_already_in_conversation` blocks a second one.

**12. What happens when the automation cap is reached?**
`automation_cap_reached`; further automatic sends are blocked until the next local day. Manual
sending keeps its own separate Phase 5 limits. A cap of zero is treated as unconfigured, not
unlimited.

**13. What happens if the recipient/opportunity/profile changes between eligibility and
transmission?**
The re-check immediately before transmission catches it, the authorization is revoked, the refusal
is recorded as its own decision, and nothing is sent. The send preflight is a second, independent
catch.

**14. Are automation decisions auditable?**
Yes. Every decision is persisted with its outcome, policy version, pinned inputs, rollout
configuration, full reasons and blockers, and the send attempt it produced. Rows are immutable;
re-evaluating appends.

**15. How many live opportunities would currently qualify under the rehearsal?**
**Zero of 273.** Six reach strong match; four of those companies have no contact on file and two
have only a generic mailbox.

**16. What data-quality issues most often prevent eligibility?**
Contact data. Beyond the 267 that are not strong matches, every remaining candidate fails on the
recipient: no contact at all, or a generic mailbox. Nineteen stored contacts were telemetry
endpoints, now marked. Missing and unidentifiable contacts are the binding constraint.

**17. Is automation still OFF after implementation unless explicitly enabled?**
Yes. `TestAutomationIsOffAfterImplementation` asserts the shipped configuration enables nothing,
and the worker refuses to start.

**18. What exact action would be required to enable the first intent?**
Four deliberate steps, in order: activate an approved candidate profile; compose and inspect a
small real strong-match sample and measure its quality (§15); obtain at least one strong match
with an acceptable recipient, which today does not exist; then set
`EMAIL_PIPELINE_MODE=v2_auto` and `AUTO_SEND_INTENTS=specific_job_application`, leaving
`AUTO_SEND_DAILY_CAP=1`. Steps 1–3 are the substantive ones; step 4 alone would change nothing.

---

## 21. Defaults

| Setting | Default | Effect |
| --- | --- | --- |
| `EMAIL_PIPELINE_MODE` | `v2_review` | No sending of any kind |
| `AUTO_SEND_INTENTS` | empty | No automatic sends |
| `AUTO_SEND_DAILY_CAP` | 1 | One automatic send per day if ever enabled |
| Automation poll interval | 15 minutes | Loop does not start unless configured |
| Sends per tick | 1 | A misconfiguration cannot drain the queue |

---

## 22. Deviations and known limits

1. **No measured real-draft sample** (§24). Blocked on an active profile, which §22 forbids
   activating here. This is the most important outstanding precondition.
2. **Reconciliation designed, not implemented** (§21). It changes approved Phase 5 invariants.
3. **Automation authorizes via a `send_approval` row** attributed to `automation:auto-v1`, rather
   than a separate authorization type. This keeps one authorization record and one content-pinning
   mechanism; the trade-off is that "approval" now has two possible actors, which the actor string
   and the linked decision make explicit.
4. **The policy refuses any semantic finding**, including review-severity. Stricter than §3 (which
   says no `review` or `block` finding) — deliberately, since a finding is the reviewer expressing
   uncertainty.
5. **Zero live candidates**, so the end-to-end automatic path is proven only on synthetic
   fixtures against a real schema, never on real data.
6. **`AUTO_SEND_DAILY_CAP` refuses values above 5.** A deliberate hard ceiling not in the brief;
   raising it is a code change.
7. **The telemetry classifier is list-and-heuristic based.** It caught 19 live cases with no false
   positives in the corpus, but a new vendor or an unusual local part could slip past. It fails
   towards review, never towards sending.
8. **No follow-up scheduling** still exists, as in Phase 6.

---

## 23. What I would check first in review

1. **The rehearsal numbers in §14**, and whether "the constraint is contact data, not policy"
   matches your sense of the data. It is the finding that should drive what happens next.
2. **`internal/automation/policy.go`** — one file, and every claim in this report reduces to
   whether its conditions are right and complete.
3. **The two bugs in §18.** Both were caught by tests, but both are the kind that fail safe and so
   could have hidden real problems for a long time.
