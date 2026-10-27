# Source fixes and dashboard refinements

Status: **implemented and verified locally; not deployed.** Tracked schema is now **29**
(`0029_company_alias.sql`, additive only). No production data, credentials or infrastructure were
touched, nothing was sent, and no paid API was called.

**Live sources were not reachable from the build environment.** Its egress policy refuses every job
board host, including BrighterMonday, Himalayas and Working Nomads. Everything below marked
*fixture* ran the real adapters, ingestion and UI against payloads shaped like each source's
published format. Nothing is described as repaired on the live site. The changes record the
evidence that the next production run needs to confirm or rule out each fix.

## 1. Source issues

### BrighterMonday: `missing_employer` (258 held, 1 stored)

What was wrong:

- **The 258 was inflated.** The held-listing key included the search page the listing was found
  on. The adapter runs ten searches (backend, python, golang...), and the same listing appears in
  several of them, so one card became up to ten held rows. The key is now the listing itself, and
  repeats increase its observation count instead. In the fixture, one employer-less card seen across
  ten searches is now **one held listing, seen 10×**, where it used to be ten rows.
- **Employer links were only read when relative.** The selector matched `a[href^="/company/"]`
  only. It now also matches absolute `brightermonday.co.ke/company/...` links. This is the same
  element with the same meaning, not a new guess about the markup.

What could not be established: whether the live cards actually name an employer. The evidence
needed is the card markup itself, which this environment cannot fetch. The adapter now stores the
card's markup (capped at 4 KB) on every listing it holds back, and the held-listing page shows it
under **Card markup as received**. After the next production run, that markup answers it:

- **If it shows an employer**, the page layout changed. Update the selector from that markup.
- **If it does not**, the employer is confidential. The listing is correctly held, because it
  cannot be pursued.

No selector was invented, and no employer name is ever made up to get a listing through.

*Verified (fixture):* an absolute employer link is stored, and an employer-less card is held once
with its markup attached (`TestBrighterMondayAbsoluteEmployerLinksAndHeldCardEvidence`, plus the
local run). *Not verified:* live markup.

### Himalayas: `missing_source_identity` (20)

What was wrong: the adapter took a listing's identity only from a numeric `id` and its URL only from
`slug`. Himalayas' current public API, as described in third-party documentation of it (the API host
was unreachable from here), publishes `guid`, `applicationLink` and epoch-millisecond `pubDate`,
with no `id`. Every listing therefore arrived without an identity.

Fix:

- Identity is `id` when present, otherwise `guid`.
- The URL is the slug page, otherwise `guid` when it is a URL, otherwise `applicationLink`.
- `applicationLink` is recorded as the published application route.
- `pubDate` and `expiryDate` are read as text or as epoch seconds or milliseconds.
- The identity is stable from run to run, so a repeat run updates the listing and never
  duplicates it.
- If a payload still carries no identity, the field names it did carry are stored on the held
  listing (`source_fields`), so a further API change is diagnosable from the UI.

*Verified (fixture):* the legacy shape, a GUID URL, a slug-style GUID and a payload with no
identity (`TestHimalayasIdentityFallsBackToGUIDAndApplicationLink`). In the local run, a GUID-shaped
listing was stored under its GUID. *Not verified:* the live payload.

### Working Nomads: `ambiguous_company_identity` (15)

Cause:

1. Working Nomads publishes an employer name but no employer website.
2. Ingestion therefore guesses a domain from the name, for example "Harbor Web" → `harborweb.com`.
3. When that domain already belongs to a company stored under a different name ("Harbor Web
   Ltd"), the name and the domain point at different companies. Company domains are unique.

The resolver holds such a listing, and that is correct: choosing either company automatically could
attach the listing to the wrong employer.

Resolution, by a person and never automatically:

- **Sources → Listings held back → Review** lists each held listing with the companies it matched.
- **Attach to *company*** stores the listing under that company and records the decision as a name
  alias (`company_alias`).
- The source's later runs attach the same employer name without holding it again.
- No company is merged, renamed or created.
- Attaching a name that is already aliased to a different company is refused.
- A listing nobody attaches stays held, and stays visible.

*Verified (fixture and local UI run):* the listing is held, its candidates are shown, attaching
stores it under the chosen company, the next run neither duplicates nor re-holds it, the company
count is unchanged, and a conflicting attach is refused (`TestAmbiguousEmployerIsHeldThenAttachedByOperator`).

## 2. Dashboard metric cards

- The coloured left-edge strips are removed.
- Cards are neutral, with one border and one height (108 px).
- The label sits above a 26 px tabular number, with a muted detail line pinned to the bottom.
- Colour now means something: only **Drafts needing attention** turns amber, and only when it is
  above zero. "Unrelated" is no longer red.
- On phones, labels wrap instead of truncating.
- The sidebar and page layout are unchanged.

## 3. Actionable alerts

Each alert in **Needs attention** that has a destination now ends in a link. The rest stay plain
text.

| Alert | Link |
| --- | --- |
| *N* companies cannot be searched for contacts until the website domain is corrected | One company: **Correct the domain** opens `/companies/{id}#company-domain`. Several: **Review companies** opens `/companies?contact_search=identity_blocked`. |
| *N* discovery sources failed on the last run | **Open sources** |
| *N* listings held back because the employer matches more than one company | **Review listings** opens the filtered held view |
| No active profile / failed compositions / unresolved send attempts | The profile, the failed compositions filter, or uncertain send records |
| Provider initialisation, worker failures | No link: there is nothing in the UI to fix them |

How the domain alert works:

- **New Domain card on the company page.** It is anchored at `#company-domain` and shows the stored
  domain and its status. When contact search is blocked, it says why, using the blocker's own
  reason, and the field is marked as needing correction.
- **Correcting uses the existing workflow.** Saving goes through the existing domain update
  (`POST /companies/{id}/domain/update`), so does the inline domain form on the companies list, and
  so does **Mark domain verified**.
- **The block clears.** Each of those actions now re-queues a search that was blocked for identity.
  The next search re-checks identity against the corrected domain, and if the domain still doesn't
  hold, it blocks again.
- **The alert updates straight away.** These actions also drop the dashboard's 10-second snapshot
  cache, so the alert changes on your next view.
- **The companies list has a filter for it.** A **Contact search** filter, with the option
  **Waiting on a domain correction**, has been added. When that filter is on, a banner explains it.

*Verified (local UI run, Playwright):*

1. 5 blocked companies → **Review companies** → the filtered list shows exactly those 5.
2. Four domains corrected on the company page → the alert reads "1 company" and links straight to
   that company's Domain card.
3. A re-queued company was immediately claimed by the contact worker, which tried the corrected
   domain (the sandbox refused the request).
4. Attaching the ambiguous listing removed that alert on the next view.

## 4. Count consistency

**Inconsistency found and fixed.** The dashboard's Opportunities, Strong, Possible, Unrelated and
"not yet assessed" counts included suppressed postings (expired, filled or taken down). Every one of
those cards links to the Opportunities list, which hides suppressed postings by default. The
dashboard was therefore higher than the list it opened, by the number of suppressed postings. All
opportunity counts now leave out live suppressions, as do the priority list and the
missing-destination count. The suppressed total is shown in **Operational details** with a link to
`?suppressed=only`.

Checked, and left unchanged:

- **Fit buckets are mutually exclusive.** A match is unique per opportunity, revision, profile
  revision and matcher version, and there is one active profile. So strong + possible + unrelated +
  not yet assessed = the total. A test asserts this, and the local run showed 5 + 15 + 1 + 0 = 21.
- **Held listings are not opportunities.** They were never stored as opportunities, so no opportunity
  count includes them. The dashboard's details panel now says so and gives the 7-day held count,
  with a link.
- **Duplicates:** an opportunity is unique per source and source key (unchanged). Held listings are
  now one row per listing (section 1).
- **The held-back count on Sources** covers unresolved listings seen in the last 7 days, matching
  the heading above it.

*Verified:* `TestDashboardOpportunityCountsMatchTheListTheyLinkTo` checks that the dashboard total
equals the list total with a suppressed posting present, that the strong count equals the strong
list, and that the buckets sum to the total.

## 5. Held-back listings view

**Sources → Listings held back** links each count to `/sources/held?source=…&reason=…`. The page
has:

- filters by source and reason, plus an option to include listings not seen this week;
- the reason in plain words, with what to do about it;
- each listing's URL, location, the domain tried, the page it was found on, the source fields and
  the card markup;
- the attach action for ambiguous employers;
- pagination at 50 per page.

## 6. Verification summary

| Check | Result |
| --- | --- |
| `go vet ./...`, `gofmt` | Clean |
| `go test ./...` with isolated PostgreSQL | All 24 test packages pass |
| New tests | Himalayas identity, BrighterMonday evidence, ambiguous attach, dashboard counts vs list, identity-blocked list and re-queue, alert links and neutral cards |
| Frontend | No separate JS test suite exists. Templates are covered by Go render tests and by the Playwright run below. |
| Migration | Applied locally with the production procedure: `backup-verify` (restore verified) → `apply -proof` → schema current |
| Local UI run | Dashboard, companies filter, company Domain card, domain correction, held views, attach. Desktop and phone width. |
| Live sources | **Not verified.** The hosts are unreachable from this environment. |

## 7. Deploying

```bash
cd /opt/apps/prospector
git pull --ff-only
bash deploy/deploy.sh      # backs up, verifies the restore, applies 0029, starts the new app
```

After deploying:

1. **Sources → Fetch all now.**
2. Open the BrighterMonday held listings and expand **Card markup as received**. If it shows an
   employer, send that markup to have the selector updated. If not, the employer is confidential.
3. Check that Himalayas now stores listings. If it still holds any, its held rows list the fields
   the API actually sent.
4. For Working Nomads, attach each ambiguous listing that really is the existing company. Leave
   the rest held.
5. Held-row counts recorded before this release keep their old keys and drop out of the 7-day count
   once they stop being seen. The new count reflects distinct listings.
