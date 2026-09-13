# Production activation

# **ACTIVE — MANUAL MODE**

The system is in real use. Candidate profile revision 3 is approved and active, 273 opportunities
carry authoritative assessments, and the rollout mode is `v2_manual_send`: a reviewed draft can be
transmitted after explicit human approval of that exact content.

**Automatic sending is off.** `AUTO_SEND_INTENTS` is empty, the mode is not `v2_auto`, and the
automation worker refuses to start. **No prospect email was sent during activation** — zero send
attempts are recorded.

---

## 1. Activation actions

| Step | Result |
| --- | --- |
| Verified backup taken before any change | schema, row counts and content digests matched on restore |
| Profile revision 3 activated, pinned to `7138cbad…` | approved and active |
| Authoritative assessment over the corpus | 273 assessed, 273 stored |
| Source-aware domain strategy implemented | §4 |
| Published application routes retained | §5 |
| Contact sanitation at point of entry | §6 |
| 179 internationally-sourced companies refreshed | §7 |
| `EMAIL_PIPELINE_MODE=v2_manual_send` | §8 |
| `AUTO_SEND_INTENTS=` left empty | §8 |
| AI composition enabled with a request budget | §8 |
| Application started and verified | §10 |

---

## 2. Profile state

Activated through `db.ActivateProfile` — the same call the `/profile` page makes, with the same
guards: explicit revision id, the content hash you approved, and the observed active revision.
The document was not modified.

| | |
| --- | --- |
| Active revision | **3** (revision number 3) |
| Status | **approved** |
| Content hash | `7138cbad64ffbe1ffd312262d0306539666685fb82273ce46059efc4d5a858da` — unchanged |
| Other revisions active | **none** (1 approved/inactive, 2 draft/inactive) |

Activation approves and activates in one transaction, so revision 3 moved from `draft` to
`approved` as part of the same act.

**Stale assessments:** none existed to invalidate. Before activation every opportunity answered
`profile_required` and nothing was stored, so the 273 assessments written afterwards are the
first authoritative ones rather than replacements.

One incidental observation: revision 2 carries the same content hash as revision 3, so they are
the same document saved twice. Harmless, and left alone.

---

## 3. Authoritative match counts

Run through `assess.Service.AssessOpportunity` — the same path the web UI uses — persisting via
`SaveOpportunityMatch`.

| Classification | Authoritative | Rehearsal prediction | Difference |
| --- | --- | --- | --- |
| `strong_match` | **6** | 6 | none |
| `possible_match` | **94** | 94 | none |
| `unrelated` | **173** | 173 | none |
| Total assessed / stored | 273 / 273 | 273 | none |

Blockers also match exactly: `unresolved_mandatory_requirement` 273,
`unrelated_application_role` 173, `family_not_clear` 55,
`insufficient_supported_capabilities` 39, `insufficient_requirements` 24,
`unresolved_mandatory_condition` 23, `known_preference_conflict` 1.

**No differences to investigate.** The rehearsal and the production path use the same
`opportunity.Assess`, and this confirms they agree on real data rather than merely in principle.
Matching rules were not touched.

---

## 4. Source-aware domain strategy

The defect: `verify.CandidateDomains` put `.co.ke` first for every company, which is right for the
Kenyan job boards it was written for and wrong for international remote boards. 180 of 212
internationally-sourced companies were stored pointing at a domain that does not exist, so
research never ran and no contact could be found.

What was implemented:

- `verify.Market` with `MarketKenya` and `MarketInternational`, and `MarketForSource` mapping
  `remoteok`, `weworkremotely`, `remotive` and `himalayas` to international. **Anything
  unrecognized stays Kenyan**, so the Kenyan boards and manual entry keep their behaviour exactly.
- `CandidateDomainsForMarket` reorders the same candidates by market. `.co.ke` is **not removed**
  for international sources — it moves behind `.com`. Both markets still try both suffixes.
- `verify.DomainFromURL` extracts an employer domain from a URL the source published, and returns
  nothing for job boards and applicant-tracking systems (RemoteOK, WeWorkRemotely, Greenhouse,
  Lever, Workday, LinkedIn and others). A link to the board says where the listing lives, not who
  posted it.
- `seed.domainForSource` prefers a published URL over any generated candidate, in both markets: a
  stated fact beats a guess.
- The worker's verification loop now also tries candidates in market order, so its correction pass
  stops wasting its first attempt.

Regression tests: `TestKenyanAndInternationalSourcesOrderDomainsDifferently` (proves the orderings
differ and are permutations of each other, so nothing was dropped), `TestMarketForSource`,
`TestDomainFromURL`, `TestDomainForSourceRespectsMarket`, `TestPublishedURLOutranksGuessedDomain`.

---

## 5. Published application information

The adapters were discarding what the boards publish. Stored metadata for every affected
opportunity contained exactly one key: `extraction`.

- `opportunity.MetaEmployerURL`, `MetaApplyURL`, `MetaApplyEmail`, `MetaApplyEmailSource` and
  `MetaRouteProvenance` are the canonical keys, and `RecordPublishedRoute` stores only non-empty
  values with their provenance — so an absent route stays absent rather than becoming an empty
  string later code has to second-guess.
- The RemoteOK adapter now reads `apply_url` and `company_url`, which it previously parsed past.
- `opportunity.PublishedApplicationEmail` extracts an application address stated in the posting
  text, **but only with an application cue nearby** ("to apply", "send your CV", "interested
  candidates"). A support, press or privacy address in the same description is not an application
  route, and writing to one would be an unsolicited message to a mailbox that never invited it.
  Board and ATS addresses are excluded outright.
- A published route flows into the existing recipient policy unchanged: `StrongMatchContacts` and
  the automation policy already treat a source-published address as an acceptable destination,
  provided `ClassifyEndpoint` says it is a human mailbox. **Recipient-quality policy was not
  weakened.**

Tests: `TestPublishedApplicationEmailRequiresAnApplicationCue`,
`TestPublishedApplicationEmailIgnoresBoardAddresses`, `TestRecordPublishedRouteKeepsProvenance`.

Note: these apply to newly ingested listings. The 273 opportunities already stored were ingested
before the change and still carry no published routes; they would need re-ingestion to gain them.

---

## 6. Contact sanitation

`store.NormalizeContactValue` now runs inside `CreateContact`, before anything is stored:
unescapes HTML entities and JSON `\uXXXX` sequences (including the lost-backslash form that
produced `u003emgmt@…`), strips markup fragments and trailing sentence punctuation, lowercases,
drops a `mailto:` prefix, and validates the address shape. A phone-shaped local part is refused
explicitly. Downstream validation is unchanged and remains defence in depth.

**A correction to the readiness report.** It claimed a phone number was stored under
`kind='email'` for Cosuno. That was wrong: it is stored as `kind='phone'`, and it appeared in that
listing only because the display did not filter by kind. Across the whole database, `kind='email'`
rows with no `@` number **zero** — no phone number ever entered the email path. The guard is still
worth having as defence in depth, but it fixes a defect that did not exist. I have corrected
`ACTIVATION_READINESS.md`.

The escape defect was real: one row, `u003emgmt@stickermule.com`, already marked `bad` and
therefore inert. It is left in place as history; new ones can no longer be created.

Tests: `TestNormalizeRejectsTheRealDefects`, `TestNormalizeUnescapesAndTrims`,
`TestNormalizeRejectsNonAddresses`, `TestNormalizeLeavesOtherKindsAlone`,
`TestNormalizeDoesNotCorruptValidAddresses` (a valid address containing a `u00` sequence
mid-string must survive untouched).

---

## 7. International company refresh

Run through the ordinary paths: the same verifier the worker uses, the same `UpdateCompanyDomain`,
the same enricher. It creates no companies and no opportunities, so **it cannot have produced a
duplicate**; it corrects a domain on an existing row and then researches it. No historical
research record was deleted — enrichment adds and updates evidence.

Took 29m54s across 179 companies.

| | Count |
| --- | --- |
| Considered (international source, guessed `.co.ke`, unverified) | **179** |
| Received corrected domains | **124** |
| Researched | **127** |
| Obtained employer URLs | **0** |
| Obtained application URLs | **0** |
| Obtained published application addresses | **0** |
| Gained any contact | **85** |
| Obtained identified recruiting contacts | **4** |
| Obtained identified engineering contacts | **0** |
| Now have an acceptable contact | **4** |
| Still have no acceptable contact | **175** |

The four that became contactable:

| Company | Domain corrected | Contacts | Recruiting |
| --- | --- | --- | --- |
| Expel | `expel.co.ke` → `expel.com` | 0 → 3 | 1 |
| Tiger Analytics Inc. | `tigeranalytics.co.ke` → `tigeranalytics.com` | 0 → 4 | 1 |
| Hyphen Connect Limited | `hyphenconnect.co.ke` → `hyphen-connect.com` | 0 → 1 | 1 |
| Discord | `discord.co.ke` → `discord.com` | 0 → 5 | 1 |

Employer and application URLs are zero because those are captured at *ingestion*, and this refresh
deliberately did not re-ingest listings — re-ingesting is how duplicates get made. They will
appear on the next discovery run.

**Honest reading of this result.** The domain fix worked: 124 corrections, 85 companies that had
no contact now have one. But contact *quality* barely moved — 4 identified recruiting contacts and
zero engineering ones out of 179. Most of what enrichment finds on a corporate site is
`info@`, `support@`, `sales@`. The bottleneck has shifted rather than cleared: from "we are looking
at the wrong website" to "the right website does not publish a named engineering or recruiting
address."

### Two caveats worth your judgement

1. **Some corrections may be the wrong company.** Verification matches on name confidence, so
   `Wakacje.pl` → `wakacje.com` and `Wonderdog` → `wonderdog.com` resolved and matched by name —
   but Wakacje.pl is Polish and its real site is `wakacje.pl`. Neither yielded contacts, so no
   harm followed, but a name-matched domain is not proof of identity. Nothing sends to a company
   without a contact, so this is a data-quality caveat rather than a sending risk.
2. **Delinea gained only `sales@delinea.com`**, which the policy classifies as commercial and
   therefore refuses for a job application. That is the correct answer.

### Effect on the six strong matches

Still **0 of 6** have an acceptable recipient. Delinea's domain was corrected and it gained
contacts, but only a commercial one. Kruger NearShore and Wakacje.pl still have none. Cosuno and
Sticker Mule have generic mailboxes only.

Automation eligibility is unchanged at **0 of 273**, now blocked by: 267 not a strong match,
2 no contact on file, 2 generic mailbox only, 2 unknown function.

---

## 8. Configuration

```diff
+ EMAIL_PIPELINE_MODE=v2_manual_send
+ AUTO_SEND_INTENTS=
+ AUTO_SEND_DAILY_CAP=1
+ EMAIL_COMPOSITION_ENABLED=true
+ AI_REQUESTS_PER_HOUR=18
+ AI_REQUESTS_PER_DAY=60
```

`EMAIL_COMPOSITION_ENABLED` and the request budget were not previously set, and without them the
compose route returns 503 — composition would have been unavailable, so manual use would not
actually have worked. **I chose the budget values**; you did not specify them. One composition
reserves up to six provider requests (compose, repairs, and an independent review), so these allow
roughly three compositions an hour and ten a day. Ten reviewed drafts a day is already more than a
person reads carefully and sits under the manual send cap of 20. Raise deliberately after
observing real usage.

Composition and review use Claude (`ANTHROPIC_API_KEY` is set, so `NewConfigured` selects it), not
the Gemini pool.

The previous `.env` is backed up at `.local/env-backup-preactivation`.

---

## 9. A change to worker startup

Phase 7 gated the **whole** scheduler on send automation being configured. With automation off —
the normal and intended state — discovery, research, scoring and domain resolution would never
have run, so the opportunity corpus would have silently stopped growing the moment the system went
into manual use.

That conflated two different decisions. Research finds and describes companies and sends nothing.
The two are now separate: research workers run in every mode except `disabled`, and the automation
loop runs only when automation is configured. `TestResearchWorkersAreNotGatedOnSendAutomation`
reads the startup block and fails if they are ever merged again.

---

## 10. Runtime verification

`prospector-verify` checks the configured system rather than the test suite. **All 24 checks
pass.**

| Check | Result |
| --- | --- |
| Tracked schema | version 25 |
| Schema check passes | startup gate clean |
| Exactly one active profile | 1 of 3 revisions |
| Active profile approved | revision 3, `7138cbad…` |
| Rollout mode | `v2_manual_send` |
| Mode is not `v2_auto` | confirmed |
| Automation config valid | ok |
| Auto-send intents empty | `[]` |
| Send path ready | from `anthonyalando8@gmail.com`, Message-ID domain `gmail.com` |
| Automation NOT enabled | not `v2_auto`; no intent allowlisted |
| Automation worker will not start | confirmed |
| Research workers will run | confirmed |
| Opportunity corpus | 273 opportunities, 596 companies |
| Authoritative assessments persisted | 273 assessed, 0 unassessed |
| AI composition enabled | yes |
| Composer configured | ok |
| Semantic reviewer configured | ok |
| Provider budget configured | 18/hour, 60/day |
| Manual approval required | preflight blocks `not_approved` |
| Send limits intact | 20/day, 90s interval, 30-day domain cooldown |
| Suppression intact | 1 row |
| No prospect email sent | 0 send attempts |
| No unresolved send attempts | 0 |
| Legacy drafts inert | 62, none reachable by the send path |

### The application itself

Started on a scratch port and exercised:

```
/  /profile  /opportunities  /compositions  /threads
/legacy/drafts  /legacy/threads  /templates  /companies      all 200
/companies/1/generate-draft  /admin/draft
/templates/1/update  /followups/send                         all 410 Gone
```

Startup log:

```
Email pipeline mode: v2_manual_send; AUTO_SEND_INTENTS is empty, so no message can be sent automatically
Research workers running in v2_manual_send. They discover and describe; they never send.
Automatic sending is off: [rollout mode v2_manual_send is not v2_auto; no intent is allowlisted]
Worker configuration (research running=true, automatic sending=false): discovery every 20m0s, …
Starting Prospector on :18080
```

It started cleanly, served every page, and stopped cleanly.

### A bug this caught

`/threads` returned **500**. `ListThreads` selected an unqualified column list while joining
`company`, which has its own `id`, `created_at` and `updated_at` — PostgreSQL rejected the query as
ambiguous. It had been broken since Phase 5 and no test caught it, because no test had ever listed
a thread that exists.

Fixed by qualifying the columns with the table alias, and covered by
`TestListThreadsReturnsRealRows`, which sends a real message through the automation path and then
lists the resulting conversation. This is exactly the class of defect that only shows up when you
run the actual system, which is why §12 asked for it.

---

## 11. Tests

Full suite green: **22 packages, no failures**, after the discovery and ingestion changes.

New in this activation: 3 domain-market tests, 3 ingestion domain tests, 3 published-route tests,
5 contact-normalization tests, 1 worker-gating test, 1 thread-listing regression test.

---

## 12. Remaining operational warnings

1. **Contact quality is still the binding constraint.** 4 of 179 refreshed companies gained an
   identified recruiting contact; none gained an engineering one. 0 of 6 strong matches has an
   acceptable recipient. Manual sending works, but you will be choosing recipients by hand for
   most companies.
2. **Published routes need a fresh discovery run.** The adapters now retain employer and
   application URLs, but the 273 stored opportunities predate the change.
3. **Some corrected domains may be the wrong company** (§7 caveat 1). Worth a glance before
   contacting any newly-researched company.
4. **The real-draft quality sample is still outstanding.** No real draft has been composed under
   the active profile, so wrong-role rate, unsupported claims, payment leakage and reviewer
   false positives remain unmeasured on real strong matches. Normal manual use is now the way to
   gather that evidence — which is the point of activating.
5. **Reconciliation is not implemented.** Unresolved `reserved`/`sending`/`uncertain` attempts
   stay fail-safe: they block that message and halt automation. If one occurs, it will be visible
   on the dashboard under "need reconciliation"; see `RECONCILIATION_DESIGN.md` for the procedure,
   and do not work around the triggers.
6. **One legacy artifact left in place:** `u003emgmt@stickermule.com`, marked bad and inert.
7. **`AI_ENABLED=false`** in the existing configuration, which disables the *legacy* draft
   budget. It does not affect v2 composition, which is gated by `EMAIL_COMPOSITION_ENABLED`.

---

## 13. How to use it

1. `/opportunities` — browse the corpus, now properly paginated. 6 strong, 94 possible.
2. Open a company, review the fit and its reasons, then create or select a contact plan on
   `/companies/{id}/plan` — choosing the intent, the recipient and the approved proof.
3. Generate a composition. It runs the composer, the deterministic validators and an independent
   semantic reviewer.
4. `/compositions/{id}` — read the email, both validation layers, staleness and send eligibility.
5. Approve that exact content, then send. The send button appears only when every preflight check
   passes.
6. `/threads` — the conversation, and where to record a reply or a bounce.

Nothing in that sequence happens on its own. Discovery and research run in the background;
composition, approval and sending are all deliberate acts.
