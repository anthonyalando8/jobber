# Prospector — Autonomous Daytime Prospecting Refactor

You are working on **Prospector**, a personal prospecting system used by one developer in Kenya to find companies that may need backend, API, payments, integrations, or contract engineering work.

This is NOT intended to become a SaaS product.

It is a personal tool that should be able to run unattended for several hours during the developer's workday and continuously:

1. discover potential companies
2. verify the company and website
3. research the company
4. collect evidence
5. score and rank prospects
6. identify only the strongest opportunities for outreach
7. generate a small number of highly relevant email drafts
8. remove obvious AI/electronic writing patterns
9. place drafts into a human review queue
10. send only after human approval
11. schedule and surface follow-ups
12. record outcomes

The developer is currently using a **Gemini free-tier API**, so AI generation is a constrained resource.

The system must therefore be designed around:

> **Continuous research, selective AI generation, human-controlled sending.**

Do NOT build a system that generates hundreds of emails simply because hundreds of companies were discovered.

---

# 1. Understand the current system first

Before changing code, inspect the entire repository.

Read:

* project structure
* `PROSPECTOR_SPEC.md`
* current models
* migrations
* seeders
* enrichment
* crawler
* scoring
* drafting
* templates
* SMTP sending
* Redis
* dashboard
* review UI
* company UI
* follow-ups
* configuration
* existing activity logging

The current implementation is the source of truth.

Do not blindly implement the original specification if the repository has intentionally diverged from it.

Run the application/build/tests first and understand the current state.

Do not rewrite working components simply for architectural aesthetics.

---

# 2. New operating model

The system should now behave more like an **autonomous daytime prospecting agent**.

The developer may start Prospector in the morning and leave it running for several hours.

During that period, the system should continuously work through available non-AI tasks.

Conceptually:

```text
DISCOVER
    ↓
VERIFY
    ↓
RESEARCH
    ↓
SCORE
    ↓
QUEUE
    ↓
SELECT BEST PROSPECTS
    ↓
AI DRAFT
    ↓
HUMAN-TONE QUALITY CHECK
    ↓
REVIEW QUEUE
    ↓
HUMAN APPROVAL
    ↓
SEND
    ↓
FOLLOW-UP
```

The system must **not** continuously generate AI emails.

Research can be continuous.

AI drafting must be deliberately throttled.

Sending must remain human-approved.

---

# 3. Separate the pipeline into three resource classes

Architecturally distinguish between:

## A. Cheap operations

These can run continuously:

* job-board discovery
* CSV/manual import
* domain verification
* HTTP fetching
* page parsing
* signal extraction
* duplicate detection
* scoring
* ranking
* follow-up calculation
* database maintenance
* queue maintenance

## B. Expensive operations

These consume external AI quota:

* email generation
* human-tone rewriting
* advanced classification if AI is required

These must be rate-limited.

## C. Sensitive operations

These must require explicit human approval:

* sending email

Never blur these three categories.

---

# 4. Continuous autonomous worker

The current application is mostly synchronous HTTP-triggered.

Refactor toward a lightweight internal scheduler/worker model appropriate for a single Go binary.

Do NOT introduce:

* Kubernetes
* NATS
* microservices
* distributed queues
* complicated orchestration

Use a simple internal worker/scheduler with PostgreSQL and Redis where appropriate.

The binary should be capable of running:

```text
prospector
```

and continuously performing background work.

The HTTP server should remain available for the UI.

---

# 5. Worker loops

Create separate logical loops/jobs for:

### Discovery worker

Periodically discovers new companies from configured sources.

Example:

```text
every 10–30 minutes
```

Make the interval configurable.

Avoid repeatedly scraping the same source unnecessarily.

---

### Research worker

Continuously finds companies that need enrichment.

Example priority:

```text
verified domain
AND
not recently researched
```

Research should happen independently of AI.

---

### Scoring worker

After enrichment:

```text
signals
→ score
→ score breakdown
→ ranking
```

This should be deterministic and require no AI.

---

### Draft selection worker

This is important.

Do NOT say:

```text
company enriched
→ generate email
```

Instead:

```text
company enriched
→ score
→ wait in prospect queue
```

Only the strongest prospects become eligible for AI drafting.

---

# 6. AI generation must be quota-aware

Gemini free-tier usage is currently limited.

Treat AI generation as a scarce resource.

Create an explicit AI generation budget/rate limiter.

Configuration should support values such as:

```env
AI_DRAFTS_PER_HOUR=5
AI_DRAFTS_PER_DAY=30
AI_MIN_SCORE=70
AI_DRAFT_COOLDOWN_SECONDS=600
AI_ENABLED=true
```

Do not assume these exact values are correct; make them configurable.

The system must enforce:

```text
hourly limit
daily limit
minimum score
minimum evidence
minimum time between calls
```

before calling Gemini.

If the AI budget is exhausted:

```text
do not generate
do not fail the pipeline
leave prospect in draft_queue
continue researching other companies
```

This is critical.

The system should remain productive even when AI quota is exhausted.

---

# 7. AI should operate on a priority queue

Create a clear AI candidate queue.

A prospect should only enter it if:

* domain is verified
* company identity is reliable
* contact exists or a realistic contact path exists
* evidence is sufficient
* score exceeds configured threshold
* company hasn't recently been contacted
* no existing live draft exists
* prospect hasn't already been rejected
* AI budget is available

Prioritize candidates by:

1. score
2. evidence freshness
3. contact quality
4. strength of commercial opportunity
5. recency of hiring/business signal

The system should generate emails for the **best opportunities first**.

---

# 8. Never waste an AI call on weak prospects

Before calling Gemini, perform deterministic checks.

For example:

```text
score < threshold
→ no AI call

no verified domain
→ no AI call

no meaningful evidence
→ no AI call

no contact path
→ no AI call

already drafted
→ no AI call

recently contacted
→ no AI call

insufficient evidence
→ no AI call
```

The objective is:

> One excellent AI-generated email is more valuable than ten mediocre ones.

---

# 9. Research should become deeper than email generation

A company can be researched extensively without consuming AI quota.

For each company, collect:

### Identity

* company name
* domain
* domain verification status
* company description
* location

### Contact

* email
* named contact
* phone
* contact page
* role

### Technical evidence

* technologies
* APIs
* payment systems
* checkout
* integrations
* mobile/web products
* backend evidence

### Business evidence

* products
* services
* pricing
* clients
* portfolio
* hiring
* recent activity

### Opportunity signals

Examples:

```text
hiring_backend
hiring_go
hiring_python
hiring_django
payment_integration
checkout
api_product
mobile_app
software_product
active_hiring
technical_team
```

Store the source URL and evidence for every meaningful signal.

---

# 10. Domain verification is mandatory

Never trust:

```text
Acme Ltd
→ acme-ltd.com
```

as the final domain.

Create explicit domain states:

```text
unknown
candidate
verified
failed
manual
```

A wrong domain must not proceed into deep enrichment.

Use whatever domain-discovery mechanism is currently available.

If external search is not configured, create a provider abstraction and safely place uncertain companies into manual review.

The system should never silently enrich an obviously unrelated domain.

---

# 11. Research scheduling

A company should not be researched continuously.

Introduce research freshness.

For example:

```text
new company
→ research immediately

researched recently
→ skip

research older than configured TTL
→ eligible again
```

Use configurable values.

Example:

```env
RESEARCH_TTL_HOURS=24
```

Different page types may eventually have different TTLs.

Do not repeatedly crawl a company every few minutes.

---

# 12. Real per-host crawl throttling

The current Redis host throttle is decorative.

Fix it.

Before requesting:

```text
https://example.com/about
```

the crawler must enforce the host's next-allowed request time.

Implement this centrally so every crawler path uses the same mechanism.

Also implement reasonable robots.txt handling.

Do not create a crawler that behaves aggressively.

---

# 13. Persistent page cache

The existing `page_cache` table should become the actual cache.

Persist:

* URL
* host
* HTTP status
* content
* content hash
* fetched time
* expiry
* title if useful

Avoid refetching unchanged pages unnecessarily.

The cache should survive application restarts.

---

# 14. Scoring

Refactor scoring so it answers:

> How likely is this prospect to be worth my time and potentially become paid engineering work?

Score based on real evidence.

Possible dimensions:

### Need

* actively hiring backend
* hiring relevant technologies
* payment/API evidence
* active software product

### Commercial potential

* established business
* commercial product
* multiple products/services
* visible customers

### Contactability

* named person
* direct email
* phone
* verified domain

### Evidence quality

* fresh evidence
* multiple independent signals
* strong source

Add penalties for:

* unverified domain
* stale data
* no contact
* no useful signals
* recent contact
* suppression

Keep scoring deterministic.

Do not use AI for normal scoring.

---

# 15. Score breakdown must be stored

Every score should be explainable.

Example:

```text
82/100

+15 hiring backend engineer
+10 Django evidence
+10 payment functionality
+8 named contact
+5 active software product
+5 fresh evidence
-5 no direct contact
```

Store enough information for the UI to display this.

Do not make the user trust an unexplained number.

---

# 16. Email generation philosophy

This is one of the most important requirements.

The generated email must NOT sound like AI.

The goal is not:

> "make AI text undetectable."

The goal is:

> **write like a competent human developer sending a short, genuine business email.**

The email should sound like the developer actually noticed something about the company.

Avoid:

* "I hope this email finds you well"
* "I came across your impressive company"
* "I was thrilled to see"
* "leveraging"
* "innovative solutions"
* "cutting-edge"
* "seamless"
* "robust"
* "exciting opportunity"
* "I wanted to reach out"
* excessive politeness
* generic compliments
* marketing language
* corporate jargon
* long introductions
* fake enthusiasm
* unnecessary bullet points
* excessive em dashes
* unnatural semicolons
* repetitive sentence structures
* "As an AI..." language
* obvious template language

Do not try to make every email clever.

Simple is better.

---

# 17. Email should sound like a developer

The sender is a developer looking for contract/freelance work.

The tone should be:

* direct
* calm
* professional
* conversational
* confident
* short
* technically credible
* not desperate
* not overly salesy

Example style:

```text
Hi Jane,

I noticed you're currently hiring for a backend engineer with Django experience.

If the need is partly about getting the work delivered rather than adding another full-time person, I can help on a contract basis. I work mainly on backend systems, APIs and payment integrations.

If that's relevant, happy to have a short chat.

Anthony
```

This is only a style reference.

Do not copy it mechanically.

---

# 18. Observation must be specific

Every email should contain a real observation.

Bad:

```text
I noticed your company is growing and doing interesting things.
```

Good:

```text
I noticed you're currently hiring a Django backend developer.
```

Better:

```text
I noticed you're hiring for a Django backend role and your platform already handles online payments.
```

Only use claims supported by stored signals.

---

# 19. Hypothesis must be modest

Do not pretend to know the company's internal problem.

Bad:

```text
Your engineering team is clearly overwhelmed.
```

Good:

```text
If the goal is to get some of that backend work delivered without adding another full-time hire, I may be able to help.
```

The email should distinguish:

```text
what we know
```

from:

```text
what we think might be useful
```

---

# 20. Email length

Default target:

```text
70–130 words
```

Shorter is generally preferable.

Avoid long AI-generated essays.

The user should be able to read the entire email in seconds.

---

# 21. Separate generation from human-tone cleanup

Do not rely on one giant prompt.

Use a controlled pipeline:

```text
evidence
→ draft generation
→ structural validation
→ human-tone cleanup
→ final validation
→ review queue
```

However, because Gemini quota is limited, be careful:

## Prefer ONE AI call per email.

The generation prompt should already request the final human-sounding version.

Only use a second AI cleanup call if explicitly configured and quota allows it.

Default:

```env
AI_HUMANIZE_SECOND_PASS=false
```

If the second pass is disabled, use deterministic local checks to catch obvious AI/electronic writing patterns.

---

# 22. Deterministic AI-tone detector

Implement a local quality gate that checks for patterns such as:

* generic greeting
* generic company compliment
* corporate buzzwords
* excessive adjectives
* repeated sentence structures
* long paragraphs
* excessive punctuation
* excessive em dashes
* fake enthusiasm
* unnecessary "I hope"
* unnecessary "I wanted to reach out"
* generic CTA
* unsupported claims
* placeholder text

If detected:

```text
draft
→ needs_manual
```

Do NOT automatically send.

The detector is a quality gate, not a magical AI detector.

---

# 23. AI prompt should enforce natural writing

The model should be told:

You are writing a short email from one software developer to a real company.

Write as a person, not as a marketing department.

The reader should feel that the sender noticed one specific thing about their company and is making a reasonable suggestion.

Use normal everyday professional English.

Prefer:

* short sentences
* concrete observations
* natural transitions
* one clear idea
* one simple CTA

Avoid:

* marketing language
* corporate jargon
* exaggerated enthusiasm
* generic compliments
* long introductions
* artificial politeness
* "I hope this email finds you well"
* "I came across"
* "I was impressed"
* "innovative"
* "cutting-edge"
* "seamless"
* "robust"
* "leverage"
* "exciting"
* "solutions"
* "ecosystem"
* "transform"
* "empower"
* "unlock"
* "revolutionize"

Do not mention AI.

Do not claim knowledge that isn't in the evidence.

---

# 24. Draft structure

Keep AI output structured.

Something like:

```json
{
  "subject": "...",
  "body": "...",
  "signal_ids": ["..."],
  "confidence": 0.0
}
```

The body must contain only claims supported by the selected signals.

The subject should be short and natural.

Avoid clickbait subjects.

Examples of acceptable styles:

```text
Backend support for [Company]
```

or:

```text
Quick question about your backend hiring
```

or a simple relevant subject based on the actual observation.

---

# 25. Human review queue

The user does NOT want to manually review hundreds of AI-generated emails.

The goal is to review a small number of high-quality emails.

Create a queue showing:

```text
Priority
Company
Score
Why this company
Evidence
Contact
Subject
Email
AI confidence
Potential AI-tone warnings
```

The user should be able to:

```text
Approve & Send
Edit
Save
Skip
Needs Manual
Suppress
```

Keyboard shortcuts are welcome.

---

# 26. Human review should be fast

The ideal workflow should be:

```text
open Prospector
↓
see best prospect
↓
read evidence
↓
read email
↓
quick edit if necessary
↓
Send
↓
next prospect
```

The user should not have to navigate through multiple screens.

Keep `/review` as the canonical review interface.

Remove the competing draft-review workflow.

---

# 27. Never autonomously send

Even though the system runs unattended for hours:

## Email sending always requires explicit human approval.

The worker may:

* discover
* research
* score
* draft
* queue
* schedule

It may NOT:

```text
automatically send email
```

without the user explicitly approving it.

This is a hard requirement.

---

# 28. Send guard

Keep ONE `executeSend()` implementation.

Before sending verify:

1. valid email
2. no suppression
3. no placeholder
4. verified company/contact
5. no recent contact to same domain
6. minimum send interval
7. daily limit
8. message is in approved review state

Then:

```text
send
→ send_log
→ follow-up dates
→ company outreach state
```

Never bypass the guarded path.

---

# 29. SMTP identity

Correctly use:

```env
FROM_NAME
FROM_EMAIL
REPLY_TO
```

Generate:

```text
From: Name <email>
Reply-To: configured reply address
```

Do not use the raw SMTP username as the visible sender if configuration provides a display name.

Add tests for this.

---

# 30. Sending limits

Keep a conservative configurable daily limit.

Add optional ramping.

Example configuration:

```env
SEND_DAILY_CAP=10
SEND_RAMP_ENABLED=true
```

Do not assume these numbers are final.

The system should make it impossible for a bug to suddenly send hundreds of emails.

---

# 31. Follow-ups

After a successful send, create follow-up tasks.

Example:

```text
initial
→ follow-up 1
→ follow-up 2
→ final follow-up
```

Do not automatically send follow-ups.

Show them in the review queue.

The user should explicitly approve each follow-up.

---

# 32. Outcomes

Track:

```text
no_response
interested
not_interested
wrong_contact
referred
meeting
proposal
won
lost
```

This data is important.

Eventually it should allow the scoring system to answer:

> Which types of companies actually become customers?

Do not build machine learning yet.

Simply collect clean data.

---

# 33. Continuous operation

The application should support a long-running mode.

The worker should:

```text
discover
→ research
→ score
→ queue
→ draft only when budget permits
→ wait
→ continue
```

When there is nothing to do:

```text
sleep
→ wake
→ check again
```

Do not busy-loop.

Make intervals configurable:

```env
DISCOVERY_INTERVAL_MINUTES=20
RESEARCH_INTERVAL_SECONDS=30
SCORING_INTERVAL_SECONDS=15
DRAFT_INTERVAL_SECONDS=60
```

Use sensible defaults.

---

# 34. Graceful shutdown

Because this is a long-running personal application:

Implement graceful shutdown.

On SIGINT/SIGTERM:

* stop new work
* finish safe in-flight work where possible
* close database connections
* close Redis
* stop HTTP server
* stop workers

Do not corrupt partially processed jobs.

---

# 35. Crash recovery

A long-running application will eventually crash or be closed.

Do not rely entirely on in-memory queues.

Important work states should be recoverable from PostgreSQL.

For example:

```text
research_pending
researching
researched
draft_pending
drafting
drafted
review
sent
```

On startup, recover stale `researching` / `drafting` states where appropriate.

Do not create a complicated distributed queue.

Simple database-backed work state is enough.

---

# 36. Avoid duplicate work

Before processing a company:

Check:

* already researched recently?
* already has active draft?
* already contacted?
* suppressed?
* domain recently fetched?
* already in review?

The worker must be idempotent.

Running Prospector for 8 hours should not produce duplicate work simply because the same discovery source is scanned repeatedly.

---

# 37. Activity log

Keep the existing activity log.

Make it useful for long-running operation.

Examples:

```text
09:12 discovered 8 companies
09:14 verified 5 domains
09:20 researched 6 companies
09:25 scored 6 prospects
09:27 selected 2 prospects for drafting
09:28 generated draft for XYZ
09:30 AI budget exhausted for this hour
09:45 researched 12 additional companies
```

The user should be able to see that the system is alive and productive.

---

# 38. Dashboard redesign

The dashboard should become an operational cockpit.

Top section:

```text
TODAY

Companies discovered
Companies researched
High-value prospects
AI drafts generated
Emails awaiting review
Emails sent
Follow-ups due
Replies
```

Then:

```text
READY FOR REVIEW
```

Then:

```text
TOP PROSPECTS
```

Then:

```text
FOLLOW-UPS
```

Then:

```text
RECENT ACTIVITY
```

Do not overcomplicate the UI.

---

# 39. AI budget dashboard

Because Gemini is a scarce resource, expose:

```text
AI USAGE

Today: 7 / 30 drafts
This hour: 2 / 5
Next available: 10:42
```

If quota is exhausted:

```text
AI drafting paused
Research continues normally
```

This is important for trust.

---

# 40. Research queue visibility

The UI should show that the system is still doing useful work even when AI generation is paused.

Example:

```text
Research queue
27 companies waiting

Currently researching
Acme Ltd

Recently researched
12 companies
```

This reinforces the separation between research and AI generation.

---

# 41. Search/filter

Company list should support:

* name search
* segment
* score
* status
* source
* has email
* contacted
* follow-up due

Sort by:

* score
* newest
* freshest evidence
* never contacted
* follow-up due

---

# 42. Database changes

Do not redesign the entire schema.

Extend the current model only where necessary.

Useful fields may include:

```text
domain_status
domain_confidence
last_researched_at
next_research_at
score
score_breakdown
draft_status
ai_generated_at
ai_model
ai_generation_cost/usage if available
```

Use migrations.

Do not destroy existing data.

---

# 43. Testing strategy

Add focused tests.

Do NOT attempt 100% coverage.

Prioritize:

### AI budget

* hourly limit enforced
* daily limit enforced
* weak prospects do not consume AI
* quota exhaustion does not stop research

### Drafting

* evidence required
* signal IDs valid
* unsupported claims rejected
* word limits enforced
* AI-tone warnings detected

### Sending

* placeholder blocked
* suppression blocked
* recent domain blocked
* daily cap enforced
* From correct
* Reply-To correct
* approval required

### Workers

* duplicate company not repeatedly processed
* stale jobs recover
* shutdown is graceful

### Scoring

* deterministic
* explainable
* no stuck score state

---

# 44. Important principle: don't make AI responsible for everything

Before using Gemini, ask:

> Can this be done deterministically?

If yes, do it without AI.

Examples:

```text
email extraction       → deterministic
phone extraction       → deterministic
technology detection   → deterministic
payment detection      → deterministic
freshness              → deterministic
domain verification    → deterministic where possible
score                  → deterministic
duplicate detection    → deterministic
queue selection        → deterministic
AI writing             → Gemini
```

This is how the system remains useful even with a free-tier AI quota.

---

# 45. Important principle: quality beats volume

Do NOT optimize for:

```text
emails generated/hour
```

Optimize for:

```text
good prospects reviewed/day
```

The system should deliberately reject weak leads.

A good day might produce:

```text
80 companies discovered
45 researched
18 useful prospects
6 high-priority
3 emails generated
2 sent
1 conversation
```

That is much better than:

```text
500 companies
100 AI emails
0 replies
```

---

# 46. Final target architecture

The finished system should conceptually look like:

```text
                ┌─────────────────────┐
                │ Discovery Scheduler │
                └──────────┬──────────┘
                           ↓
                 ┌──────────────────┐
                 │ Company Identity │
                 │ + Domain Verify  │
                 └────────┬─────────┘
                          ↓
                 ┌──────────────────┐
                 │ Research Queue   │
                 └────────┬─────────┘
                          ↓
                 ┌──────────────────┐
                 │ Web Enrichment   │
                 │ + Signals        │
                 └────────┬─────────┘
                          ↓
                 ┌──────────────────┐
                 │ Deterministic    │
                 │ Scoring          │
                 └────────┬─────────┘
                          ↓
                 ┌──────────────────┐
                 │ Prospect Queue   │
                 └────────┬─────────┘
                          ↓
                   score threshold?
                    /          \
                  no            yes
                  ↓              ↓
              remain queued   AI budget?
                               /     \
                             no       yes
                             ↓         ↓
                         wait       Gemini
                                      ↓
                              human-tone draft
                                      ↓
                               quality gates
                                      ↓
                               review queue
                                      ↓
                                HUMAN REVIEW
                                  /      \
                               edit       approve
                                ↓           ↓
                              review      executeSend()
                                              ↓
                                          follow-ups
                                              ↓
                                           outcome
```

---

# 47. Definition of done

The refactor is complete when the developer can:

1. Start the application.
2. Leave it running for several hours.
3. Have it continuously discover companies.
4. Have it continuously research them.
5. Have it verify domains rather than blindly guessing.
6. Have it rank prospects without AI.
7. Have only high-quality prospects enter the AI queue.
8. Have Gemini calls strictly rate-limited.
9. Have the system stop AI generation when quota is exhausted while continuing research.
10. Receive a small queue of high-quality, human-sounding emails.
11. Review each email quickly.
12. Edit it if necessary.
13. Explicitly approve sending.
14. Have sending pass through one guarded path.
15. Have correct From and Reply-To headers.
16. Have follow-ups created.
17. Record outcomes.
18. Restart the application without losing important work.
19. See what the system has been doing throughout the day.

The core philosophy is:

> **Research continuously. Score deterministically. Spend AI quota carefully. Write like a human. Never send without human approval.**

---

# 48. Implementation discipline

Work in phases.

### Phase 1

Audit and stabilize the current code.

### Phase 2

Implement the long-running worker/scheduler and recoverable work states.

### Phase 3

Fix domain verification and research reliability.

### Phase 4

Implement deterministic scoring and prospect prioritization.

### Phase 5

Implement AI quota management and selective drafting.

### Phase 6

Implement human-tone generation and deterministic quality gates.

### Phase 7

Consolidate review/send UX.

### Phase 8

Implement follow-ups/outcomes.

### Phase 9

Add focused tests and crash/recovery handling.

After every phase:

* format code
* build
* run tests
* verify migrations
* inspect for regressions

Do not implement the entire system as one giant rewrite.

At the end, provide:

1. files changed
2. migrations added
3. configuration added
4. worker behavior
5. AI quota behavior
6. review/send behavior
7. tests added
8. remaining limitations
9. anything intentionally deferred

Do not claim a feature is implemented unless it actually exists and has been tested.



# Critical Workflow Change — Autonomous Sending With Exception-Based Human Review

Prospector is intended to operate autonomously for several hours during the developer's workday.

Therefore:

## Human review must NOT be the default path.

The automation is only useful if strong, low-risk emails can be generated and sent without the developer manually approving every single message.

The correct model is:

```text
research
→ score
→ select prospect
→ generate email
→ validate
→ quality gate
→ classify
      ↓
 ┌───────────────┬──────────────────┬─────────────────┐
 │               │                  │
 AUTO_SEND       REVIEW_REQUIRED    BLOCKED
 │               │                  │
 ↓               ↓                  ↓
send          human review       do nothing
                  ↓
             approve/edit
                  ↓
                 send
```

---

# 1. Three send decisions

Every generated email must receive exactly one of:

```text
auto_send
review_required
blocked
```

Do not use a vague boolean such as:

```text
approved=true
```

The system needs to know WHY a message was automatically sent, flagged, or blocked.

---

# 2. AUTO_SEND

An email may be automatically sent only when ALL required safety and quality conditions pass.

At minimum:

### Company identity

* company identity is sufficiently verified
* domain is verified
* domain is not suspicious
* company has meaningful evidence

### Contact

* email address is valid
* contact is not suppressed
* recipient is appropriate for business outreach

### Evidence

* observation comes from real stored signals
* signal IDs are valid
* claims are supported
* no unsupported numbers
* no invented facts

### Prospect quality

* score exceeds configurable auto-send threshold
* evidence is sufficiently fresh
* opportunity is commercially relevant

### Email quality

* no placeholders
* no obvious AI/electronic language
* no generic fluff
* no excessive marketing language
* reasonable length
* natural subject
* one clear CTA
* no suspicious formatting
* no obvious hallucination

### Sending safety

* no recent contact to same domain
* no suppression
* daily cap not exceeded
* hourly cap not exceeded
* minimum interval satisfied

Only if ALL of these pass may the system automatically send.

---

# 3. REVIEW_REQUIRED

Flag the email instead of sending if any condition indicates that human judgment is useful.

Examples:

### Identity uncertainty

```text
domain confidence below threshold
company identity ambiguous
recipient/company relationship unclear
```

### Evidence uncertainty

```text
only weak evidence
stale evidence
conflicting evidence
important claim difficult to verify
```

### AI quality concern

```text
AI-tone detector triggered
unusual wording
too verbose
generic opening
unsupported claim
uncertain interpretation
```

### Recipient concern

```text
generic email address where a named contact is preferable
recipient role unclear
multiple possible contacts
```

### Commercial uncertainty

```text
score is near the minimum threshold
opportunity requires interpretation
```

Do not send these automatically.

Place them in:

```text
REVIEW REQUIRED
```

---

# 4. BLOCKED

Block without sending when:

* domain is unverified
* email is invalid
* company is suppressed
* contact is suppressed
* duplicate/recent contact exists
* evidence is insufficient
* required citation is missing
* citation does not exist
* unsupported claim exists
* placeholder remains
* sending limits are exceeded
* system detects a safety/quality violation that cannot reasonably be resolved automatically

Blocked items should remain visible in logs/history with a clear reason.

---

# 5. Human review should be exception-based

The review queue should contain only exceptions.

The developer should NOT have to review:

```text
100 normal emails
```

if all of them satisfy the configured auto-send policy.

Instead, the expected workflow is:

```text
50 companies researched
↓
15 strong prospects
↓
8 email candidates
↓
6 auto-send
↓
2 review_required
```

The developer reviews only the 2 exceptions.

This is a core requirement.

---

# 6. Make thresholds configurable

Do not hard-code the policy.

Provide configuration such as:

```env
AUTO_SEND_ENABLED=true

AUTO_SEND_MIN_SCORE=80
REVIEW_MIN_SCORE=65

AI_DRAFTS_PER_HOUR=5
AI_DRAFTS_PER_DAY=30

SEND_DAILY_CAP=10
SEND_MIN_INTERVAL_SECONDS=900

AUTO_SEND_REQUIRE_FRESH_EVIDENCE=true
AUTO_SEND_MAX_EVIDENCE_AGE_HOURS=72

AI_HUMANIZE_SECOND_PASS=false
```

Use sensible defaults, but keep them configurable.

---

# 7. Add an explicit policy engine

Do not bury auto-send decisions inside the HTTP handler.

Create a clear decision function/service.

Conceptually:

```go
decision := EvaluateSendPolicy(company, contact, draft, evidence, limits)
```

which returns something equivalent to:

```text
AUTO_SEND
REVIEW_REQUIRED
BLOCKED
```

plus structured reasons.

Example:

```json
{
  "decision": "review_required",
  "reasons": [
    "domain_confidence_below_auto_send_threshold",
    "ai_tone_warning"
  ]
}
```

This makes the behavior testable and explainable.

---

# 8. Record why every email was handled

For every generated draft, record:

```text
decision
decision_reason
decision_at
score_at_decision
evidence_age
ai_model
ai_generation_timestamp
```

For automatic sends, the history should make it obvious why the system believed it was safe.

Example:

```text
AUTO-SENT

Score: 87
Domain: verified
Evidence: 3 fresh signals
AI quality: passed
AI tone warnings: 0
Contact: verified
Send policy: passed
```

For a review:

```text
REVIEW REQUIRED

Score: 74
Reason:
- domain confidence borderline
- AI tone warning
```

---

# 9. Review UI

The review UI is now an exception queue, not the normal workflow.

Show:

```text
REVIEW REQUIRED

Company
Score
Reason for review
Evidence
Contact
Draft
Warnings
```

Actions:

```text
Send
Edit & Send
Save
Skip
Block
```

The developer should be able to process an exception quickly.

---

# 10. Automatic sending still uses the same guarded send path

There must still be exactly ONE sending implementation:

```text
executeSend()
```

Both:

```text
AUTO_SEND
```

and:

```text
human-approved REVIEW_REQUIRED
```

must eventually call the same function.

Do NOT create:

```text
autoSend()
manualSend()
```

with duplicated safety logic.

The policy decides whether sending is permitted.

`executeSend()` remains the final technical guard.

---

# 11. Automatic sending must remain conservative

The goal is not maximum sending volume.

The goal is:

> Automatically send emails that are so well-supported and low-risk that manual approval would add little value.

Therefore the default should be conservative.

If uncertain:

```text
REVIEW_REQUIRED
```

not:

```text
AUTO_SEND
```

The system should become more autonomous as confidence improves, not because we lower standards.

---

# 12. AI quality gate

The AI should generate the final email in one call where possible.

Do not spend a second Gemini call on every message simply to humanize it.

Use:

1. strong generation prompt
2. deterministic quality checks
3. optional second AI pass only when explicitly enabled

If the first-pass email triggers a warning:

```text
AI quality warning
→ REVIEW_REQUIRED
```

Do not automatically consume another AI call unless configured.

---

# 13. Human-tone rules

The email should sound like a normal developer writing to another business.

Avoid:

* "I hope this email finds you well"
* "I came across"
* "I was impressed"
* "exciting opportunity"
* "innovative solutions"
* "cutting-edge"
* "seamless"
* "robust"
* "leverage"
* "ecosystem"
* "empower"
* "unlock"
* excessive adjectives
* excessive punctuation
* marketing language
* fake enthusiasm
* long introductions
* generic compliments
* unnecessary corporate language

Prefer:

* concrete observations
* short sentences
* direct language
* normal contractions where appropriate
* modest claims
* one clear reason for contacting them
* one simple CTA

The goal is not to fool AI detectors.

The goal is to make the message genuinely sound like something a developer would naturally write.

---

# 14. Important distinction

Do NOT make "AI detected" the sole reason to block an email.

AI-tone detection is only one signal.

A message may be perfectly acceptable even if it contains a phrase that appears on a generic AI-word blacklist.

Use a combined quality evaluation:

```text
evidence quality
+
claim validity
+
naturalness
+
specificity
+
brevity
+
commercial relevance
```

The deterministic checks should catch obvious problems, not pretend to perfectly detect AI authorship.

---

# 15. Final operating model

The finished system should behave like:

```text
Morning
  ↓
Start Prospector
  ↓
Continuous discovery
  ↓
Continuous research
  ↓
Continuous scoring
  ↓
Best prospects enter AI queue
  ↓
Gemini generates only a small number of drafts
  ↓
Each draft is automatically quality-checked
  ↓
Most strong drafts:
      AUTO_SEND
  ↓
Suspicious/ambiguous drafts:
      REVIEW_REQUIRED
  ↓
Bad drafts:
      BLOCKED
  ↓
Research continues all day
  ↓
AI budget replenishes hourly / daily
  ↓
New prospects continue entering the pipeline
```

The developer should be able to leave Prospector running for several hours and return to find:

```text
87 companies researched
23 high-value prospects
5 emails generated
3 automatically sent
2 flagged for review
4 follow-ups due
1 reply received
```

That is the intended behavior.

The system should be **autonomous by default, conservative by design, and human-assisted only when confidence is insufficient**.
