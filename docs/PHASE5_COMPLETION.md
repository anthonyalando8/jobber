# Phase 5 completion: guarded sending and delivery execution

Status: implemented, tested against an isolated PostgreSQL instance, and migrated on the
configured database. **No email has been sent.** Automatic outreach remains disabled with no
setting that turns it on. Awaiting review before Phase 6.

The Phase 5 success criterion was: *a reviewed and explicitly approved v2 draft can be sent
exactly once through one canonical path, with recipient, intent and content frozen; failures,
retries, suppression and uncertain delivery cannot silently create duplicate or inappropriate
email; and every send remains auditable.* Section 22 states where this is met and where the
honest limits are.

---

## 1. Architecture

Four layers, each with one job:

| Layer | Package | Responsibility |
| --- | --- | --- |
| Policy | `internal/send` | Decides. Pure functions over a supplied snapshot; touches no database and no network. |
| Transport | `internal/send` (`SMTPTransport`) | Talks to a mail server and reports *what stage it reached*. Makes no policy decision. |
| Persistence | `internal/store` (`sendattempt.go`, `thread.go`) | Reserves, settles, and refuses to forget. Invariants enforced by the database, not by Go. |
| Orchestration | `internal/dispatch` | The only code permitted to call a transport. Loads one consistent view, evaluates, reserves, transmits, records. |

`internal/web` holds no send logic at all. It renders what `dispatch` reports and submits
operator intent back to it.

The decisive design choice is that **policy is a pure function of an explicit snapshot**.
`send.Evaluate(Preflight) Decision` reads no database. Everything it is allowed to consider is a
field on its input struct, gathered once by `dispatch.load`. A check cannot silently disagree
with another check because it happened to read the database a moment later, and the entire gate
can be tested without a database at all.

---

## 2. Migrations and schema

One additive migration, `internal/store/migrations/0023_send_execution.sql` (183 lines), adding
four tables and no changes to any existing table, column, or row.

- **`send_approval`** — the explicit human authorization. Pins draft, composition revision,
  content hash, snapshot input hash, recipient contact and address, and intent.
- **`outbound_thread`** — one conversation per accepted first contact. Holds the frozen intent,
  the recipient, the root message id, the follow-up step and the thread state.
- **`send_attempt`** — one row per attempt, carrying the idempotency key, the full transmitted
  subject and body, every pinned revision, the initiator, the provider verdict and the stage
  reached.
- **`thread_event`** — append-only record of what happened after acceptance.

Invariants are database objects rather than application conventions:

```sql
CREATE UNIQUE INDEX send_attempt_live_key ON send_attempt(idempotency_key)
 WHERE state IN ('reserved','sending','accepted','uncertain','rejected');
```

That partial index is the duplicate guard. It permits a new attempt after a `transient_failed`
or `cancelled` attempt, and makes a concurrent second reservation of a live message impossible
at the storage layer. `send_approval_live` (unique on `draft_id` where `revoked_at IS NULL`)
allows at most one live approval per draft. `outbound_thread_active_recipient` allows at most
one active thread per contact.

Four triggers — `protect_send_approval`, `protect_outbound_thread`, `protect_send_attempt`,
`protect_thread_event` — refuse the updates and deletes that would destroy the audit trail. A
settled attempt cannot be rewritten or deleted at all.

`internal/store/schema_v2.go` refuses to start the application when any of this is missing:
column specs for the four tables, the four triggers, the foreign keys, and explicit
`requirePartialUniqueIndex` checks for all three partial unique indexes. A binary that expects
the duplicate guard will not run against a database that lacks it.

---

## 3. The canonical send path

`dispatch.Service.Send` is the only function in the application that calls a mail transport.

```
load one consistent view
  └─ send.Evaluate(preflight)          ← the gate; no argument disables a check
       ├─ refused → return the blockers, transmit nothing
       └─ allowed
            ├─ ReserveSend             ← durable row BEFORE any network contact
            │    └─ unique violation → another request owns this message; stop
            ├─ MarkSending
            ├─ transport.Send          ← the only external transmission
            ├─ SettleSend              ← records the verdict and the stage reached
            └─ accepted → OpenThread / RecordThreadSend
```

`dispatch.Request` carries `DraftID`, `Initiator`, `Kind`, `FollowupStep`, `ThreadID` — and
deliberately **no content, no recipient and no intent**. All three are read from the approved
composition. A caller cannot smuggle in a different message, a different address, or a different
purpose, which is precisely what the legacy route allowed through its `toOverride` parameter.

`Service.Preview` runs the same evaluation without transmitting, so the UI shows the real
decision rather than a guess about it.

---

## 4. Send preflight rules

`send.Evaluate` applies roughly twenty checks in one pass and returns *every* blocker, not the
first. Each is an `explain.Reason{Code, Text}` so the interface can state the actual cause.

| Group | Blockers |
| --- | --- |
| Identity | `invalid_send_identity` |
| Readiness | `not_ready`, `rollout_mode_forbids_sending` |
| Composition | `no_composition`, `not_reviewed`, `deterministic_validation_failed`, `semantic_review_failed`, `revision_superseded`, `stale_inputs` |
| Approval | `not_approved`, `approval_revoked`, `approval_content_mismatch`, `approval_recipient_mismatch`, `approval_intent_mismatch` |
| Plan | `plan_not_selected` |
| Recipient | `recipient_missing`, `recipient_changed`, `recipient_address_changed`, `recipient_marked_bad`, `recipient_company_mismatch`, `recipient_address_invalid` |
| Suppression | `address_suppressed`, `domain_suppressed` |
| Prior attempts | `already_accepted`, `delivery_uncertain`, `already_rejected`, `send_in_flight`, `attempt_limit_reached` |
| Volume | `daily_cap_reached`, `min_interval_not_elapsed`, `domain_already_in_conversation`, `domain_cooldown` |
| Follow-up | `no_thread`, `thread_not_active`, `followup_intent_mismatch`, `followup_recipient_mismatch`, `followup_step_out_of_order`, `followup_limit_reached`, `followup_cooldown` |

Default limits (`send.DefaultLimits`): daily cap 20, one active thread per domain, 90-second
minimum interval, 30-day first-contact cooldown per domain, at most 3 attempts per idempotency
key, at most 2 follow-ups per thread, 3-day follow-up cooldown.

Both validation layers are read from the **same** attempt record, so a later clean semantic
review cannot erase an earlier deterministic finding. Passing requires
`!composition.NeedsReview(validation) && review.Passed && review.Complete`.

---

## 5. Approval model

Three distinct states, and the distance between them is the point:

```
reviewed  ≠  approved_to_send  ≠  sent
```

`reviewed` is what the deterministic validators and the semantic reviewer concluded about the
text. It is a property of the email. `approved_to_send` is a person saying *send this one*,
recorded with who and when. `sent` is a provider having accepted it.

`store.ApproveForSend` refuses unless the draft's status is `reviewed`, and verifies that the
submitted content hash equals the hash of the stored current revision — so an operator cannot
approve bytes they did not read, and a draft that changed underneath the page is refused rather
than silently approved. Approval pins content hash, snapshot input hash, recipient contact id,
recipient address and intent. Preflight re-compares all of them at send time.

`RevokeApproval` is available at any time before transmission and blocks sending immediately.

---

## 6. Idempotency key semantics

```go
parts := []string{"send-v1", draftID, compositionRevision, contentHash,
    recipientContactID, lower(trim(recipientEmail)), planID, planRevision,
    intent, kind, followupStep}
key := sha256(join(parts, "|"))
```

The key is the identity of *a logical message*, not of a request. Two requests describing the
same message produce the same key; anything that makes it a materially different message —
different content, recipient, plan revision, intent, or follow-up step — produces a different
one. Address casing and surrounding whitespace are normalized so that `Person@Example.test` and
`  person@example.test ` cannot become two messages.

This is verified directly in `TestIdempotencyKeySemantics`, which asserts both stability and
that each of six mutations changes the key.

---

## 7. Concurrency behavior

Reservation happens **before** the network, in the database, guarded by
`send_attempt_live_key`. Two concurrent requests race to insert; PostgreSQL lets exactly one
win. The loser receives a `23505` unique violation on that specific index, which
`store.ReserveSend` maps to `ErrAlreadyReserved`, and `dispatch.Send` converts to a
`send_in_flight` refusal that attaches to the winning attempt instead of transmitting.

`TestConcurrentSendsTransmitOnce` runs two goroutines released simultaneously against one
approved draft and asserts the fake transport was called **exactly once**, with one outcome
marked sent and one marked resolved.

The window between reservation and transmission is the one place a crash can lose information —
but it loses it in the safe direction: the durable row already exists, in state `reserved` or
`sending`, and both are blocking states. A crashed attempt therefore blocks further sends until
a person resolves it, rather than being quietly retried.

---

## 8. Uncertain delivery

This is the case the whole transport design exists to serve. `SMTPTransport` drives the
conversation step by step and records the stage reached:

```
dial → greeting → starttls → auth → mail_from → rcpt_to → data_open → data_write → data_terminate → quit
```

Everything before `data_terminate` **cannot** have been accepted: the terminating dot was never
written, so the server had nothing to commit. Those failures are definite and classified as
transient or rejected accordingly.

At `data_terminate` — `writer.Close()`, which writes the dot and reads the verdict — the outcome
divides:

- The server answered with a code: authoritative. ≥500 is a rejection, otherwise transient.
- **No answer arrived**: the server may already have queued the message. This is
  `OutcomeUncertain` / `ErrorTimeoutUncertain`, and the attempt is stored in state `uncertain`.

An `uncertain` attempt is a blocking state. It opens no thread, is never retried automatically,
and every later send of that key is refused with `delivery_uncertain`. `Retryable()` returns
false for it. Resolution requires a person to establish what actually happened and record it as
a thread event — the system will not guess.

`TestSMTPHangupAtCommitPointIsUncertain` proves the classification against a scripted SMTP
server that hangs up exactly at the commit point, and
`TestSMTPFailuresBeforeCommitAreDefinitelyNotSent` proves five earlier-stage failures are never
reported as uncertain.

---

## 9. Provider result model

`send.Result` carries `Provider`, `Outcome`, `Stage`, `ErrorCategory`, `Response`,
`ProviderMessageID`, `AcceptedAt`. `Result.State()` maps outcome to the stored attempt state.

Outcomes: `accepted`, `rejected`, `transient`, `uncertain`, `config_error`.
Error categories: `connection`, `transient`, `rate_limit`, `recipient`, `provider_reject`,
`auth_or_config`, `timeout_after_possible_acceptance`.

Attempt states: `reserved`, `sending`, `accepted`, `rejected`, `transient_failed`, `uncertain`,
`cancelled`. `BlockingState()` identifies the four that prevent another send.

The `Transport` interface is `Name() string` and `Send(Context, Message) Result`. Adding a
provider is implementing two methods; no policy moves with it. Nothing in the interface lets a
provider decide whether it should have been called.

---

## 10. Retry rules

`send.Retryable(category)` is true only for `connection`, `transient` and `rate_limit` — the
categories that can only arise **before** the commit point.

Not retryable: `recipient` (the address is wrong; retrying is pointless and looks like
harassment), `provider_reject` (the server said no), `auth_or_config` (a broken credential; a
retry loop achieves nothing and can lock the account), and `timeout_after_possible_acceptance`
(retrying may duplicate a delivered message).

Retries are bounded regardless of category: `MaxAttemptsPerKey` is 3, counted per idempotency
key, after which `attempt_limit_reached` blocks permanently. There is no backoff loop anywhere,
because there is no automatic retry anywhere — a retry is always a person pressing send again,
and the gate re-evaluates from scratch each time.

`TestAuthFailureDoesNotRetryStorm` presses send five times against a server that always rejects
the credentials and asserts the provider was contacted no more than three times.

---

## 11. Suppression

Suppression is checked **at send time**, against the live tables, not at approval time. Approval
cannot outlive a later suppression: `TestSuppressionAfterApprovalBlocks` approves first, then
suppresses, then asserts the send is refused and the transport was never called.

Both the address and the company domain are checked. Contact status is checked independently —
a contact marked `bad` blocks with `recipient_marked_bad` even if no suppression row exists.

A permanent bounce recorded on a thread marks the contact bad **and** inserts a suppression row
in the same transaction, so one recorded fact closes every future route to that address.

---

## 12. Thread model

One thread per accepted first contact, created only from an `accepted` attempt — never from a
reservation, a failure, or an uncertain outcome. The thread freezes:

- the **intent**, which can never change for the life of the conversation;
- the recipient contact and address;
- the root message id, used for `In-Reply-To` and `References` on follow-ups;
- the company, plan and plan revision;
- the follow-up step and the last send time.

Thread states: `active`, `replied`, `bounced`, `closed`, `do_not_contact`. `thread_event` is
append-only; `RecordThreadEvent` applies the consequence of each fact within one transaction and
requires every event to declare its source (`operator` or `provider`).

`TestThreadFreezesApprovedIntent` runs all three intents end to end and asserts the stored thread
intent matches the approved intent in each case.

---

## 13. Follow-up foundation

The machinery exists and is gated; **no follow-up can be sent autonomously**.

`evaluateFollowup` requires an existing active thread, identical intent, identical recipient, the
correct next step, the step limit not reached, and the cooldown elapsed. Follow-up composition
and approval reuse the same review and approval path as a first contact: a follow-up is an
approved composition like any other.

`TestFollowupCannotChangeIntent` asserts that an application thread refuses a service-outreach
follow-up with `followup_intent_mismatch`.

What is deliberately **not** built: scheduling. Nothing decides *when* a follow-up is due, and
nothing sends one without a person. That belongs to a later phase.

---

## 14. Resend behavior

There is no resend, in the sense of a button that transmits something again. Pressing send on a
draft whose message was accepted is refused with `already_accepted`. On an uncertain attempt it
is refused with `delivery_uncertain`. On a rejected one, `already_rejected`.

A genuinely new message requires a new composition revision, which produces a different content
hash, which produces a different idempotency key, and which requires fresh review and fresh
approval. That is the only path, and it is a deliberate one rather than a repeated click.

The legacy `/sent/{id}/resend` route returns **410 Gone**.

---

## 15. Bounce and reply modeling, and its limits

Modeled: `replied`, `bounced_permanent`, `bounced_transient`, `closed`, `do_not_contact`,
`reconciled`, `note`. Each updates thread state; permanent bounce and do-not-contact also
suppress the address.

**The honest limitation:** nothing detects these automatically. There is no IMAP polling, no
bounce mailbox parsing, and no provider webhook. Every event above is recorded by a person on
`/threads/{id}`. The data model is ready for automated ingestion — `thread_event.source` already
distinguishes `operator` from `provider` — but no automated source exists yet.

The practical consequence is that a bounce suppresses an address only once someone records it.
SMTP-time rejections (`5xx` at `RCPT TO`) *are* caught automatically, since the server refuses
during the conversation; it is the asynchronous bounce arriving later by email that needs a
person.

---

## 16. User interface

**`/compositions/{id}`** now shows, in one place: the authoritative intent, the pinned recipient,
the advertised title, subject and body, both validation layers, stale-input reasons, the rollout
mode and readiness reasons, the approval state, the full preflight decision, the send history and
the thread link.

The rule from §24 of the brief is implemented literally: **when preflight would fail, there is no
send button at all** — not a disabled one. The form is absent from the rendered HTML, and the
blockers are listed in their own words instead. Three tests assert this
(`TestSendButtonAbsentWhenPreflightWouldFail`, `TestUnapprovedDraftOffersApprovalNotSending`,
`TestUnreadySendPathStatesItsReasons`), each checking for the literal absence of
`action="/compositions/4/send"`.

The approval form carries the content hash of what is displayed, so approval is bound to the
bytes on the page.

**`/threads`** lists conversations; **`/threads/{id}`** shows the frozen intent, state, follow-up
step, full event history, and the form for recording a delivery fact — with the consequences
("suppresses this address") stated on the options that carry them.

---

## 17. Files changed

**New:**

| File | Lines | Purpose |
| --- | --- | --- |
| `internal/send/send.go` | 259 | States, categories, `Identity`/`Key()`, `Transport` interface |
| `internal/send/preflight.go` | 337 | The canonical gate |
| `internal/send/readiness.go` | 81 | Rollout modes, `AutomaticSendingEnabled()` |
| `internal/send/smtp.go` | 246 | Staged SMTP transport |
| `internal/send/smtp_test.go` | 270 | Stage classification tests |
| `internal/dispatch/dispatch.go` | 305 | The one canonical send path |
| `internal/dispatch/dispatch_test.go` | 1077 | Acceptance tests A–Z |
| `internal/dispatch/singlepath_test.go` | 60 | Single-transport invariant |
| `internal/store/sendattempt.go` | 341 | Approvals, reservation, settlement |
| `internal/store/thread.go` | 229 | Threads and events |
| `internal/store/migrations/0023_send_execution.sql` | 183 | The four tables |
| `internal/web/dispatch.go` | 227 | Approve/revoke/send/thread handlers |
| `internal/web/phase5_test.go` | 199 | UI guarantees |
| `internal/web/templates/threads.html`, `thread.html` | 35 | Conversation pages |

**Modified:** `internal/store/db.go` (`SchemaVersion`), `internal/store/schema_v2.go` (v23
gate), `internal/web/handler.go` (interlock transition; legacy transport closed),
`internal/web/followups.go`, `internal/web/resend.go` (410 Gone),
`internal/web/composition.go` (send view), `internal/web/templates/composition.html` (send
panel), `cmd/prospector/main.go` (mode validation, routes, scheduler gate).

---

## 18. Tests and results

42 new tests. All pass.

```
ok  github.com/anthonyalando8/prospector/internal/dispatch   60.3s
ok  github.com/anthonyalando8/prospector/internal/send        0.8s
ok  github.com/anthonyalando8/prospector/internal/web         0.4s
ok  github.com/anthonyalando8/prospector/internal/store      44.2s
... full suite: 20 packages, all ok
```

Every `internal/dispatch` test creates its own randomly named PostgreSQL database from
`template0`, applies all 23 migrations, **asserts `current_database()` matches that name before
any mutation**, and drops it afterwards. The live database is never touched by a test.

Acceptance coverage, mapped to the brief:

| | Case | Test | Result |
| --- | --- | --- | --- |
| A | Reviewed but not approved | `TestReviewedWithoutApprovalCannotSend` | blocked `not_approved`, 0 provider calls |
| B | Approved clean draft | `TestApprovedDraftSendsOnce` | exactly 1 call; content, recipient and intent verified |
| C | Two concurrent requests | `TestConcurrentSendsTransmitOnce` | exactly 1 call; 1 sent, 1 resolved |
| D | Retry after accepted | `TestRetryAfterAcceptedDoesNotTransmitAgain` | blocked `already_accepted` |
| E | Timeout after possible acceptance | `TestUncertainDeliveryBlocksFurtherSends` | state `uncertain`; no thread; no auto-retry |
| F | Definite pre-send failure | `TestConnectionFailureBeforeTransmissionIsRetryable` | one bounded retry, then accepted |
| G | Recipient rejection | `TestRecipientRejectionIsPermanent` | `rejected`, not retryable |
| H | Suppression after approval | `TestSuppressionAfterApprovalBlocks` | blocked `address_suppressed` |
| I | Contact marked bad | `TestContactMarkedBadAfterApprovalBlocks` | blocked `recipient_marked_bad` |
| J | Recipient changed in plan | `TestRecipientChangeBlocksOldDraft` | blocked `stale_inputs`; never redirected |
| K | Profile revision changed | `TestProfileRevisionChangeBlocks` | blocked `stale_inputs` |
| L | Opportunity revision changed | `TestOpportunityRevisionChangeBlocks` | blocked `stale_inputs` |
| M | Edited after approval | `TestNewRevisionAfterApprovalInvalidatesIt` | blocked `approval_content_mismatch` |
| N | Semantic review fails | `TestEitherLayerFailingPreventsApprovalAndSend` | blocked `semantic_review_failed` |
| O | Deterministic fails, review clean | same | blocked `deterministic_validation_failed` |
| P | Two opportunities, one company | `TestSecondFirstContactAtSameDomainIsBlocked` | blocked on domain policy |
| Q/R/S | Each intent | `TestThreadFreezesApprovedIntent` | thread intent matches in all three |
| T | Follow-up intent continuity | `TestFollowupCannotChangeIntent` | blocked `followup_intent_mismatch` |
| U | Accepted then resend | D, plus `TestLegacySendRoutesStayClosed` | no duplicate call; legacy route 410 |
| V | Uncertain then resend | `TestUncertainDeliveryBlocksFurtherSends` | blocked `delivery_uncertain` |
| W | Permanent bounce | `TestPermanentBounceStopsFutureSends` | thread bounced, contact bad, address suppressed |
| X | Auth/config failure | `TestAuthFailureDoesNotRetryStorm` | ≤3 calls, then `attempt_limit_reached` |
| Y | Legacy send route | `TestLegacyRouteAndAutomationRemainClosed`, `TestOnlyTheSendPackageMayReachAMailTransport` | closed, and no second transport exists |
| Z | Scheduler attempt | `TestAutomaticSendingRemainsDisabled` | `AutomaticSendingEnabled()` is false |

Supporting tests: `TestAttemptAuditTrailIsImmutable` (verifies twelve recorded fields, then
proves the database refuses to update the state, rewrite the body, or delete the row),
`TestApprovalLifecycle`, `TestIdempotencyKeySemantics`, `TestReviewModeCannotSend`,
`TestDispatcherWithoutDatabaseIsNotReady`, and the seven SMTP stage tests.

`TestOnlyTheSendPackageMayReachAMailTransport` walks every `.go` file in the repository and
fails if any file outside `internal/send` imports the standard SMTP package. It exists because
the single-path invariant is about what someone might add next year, not about what the code
does today.

No email was sent at any point. No test contacts a real address; every fixture uses
`example.test` and `northwind.example`.

---

## 19. Live-data impact

Migration 0023 was applied to the configured database following the documented procedure:
`inspect` → `backup-verify` (pg_dump, restore into an isolated scratch database, compare schema,
row counts, content digests and sequences) → `apply -proof` → `check` → `inspect`.

Before and after comparison:

- New tables: `send_approval`, `outbound_thread`, `send_attempt`, `thread_event`.
- Removed tables: **none**.
- Row-count changes in pre-existing tables: **none**.
- Ledger: 16 (baseline), 17–23 applied. `check` reports the schema current.

Preserved and untouched: `send_log` (33), `draft` (107), `contact` (771), `company` (596),
`suppression` (1), `candidate_profile_revision` (3), plus all signal, follow-up and company
history.

The four new tables are empty. No approval exists, no attempt exists, no thread exists.

Separately worth stating plainly: the live database has **no active profile revision** (three
revisions, all inactive) and **no contact plans**. Matching therefore answers `profile_required`
there, so nothing on the configured database is currently sendable regardless of rollout mode.
Phase 5 changes what the system is capable of, not what it is currently positioned to do.

---

## 20. Legacy compatibility

The Phase 1–4 construction interlock was a compile-time constant meaning "not yet". It has been
replaced by two separate things:

- `web.LegacySendEnabled = false` — now **permanent**, not a phase gate. The legacy routes
  re-pick the recipient, accept an arbitrary address override, and have no snapshot, approval,
  idempotency key or durable attempt record. They are closed for good.
- `send.EvaluateReadiness` — a computed gate for the v2 path that names exactly which condition
  is unmet (rollout mode, schema version, schema health, transport configuration, sender
  identity, daily cap).

Legacy routes now return **410 Gone** with an explanation pointing at the composition page,
rather than 503 "under construction" which would have implied they were coming back:
`/companies/{id}/send`, `/followups/*/send`, `/sent/{id}/resend`.

`Handler.sendSMTP` no longer transmits. It returns an error unconditionally, and the `net/smtp`
import was removed from `internal/web` entirely, so no file in that package can open an SMTP
conversation. Legacy *data* is fully preserved and still displayed; only legacy *sending* is
gone.

---

## 21. Rollout-mode behavior

| Mode | Behavior |
| --- | --- |
| `disabled` | No sending. Readiness reports the mode as the reason. |
| `v2_review` (default) | Compose, validate, review, inspect. Preflight refuses with `rollout_mode_forbids_sending`; the UI shows no send button. |
| `v2_manual_send` | A person may approve and send one reviewed draft at a time, through every check above. |

`send.AutomaticSendingEnabled()` returns `false` unconditionally — a function body, not a
configuration value. `cmd/prospector/main.go` gates the scheduler on it, so background workers
do not run and cannot initiate contact. There is no mode, environment variable, or flag that
enables automatic sending in Phase 5.

---

## 22. Explicit answers

**1. Can any legacy path send directly?**
No. Legacy routes return 410 Gone; `sendSMTP` refuses unconditionally; `net/smtp` is no longer
imported anywhere outside `internal/send`; and a test walks the source tree to keep it that way.

**2. Can a reviewed but unapproved draft send?**
No. `reviewed` is a property of the text; approval is a separate, attributed act. Preflight
blocks with `not_approved` (test A).

**3. Can two concurrent requests transmit twice?**
No. Reservation precedes transmission and is guarded by a partial unique index in PostgreSQL.
The loser of the race gets a unique violation and attaches to the existing attempt. Verified
with two simultaneous goroutines (test C).

**4. Can an accepted message be resent accidentally?**
No. `already_accepted` blocks it, the legacy resend route is gone, and a genuinely new message
requires a new revision, a new hash, a new key, fresh review and fresh approval.

**5. What happens when provider acceptance is uncertain?**
The attempt is stored as `uncertain` with the stage that produced it. No thread opens, nothing
retries automatically, and every later send of that key is blocked with `delivery_uncertain`
until a person establishes what happened and records it. The system never guesses whether a
message arrived.

**6. Can a suppressed address send because approval happened earlier?**
No. Suppression is evaluated at send time against live data, not captured at approval time
(test H). Domain suppression and contact status are checked the same way.

**7. Can recipient changes reuse the old draft?**
No. The recipient is pinned in the snapshot, in the approval and in the idempotency key.
Changing the plan's recipient marks the composition stale and blocks the send; the old content is
never redirected to a new address (test J).

**8. Can stale profile/opportunity/plan state send?**
No. `CompositionStaleReasons` covers policy, plan, profile, recipient, company, opportunity,
matcher, match, selection and evidence; any of them blocks with `stale_inputs` (tests K, L).

**9. Can a human edit retain old approval?**
No. An edit creates a new revision with a new content hash; the approval pins the old hash and
fails with `approval_content_mismatch`, and the new revision needs review again (test M).

**10. Is intent frozen into the sent thread?**
Yes. Intent is part of the snapshot, the approval, the idempotency key, the attempt row and the
thread row. All three intents are verified end to end (tests Q, R, S).

**11. Can an application follow-up become service outreach?**
No. `followup_intent_mismatch` blocks it; the thread's intent is immutable for the life of the
conversation (test T).

**12. Can provider errors trigger an unbounded retry loop?**
No. There is no automatic retry anywhere in the system. Non-retryable categories are refused
outright, and every key is bounded at 3 attempts regardless of category (test X).

**13. Is every send attempt durably auditable?**
Yes. Each attempt records the idempotency key, transmitted subject and body, content hash,
approval id, profile/plan/opportunity revisions, initiator, policy version, provider, stage,
error category, message id and the reserved/started/settled timestamps. Triggers refuse to
update a settled attempt or delete any attempt — verified by attempting all three and requiring
each to fail.

**14. Are automatic first-contact sends still disabled?**
Yes. `AutomaticSendingEnabled()` returns false unconditionally; the scheduler is gated on it;
no mode enables it.

**15. Are automatic follow-ups still disabled?**
Yes. Follow-up *policy* exists and is enforced, but nothing schedules or sends a follow-up. Each
one needs composition, review, approval and a person.

---

## 23. Deviations and known limits

1. **Bounce and reply detection is manual.** The data model and event vocabulary are complete;
   no automated ingestion exists. An asynchronous bounce suppresses an address only once someone
   records it. SMTP-time rejections are caught automatically.
2. **No follow-up scheduling.** Deliberate — §13.
3. **One transport implemented.** The interface is provider-agnostic and the store is not
   SMTP-specific, but only `SMTPTransport` exists. The abstraction is unproven against a second
   provider.
4. **`senderDomain()` is a placeholder.** It returns `prospector.local` for message-id
   generation regardless of input. Harmless for uniqueness; should become the real sending domain
   before live use.
5. **No real provider smoke test has been run.** Per §29, none was attempted. Before any live
   send, one test to a controlled address you own would be worth doing — the SMTP conversation
   itself has only been exercised against a scripted in-process server.
6. **The uncertain path has no reconciliation tooling.** An operator can record `reconciled` on
   the thread, but nothing helps them determine what actually happened.
7. **`buildEmailMessage` is now unused** by any live path, retained only for its existing test.
8. **Rendered messages are plain text only.** No HTML alternative part, no attachments — the CV
   is `available_on_request`, so nothing needed attaching.

---

## 24. What I would check first in review

The three places where the guarantees are thinnest, in order:

1. **`internal/send/preflight.go`** — the whole gate is here. Every claim above reduces to
   whether these checks are right and complete.
2. **The commit-point classification in `internal/send/smtp.go`** — the boundary between
   "definitely not sent" and "possibly sent" is a single `errors.As` on `textproto.Error`. If
   that is wrong in either direction, either duplicates or false blocks follow.
3. **The reservation/transmission window in `internal/dispatch/dispatch.go`** — a crash between
   `MarkSending` and `SettleSend` leaves an attempt stuck in `sending` with no operator tooling
   to resolve it. It fails safe (it blocks), but it will need a reconciliation path before real
   volume.
