# Contact discovery — completion report

Second-stage contact discovery is implemented, migrated, tested and rehearsed against the live
corpus. Prospector no longer stops at "the company's website doesn't publish a useful email."

The short version of what changed: the blocker was never "no email address". It was **no
acceptable application or contact destination**, and those are different problems with different
answers. A vacancy whose employer publishes a Greenhouse link has somewhere perfectly good to
send an application and no mailbox at all. That distinction is now the definition the whole
pipeline works from.

---

## 1. Architecture

`contact_discovery` is a separate worker and a separate package. It is independent of opportunity
discovery, company research, composition, sending and automation.

```
company / opportunity
  → website research (internal/enrich)
  → contacts classified (internal/relevance)
  → no acceptable destination?           ← internal/plan.HasAcceptableDestination
  → queued                               ← contact_discovery_task
  → employer identity gate               ← contactsearch.EstablishIdentity
  → employer's own pages, then public search
  → candidates + evidence stored         ← contact_candidate, contact_candidate_evidence
  → existing recipient policy decides    ← internal/plan.RankRecipients
  → usable / review_required / unusable
```

It does not duplicate normal company crawling: it reads pages through
`enrich.Enricher.FetchPublicPage`, which is the same rate limiter, the same shared Postgres page
cache, the same timeout and the same size and content-type limits company research already uses.
A second fetcher would mean a second set of limits to keep in step, and the one that drifted
would be the one hitting somebody else's server.

Three boundaries are structural, not conventional:

- **It discovers; it does not decide.** Every candidate is classified by `internal/relevance` and
  judged by `internal/plan`. This package contains no second opinion about what makes a valid
  recruiter contact, because two such opinions eventually disagree and the looser one wins by
  accident.
- **It records facts; it does not invent them.** A candidate exists only where a source excerpt
  literally contains the fact claimed. `contactsearch.Ground` enforces this on every candidate
  from every origin.
- **It supplies data; it authorizes nothing.** Finding a recruiter does not reclassify an
  opportunity, create a plan, change intent or permit a send. Every Phase 3–7 gate is untouched.

### Files

New:

| File | Lines | Purpose |
|---|---|---|
| `internal/contactsearch/contactsearch.go` | 260 | Candidate/evidence model, source trust ranking, deduplication identity |
| `internal/contactsearch/provider.go` | 176 | `SearchProvider` abstraction, Google CSE, Null and Static implementations |
| `internal/contactsearch/identity.go` | 195 | Employer identity gate |
| `internal/contactsearch/extract.go` | 713 | Deterministic extraction and the grounding check |
| `internal/contactsearch/classify.go` | 143 | Classification via `relevance`, confidence, promotion decision |
| `internal/contactsearch/targets.go` | 161 | Intent-sensitive search strategy and ranking |
| `internal/contactsearch/inference.go` | 178 | Email-format detection and fenced-off inference |
| `internal/contactsearch/budget.go` | 223 | Dedicated daily budget and kill switch |
| `internal/contactsearch/discover.go` | 893 | The pipeline; one implementation for worker, UI and CLI |
| `internal/plan/destination.go` | 177 | Destination resolution — the "acceptable destination" policy |
| `internal/store/contactdiscovery.go` | 714 | Queue, candidates, evidence, eligibility, counts |
| `internal/store/migrations/0026_contact_discovery.sql` | 175 | Schema |
| `internal/web/contactdiscovery.go` | 243 | Manual action, detail view, configuration |
| `internal/web/templates/contact_discovery.html` | 160 | Detail page |
| `internal/web/static/contact-discovery.css` | 200 | Detail page styling |
| `cmd/prospector-contactsearch/main.go` | 264 | Rehearsal tool |
| tests | 1,275 | `contactsearch`, `store`, `web` |

Modified: `cmd/prospector/main.go` (routes, worker wiring, interval config),
`internal/worker/scheduler.go` and `status.go` (the loop), `internal/web/handler.go` (engine
construction and accessor), `internal/web/dashboard.go` + `internal/store/dashboard_snapshot.go`
(counts, card, next step), `internal/web/navigation.go` + `internal/store/opportunity_browse.go`
(filters), `internal/enrich/enricher.go` (exported bounded fetch, response limits, UTF-8
sanitizing), `internal/relevance/contacts.go` (one keyword gap), `internal/store/schema_v2.go`,
`internal/web/shell.go` (page registered with the application shell), four templates, `.env.example`.

Note on the working tree: the navigation/search redesign was already present uncommitted when
this work started (`internal/web/navigation.go`, `internal/store/opportunity_browse.go`,
`workflow_browse.go`, `docs/NAVIGATION_SEARCH_REDESIGN.md` and related template edits). The
contact-discovery filters extend that work; its other changes are not mine and are untouched.

---

## 2. Queue and state model

`contact_discovery_task`, keyed by **company** — "find a recipient at this employer" is a question
about the employer, so two strong matches at one company are one search, not two.

States: `pending`, `claimed`, `searching`, `completed`, `partial`, `no_result`,
`review_required`, `identity_blocked`, `failed`.

Persisted per company: `attempts`, `last_searched_at`, `next_eligible_at`, `result_summary`
(jsonb), `failure_reason`, `eligibility_reason`, `intent`, `opportunity_id`, `plan_id`,
`priority`, and a durable claim (`claimed_by` + `claim_expires_at`) so a worker and a manual
request can never search the same company at once. An expired claim is recoverable, so a crashed
worker does not block a company forever.

### Eligibility

A company becomes eligible when it has a **live strong match**, an **open contact plan**, or a
**possible match a person explicitly selected** — and, evaluated in Go by the recipient rules, has
no acceptable destination. Priority is ascending: strong match (1), open plan (2), selected
possible (3).

An unrelated opportunity never queues a search because its employer publishes no address. That
rule is what keeps a scarce budget on work that matters.

Whether a company actually lacks a destination is deliberately *not* decided in SQL. The
eligibility query narrows the population; `plan.HasAcceptableDestination` answers the question,
using the same classifiers the send path uses.

---

## 3. Search provider abstraction

```go
type SearchProvider interface {
    Name() string
    Enabled() bool
    Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
}

type SearchResult struct {
    Title, URL, Snippet, Provider string
    ObservedAt time.Time
    Rank       int
}
```

Implementations: `GoogleProvider` (Custom Search JSON API — chosen only because the project
already holds credentials for it), `NullProvider`, and `StaticProvider` for tests. Nothing above
this interface knows which engine answered.

`Enabled() == false` is an ordinary state, not an error. The highest-trust evidence in this
pipeline — the job posting and the employer's own careers page — is reached by direct fetch. A
missing search engine narrows what can be discovered; it does not stop discovery. That is not a
theoretical claim: the live rehearsal ran with no search credentials configured and still resolved
a company.

---

## 4. Evidence and provenance model

`contact_candidate_evidence` holds one row per observation, with source type, the numeric trust in
force when it was made, source URL, **the exact excerpt**, the discovery method, the provider,
whether the source is employer-owned, and the observation time. It is append-only: a database
trigger refuses `UPDATE` and `DELETE`, and re-observing the same source refreshes `observed_at`
through a unique key rather than inserting a row that would read as independent corroboration.

Trust ranking:

| Source | Trust | Employer-owned |
|---|---|---|
| `job_posting` | 100 | yes |
| `source_metadata` | 95 | yes |
| `employer_careers_page` | 90 | yes |
| `employer_team_page` | 80 | yes |
| `employer_page` | 70 | yes |
| `public_profile` | 50 | no |
| `third_party` | 30 | no |
| `search_snippet` | 10 | no |

`TrustFloorForPublishedEmail = 70`. A snippet-only candidate is kept as the real lead it is, but
is capped at low confidence and can never become a contact row.

Every candidate stores: company, name (if known), role, email (if known), source URL, source type,
source excerpt, discovery method, provider, observed time, confidence, employer-owned flag,
published-vs-inferred provenance, normalized value, recipient function classification, and
endpoint classification. `SaveContactCandidate` refuses a candidate with no evidence.

---

## 5. Contact candidate model, and why it is separate

`contact_candidate` is deliberately **not** the `contact` table, and the current schema could not
safely represent these states directly:

- a person with no email at all — `contact` requires a value that passes `NormalizeContactValue`;
- an email whose owner is unknown;
- an inferred address, which must never be visible to the send path;
- a conflicting employer identity;
- duplicate evidence resolving to one candidate.

`contact` also has no provenance columns, so promoting everything into it would mean operational
recipients with unexplained origins — the precise thing this work exists to stop. Candidates are
promoted into `contact` only after passing every deterministic check, and the promoted row names
the candidate it came from (`promoted_contact_id`), so a contact the send path sees can always be
traced back to the page that published it.

Candidate kinds stay distinct all the way to the database: `application_email`,
`application_url`, `person`, `mailbox`. Collapsing them is what produces invented people.

### Deduplication

Identity keys, strongest first: normalized email → source-published profile URL → normalized
person name + company domain. Multiple evidence rows resolve to one candidate; merging fills gaps
and never overwrites a recorded fact with a later reading of it, and the stronger publication
claim wins. Name normalization collapses case and punctuation but never surnames or particles, so
"Jan de Vries" and "Jan de Vroom" stay two people. When a person already found without an address
is found again with a published one, the weaker record is marked `superseded`, not deleted.

---

## 6. Email inference policy

Off by default. `CONTACT_DISCOVERY_INFER_EMAILS` must be exactly `"true"`.

A format is read only from a published address **paired with a separately evidenced person**:
`john.smith@example.com` only demonstrates `firstname.lastname` once you know a John Smith works
there. Without that pairing the "pattern" is a guess about a guess, so `DetectEmailFormat`
returns nothing.

An inferred address is stored with provenance `inferred_pattern`, verification `inferred`,
confidence `low`, and evidence that states plainly: *"Address not published. Derived from this
employer's observed address format …, read from the published address …, applied to the separately
evidenced name …"*. It is held at `review_required` and never auto-promoted. First-name-only
formats are refused outright, because they cannot distinguish two colleagues who share a first
name.

Two CHECK constraints make this structural rather than conventional:

```sql
CHECK (NOT (email_provenance = 'inferred_pattern' AND verification IN ('published','source_confirmed')))
CHECK (NOT (email_provenance = 'inferred_pattern' AND status = 'promoted'))
```

`PromoteContactCandidate` also refuses one in Go. Both are tested.

---

## 7. Verification states

`published`, `source_confirmed`, `inferred`, `unverified`, `bad`, `generic`,
`not_human_mailbox` — terminology consistent with the existing `relevance` vocabulary.

None of these asserts that a mailbox exists. **No mailbox probing is performed at all**: no SMTP
`RCPT` probing, no MX-existence-as-verification. The strongest honest claim this pipeline makes is
who published an address and where. An address is never called verified because a domain resolves.

---

## 8. Company identity gate

Contact discovery is refused until the employer is established. `EstablishIdentity` requires
`domain_status` of `verified` or `manual`, and refuses:

- a missing domain (`company_domain_missing`);
- a job board, ATS or social platform as the "employer" domain (`company_domain_is_aggregator`);
- a guessed/candidate domain (`company_domain_unverified`);
- a **name/domain mismatch** (`company_name_domain_mismatch`).

The last one is the rule that matters, and it came straight from production. `Wakacje.pl` is
stored with the domain `wakacje.com`, and that domain **passed verification** — the verifier
checks that the company's name appears on the page, and "wakacje" appears on both. Searching for
the recruiters of `wakacje.com` would have been searching a different company's staff, and the
resulting contacts would have looked perfectly well-evidenced while being about the wrong
employer.

So when a company's own name states a domain, that domain is authoritative and a stored domain
disagreeing with it is a mismatch rather than a detail. The same check runs from the other side:
a published `employer_url` that disagrees with the stored domain is the stronger statement of the
two and also blocks.

This is narrow and checkable. It does not attempt to prove every domain correct — it refuses the
cases that are demonstrably wrong.

---

## 9. Intent-sensitive search

`StrategyFor(intent, name, domain)` produces different queries, different preferred functions and
a different answer to "does an apply route count":

| Intent | Prefers | Accepts application route |
|---|---|---|
| `specific_job_application` | recruiting → engineering → leadership | **yes** |
| `speculative_job_enquiry` | recruiting → engineering → leadership | no |
| `service_outreach` | engineering → leadership | **no** |

Service outreach never searches for a recruiter and never accepts an ATS route: an applicant
tracking system is where candidates apply, and a services pitch in a hiring queue is both useless
and rude. `RankForIntent` scores an application route `+1000` when the intent accepts one and
`-500` when it does not.

Where no plan exists, no intent is invented. The destination is judged conservatively as a
speculative enquiry would judge it, and the *vacancy's* application readiness is computed
separately (below) — answering a question about a vacancy that already exists rather than choosing
an intent.

---

## 10. Application routes and the corrected definition of the blocker

**"Missing contact" is not "no email".** The blocker is *no acceptable application or contact
destination*.

`plan.ResolveDestination` returns, in order, for a specific application:

1. the job-specific application route (`apply_url`, or an employer-published route this pipeline
   discovered);
2. the published application address, when it is a mailbox a person reads;
3. the best stored contact the recipient rules accept.

For speculative and service intents, steps 1 and 2 do not apply at all.

ATS providers recognized: Greenhouse, Lever, Ashby, Workday, Workable, SmartRecruiters, Jobvite,
Recruitee, Breezy, BambooHR, Teamtailor, Pinpoint, Rippling, Personio, Join — plus employer-owned
apply paths.

A route discovered on the employer's own careers page counts exactly as much as one the job board
recorded: both are the employer saying where to apply, and only the place we read it differs. This
is reflected in the opportunity filters and the dashboard too, so a company the pipeline has
resolved stops being reported as blocked.

---

## 11. Worker scheduling, cooldown and budget

The `contact-discovery` loop runs in `internal/worker` alongside discovery, research and domain
resolution. Default interval 45 minutes — the slowest loop, because per-company cooldowns are
measured in days and checking faster only re-reads an unchanged queue. Ticks never stack.

Cooldowns (all configurable):

| Outcome | Default | Reasoning |
|---|---|---|
| destination found | 30 days | nothing to re-search until it goes stale |
| partial / review required | 3 days | there is something to come back to |
| no result | 7 days | not minutes — the answer will not differ tomorrow |
| identity blocked | 14 days | it will not resolve until a human fixes the domain |
| transient failure | 6 hours, max 4 attempts | bounded retry |
| attempts exhausted | 14 days | stop asking |

Dedicated daily budget, separate from the AI and domain-search budgets so they cannot starve each
other. Defaults: 10 companies, 30 searches, 60 page fetches, **0 AI calls**. Tracked: companies
searched, search requests, pages fetched, AI calls, contacts found, routes found, recruiters
found, engineering contacts found, people found without email, no-result searches, failures,
identity blocks. A Redis kill switch (`contactsearch:paused`) takes effect on the next company,
not the next restart.

---

## 12. AI use

No AI provider is wired in, and none is needed: deterministic extraction is sufficient, and
`CONTACT_DISCOVERY_AI_CALLS_PER_DAY` defaults to 0 so AI is opt-in rather than opt-out.

The guarantee that would govern an AI extractor is already implemented and enforced for
*everything*: `contactsearch.Ground` drops any candidate whose asserted email, person name or
application URL is not literally present in its own evidence excerpt. It applies to the
deterministic extractors exactly as it would to a model's output, because the promise wanted is
not "our extractors are careful" but "a contact cannot exist here without a source that says so".
Tested directly (test R).

---

## 13. Privacy and scope

Only publicly accessible information. Login-gated platforms are **skipped, never worked around**:
LinkedIn, Facebook, Instagram, X, Glassdoor, Xing, Crunchbase, ZoomInfo, Apollo, RocketReach,
Hunter, Lusha, SignalHire, ContactOut. No authentication bypass, no anti-bot evasion, no leaked or
breach data.

Stored per person: name, role/title, employer, business email if published, source. Nothing else —
no personal phone numbers, home addresses, private emails or profile data.

---

## 14. UI

- **`/companies/{id}/contact-search`** — the detail page, led by the verdict rather than by the
  machinery. A coloured banner states the one thing that matters — *this vacancy can be applied
  to* / *a recipient exists but a person must confirm it* / *there is nowhere acceptable to send
  anything* / *employer identity is not established* — followed by three status tiles (employer
  identity, application route, purpose being searched for) and the **Find contact** action.

  Both destinations are shown, because they are different questions: where an *application* would
  go, and where *outreach* would go. Delinea reads "This vacancy can be applied to — published
  application route" alongside "Outreach would reach Unidentified mailbox — sales@delinea.com,
  which requires review". Showing only the second understates what is possible; showing only the
  first would overstate it.

  Each candidate is a card with its status, function, confidence and provenance class as badges,
  the facts as key-values, an inline caveat where one applies ("A person and a role were found. No
  address was published for them, and none is guessed from a name."), and full provenance in a
  collapsible block: source type, trust, URL, **the exact excerpt**, discovery method and
  observation time. Provenance is collapsed so a page with many candidates stays readable — never
  hidden behind "AI found this". Queue state, capability and budget sit in a collapsed
  diagnostics panel underneath, since they are secondary to the verdict. The page is a GET and
  searches nothing.

- **"Find contact"** — a POST on the company page, the contact-plan workspace and the opportunity
  page. All three enqueue and run the same pipeline the worker runs; there is no ad-hoc manual
  path. The manual action's one extra effect is clearing the cooldown, because an explicit request
  is information the automatic schedule did not have.
- **Dashboard** — a `Missing a destination` metric, a next-step card reading
  `6 strong matches · N missing an acceptable destination` linking to
  `/opportunities?fit=strong_match&recipient=missing`, an identity-blocked problem line, and the
  `contact-discovery` worker in the research status card.
- **Filters** — `recipient=acceptable|missing|generic` and
  `contact_search=pending|searching|completed|partial|review_required|no_result|identity_blocked|failed`,
  integrated with the existing navigation work and surviving pagination round-trips.

---

## 15. Migrations

`0026_contact_discovery.sql`, strictly additive. Three new tables
(`contact_discovery_task`, `contact_candidate`, `contact_candidate_evidence`), two immutability
triggers, no existing table, row, index, constraint or trigger modified.

Applied to the live database through the project's own gate: `backup-verify` (pg_dump, restore
into an isolated scratch database, schema/row-count/fingerprint comparison) produced a proof, then
`apply -proof`. Company count before and after: **600**. Contact count unaffected by the
migration.

---

## 16. Tests

93 tests across the three packages, all passing, plus the full existing suite.

| | Case | Covered by |
|---|---|---|
| A | Strong match, no contact → eligible | `TestStrongMatchIsEligibleForContactDiscovery` |
| B | Unrelated opportunity → not queued | `TestUnrelatedOpportunityIsNotQueuedForMissingContact` |
| C | Generic mailbox only → discovery still allowed | `TestGenericMailboxOnlyLeavesCompanyBlocked` |
| D | Published application email → stored with provenance | `TestPublishedApplicationRoutesArePreservedWithProvenance` |
| E | Published application URL → valid destination | same, + `TestApplicationURLMakesAJobApplicationDestinationReady` |
| F | Recruiter found publicly | `TestRecruiterFoundWithoutAddressYieldsPersonOnly` |
| G | Person found, no email → no guessed address | same |
| H | Known pattern → marked `inferred`, never published | `TestInferredAddressIsNeverPublishedOrPromotable`, `TestInferredAddressCannotBePublishedOrPromoted` |
| I | Snippet claims an address the page does not | `TestSnippetOnlyEvidenceCannotProduceAUsableContact` |
| J | Wrong/ambiguous domain → refused | `TestAmbiguousCompanyIdentityRefusesContactDiscovery` |
| K | Sentry/telemetry endpoint → unusable | `TestTelemetryEndpointRemainsUnusable` |
| L | Careers mailbox → not a named recruiter | `TestCareersMailboxIsNotANamedRecruiter` |
| M | Commercial address → unsuitable for an application | `TestCommercialAddressIsNotSuitableForAJobApplication` |
| N | Same contact from several sources → one candidate | `TestSameContactFromMultipleSourcesIsOneCandidateWithAllEvidence`, `TestCandidateKeepsEveryEvidenceRecord` |
| O | Rerun after no result → cooldown | `TestCooldownPreventsImmediateRepeatSearch` |
| P | Manual "Find contact" → same queue/pipeline | `TestManualRequestUsesTheSameQueueAndClearsCooldown` |
| Q | Provider failure → bounded, visible, no infinite retry | `TestSearchProviderFailureIsBoundedAndHonest` |
| R | Extractor invents an unsupported email → rejected | `TestUnsupportedFactsAreRejectedRegardlessOfOrigin` |
| S | Apply URL → destination-ready with no email | `TestApplicationURLMakesAJobApplicationDestinationReady`, `TestDiscoveredApplicationRouteCountsInTheOpportunityFilters` |
| T | Intent-sensitive search | `TestSearchStrategyDiffersByIntent` |

Beyond the required list: generic mailbox retention and ranking, similar names not merged, page
furniture not read as a person, login-gated hosts skipped, claim exclusivity and expiry, evidence
immutability, candidate identity immutability, filter/policy agreement, inference off by default,
budget defaults, and three regression tests for defects the live rehearsal found (below).

---

## 17. Live rehearsal

Run against the real corpus: 600 companies, 280 opportunities, 6 strong matches. Read-only with
respect to sending; no SMTP path exists in the rehearsal binary.

```
companies examined                     6
searches run                           0   (no search provider configured)
pages fetched                         40
published application routes found     1
published emails found                 0
recruiting people found                1
engineering people found               0
people found without an email          1
inferred addresses                     0   (inference disabled)
promoted to contacts                   0
gained an acceptable destination       1
still unresolved                       5
refused on employer identity           1
search provider failures               0
emails sent                            0
```

Per company:

| Company | Outcome | Detail |
|---|---|---|
| **Delinea** | `completed` | Discovered the employer-published Ashby route `jobs.ashbyhq.com/delinea` on its own careers page (trust 90, high confidence) — **the vacancy is now destination-ready with no email address**. Also found *Michele Eaton — People Operations*, correctly held: no address was published, and none was guessed. |
| **Wakacje.pl** | `identity_blocked` | `company_name_domain_mismatch` — the exact production failure the gate exists for. No search was performed against `wakacje.com`. |
| **Cosuno** | `no_result` | Only `info@cosuno.de` on file; no route published on its own pages; no search engine to go further. |
| **Sticker Mule** | `no_result` | Only `help@stickermule.com`. |
| **Wonderdog** | `no_result` | Only unidentified mailboxes. |
| **Kruger NearShore LLC - Rekluti** | `no_result` | No contact of any kind, no published route. |

Nothing was sent. No contact plan was created. Two candidates and two evidence rows were stored;
the `contact` table was not modified by contact discovery (no candidate cleared the promotion bar,
which is the correct outcome for this corpus rather than a failure).

### Defects the rehearsal found, and fixed

The rehearsal earned its keep. Four real defects surfaced only against live pages:

1. **Invalid UTF-8 aborted whole searches.** A page served as Latin-1 puts a bare `0xa0` where a
   non-breaking space belongs. Postgres refuses a byte that is not a character, so one of them
   anywhere on a page aborted the write of everything extracted from it. Fixed in
   `enrich.pageText` (one implementation, so company research is fixed too), with sanitizing
   defence at the store boundary. It becomes a space rather than a replacement character, because
   an unreadable byte separating two words must not glue them into a token nobody wrote.
2. **A substring match invented a person.** `"Contractors"` contains `"cto"`, and the live corpus
   duly produced a person called *"Features Cosuno"* whose title was *"cto"*, read out of a
   navigation menu. Title matching is now whole-word. Short titles are the most valuable to match
   and the most dangerous to match loosely.
3. **A title supplied its own words as a name.** *"Talent Acquisition Professional"* produced a
   person called *"Acquisition Professional"* — a job description with capital letters — because
   the name search began at the title's first space instead of its end.
4. **"Not configured" was reported as "failed".** An unconfigured search provider produced five
   phantom provider failures in the first rehearsal. A capability gap and a failure are now
   separate fields.

A fifth defect was found by looking at the page rather than the data: **the contact discovery
page was not registered in `applicationShell`**, so it rendered as bare HTML — no stylesheet, no
sidebar, no layout. Nothing failed, because nothing was checking: the shell returns a page
untouched for a name it does not recognize, which is right for a fragment and silently wrong for a
page. `shell_test.go` now asserts that every page template carrying no styling of its own is
registered, and that the shell actually injects the stylesheets and navigation. The two older
self-styled pages (`review.html`, `followups.html`) carry complete inline styling and stand on
their own, so they are exempt by the rule rather than by being named in it.

All four data defects have regression tests using the exact live text that produced them. Two smaller fixes:
`"/careers"` and `"/careers/"` were both being fetched, spending two of the page budget and
recording one observation twice; and route evidence was a raw slice of markup, now the visible
link text plus the stated link target.

---

## 18. Production rollout

The worker runs in normal manual mode alongside discovery, research and enrichment, gated only on
`EMAIL_PIPELINE_MODE != disabled`. **Automation being off does not stop it**, for the same reason
research does not stop: an operator working manually still needs somewhere to send an application,
and gating contact discovery on send automation would leave the bottleneck exactly where it was.

Scheduling and budget are conservative by default (45-minute check, 10 companies/day). Email
inference is off. AI extraction is off.

---

## 19. Live-data impact

- Schema: 3 tables added; 600 companies, 1,097 contacts, 280 opportunities unchanged.
- `contact_discovery_task`: 6 rows — 1 completed, 1 identity-blocked, 4 no-result.
- `contact_candidate`: 2 rows; `contact_candidate_evidence`: 2 rows.
- `contact`: unmodified by contact discovery.
- Strong matches missing an acceptable destination: **6 → 5**.
- Emails sent: **0**.

---

## The fifteen questions

**1. Does Prospector now search beyond the company website for recipient information?**
Yes. It reads the job posting and the source's structured route fields, the employer's own
careers/team/about/contact pages, and — when a search provider is configured — the public web
through a provider-abstracted interface, following promising results to the actual page rather
than trusting snippets. In the rehearsal no search provider was configured, and it still found an
application route on an employer's own careers page that nothing previously looked at.

**2. When does the contact-discovery worker run?**
Every 45 minutes by default, whenever the rollout mode is not `disabled`. Each tick takes up to 10
eligible companies, best first, subject to the daily budget and each company's own cooldown. It
also runs immediately on a manual "Find contact".

**3. Can it run when send automation is off?**
Yes. It is gated only on the rollout mode, never on automation. It is in the same group as
discovery, research and domain resolution — the loops that describe companies and never send.

**4. Can it guess an email and treat it as verified?**
No. Inference is off by default; when enabled, an inferred address is stored with provenance
`inferred_pattern`, verification `inferred`, low confidence, and evidence stating that nobody
published it. Two CHECK constraints refuse to store it as published or promoted, and the Go
promotion path refuses it independently.

**5. Can a person be discovered without an email?**
Yes, and this is the normal case. A person and an address are separate facts with separate
provenance. The rehearsal found *Michele Eaton — People Operations* at Delinea with no address,
stored her as a person candidate, and guessed nothing.

**6. Is provenance stored for every candidate/contact?**
Yes. Every candidate carries at least one evidence row with source type, trust, URL, exact
excerpt, discovery method, provider, employer-owned flag and observation time. A candidate with no
evidence is refused at the store boundary, and `Ground` drops any candidate whose claim is not
literally in its own excerpt. A promoted contact names the candidate it came from.

**7. Can a job application be destination-ready through an apply URL without an email?**
Yes — this is the corrected definition of the blocker, and it is what resolved Delinea in the live
rehearsal with no address involved.

**8. Can a generic mailbox still be retained without becoming preferred?**
Yes. Generic mailboxes are kept and classified honestly as `generic`. Promotion never deletes
them; `plan.RankRecipients` places a named recruiter above them. Tested both ways.

**9. Can an ambiguous company identity trigger contact research?**
No. `EstablishIdentity` runs before any network request. Wakacje.pl was refused in the live
rehearsal, and no request was made to `wakacje.com`.

**10. Are inferred addresses ever auto-send eligible?**
No. They stop at `review_required`, cannot become `contact` rows, and therefore never reach the
send path at all — enforced by the database, not only by the caller.

**11. Does the worker search differently depending on intent?**
Yes: different queries, different preferred functions, and a different answer to whether an
application route counts. Service outreach never searches for a recruiter and never accepts an ATS
route. Where no plan exists, no intent is invented.

**12. Can a search result or AI output create an unsupported email?**
No. `Ground` rejects any candidate whose email, name or URL is absent from its own evidence
excerpt, regardless of origin. A snippet-only candidate is additionally capped at low confidence
and below the promotion trust floor.

**13. How many current strong matches gained an acceptable destination in the live rehearsal?**
One of six — Delinea, via the employer-published Ashby application route. Strong matches missing
an acceptable destination went from 6 to 5.

**14. What remains the dominant contact-discovery blocker?**
Two things, in order.

First, **no search provider is configured**. `GOOGLE_SEARCH_API_KEY`/`GOOGLE_SEARCH_CX` are unset,
so five of six companies were examined using only their own pages and no public search ran at all.
Setting those two credentials is the single highest-value next step, and the code needs no other
change to use them.

Second, and not fixable by more searching: **these employers publish only generic mailboxes**.
Cosuno publishes `info@`, Sticker Mule `help@`, and neither names a recruiter or an application
route anywhere on its own site. For those, the honest outcome is a generic mailbox at
`review_required`, or applying through the job board the posting came from.

A third, smaller blocker is worth naming: two of the six (Wonderdog, and Wakacje.pl behind the
identity gate) look like wrong-domain matches of the same kind the gate is built to catch. The
identity rule only fires when the company's name itself states a domain; `Wonderdog → wonderdog.com`
cannot be refused by that rule and is best fixed by correcting the domain.

**15. Were any emails sent during implementation?**
No. Zero. No SMTP connection was opened, no draft was composed, no contact plan was created, and
no approval was recorded. The rehearsal binary imports no sender and reports `emails_sent: 0`
explicitly rather than leaving it to be assumed.
