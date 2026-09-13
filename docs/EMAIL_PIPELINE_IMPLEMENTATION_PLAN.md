# Email pipeline v2: implementation design

Date: 2026-09-12  
Status: approved with the implementation constraints below; Phases 1-7 implemented on tracked schema 25. Selective automation is implemented and switched off: the rollout mode is `v2_review` and `AUTO_SEND_INTENTS` is empty. Phase 7 awaits review; enabling any intent is a separate, explicit decision.  
Baseline: [Email generation investigation](EMAIL_GENERATION_FINDINGS.md).

### Approved implementation constraints

The user's implementation approval supersedes proposal details where they differ:

- Fit classification and explicit reasons are authoritative. Numerical scores are secondary ordering aids, not routing gates; the proposed score threshold must not independently decide classification.
- Composer and semantic reviewer have independently configurable providers/models and contracts. Reviewer assessment does not consume or trust composer reasoning.
- Intent and selected opportunity/title/target families/profile/recipient/proof are fixed before composition and remain isolated through edits, retries, regeneration, follow-ups, and company-state changes.
- Add an explicit fixture using an approved payments project's Go/PostgreSQL/API/distributed-work responsibilities as broad backend proof while rejecting an unsupported recipient payment-repair diagnosis.
- Add an explicit partial-fit fixture: backend requirements match but an unapproved frontend technology is a disclosed gap requiring review, not an unrelated classification or fabricated skill.
- Backend invariants and the profile/opportunity/plan/full-review UI take priority over dashboard polish. Retain only necessary history/versioning, not a generalized workflow framework.
- Development uses synthetic candidate fixtures; no real technologies, experience, outcomes, availability, or pricing are invented or pre-approved.
- Rollout stays `v2_review` with no auto-send intents until reviewed offline evaluation. All send paths enforce the same v2 checks.

Phase reports: [Phase 1 operations](PHASE1_MIGRATIONS.md), [Phase 1 completion](PHASE1_COMPLETION.md), [Phase 2 completion](PHASE2_COMPLETION.md), [Phase 3 completion](PHASE3_COMPLETION.md).

## 1. Outcome and design decisions

The next pipeline will represent Anthony as a **Full-Stack Software Developer whose primary strength is Backend Engineering**. Payments is an optional specialization and project area. It is never the fallback professional identity, and a company's checkout is never sufficient evidence that its payments need fixing.

The pipeline will follow this order:

```text
Discovery and research -> stored facts and opportunities
Approved candidate profile + opportunity -> fit assessment
Fit + company relevance + engagement preference -> contact intent
Intent + recipient + evidence -> selected capabilities and proof
Structured context -> complete email composition
Deterministic validation -> semantic review -> send policy
Reviewed, current draft -> existing guarded SMTP path
Sent message + original intent -> reviewed follow-up
```

Decisions fixed by this proposal:

1. Keep the existing Go binary, PostgreSQL, Redis, chi routes, HTML templates, and in-process scheduler. No separate AI service, vector database, distributed workflow engine, or frontend framework.
2. Introduce versioned candidate profiles, first-class opportunities, and explicit contact plans. A contact plan is the small persisted record of why this recipient is being contacted; it replaces misuse of company segment.
3. Use deterministic, explainable opportunity matching initially. AI may identify ambiguities during full-email review, but cannot promote an unrelated role to a suitable application or override matching rules.
4. Compose the complete subject and body in one AI call. Use a second call for independent, structured review. Neither model chooses the authoritative intent, opportunity, recipient, or approved profile.
5. Keep source facts, approved candidate facts, and model inferences distinct. Do not promote old template claims into approved profile facts.
6. Preserve existing records and sent history. Legacy unsent drafts become visibly stale and cannot bypass v2 review.
7. Initially require human approval for every v2 send. After acceptance, automatic sending can be enabled separately by intent. Ambiguous fits and follow-ups always remain human-reviewed.
8. Selecting multiple opportunities never authorizes multiple sends. Preserve the current 30-day same-domain contact restriction and daily/interval limits; multiple jobs remain usable for selection, comparison, and drafts.

Approval of this plan would authorize the implementation design, not certify unverified CV/project facts or enable live automatic sending.

## 2. Current architecture and inspected integration points

The investigation remains the baseline. Additional inspection for this design confirmed:

| Current path | Change-driving behavior |
| --- | --- |
| `internal/seed/{adapters,remoteok,weworkremotely,remotive,himalayas,seeder,usjobs}.go` | Job signals are attached only after creating a new company. Redis domain deduplication can prevent a new job from reaching storage. Kenyan listings lack the remote title filter. |
| `internal/store/company.go` | `ListCompaniesForDrafting` requires company status `enriched` and excludes any company with a non-discarded draft, including sent drafts. `source` also acts as a market proxy. |
| `internal/enrich/enricher.go`, `internal/store/contact.go` | Research creates generic signals repeatedly. In-process cache has neither synchronization nor expiry; persistent cache has an age check. |
| `internal/score/scorer.go` | Company source and hiring indicators decide hiring/application-style segments; repeated signals add points. |
| `internal/draft/{generate,template,provider,role,factory,claude,gemini}.go` | Shared generation produces tiny AI fragments and substitutes them into DB copy. `CreateDraft` does not populate `EvidenceUsed` on its returned struct even though the DB insert contains it; immediate policy evaluation therefore may see missing citations. |
| `internal/web/company_actions.go`, `internal/web/handler.go`, `internal/worker/scheduler.go` | Manual generation, batch generation, and worker generation can attempt automatic sending. All must move together. |
| `internal/web/review.go`, `internal/web/templates/review.html` | Save only persists body; subject is readonly. Edited content does not receive a full semantic review. |
| `internal/web/resend.go` | Bounce retry clones body/subject but drops evidence and other generation context. |
| `internal/draft/followups.go`, `internal/web/followups.go` | Fixed follow-ups ignore intent. Preview uses original recipient, but send can reselect the company's current best contact. Follow-up draft IDs are not linked from their root send log. |
| `internal/store/sendlog.go`, `internal/store/dashboard.go` | Follow-up tracking is based on root send logs. Company outcome actions mark the latest thread replied, which becomes ambiguous with multiple opportunities. |
| `cmd/prospector/main.go` | Starts four loops immediately. Discovery currently automates Kenyan jobs; remote seeding is manual. Several generation routes have a 30-second request timeout. |
| `internal/store/migrations/0001_init.sql` through `0016_application_template_depth.sql` | No profile, job entity, or generation-version model. Template rows can be edited independently of migrations. |
| `Makefile`, `setup.bat`, `internal/store/db.go` | No tracked migration runner. Make replays SQL files; Windows setup applies only `0001`. Startup checks connections but not schema version. |

Live DB schema/template contents and historical messages have not been inspected. The implementation starts with a read-only schema/data inventory, not an assumption that all migrations are applied. No `.env` inspection is needed to write this design.

## 3. Separate domain concepts

| Concept | Owner and meaning |
| --- | --- |
| Company segment | `company.business_segment`: agency, enterprise, incubator, SME, or unknown. Describes the business; does not express email intent. |
| Opportunity | A particular advertised vacancy, with source identity, title, description, and lifecycle. Many per company. |
| Advertised role | Original title from the selected opportunity revision. Never guessed from the candidate's desired role. |
| Desired role | Approved candidate job family: backend, full-stack, or software engineering, with approved aliases. |
| Candidate/opportunity fit | Versioned assessment of a specific opportunity against a specific profile. Separate from company score. |
| Company relevance | Explainable business/engineering relevance, used for prioritization and proposing enquiries/outreach. |
| Email intent | Exactly `SPECIFIC_JOB_APPLICATION`, `SPECULATIVE_JOB_ENQUIRY`, or `SERVICE_OUTREACH`. |
| Recipient | A selected active contact and frozen address/name; relevance to the intent is evaluated separately from email quality. |
| Selected proof/capabilities | Approved profile items chosen for this purpose and linked to selection reasons. |
| Contact plan | Explicit company/opportunity/intent/recipient/target-family selection, with a revision and origin. |
| Draft | A generated or edited message revision with complete context and review results. |

Internal enum values use lowercase snake case to match existing DB decision strings; UI labels may use the uppercase names above. Invalid enum values are rejected at both Go and SQL boundaries.

## 4. CandidateProfile: facts approved by Anthony

### Storage and approval

Use a singleton profile head plus immutable JSONB revisions. This is a personal tool, so separate relational tables for every skill/project are unnecessary. JSON documents use typed Go structs, stable IDs, and explicit validation; they are not arbitrary prompts.

`candidate_profile` has `id`, `active_revision_id`, `created_at`. `candidate_profile_revision` has `id`, `profile_id`, `revision`, `document jsonb`, `content_hash`, `approval_status`, `approved_at`, `approval_note`, `created_at`. Unique `(profile_id, revision)`. Approved document content is immutable. Editing creates a new draft revision; activating an approved revision atomically changes the head and invalidates unsent drafts.

Only the following initial facts can be prefilled from this conversation: professional identity, primary/secondary positioning, and the desired engineering job families. Existing technologies, payments project descriptions, and signatures can be shown as import suggestions, but remain unapproved until confirmed. Empty optional sections are valid; they reduce the strength of possible matching/composition rather than causing invented facts.

Proposed new `internal/profile/profile.go`:

```go
type CandidateProfile struct {
    ID                 int64
    Revision           int64
    Identity           string
    PrimaryStrength    string
    SecondaryPosition  string
    DesiredRoles       []JobFamily
    Technologies       []Technology
    Capabilities       []Capability
    Projects           []Project
    ExperienceDomains  []ApprovedFact
    Specializations    []ApprovedFact
    Engagement         *EngagementPreferences
    Resources          []Resource
    Signature          Signature
}

type ApprovedFact struct {
    ID   string
    Text string
}
type Capability struct {
    ID            string
    Name          string
    Description   string
    TechnologyIDs []string
    ProjectIDs    []string
    Tags          []string
}
type Project struct {
    ID               string
    Name             string
    Description      string
    Responsibilities []ApprovedFact
    Outcomes         []ApprovedFact
    TechnologyIDs    []string
    CapabilityIDs    []string
    ResourceIDs      []string
    Domains          []string
}
```

Supporting types have these concrete fields:

- `JobFamily`: stable ID, display name, normalized title aliases.
- `Technology`: stable ID, display name, aliases. No inferred proficiency level or years.
- `Resource`: stable ID, kind (`portfolio`, `project`, `cv`, `article`), approved label and URL; `available_on_request` is explicit for a CV without a public link.
- `EngagementPreferences`: nullable confirmed employment types, service-work permission, location/remote constraints, availability text, pricing text. Unknown values stay nil. Unknown preferences prevent unattended intent selection, not research.
- `Signature`: approved display name, professional descriptor, approved resource IDs, optional phone/location. SMTP sender configuration remains separate.

Profile approval confirms every included claim; item IDs allow exact claim references. Cross-references must resolve within that revision. URL syntax is validated, but a valid URL is not proof of achievements. No scraping of GitHub or importing template prose silently approves experience.

Affected existing files: `internal/draft/generate.go`, `provider.go`, `template.go`, `internal/web/handler.go`, `.env.example` (configuration documentation only). New storage/UI: `internal/store/profile.go`, `internal/web/profile.go`, `internal/web/templates/profile.html`.

## 5. Opportunities and ingestion

### Schema

`opportunity` stores current searchable fields:

| Column | Definition |
| --- | --- |
| `id`, `company_id` | bigint PK and FK; no uniqueness constraint on company ID. |
| `source`, `source_key` | Required source and stable deduplication key; unique together. |
| `external_id`, `url`, `canonical_url` | External ID nullable; original URL retained separately from canonical identity. |
| `title`, `description` | Separate text fields. Missing title is unknown, never “Backend Developer.” |
| `location`, `employment_type`, `remote_status` | Nullable normalized fields with original values retained in metadata. Remote is `remote`, `hybrid`, `onsite`, or `unknown`. |
| `published_at`, `expires_at` | Nullable source facts, not inferred from discovery time. |
| `discovered_at`, `last_seen_at`, `updated_at` | First capture, most recent successful observation, and meaningful update time. |
| `availability_status` | `open`, `closed`, `unknown`; absence from one scrape does not mean closed. |
| `selection_status` | `new`, `selected`, `review_required`, `ignored`. Selection is not permission to send. |
| `current_revision`, `content_hash` | Changes only for meaningful content/availability changes. |
| `description_status`, `raw_metadata` | `complete`, `partial`, `unavailable`; JSONB source fields, extraction warnings and bounded original payload. |

`opportunity_revision`: `(opportunity_id, revision)` PK, immutable normalized document, bounded raw metadata, content hash, captured timestamp. Drafts pin exact revisions. `opportunity_match`: `(opportunity_id, opportunity_revision, profile_revision_id, matcher_version)` unique, score, classification, reasons JSONB, requirement-to-profile mappings, missing requirements, assessed timestamp. UI reads the assessment matching the current revisions; stale assessments are never treated as current.

Use `ON DELETE RESTRICT` for new references to historical records. Archive opportunities; do not delete them through UI. Index `(company_id, updated_at DESC)`, `(selection_status, updated_at)`, and current-match lookup columns.

### Identity and update rules

1. Prefer stable source external ID: `source_key = "id:" + external_id`.
2. Otherwise use a normalized canonical job URL: strip fragments and a known tracking-parameter allowlist; retain meaningful job query parameters. Persist the key so a later external ID can be attached without creating a second row.
3. If neither usable ID nor URL exists, retain the listing in a bounded source-ingestion report for review. Do not merge different vacancies using company name or a guessed title hash.
4. Source-specific identity rules are fixture-tested. Same key updates content and creates a revision only when its content hash changes. Redis is only a fetch throttle/cache; PostgreSQL uniqueness owns deduplication.
5. Different sources remain separate opportunities unless a human links them. Show likely same-vacancy candidates by company/title/canonical employer URL; do not auto-delete or merge them. Company-level send limits still apply.
6. Company upsert returns both existing and newly created rows. Always upsert the opportunity afterward. Guessed-domain/name collisions are marked for identity review rather than silently reassigning an existing company. Keep the existing company IDs and domain-verification path.

### Shared ingestion shape

All five job adapters produce `DiscoveredOpportunity` with source identity, employer name/domain candidates, title, description, URL, location/employment/remote data, timestamps, and metadata. A shared `seed.IngestOpportunity` transaction resolves the company, upserts the opportunity and revision, and schedules matching. Source adapters no longer generate `hiring_backend` as the canonical job representation.

Preserve meaningful descriptions up to 64 KiB UTF-8 plain text, with a 2 MiB HTTP response limit and explicit `partial` marker on truncation. Keep the cleaned full description; composition later selects relevant excerpts. Kenyan adapters fetch the linked job page when listing-card content is insufficient. Feed sources use their complete provided description before requesting a page. Strip navigation/scripts, preserve paragraph boundaries, store extraction source URL and quality. Fetch failures leave a partial opportunity available for review.

Every source runs the same matcher after storage. Unrelated roles encountered within configured searches remain as unrelated opportunities; they are excluded from the specific-application queue. Do not broaden remote discovery into an unbounded all-jobs crawl merely to retain unrelated roles. Keep current source scope and manual remote-seed behavior initially.

Affected: all `internal/seed/*.go` adapters, `internal/store/company.go`, new `internal/store/opportunity.go`, `internal/seed/ingest.go`, `internal/opportunity/{types,match}.go`. Remove `usJobSignalText` as the canonical job payload; retain a compact UI summary function if useful.

## 6. Matching and intent selection

### Matching is evidence-based and separate from ranking

Normalize case, whitespace, Unicode/hyphens, and approved title aliases. Preserve the original title for the email. Determine job family from the title first; incidental description words such as “marketing platform” cannot turn Marketing Manager into engineering. Backend/full-stack/software-engineer aliases identify a relevant family, not proven qualifications. Mixed titles, “Engineer” without context, and conflicting title/description are possible matches.

Extract recognized requirement phrases conservatively from the available description, preserving exact excerpts. Do not treat absence of a recognized requirement as proof that none exists. A bounded dictionary covers capabilities and technology aliases; unparsed requirements stay visible as unknowns.

Initial explainable scoring, version `fit-v1`:

- 40 points: approved desired engineering family established by title and non-conflicting description; 20 for an ambiguous engineering title.
- Up to 40 points: 20 per distinct approved capability supported by an actual requirement excerpt, capped at two. Technology keyword repetition does not add points.
- 10 points: an approved project supports at least one matched capability.
- 10 points: known employment/location conditions agree with confirmed preferences. Unknown earns zero and is disclosed.

Classification gates take precedence over score:

| Classification | Conditions and allowed use |
| --- | --- |
| `strong_match` | Score at least 80; clear relevant family; meaningful requirements; no unresolved mandatory requirement, source conflict, or known preference conflict. Can propose a specific application. |
| `possible_match` | Relevant/ambiguous engineering family but insufficient description, missing approved proof, unknown mandatory requirements, or unresolved eligibility. Human review required; no invented fit. |
| `unrelated` | Clearly outside desired engineering families, such as Marketing Manager or Finance Officer. Never a specific application. |

Known employment/eligibility conflicts are separate reasons, not evidence that a title is nontechnical. For example, an engineering role with an unmet stated location requirement is possible/unsuitable for selection and requires review. “Strong” is a rule-based relevance assessment, not a guarantee of hiring eligibility. A title alone cannot yield strong match. Default profile with only identity facts will produce possible matches until skills/proof are approved.

### Intent rules

`ResolveIntent` consumes an assessment, company evidence, explicit user intent where supplied, and approved engagement preferences. It returns a suggested contact plan or `review_required`/`ignore`; it never asks the composer to choose intent.

| Situation | Default |
| --- | --- |
| Strong relevant job; employment interest confirmed | Propose `SPECIFIC_JOB_APPLICATION`, pinning the exact opportunity revision. |
| Possible fit | Show requirement gaps. User may select a truthful specific application after acknowledging soft gaps, or a speculative enquiry. Always review; never auto-send. |
| Unrelated/no job; relevant company; employment interest confirmed | Propose `SPECULATIVE_JOB_ENQUIRY` with approved target families. Discovered unrelated job may be linked as provenance, but is not the applied-for role. |
| Relevant potential client; service engagement confirmed | Propose `SERVICE_OUTREACH` with evidenced capabilities or broad backend/full-stack introduction. |
| Both employment and service purposes plausible, or preferences unknown | Ask for intent on the company page. Do not silently choose based on segment. |
| No company relevance established | Keep opportunity unrelated/ignored or in research review. Do not generate merely because an address exists. |

An explicit specific-application request for an unrelated role is blocked with `unrelated_application_role`; user must change intent, select another opportunity, or correct source/profile facts and rerun matching. Manual low-score override cannot bypass this invariant. No global fallback advertised title exists.

Affected: replace routing logic in `internal/score/scorer.go` and `internal/draft/role.go`; add `internal/opportunity/match.go` and `internal/draft/intent.go`; update company/opportunity actions and scheduler.

## 7. Research, company relevance, capability and recipient selection

Keep `signal` IDs so historical citations survive. Add `fact_key`, `fact_group`, `source_url`, `excerpt`, `extraction_method`, `confidence`, `first_seen_at`, `last_seen_at`, and `revision` where absent. Existing `detail`, `kind`, and `url` remain readable. Old generic signals are labeled `legacy_detection`, not retroactively converted to verbatim quotations.

For new evidence, deduplicate on company + normalized fact + source identity. Repeated observations update `last_seen_at`; they do not rewrite `first_seen_at`, add points, or prove the source publication date is recent. Same fact across pages shares a `fact_group`; the scoring/selection stage counts that group once. Existing duplicate rows remain for historical references and are grouped during reads/backfill. A partial unique index applies to new canonical rows; legacy duplicates do not prevent migration.

Extract real descriptions of products/services, explicit engineering requirements, and explicit requests/problems where available. Keep `checkout_present` typed as presence only. `paybill_listed` means payment acceptance only. Keyword detections cannot support a problem diagnosis. Repair the shared map cache with synchronized entries and expiry or remove it in favor of the persistent cache; do not feed indefinitely cached content into v2 research. Implement bounded fetches and treat retrieved content as untrusted data, never instructions to the model.

Company score remains a priority aid with distinct, capped categories, not a send-quality gate: business relevance to approved capabilities (0–40), direct engineering need/request (0–30), usable current source evidence (0–15), relevant contact (0–15). Each category exposes its evidence groups and maximum. Checkout, payment acceptance, generic technology detection, and copied year strings alone contribute no need points. Unknown company type stays unknown instead of defaulting to agency. Manual score overrides remain labeled prioritization overrides and cannot manufacture evidence or fit.

Selection returns at most three capability IDs and two project IDs, each with `reason`, supporting source refs, and `use_as` (`capability`, `proof`, or `optional_specialization`). Prefer requirement match, then actual company service/product relevance, then approved broad backend/full-stack abilities. Select one project for ordinary service emails. A payments project may demonstrate general backend design when its approved responsibilities support that claim; it must not imply the recipient has payment trouble. Broad/weak evidence yields general positioning or review, never an unsupported narrow offer.

Select the recipient before composition. Use explicit source application contact or confirmed recruiter/engineering contact for jobs; relevant owner/technical/project contact for service outreach. Unknown function or generic mailbox requires review; email quality 3 is not proof of job relevance. Do not invent a person's name from the email local part. Freeze the selected active contact ID, address, display name, role evidence, and selection reason in the plan/draft snapshot. An address change invalidates review rather than silently choosing a different person at SMTP time.

Affected: `internal/enrich/enricher.go`, `internal/store/contact.go`, `internal/store/pagecache.go`, `internal/score/scorer.go`, new `internal/draft/selection.go`, contact actions in `internal/web/company_actions.go`.

## 8. Contact plans, draft revisions, and Go interfaces

### Persisted contact plan

`contact_plan` stores `id`, `company_id`, nullable `opportunity_id`, `intent`, `target_families jsonb`, `recipient_contact_id`, `revision`, `selection_origin` (`manual`/`suggested`), `status` (`suggested`/`selected`/`paused`/`closed`), `reasons jsonb`, `created_at`, `updated_at`. A speculative plan may link the unrelated discovery opportunity for provenance; only specific applications require a relevant opportunity.

A stable `plan_key` covers company + opportunity-or-none + intent. One active plan per key; recipient changes increment its revision instead of making an extra campaign. Explicitly selecting a plan authorizes generation, not sending. Alternative plans at one company remain visible and are subject to domain-wide send restrictions.

### Draft/schema additions

Add nullable v2 fields to `draft`: `pipeline_version`, `plan_id`, `plan_revision`, `opportunity_id`, `opportunity_revision`, `profile_revision_id`, `intent`, `generation_snapshot jsonb`, `input_hash`, `content_revision`, `content_hash`, `stale_reasons jsonb`, `supersedes_draft_id`, `root_send_log_id`, `followup_step`, `generation_status`, `lease_token`, `lease_until`, `next_attempt_at`, `attempt_count`.

`generation_status`: `queued`, `composing`, `validating`, `review_pending`, `ready`, `failed`. Retain current draft `state` for lifecycle, adding `superseded`; send decision stays independent. Empty queued drafts are not shown as ready to send. One live generation/unsent draft per plan via a partial unique index excluding sent/discarded/superseded. Claim work with `FOR UPDATE SKIP LOCKED` and a lease; persist an attempt before a network request. Failed drafts require explicit retry after bounded transient retries.

`draft_revision`: immutable subject/body/recipient snapshot keyed by draft ID and content revision. `draft_review`: draft ID, content revision/hash, reviewer kind (`deterministic`, `semantic`, `human`), schema/prompt/model version, structured findings, result, reviewed timestamp. Approvals bind to an exact content hash and recipient. Draft input snapshot holds copied source excerpts and profile/scaffold revisions, not only mutable foreign keys.

Illustrative Go contracts (supporting types live in the packages indicated; no interfaces for unrelated CRUD):

```go
// internal/draft/types.go
type Intent string
const (
    SpecificJobApplication Intent = "specific_job_application"
    SpeculativeJobEnquiry   Intent = "speculative_job_enquiry"
    ServiceOutreach        Intent = "service_outreach"
)

type CompositionRequest struct {
    SchemaVersion string
    Plan          PlanSnapshot
    Profile       profile.CandidateProfile
    Opportunity   *opportunity.Snapshot
    Match         *opportunity.MatchResult
    Company       CompanyContext
    Recipient     RecipientSnapshot
    Selection     ContentSelection
    Evidence      []EvidenceRef
    Conversation  []MessageSnapshot
    Scaffold      ScaffoldSnapshot
}
type GeneratedEmail struct {
    Subject          string
    Body             string
    Intent           Intent
    OpportunityID    *int64
    EvidenceUsed     []string
    ProjectsUsed     []string
    CapabilitiesUsed []string
    Claims           []ClaimReference
}
type ClaimReference struct {
    Text       string
    Location   string // subject or body
    SourceRefs []string // e.g. profile:cap:api, opportunity:42:requirement:3
}
type Finding struct {
    Code       string
    Severity   string // block, review, info
    Location   string
    Text       string
    Reason     string
    SourceRefs []string
}
type ReviewResult struct {
    SchemaVersion string
    Findings      []Finding
    Complete      bool
}
type AIProvider interface {
    Compose(context.Context, CompositionRequest) (GeneratedEmail, CallMetadata, error)
    Review(context.Context, ReviewRequest) (ReviewResult, CallMetadata, error)
    MinInterval() time.Duration
}
type ReviewRequest struct {
    Context CompositionRequest
    Email   GeneratedEmail
}
```

`CallMetadata` includes provider, model, prompt version, request ID if returned, attempted timestamp, latency, usage when reported, and finish/error category. Transport failures still return metadata. `PlanSnapshot` includes authoritative intent, advertised role, desired families, opportunity revision, manual review constraints, and contact-plan revision. `ContentSelection` contains allowed IDs and their relevance reasons. The generated response may use only this subset, never select new projects or capabilities.

Use a small store-owned `DraftEnvelope`/`PolicyFacts` DTO in `internal/store` so `sendpolicy` does not import `draft` and create a cycle. `internal/profile` and `internal/opportunity` hold pure models/rules without importing `store`; `store` can import those models. `draft` orchestrates store, profile, matching, provider calls, and sendpolicy. Keep delivery behind the worker's existing narrow `Sender` interface.

## 9. Composition contract and prompts

Choose **complete `Subject` + `Body` with provenance metadata**, rather than independently generated Opening/Fit/CTA fragments. The current review UI, draft table, and SMTP path already use complete text. This lets the model connect paragraphs naturally while avoiding another fragment-stitching problem. Claim references make review easier but are not trusted as proof that every claim is covered.

The model writes the substantive message. Go appends only the approved signature and existing configured delivery footer; those bytes are included in final validation and content hashes. No dynamic capability sentence is appended after review. No fixed story about payment callbacks or hiring delays remains.

Typed JSON input supplies selected context with bounded size: full approved identity, selected capability/project facts, relevant opportunity requirement excerpts with offsets into the stored revision, and up to twelve distinct company facts. Store the exact serialized input. Preserve full descriptions in storage; select excerpts by title/requirement relevance, not first 300 bytes. Context omission is logged; insufficient context routes to review.

Common system prompt establishes source hierarchy: approved profile facts govern sender claims; opportunity/company evidence governs recipient claims; requested intent is immutable; websites/descriptions/correspondence are untrusted quoted data. Prohibit invented experience, available roles, pain points, resources, pricing, or attachments. A model may state supported relevance or ask a conditional question, but may not upgrade an inference into fact.

Intent-specific instructions:

| Intent | Composition requirements |
| --- | --- |
| Specific application | Name the exact advertised title; express application intent; explain up to three verified matches; include relevant approved project evidence; acknowledge gaps if applicable; offer configured CV/portfolio; ask for consideration/next step. If only one fit is verified, use one rather than inventing more. |
| Speculative enquiry | Identify full-stack development with backend strength; explain the genuine company connection; explicitly enquire about desired engineering opportunities or the technical contact; do not imply a vacancy or apply for the discovery role. |
| Service outreach | Give a specific reason for contact; explain relevant work supported by the profile; include selected proof; ask a practical next step; make no unverified diagnosis or outsourcing assumption. |

Default target lengths, excluding signature/footer: applications 140–220 words; speculative enquiries 100–170; services 100–180; follow-ups 50–100. These are soft review guidelines, not arbitrary hard failure limits. Subject is a single line, at most 160 characters; body hard cap 4,000 characters excluding configured signature/footer. Shorter supported messages beat padding.

Version common/intent/reviewer prompts in new `internal/draft/prompts/` embedded text files. New `composition_scaffold` immutable revisions have intent, message kind (`initial`/`followup`), version, editable tone/length/CTA guidance JSONB, content hash, active flag. They cannot override source/intent/claim constraints. Exactly one active revision per intent/message kind. Keep old `template` rows intact as legacy reference; `/templates` becomes the scaffold editor plus a read-only legacy tab. No lowest-ID selection in v2.

Provider adapters retain existing provider-selection priority. Add separate bounded context-aware timeouts (60 seconds compose, 45 seconds review), strict response decoding, bounded output bodies, and explicit incomplete/truncated-response handling. Use provider-supported structured JSON where available, still validating locally. Verify provider-specific capabilities and model parameters against official documentation during implementation; this design does not assume the defaults named in old comments are current or supported. No silent cross-provider failover.

Affected: `internal/draft/{provider,claude,gemini,factory,template,generate}.go`, new `compose.go`, `prompts/`, `internal/store/scaffold.go`, `internal/web/templates_admin.go`.

## 10. Layered validation and review

### Layer 1: deterministic checks

Run before semantic review and again on the exact final content at send time:

- Strict response schema, nonempty subject/body, length bounds, no header CR/LF injection, valid intent and IDs matching the request. Model-returned intent/opportunity is an assertion to check, not routing authority.
- Selected opportunity belongs to company; contact belongs to plan/company; approved profile and current revisions exist. Specific application requires a selected non-unrelated opportunity and a real source title.
- Every referenced source/project/capability exists in the selected revision and allowed subset. Resource URLs must match approved resources or selected source URLs. No unconfigured attachment/CV/resource claims.
- Application title in subject/declaration must match the selected title after punctuation/whitespace normalization. Do not collapse different seniority or job families. If the title cannot be reliably extracted from prose, require semantic/human review; do not infer a successful check.
- Known technology claims outside the approved profile block. Approved technology names appearing only in the employer's requirements must not be mistaken for candidate experience; check attribution through claims and review.
- Candidate numeric claims must map to the cited candidate fact, company numeric claims to the cited company/opportunity excerpt. Normalize numeric tokens with boundaries; `5` inside `500` does not substantiate five years. Check written quantities semantically as well. Approved phone numbers, URLs, role titles, and footer addresses are separately typed, not an unrestricted number allowlist.
- Detect unresolved `{{...}}` and placeholder patterns in both subject/body; allow ordinary brackets when they are not placeholders. Retain useful tone warnings without applying old fragment-specific wording rules to complete prose.
- Any content or recipient edit invalidates previous reviews and approvals. Unknown parser coverage becomes a review reason, not an automatic pass.

### Layer 2: semantic full-email review

Second provider call receives the complete final subject/body, the same source context, immutable intent, matching reasons, selected capability/project facts, and deterministic findings. It returns `ReviewResult`, never replacement email text. Persist all reasons and severity. Review must inspect the entire prose for unreferenced claims, not merely verify model-supplied claim annotations.

Required finding codes include:

| Severity | Codes/examples |
| --- | --- |
| Block | `unrelated_application_role`, `application_title_mismatch`, `invented_candidate_fact`, `unsupported_numeric_claim`, `invented_vacancy`, `hallucinated_company_problem`, `invalid_source_reference`, `resource_not_configured`, `intent_contradiction`. |
| Review | `fit_uncertain`, `requirement_not_verified`, `recipient_function_unknown`, `weak_offer_evidence`, `irrelevant_payment_focus`, `identity_too_narrow`, `poor_flow`, `repetition`, `generic_cta`, `insufficient_context`, `review_unavailable`. |
| Info | Optional improvement that does not affect truth/intent, such as a minor length preference. |

An explicitly false or materially unsupported payment diagnosis is a block; a truthful but poorly chosen payments proof point is review. Applications sounding like outsourcing sales, speculative enquiries claiming an opening, and services claiming application intent are contradictions. The reviewer cannot weaken deterministic blocks or change authoritative fit/intent.

Provider failure, missing review fields, invalid JSON, or incomplete coverage results in `review_pending`/`REVIEW_REQUIRED`, never `AUTO_SEND`. One bounded transport retry is allowed for transient failure; no unbounded automatic rewrite/review loop. User-triggered regeneration creates a new revision and shows prior reasons. A semantic model is not a guarantee of factual correctness; ambiguous claims require human review and calibrated fixtures before automatic sending.

### Layer 3: policy and human resolution

| Outcome | Rule |
| --- | --- |
| `BLOCKED` | A deterministic or semantic block, unapproved profile, unrelated specific application, or a hard send restriction. Correct inputs/content and rerun checks; score override is not a bypass. |
| `REVIEW_REQUIRED` | Any review-severity finding, possible fit, unknown intent preference/recipient relevance, missing semantic result, stale inputs, edited draft, follow-up, or automatic sending disabled for this intent. |
| `AUTO_SEND` | All checks pass, inputs current, clean completed semantic review for exact hash, confirmed intent/preferences/contact, strong application fit where relevant, intent explicitly enabled for automation, no human edits, and live send restrictions pass. |

For service/speculative plans, require selected approved capabilities, a supported company relevance reason, and a confirmed recipient purpose; a high company score alone cannot authorize sending. Broad evidence can support a truthful general enquiry after human review.

Human approval records per-finding resolution and exact content hash. Soft style/fit uncertainties may be acknowledged with a reason. Unknown semantic-review availability can be resolved through an explicit full manual review checklist; ordinary “Send” does not silently waive it. Hard factual contradictions require correction or an approved fact/source update. Stale content requires regeneration or an explicit rebase with full validation; approving a stale badge is not enough.

## 11. Sending, concurrency, and review integrity

Keep one guarded send path, reached by company send, review send, worker send, follow-up send, and resend. Change its input to a draft ID, reviewed revision, and explicit send mode; it loads the company/recipient from the plan. Do not trust posted company ID or arbitrary recipient to bypass stored associations.

At send time, atomically claim the draft/content revision, recheck active input versions, hard validations, recipient status, suppression, and recent-domain contact. Policy/suppression/history read errors fail closed. Reserve the daily/interval slot atomically across handlers and worker. Snapshot exact outgoing subject/body/address in a new `send_attempt` before SMTP.

`send_attempt`: ID, draft/content revision, root thread reference, message ID, exact payload snapshot, status (`reserved`, `accepted`, `failed`, `unknown`), timestamps, diagnostic category. Concurrent requests cannot reserve the same draft revision twice. Confirmed failures release reservations; ambiguous timeout/handoff or process crash after SMTP remains `unknown` for manual reconciliation, never automatic resend. SMTP does not provide exactly-once delivery; record that limitation explicitly in UI.

After confirmed handoff, a transaction records send history and draft state together. Existing root `send_log` IDs and follow-up dates remain. New send attempts supply exact follow-up history without duplicating root follow-up schedules. A stable Message-ID and `In-Reply-To`/`References` for new threads support actual threading rather than subject-only `Re:`.

Preserve existing limits and suppression semantics, including the confirmed-bounce exception to the recent-domain rule. A different opportunity at the same domain does not bypass that rule. Do not change postal/footer policy based solely on refactoring: carry forward the existing source-derived configuration requirement as a compatibility rule until explicit market data is available, and mark uncertain market for review. This plan makes no new legal determination about which messages require which footer.

Affected: `internal/web/handler.go` (`executeSend`, SMTP construction, counters), `review.go`, `resend.go`, `followups.go`, `internal/store/{draft,sendlog,suppression}.go`, new `internal/store/sendattempt.go`, `internal/sendpolicy/policy.go`, `internal/worker/scheduler.go`.

## 12. Follow-ups and bounce retries

Follow-up generation reads the root sent draft's immutable intent, selected opportunity/title, target families, original recipient, actual sent text, and any prior follow-up attempts. Use the current approved candidate profile for new claims; keep the original snapshot for understanding history. Changed/closed job or revoked project evidence requires review. Do not change original intent because the company's current segment/profile changed.

The existing +3/+8 day schedule remains configurable as it is today. The due screen offers “Prepare follow-up,” which creates a normal v2 draft for review. Never generate new content on a GET request. Follow-ups always require explicit approval and preserve the original address unless deliberately changed and reviewed.

Specific applications continue the application and exact role; speculative enquiries continue engineering-opportunity/contact enquiries; service messages continue the specific service discussion. Every one uses the same composition/validation/review pipeline with a follow-up scaffold and preceding conversation. No generic payments constants remain. A resource can be mentioned only by approved resource ID with its actual configured link or explicitly approved availability.

Add `(root_send_log_id, followup_step)` uniqueness for a live draft and link accepted send attempts to the root. Step 2 requires step 1 accepted or explicitly skipped with a reason. Replied, bounced, suppressed, paused, or unknown-delivery threads cannot offer a ready-to-send follow-up. Record replies against a selected thread once a company has more than one; do not silently mark the latest thread. No inbox ingestion is introduced: only stored sent messages and explicitly entered reply text are available, labeled accordingly.

Legacy threads with unknown intent require a one-time human classification. Never infer application intent from their current company segment. A bounce retry retains original lineage/evidence snapshots, selects a new recipient explicitly, becomes a new unsent draft, and reruns freshness/content review; it cannot copy a sent approval.

Affected: `internal/draft/followups.go`, `internal/web/{followups,resend,company_actions}.go`, `internal/store/sendlog.go`, `internal/web/templates/{followups,sent,company}.html`.

## 13. Versioning and stale drafts

Store in `generation_snapshot`: approved profile revision/hash; opportunity revision; matching version/result; plan revision/intent; original advertised title; desired families; recipient snapshot; company business segment/relevance reasons; evidence IDs, revisions and quoted excerpts; selected project/capability IDs; scaffold version/hash; compose/review prompt versions; provider/model and parameters; generation timestamp; exact request; response and review metadata. Exclude API keys/SMTP secrets. Historical context reproduces the decision/input; probabilistic AI output is not promised to reproduce byte-for-byte.

Compute a canonical input hash and a separate final content/recipient hash. Stale reasons are both proactively marked on edits and calculated authoritatively before review/send. Comparison uses material versions, not company `updated_at` or repeated `last_seen_at` refreshes.

| Change | Unsent draft treatment |
| --- | --- |
| Active profile revision | Stale, rematch and regenerate/rebase. |
| Selected opportunity title/requirements/status | Stale, rematch; closed job blocks current specific application. |
| Intent, target families, selected contact | Stale; regenerate with corrected context. |
| Scaffold/compose prompt/model selection | Stale; deliberate regeneration or explicit rebase retaining text with fresh complete review. |
| Review prompt/policy version only | Re-review/re-evaluate exact content; no compulsory compose call. |
| Evidence materially changes/disappears/expires | Stale/review; remove unsupported claims or regenerate. |
| Subject/body edited | Increment content revision and invalidate all earlier approvals/reviews. |
| Company note unrelated to message, discovery timestamp refresh | No automatic invalidation. |

Regeneration does not discard the original first. Create the replacement under a controlled generation lease, then atomically mark the previous draft superseded only after the replacement is saved. Failure leaves the original readable and stale. Sent content is immutable, never regenerated in place, and never receives a retroactive changed intent/profile attribution.

## 14. Background jobs, budgets, and route behavior

Reuse `internal/worker/scheduler.go` loops and PostgreSQL polling. Do not activate the unused Redis job-list abstraction just for this change. Discovery upserts opportunities; research refreshes selected/relevant companies and partial job descriptions; matching runs after changed opportunity/profile revisions; drafting processes selected current plans rather than companies in one status.

Draft requests enqueue a `draft` with generation status and return quickly. Both manual and autonomous requests use the same scheduler/service. The worker can pick a manual queued request on the next short poll; the autonomous cooldown gates only creation of new autonomous work. No two-minute AI work inside a 30-second page handler. `cmd/prospector/main.go` wiring supplies one composition service with shared dependencies.

Worker selection excludes ignored opportunities, paused plans, blocked/unknown-delivery threads, bad/suppressed contacts, stale match assessments, and duplicate active generations. A company being `sent` or `enriched` does not prevent storing/matching new jobs. Research scheduling follows TTL and active plans instead of only `company.status='new'`; do not reset a business outcome to enriched after a refresh.

Revise `internal/aibudget/budget.go`: retain draft quotas for UI compatibility, add actual request quotas covering compose, review, and retries. Before starting a generation, atomically reserve capacity for compose plus review; consume each slot immediately before its provider attempt, release unused reservations on pre-call cancellation, and record failed attempts as attempts. Provider-level Gemini retries must use the same accounting hook. Composition completion without review capacity persists `review_pending`; it cannot send. Unknown usage remains conservatively consumed. New `AI_REQUESTS_PER_HOUR/DAY` and `AI_REVIEW_RESERVED_REQUESTS` are documented; defaults must be explicitly set at rollout against actual quota, not inferred from number of API keys.

All budget/lease operations use bounded contexts and owner tokens; background cancellation releases only owned unspent reservations. Generation transport retry limit is one automatic retry with exponential delay; invalid content requires user-triggered regeneration. No attempt endlessly restarts each ticker cycle. Dashboard reports compositions, reviews, failures, queued jobs, and remaining request budget separately.

Add `EMAIL_PIPELINE_MODE=disabled|v2_review|v2_auto` with default `v2_review` on v2 release. `AUTO_SEND_ENABLED` remains a master off switch; new `AUTO_SEND_INTENTS` starts empty. No auto-start loophole: worker, manual Generate, and admin batch all honor the same settings. Actual sending remains disabled in tests and evaluation fixtures.

## 15. Web/admin changes

Retain server-rendered HTML and SSE activity feed.

| Page/route | Behavior and affected files |
| --- | --- |
| New `/profile` | Draft/edit approved facts, preview diff, activate profile revision. New `web/profile.go`, `templates/profile.html`. |
| New `/opportunities` and `/opportunities/{id}` | Filter by source/company/family/fit/status; show full job text, missing requirements, source URL, revisions; select, ignore, or request review. New `web/opportunities.go` and templates. |
| Company detail | Show multiple opportunities; business segment separate from intent; create/select a contact plan with recipient and purpose. Replace apply-mode toggle with intent controls. `company_actions.go`, `handler.go`, `templates/company.html`. |
| Generate actions | Explicit “Generate draft” queues composition only. Automatic behavior is labeled separately. `/companies/{id}/generate-draft` requires a plan ID in v2; old ambiguous requests return an actionable selection screen. |
| `/review` | Editable subject and body, exact recipient, intent/title/fit, approved proof, evidence excerpts, structured findings, stale reason, re-review and explicit approve/send actions. `review.go`, `templates/review.html`. |
| `/drafts` | Filter by intent/opportunity/staleness/generation stage; link failed and superseded revisions; explicit regenerate/rebase. `drafts.go`, `templates/drafts.html`. |
| `/templates` | Versioned intent scaffolds; read-only legacy copy tab. `templates_admin.go`, `templates/templates_admin.html`. |
| `/followups` | Root intent and prior text; prepare -> review -> explicit send. Hide misleading generic copy/mailto shortcuts for unreviewed drafts. `followups.go`, `templates/followups.html`. |
| `/sent` | Immutable exact sent content, root thread, intent/opportunity where known, bounce/retry lineage. `sent.go`, `resend.go`, `templates/sent.html`. |
| Dashboard | Companies discovered separate from jobs discovered; fit queues, review backlog, stale drafts, request budget, mode. `handler.go`, `store/dashboard.go`, `templates/dashboard.html`. |

Old `/apply-mode` becomes a compatibility redirect to contact-plan selection; it must not set company segment or quietly choose an advertised role. Old template update requests cannot mutate active v2 prompts. Existing status/notes/contact actions remain but invalidate only relevant plans/reviews. Persist subject/body together with optimistic content revision checks, so two tabs cannot silently approve different text.

## 16. Migration and compatibility strategy

### Preflight before any live mutation

Stop application workers and sends for migration. Inventory tables/columns/indexes, existing migration state, counts and IDs, pending/sent drafts, template rows, company domains, duplicate signals, and follow-up logs. Back up the database and prove restore in a scratch database. Do not log credentials or full message bodies to console. Produce a migration report with row counts and ambiguous records requiring review.

### Tracked migration runner

Introduce a small Go migration command (`cmd/prospector-migrate/main.go`, `internal/store/migrate.go`) using the same connection configuration and `schema_migrations(version, checksum, applied_at)` with an advisory lock. Explicit migration command only; server startup checks version and refuses incompatible schema without starting workers. Update Makefile and Windows setup to invoke the same runner.

Fresh databases apply legacy scripts in order once, then additive v2 scripts. Existing untracked databases must not replay copy migrations: inspect structural milestones and record a documented legacy baseline; template-only migrations cannot be inferred from schema. Preserve current template rows and record their actual snapshot. If legacy structural columns are missing, use a dedicated repair migration with explicit preconditions rather than running all old files or guessing they succeeded. Checksums apply to migrations actually run/verified; a baseline record must be labeled as baseline, not falsely marked as execution of every historical script.

### Proposed additive migration sequence

| Proposed file | Work |
| --- | --- |
| `0017_candidate_profile.sql` | Profile/head and immutable revision storage; seed an unapproved minimal identity draft, no achievements. |
| `0018_opportunities.sql` | Opportunity, revision, match tables/indexes; source identity constraints. |
| `0019_research_evidence.sql` | Add evidence provenance/grouping fields and company `business_segment`; preserve legacy segment/score. |
| `0020_contact_plans_scaffolds.sql` | Plan/scaffold tables and initial guidance only; no old payments paragraphs copied into scaffolds. |
| `0021_draft_versions_review.sql` | Nullable v2 draft metadata, revisions/reviews, queue/lease fields, partial live-generation indexes. |
| `0022_send_attempts.sql` | Durable outgoing payloads, send attempt status, thread/follow-up lineage. |

Use text columns with CHECK constraints for new enums rather than introducing PostgreSQL enum migration complexity. Foreign keys and partial indexes are tested on the actual PostgreSQL version. Constraint creation follows backfill validation; no unique index is imposed on legacy duplicates without handling them.

### Backfill policy

- Keep every company/contact/signal/draft/send-log/template ID and body unchanged. `business_segment` maps only obvious business segments; legacy hiring/application segments are retained as legacy fields and new type is unknown unless independently known.
- Convert legacy `hiring_backend` signals to opportunities only where URL/title evidence is recoverable. Each source identity upserts once; keep a mapping from legacy signal ID to opportunity revision. Missing titles remain unknown, descriptions partial, match review-required. Never invent role titles or claim full descriptions were recovered.
- Existing company `application` preference becomes a suggested job-intent plan requiring opportunity selection/fit review; it is not a blanket specific-application authorization.
- Existing unsent drafts receive `pipeline_version=legacy`, `stale_reasons=[legacy_generation_context_missing]`. Exclude from automatic send and require deliberate v2 regeneration/rebase with profile/intent selection. Do not rewrite them.
- Existing sent drafts remain immutable legacy history with intent `unknown` in display where absent. Do not claim reconstructed metadata was used originally. Follow-ups require classification before preparation.
- Duplicate signals receive grouping metadata; preserve cited IDs. Recompute new company relevance from groups with versioned breakdown, preserving old score as legacy reference in the migration report.
- Backfill runs as an idempotent command in batches with a progress ledger, not long network calls inside SQL migrations. No AI or SMTP during backfill.

Verify before/after counts, original IDs, body hashes, contact/suppression records, relationships, and sent/follow-up timestamps. Added rows are expected; deletion of original history is not. New drafts must not inherit the old company-wide live-draft exclusion.

### Rollback

Use additive schema and retain old columns, but **do not roll back to the old sending binary** after v2 drafts exist: it does not understand stale/review metadata. Disable sends and roll back only to a compatible read-only/v2-aware release, or restore the tested backup if no post-migration writes need retention. Keep v2 tables/history even if composition is disabled. Drop legacy code/columns only in a later separately reviewed cleanup.

## 17. Package/file implementation map

| Existing package/files | Planned implementation |
| --- | --- |
| `internal/seed/adapters.go`, `remoteok.go`, `weworkremotely.go`, `remotive.go`, `himalayas.go`, `seeder.go`, `usjobs.go` | Shared job ingestion, full descriptions, stable opportunity identity, common relevance assessment; company upsert independent of job insert. |
| `internal/store/company.go`, `contact.go`, `pagecache.go`, `db.go` | Business-segment/company-priority separation; fact provenance/grouping; matching storage inputs; schema check. Add `profile.go`, `opportunity.go`, `plan.go`, `scaffold.go`, `draft_review.go`, `sendattempt.go`, `migrate.go`. |
| `internal/enrich/enricher.go` | Context-rich source excerpts, dedup, bounded fetch/cache expiry, research status independent of company outcome. |
| `internal/score/scorer.go` | Company-priority only; remove source-to-email-intent rules and repeated-row score boosts. |
| New `internal/profile`, `internal/opportunity` | Typed approved profile and pure role-fit assessment. |
| `internal/draft/generate.go` | Shared plan-based composition orchestration with immutable input snapshot and stage persistence. |
| `internal/draft/provider.go`, `claude.go`, `gemini.go`, `factory.go` | Compose/review contracts, strict parsing, model metadata, bounded timeouts and accounted retries. |
| `internal/draft/template.go`, `role.go` | Replace fragment fill/rejection assumptions with complete-email validation and source-title handling. Keep only useful generic helpers. Add `types.go`, `intent.go`, `selection.go`, `compose.go`, `validate.go`, `review.go`, `prompts/`. |
| `internal/draft/followups.go` | Conversation-aware composition request builder; remove universal payments prose. |
| `internal/sendpolicy/policy.go`, `internal/tonecheck/tonecheck.go` | Structured result aggregation, current review/version requirements, retain useful stylistic warnings. |
| `internal/aibudget/budget.go` | Atomic request reservations and attempt accounting, compose/review capacity. |
| `internal/worker/scheduler.go` | Opportunity matching and plan-based queued stages; durable claims; no company-status draft gate. |
| `internal/web/*.go` and `internal/web/templates/*.html` | Plan/profile/opportunity views, asynchronous generation, review invalidation, follow-up/retry lineage, exact recipient. |
| `cmd/prospector/main.go`, `Makefile`, `setup.bat`, `.env.example` | Wiring, schema gate, common migration command, explicit rollout/automation/request settings. |
| `README.md`, `docs/CURRENT_STATE.md` | Update operational instructions and architecture after implementation, linking this design and actual rollout status. |

No new distributed queue, identity/auth system, inbox integration, CV parser, embedding search, automated model-written profile approval, or general CRM redesign is included.

## 18. Test changes and acceptance criteria

### Existing tests to replace or extend

| Existing tests | Required treatment |
| --- | --- |
| `draft/template_test.go`: `TestServicePitch_PaymentSiteSignalLeadsWithPayments`, `...PaymentRoleTextLeadsWithPayments`, `...PaymentSignalTakesPriorityOverBackendGap` | Replace with evidence-aware selection tests: checkout is presence only; unrelated finance role does not select a payment service; payment-specific work selected only on supported need/fit. |
| Same file: `...NoStrongSignalFallsBackToGeneral`, `...BackendGapSignalLeadsWithBackend`, `...USMarketDropsMPesaBranding` | Replace string-constant assertions with correct approved capability/proof selection and no fabricated gap. General fallback is full-stack/backend. Market never forces or forbids otherwise relevant proof solely by source name. |
| `draft/role_test.go`: `TestExtractRole_UnrecognizedTitleUsesSuffixPattern` | Accurate extraction of a nontechnical title remains valid, but pair it with unrelated classification and blocked specific application. Do not merely rename Marketing Manager to Backend Developer. |
| `draft/role_test.go`: `TestExtractRole_NoTitleShapeFallsBackToGeneric` | Replace with missing-title/possible-match/review; no invented advertised title. |
| `seed/usjobs_test.go`: `TestUsJobSignalText` | Replace 300-byte truncation expectation with meaningful description retention, UTF-8-safe limits and partial flag. Extend `TestIsRelevantJobTitle` with full-stack hyphens, mixed titles and shared Kenyan behavior. |
| `score/scorer_test.go`: `TestClassifySegment`, `TestCalculateScore_CapsAt100`, breakdown tests | Replace source-always-hiring/default-agency expectations; repeated identical signals cannot reach 100. Test bounded unique categories and separate match score. Preserve determinism and explanatory breakdown properties. |
| `draft/template_test.go`: fragment word-count/repeated-role/leading-framing tests | Replace obsolete <=25/<=20 and fragment grammar assumptions with complete-email cases. Preserve missing/invalid citations, unsupported numbers, malformed response and placeholder coverage in the new validator. |
| `sendpolicy/policy_test.go`: `TestEvaluate` and ambiguity tests | High company score alone no longer auto-sends; require current approved profile, clean exact-content review, permitted intent and recipient. Hard blocks still apply when automation disabled. |
| `store/draft_test.go`: `TestListCompaniesForDrafting_RequiresEmailContact` | Move eligibility test to contact-plan queue; require selected active recipient while allowing multiple company opportunities. Retain blocked/review-required queue coverage with explicit stale/failed visibility. |
| `aibudget/budget_test.go` | Retain hourly/daily/cooldown intent; extend to atomic reservations, two-stage review, retries, failures and concurrent worker/manual requests. |
| `web/handler_test.go`, `enrich/enricher_test.go` | Preserve sender-header/contact filtering tests; add recipient/review binding and repeated-evidence tests. |

There are no existing dedicated tests for company deduplication retaining new jobs or intent-aware/fixed follow-up behavior in the inspected test inventory. Add those missing regressions; do not claim obsolete tests exist or delete coverage to conceal them.

### Required acceptance scenarios

| # | Fixture | Required result |
| --- | --- | --- |
| 1 | Marketing Manager; desired engineering family | Unrelated. No specific application. Relevant company may yield speculative plan; otherwise review/ignore. |
| 2 | Backend Engineer + meaningful matched requirements + approved proof | Specific application explains verified fit. Payments optional, not obligatory. |
| 3 | Full-Stack Developer + matching approved capabilities | Full-stack identity with backend strength; no unapproved frontend technology. Missing frontend requirement becomes a disclosed gap/review. |
| 4 | Generic company with checkout | No payment-repair diagnosis or payments-first fallback. |
| 5 | Agency offering software/web services | Evidence-grounded technical collaboration; relevant approved work/proof. |
| 6 | Enterprise, no payment need evidence | No mandatory reconciliation-only pitch. |
| 7 | Sent specific application -> follow-up | Exact application intent/role preserved; no outsourcing question. |
| 8 | Sent speculative enquiry -> follow-up | Continues engineering opportunities/technical-contact enquiry, no invented role. |
| 9 | Existing company gains Backend Engineer listing | New opportunity stored and independently selectable. |
| 10 | Marketing, Finance, Backend jobs at one company | Three distinct opportunities; selects backend without losing others. |
| 11 | Weak/ambiguous description | Possible match/review; title alone is insufficient to claim verified fit. |
| 12 | Profile activated after drafting | Unsent draft visibly stale; stale send rejected; regenerated/rebased version tracked. |
| 13 | Same fact repeated across pages/research runs | Same fact group, no independent score increase. |
| 14 | Finance Officer description says billing/collections | Unrelated; no automatic payment-service offer. |
| 15 | Fluent generated email claims unapproved skill | Block for identified invented fact; uncertain detection requires review, never auto-send. |
| 16 | Advertised title differs from application title | Block when mismatch is clear; parsing ambiguity requires review. |

Additional release gates: all five adapter fixtures exercise identical matching protections; updated descriptions create revisions without duplicating jobs; concurrent ingestion preserves uniqueness; user edits/recipient changes invalidate approval; cross-company opportunity/contact rejected; invalid AI references/JSON/truncated responses cannot send; prompt injection in a job description cannot alter intent; claimed skills omitted from model annotations still reach semantic review; provider failure/insufficient budget leaves an unsendable review-pending draft; follow-up and bounce retries retain context; multiple sends to one company still respect domain limits; simultaneous sends do not duplicate; unknown SMTP delivery is not retried automatically; migration preserves sent-body hashes and IDs.

### Verification method

Use deterministic provider fakes for pipeline tests, source HTML/JSON fixtures for adapters, and table-driven adversarial composer/reviewer outputs. PostgreSQL integration tests use a disposable migrated database, not the personal outreach database; Redis tests use isolated keys/instance. Fake SMTP transport records attempted sends and failure stages. Run race tests for shared enrichment cache, claims, and simultaneous handler/worker operations.

Live AI quality evaluation is a separate explicit offline-draft exercise with approved profile fixtures, saved outputs and rubric results, never real recipients or automatic sends. Human review should inspect all three intents, weak-source cases, and payments/non-payments examples before enabling automation. Automated semantic review cannot certify its own accuracy.

The prior investigation's `go test` attempt failed with `testing/internal/testdeps: package testmain: cannot find package`. Before implementation validation, inspect Go version/toolchain environment and build-cache configuration, then reproduce a minimal test package; repair the actual environment cause without silently changing module dependencies. This planning phase does not claim tests pass and does not rerun a known failure for a documentation-only change.

## 19. Rollout order and phase completion

1. **Preflight and migration foundation:** inventory/backup/restore proof, fix test toolchain, introduce migration tracking and startup schema gate. Deliver a reviewed data-impact report and passing scratch migration tests.
2. **Profile and opportunity storage:** approved-profile editor; additive job tables; unified adapter ingestion and backfill. Keep sends on the existing paused maintenance setting; no v2 generation before profile approval. Gate: multi-job/dedup/description fixtures pass.
3. **Matching, research and contact plans:** common fit rules, unique evidence groups, business relevance, recipient and intent selection. Gate: Marketing/Finance cannot become specific applications; ambiguous descriptions require review.
4. **Composition and complete review:** provider contract, versioned scaffolds/prompts, request accounting, queued generation, deterministic + semantic results. Gate: all three intents produce coherent reviewed drafts under fake providers and approved evaluation fixtures.
5. **Send-path and follow-up integration:** exact-content approvals, recipient pinning, durable attempts, every route/worker gate, thread-aware follow-ups and retries. Gate: no bypass through company send, admin generation, resends, follow-ups, or old form posts.
6. **Legacy transition and review-only pilot:** mark legacy unsent drafts stale, classify threads as needed, update dashboard/docs, enable `v2_review` and keep auto-send intents empty. Inspect actual stored templates/profile status and representative drafts; preserve all sent history.
7. **Selective automation:** only after acceptance and explicit rollout choice, enable individual intents with clean evidence/fit/review requirements. Monitor structured rejection reasons, stale backlog, budget usage, and manual edit frequency. Possible matches, uncertain recipients, and follow-ups stay review-required.

Each phase ends with its tests and migration checks before moving to the next. Do not ship a templates-only intermediate release that leaves the wrong-role declaration reachable.

## 20. Risks, limits, and inputs required before activation

| Risk | Mitigation |
| --- | --- |
| Approved profile initially lacks enough facts | Keep generation factual and review-required; collect explicit capability/project approval through profile UI. Do not infer production scale/years/frontend stack. |
| Source descriptions are inaccessible or misleading | Preserve partial data and extraction warnings; require review; no guessed title. |
| Duplicate jobs across boards or guessed company collisions | Stable per-source identity, visible possible duplicates, manual identity resolution, unchanged domain contact limits. |
| Semantic reviewer misses a fluent falsehood | Approved source restrictions, deterministic checks, adversarial fixtures, human pilot, no confidence claim based solely on model score. |
| Two AI stages consume more budget/latency | Bounded context, request reservations, queue stages, review capacity and explicit failure states. |
| Legacy schema is partly migrated or templates were edited | Schema inventory, preserved template snapshots, explicit baseline/repair, scratch restore test. |
| Stale drafts accumulate after profile edits | Visible reasons, explicit regenerate/rebase, no silent overwrite or repeated automatic spending. |
| Complete-email generation drifts back to payment identity | Dedicated selection/identity review findings and acceptance fixtures across all intents. |
| SMTP succeeds while persistence fails | Durable pre-send attempt and unknown status; no automatic duplicate retry; preserve reconciliation evidence. |

Before activating generation with real profile facts, Anthony needs to approve the candidate profile: actual technologies, capabilities, project responsibilities/results, resource links, and any employment/service/availability preferences he wishes to use. This is content approval, not an unresolved architecture choice. The design and implementation can proceed with empty optional sections and clearly labeled synthetic test fixtures.

No implementation, database migration, profile activation, email generation, or sending is performed by this document. The next action after review is implementation in the phase order above.
