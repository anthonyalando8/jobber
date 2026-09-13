# Email generation investigation

Date: 2026-09-12

## Main finding

The generation pipeline still represents Anthony as a narrowly positioned Go/payments contractor. The intended identity is a **full-stack developer whose main strength is backend engineering**. This mismatch exists in the fixed templates, capability selection, AI instructions, job classification, and follow-ups.

The AI does not write or review the complete email. It supplies an observation of at most 25 words and a hypothesis of at most 20 words. Go inserts those fragments into database templates that determine the subject, introduction, experience, offer, and closing. Improving the model alone cannot correct the current positioning or structure.

The application problem has a direct code explanation: the advertised role becomes the role Anthony says he is applying for. There is no separate representation of his desired backend/full-stack opportunity and no suitability decision before that substitution.

## Scope and evidence limits

This investigation follows the repository's seeders, enrichment, scoring, draft orchestration, provider prompts, template migrations, validation, review/send handlers, follow-ups, and existing tests. Source links below are relative to this document.

Findings about code behavior are confirmed by source inspection. Template descriptions reflect the SQL migrations in this checkout, assuming they have been applied in order. Runtime templates are editable database rows: their current contents, migration state, and actual sent emails were not queried. Therefore this report explains mechanisms capable of producing the reported symptoms; it does not claim that every stored or sent email mentions payments, or attribute a particular sent email to an exact model response.

No `.env` contents were read, credentials exposed, AI requests made, service started, emails sent, or application behavior changed during this investigation.

## 1. How an email is assembled

| Stage | Actual behavior | Relevant source |
| --- | --- | --- |
| Discovery | Creates companies and short `hiring_backend` signals from listings. | [Kenyan seeder](../internal/seed/adapters.go), [remote helpers](../internal/seed/usjobs.go) |
| Research | Crawls seven fixed website paths and extracts contacts and keyword-derived signals. | [Enricher](../internal/enrich/enricher.go) |
| Classification | Uses source, hiring signals, company-name heuristics, and manual overrides to assign a segment and score. | [Scorer](../internal/score/scorer.go) |
| Draft preparation | Loads signals/contacts, extracts a role, chooses a capability sentence, and filters AI evidence by segment. | [GenerateAndSaveDraft](../internal/draft/generate.go) |
| Template selection | Reads the lowest-ID sendable database template for segment and channel. | [GetTemplate](../internal/store/template.go) |
| AI call | Sends company name, domain, segment, and signal ID/kind/detail/URL. Requests only observation, hypothesis, and citation IDs. | [Provider contract and prompts](../internal/draft/provider.go) |
| Assembly | Normalizes the AI fragments, validates them, and substitutes variables into the fixed subject/body. | [TemplateService](../internal/draft/template.go) |
| Decision | Saves the draft and assigns auto-send, review-required, or blocked using policy rules. | [Send policy](../internal/sendpolicy/policy.go) |
| Delivery | Sends the stored body through SMTP; does not rewrite it or perform a final semantic review. | [Web handler](../internal/web/handler.go) |

Manual generation, batch drafting, and the background draft worker share `GenerateAndSaveDraft`. This is a central design issue affecting all three entry paths, not just one screen. See [company actions](../internal/web/company_actions.go), [batch handlers](../internal/web/handler.go), and [scheduler](../internal/worker/scheduler.go).

## 2. Why payments dominate outreach

### 2.1 Capability selection explicitly prefers payments

In `servicePitch`, any `paybill_listed`, `payment_complaint`, or `checkout_present` signal selects a payments pitch. Payment-related words anywhere in a hiring snippet also qualify, including broad words such as `finance`, `collections`, and `billing`.

Payments win even when backend/technical-service evidence is also present. When no strong signal exists, the general fallback still starts with payments. The alternative backend sentence describes a Go API with auth and deployment; there is no corresponding full-stack capability option.

This creates several concrete failures:

- Having a checkout or accepting M-Pesa is treated as a reason to offer payment repairs, although it does not establish a problem.
- A finance-sector or collections-related job can trigger a payments offer without being a software engineering role.
- A generic backend opportunity can still receive a payments-first fallback.
- The available options cannot describe the user's wider full-stack positioning.

Source: [capability constants and servicePitch](../internal/draft/generate.go).

### 2.2 AI evidence filtering does not control the capability pitch

`GenerateAndSaveDraft` passes filtered signals to the AI, but passes the original, unfiltered signals to `servicePitch`. For technical audiences, `relevantSignalsForSegment` removes checkout and marketing-site technology signals from the AI context. Those same removed signals can still determine the fixed offer.

Consequently, the opener might discuss a genuine engineering requirement while the next paragraph pivots to payment integration because of a checkout keyword elsewhere on the website. The AI never sees the selected capability sentence and cannot reconcile this mismatch.

Source: [GenerateAndSaveDraft and relevantSignalsForSegment](../internal/draft/generate.go).

### 2.3 Several templates force payments independently of AI

Repository template state after the migrations:

| Segment | Fixed positioning and resulting limitation |
| --- | --- |
| `agency` | Introduces Anthony as backend-only with Go, Postgres, and M-Pesa. The original subject remains “Backend overflow work” with Go/M-Pesa branding; later body rewrites do not update that subject. |
| `hiring` / `hiring_us` | Offers contract cover while the company hires. Assumes the hire creates a delivery gap and that short-term fixed-price work is appropriate. These are service pitches, not applications. |
| `enterprise` | Explicitly says the offer is one narrow problem: M-Pesa reconciliation. Does not use `service_pitch`, so improving that function alone will not broaden these emails. |
| `sme` | Fixed offer about payment confirmations, matching payments to customers, receipts, and reporting. |
| `incubator` | Seeded variants concentrate on payments/backend support; the founder-oriented variant is explicitly a payments package. Variant selection is lowest ID, not recipient fit. |
| `application` | Fixed experience paragraph centers on distributed payments and the M-Pesa reconciliation project. Does not use `service_pitch`. |

Sources: [initial templates](../internal/store/migrations/0001_init.sql), [enterprise](../internal/store/migrations/0007_enterprise_segment.sql), [service-pitch migration](../internal/store/migrations/0011_service_pitch.sql), [latest outreach body rewrites](../internal/store/migrations/0014_dash_reduction.sql), [application introduction](../internal/store/migrations/0015_application_segment.sql), [application expansion](../internal/store/migrations/0016_application_template_depth.sql).

Payments should remain an available specialization and proof point. The failure is making it the default identity and offer regardless of relevance.

## 3. Why applications target unrelated advertised roles

### 3.1 Kenyan discovery does not validate the advertised title

The Kenyan seeder searches backend-related terms, but trusts whatever listing cards the results page returns. It does not call a role-relevance filter before storing each accepted card as `hiring_backend`. The job-title link is available, but the saved text comes from the entire card.

A search query is not evidence that every returned job is a backend role. This permits marketing, finance, procurement, and other unrelated listings to enter the engineering pipeline if returned by the source. This is a code-level explanation for the reported symptom, not confirmation of a specific current job-board response.

The four remote seeders do use `isRelevantJobTitle`. That is a useful partial safeguard, but it is not shared by the Kenyan path, and it does not fix existing records or decide application intent. Its phrase list is also imperfect: for example, it includes `full stack` and `fullstack`, but not `full-stack` as a standalone matching phrase.

Sources: [Kenyan discovery](../internal/seed/adapters.go), [remote title filter](../internal/seed/usjobs.go).

### 3.2 Role extraction preserves unrelated job titles

`ExtractRole` first searches for known engineering phrases anywhere in the snippet. It then accepts a capitalized title ending in words such as Manager, Officer, Analyst, Specialist, or Director. A simple title such as `Marketing Manager` fits this fallback. With no recognized title, it returns `Backend Developer` even though that vacancy may not exist.

The existing test explicitly expects `Checkoff Product Scheme Manager` to survive extraction. That fixes accurate transcription of an advertised role, but does not answer whether Anthony should apply for it.

Sources: [ExtractRole](../internal/draft/role.go), [role tests](../internal/draft/role_test.go).

### 3.3 Application mode changes the template, not the target role

For `application`, orchestration uses the same role extraction as the hiring segments. The template then inserts it into both:

> Application: {{role}} at {{company}}

> I'd like to apply for the {{role}} role at {{company}}

There is no `desired_role`, candidate profile, role-fit result, or speculative-enquiry mode. Switching to application mode only changes the company segment; it does not reinterpret the advertised job or rewrite an existing draft.

A synthetic trace, derived from the code rather than a captured email:

```text
Search result: Marketing Manager at Example Company
Stored kind: hiring_backend
Manual mode: application
ExtractRole: Marketing Manager
Subject: Application: Marketing Manager at Example Company
Body: I'd like to apply for the Marketing Manager role ...
Experience paragraph: Go, Postgres, distributed payment systems ...
```

This is the wrong intention expressed accurately by the template. The model cannot change the fixed subject or the application declaration.

Sources: [role preparation](../internal/draft/generate.go), [application template](../internal/store/migrations/0016_application_template_depth.sql), [CompanyApplyMode](../internal/web/company_actions.go).

### 3.4 Three different intentions need separate handling

| Situation | Appropriate intention |
| --- | --- |
| Relevant backend/full-stack vacancy | Apply to that specific role and explain verified fit. |
| Unrelated vacancy, but company is interesting | Ask about backend/full-stack opportunities or the appropriate engineering contact. Do not claim to apply for the unrelated role or imply an engineering vacancy exists. |
| Company is a potential client | Offer relevant development services with a clear scope and reason for contacting them. |

Company type and message intent are currently mixed into one segment field. An enterprise can be a prospective client, a relevant employer, or a speculative employer; its name alone cannot choose the right email purpose.

## 4. Why messages are thin and poorly connected

### 4.1 The AI has no candidate profile or complete-email context

`ObserveRequest` contains only company name, domain, segment, and signals. It contains no approved skills profile, project evidence, desired roles, CV, experience history, portfolio selection, or engagement preference. Company notes are not forwarded either.

The system prompt calls the sender a Nairobi backend engineer and frames the task as cold-outreach openers. There is an agency-specific instruction, but no application-specific instruction. Applications still request a hypothesis about the recipient's likely problem even though their template does not use it. An overlong hypothesis can therefore reject an application despite never appearing in its body.

The model also does not receive the chosen template, extracted role as a separate field, selected service pitch, or final email for review. It cannot reliably assess paragraph flow or match evidence to the fixed experience paragraph.

Sources: [provider contract/prompts](../internal/draft/provider.go), [GenerateDraft and rejectionReason](../internal/draft/template.go).

### 4.2 Evidence is too shallow to support detailed personalization

The Kenyan path saves the first 300 **bytes** of the listing-card text, not the full job description. Remote sources also truncate to 300 bytes, even where their feed supplied a longer description. Company names, titles, navigation, or introductory boilerplate can consume most of this space.

The enricher checks seven fixed paths and converts keyword matches into generic statements such as “E-commerce or payment functionality detected” and “Found react in site code.” These are detection summaries, not rich quotations describing the company's product, requirements, or actual engineering challenges. Its freshness detection also uses fixed year strings.

The AI is not given a browsing tool; a URL in a signal is supporting metadata, not a fetched full page. It can only work with the supplied snippets.

Sources: [Kenyan snippets](../internal/seed/adapters.go), [remote snippets](../internal/seed/usjobs.go), [RemoteOK](../internal/seed/remoteok.go), [website signals](../internal/enrich/enricher.go).

### 4.3 Company deduplication prevents richer job context

The seeders attach job signals only when a company is newly created. Later listings for an existing company are skipped rather than stored as additional opportunities. This can preserve the first, weak or unrelated listing while losing a later relevant engineering opening.

Role selection uses the first returned `hiring_backend` signal; the store orders by kind and newest creation time, not relevance to the candidate. The architecture has no selected job record with its own title, requirements, and application intent.

Sources: [CreateCompanyIfNew](../internal/seed/seeder.go), [seed adapters](../internal/seed/adapters.go), [signal ordering](../internal/store/contact.go), [draft role selection](../internal/draft/generate.go).

### 4.4 Fixed prose makes assumptions and provides narrow proof

The hiring templates assert that filling the seat usually takes months and offer contract cover without evidence that the recipient wants that arrangement. The enterprise template says “no pitch” inside a service pitch. The application expansion adds length chiefly by elaborating payment callbacks, rather than matching different job requirements to different projects.

Claims such as years of experience, production delivery, project scale, pricing, availability, and references are hard-coded. They may be true, but this pipeline does not validate them against an approved candidate profile. Their presence in a migration is not independent verification. Future copy should use only facts Anthony confirms.

The default templates contain paragraphs; the core structural problem is logical flow and information selection, not a missing HTML layout. SMTP sends the stored plain-text body without adding useful content.

### 4.5 Template choice is too coarse

`GetTemplate` selects by segment/channel and lowest ID. It does not use the stored `role_filter`, a chosen job, a capability match, or the recipient's function to select a variant. Templates are read from the database on each generation; editing SQL files without applying them does not change current generation.

Template edits and mode changes do not update already-saved drafts. The draft record also lacks a generation snapshot containing template version, prompt version, model, selected role, and selected capability. This makes reproducing a poor historical email harder.

Sources: [template store](../internal/store/template.go), [draft store](../internal/store/draft.go), [template editor](../internal/web/templates_admin.go), [company actions](../internal/web/company_actions.go).

## 5. Follow-ups repeat the same mismatch

Follow-up 1 asks whether backend/payments work is something the recipient would outsource. Follow-up 2 says Anthony wrote about making M-Pesa payments reliable. Both are fixed strings and are used without branching on original message intent or market.

This means an application can be followed by an outsourcing question, and a remote opportunity can be followed by an M-Pesa message. The second message refers to written material but supplies no actual resource link. Follow-ups do require a user-triggered send; the issue here is their generated content, not automatic follow-up dispatch.

Sources: [follow-up text](../internal/draft/followups.go), [follow-up handlers](../internal/web/followups.go).

## 6. Why validation does not catch these failures

Current validation checks citation IDs, length limits, certain unsupported numbers, repeated hiring/company phrases, sentence fragments, recruitment logistics, and unresolved placeholders. Send policy adds domain/contact/suppression checks, score thresholds, evidence age, and a tone word list.

These are useful controls, but none establishes that:

- The application role matches Anthony's skills and intent.
- The offer follows from the evidence rather than an incidental keyword.
- The email represents his full-stack/backend capabilities accurately.
- The experience paragraph supports the particular application.
- The opening, offer, and closing make sense together.
- Follow-up intent agrees with the original email.

Valid citation IDs only establish that referenced records exist. The number check searches the supplied signals, not exclusively the cited subset, and does not establish semantic support for all claims. A hypothesis is requested as a likely problem, but there is no semantic check against unsupported diagnoses.

Scoring also adds points per signal row rather than per unique fact. Enrichment inserts signals without deduplication, so repeated page detections or repeated research can increase scores without adding distinct evidence of fit. A high score is therefore not an email-quality assessment.

Sources: [validation](../internal/draft/template.go), [policy](../internal/sendpolicy/policy.go), [tone check](../internal/tonecheck/tonecheck.go), [scoring](../internal/score/scorer.go), [signal insertion](../internal/store/contact.go).

## 7. AI provider assessment

Claude and Gemini use the same prompt and two-field contract. The factory prefers Claude when its key is configured; Gemini is selected when Claude is not configured. This is configuration fallback, not automatic Claude-to-Gemini failover on a failed generation request.

Gemini uses temperature 0.2 and an 800-token output limit with a zero thinking budget request. Claude requests 300 output tokens. These settings support short extraction, but there is no evidence here that changing them would solve the fixed-template and intent problems. Actual provider/model configuration and sample responses were not inspected.

Sources: [factory](../internal/draft/factory.go), [Claude](../internal/draft/claude.go), [Gemini](../internal/draft/gemini.go).

## 8. Recommended implementation order

These are proposed changes for the next phase, not changes made by this investigation.

1. **Represent the sender correctly.** Add an approved profile identifying Anthony as a full-stack developer with backend as his main strength. Store verified skills, projects, responsibilities, and proof links. Treat payments as one capability. Do not invent frontend frameworks, experience duration, results, or availability.
2. **Separate intent from company segment.** Support service outreach, a specific relevant job application, and a speculative backend/full-stack opportunity enquiry. Keep advertised role separate from desired role. Never substitute a nontechnical advertised role into an application declaration.
3. **Improve opportunity data.** Apply title/fit checks consistently across sources, retain meaningful job descriptions and separate title fields, and preserve additional jobs for existing companies. Unrelated listings can identify interesting companies without becoming engineering vacancies.
4. **Replace payments-first selection.** Choose the offer from relevant evidence and the approved profile. Default to broader backend/full-stack positioning when a narrow need is unproven. Do not interpret a checkout/paybill as a defect.
5. **Give each intent an appropriate composition contract.** Supply the selected opportunity, sender profile, evidence, and approved proof. Generate or assemble a coherent reason for writing, relevant capabilities, concrete proof, and one clear next step. Applications need a fit explanation; speculative enquiries must honestly ask about opportunities that may not be advertised.
6. **Review the complete email.** Validate subject/body agreement, role fit, supported experience claims, evidence-to-offer connection, and intent consistency. Preserve existing citation and send controls. Keep ambiguous role/intent matches for human review.
7. **Make follow-ups intent-aware.** Continue the original application or service conversation. Mention payments or a resource only when relevant and when that resource actually exists.
8. **Handle persisted drafts and traceability.** Version templates/prompts/profile inputs and record generation choices. Explicitly identify stale drafts after profile/template/mode changes; preserve sent history. Ensure revised copy reaches database templates, not just migration files.

For content structure, use these distinct objectives:

| Email type | Information the reader should receive |
| --- | --- |
| Service outreach | Why this company; full-stack/backend positioning; relevant work Anthony can own; verified supporting project; one practical next step. |
| Relevant application | Exact role and intent; two or three supported areas of fit; relevant project evidence; portfolio/CV availability; request for consideration. |
| Speculative enquiry | Honest interest in backend/full-stack opportunities; brief capabilities and proof; request for the relevant team/contact, without pretending the unrelated vacancy is a fit. |

## 9. Validation status and proposed acceptance cases

Existing tests already encode payment priority and extraction of nontechnical titles. They protect the present behavior, so passing them would not demonstrate that the user-facing problem is solved.

Attempted:

```text
go test ./internal/draft ./internal/seed ./internal/score ./internal/tonecheck ./internal/sendpolicy
```

The test build failed with `testing/internal/testdeps: package testmain: cannot find package`. No tests completed successfully in this run. The cause of this build-environment failure was not investigated as part of the email-content review; conclusions above are source-based.

Acceptance cases for the eventual fix:

| Input | Expected behavior |
| --- | --- |
| Marketing Manager listing; user seeks engineering work | Speculative backend/full-stack enquiry or explicit review; never “I apply for Marketing Manager.” |
| Relevant Backend Engineer listing | Application explains verified backend fit; payment proof is optional and relevant. |
| Relevant Full-Stack Developer listing | Full-stack identity with backend strength; no invented frontend skills. |
| Generic company with a checkout | No claim that payments are broken and no automatic payment-repair pitch. |
| Agency selling web services | Relevant development collaboration with clear capabilities and proof. |
| Enterprise without payment evidence | No fixed reconciliation-only offer. |
| Application follow-up | Continues the application; does not ask about outsourcing payments. |
| Company gains a new relevant job after import | New opportunity is stored and can be selected. |
| Template/profile/intent changes after draft creation | Existing draft is visibly stale or deliberately regenerated; no silent assumption it changed. |
| Repeated copies of the same website fact | Do not increase confidence as though they were independent evidence. |

The essential correction is to make the system decide **what opportunity Anthony actually wants and how his verified abilities fit it** before composing the email. The current pipeline decides which fixed pitch to fill based largely on a company segment and a few keywords.
