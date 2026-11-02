# Opportunity triage and autonomous outreach

Status: **implemented and verified locally, against real PostgreSQL with a fake mail provider and
fake AI providers. Not deployed. No real email was sent and no live AI call was made.** The
tracked schema is now **31** (`0031_triage_and_autonomy.sql`, additive). Every new behaviour
ships switched **off**, and the operator turns each one on from the dashboard.

## 1. Investigation: why unrelated opportunities accumulated

**How a posting flows today.** Discovery reads each source. Ingestion normalises the posting and
deduplicates it per source, by external ID or canonical URL. The deterministic matcher then
assesses it once per (posting revision, profile revision, matcher version).

**What the matcher does:**
- The title gate decides the role family.
- A title that names a non-engineering function ("Accountant", "Marketing Manager"), or no
  engineering role at all, is `unrelated`.
- Strong and possible matches need an engineering title, or since `fit-v2` requirements that
  establish the family.

**Why there are so many unrelated postings:**
1. **Several sources publish every department.**
   - Remote OK's API, Himalayas and the employer career boards are mixed.
   - The Kenyan boards are keyword searches ("python", "node", "software engineer") that return
     data, finance and support roles mentioning those words.
   - Nothing was wrong with these sources. Their breadth is the point, and every unrelated
     posting was assessed correctly.
2. **Unrelated postings were kept, visible, forever.** Nothing ever took one out of the list
   except an operator's dismissal or an expiry.
3. **Some genuinely relevant work was classified unrelated.** Freelance and contract postings are
   often titled by the task ("Golang", "Build our Django REST API"). With no role noun in the
   title, the matcher filed them as unrelated however engineering-heavy the requirements were.

**AI cost: none.** No AI is used anywhere in discovery, matching, research, contact discovery or
triage. AI is used only to compose and review emails. The 1,037 unrelated postings have consumed
**zero** AI requests.

**Repeated assessment: no.** A posting is reassessed only when its revision, the active profile
or the matcher version changes.

**Profile use: correct.** Families come from the profile's desired roles, technologies from the
profile, and capability support from requirement excerpts. The profile's positioning (Full-Stack,
backend-focused) is untouched.

**Source quality: not visible before.** It is now on the Sources page (section 2).

## 2. Opportunity triage

### Matcher recovery (`fit-v3`)

A title with no engineering role noun and no non-engineering function, whose own requirements
support two or more approved capabilities, is now **possible** instead of unrelated
(`engineering_work_without_role_title`). It is never strong. This is how a relevant posting
"classified unrelated" returns to the pipeline: through the matcher, so no stored assessment is
ever rewritten.

A few commercial titles were also added to the non-engineering list: go-to-market, account
executive, account manager, partnerships and community manager.

### The triage worker (`internal/triage`, loop `triage`, every 10 minutes)

**What it reads.** It considers only live postings the current matcher classified unrelated, and
uses what is already stored:
- the title, description and description status;
- the source;
- the assessment's supported capabilities;
- the approved technologies the description mentions.

It is deterministic and makes **no AI calls**.

**Confidence policy.** Each threshold is tied to an existing rule rather than chosen by eye.

| Outcome | Confidence | Rule |
| --- | --- | --- |
| `manual_review` | low | The requirements support **≥ 2** approved capabilities, which is the matcher's own bar for establishing a family, even though the title is unrelated. The evidence is contradictory, so a person decides. |
| `suppressed` | high | The title names a non-engineering function, and the description mentions **≤ 1** approved technology (one passing mention is normal in business roles). |
| `suppressed` | high | The title names no engineering role at all, a **complete** description of **≥ 200** characters mentions no approved technology, and no capability is supported. |
| `confirmed_unrelated` | medium | Everything else. Some technical signal or an incomplete description means it stays **visible** as unrelated. |
| `kept` | — | A person restored or kept it. Triage never overrules that for the same revision. |
| `failed` | — | The triage step itself failed. It is retried up to 3 times for the same inputs, then left for a person. |

**Suppression is not deletion.**
- A triage suppression is an ordinary, reversible suppression record: reason `not_relevant`,
  basis `triage_rules`, decided by `triage:triage-v1`, with the reasons stored as evidence.
- Suppressed postings leave the active list and appear in **Opportunities → Dismissed**, each
  with its badge.
- **Restore** brings one back.
- Source, discovery data, revisions and assessments are untouched.
- The four states stay distinct:
  - **unrelated** is the matcher's classification;
  - **suppressed** is a live suppression with a reason;
  - **dismissed** is an operator's suppression, reason "Not pursuing";
  - nothing is ever **deleted**.

**Reassessment.** A triage row is pinned to (posting revision, matcher version, profile revision,
triage version). When any of these changes, the posting is triaged again; otherwise it is never
processed twice. A posting a person restores is not suppressed again for that revision, even
under new rules. New revisions are new evidence and are triaged normally.

**Calibration.** Production data is not reachable from this environment, so the rules could not
be calibrated against the real 1,719 postings. Instead:
- **Hiding is off by default.** Triage starts in record-only mode: it records "would hide" and
  hides nothing.
- **A read-only report shows what it would do:**

  ```
  prospector-migrate -command triage-report [-out triage.json]
  ```

  It prints every live unrelated posting's would-be outcome, broken down by source, with
  examples, and changes nothing.
- **The dashboard shows "Would be hidden"**, which links to `/opportunities?triage=pending` to
  inspect them.
- **When the examples look right**, click **Let triage hide unrelated postings**. The pending
  postings are hidden on the next tick.

**Source quality.** The Sources page now shows, per source:
- live listings;
- relevant (strong or possible) listings;
- unrelated listings, with an unrelated percentage;
- listings hidden by triage.

No source is turned off for being noisy. Triage applies the same rules to every source, so a
noisy source loses its noise without losing coverage.

**Operational visibility.** The dashboard's **Automation** card shows triage counts:
- waiting;
- hidden as unrelated;
- would be hidden;
- kept visible;
- need your decision;
- returned to the pipeline;
- failed.

It also has **Pause/Resume triage** and the hiding switch. A "needs a decision" alert links to
`/opportunities?triage=manual_review`, where each row has **Keep** or **Dismiss**. The triage
loop also appears among the research workers' heartbeats.

## 3. The sending policy (`auto-v2`)

Every reviewed draft considered for automatic sending gets one of three outcomes, recorded with
**every** reason in `automation_decision` and shown on the draft's page.

### AUTO_SEND (`eligible_for_auto`)

Every condition holds.

**Opportunity:**
- the intent is a specific job application to a **strong match** with a clear family and a
  complete description;
- no unresolved mandatory requirement;
- at least 2 supported capabilities;
- the opportunity is open and not dismissed or suppressed;
- the pinned revision is current;
- nothing is stale.

**Recipient:**
- the address is a mailbox for people and not marked bad;
- it is not suppressed by address or domain;
- the function suits the intent: recruiting or engineering for an application, or an application
  address the employer published;
- guessed addresses are never promoted to contacts.

**Email:**
- the composition is reviewed;
- no deterministic finding above info;
- the AI review is complete, passed and has no warnings;
- the revision is generated, not hand-edited;
- the content hash is pinned.

**Readiness and limits:**
- the send path is ready (SMTP, sender, schema);
- the daily and hourly automatic caps are not reached;
- no unresolved send attempt exists anywhere;
- no retry backoff is pending;
- no open conversation with the company's domain;
- the AI budget is available.

**Operator:**
- the deployment permits automation (`EMAIL_PIPELINE_MODE=v2_auto` plus `AUTO_SEND_INTENTS`);
- automatic sending is **switched on**;
- it is **not paused**;
- the revision was written **after** automatic sending was switched on.

**Business outreach** (`service_outreach`, only if allowlisted) additionally requires:
- company research evidence of a reason to write, not an operator override;
- an engineering, leadership or commercial contact;
- a sentence that lets the recipient decline further contact. The writer's prompt now asks for
  one.

### REVIEW_REQUIRED (`review_required`)

Anything uncertain or temporarily disallowed. The draft stays in the review queue. Examples:
- a possible match;
- a generic or unknown recipient;
- an AI review warning;
- a deterministic warning (a volunteered gap, for instance);
- stale inputs;
- automatic sending off or paused;
- a draft written before automation was enabled;
- a cap reached;
- retry backoff;
- an open conversation with the company;
- a speculative enquiry, which is never automated.

### BLOCKED (`blocked`)

It must not be sent. Examples:
- a **critical deterministic finding**: an invented technology, a false attachment, an
  unconfigured link, the wrong title, an invented name, an unsupported number;
- a suppressed address or domain;
- a recipient marked bad;
- an address that is not a mailbox for people;
- no recipient;
- an opportunity that is closed or dismissed.

The send preflight refuses the same conditions, so **no setting and no manual approval can send a
blocked email**.

### Manual approval and the audit trail

Manual approval still overrides **AI editorial objections and warnings only**. It never
overrides critical factual checks, suppression, recipient validity, duplicate protection or
sending limits; the preflight enforces those for every send.

The audit record keeps every case distinguishable:

| What happened | How it is recorded |
| --- | --- |
| AI-approved for automatic sending | `send_approval.approved_by = automation:auto-v2`, linked to its `automation_decision` |
| Manually approved | `approved_by = <operator>`, basis `reviewed` or `manual_override` |
| Sent automatically | `send_attempt.initiator = automation:auto-v2` |
| Sent after manual approval | `initiator = operator` |
| Blocked, or held for review | `automation_decision.outcome`, with every blocker |

The draft page labels each case ("Approved automatically by the sending policy", "sent
automatically" or "sent by operator") and shows the policy's latest decision.

## 4. Autonomous workflow and dispatch

```
discovery → normalisation → dedup → matching → triage → research → contact discovery
  → outreach worker (plan + compose + AI review) → sending policy → automatic send OR review queue
  → conversation tracking
```

**The outreach worker** (`internal/outreach`, loop `outreach`, every 30 minutes) is new.
- **Where it runs.** It acts only when **Automatic drafting** is switched on.
- **What it chooses.** It picks a strong match where:
  - the employer's domain is confirmed;
  - no live plan exists for the company;
  - no conversation was ever opened with the company.
- **What it does.**
  1. Asks the plan rules. A plan that needs review is left for a person.
  2. Takes the best **preferred, acceptable or employer-published** recipient. Generic and
     unverified recipients are never chosen.
  3. Records a plan with `selection_origin = suggested`, distinct from a person's `manual` plans.
  4. Composes and AI-reviews one draft.
- **Its limits.**
  - At most one draft per tick, and `OUTREACH_DAILY_DRAFTS` per day (default 3).
  - Each draft draws from the existing AI request budget (`AI_REQUESTS_PER_HOUR` and
    `AI_REQUESTS_PER_DAY`).
  - It never sends.
  - A posting left for a person is not reconsidered for 24 hours unless its revision changes.
  - A restart mid-draft is recovered: the worker reuses its own plan and retries.
- **Business prospecting is never started by the worker**, because it needs your judgement.
  Service-outreach plans you create can be sent automatically only if you allowlist
  `service_outreach` and the evidence, contact and opt-out gates hold.

**The automation runner** is the existing `auto-v1` loop, extended.
- It polls every 15 minutes, only when the deployment permits automation.
- It reads the switches **every tick**, so pause takes effect at once.
- **What it considers.** Only drafts reviewed after the switch-on time are candidates. A draft
  decided BLOCKED is not reconsidered until it changes, and one decided REVIEW_REQUIRED waits an
  hour before it is decided again.
- **How it sends:**
  1. It claims the draft with a durable lease, so two workers cannot take the same one.
  2. It evaluates the policy.
  3. It approves the exact content hash as `automation:auto-v2`.
  4. It re-evaluates the policy against freshly read state.
  5. It sends through the **one canonical send path**. That path runs the full preflight, then
     **reserves the idempotency key before contacting the provider**, so a restart, timeout,
     retry or second worker cannot send twice.
- At most one send per tick.

**Uncertain outcomes.** A provider result whose fate is unknown is recorded as `uncertain`. All
automation halts until a person reconciles it, and the dashboard says so. SMTP has no idempotency
keys; the Message-ID is derived from the idempotency key, and the reservation is the guarantee.

**Limits, all enforced together:**

| Limit | Setting |
| --- | --- |
| Automatic sends per day | `AUTO_SEND_DAILY_CAP` (default 1, refused above 20) |
| Automatic sends per hour | `AUTO_SEND_HOURLY_CAP` (default 1) |
| All sends per day | `DAILY_SEND_LIMIT` (default 20) |
| Minimum gap between sends | `MIN_SEND_INTERVAL_SECONDS` (default 90) |
| Live first contacts per domain | 1 |
| First-contact cooldown per domain | 30 days |
| Attempts per message | 3, with automatic retry backoff of 15, 30, then 60 minutes |
| Suppression lists | address and domain |
| Parallel threads | none: the policy refuses an open conversation with the company |

**Mode and pause.**
- **The three modes** follow the dashboard switch: Manual (the default), Automatic, and Paused.
- **Pause** is a red button that stops automatic sending immediately. Discovery, research,
  triage and drafting keep running.
- **Separate pauses:** triage and drafting have their own switches.
- **Historical drafts:** switching automatic sending on never sends drafts that were already
  waiting.

## 5. Data preservation

- Migration 0031 only widens three CHECK lists and adds two tables (`opportunity_triage` and
  `operator_setting`). No row is changed.
- **Not deleted:** no opportunity is deleted, and triage hides through reversible suppression.
- **Not rewritten:** assessments, drafts, manual edits, reviews, approvals, send attempts and
  conversations.
- Triage reads assessments and never writes them.
- **Not regenerated or auto-sent:**
  - historical drafts are never auto-sent and never regenerated;
  - the outreach worker drafts only for companies with no live plan or conversation.
- **Existing paths keep working:** the manual approval, manual override and manual send paths
  are unchanged.

## 6. Tests

`go vet` and `gofmt` are clean. `go test ./...` passes in **all 25 packages**, with no failures,
against isolated PostgreSQL databases.

**New tests:**
- **Triage policy (`internal/triage`):**
  - clearly unrelated roles are suppressed;
  - business roles with one incidental technology are suppressed;
  - no-role-noun titles with complete or incomplete descriptions;
  - technical signal keeps a posting visible;
  - contradictory evidence goes to review;
  - task-titled relevant work is recovered by the matcher before triage;
  - a person's restore is never overruled;
  - triage never touches a non-unrelated assessment.
- **Triage on a real store (`internal/assess`):**
  - record-only mode hides nothing;
  - nothing is re-processed;
  - pause works;
  - activation hides only the high-confidence posting, with a `not_relevant`/`triage_rules`
    record;
  - a person's restore survives a rules change;
  - the assessment is never rewritten.
- **Policy outcomes (`internal/automation`):**
  - every AUTO_SEND, REVIEW_REQUIRED and BLOCKED case;
  - operator switches, historical drafts, hourly cap and retry backoff, including its schedule;
  - business-outreach gates;
  - allowlist parsing.
- **Dispatch, with a fake provider and real store:**
  - global pause, then resume;
  - historical drafts are never sent;
  - a provider failure is followed by backoff with no second transmission;
  - a critical unsupported-technology claim is BLOCKED and recorded;
  - the existing tests still pass: concurrent workers claim once, suppression or a bad recipient
    between decision and send stops it, hand edits and manual approvals stay manual,
    unresolved attempts halt.
- **End to end (`TestAutonomousWorkflowEndToEnd`):**
  - a strong-match posting at a verified employer with an engineering contact goes through the
    outreach worker (fake writer and reviewer), then the plan and reviewed draft, then AUTO_SEND,
    then **exactly one** send through the fake transport;
  - nothing happens while switched off;
  - no second draft for the same company;
  - the sent draft is never a candidate again.
- **Restart recovery:** an interrupted draft is retried with the same plan.
- **Web:**
  - the draft page shows BLOCKED and AUTO_SEND decisions with reasons;
  - automatic and manual approvals and sends are labelled distinctly;
  - the dashboard shows the mode and the pause control.

**Local run:**
- Migration 0031 was applied with the production procedure (`backup-verify`, then `apply
  -proof`), and the schema check passes.
- `triage-report` ran read-only against the local data.
- In a browser (Playwright), I paused, resumed, and toggled drafting and triage on the dashboard.
  The settings persisted with `updated_by = operator`, and the dashboard alert appeared while
  paused.

**Limitations:**
- No live email provider and no live Gemini call were used.
- The local database has 21 postings, so the triage thresholds are **not calibrated against
  production**. Use `triage-report` before turning hiding on.
- The production share of each outcome is unknown until that report is run.

## 7. Deployment

```bash
cd /opt/apps/prospector
git pull --ff-only
bash deploy/deploy.sh                                   # backs up, verifies, applies 0031
docker compose exec app prospector-migrate -command triage-report   # or run it however you run migrate commands
```

**Everything starts switched off.** Turn it on in this order:
1. Read the triage report. If the "would be hidden" examples look right, click **Let triage hide
   unrelated postings**.
2. Click **Switch on drafting**. Read the first few drafts it writes (they wait in Compositions).
3. To let it send, set these in `.env.production` and restart, then click **Switch on automatic
   sending**:
   ```
   EMAIL_PIPELINE_MODE=v2_auto
   AUTO_SEND_INTENTS=specific_job_application      # add ,service_outreach only if you want proposals automated
   AUTO_SEND_DAILY_CAP=2                           # optional (default 1, maximum 20)
   AUTO_SEND_HOURLY_CAP=1                          # optional
   ```
   SMTP and the sender address must already be configured; the dashboard says if they are not.

**Other optional settings:**

```
OUTREACH_DAILY_DRAFTS=3
OUTREACH_INTERVAL_MINUTES=30
TRIAGE_INTERVAL_MINUTES=10
```

## 8. Deferred or not done

- **AI-assisted triage** is not used: unrelated postings cost no AI today, the contradictory
  cases are few and go to a person, and adding AI to 1,000+ rows would spend budget for little
  gain. If the triage report shows a large review bucket, AI classification of that bucket is the
  natural next step.
- **The outreach worker does not start business-prospecting emails or speculative enquiries.**
  They need your judgement on whether a reason exists.
- **Cross-source duplicates** (the same job on two boards) are still two rows.
- **Live verification is still to do:** a real Gemini compose/review and a real SMTP send.

## Addendum: review of discovered contact addresses (migration 0032)

Contact discovery leaves addresses it cannot confirm (inferred from a company's format, published
by a third party, or found only in weak evidence) in `review_required`. Previously nothing could
accept or reject them. Now:

- **Free check, automatic** (`internal/addresscheck`, loop `address-check`, every 15 minutes,
  `ADDRESS_CHECK_INTERVAL_MINUTES`): syntax, no-reply/machine mailbox, company domain (own,
  sibling domain the company already publishes on, personal mail provider, one-or-two-letter
  look-alike), DNS mail servers (MX, address-only fallback, none, null MX), the person's name in the
  address, and provenance. Verdict: looks deliverable / doubtful / will not receive. A resolver that
  cannot find a known domain is treated as "lookup failed", never as "no mail servers".
- **AI opinion, optional**: plausible / doubtful / implausible with reasons, from the review provider.
  Background opinions only when the dashboard switch is on (`contacts.ai_check`, off by default), at
  most `ADDRESS_AI_DAILY` (default 20) a day and inside `AI_REQUESTS_PER_HOUR/DAY`; an "Ask AI"
  button per address. The AI cannot call an address with a hard fault plausible. Evidence text is
  passed as untrusted.
- **Only a person decides**: Accept (becomes a contact, `accepted_by` recorded) or Reject (kept with
  its evidence; rediscovery does not reopen it). Checks are append-only history.
- **Automatic sending never goes to an accepted inferred address** (`recipient_inferred_address`,
  review required). A person can send to it manually.
- Nothing is sent to an address to test it (no SMTP probing): it is unreliable and harms the
  sending domain's reputation. No check proves a mailbox exists.
