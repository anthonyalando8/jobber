# Activation readiness

Status: **evidence gathered, nothing turned on.** `EMAIL_PIPELINE_MODE=v2_review` and
`AUTO_SEND_INTENTS=` are unchanged. No profile was activated, no draft was generated, no prospect
was contacted.

## Conclusions first

| Question | Answer |
| --- | --- |
| Is the system ready for normal manual use? | **Yes, mechanically** — but it produces nothing until a profile is activated. |
| Is `specific_job_application` ready for automation? | **`NOT_READY`** — the required real-draft sample could not be run, and there are currently zero eligible candidates anyway. |
| What is the real bottleneck? | **Contact data, and upstream of it, discovery.** Not policy, not matching, not composition. |
| SMTP smoke test | **Done. Accepted by Gmail over TLS 1.3.** One confirmation still owed: that it arrived in your inbox. |

The single most useful finding is in §3: of the six strong-match companies, **four were never
researched because their stored domain is a guess that is almost certainly wrong**, and the job
adapters discard the employer and application URLs the boards actually publish. Fixing that is
worth more than any change to matching or automation policy.

---

## 1. Candidate profile state

Presented exactly as stored, via `prospector-migrate -command profile` (read-only; it cannot
modify or activate anything). Nothing below was inferred or added.

| | |
| --- | --- |
| Revision id | **3** |
| Revision number | 3 |
| Content hash | `7138cbad64ffbe1ffd312262d0306539666685fb82273ce46059efc4d5a858da` |
| Approval status | **`draft`** |
| Active | **false** |
| Created | 2026-09-12T14:15:43+03:00 |

**Identity:** Full-Stack Software Developer · primary strength Backend Engineering · secondary
position Full-Stack Development.

**Desired roles (3):** `role-backend` Backend Engineering · `role-fullstack` Full-Stack
Development · `role-software` Software Engineering, with aliases.

**Technologies (28):** Go, PostgreSQL, SQL, Redis, TimescaleDB, pgvector, Python, FastAPI,
JavaScript, TypeScript, React, Next.js, HTML/CSS, gRPC, WebSockets, Webhooks, HMAC, JWT, NATS,
Kafka, Docker, nginx, Traefik, Kubernetes, AWS, AWS KMS, Polygon, WebCodecs.

**Capabilities (23):** backend API development, service and microservice architecture, relational
data modelling, database migrations, transactional correctness, idempotency, concurrent and
background processing, event-driven architecture, third-party integrations, API integration
architecture, data ingestion, rule engines, financial transaction systems, double-entry
accounting, payment integrations, authentication and identity systems, digital identity and
verifiable credentials, reliability and reconciliation workflows, React application development,
Next.js application development, frontend/backend integration, full-stack application
development, system and API architecture.

**Projects (5):** Prospector (22 responsibilities, 5 outcomes) · LearnerOS (17, 0) · PXYZ
platform (14, 0) · Seabytes (8, 0) · Zentora (4, 0).

**Domains (10):** fintech, payments, wallets and accounting, e-commerce, learning and education
technology, digital identity and verifiable credentials, developer and platform tooling,
automation, AI-assisted media tooling, job and company research and outreach automation.

**Specializations (11):** backend engineering, API design, PostgreSQL and data modelling,
transactional systems, distributed and event-driven backend systems, external integrations,
auth architecture, fintech and payment systems, wallet and accounting systems, full-stack
application development, React and Next.js frontend development.

**Engagement:** employment types full-time, part-time, contract, freelance, permanent, temporary ·
service work **true** · remote **true** · locations **Kenya** · availability *not stated* ·
pricing *not stated*.

**Resources (1):** `res-cv` — CV, available on request, no public link.

**Signature:** Anthony Alando · "Full-Stack Software Developer | Backend Engineering" · Kenya ·
no phone · resources `res-cv`.

### Difference from the last approved revision (id 1, hash `2df49f62…`)

Revision 1 is the minimal seed. Everything substantive is an addition:

- **Desired roles** — two changed: aliases added (`Backend Software Engineer`; the three
  `…Engineer` full-stack spellings). No role added or removed.
- **Technologies** — all 28 added.
- **Capabilities** — all 23 added.
- **Projects** — all 5 added.
- **Domains** — all 10 added. **Specializations** — all 11 added.
- **Resources** — CV (available on request) added.
- **Engagement** — from *(none stated)* to the preferences above.
- **Signature** — from empty to name, descriptor, location and `res-cv`.

Nothing was removed and no identity or positioning text changed apart from the secondary
position being filled in.

### Two things to know before activating

1. **Revision 3 is a `draft`, not approved.** `ActivateProfile` approves and activates in one
   transaction, pinned to the content hash above, so activating it *is* approving it. There is no
   separate approve step to do first.
2. **Activating invalidates existing assessments.** That is by design (`TestActivatingNewProfile
   RevisionMakesMatchesStale`); §2 below is what to run afterwards.

I have not modified, approved or activated anything.

---

## 2. Authoritative matching after activation

**Not run — activation has not happened.** The live pipeline still correctly answers
`profile_required` for every company.

When you activate revision 3, matching becomes authoritative and should be re-run over the
current opportunities. The rehearsal (in-memory, nothing stored) predicts:

| | Count |
| --- | --- |
| `strong_match` | 6 |
| `possible_match` | 94 |
| `unrelated` | 173 |
| Total | 273 |

Most common blockers: `unresolved_mandatory_requirement` (273 occurrences),
`unrelated_application_role` (173), `family_not_clear` (55),
`insufficient_supported_capabilities` (39), `insufficient_requirements` (24),
`unresolved_mandatory_condition` (23), `known_preference_conflict` (1).

**These are predictions, not stored truth.** Rehearsal rows are computed in memory and written
nowhere. After activation the numbers must be re-read from stored assessments and compared
against this table; any difference is worth investigating, since the rehearsal uses the same
`opportunity.Assess` the real path does and should agree exactly.

---

## 3. The six strong-match companies

Investigated from stored data only. Nothing was searched, guessed or invented; no name was
derived from a mailbox local part; no generic, telemetry or no-reply address was treated as a
recruiting contact.

| # | Company | Opportunity / title | Domain (status) | Stored contacts | Published apply URL | Published apply email | Meets policy |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | Kruger NearShore LLC – Rekluti | 5 · DESARROLLADOR FULL STACK | `rekluti.com` (manual) | **none** | none | none | **No** |
| 2 | Cosuno | 18 · Senior Full Stack Developer (TypeScript) | `cosuno.com` (verified) | `info@cosuno.de` (generic email); a phone contact | none | none | **No** |
| 3 | Sticker Mule | 33 · Software engineer | `stickermule.com` (verified) | `help@stickermule.com` (generic), `u003emgmt@stickermule.com` (marked bad) | none | none | **No** |
| 4 | Delinea | 76 · Staff Software Development Engineer SDM | **`delinea.co.ke`** (candidate, never researched) | **none** | none | none | **No** |
| 5 | Wakacje.pl | 126 · Software Engineer GO | **`wakacje.co.ke`** (candidate, never researched) | **none** | none | none | **No** |
| 6 | Wonderdog | 231 · Full-Stack Product Engineer – Agentic First | **`wonderdog.co.ke`** (candidate, never researched) | **none** | none | none | **No** |

Provenance: all six postings came from RemoteOK or WeWorkRemotely, and the posting URL is stored
for each. Confidence in the contact data is low for every one of them, for two distinct reasons.

### Finding A — the domain guesser assumes a Kenyan company

`verify.CandidateDomains` puts `.co.ke` **first**, which is right for the Kenyan job boards it was
written for and wrong for international remote boards. Companies 4, 5 and 6 carry `delinea.co.ke`,
`wakacje.co.ke` and `wonderdog.co.ke` — Delinea is US, Wakacje.pl is Polish, and none of those
domains is plausibly the employer's. All three are `domain_status = 'candidate'` and
`last_researched_at` is **never**: research never ran, so no contact could ever have been found.

The scale:

| | |
| --- | --- |
| Companies from international remote boards (remoteok, weworkremotely, remotive, himalayas) | **212** |
| …of which carry a guessed `.co.ke` domain | **180** |

So roughly 85% of internationally-sourced companies are pointed at a domain that is very likely
wrong. This is the largest single cause of missing contacts.

### Finding B — the adapters discard the URLs the boards publish

The stored source metadata for all six opportunities contains exactly one key,
`extraction: source_adapter_normalized_text_v1`. No employer URL, no apply URL, no apply email.

RemoteOK's API publishes `apply_url` and company URL fields; `remoteOKJob` in
`internal/seed/remoteok.go` declares only date, id, company, position, tags, description, url and
location. The employer's own URL and the application route are read from the feed and thrown
away — which is a pity, because a published application address would satisfy the recipient
policy directly, and the employer URL would give research a correct domain to work from.

### Finding C — two contact-data defects worth noting

- `u003emgmt@stickermule.com` — a JSON/HTML escape artifact (`>` is `>`) leaked into a stored
  address. It is already marked bad, so nothing can send to it, but the extractor is not
  unescaping entities.
- `+254123456789` — shown against Cosuno in the table above. **Correction:** this is stored with
  `kind='phone'`, not `kind='email'`; it appeared in that column only because the listing did not
  filter by kind. No phone number ever entered the email path — `kind='email'` rows with no `@`
  number zero across the whole database. There is no defect here.

### What this means

**Legitimate contacts cannot be found for any of the six from existing stored data.** For two
(Cosuno, Sticker Mule) the company was researched properly and only a generic mailbox exists —
that is a genuine "this company publishes no named engineering contact" result, and the correct
answer is a manual decision, not a policy change. For the other four, research either never ran
or ran against the wrong domain, so the question has not actually been asked yet.

The ranked next actions, none of which I have taken:

1. **Order domain candidates by source.** International sources should try `.com` and the
   employer's published URL before any country suffix. This is a small change with a large effect:
   180 companies.
2. **Retain `apply_url` and the employer URL at ingestion.** A published application route
   satisfies the recipient policy as it stands, with no loosening.
3. **Re-research the affected companies** once their domains are corrected.
4. Unescape entities in extracted contacts, and reject non-address values at `kind='email'` as
   defence in depth.

All four are discovery/ingestion changes. I did not make them: they change crawl behaviour and
imply re-crawling 180 companies, which is a decision rather than a cleanup.

---

## 4. Real-draft validation sample

**Not performed.** It requires real strong matches *with acceptable recipients* (§4 of the brief),
and there are currently **zero**. It also requires an active profile, which is yours to decide.

So the Phase 7 measured real-draft requirement remains open, and nothing about composer or
reviewer behaviour on real strong matches has been measured. What exists instead: the Phase 4
evaluation against adversarial fixtures, and the synthetic end-to-end tests. Neither is a
substitute.

The sample becomes runnable once §3's actions produce at least three strong-match companies with
an identified recruiting or engineering contact, or a published application address.

---

## 5. Automation readiness — `NOT_READY`

Not one of the §5 conditions can currently be evaluated, because no real draft exists to evaluate
them against:

| Condition | State |
| --- | --- |
| No wrong-role errors | **Unmeasured** |
| No unsupported candidate claims | **Unmeasured** |
| No unsupported company claims | **Unmeasured** |
| No payment leakage | **Unmeasured** |
| Acceptable recipient quality | **Failed** — 0 of 6 strong matches has an acceptable recipient |
| Complete semantic reviews | **Unmeasured** |
| Low/no manual-edit requirement | **Unmeasured** |
| All inputs current | Yes, but vacuously — there are no candidates |

Speculative enquiries, service outreach and follow-ups remain manual regardless, by policy rather
than configuration.

Enabling automation today would be harmless and pointless in equal measure: the policy would
refuse every candidate. The work that changes this is §3, not anything in the automation layer.

---

## 6. Manual operation readiness — `READY, pending profile activation`

The bar is lower because a person reads and approves every send, and the workflow holds up:

| Step | State |
| --- | --- |
| Manual job discovery and review | **Ready.** 273 opportunities stored, 262 with complete descriptions. `/opportunities` is now properly paginated, so all 273 are reachable. |
| Fit inspection | **Ready.** Every assessment states its reasons and blockers. |
| Manual plan selection | **Ready** — blocked only by `profile_required`, which activation clears. |
| Real email generation | **Ready** — provider configured, budget accounting in place, composition requested explicitly and never automatically. |
| Human review | **Ready.** Both validation layers, stale state, findings and approval state are on one page. |
| Explicit manual send | **Ready** once `EMAIL_PIPELINE_MODE=v2_manual_send` and the smoke test is confirmed received. |

Two caveats that are not blockers but are worth knowing:

- Manual sending is only as good as the recipient data (§3). The system will correctly refuse to
  send to a generic mailbox unless you deliberately choose it.
- An unresolved `reserved`/`sending`/`uncertain` attempt blocks that message permanently until
  reconciled. See `RECONCILIATION_DESIGN.md` for the procedure.

---

## 7. SMTP smoke test — **passed, one confirmation outstanding**

Two steps, both run today. Neither touched a prospect: the test composes its own fixed body and is
not connected to the draft, plan or contact tables at all.

**Step 1 — configuration verification** (`prospector-smtp`), which sends nothing and contacts
nobody. It stops after AUTH and never issues `MAIL FROM`:

| | |
| --- | --- |
| Reached stage | `quit` |
| STARTTLS | **true** |
| TLS | **TLS 1.3 / TLS_AES_128_GCM_SHA256** |
| Authenticated | **true** |
| From | `anthonyalando8@gmail.com` |
| Reply-To | `anthonyalando8@gmail.com` |
| Message-ID domain | `gmail.com` |

**Step 2 — one real message** to `anthonyalando8@gmail.com`, sent with your explicit approval:

| | |
| --- | --- |
| Outcome | **accepted** |
| Stage reached | `quit` |
| Provider response | accepted by smtp.gmail.com |
| Message-ID | `<smoketest-1789295970781831700@gmail.com>` |
| Body | plain text, no HTML part, no attachment |

**Outstanding:** confirm it arrived in your inbox, and check the received copy's `From`,
`Reply-To` and `Message-ID` headers. That is the one part of §7 only you can verify. Until you do,
treat the smoke test as passed at the provider and unconfirmed at the inbox.

---

## 8. Unresolved operational blockers

| # | Blocker | Blocks | Owner |
| --- | --- | --- | --- |
| 1 | No active candidate profile | Everything authoritative | Your decision (§1) |
| 2 | Zero strong matches with an acceptable recipient | Real-draft sample, automation | §3 actions |
| 3 | 180 companies on a likely-wrong `.co.ke` domain | Contact discovery at scale | Discovery change |
| 4 | Adapters discard `apply_url` / employer URL | Published application routes | Ingestion change |
| 5 | Real-draft quality unmeasured | Automation decision | Follows 1 and 2 |
| 6 | Inbox receipt of the smoke test unconfirmed | First real manual send | You |
| 7 | No reconciliation tooling for stuck attempts | Recovery, and automation while one exists | Separate proposal |

Reconciliation remains deliberately unimplemented. `RECONCILIATION_DESIGN.md` specifies it;
implementing it changes Phase 5 invariants and should be proposed on its own.

---

## 9. Exact configuration to enable manual sending

Prerequisites: activate profile revision 3 on `/profile`, and confirm the smoke test arrived.

```diff
- EMAIL_PIPELINE_MODE=v2_review
+ EMAIL_PIPELINE_MODE=v2_manual_send
```

Nothing else changes. `AUTO_SEND_INTENTS` stays empty. Existing limits (20/day, 90s interval,
30-day per-domain cooldown) continue to apply, every send still requires you to approve that exact
content, and the send button only appears when preflight passes.

## 10. Exact configuration to enable limited automation

**Do not do this yet — the conclusion in §5 is `NOT_READY`.** When the preconditions are met:

```diff
- EMAIL_PIPELINE_MODE=v2_review
+ EMAIL_PIPELINE_MODE=v2_auto
- AUTO_SEND_INTENTS=
+ AUTO_SEND_INTENTS=specific_job_application
  AUTO_SEND_DAILY_CAP=1
```

Preconditions, all of which must hold first:

1. An approved profile revision is active.
2. At least three real strong matches have an acceptable recipient.
3. A 3–5 draft sample has been composed, reviewed and inspected, with no wrong-role error, no
   unsupported candidate or company claim, no payment leakage, complete semantic reviews and no
   manual editing required.
4. Manual sending has been used for real long enough to trust the output.
5. No unresolved send attempt exists.

Even then, each message is decided individually by the automation policy and then by the send
preflight, and the cap is one per day.

---

## 11. What changed in the repository today

Read-only tooling and one deliberate data change, all reversible:

- `prospector-migrate -command profile` — presents a stored profile with a diff against the last
  approved revision. Read-only.
- `prospector-rehearse` — now also reports the contact situation at strong-match companies.
  Read-only.
- `prospector-smtp` — `-verify` (sends nothing) and `-send -to ADDRESS` (requires an explicit
  destination, refuses non-human mailboxes, composes its own body).
- `send.SMTPTransport.Verify` — authenticate-and-stop, never issues `MAIL FROM`.
- Tests for the profile diff, the contact-gap descriptions, published-route reading, and SMTP
  verification.
- **One email sent**, to your own address, with your approval.

`EMAIL_PIPELINE_MODE` and `AUTO_SEND_INTENTS` are untouched. No profile was activated, no
opportunity reassessed, no contact plan created, no draft generated, no prospect contacted.
