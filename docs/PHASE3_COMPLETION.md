# Phase 3 completion report

Date: 2026-09-12  
Status: implemented and verified; awaiting review before Phase 4.

Phase 3 decides **what a future email is allowed to say and why we are contacting the
recipient**. It implements candidate/opportunity matching, requirement extraction, research
evidence quality and grouping, company relevance, recipient relevance and selection,
capability/proof selection, explicit contact plans and authoritative intent selection.

It does **not** implement composition, semantic email review, follow-up writing or sending.
No AI request was made, no message was generated, no email was sent, and no background worker
was started. The construction interlock is intact.

## Schema and migrations

Applied through the existing tracked runner. Three additive migrations, no legacy SQL replayed:

| Migration | Additions |
| --- | --- |
| `0019_research_evidence.sql` | Evidence provenance on `signal` (`fact_key`, `fact_group`, `excerpt`, `extraction_method`, `confidence`, `first_seen_at`, `last_seen_at`, `observations`, `revision`); partial unique index `signal_canonical_fact` on new canonical rows only; `protect_signal_history` trigger; `company.business_segment`. |
| `0020_opportunity_matching.sql` | `opportunity_match` (pinned to opportunity revision, profile revision and matcher version) and `company_relevance`, with classification and family-confidence CHECK constraints. |
| `0021_contact_plans.sql` | `contact_plan` and immutable `contact_plan_revision`; the unrelated-role CHECK; the specific-application-pins-a-revision CHECK; the provenance-is-not-the-applied-role CHECK; `contact_plan_active_key` partial unique index; `protect_contact_plan` and `immutable_contact_plan_revision` triggers. |

The application now requires tracked schema **21**. Startup additionally verifies the new
columns, the two partial unique indexes, the three new triggers and the unrelated-role CHECK
constraint by name; a database with that constraint dropped will not start.

The rehearsal preservation check in `backup-verify -rehearse-phase2` previously required each
original table's inventory to be byte-identical, which an additive column change can never
satisfy. It now proves the stronger and more accurate claim: identical row count, identical
fingerprint over the **original columns only**, and no original column removed or retyped.
Added columns are reported explicitly. `SchemaReport` gained `TableInventory.LegacyDataHash`
for this purpose.

## Live-data impact

Sequence run against the configured database, application stopped throughout:

```
backup-verify -pg-bin 'C:\Program Files\PostgreSQL\18\bin' -out .local/phase3-preflight.dump -rehearse-phase2
inspect -out .local/phase3-before.json
apply -proof .local/phase3-preflight.dump.verified.json
check
inspect -out .local/phase3-after.json
```

The rehearsal ran on a **restored copy** of the actual backup before anything touched the live
database, and reported:

```
Rehearsal added columns to company: business_segment (all 417 original rows preserved unchanged)
Rehearsal added columns to signal: confidence, excerpt, extraction_method, fact_group, fact_key,
  first_seen_at, last_seen_at, observations, revision (all 1697 original rows preserved unchanged)
Scratch migration/backfill rehearsal preserved every original table and sequence
```

Live before/after, from the two inspection reports:

| Original table | Rows before | Rows after | Original-column fingerprint |
| --- | ---: | ---: | --- |
| company | 417 | 417 | unchanged |
| contact | 771 | 771 | unchanged |
| signal | 1,697 | 1,697 | unchanged |
| page_cache | 1,120 | 1,120 | unchanged |
| draft | 107 | 107 | unchanged |
| send_log | 33 | 33 | unchanged |
| suppression | 1 | 1 | unchanged |
| template | 10 | 10 | unchanged |

Row counts also match `.local/phase2-after.json` exactly for every Phase 2 table, including
`opportunity` (38), `opportunity_revision` (38), `ingestion_review` (354) and
`legacy_opportunity_backfill` (392). The 14 preserved duplicate signal groups remain 14. No
original sequence changed; only `contact_plan_id_seq` and `opportunity_match_id_seq` were added.

| New live table | Rows |
| --- | ---: |
| `opportunity_match` | 0 |
| `company_relevance` | 0 |
| `contact_plan` | 0 |
| `contact_plan_revision` | 0 |

All 1,697 live signals are labelled `legacy_detection`; there are 0 canonical evidence rows,
because no research run has been performed since the migration. No legacy detection text was
rewritten, and the database now refuses to rewrite one.

Migration history is `16 baseline, 17 applied, 18 applied, 19 applied, 20 applied, 21 applied`.

## Profile activation status

**No profile revision was activated.** The live database still holds exactly one approved but
inactive seed revision containing only identity, primary/secondary positioning and the three
desired role families. No technology, capability, project, responsibility, outcome, resource,
engagement preference or signature fact was added, inferred or approved.

Consequently, on the live database:

- matching returns `profile_required` and stores nothing;
- company relevance reports that company work cannot be matched to approved capabilities;
- no contact plan can be created — `CreatePlan` refuses, and every intent resolves to
  `blocked` with `profile_required`.

Discovery, opportunity storage, opportunity inspection and company research all continue to
work with no active profile, as required.

## Match assessments created

**0 on the live database**, for the reason above. `opportunity_match` is empty and the
`profile_required` result is deliberately not storable: `SaveOpportunityMatch` refuses it, and
the table's foreign key to `candidate_profile_revision` makes an unattributed assessment
impossible.

Classification breakdown, live: none exist. In the isolated test databases, synthetic approved
profiles produce all three classifications; the multi-vacancy fixture stores three assessments
for one employer and reports `unrelated: 2` alongside one relevant backend match.

## Matching implementation

`internal/opportunity/match.go` and `requirements.go`, matcher version `fit-v1`. Deterministic
and dictionary-based. No AI participates in matching.

**Title/family gate.** The advertised title decides the role family and the description can
never override it. Titles are normalized for case, whitespace, Unicode hyphen variants
(‐ ‑ ‒ – — ― − ⁃ and the soft hyphen) and the several spellings of full-stack, back-end and
front-end, while the original advertised title is preserved verbatim. The *role designation* —
everything before the first separator — is read first:

1. a non-engineering business function or non-software discipline in the role designation →
   `unrelated`, whatever the description says;
2. an approved desired specialization plus an engineering role noun → family `clear`;
3. an engineering role noun alone → family `ambiguous`;
4. an approved family present only outside the role designation → `ambiguous`;
5. otherwise → `unrelated`.

Specializations are derived from the **approved profile's own** desired families and aliases by
stripping interchangeable role nouns, so `Backend Engineering`, `Backend Engineer` and
`Backend Developer` all yield `backend`, and `Full Stack Engineer` matches even though it is
not a listed alias. A family the operator has not approved can never match.

Reading the role designation rather than the whole title is what keeps
`Software Engineer - Marketing Platform` relevant while `Product Marketing Manager` is not.

**Requirement extraction.** Conservative and section-aware. Only bulleted lines, lines inside a
requirements or responsibilities section, and lines carrying an experience cue are read;
benefits, perks, "about us" and "how to apply" sections are skipped entirely. Every extracted
requirement keeps its verbatim source line and byte offset. Requirements are typed as
`technology`, `database`, `cloud_infrastructure`, `integration_api`, `backend_capability`,
`frontend_capability`, `experience`, `employment_eligibility` or `unknown`. Terms are matched on
whole-word boundaries, and the bare word "go" is only read as the language when the line is
unmistakably about software — "willing to go the extra mile" is not a Go requirement.

Unrecognized requirement lines are retained as `unknown` and displayed, with an explicit stated
reason that failure to recognize a requirement is not evidence that none exists.

**Requirement resolution.** Each recognized requirement is matched against approved
technologies (name and aliases), capabilities (name, description, and linked technologies) and
projects (name, description, responsibilities, outcomes, domains, and linked technologies and
capabilities), producing exact references such as `profile:capability:cap-api`. An unmatched
requirement becomes an `unmet` gap that names itself as never claimable. Eligibility conditions
are resolved only against approved engagement preferences: a `conflict` is reported only where
a confirmed enumeration genuinely excludes the requirement, and everything else stays `unknown`
and disclosed.

**Score and classification.** The `fit-v1` score (family 40/20, up to two supported capabilities
at 20 each, 10 for a supporting project, 10 for agreeing known conditions) is a ranking and
debugging aid. It decides nothing. Classification gates take precedence:

- `unrelated` — settled by the title gate before any requirement or score is considered; score
  is forced to 0.
- `strong_match` — requires all of: family `clear`; a `complete` description with at least three
  recognized requirements; at least two approved capabilities supported by an actual requirement
  excerpt; no unresolved mandatory requirement; no known preference conflict; score at least 80.
- `possible_match` — everything else, with every blocking reason listed.
- `profile_required` — no active approved profile; never stored.

A title alone therefore cannot reach `strong_match`, and a missing title yields family `none`,
which cannot authorize an application at all.

## Company relevance implementation

`internal/relevance`, assessor version `relevance-v1`. It answers only whether there is a
truthful engineering reason to contact a company, and is computed from **fact groups**, never
from rows. Four capped categories:

| Category | Cap | What earns points |
| --- | ---: | --- |
| `relevant_opportunity` | 25 | A current vacancy assessed strong (25) or possible (15). Unrelated vacancies earn nothing and are reported as such. |
| `engineering_need` | 30 | A need or request the company stated itself, 15 per fact group. Legacy keyword detections earn 5 each to a maximum of 10 and are labelled as detections rather than the company's own words. |
| `capability_relevance` | 30 | Company work the company describes itself, 15 per group, only where it matches an approved capability or technology. |
| `usable_contact` | 15 | A recruiting, engineering or leadership contact (15); a generic mailbox only (5). |

A company is `relevant` only if a vacancy, a stated need or matching described work exists. A
contact alone is never a reason to make contact. Generic technology detection, checkout
presence and payment acceptance contribute nothing.

## Evidence grouping and deduplication behaviour

`internal/evidence` plus `store.UpsertEvidence`. A fact's identity is its kind plus its
normalized detail, so the same sentence observed on `/`, `/about` and `/services` is one fact
observed three times: one row, `observations` incremented, `last_seen_at` extended,
`first_seen_at` untouched, and all source URLs retained.

New canonical rows carry `fact_key`, `fact_group`, the source URL, a verbatim excerpt, the
extraction method and a confidence. The partial unique index applies to canonical rows only —
`fact_key <> ''` — so the **14 preserved legacy duplicate groups are entirely outside it** and
every historical signal ID remains citable. At read time those duplicates collapse into one
group, so they cannot inflate relevance while still being individually referenceable.

Each detection kind declares what it establishes and what it can never establish. `SemanticsFor`
records, for example, that a checkout establishes only that checkout functionality is present,
and cannot establish that the recipient has a payment problem, needs an integration, has
reconciliation failures, or wants payment work outsourced.

Legacy detections are labelled `legacy_detection` and their text is protected by the
`protect_signal_history` trigger: an attempt to rewrite one into a quotation is refused by the
database.

## Research refresh safety

The enricher's in-process page cache was an unsynchronized map with no expiry and no bound. It
is now mutex-protected, expires entries after 15 minutes, and evicts expired then oldest entries
at a 512-entry cap, so a long-running worker can neither race the HTTP enrich path nor feed
indefinitely stale content into Phase 3. Retrieved page content is stored as quoted evidence
data; nothing in this phase turns it into an instruction.

The enricher now writes evidence with provenance rather than repeated bare signals, and extracts
two new quoted kinds: `product_description` (what the company says it builds) and
`engineering_request` (a hiring need stated by the company, only where the same sentence is
actually about engineering).

## Recipient selection implementation

`relevance.ClassifyContact` infers a function — `recruiting`, `engineering`, `leadership`,
`commercial`, `generic_mailbox` or `unknown` — from the stored role first and the mailbox local
part second, matching whole segments so `dev@` and `jobs.dev@` match but `davidevans@` does not.
A person's name is reported only when one is actually stored; **no name is ever reconstructed
from a local part**.

`plan.RankRecipients` ranks candidates per intent into `preferred`, `acceptable`,
`review_required` or `unusable`:

| Intent | Preferred | Acceptable | Review required |
| --- | --- | --- | --- |
| `specific_job_application` | application address published by the source, recruiting, engineering | leadership | commercial, generic mailbox, unknown |
| `speculative_job_enquiry` | recruiting, engineering | leadership | commercial, generic mailbox, unknown |
| `service_outreach` | engineering, leadership | — | recruiting, commercial, generic mailbox, unknown |

An application address the source itself published outranks every inferred contact. A contact
marked bad is `unusable`. A generic or unidentified mailbox is usable but stays
`review_required`, with the reason stating that its relevance is unverified.

The chosen recipient's contact ID, address, name, relevance tier and reason are frozen into the
contact plan and into an immutable plan-revision snapshot.

## Contact plan implementation

`contact_plan` records company, intent, applied-for opportunity and revision (specific
applications only), provenance opportunity, match classification, target families, frozen
recipient, profile revision, plan key, revision, origin, status, review requirement, reasons and
the full selection. A plan key is company plus vacancy-or-none plus intent, with one live plan
per key enforced by a partial unique index.

Changing the recipient, intent, pinned vacancy or profile revision requires a new plan revision;
the `protect_contact_plan` trigger refuses a silent in-place edit. Each revision writes an
immutable snapshot, and a recipient change reopens review. `UpdatePlanRecipient` also refuses a
contact belonging to another company. Plans are closed, never deleted.

Selecting a plan authorizes future draft generation only. It authorizes no sending, and sending
remains disabled regardless by the construction interlock.

## Capability and project selection implementation

`plan.SelectProof`, selector version `selection-v1`. Bounded to at most three capabilities and
two projects — one project for service outreach. Priority: direct requirement match, then work
the company actually describes, then broad approved backend/full-stack ability. Every selected
item carries its approved ID, why it was chosen, the supporting requirement or evidence
reference, and a `use_as` of `capability`, `proof` or `optional_specialization`.

The selection also emits `allowed_refs` — the exact and only profile items a later composer may
draw on — and `forbidden_claims`, which always include:

- `unselected_profile_item`: no other skill, technology, project, outcome, resource,
  availability or price may be introduced;
- `recipient_payment_problem`, unless a payment need is genuinely evidenced;
- one `unapproved_requirement_*` entry per unmet requirement, naming it as acknowledgeable as a
  gap but never claimable;
- `unrelated_title_as_target_role` for a speculative enquiry carrying an unrelated provenance
  listing.

With no supported capability, positioning falls back to
`broad_backend_full_stack_introduction` with a note that only broad truthful positioning is
permitted — no narrow offer and no diagnosis.

## Payments invariant

A payment approach requires actual evidence of payment-related need: a reported payment problem
in the source text, or a vacancy that itself demands payment engineering. Checkout presence,
M-Pesa acceptance, billing or collections vocabulary, a finance-sector company, and the
candidate's own payments experience are each explicitly insufficient, individually and together.

Where no payment need is evidenced, a payments-domain capability is demoted to
`optional_specialization` and excluded from broad positioning entirely, while a payments project
remains selectable as `proof` of the underlying backend abilities, annotated
`payments_item_as_backend_proof_only`, with the payment diagnosis added to forbidden claims.

## Intent rules implemented

Exactly three intents. Legacy company source, legacy segment, legacy score, `hiring_backend`
signals, the old service pitch and the old templates are absent from the intent inputs entirely.

| Requested | Rule |
| --- | --- |
| `specific_job_application` | Requires an active approved profile, a selected opportunity with a current (non-stale) assessment, and `AllowsSpecificApplication()` — a strong or possible match with a family confidence other than `none`. Otherwise `blocked` with `unrelated_application_role`. Strong match is `suggested` and does not require review; possible match is always `review_required`. Unconfirmed employment interest forces review. |
| `speculative_job_enquiry` | Requires company relevance, else `ignore` with `no_company_relevance`. Targets the approved desired families. An unrelated discovery listing is recorded as provenance with an explicit reason that it is not the role sought and implies no vacancy. Unconfirmed employment interest forces review. |
| `service_outreach` | `blocked` if the approved profile refuses service work; `ignore` without company relevance; `review_required` if service permission is unknown. Never chosen because a company has a website or a payment method. |
| none given | Unknown engagement preferences → `review_required`. Both purposes plausible → `review_required` with `ambiguous_purpose`. Relevant vacancy with confirmed employment interest → application. Relevant company, no relevant vacancy, confirmed employment interest → speculative, with any unrelated match demoted to provenance. Relevant company with service permission → service outreach. Otherwise `ignore`. |
| no active profile | Every intent is `blocked` with `profile_required`. |

A stale assessment is never accepted as a current fit: `Propose` adds a `stale_assessment`
blocker and downgrades a suggestion to review, and `CreatePlan` refuses outright.

## Engagement preferences

Unknown preferences stay unknown. Nothing infers that contract work, permanent employment,
remote or on-site conditions, availability, geographic eligibility or service permission are
acceptable. Where an unknown preference prevents an authoritative choice, it is surfaced as a
review reason (`employment_preference_unknown`, `service_permission_unknown`,
`engagement_preferences_unknown`, `condition_unknown`) rather than filled in.

## UI

| Page | Shows |
| --- | --- |
| `/opportunities/{id}` | Advertised title, classification, family confidence, matcher and profile revision used, stale state and why, matched requirements with source excerpts and approved references, unmet and unknown requirements, unparsed source lines, the reasons for the classification, the blockers preventing a stronger one, and the company relevance summary. An explicit button reruns the assessment. |
| `/companies/{id}/plan` | Every vacancy with its own classification and reasons, company relevance with per-category points, the evidence table with what each fact does and does not establish, the resolved intent with reasons and blockers, ranked recipients with function and relevance, the exact capabilities and proof a future email may use, the claims it may never make, a form to record a plan, and existing plans with recipient-change, pause and close actions. |
| `/companies/{id}` | A Phase 3 panel listing each vacancy's classification and staleness, company relevance, existing plans, and links into the workspace. It states that advertised role, desired role family and email intent are three separate things, and that the legacy segment, score and application status take no part. |

New routes: `POST /opportunities/{id}/assess`, `GET /companies/{id}/plan`,
`POST /companies/{id}/assess`, `POST /companies/{id}/plans`, `POST /plans/{id}/recipient`,
`POST /plans/{id}/status`. Everything that writes an assessment or a plan is a POST; nothing is
generated on a GET. The legacy `/companies/{id}/apply-mode` control is untouched and remains a
legacy segment field with no effect on any v2 decision.

## Files changed

| Files | Purpose |
| --- | --- |
| `internal/explain/explain.go` | Shared reason type: a code, a sentence and the records it cites. |
| `internal/evidence/evidence.go`, `evidence_test.go` | Fact model, fact keys, grouping, and per-kind semantics of what a detection cannot establish. |
| `internal/opportunity/requirements.go` | Bounded requirement dictionary, section-aware conservative extraction, excerpts and offsets. |
| `internal/opportunity/match.go`, `match_test.go` | Title/family gate, requirement resolution, `fit-v1` scoring and the authoritative classification gates. |
| `internal/relevance/relevance.go`, `contacts.go`, `relevance_test.go` | Capped explainable company relevance and contact-function classification. |
| `internal/plan/plan.go`, `intent.go`, `recipient.go`, `selection.go`, `plan_test.go` | Intents, intent resolution, recipient ranking, bounded proof selection and forbidden claims. |
| `internal/assess/assess.go`, `assess_test.go` | Orchestration: load, assess, persist, propose and record plans. |
| `internal/store/evidence.go`, `match.go`, `plan.go`, `phase3_test.go` | Evidence upsert and grouping, assessment and relevance storage with staleness, contact plans and revisions. |
| `internal/store/migrations/0019_research_evidence.sql`, `0020_opportunity_matching.sql`, `0021_contact_plans.sql` | Additive schema. |
| `internal/store/schema_v2.go` | Startup checks for the new columns, triggers, partial unique indexes and the unrelated-role constraint. |
| `internal/store/migrate.go`, `cmd/prospector-migrate/main.go` | `LegacyDataHash`, `LegacyColumns`, `PreservationDiff`, and a rehearsal preservation check that is correct for additive migrations. |
| `internal/enrich/enricher.go` | Bounded, expiring, mutex-protected page cache; evidence with provenance and excerpts; product-description and engineering-request extraction. |
| `internal/web/phase3.go`, `phase3_test.go`, `opportunities.go`, `handler.go` | Phase 3 handlers, assessor wiring and page data. |
| `internal/web/templates/plan.html`, `opportunity.html`, `company.html` | Contact-plan workspace, fit panel and company summary. |
| `cmd/prospector/main.go` | Phase 3 route registration. |
| `README.md`, `docs/CURRENT_STATE.md`, implementation-plan status, this report | Current operations and completion evidence. |

## Tests executed

`go test ./... -count=1` with a writable temporary Go cache and the opt-in isolated PostgreSQL
tests enabled through `MIGRATION_TEST_ENV_FILE`. All packages pass. `gofmt -l` reports nothing
and `git diff --check` passes.

Acceptance coverage:

| # | Case | Where |
| --- | --- | --- |
| A | Marketing Manager stays unrelated; no application possible; speculative plan allowed on relevance and preferences | `opportunity`, `plan`, `assess` |
| B | Finance Officer with billing and collections text is unrelated and states no payment requirement | `opportunity` |
| C | Backend Engineer strong match with explicit family, capability and project reasons | `opportunity` |
| D | Backend partial fit keeps verified matches, shows React as an unmet gap, never claims it | `opportunity`, `plan` |
| E | Full-stack titles match the approved family; an unapproved framework stays a gap; no frontend capability is invented | `opportunity` |
| F | Missing, partial and thin descriptions cannot reach strong match | `opportunity` |
| G | Checkout and M-Pesa presence establish no relevance, no payment need and no payment offer | `relevance`, `plan` |
| H | An explicit payment-engineering vacancy allows the approved payment capability to lead | `opportunity`, `plan` |
| I | A payments project is selectable as backend proof with the payment diagnosis forbidden | `plan` |
| J | Repeated evidence groups once and cannot inflate relevance; preserved legacy duplicates group too | `evidence`, `relevance`, `store` |
| K | Marketing, Finance and Backend at one company are assessed and stored independently | `opportunity`, `store` |
| L | Unknown engagement preferences force review and are never invented | `opportunity`, `plan` |
| M | A generic mailbox is review-required and no name is inferred from it | `relevance`, `plan` |
| N | A company score of 100 does not let an unrelated role become an application | `plan`, `assess` |
| O | A manual override cannot bypass the invariant, in Go or in the database | `plan`, `assess`, `store` |
| P | Activating a new profile revision makes prior assessments stale and non-current | `store` |
| Q | A changed opportunity revision makes the prior assessment stale and requires a rerun | `store`, `assess` |

Additional regressions: ordinary English is not read as the Go language; benefits sections
produce no requirements; unparsed requirement lines stay visible; non-software engineering
disciplines are unrelated; an incidental domain word after the role designation does not
unrelate an engineering title; a known engagement conflict blocks strong match; a
`profile_required` assessment is never stored; one live plan per key with reuse after closing;
a plan revision snapshot is immutable; a cross-company contact is refused; every template
parses; the plan workspace states that it authorizes no sending.

Every mutating integration test creates its own scratch database and asserts
`current_database()` before its first write, in both `internal/store` and `internal/assess`. No
adapter test touches the live application database. No AI request, SMTP connection or worker
was started by any test.

## Live preservation verification

- Restore of the actual backup verified against the live database before any change, with
  matching schema, row counts and data fingerprints.
- Full migration rehearsal on the restored copy, reporting the added columns and proving every
  original table and sequence unchanged.
- Post-apply comparison of `.local/phase3-before.json` and `.local/phase3-after.json`: identical
  row counts and identical original-column fingerprints for all eight application tables.
- Row counts also identical to `.local/phase2-after.json` for every Phase 2 table.
- `prospector-migrate check` passes against the live database at schema 21.
- Read-only handler smoke check against live data: `/profile`, `/opportunities`, an actual
  opportunity detail and an actual company plan workspace all returned 200 without starting a
  server or a worker, and the workspace correctly stated `profile_required`.

Private ignored artifacts: `.local/phase3-preflight.dump`, its `.verified.json` and
`.rehearsal.json` reports, `.local/phase3-before.json`, `.local/phase3-after.json` and
`.local/phase3-readonly-smoke.go`. Backup archives contain private application data.

## Explicit answers

1. **Can Marketing Manager ever become a specific application?** No. The title gate classifies
   it `unrelated` before any requirement or score is read; `AllowsSpecificApplication()` returns
   false; `ResolveIntent` blocks with `unrelated_application_role`; `CreatePlan` refuses; and the
   `contact_plan` CHECK constraint refuses the row even if every Go check were bypassed.
2. **Can a numerical score override `unrelated`?** No. An unrelated classification forces the
   score to 0, and no code path consults a score when deciding whether an application is
   permitted. This is verified with a company score of 100.
3. **Can checkout or M-Pesa presence alone trigger a payment-service plan?** No. Both are
   presence-only detections that declare they cannot establish a payment problem, contribute
   nothing to relevance, and never set `payment_need_evidenced`. Without that flag the payments
   capability is excluded from selection and the payment diagnosis is a forbidden claim.
4. **Can a payment project be used as backend proof without triggering payment outreach?** Yes.
   It is selected as `proof` for the backend abilities its approved responsibilities support,
   annotated `payments_item_as_backend_proof_only`, while `recipient_payment_problem` remains
   forbidden.
5. **Does partial fit preserve verified strengths without inventing missing technologies?** Yes.
   Verified matches are retained with their references, the missing technology is an `unmet` gap
   with no profile references, it never enters `allowed_refs`, and it appears as an
   `unapproved_requirement_*` forbidden claim.
6. **Does every contact plan have an explicit intent?** Yes. `intent` is `NOT NULL` with a CHECK
   restricting it to the three values, and both the Go layer and the store validate it.
7. **Does every specific application pin a real opportunity revision?** Yes. A CHECK makes
   `opportunity_id` and `opportunity_revision` present exactly when the intent is
   `specific_job_application`, and a composite foreign key requires that pair to be a real
   `opportunity_revision` row.
8. **Can a speculative enquiry accidentally use the unrelated advertised title as the desired
   role?** No. Provenance is a separate column from the applied-for vacancy, a CHECK forbids them
   being the same, target families come only from the approved profile, and the selection emits
   an `unrelated_title_as_target_role` forbidden claim.
9. **Can multiple opportunities at one company be assessed independently?** Yes. Assessments are
   keyed per opportunity and revision, and the multi-vacancy fixture stores three distinct
   classifications for one employer.
10. **Can repeated evidence inflate relevance?** No. Relevance counts fact groups, canonical
    rows deduplicate on insert, and preserved legacy duplicates collapse into one group at read
    time. Three copies of a fact score identically to one.
11. **Can an inactive or unapproved profile produce an authoritative match or contact plan?** No.
    `ActiveProfile` returns only the active approved revision, matching returns
    `profile_required`, `SaveOpportunityMatch` refuses to store it, and every intent is blocked.
12. **Can recipient selection silently change after the plan is created?** No. The recipient is
    frozen in the plan and in an immutable revision snapshot; the `protect_contact_plan` trigger
    refuses an in-place change without a new revision, and a recipient change reopens review.
13. **Are AI, workers and sending still interlocked?** Yes. `web.EmailPipelineReady` is still
    `false`; no AI provider is constructed; the scheduler is not started; `executeSend`,
    follow-up preparation and resend all refuse before touching the database; and a test asserts
    the interlock is intact.

## Limitations and deviations

- **Migration numbering.** The design proposed `0019_research_evidence` and
  `0020_contact_plans_scaffolds`. Matching storage was split into its own `0020`, and contact
  plans became `0021`. Composition scaffolds were deliberately left out: they belong to Phase 4.
- **Package placement.** The design named `internal/draft/intent.go` and
  `internal/draft/selection.go`. The pure intent, recipient and selection rules live in
  `internal/plan` instead, because `internal/store` must persist an intent and cannot import
  `internal/draft`, which imports `store`. `internal/plan` imports no storage.
- **Rehearsal preservation check.** Changed as described above. The old byte-identical
  comparison could not express an additive column change; the replacement proves preservation of
  original rows and columns directly and reports additions explicitly.
- **Company relevance on the live database is uncomputed.** With no active profile it would
  report only that company work cannot be matched to approved capabilities, so nothing was
  stored.
- **Evidence provenance applies to new research only.** The 1,697 existing signals keep their
  original text, labelled `legacy_detection`. They were not, and cannot be, rewritten into
  quotations. Richer evidence appears only as research runs again after Phase 3 review.
- **The requirement dictionary is bounded and hand-checked.** It will not recognize every
  demand. Unrecognized lines stay visible as unknown requirements rather than being silently
  dropped, and they hold a vacancy back from `strong_match` when they are mandatory.
- **Matching quality has not been evaluated against real job descriptions.** All fixtures are
  synthetic. Running the matcher over the 38 live opportunities requires an active approved
  profile, which is a content decision for the operator.
- **Race-detector tests were not run:** this environment has `CGO_ENABLED=0` and no `gcc`.
  Concurrent database tests did run.
- **No legacy behaviour was removed.** The legacy scorer, segment, apply-mode control and
  templates all remain, visible and inert with respect to every v2 decision.

## What is needed before Phase 4

Phase 4 composes the email. It can only say something worth reading if the profile has facts to
say. Activating a profile revision with real approved technologies, capabilities, projects,
responsibilities, outcomes, resources and engagement preferences is the remaining input, and it
is a content decision only the operator can make. Until then, matching will keep answering
`profile_required`, which is the correct answer.

Phase 3 is complete and awaiting review. Phase 4 has not started.
