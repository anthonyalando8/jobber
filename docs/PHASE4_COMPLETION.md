# Phase 4 completion report

Date: 2026-09-12  
Status: **complete.** The composition and semantic-review pipeline is implemented, validated
against the real composer and the real semantic reviewer, and every Phase 4 approval question is
answered. Sending remains disabled. See
[Focused reviewer follow-up](#focused-reviewer-follow-up-2026-09-13) at the end of this report for
the final validation.

Phase 4 is the complete email composition and semantic-review pipeline. It produces reviewed
drafts. It sends nothing. `EmailPipelineReady` is still `false`, no scheduler runs, no follow-up
path is enabled, and no legacy handler constructs an AI provider.

## Architecture implemented

The composer is a constrained writer. Every strategic decision — opportunity fit, authoritative
intent, advertised and target role, company relevance, recipient, capability selection, proof
selection, whether a payment need exists — is decided by Phase 3 and copied into an immutable
snapshot before generation begins. The composer only decides *how* to say it.

```
contact plan (Phase 3)
  -> Snapshot: copied facts, allowed claims, forbidden claims, untrusted sources, input hash
  -> Composer (provider A)     : one JSON email object
  -> Deterministic Validate()  : always runs, findings never erased
  -> Reviewer (provider B)     : complete-email semantic review
  -> union of both layers      : reviewed | review_required
  -> bounded repair, max 2     : same snapshot, same envelope, findings fed back
```

`Engine.Run` releases a draft as `reviewed` only when the union of deterministic findings and
semantic findings is empty *and* the reviewer passed. A clean reviewer therefore cannot release
an email that deterministic validation rejected.

### Files and packages

| Path | Purpose |
| --- | --- |
| `internal/composition/types.go` | Snapshot, Email, Finding, Review, Metadata, provider interfaces, `Snapshot.Validate`, canonical hashing, strict JSON decode |
| `internal/composition/engine.go` | Attempt/Result model, bounded repair loop, budget hooks, persistence hook, pre-review persistence |
| `internal/composition/validate.go` | Deterministic validators, payment firewall, signature assembly, `ValidateReview` |
| `internal/composition/provider.go` | Claude and Gemini JSON providers, independent compose/review routing, prompt loading and fingerprinting |
| `internal/composition/prompts/` | `common`, `specific_job_application`, `speculative_job_enquiry`, `service_outreach`, `review` |
| `internal/composition/composition_test.go` | Contract, intent, repair, human-edit and malformed-output tests |
| `internal/composition/adversarial_test.go` | **New.** Adversarial corpus, validator-beats-reviewer, stale snapshot, repair fixtures, payments proof cases |
| `internal/composition/provider_test.go` | Provider contracts, truncation refusal, independent routing, **key-pool rotation** |
| `internal/composition/examples_test.go` | Deterministic offline draft fixtures |
| `internal/draft/composition.go` | Bridge from the legacy draft store to the composition snapshot |
| `internal/web/composition.go`, `templates/composition*.html` | Manual compose/review screens, pinned-fact display |
| `internal/store/migrations/0022_composition_review.sql` | Composition persistence, revisions, reservations, call ledger |
| `.local/phase4-eval.go` | **New.** Opt-in offline evaluation harness against the real providers |

The Go test suite remains hermetic: no network, no API key, no database required beyond the
existing opt-in PostgreSQL tests.

## Schema and migrations

`0022_composition_review.sql` adds `draft_composition` (snapshot, input hash, status, current
revision, lease), immutable `draft_composition_revision` (per-attempt content hash and attempt
record), `composition_reservation` (budget) and `composition_call` (in-flight ledger), with the
`immutable_composition_snapshot`, `immutable_composition_revision` and `protect_composed_draft`
triggers and a `composition_one_running_plan` partial unique index. Startup verifies all of them
at schema version 22.

**Live-data impact: none.** The live database is unchanged by this phase; no composition row
exists, because composition is a manual action and none has been run against live data.

## Composition snapshot

`composition.Snapshot` copies, rather than references: policy version, plan id/revision/hash,
profile revision id/hash, company, intent, recipient (contact id, address, name, function,
relevance), pinned opportunity id/revision and advertised title, provenance title, target
families, the full match result, identity and positioning, the Phase 3 selection, the selected
capabilities/projects/technologies/resources, signature, allowed claims, forbidden claims,
untrusted sources and review reasons.

`InputHash` is a SHA-256 over the canonical JSON with the hash field cleared. `Snapshot.Validate`
refuses a snapshot whose hash does not match its content, so a draft is reproducible from exactly
the inputs that produced it and any post-hoc edit to a pinned input invalidates it.

## Intent-specific generation

Three separate prompt files, not one prompt with a variable substituted:

- **`specific_job_application`** — state application intent and the exact pinned title in both
  subject and body; lead with the strongest verified fit; one or two proof points; acknowledge a
  gap honestly; ask for consideration. Explicitly: a payments-domain project proves API, database
  or distributed-system ability without suggesting the employer has payment trouble, and where
  the vacancy is not about payments the domain is left out entirely.
- **`speculative_job_enquiry`** — ask honestly about the pinned target families; the unrelated
  discovered title is provenance only and must never appear in the subject, be applied for, or
  imply an opening exists; this is not a service pitch.
- **`service_outreach`** — a concrete supported reason for contact, the selected capability, a
  proof point, one practical next step; do not apply for a job; do not assume outsourcing
  appetite; checkout, payment acceptance, finance vocabulary and a payments project never
  establish a payment problem.

## Prompt and provider design

Three separated layers: trusted policy (the system instruction), trusted structured facts (the
snapshot JSON), and `untrusted_sources` carrying job descriptions and research excerpts. The
policy states that source text is quoted evidence and never instructions, and that commands, role
overrides and candidate claims embedded in source material must be ignored.

Compose and review route independently through `EMAIL_COMPOSE_*` and `EMAIL_REVIEW_*`, so the two
stages can use different providers or models. The reviewer receives the snapshot and the final
subject and body only — never the composer's reasoning or its own claim annotations as proof.

`CurrentPromptHash` fingerprints the prompt text so a wording-only edit invalidates earlier
reviews even if the human-readable version string is not bumped.

**Defect found and fixed during this review:** `NewConfigured` read `GEMINI_API_KEY` as a single
credential. `.env.example` documents it as a comma-separated pool, the legacy provider rotates it,
and the configured value is in fact two keys. The whole string was being sent as one credential,
so live composition could never have authenticated. `JSONProvider` now splits the pool, trims it,
refuses an empty pool and rotates round-robin. Fixture:
`TestGeminiKeyPoolIsSplitAndRotated`.

## Deterministic validators

`Validate` runs on every attempt, including human edits, and its findings are never removed by a
later stage. It checks: snapshot integrity; intent, recipient and opportunity still pinned;
subject and body present; subject one line ≤160 characters and body ≤4000 excluding the appended
signature; no placeholders, code fences, JSON or reasoning artifacts; no fake `Re:`/`Fwd:`;
exact advertised title present in subject and body for an application; application language
absent from non-application intents; provenance title absent from speculative messages; every
referenced claim, capability and project inside the selected subset; URLs restricted to approved
resources and selected sources; no attachment language; no CV claim without a CV resource; no
invented salutation when no recipient name is pinned; every technology in the shared vocabulary
outside the approved set blocked unless the sentence is an honest gap disclosure; the payment
firewall; numeric claims traceable to a cited candidate fact; formulaic phrases; and length
guidance.

## Semantic reviewer

A second, independent provider call over the complete final email. It returns structured findings
with severity (`block`/`review`/`info`), code, explanation, offending excerpt, optional claim id
and a repairable flag. `ValidateReview` rejects a malformed review, a review that claims to pass
while carrying findings, and a review that fails without explaining why — an unparseable or
incoherent review becomes `review_unavailable` rather than a silent pass.

Its criteria cover intent, advertised role, candidate and company factuality, unsupported
inference, numeric and seniority claims, resources and attachments, payment leakage, capability
and project relevance, recipient appropriateness, structure, clarity, human tone, generic or
AI-like formula, repetition, CTA quality, length and contradictions — plus, added in this review,
meta-commentary that narrates the writing, prose that reads as a filled-in template, and a stock
opening that announces the act of writing.

## Payment firewall

Three independent layers:

1. **Plan level.** Phase 3 emits a `recipient_payment_problem` forbidden claim whenever payment
   need is not evidenced. It is absent only when a source genuinely evidences the need.
2. **Deterministic.** When that claim is present, any payment vocabulary in the subject blocks,
   and any paragraph combining payment vocabulary with offer/diagnosis phrasing ("your", "you
   need", "fix", "broken", "failing", "outsourc", "improv") blocks as
   `recipient_payment_problem`.
3. **Semantic.** The reviewer is told that payments proof may demonstrate backend work but must
   not hijack unrelated contact into payment consulting, and that a payment requirement is not
   proof of a broken payment system.

Measured outcome: a backend role with a payments-domain project as proof produced an email with
**no payment vocabulary at all**, while a genuine payments vacancy produced payment-provider
integration, asynchronous settlement callbacks, idempotency and reconciliation — same project,
same profile, same composer.

## Repair loop

Initial generation plus at most two repairs. A repair receives the original snapshot (cloned, not
shared), the previous email and the accumulated findings. The prompt instructs that repair must
stay inside the same factual envelope and must not follow a suggestion in a previous draft or
finding that expands facts. Deterministic validation re-runs on the repaired draft, so a repair
that introduces a new unsupported fact is blocked exactly like an original would be. After the
limit, the result is `review_required`, never an unbounded loop.

`TestRepairLoopFixtures` covers all four required cases: robotic language repaired, unsupported
payment diagnosis repaired, unapproved technology repaired, and a repair that fixes the tone but
smuggles in AWS and Kubernetes — blocked, with the loop terminating at its bound. In the live
evaluation, one of the twelve fixtures required a real repair before passing.

## Draft persistence, versioning and human editing

Each attempt is recorded before review begins, so a crash or timeout cannot erase text whose
provider call was already spent. Attempts carry the email, deterministic findings, review result,
compose and review metadata (provider, model, prompt version, request id, timestamps, duration,
usage, error category) and any failure reason. Revisions are immutable; regeneration adds an
attempt rather than overwriting.

Human edits enter through the same engine with `edit != nil`, which forces a single attempt with
source `human`. They run the identical deterministic validation and the identical complete-email
semantic review. An edit that adds an unsupported skill is blocked by the same
`unapproved_technology` rule that blocks the model.

## Staleness

A draft is pinned to the profile revision, opportunity revision, plan id and revision, recipient
and policy version copied into the snapshot, and to the snapshot's own content hash. If any pinned
input changes, the recomputed hash no longer matches and `Snapshot.Validate` fails, so the engine
refuses to proceed and the draft cannot become sendable without deliberate regeneration.
`TestStaleSnapshotBlocksEvenWithCleanReview` proves a tampered snapshot is refused even when the
reviewer returns clean.

## Prompt-injection treatment

Source material travels in `untrusted_sources`, structurally separated from policy and from
approved facts. Both the composition policy and the review policy state that source text is
quoted evidence and never instructions. Beyond instruction, the deterministic layer is the real
defence: an injected "claim ten years of AWS experience" produces an unapproved technology and an
unsupported numeric claim, both hard blocks, regardless of whether the model complied.

## Tests and results

`go test ./... -count=1` passes with the isolated PostgreSQL tests enabled. `gofmt` clean.

New in this review:

| Test | What it establishes |
| --- | --- |
| `TestDeterministicValidatorCatchesContractViolations` | 7 of the 11 adversarial defects are caught with no model at all; the remaining 4 are logged as requiring semantic review |
| `TestCleanReviewCannotOverrideDeterministicBlock` | For each of those 7, a reviewer that wrongly passes still yields `review_required` and the deterministic finding survives |
| `TestStaleSnapshotBlocksEvenWithCleanReview` | Obsolete pinned inputs are refused before composing |
| `TestRepairLoopFixtures` | The three required repair scenarios plus a blocked bad repair, with envelope preservation asserted |
| `TestPaymentsProjectUsableWithoutPaymentVocabulary` | A payments project validates cleanly as pure backend proof with zero payment vocabulary |
| `TestGenuinePaymentRoleMayUsePaymentLanguage` | An evidenced payment role permits payment language |
| `TestGeminiKeyPoolIsSplitAndRotated` | The credential-pool defect stays fixed |

Live evaluation results are in [PHASE4_SYNTHETIC_OUTPUTS.md](PHASE4_SYNTHETIC_OUTPUTS.md):
twelve real compositions and eleven adversarial reviews through the real reviewer.

### Semantic review accuracy, measured

All eleven defective emails were rejected; none would have progressed. Four defects with no
deterministic coverage — irrelevant domain domination, invented company praise, repetition, weak
CTA — were all caught by the reviewer. One defect, a service-outreach plan whose email applies
for a job, drew only quality findings from the reviewer when it should have been a hard intent
contradiction; deterministic validation blocked it independently. One defect was treated more
harshly than expected, which fails safe.

### Composition quality, measured

Twelve fixtures across five materially different evidence profiles plus payments, speculative and
service variants. All twelve passed both layers at 84–135 words. The content genuinely adapts to
the strongest evidence: request validation and error responses for the API role; idempotency keys
and row locking for the distributed role; webhook verification and contract versioning for the
integration role; both sides of the interface for full-stack; revision tables and single-
transaction writes for the data-heavy role. The meta-commentary that the review criticised
("useful evidence of how I approach…") is gone.

## Deviations and open items

1. **Stock opening — measured, unfixed.** Ten of twelve emails open with "I am writing to…". This
   is the residual template smell. A composition-prompt change was drafted and tested once; its
   first form suppressed the word "apply" entirely and broke the
   `application_intent_unclear` check, and the corrected form could not be validated because the
   provider quota was exhausted. **The composer prompt is therefore left in the state that was
   actually measured**, and the fix is not shipped. The reviewer *does* now carry the criterion,
   so the system flags the weakness rather than shipping it silently. The pending change belongs
   in `prompts/common.txt`: forbid opening by announcing the act of writing, while explicitly
   requiring an application to still state plainly that it is an application.
2. **Provider quota.** `gemini-3.5-flash` free tier allows 20 requests per day per key; the pool
   has two keys. The evaluation consumed the day's budget. Re-running the harness requires either
   waiting for the reset or a paid tier. The harness now paces calls 7 seconds apart and records
   every attempt, so a re-run will also capture per-attempt repair detail that this run missed.
3. **Reviewer severity calibration.** The one miss above suggests the review prompt should name
   intent contradiction as a hard failure more explicitly. Not changed, because it could not be
   validated in this session.
4. **Single provider for both stages.** Compose and review are routed independently by design but
   were both Gemini in this evaluation, since no Anthropic key is configured. An independent
   reviewer model would be a stronger test of review independence.
5. Evaluation used synthetic profiles throughout. Anthony's real profile revision remains
   **inactive**, so no live composition is possible, which is the correct state for this phase.

## Explicit answers

1. **Can the composer choose its own intent?** No. Intent is copied into the snapshot; a returned
   intent that differs is `block: intent_changed`, and the snapshot hash pins it.
2. **Can it choose arbitrary candidate capabilities or projects?** No. Only IDs in the selected
   subset are accepted; others are `block: unselected_capability` / `unselected_project`.
3. **Can it change the target job?** No. `block: opportunity_changed`, plus
   `application_title_mismatch` if subject and body do not carry the exact pinned title.
4. **Can an unrelated role become an application?** No. `Snapshot.Validate` refuses a specific
   application whose match does not permit one, before any model call; the Phase 3 database CHECK
   prevents the plan existing in the first place.
5. **Can payments experience leak into unrelated emails?** No. Three layers, and the measured
   backend-with-payments-proof email contains no payment vocabulary at all.
6. **Can source content instruct the model?** Source text is structurally separated as
   `untrusted_sources` and both policies forbid following it. Independently, an injected claim
   produces unapproved-technology and unsupported-numeric blocks whether or not the model complied.
7. **Can the model claim an unavailable technology?** No. Every technology in the shared
   vocabulary outside the approved set is a hard block unless the sentence is an honest gap
   disclosure.
8. **Can it claim a CV is attached when it is not?** No. Any "attached", "attachment" or
   "enclosed" is `block: fabricated_attachment`; a CV claim without a CV resource is
   `block: cv_not_configured`.
9. **Does semantic review inspect the complete email?** Yes — final subject and body together,
   with the policy stating that annotations are not proof and all prose must be inspected.
10. **Can a failed semantic review progress?** No. `reviewed` requires both an empty union of
    findings and `review.Passed`. An unavailable or malformed review is `review_unavailable`.
11. **Can human edits bypass review?** No. Edits run the same validation and the same
    complete-email semantic review.
12. **Are drafts pinned to the exact inputs?** Yes — profile revision, opportunity revision, plan
    id and revision, recipient and policy version, sealed by a content hash.
13. **Do changed inputs make drafts stale?** Yes. The hash stops matching and the snapshot is
    refused.
14. **Are sending and follow-up paths still disabled?** Yes. `EmailPipelineReady` is `false`, the
    scheduler is not started, no legacy handler constructs a provider, and `executeSend`,
    follow-up preparation and resend all refuse before touching the database.

## Success criterion

> Given an already-decided opportunity, intent, recipient, capability set and proof set, the
> system can produce a concise, natural and informative email that represents the candidate
> accurately without inventing facts, changing strategy, or allowing a project's domain —
> especially payments — to hijack the message.

**Safety and correctness: demonstrated.** Eleven adversarial emails, all rejected. Seven contract
violations caught with no model at all, and proven to survive a reviewer that wrongly passes. The
payments firewall holds in both directions on real output.

**Writing quality: substantially demonstrated, with one named exception.** Twelve real
compositions, concise, materially different from one another, grounded in the evidence each plan
selected, free of the meta-commentary the previous fixtures contained. The stock opening in ten of
twelve is the remaining defect; it is measured, its fix is drafted, and the reviewer now flags it.

Phase 5 should not begin until that opening fix is validated against the provider and this report
is re-reviewed.

---

# Final validation round: opening fix

Date: 2026-09-12 (later session)

This round addresses the one accepted outstanding defect: the stock `I am writing to...` opening
measured in ten of twelve emails in the first evaluation. Everything in this section is additive.
The evidence above is the first evaluation and is retained unchanged.

## Applied: the corrected opening instruction

`prompts/common.txt` now forbids the content-free opener while explicitly protecting the
declaration of purpose:

> Do not open with a content-free announcement of the act of writing: "I am writing to ..." and
> its variants say nothing before the comma. Put the substance in the first sentence instead: the
> role and why it fits, the opportunity you are asking about, or the specific thing you noticed
> about this company. **Stating your purpose plainly is not the problem and must not be dropped.**
> "I'm applying for the &lt;exact advertised title&gt; role at &lt;company&gt;" is a good opening
> precisely because it carries the substance; "I am writing to apply for ..." is the same sentence
> with throat-clearing in front.

No replacement openings are hard-coded; the rule describes what a good opening does, and gives one
worked example specifically to prevent the earlier failure mode where the model dropped the word
"apply" altogether.

`prompts/specific_job_application.txt` was reinforced in the same edit so the opening guidance can
never be read as licence to soften intent:

> This is not optional and is not softened by the guidance on openings: the reader must be in no
> doubt that this is an application for that exact role. Say so directly rather than announcing
> that you are writing to say so.

## Applied: reviewer severity for intent contradiction

The first evaluation found the reviewer classifying a service-outreach email that says "I'd like to
apply" as a quality issue rather than a hard failure. `prompts/review.txt` now states:

> A message whose language contradicts its authoritative intent is a hard failure, never a tone or
> quality remark: a service_outreach or speculative_job_enquiry email that offers to apply, or asks
> for a position, contradicts its intent even when every sentence in it is true, and an application
> that pitches services does the same.

The deterministic `intent_contradiction` block is untouched and still blocks this case
independently of anything the reviewer decides.

## Applied: provider defect found while preparing the run

The pool rotates blindly, so with one exhausted credential every second call failed with a
rate-limit error that surfaced as `malformed_model_output` — a provider fault wearing the costume
of a model-quality fault. `JSONProvider.call` now rotates past a rate-limited credential for at
most one full pass over the pool. It never reissues a rejected request against the same
credential and never sleeps in a loop; a fully exhausted pool is an explicit bounded failure.
Fixture: `TestRateLimitedKeyRotatesToTheNextCredential`.

## Applied: measurement built into the harness

`.local/phase4-eval.go` now reports, over whatever it composed:

- how many emails open with `I am writing` / `I'm writing`;
- how many distinct first-five-word opening shapes occur, and how many emails sit in a repeated
  shape;
- how many applications still contain apply/applying/application anywhere in subject or body;
- how many applications carry the exact pinned title in **both** subject and body.

It also gained a scenario filter, so the required coverage can be run without spending quota on
the full set, and it records every attempt so a repair is captured if one occurs naturally.

## Re-run of the deterministic tests (item 5)

Run after the prompt change. All green, no regression:

| Test | Guards |
| --- | --- |
| `TestDeterministicValidatorCatchesContractViolations` | 7 of 11 adversarial defects, including `application_intent_unclear`'s sibling checks |
| `TestCleanReviewCannotOverrideDeterministicBlock` | deterministic findings survive a wrongly-passing reviewer |
| `TestPinnedIntentFactsResourcesAndPaymentFirewall` | pinned intent, facts, resources, payment firewall |
| `TestIntentAndProofAcceptanceFixtures` | exact advertised-title enforcement, proof selection |
| `TestServiceIntentRemainsSeparateThroughRepair` | intent contradiction across repair |
| `TestPartialBackendFitRetainsFamilyAndDisclosesGap` | speculative-role leakage and gap disclosure |
| `TestPaymentsProjectUsableWithoutPaymentVocabulary`, `TestGenuinePaymentRoleMayUsePaymentLanguage` | payment firewall both directions |
| `TestPreflightHumanEditsAndReviewFailures` | human edits revalidated |
| `TestRepairLoopFixtures` | bounded repair, envelope preservation, blocked bad repair |
| `TestPromptsRetainNonNegotiableInstructions` | **new** — asserts the shipped prompt files still demand application intent and the exact title, and that the opening rule contains no instruction suppressing application language |

`go test ./... -count=1` passes across every package with the opt-in PostgreSQL tests enabled.
`gofmt` and `go vet` clean.

`TestPromptsRetainNonNegotiableInstructions` exists because prompt text is not exercised by the
scripted-provider tests. It is the guard that stops a future opening tweak from quietly weakening
intent the way the first attempt did.

## Not done: the live re-run

**The live evaluation did not run. The provider quota is exhausted.**

`gemini-3.5-flash` free tier enforces `GenerateRequestsPerDayPerProjectPerModel-FreeTier` at 20
requests per day per key. Both configured keys are spent: a small probe on key 1 returned HTTP 200
and consumed what turned out to be its last request, after which every composition request on both
keys returned HTTP 429. The attempted run produced zero emails.

Nothing was inferred from that failed run, and no measurement in this report is derived from it.

### Consequence for the repository state

**The prompt now stored in the repository is not the prompt that was measured.** The measured
prompt is the one behind the twelve emails in the first evaluation; the stored prompt additionally
carries the opening fix, the application-intent reinforcement and the reviewer severity change.
This is the state the review asked for — apply, then validate — with the validation outstanding.

### What the re-run requires

After the quota resets (midnight Pacific for the Google free tier), one command:

```
go run ./.local/phase4-eval.go compose        # the 8 required scenarios, ~16-20 calls
go run ./.local/phase4-eval.go review:service # the intent-contradiction case, 1 call
```

The first prints the opening-diversity table and the invariant counts directly. Budget is
adequate on a fresh day: 40 calls against roughly 21 needed.

## Explicit answers for this round

| Question | Answer |
| --- | --- |
| How many evaluated emails still open with `I am writing`? | **Not measured.** The re-run did not execute. The measurement is implemented and prints automatically. |
| Did any application lose explicit application intent? | **Not measured live.** Guarded three ways offline: the deterministic `application_intent_unclear` check is unchanged, the application prompt now states the requirement is not softened by the opening guidance, and `TestPromptsRetainNonNegotiableInstructions` fails if either is removed. |
| Did any exact-role/title invariant regress? | No. `application_title_mismatch` is unchanged and its tests pass; the prompt still demands the exact title in both subject and body. |
| Did the semantic reviewer correctly classify the service/application contradiction? | **Not measured.** The review prompt now names it a hard failure; validation is pending. The deterministic block is unchanged and still catches it regardless. |
| Did all existing Phase 4 tests remain green? | Yes, plus three new ones (`TestPromptsRetainNonNegotiableInstructions`, `TestRateLimitedKeyRotatesToTheNextCredential`, and the earlier `TestGeminiKeyPoolIsSplitAndRotated`). |
| Is the final measured prompt the exact prompt now stored in the repository? | **No.** The stored prompt carries three unvalidated changes. This is the single reason Phase 4 is not marked complete. |

## Status

Phase 4 is **not complete**. Every offline obligation of this round is met and every safety
invariant is re-verified, but the defect this round exists to close — the stock opening — is a
property of generated text and cannot be confirmed without generating text. One evaluation run on
a fresh quota closes it.

---

# Validation run, 2026-09-13

The run the previous round could not perform. Two additional Gemini keys were added, bringing the
pool to four. The implementation was frozen before the run and is unchanged by it.

## Freeze verification

Prompt file MD5s, taken immediately before and immediately after the provider run, are identical:

```
c101802ae4a0abde74263297739238cf  common.txt
ab2b5ef4464003bf32cd0c27a9850838  review.txt
4c2e1b430275f14d3d8cbadb230abe0d  service_outreach.txt
068ed00fdb1b30d4f00370a0e050d596  specific_job_application.txt
a4d7c65e08c1c9656f13ed1b41da521d  speculative_job_enquiry.txt
```

**The prompt measured by this run is exactly the prompt stored in the repository.** No prompt,
validator, routing, repair or architecture change was made between the freeze and this report.

## Provider usage

| | |
| --- | ---: |
| Scenarios attempted | 8 |
| Scenarios that produced an email | 8 |
| Compose calls | 19 |
| Review calls | 15 |
| Targeted reviewer case (`review:service`) | 1 |
| **Total provider calls** | **35** |
| Daily budget across 4 keys | 80 |
| **Quota deliberately left unused** | **45 (56%)** |

No health probes or warm-up requests were issued; the first provider call of the session was the
first evaluation request. Keys were used through the existing bounded round-robin rotation, so
all four were touched roughly evenly; no key was deliberately drained before moving on, and no
sleeping-on-quota or same-key retry occurred.

Nineteen compose calls for eight scenarios is higher than the ~16 planned. The cause is the
reviewer defect below: almost every email was rejected, which triggered repairs, which doubled
and tripled several scenarios. Cost overrun and root cause are the same thing.

**Seven provider-side failures occurred** — four `malformed_model_output` on compose and three
`review_unavailable`. The harness records the failure reason but not the provider error category,
so these cannot be attributed between rate limiting and malformed model JSON without further
calls, which were not made. Either way they were handled as provider failures, not as model
quality failures, and the bounded repair loop absorbed them.

## Opening quality: the fix works

**Zero of eight emails begin with "I am writing".** The previous round measured ten of twelve.

| Scenario | Opening sentence |
| --- | --- |
| Backend API role | Please consider my application for the Backend Engineer role at Northwind Systems. |
| Distributed backend role | I am applying for the Backend Engineer role at Halyard Data. |
| Integration-heavy role | I'm applying for the Backend Engineer role at Coastline Connect because my experience building resilient API integrations matches your focus on partner APIs and webhooks. |
| Full-stack role | My experience building React and Next.js interfaces alongside the backend HTTP APIs they consume fits the Full-Stack Developer role at Bramble Retail. |
| Backend role, payments proof | I'm applying for the Backend Engineer role at Verity Logistics. |
| Genuine payment-engineering role | I would like to apply for the Payments Backend Engineer position at Tandem Pay, bringing my experience building transactional workflows. |
| Speculative enquiry | I am looking for backend or full-stack software engineering opportunities at Larkspur Group and want to ask if you have any open roles. |
| Service outreach | I saw that Quarry Health is looking to connect your booking platform with partner lab systems. |

| Measure | Result |
| --- | ---: |
| Begin with "I am writing" | **0 of 8** |
| Distinct first-five-word shapes | **7 of 8** |
| Emails inside a repeated opening shape | 2 of 8 |

### Did it just create another template?

Partly, and defensibly. Five of six applications open by declaring the application, which is what
an application should do — but across four distinct forms: "Please consider my application…",
"I am applying…", "I'm applying…" (twice), "I would like to apply…". The only repeated
five-word shape is "I'm applying for the" in two scenarios.

The two non-application intents opened on substance rather than on a declaration: the speculative
enquiry on what is being asked for, the service outreach on the company's own stated need. That
is the context-sensitivity the guidance was aiming at, not randomness.

## The defect: the reviewer rejects the openings the guidance endorses

Seven of eight scenarios ended `review_required`. The dominant reason is a single reviewer finding
code:

| Scenario | Final reviewer findings |
| --- | --- |
| Backend API role | `review: stock_opening` |
| Integration-heavy role | `review: quality_formula` (attempt 2) |
| Full-stack role | `review: stock_opening` (attempt 1) |
| Backend role, payments proof | `review: quality_templated_opening`, then `review: stock_opening` |
| Genuine payment-engineering role | `review: stock_opening` |
| Speculative enquiry | `review: stock_opening` |
| Service outreach | *(none — passed first time)* |

The reviewer flagged, among others:

> "I'm applying for the Backend Engineer role at Coastline Connect because my experience building
> resilient API integrations matches your focus on partner APIs and webhooks."

> "Please consider my application for the Backend Engineer role at Northwind Systems."

Both are direct, substantive and specific to the role and company. The first is almost verbatim
the pattern the review brief listed as acceptable.

**Root cause.** The review-prompt edit made in the previous round lists
`"I would like to apply for"` alongside `"I am writing to"` and `"I am reaching out to"` as
examples of a stock opening. The approved guidance explicitly names
`I'm applying for the Backend Engineer role at ...` as an acceptable natural opening. The review
prompt therefore contradicts the guidance, and the reviewer applies it consistently.

**Severity.** Quality, not safety. The failure direction is over-rejection into human review, and
deterministic validation, the payment firewall and every hard invariant behaved correctly
throughout. But it makes the pipeline impractical: only one of eight drafts would reach a human
as reviewed, and it is what inflated this run's cost.

**Proposed fix, one line, not applied.** Remove `"I would like to apply for"` from the
stock-opening examples in `prompts/review.txt`, leaving only content-free announcements of the act
of writing. Per the instruction that a discovered defect does not authorize another expensive run,
the prompt is left exactly as measured and the decision to re-run is yours.

## Application invariants: one real finding

| Check | Result |
| --- | --- |
| Exact advertised title in **subject and body** | **6 of 6** — no `application_title_mismatch` anywhere |
| Subject corresponds to the pinned role | 6 of 6 |
| Body corresponds to the pinned role | 6 of 6 |
| Unrelated/provenance role substituted | none — no `unrelated_title_leakage` |
| Evidence stayed inside the snapshot | yes — no `unselected_capability` or `unselected_project` |
| Body explicitly states application intent | **5 of 6** |

The exception is the full-stack scenario. Its repaired draft opens *"My experience building React
and Next.js interfaces … fits the Full-Stack Developer role at Bramble Retail"* — the subject says
"Full-Stack Developer Application", but the body never says apply, applying or application.
**Deterministic validation caught it**: `review: application_intent_unclear`, and the draft ended
`review_required`.

This is the system working. The opening guidance did create pressure toward dropping the
declaration, and the check that exists precisely for that caught it. Note that the harness's own
opening metric reported 6 of 6 because it searches subject *and* body; the deterministic validator
searches the body alone and is the authoritative answer.

Two drafts were also blocked by `block: intent_changed` on intermediate attempts — the model
returned an intent value that did not match the snapshot, and the validator rejected it before
anything else could happen.

## Payment behaviour: no regression

**Generic backend role using payments-domain proof** (Verity Logistics, shipment tracking). Final
body:

> I'm applying for the Backend Engineer role at Verity Logistics.
>
> I build Go HTTP services with explicit API contracts. In the Synthetic Ledger project, I built
> the Go service layer, its PostgreSQL schema, and the transaction boundaries surrounding
> multi-step state changes. To ensure retries are safe, I design transactional boundaries and
> idempotent handlers so database state remains consistent.

The words payment, payments, M-Pesa, checkout, billing, settlement and reconciliation do not
appear. The payments project proved transferable backend engineering with its domain omitted, as
required.

**Genuine payment-engineering role** (Tandem Pay). Final body:

> In the Synthetic Ledger project, I used Go and PostgreSQL to integrate a payment provider,
> handle asynchronous settlement callbacks, and build the reconciliation job. To guarantee
> transactional correctness, I made callback handling idempotent so that repeated provider
> notifications cannot double-apply.

Payment-specific experience appears because the opportunity asks for it, and nothing asserts that
Tandem Pay's payments are broken.

Same project, same profile, same composer, same prompt. Only the evidenced need differs. No
payment vocabulary appeared in any other scenario, including the service outreach.

## Service/application contradiction: reviewer change validated

`go run ./.local/phase4-eval.go review:service`, one call:

```
service/application confusion  passed=false
findings=[block:wrong_intent  review:stock_opening  review:generic_salutation]
```

**`block: wrong_intent` — a hard failure.** In the previous round this same email drew only
`review: template_tone` and `review: weak_proof`. The review-prompt change did what it was meant
to do. Deterministic `intent_contradiction` continues to block it independently.

(The same call also emitted `review: stock_opening`, further confirming the over-firing above.)

## Repair evidence, real provider

Repairs occurred naturally in seven of eight scenarios; none was manufactured. The clearest
complete trace, the Backend API role:

| Attempt | Source | Deterministic | Semantic |
| --- | --- | --- | --- |
| 1 | generated | clean | fail: `review: stock_opening`, `review: meta_commentary` |
| 2 | repair | **`block: intent_changed`** | fail: `review: stock_opening` |
| 3 | repair | clean | fail: `review: stock_opening` |

Final status `review_required` after the bounded limit, exactly as designed: three attempts, no
fourth, no loop. Attempt 2 shows both layers acting at once — the repair fixed nothing the
reviewer wanted and simultaneously broke a hard invariant, which deterministic validation caught
independently of the reviewer's opinion.

## Deterministic test suite after the run

`gofmt` clean, `go vet` clean, `go test ./... -count=1` green across every package with the
opt-in PostgreSQL tests enabled. All nineteen composition tests pass, including:

exact advertised-title enforcement · `application_intent_unclear` · intent contradiction ·
speculative-role leakage · payment firewall both directions · capability and project restrictions ·
CV and resource validation · deterministic validation surviving a wrongly-passing reviewer ·
prompt-invariant assertions · key-pool split, rotation and exhaustion behaviour.

## Explicit answers

1. **How many evaluated emails still begin with `I am writing...`?** **Zero of eight**, down from
   ten of twelve.
2. **Did the new guidance simply create another repeated opening?** No rigid template. Seven of
   eight first-five-word shapes are distinct; one shape repeats twice. Five of six applications
   declare the application, across four distinct phrasings, which is appropriate to the intent.
   The two non-application intents opened on substance instead.
3. **Did every specific application retain explicit application intent?** **No — five of six.**
   The full-stack draft lost it from the body; deterministic `application_intent_unclear` caught
   it and the draft was held for review.
4. **Did every specific application retain the exact pinned advertised title in subject and body?**
   **Yes, six of six.** No title mismatch occurred in any attempt.
5. **Did any payment behaviour regress?** **No.** The generic backend email used the payments
   project with zero payment vocabulary; the genuine payment role used payment-specific experience
   without asserting a recipient problem.
6. **Did the semantic reviewer classify the service-outreach/application-language contradiction as
   a hard failure?** **Yes — `block: wrong_intent`**, where it previously returned only quality
   findings.
7. **Did deterministic validation remain authoritative?** **Yes.** It caught
   `intent_changed` twice and `application_intent_unclear` once during this run, independently of
   the reviewer, and no draft was released against a deterministic finding.
8. **Did all Phase 4 tests remain green?** **Yes**, all nineteen composition tests and every other
   package.
9. **Is the exact prompt measured by this run the prompt currently stored in the repository?**
   **Yes.** MD5s identical before and after.
10. **How many Gemini calls were consumed and how many keys were needed?** **35 calls** against an
    80-call daily budget across four keys, leaving **45 unused (56%)**. All four keys were in the
    rotation; no key was drained deliberately and no probe requests were issued.

## Status: not approved

Safety, factuality, intent isolation, payment safeguards, title invariants and deterministic
authority all held under real provider load. The opening fix achieved its objective and the
reviewer severity change validated.

The blocker is the reviewer's `stock_opening` criterion rejecting endorsed openings, which put
seven of eight drafts into `review_required`. It is one line of `prompts/review.txt`. The fix is
identified but deliberately not applied, and applying it would require a further validation run —
roughly nine calls if limited to the applications, well inside the remaining budget — which is
your call to authorise.

One incidental fix, outside the frozen surface and made after the run: the harness wrote a single
results file that a later partial run overwrote, which destroyed the first round's raw JSON and
nearly destroyed this one's. It now also writes a timestamped per-run file.

---

# Focused reviewer follow-up, 2026-09-13

The authorized one-line correction and its narrow revalidation.

## The change

`prompts/review.txt` only. The stock-opening criterion listed `"I would like to apply for"`
alongside genuine throat-clearing, which made the reviewer reject direct application declarations.
It now separates the two:

> …a content-free opening that only announces the act of writing ("I am writing to ...", or "I am
> reaching out to ..." where it announces contact and adds nothing) instead of starting with
> substance…
>
> Directly declaring the purpose is NOT a stock opening. "I'm applying for the &lt;title&gt; role at
> &lt;company&gt;", "I am applying for ...", "I would like to apply for ..." and "Please consider my
> application for ..." all carry substance, and an application is required to state that it is one.
> Never raise a stock-opening or templated-opening finding merely because the email says plainly
> that it is an application. Raise it only where the opening is genuinely content-free, generic or
> formulaic in context.

Nothing else changed. MD5s confirm it: `common.txt`, `specific_job_application.txt`,
`speculative_job_enquiry.txt` and `service_outreach.txt` are byte-identical to the previous run;
only `review.txt` moved, from `ab2b5ef4…` to `b1fa94f7…`. No composer prompt, validator, intent
rule, payment-firewall rule, provider route, repair logic or architecture was touched. The review
prompt hash was identical immediately before and immediately after the provider run.

Broader tone review survives: generic or robotic phrasing, repetition, weak CTA, filled-in-template
prose and meta-commentary are all still flagged.

## Offline reviewer fixtures, both directions

`TestReviewPromptSeparatesThroatClearingFromDirectPurpose` asserts, without any provider call,
that the shipped review prompt still flags content-free announcements, that it explicitly protects
all four direct application declarations, and that the broader tone criteria were not removed by
the correction. It sits alongside `TestPromptsRetainNonNegotiableInstructions`, which guards
application intent and the exact-title requirement.

## Revalidation run

Five application scenarios, the narrowest set that exercises the reviewer against direct
application language across different evidence profiles.

| | |
| --- | ---: |
| Application scenarios tested | 5 |
| Compose calls | 6 |
| Review calls | 5 |
| **Total provider calls** | **11** |
| Consumed today across both runs | 46 of 80 |
| **Quota left unused** | **34 (43%)** |

No probes were issued. The run was not repeated, and no scenario was regenerated to chase better
wording.

### Results

| Scenario | Status | Attempts | Deterministic | Reviewer |
| --- | --- | ---: | --- | --- |
| Backend API role | `reviewed` | 1 | clean | passed, no findings |
| Integration-heavy role | `reviewed` | 1 | clean | passed, no findings |
| Full-stack role | `reviewed` | 1 | clean | passed, no findings |
| Backend role, payments proof | `review_required` | 2 | clean | **unavailable — provider fault** |
| Genuine payment-engineering role | `reviewed` | 1 | clean | passed, no findings |

**`stock_opening` false positives: 6 before the fix, 0 after.** Every reviewer verdict returned in
this run was `passed=true` with an empty findings list.

The one scenario that did not reach `reviewed` failed for provider reasons, not quality ones: its
first compose attempt returned malformed JSON and the review call on its second attempt came back
unusable. Its final draft is deterministically clean and satisfies every content check below. The
engine handled both faults as designed — bounded, no loop, held for human review rather than
released. No further call was spent re-running it.

### Openings

| Scenario | Opening |
| --- | --- |
| Backend API role | I'm applying for the Backend Engineer role at Northwind Systems. |
| Integration-heavy role | I'm applying for the Backend Engineer role at Coastline Connect. |
| Full-stack role | I'm applying for the Full-Stack Developer role at Bramble Retail. |
| Backend role, payments proof | I am applying for the Backend Engineer role at Verity Logistics. |
| Genuine payment-engineering role | I am applying for the Payments Backend Engineer role at Tandem Pay. |

Zero begin with "I am writing". All five declare the application directly and name the exact role
and company in the first sentence.

**Structural repetition, reported not chased.** Three distinct first-five-word shapes across five
emails; four of five sit in a repeated shape. Every application now opens with "I'm applying for
the …" or "I am applying for the …". This is the outcome the review brief anticipated: safer and
better than throat-clearing, but a narrower band than the previous run, which produced four
distinct declaration forms while the reviewer was pressuring away from the direct one. With that
pressure removed the composer settles on the most direct form. No emails were regenerated to widen
it. Note that the non-application intents, measured in the previous run under the same composer
prompt, opened on substance rather than on a declaration — the speculative enquiry on what was
being asked for, the service outreach on the company's own stated need.

### Required checks

| Check | Result |
| --- | --- |
| Reviewer emits `stock_opening` merely for direct application intent | **No — zero occurrences** |
| Body clearly states application intent | **5 of 5** |
| Exact pinned advertised title in subject **and** body | **5 of 5** — no `application_title_mismatch` |
| Deterministic validation clean on every final draft | **5 of 5** |
| Payment regression | **None** |
| Capability / project / factuality regression | **None** — no `unselected_capability`, `unselected_project` or `unapproved_technology` |
| Unrelated-role leakage | **None** |
| Fabricated resource or attachment claim | **None** — every message offers the CV truthfully |

### The full-stack case

The previous run's repaired full-stack draft lost the declaration from its body. This run:

> I'm applying for the Full-Stack Developer role at Bramble Retail. I build React and Next.js
> interfaces alongside the backend HTTP APIs that power them, taking features from frontend UI to
> database integration.

No throat-clearing, and an unmistakable application for the exact pinned role. `reviewed` on the
first attempt with no findings from either layer.

### Payment behaviour

**Backend role using payments-domain proof** — the words payment, payments, M-Pesa, checkout,
billing, settlement and reconciliation appear **nowhere** in the body:

> My background is in building Go HTTP services with explicit API contracts. To handle concurrency
> and network failures, I design strict transaction boundaries and idempotent handlers so that
> retries are always safe. For example, in the Synthetic Ledger project, I built the Go service
> layer, designed its PostgreSQL schema, and implemented the transaction boundaries around
> multi-step state changes.

**Genuine payment-engineering role** — payment-specific experience appears because the vacancy
asks for it, with no assertion that the employer's payments are broken:

> In my work on Synthetic Ledger, built with Go and PostgreSQL, I integrated payment providers and
> managed asynchronous settlement callbacks. To ensure transactional correctness, I designed the
> callback processing to be idempotent, preventing repeated provider notifications from
> double-applying. I also built the daily reconciliation jobs to resolve mismatched transaction
> states across the systems.

Same project, same profile, same composer prompt. Only the evidenced need differs.

## Tests

`gofmt` clean, `go vet` clean, `go test ./... -count=1` green across every package with the
opt-in PostgreSQL tests enabled. **All 20 composition tests pass**, including the two prompt-
invariant guards.

## Explicit answers

1. **Provider calls used:** 11 (6 compose, 5 review). 46 of 80 consumed today across both runs,
   **34 left unused**.
2. **Application scenarios tested:** 5.
3. **`stock_opening` false positives before vs after:** **6 → 0.**
4. **Every tested application states application intent in the body:** **Yes, 5 of 5.**
5. **Every tested application retains the exact pinned title in subject and body:** **Yes, 5 of 5.**
6. **Full-stack application preserves explicit application intent:** **Yes** — "I'm applying for
   the Full-Stack Developer role at Bramble Retail", reviewed clean on the first attempt.
7. **Payment behaviour unchanged:** **Yes.** Zero payment vocabulary in the generic backend email;
   supported payment language in the genuine payment role.
8. **All deterministic and Phase 4 tests green:** **Yes**, 20 of 20 composition tests plus every
   other package.
9. **Is the exact reviewer prompt measured the prompt stored in the repository:** **Yes.**
   `review.txt` hashed `b1fa94f7384c142d9d04db2b6d5cf9f7` before and after the run.

## Phase 4: complete

Every approval criterion is met.

**Safety and correctness.** Eleven adversarial emails rejected, seven of them by deterministic
rules with no model involved and proven to survive a reviewer that wrongly passes. Intent,
recipient, pinned vacancy and exact title held under real provider load. The payment firewall holds
in both directions. Prompt injection is answered structurally and by deterministic blocks rather
than by instruction alone.

**Writing quality.** Zero stock openings across two runs and thirteen real compositions. Content
adapts to whichever evidence the plan selected. The meta-commentary criticised earlier is gone.
Applications now open in a narrower band than ideal — a known, reported, non-blocking observation
rather than an unmeasured risk.

**Residual notes for a later phase, none blocking:**

- Application openings cluster on the direct "I'm applying for the …" form. Worth revisiting only
  if it reads poorly in practice; deliberately not tuned further here.
- Compose and review both ran on Gemini because no Anthropic key is configured. Routing is already
  independent; a different reviewer model would test review independence more strongly.
- Seven provider faults across the two runs could not be attributed between rate limiting and
  malformed model JSON, because the harness records the failure reason but not the provider error
  category. Recording it would cost nothing and would sharpen future diagnosis.

Sending, follow-ups, the scheduler and automatic outreach all remain disabled.
`EmailPipelineReady` is `false`. Phase 5 has not begun.
