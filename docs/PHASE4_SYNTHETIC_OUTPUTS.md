# Phase 4 offline evaluation outputs

Date: 2026-09-12

**These are real model outputs, not authored fixtures.** Every email below was produced by the
actual composer and assessed by the actual semantic reviewer, run through
`.local/phase4-eval.go` against synthetic fixtures.

- Composer: `gemini` / `gemini-3.5-flash`, prompt policy `composition-v1`
- Reviewer: `gemini` / `gemini-3.5-flash`, prompt policy `email-review-v1`
- Every candidate, company, role and project fact is invented for this exercise. None of it is
  Anthony's approved profile, and no live company data was read.
- No email was sent. No scheduler ran. `EmailPipelineReady` remains `false`.
- Signatures are appended by Go after generation, which is why they appear in each body.

An earlier version of this file contained hand-authored emails checked by a scripted "clean"
reviewer. That demonstrated the contracts and nothing about quality. It has been replaced.

## Part 1: composition

Twelve fixtures, each differing in which evidence is strongest, so the question "does the
message adapt, or is it one template with nouns swapped?" can actually be answered.

All twelve reached `reviewed`: deterministic validation clean and the real reviewer passing.
Lengths ran 84–135 words against the 100–220 guidance. One required a repair.

### 1. Backend API role — Go APIs and PostgreSQL

Plan: `specific_job_application`, Northwind Systems, advertised role **Backend Engineer**,
recipient `jobs@northwind.example` (recruiting).
Selected capabilities: Backend API development; Relational data modelling.
Selected proof: Synthetic Feeds.
Forbidden: `recipient_payment_problem`, `unselected_profile_item`.

> **Subject:** Application for Backend Engineer
>
> Hello,
>
> I am writing to apply for the Backend Engineer position at Northwind Systems.
>
> My work focuses on building Go HTTP APIs and designing PostgreSQL schemas. In my project,
> Synthetic Feeds, I built the ingestion API and its PostgreSQL schema, including tracked
> migrations to manage database updates. When building REST services in Go, I focus on
> implementing thorough request validation and clear error responses. For relational data, I
> design schemas using strict constraints and indexes to ensure data integrity.
>
> My CV is available on request. I would love to discuss how my backend engineering experience
> aligns with your team's goals.

Validation: clean. Semantic review: passed, no findings. Attempts: 1. 109 words.

### 2. Distributed backend role — event-driven processing, workers, transactional correctness

Plan: `specific_job_application`, Halyard Data, **Backend Engineer**.
Selected capabilities: Event-driven processing; Transactional correctness. Proof: Synthetic Feeds.

> **Subject:** Backend Engineer Application
>
> I am writing to apply for the Backend Engineer position at Halyard Data. I understand your
> team needs to ensure correct transaction boundaries and exactly-once behavior within your
> asynchronous processing platform.
>
> In my work on Synthetic Feeds, I built a scheduled worker pipeline to process multi-source
> ingestion. The primary challenge was making sure repeated observations were idempotent. To
> solve this, I set up explicit transaction boundaries, row locking, and idempotency keys so
> that background workers can safely retry queue tasks without applying the same work twice.
>
> I would be happy to share my CV if you want to review my full background. Let me know if you
> are available for a brief call to discuss how my experience fits the team's needs.

Validation: clean. Semantic review: passed. Attempts: 1. 131 words.

### 3. Integration-heavy role — third-party APIs, webhooks, service contracts

Plan: `specific_job_application`, Coastline Connect, **Backend Engineer**.
Selected capabilities: Third-party integrations; Backend API development. Proof: Synthetic Feeds.

> **Subject:** Application for Backend Engineer - Coastline Connect
>
> I am writing to apply for the Backend Engineer position at Coastline Connect.
>
> I focus on building stable third-party integrations and robust backend APIs. In my Synthetic
> Feeds project, I built a multi-source ingestion service where I integrated several external
> sources with differing formats and failure behaviors. When working with APIs, I deliberately
> define and version service-to-service contracts, and I make sure to verify inbound webhooks
> before acting on them.
>
> I can share my CV on request if you would like to review my background. What is the best next
> step for us to connect?

Validation: clean. Semantic review: passed. Attempts: 1. 104 words.

### 4. Full-stack role — React/Next.js plus API integration

Plan: `specific_job_application`, Bramble Retail, **Full-Stack Developer**.
Selected capabilities: React and Next.js interfaces; Backend API development. Proof: Synthetic Console.

> **Subject:** Application for Full-Stack Developer
>
> I am writing to apply for the Full-Stack Developer role at Bramble Retail.
>
> My background matches your focus on owning features end-to-end across frontend and backend
> layers. I build React and Next.js interfaces as well as the backend HTTP APIs that those
> interfaces consume.
>
> For example, in my work on the Synthetic Console project, I built both the Next.js operations
> interface and the API behind it. Handling both sides of the integration allowed me to design
> clean API contracts and deliver a cohesive operations console without needing to hand off work
> across a team boundary.
>
> I would be glad to share my CV if you would like to review my background. Please let me know
> if we can schedule a brief call to discuss the role.

Validation: clean. Semantic review: passed. Attempts: 1. 135 words. Backend strength is present
without an "although I am full-stack" disclaimer, and no unapproved frontend technology appears.

### 5. Data-heavy backend role — schema, history, transactional processing (repair occurred)

Plan: `specific_job_application`, Meridian Analytics, **Backend Engineer**.
Selected capabilities: Relational data modelling; Transactional correctness. Proof: Synthetic Feeds.

> **Subject:** Application for Backend Engineer
>
> I am writing to apply for the Backend Engineer position at Meridian Analytics.
>
> In my work on the Synthetic Feeds project, I designed the revision tables that keep every
> earlier version of a record readable. I design normalized PostgreSQL schemas with constraints,
> indexes, and immutable history tables. I also keep multi-table changes inside one transaction
> so partial writes cannot be observed.
>
> My CV is available on request. What are the next steps in your process?

Validation: clean. Semantic review: passed. **Attempts: 2** — the first draft did not pass and
the bounded repair loop produced this one. 84 words. The per-attempt findings for this run were
not captured; the harness now records every attempt, but re-running to capture them is blocked
by the provider quota described below.

### 6. Backend role using a payments-domain project as proof — domain omitted entirely

Plan: `specific_job_application`, Verity Logistics (shipment tracking), **Backend Engineer**.
Selected capabilities: Backend API development; Transactional correctness.
Proof: **Synthetic Ledger**, whose `domains` field is `["payments"]`.
Forbidden: `recipient_payment_problem`, `unselected_profile_item`.

> **Subject:** Application for Backend Engineer
>
> I am writing to apply for the Backend Engineer role at Verity Logistics.
>
> In my previous work on Synthetic Ledger, I built the Go service layer, its PostgreSQL schema,
> and the transaction boundaries managing multi-step state changes. This required designing
> explicit HTTP API contracts and implementing idempotent handlers to ensure retries are
> completely safe.
>
> This experience with robust Go and PostgreSQL services aligns closely with your team's focus
> on reliable shipment tracking services.
>
> I would be happy to share my CV if you would like to review it. What are the next steps in
> your hiring process?

Validation: clean. Semantic review: passed. Attempts: 1. 106 words.

**This is the case the review asked for.** The word "payments" does not appear. Neither does
M-Pesa, checkout, billing, settlement or reconciliation. The project is described by the
engineering it required, and its industry is simply left out because the industry is not the
point. Compare directly with example 7.

### 7. Genuine payment-engineering role — payment experience becomes the fit explanation

Plan: `specific_job_application`, Tandem Pay, **Payments Backend Engineer**. The vacancy itself
states the payment requirement, so payment need is evidenced and the
`recipient_payment_problem` forbidden claim is **absent** from this plan.
Selected capabilities: Payment integrations; Transactional correctness. Proof: Synthetic Ledger.

> **Subject:** Application for Payments Backend Engineer
>
> I am writing to apply for the Payments Backend Engineer position at Tandem Pay.
>
> In my Synthetic Ledger project, I built Go and PostgreSQL services with transactional
> workflows. I integrated a payment provider, handled asynchronous settlement callbacks, and
> built the reconciliation job. To ensure correctness, I made the callback handling idempotent
> so that a repeated provider notification cannot double-apply.
>
> I can share my CV on request if you would like to discuss this work further.

Validation: clean. Semantic review: passed. Attempts: 1. 84 words.

Payment-provider integration, asynchronous settlement callbacks, idempotent capture and
reconciliation all appear — and none of it asserts that Tandem Pay's payments are broken. The
same project, same profile and same composer produced examples 6 and 7; only the evidenced need
differs.

### 8. Speculative enquiry triggered by an unrelated vacancy

Plan: `speculative_job_enquiry`, Larkspur Group. Provenance title: **Marketing Manager**.
No advertised title is pinned. Target families: Backend, Full-Stack, Software Engineering.

> **Subject:** Software Engineering Opportunities
>
> I am writing to see if you have any open backend or full-stack software engineering roles.
>
> My focus is on backend API development, building Go HTTP services and the PostgreSQL schemas
> behind them. In my project Synthetic Feeds, I built the ingestion API, its schema, and its
> scheduled workers to manage multi-source ingestion and deduplication.
>
> Are there opportunities for this kind of work on your team, or is there a technical lead I
> could speak with? I am happy to share my CV if you would like to review it.

Validation: clean. Semantic review: passed. Attempts: 1. 99 words.
"Marketing Manager" appears nowhere, no engineering vacancy is invented, and the question asked
is an honest one.

### 9. Speculative enquiry with real hiring evidence

Plan: `speculative_job_enquiry`, Fernbank Software, recipient `cto@fernbank.example`
(engineering). Sources: a careers page stating *"We are growing the platform team and hiring
backend engineers to work on our scheduling service"*, plus a product description.

> **Subject:** Backend & Full-Stack Engineering - Fernbank Software
>
> Hello,
>
> I am writing to ask about potential backend or full-stack engineering opportunities on your
> platform team, particularly as you develop your scheduling services.
>
> My work focuses on backend engineering and building Go HTTP services with explicit contracts.
> For example, in my Synthetic Feeds project, I built the ingestion API and the scheduled worker
> pipeline behind it, using background workers and queue consumers to manage the asynchronous
> processing.
>
> Are you open to adding engineers with this background to your team? I would be glad to share
> my CV if you have time for a brief conversation.

Validation: clean. Semantic review: passed. Attempts: 1. 105 words.
The personalization ("your platform team", "scheduling services") tracks the stored evidence and
stops there — it does not become a diagnosis of anything.

### 10. Speculative enquiry with weak evidence

Plan: `speculative_job_enquiry`, Ashgrove Ltd. Only source: *"Ashgrove Ltd provides business
software services."*

> **Subject:** Software Engineering opportunities at Ashgrove
>
> I am writing to inquire about potential software engineering opportunities within your backend
> or full-stack development teams.
>
> My focus is on building Go HTTP services and designing PostgreSQL schemas. Recently, on a
> project called Synthetic Feeds, I built the ingestion API and its database schema to support a
> multi-source ingestion service. This involved handling scheduled workers and ensuring clean
> deduplication.
>
> If you have any relevant openings or expect to expand your engineering team, I would be glad
> to share my CV. Who would be the best technical contact to discuss potential opportunities?

Validation: clean. Semantic review: passed. Attempts: 1. 101 words.
With thin evidence the message stays general. Nothing about Ashgrove is invented.

### 11. Service outreach with company evidence, no payment need

Plan: `service_outreach`, Quarry Health, recipient `founder@quarryhealth.example` (leadership).
Sources: *"We are looking for engineering help connecting our booking system to the lab systems
our partners use"*, plus a product description.
Selected capabilities: Third-party integrations; Backend API development. Proof: Synthetic Feeds.
Forbidden: `recipient_payment_problem`, `unselected_profile_item`.

> **Subject:** Integrating partner lab systems with Quarry Health
>
> Hello,
>
> I am reaching out because I saw Quarry Health is looking for engineering help to connect your
> booking platform with partner lab systems.
>
> I specialize in backend API development and third-party integrations. I build the Go services
> and contracts those integrations run through, including integrating external HTTP APIs and
> verifying inbound webhooks.
>
> For my Synthetic Feeds project, I built a multi-source ingestion service that connected several
> external sources. The main challenges were handling the different data formats and managing
> the varied failure behaviors of each external provider.
>
> If you are open to working with an external developer on these partner lab integrations, let
> me know if I can share my CV.

Validation: clean. Semantic review: passed. Attempts: 1. 121 words.
The reason for contact is the company's own stated request. No payment vocabulary appears, and
no delivery gap or outsourcing appetite is assumed.

### 12. Service outreach with an evidenced payment need

Plan: `service_outreach`, Rill Commerce, recipient `cto@rill.example`. Source:
*"We are looking for a contractor to help us add a second payment provider and rebuild our
settlement reconciliation, which we currently do by hand."* Payment need is evidenced, so the
`recipient_payment_problem` forbidden claim is absent.

> **Subject:** Payment integration and reconciliation for Rill Commerce
>
> I saw that Rill Commerce is looking to add a second payment provider and automate your manual
> settlement reconciliation. I help companies build reliable payment integrations and robust
> reconciliation workflows.
>
> In my Synthetic Ledger project, I built Go and PostgreSQL services to handle payment provider
> integration and the underlying reconciliation job. When integrating payment APIs, a core focus
> is ensuring transactional correctness. I make callback handling idempotent so that repeated
> notifications cannot double-apply.
>
> If you are still looking for help with this integration, I would be happy to share my CV or
> jump on a quick call to discuss how I can assist with your settlement workflows.

Validation: clean. Semantic review: passed. Attempts: 1. 116 words.

Examples 11 and 12 together answer the question the review posed: service outreach is not
payments outreach. The payment offer in 12 exists only because the company asked for it in its
own words, and it repeats their stated situation rather than diagnosing a new one.

## Part 2: adversarial semantic review

Eleven deliberately defective complete emails, each sent to the **real reviewer**. Deterministic
validation was computed separately for the same emails so the two layers can be compared.

| # | Defect | Deterministic validator | Real reviewer verdict |
| --- | --- | --- | --- |
| 1 | Claims extensive Kubernetes experience | `block: unapproved_technology` | fail — `block: unselected_profile_item` |
| 2 | Applies for the DevOps Engineer role when Backend Engineer is pinned | `block: application_title_mismatch` | fail — `block: wrong_role` ×2 |
| 3 | Speculative enquiry says it is interested in the Marketing Manager vacancy | `block: unrelated_title_leakage` | fail — `block: wrong_role` |
| 4 | "your payment reconciliation may need improvement" | `block: recipient_payment_problem` | fail — `block: recipient_payment_problem` |
| 5 | Backend role; email is dominated by payments-domain detail | *(none — factually true)* | fail — `block: unselected_profile_item` |
| 6 | "your impressive engineering culture and rapid growth" | *(none)* | fail — `review: generic_praise` |
| 7 | "keen interest… unique blend… dynamic team" | `review: formulaic_language` | fail — `review: generic_phrasing`, `review: weak_proof` |
| 8 | Same backend experience restated three times | *(none)* | fail — `review: repetition` |
| 9 | Ends "Anyway, that is all from me." | *(none)* | fail — `review: poor_flow`, `review: weak_cta` |
| 10 | Service-outreach plan; email says "I'd like to apply for a position" | `block: intent_contradiction` | **fail, but only `review: template_tone`, `review: weak_proof`** |
| 11 | "I've attached my CV" | `block: fabricated_attachment` | fail — `block: fabricated_attachment` |

**All eleven were rejected — `passed=false` in every case.** No defective email would have
progressed.

Two results deserve attention.

**Case 10 is a real reviewer miss.** A service-outreach plan whose email applies for a job is a
hard intent contradiction, and the reviewer returned only quality findings. The email is still
blocked, because deterministic validation independently raises
`block: intent_contradiction` and the engine unions both layers. This is precisely why the
deterministic floor exists, and it is direct evidence that semantic review alone would not be
sufficient.

**Case 5 is the reviewer being stricter than expected**, in the safe direction. A message
factually true but hijacked by an irrelevant domain was expected to draw a quality finding; the
reviewer treated it as a hard block. Over-blocking sends the draft to human review, which is the
correct failure direction.

Cases 5, 6, 8 and 9 have **no deterministic coverage at all** — they are the four defects that
only semantic review can see, and the reviewer caught all four.

## Provider quota constraint

`gemini-3.5-flash` on the free tier enforces
`GenerateRequestsPerDayPerProjectPerModel-FreeTier` at **20 requests per day per key**. The
configured pool has two keys, so the ceiling is 40 calls per day. The evaluation above consumed
35 (12 compositions with their reviews, plus 11 adversarial reviews) and exhausted the remainder
on two follow-up runs, both of which returned HTTP 429 for every call.

Two consequences, recorded rather than hidden:

1. Per-attempt detail for the one repair (example 5) could not be recaptured. The harness now
   records every attempt; the re-run is quota-blocked.
2. A proposed composition-prompt change to remove the stock opening is **not applied**, because
   it could not be validated. See the completion report.
