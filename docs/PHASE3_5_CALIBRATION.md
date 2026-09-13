# Phase 3.5 calibration report

Date: 2026-09-12  
Status: **Part 1 complete. A real candidate-profile draft is saved and awaiting your activation.**

Part 1 repaired the source data, exercised the title gate and the requirement extractor against
273 real job descriptions, built your approved candidate profile as **draft revision 2**, and
fixed five defects that real data exposed — the last of them found only once the real profile
existed. Part 2 runs the moment you activate that revision.

Nothing here generated an email, made an AI request, started the scheduler or sent anything.
`EmailPipelineReady` is still `false`. No profile revision was activated. No contact plan was
created.

## Part 1

### 1.1 The blocker found first: there were no job descriptions

The initial survey of the 38 live opportunities found every one of them was **exactly 300
bytes**, `description_status = partial`, `metadata.extraction = "legacy_signal_prefix"`. They
were Phase 2 backfill reconstructions of the *old* pipeline's 300-byte truncated snippets, cut
mid-sentence, with their bullet structure already destroyed before Phase 2 ever saw them.
Roughly a third of each was the synthetic `"<title> at <company>. Headquarters: …"` prefix the
backfill itself wrote.

Across all 38, the extractor found **4 substantive requirements in total**. Calibrating on that
would have measured the old pipeline's truncation, not this matcher.

### 1.2 Fresh discovery crawl

With your approval, the Phase 2 adapters were run directly against live sources — no HTTP
server, no scheduler, no AI provider, no sending. Script: `.local/phase35-crawl.go`.

| Source | New opportunities |
| --- | ---: |
| weworkremotely | 117 |
| remoteok | 97 |
| remotive | 16 |
| jobboard (BrighterMonday + MyJobMag) | 5 |
| himalayas | 0 (feed returned nothing usable) |

| | Before | After |
| --- | ---: | ---: |
| Opportunities | 38 | 273 |
| Opportunity revisions | 38 | 301 |
| Companies | 417 | 596 |
| Ingestion-review rows | 354 | 476 |

The identity rules worked as designed: **27 of the 38 legacy stubs were matched by source
identity and repaired in place**, gaining a new revision carrying the real description rather
than becoming duplicates. Eleven remain `legacy_signal_prefix` — their listings have expired
from the feeds.

| Description quality | Count |
| --- | ---: |
| complete | 262 |
| partial | 11 |

Freshly crawled description size: min 543 bytes, **median 4,362**, max 25,280. Every opportunity
is `availability_status = unknown`; the adapters do not assert that a listing is open, which is
correct.

### 1.3 Title/family gate on 273 real titles (section 7)

| Family confidence | Count |
| --- | ---: |
| clear | 45 |
| ambiguous | 55 |
| none (unrelated) | 173 |

173 unrelated out of 273 is not a matcher failure — it is what unfiltered job-board discovery
returns. The Kenyan path in particular has never had a title filter.

**Two title defects found.**

*Fixed:* `FULL TIME: Software Engineer Position - React and Rest` read its role designation as
`"full time"`, because the designation is the text before the first separator. A leading segment
is now skipped only when it is one of a small closed list of posting qualifiers (`full time`,
`remote`, `urgent`, `hiring`, `contract`, `vacancy`, …). Any other leading noun still decides the
designation, so `Manager, Software Engineering` stays `ambiguous` and is **not** promoted into a
hands-on family, and `Hiring: Marketing Manager` stays `unrelated`. Fixture:
`TestLeadingPostingQualifierDoesNotHideTheRole`.

*Fixed with your approval:* `DESARROLLADOR FULL STACK` — a genuine Python/Angular/Go full-stack
role — was `unrelated` because `desarrollador` is not an English role noun. Spanish and
Portuguese engineering role nouns were added to the matcher, and slash-joined words are now split
so the gendered form `Desarrollador/a` is recognized. This relaxes nothing else: the title still
needs an approved desired specialization for a clear family, `Desarrollador` alone is still only
`ambiguous`, and `Ejecutivo de Marketing` is still `unrelated`. Fixture:
`TestNonEnglishEngineeringRoleNouns`.

**DevOps and Platform titles**, per your decision, remain `ambiguous`: an engineering role noun
without an approved desired specialization, capped at `possible_match`, always review. No profile
change was made.

### 1.4 Requirement extraction on real descriptions (section 6)

Your section 6 concern was correct, and the real descriptions demonstrated it conclusively. A new
deterministic, read-only `opportunity.Diagnose` reports every skipped line that nonetheless
contains a recognized term. The clearest case, Sticker Mule:

> Ship across web and mobile from the same codebase. We use Go, TypeScript, React, Expo, GraphQL, Postgres, and run on GCP.

The **entire stack** was lost. Others in the same shape: `You will work across our Laravel
services (PHP 8.2 / Laravel 10), React frontend (React 18 + TypeScript + Vite), and AWS
infrastructure`; `Our stack is Ruby on Rails, MongoDB, Elasticsearch, ClickHouse, Redis/Sidekiq,
and React`; label lists such as `Core: Linux, Nginx, PHP-FPM` / `Data: MySQL, Redis, RabbitMQ`;
and the Spanish `Desarrollar servicios y APIs REST utilizando principalmente Python y Go`.

The same data also exposed **dictionary false positives** that had to be fixed before any
broadening, or the change would have made matters worse:

| Real text | Was read as |
| --- | --- |
| "the **rest** of engineering depends on" | REST APIs |
| "Independent **workers**, full-time employees" | Background processing |
| "campaign closeout, **reconciliation**, and reporting" | Payment reconciliation |
| "**Work Authorization**" (an eligibility heading) | Authentication and authorization |

**Changes made, in this order:**

1. Over-broad single-word spellings removed: bare `rest`, `workers`, `reconciliation`, `testing`.
   The genuine spellings still match (`rest api`, `restful`, `background jobs`, `job queue`,
   `payment reconciliation`, `unit testing`, `automated testing`).
2. A veto list: a term is not matched when the line shows it means something else. `Work
   Authorization` no longer produces an authorization requirement, while
   `authentication and authorization in APIs` still does.
3. A **stack-sentence rule**: an otherwise-skipped prose line is read only when it *both* states
   what is built, used or operated (`we use`, `our stack`, `runs on`, `you will work`, `build`,
   `maintain`, `own the`, `using`, plus Spanish/Portuguese equivalents, or a `Core:` / `Data:`
   style label) *and* names something the dictionary recognizes. Company and benefits prose that
   names nothing concrete is still ignored.
4. Requirements recovered this way are marked `Section: "prose"` and are **never mandatory**,
   because prose does not say whether a demand is essential.

Excerpts remain exact, unknown lines remain visible, and every fixture is written against the
structural pattern rather than any company's text.

| | Before crawl | After crawl, before fixes | After fixes |
| --- | ---: | ---: | ---: |
| Substantive requirements extracted | 4 | ~450 | **1,374** |
| Opportunities with ≥1 requirement | 3 of 38 | — | **228 of 273** |
| Opportunities with ≥3 (the strong-match floor) | 0 | — | **144 of 273** |
| Opportunities still showing a missed term | — | 19 | **16** |

Representative result, Proxify's Senior Backend Developer: 11 mandatory requirements, 3 unparsed
lines, terms `Python, Django, 5 years of experience, PostgreSQL, SQL, Kubernetes, Docker, AWS,
Google Cloud, CI/CD, location: remote`.

Of the 38 residual misses, a substantial share are correct rejections — terms inside company
blurbs and "Work Authorization" headings that the extractor is right to ignore.

### 1.5 Requirement importance (section 8)

The distinction already exists as `Requirement.Mandatory`, read from section headings
(`Requirements` / `Must have` / `Minimum` versus `Nice to have` / `Preferred` / `Bonus`) and
in-line wording, and classification already uses it: only an unresolved *mandatory* gap blocks a
strong match.

Before the crawl it produced 1 mandatory and 3 optional, because there were no headings. Against
real descriptions it produces **1,020 mandatory and 354 non-mandatory**. The field works; it was
starved of input.

**Recommendation: do not add a requirement-importance ontology.** The three-way distinction you
described (mandatory / preferred / unknown) is already modelled, now populated, and prose-derived
requirements are explicitly the "unknown importance" bucket.

Section 8's other point is already correct: an unmet requirement is worded *"X is not present in
the approved profile"*, never as a claim about your ability, and the forbidden-claim entry says a
gap may be acknowledged honestly but never claimed.

### 1.6 Company relevance defect found and fixed (section 10)

All 1,697 pre-existing signal rows are `legacy_detection`:

| Kind | Rows |
| --- | ---: |
| hiring_backend | 392 |
| active_site | 392 |
| tech:wordpress | 244 |
| checkout_present | 220 |
| tech:react | 134 |
| tech:woocommerce | 123 |
| stale_site | 45 |
| paybill_listed | 38 |
| offers_tech_services | 37 |

`hiring_backend` counts as legacy need evidence, correctly capped at 10 points — but relevance
was `true` whenever any category scored above zero. That would have made **392 companies
"relevant"** on a pre-v2 keyword detection attached to a job ad now months old. Relevance is the
gate that unlocks speculative and service intents, so this was a real false-relevance path.

Tightened: a category whose every point came from a legacy detection is marked `LegacyOnly` and
cannot on its own establish relevance. It still contributes to the ranking score. The stated
reason becomes `legacy_detection_needs_reresearch`. Fixture:
`TestLegacyDetectionAloneDoesNotMakeACompanyContactable`. This is a tightening; no safeguard was
weakened.

`checkout_present` (220), `paybill_listed` (38), `active_site` (392) and every `tech:*` detection
already contribute exactly zero to relevance and zero to payment-need evidence.

### 1.7 Payments on live data (section 9)

| Check | Live result |
| --- | ---: |
| Evidence rows that could set `payment_need_evidenced` | 0 |
| `payment_complaint` signals — the only qualifying evidence kind | 0 |
| `paybill_listed` signals — explicitly non-qualifying | 38 |
| `checkout_present` signals — explicitly non-qualifying | 220 |
| Capabilities or projects selected | 0 (no active profile) |

258 live signals carry payment vocabulary and **none of them authorizes anything**. The correct
result is zero, and it is zero. Re-check after activation.

### 1.8 Changes made in Part 1

| Change | Evidence | Fixture |
| --- | --- | --- |
| `opportunity.Diagnose` added | Section 6 requires demonstrating failures before broadening. Read-only. | exercised by the survey |
| Leading posting qualifier skipped | `FULL TIME: Software Engineer …` downgraded a clear title | `TestLeadingPostingQualifierDoesNotHideTheRole` |
| Spanish/Portuguese role nouns; slash-joined words split | `DESARROLLADOR FULL STACK`, a real Python/Angular/Go role, rejected on language | `TestNonEnglishEngineeringRoleNouns` |
| Over-broad dictionary words removed; veto list added | "the rest of engineering", "independent workers", "campaign reconciliation", "Work Authorization" | `TestOrdinaryEnglishDoesNotBecomeRequirements`, `TestCalibrationResidualExtractionCases` |
| Stack-sentence rule for prose | Sticker Mule's whole stack lost; four more in the same shape | `TestStackStatedInProseIsExtracted`, `TestLabelledStackListIsExtracted`, `TestCompanyProseDoesNotBecomeRequirements`, `TestSpanishRequirementProseIsExtracted` |
| Legacy-only evidence cannot establish relevance | 392 companies would have become contactable on a keyword detection | `TestLegacyDetectionAloneDoesNotMakeACompanyContactable` |
| A shared technology no longer evidences a capability; supported capabilities ordered by support | A Go/TypeScript/React vacancy matched 23 capabilities including double-entry accounting and payments | `TestSharedTechnologyDoesNotEvidenceAnUnrelatedCapability`, `TestSupportedCapabilitiesAreOrderedBySupport` |

All A–Q acceptance cases preserved. Full suite passes including the isolated PostgreSQL tests.
`gofmt` clean.

### 1.9 Candidate profile draft (sections 1 to 3)

Your approved facts were encoded as **draft revision 2**, saved with `CreateProfileDraft` and
**not activated**. Revision 1, the sparse seed, remains approved-but-inactive. No revision is
active, so matching still answers `profile_required`.

| Section | Count |
| --- | ---: |
| Desired role families | 3 (Backend Engineering, Full-Stack Development, Software Engineering) |
| Technologies | 28 |
| Capabilities | 23 |
| Projects | 5 (Prospector, LearnerOS, PXYZ platform, Seabytes, Zentora) |
| Project responsibilities | 65 |
| Project outcomes | 5 (Prospector only, all technical) |
| Experience domains | 10 |
| Specializations | 11 |
| Resources | 1 (CV, `available_on_request`, no URL) |

Left deliberately absent: years of experience, seniority labels, quantitative scale, business or
performance metrics, availability, salary, pricing, certifications, references, the legacy
portfolio URL and the legacy phone number.

Two encoding decisions worth your explicit check:

1. **`employment_types` carries six values** — full-time, part-time, contract, freelance,
   permanent, temporary. This field does double duty in Phase 3: a non-empty list is what
   confirms employment interest, and a *confirmed* list that excludes an advertised type is
   reported as a `known_preference_conflict` that blocks a strong match. Leaving it empty would
   have made every job "employment preference unknown", contradicting your "Employment interest:
   Yes". Listing these six encodes "yes, and not restricted". Internship is deliberately absent.
2. **Sections 18 and 19** (authentication systems, digital identity and verifiable credentials)
   were described as "project/domain" rather than as named projects, so they are encoded as
   capabilities (`cap-auth`, `cap-verifiable-credentials`) and domains, evidenced by LearnerOS and
   PXYZ, rather than invented as standalone project entries.

### 1.10 Dry run against the draft, and the fifth defect

A read-only dry run loaded the draft in memory and assessed all 273 opportunities. It stored
nothing, activated nothing and created no plan. It immediately exposed a real over-claim:

> Sticker Mule's **Software engineer** vacancy (Go, TypeScript, React, GraphQL, Postgres, GCP)
> matched **23 capabilities**, including double-entry accounting systems, payment integrations,
> financial transaction systems, reconciliation workflows and verifiable credentials.

The cause: a capability counted as "supported by a requirement" if it merely *referenced a
technology* the vacancy demanded. Because your payments, accounting, identity and reconciliation
capabilities were genuinely built in Go and PostgreSQL, a single `Go` requirement dragged all of
them in — precisely the "domain worked in is not the entire professional identity" failure your
section 2 warned about.

Fixed: a capability is supported only when the requirement actually names what that capability is
about. Sharing a technology now evidences the *technology*, and projects still match through
technology — which is what keeps a payments project usable as backend proof — but a capability
must be named. Supported capabilities are also now ordered by how many requirements support them,
so the two that score are the two the vacancy asks for most rather than the two that sort first
alphabetically. Fixtures: `TestSharedTechnologyDoesNotEvidenceAnUnrelatedCapability`,
`TestSupportedCapabilitiesAreOrderedBySupport`.

Effect on the same vacancy: 23 capabilities to 9, all genuinely named by the description; the
payments, accounting, reconciliation and credential capabilities disappear from every vacancy
that does not ask for them, and still appear on one that does.

**Dry-run outcome across 273 opportunities** (indicative only — nothing stored):

| Classification | Count |
| --- | ---: |
| strong_match | 6 |
| possible_match | 94 |
| unrelated | 173 |

Six strong matches: Cosuno (Senior Full Stack Developer, TypeScript), Sticker Mule (Software
engineer), Wakacje.pl (Software Engineer GO), Wonderdog (Full-Stack Product Engineer), Kruger
NearShore (DESARROLLADOR FULL STACK — the listing the non-English fix recovered), and Delinea
(Staff Software Development Engineer). The 94 possible matches are overwhelmingly held back by
`unresolved_mandatory_requirement`, which is the system correctly refusing to overlook a stated
mandatory demand your profile does not cover.

Exactly **2 of 273 vacancies state a payment-engineering requirement**, so payments capability
selection is available on those two and nowhere else.

Payments invariant re-checked against the real profile, on the Cosuno full-stack vacancy:
capabilities selected were React application development, Backend API development and Frontend
and backend integration; proof was LearnerOS and Seabytes; positioning
`targeted_to_stated_requirements`; and the forbidden claims included `recipient_payment_problem`
plus one `unapproved_requirement_*` entry for each of Terraform, CI/CD and Node.js. No payments
capability led, and no payment need was asserted.

A known dictionary gap, reported rather than patched: the requirement dictionary is
backend-and-web oriented and has no vocabulary for accounting or ledger concepts, so
`cap-double-entry` and `cap-financial-systems` can currently only be matched through payment
terms. If you want fintech-backend vacancies to match those capabilities directly, that needs a
small approved vocabulary addition, and there is no evidence in the current 273 that it is needed.

## Part 2: awaiting your activation

Sections 4, 5, 9 (selection side), 10 (scoring side), 11 and 12 all require an *active* approved
candidate profile. Draft revision 2 is saved and visible at `/profile`; section 3 reserves
activation to you, and nothing here activated it.

Section 12's regression already exists and is permanent:
`TestPaymentsProjectIsBackendProofOnlyWithoutPaymentNeed` in `internal/plan`, asserting the
payments project is selected as `proof`, the payments capability does not lead,
`recipient_payment_problem` stays forbidden, and the intent is decided independently. It will be
re-confirmed against your real profile in Part 2.

What Part 2 will have to work with, once a profile exists: **100 of the 273 titles are in or near
an approved desired family** (45 clear, 55 ambiguous), 94 of those have complete descriptions,
and 76 have at least three extracted requirements — so the strong-match floor is genuinely
reachable now, subject entirely to which capabilities you approve.

## Explicit answers, so far as Part 1 allows

1. **How many of the 38 are genuinely relevant?** The question has moved: there are now 273
   opportunities, of which 100 are in or near an approved family on title. Of the original 38, 27
   were repaired with real descriptions and 11 remain expired stubs. Fit cannot be stated without
   your approved capabilities.
2. **Strongest application candidates?** Not answerable without a profile. 76 opportunities now
   clear the evidence floor for a strong match, so the answer will be real once you activate.
3. **Which relevant opportunities are only possible matches, and why?** Before the crawl the
   answer was "all of them, because every description was a 300-byte fragment". That artifact is
   gone. The remaining honest answer depends on your approved facts.
4. **Which were incorrectly discovered by the old pipeline?** 173 of 273 titles are unrelated,
   including Field Technician, Ongoing Recruitment, Customer Support Intern and Lead Product
   Designer. The Kenyan job-board path has no title filter at all, exactly as the original
   investigation predicted.
5. **Did real descriptions reveal title-classification problems?** Yes, two, both now fixed: the
   posting-qualifier prefix and non-English role nouns.
6. **Did real descriptions reveal requirement-extraction problems?** Yes, decisively, and in both
   directions — whole stacks lost to prose, and four false-positive patterns from over-broad
   dictionary words. All fixed, with the false positives fixed first.
7. **Did any unrelated role get close to becoming a specific application?** No. All 173 unrelated
   titles score 0 and are blocked at four independent layers.
8. **Did payment keywords create incorrect relevance or intent?** No. 258 payment-vocabulary
   signals contribute zero relevance and zero payment-need evidence.
9. **Was a payments project usable as backend proof without payment outreach?** Yes, in the
   permanent synthetic regression. Not yet re-confirmed against a real profile.
10. **Are important candidate facts missing?** Yes — this is now the only remaining blocker.
11. **Are Phase 3 decisions trustworthy enough to feed Phase 4?** The invariants are, and the
    inputs are now much closer: real descriptions exist and extraction works. The last missing
    input is your approved profile.
