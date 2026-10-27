# Discovery repair and expansion

Status: **implemented and verified locally; not deployed.** Tracked schema is now **28**
(migration `0028_opportunity_access_tracking.sql`, additive only). Nothing was sent, no paid API
was called, and no production data or host setting was touched.

## A. Initial findings: what was broken and why

| Problem | Actual cause |
| --- | --- |
| Only MyJobMag seemed to work | The discovery worker called exactly one seeder, `jobboard` (BrighterMonday + MyJobMag). We Work Remotely, Remote OK, Remotive and Himalayas were registered but ran **only** when someone POSTed `/admin/seed/{source}` by hand, and no UI button did that. In production they effectively never ran. |
| We Work Remotely jobs not findable or trackable | Same cause: never scheduled. The adapter's parse → normalize → persist path was correct (the existing fixture test proves it). Secondary defects: one failed feed or one failed row aborted the whole WWR run; the RSS `region`, `country`, `type` and `expires_at` fields were ignored, so every WWR listing had no location, no work type and no expiry. There was also no way to save or track any opportunity. |
| New listings never ranked or surfaced | Fit assessment only ran from a manual button or the CLI, so new listings stayed "unassessed" and never reached the priority list or the dashboard. |
| Failures invisible | A failing source logged to the server log only. Listings held back at ingestion (no employer named, ambiguous identity) went to `ingestion_review`, which nothing displayed. |
| Fragile fetching | 10-second timeout and no retry: a slow feed from the EU VPS failed the whole source. A Kenyan page whose markup changed matched zero job cards silently, which looked like "no jobs today". |
| No eligibility or application info | Nothing recorded whether a Kenyan applicant was eligible, how to apply, or whether applying cost money. |
| Contact availability | Addresses were found only by contact discovery. Published apply routes were recorded for Remote OK only; a listing without an email had no "how to apply" path in the UI. |
| Prospecting | Companies were ranked by the legacy keyword score. There was no form to add a prospect, `business_segment` (agency, SME...) was never set, and company relevance was only computed on a manual "rerun". |

**Live-source verification was not possible from this environment.** The build sandbox's egress
policy refuses every job-board host, so the live HTTP status of each board from the Contabo VPS
could not be observed. The causes above come from the code and the logs, not from guessing about
hosting location or IP reputation. After deploying, the new **Sources** page shows each source's
real result from the VPS (see section F).

## B. Implemented changes

Discovery and reliability (`internal/seed`, `internal/worker`)
- The discovery loop now runs **every scheduled source**, each on its own Redis lease
  (`SOURCE_INTERVAL_MINUTES`, default 180), with a 5-minute per-source timeout. One source failing
  never stops the others.
- Fetching: 25-second timeout; one retry after a pause for network errors, 429 and 5xx. A 403 or 404
  is not retried, because it is an answer.
- Batch ingestion: a bad feed or row is logged and skipped. A run is an error only when every feed
  failed or every listing failed to store. A Kenyan page that matches no job cards now reports "no
  job cards matched the page markup".
- The outcome of every source run (last run, new count, error, last success) is stored in Redis and
  shown on the new **Sources** page, with a "Fetch now" button per source and for all sources.
- After each discovery pass, new and changed listings are **classified** (eligibility, route, cost),
  **assessed** against the active profile, agencies are detected, and stale company relevance is
  refreshed. All of this is deterministic and local: no AI, no network (`assess.Service.ProcessNew`).
- WWR now records region and country as location, `type` as employment type, `expires_at` (so the
  existing suppression sweep can retire expired listings) and the category.

New sources (public, documented, unauthenticated APIs only)
- **Jobicy** (development category) and **Working Nomads** (development category with an engineering
  title filter).
- **Employer career boards**: Greenhouse, Lever and Ashby public job-board APIs, engineering roles
  only. Boards come from `ATS_BOARDS` **and automatically from any discovered listing whose apply
  link points into one of these boards**. The employer's own board then becomes a direct source of
  their listings, with their official application links. At most 30 boards per run, with a pause
  between requests.

Eligibility, application route and ranking (`internal/opportunity/access.go`)
- `ClassifyAccess` reads only what the listing states:
  - **eligibility**: worldwide, region-restricted, country-specific or unclear;
  - **from Kenya**: open, likely (EMEA/Africa), restricted or unknown;
  - **route**: published email, employer careers system (ATS), employer site, job board or unknown;
  - **cost**: free, account required, paid or unknown.

  Each label keeps the excerpt that supports it.
- A text restriction ("must be authorized to work in the United States") overrides a hopeful
  location field. An unstated location stays **unclear**; it is never assumed to be worldwide.
  Nothing is marked **paid** without a stated paywall phrase.
- Stored in the new `opportunity_access` table (keyed by opportunity, versioned by classifier and
  listing revision). It is deliberately not stored in `opportunity.metadata`, because metadata is
  part of the content hash and would mint new revisions and invalidate fit assessments whenever the
  rules changed.
- The **"Best to pursue"** ranking is now the default list order: fit + Kenya access + route + cost
  + contract bonus + freshness. These are small fixed weights in `opportunity.PursuitScore`, and a
  test keeps the SQL version identical to the Go version.

Workflow and UI
- **Opportunities**:
  - quick views: Open to Kenya, Direct contact, Contract & freelance, Saved & applied;
  - filters: From Kenya, How to apply, Cost (including "hide paid"), Your progress and contract-only;
  - list columns: eligibility, apply route, paid/account flags, discovery date and progress.
- **Opportunity page**:
  - "How to apply" card with the eligibility evidence, route, cost, and buttons for the published
    email (opens your own mail client), the employer's application page, the original listing and
    the employer website;
  - "Your progress" card: save, applied, interviewing, offer, rejected or not interested, with notes
    and an applied date;
  - "Outreach" card with the contacts on file and links to plan outreach and contact discovery.
- **Companies**:
  - lead categories, strongest first: contract work posted, hiring, technical need found (from
    researched relevance evidence) and general prospect;
  - "Agencies & consultancies" view;
  - **Add prospect** form and **Import a list** (CSV) form;
  - business type editable on the company page.
- Sidebar: **Sources** and **Saved & applied**. Dashboard quick actions point at the new views.
- The existing outreach path is unchanged: contact plan → compose → review → explicit approval →
  send. Nothing sends automatically.

Schema: `0028_opportunity_access_tracking.sql` adds `opportunity_access` and `opportunity_tracking`.
It is strictly additive.

## C. Source status

| Source | Status | Notes |
| --- | --- | --- |
| MyJobMag | Working (unchanged) | Public listing pages. |
| BrighterMonday | Unverified | Same adapter. Live state from the VPS is unknown here; the Sources page will show either a 403 (blocked) or "no job cards matched" (markup change). |
| We Work Remotely | **Repaired** | Now scheduled; region/type/expiry parsed; failed feeds isolated. Public RSS. |
| Remote OK | **Repaired** (now scheduled) | Public JSON API; apply and employer links recorded. |
| Remotive | **Repaired** (now scheduled) | Public JSON API. |
| Himalayas | **Repaired** (now scheduled) | Public JSON API. An empty location restriction is left "unclear" rather than assumed worldwide. |
| Jobicy | **Newly integrated** | Public API; asks for sparing polling and link-back, which the 3-hour interval and stored listing URLs respect. |
| Working Nomads | **Newly integrated** | Public jobs export. |
| Greenhouse / Lever / Ashby | **Newly integrated** | Public employer job-board APIs. Boards come from ATS_BOARDS plus apply links in discovered listings. |
| Wellfound | Not integrated | No public API; automated access is not permitted. |
| Upwork / Fiverr / Freelancer | Not integrated | Their terms forbid scraping, and they route work through on-platform bidding. |
| Clutch / GoodFirms (agency directories) | Not integrated | They refuse non-browser clients (the existing code already notes 403s); bypassing that is out of scope. |

## D. Client prospecting

- Agencies, studios and consultancies are detected from the company name, or from the employer
  describing itself in its own listing ("we are a digital agency..."). An operator's own
  classification is never overwritten.
- Employer career boards found through job listings put smaller companies' own boards into the
  pipeline, with their official application routes.
- Prospects found by hand (an agency's site, a directory you browse yourself) are added with **Add
  prospect** or **Import a list**. Research, domain verification, contact discovery and relevance
  then run on them automatically.
- Companies are ordered by evidence of paid work, and a company is never described as needing
  anything it has not published or been researched to show. Contacts are stored with their source
  and verification state as before; guessed address patterns stay review-only (unchanged policy).

## E. Verification

- `go test ./...` with the isolated PostgreSQL tests enabled: **all 25 packages pass**. `go vet` and
  `gofmt` are clean.
- New tests:
  - eligibility, route, cost and contract classification (`internal/opportunity/access_test.go`);
  - every source from payload to stored, classified opportunity, including a three-of-four failed WWR
    feed run, an ATS board auto-discovered from a Remote OK apply link, no duplicates on repeat, an
    unattributed WWR item held for review, and WWR expiry stored (`internal/seed/sources_test.go`);
  - a 403 not retried; a 503 retried once;
  - the SQL pursuit score equals the Go score, plus the access and route filters
    (`internal/store/opportunity_access_test.go`);
  - the tracking lifecycle;
  - lead categories and agency detection (`internal/store/company_lead_test.go`).
- Local end-to-end run against a real PostgreSQL and Redis, with the production migration procedure
  (`backup-verify`, then `apply -proof`):
  - the real registry, adapters, ingestion, classification and assessment ran with fixture payloads
    shaped like each source's public format;
  - WWR, Remote OK, Remotive, Jobicy, Working Nomads and an auto-discovered Lever board stored
    listings, while the Kenyan boards (403) and Himalayas (429) failed in isolation;
  - a second run added nothing;
  - the UI showed WWR jobs ranked with eligibility evidence, a tracking update recorded over HTTP and
    reflected in the list, a paid listing ranked below free ones, the Sources page with per-source
    results, and an agency prospect with its lead category and relevance.
- The live app's scheduler was also observed running every source in turn. All were refused here by
  the sandbox proxy, which is the expected result in this environment.
- **Limitation: no live job-board response was observed.** Fixture shapes follow each source's
  published format, but a field rename on a live API would only show up after deploying, on the
  Sources page.

## F. Deployment

Ready to deploy. The standard path handles the migration:

```bash
cd /opt/apps/prospector
git pull --ff-only
bash deploy/deploy.sh      # backs up, verifies the restore, applies 0028, starts the new app
```

Optional settings in `.env.production` (defaults shown):

```
SOURCE_INTERVAL_MINUTES=180
ATS_BOARDS=greenhouse:gitlab,lever:example
```

After deploying:
1. Open **Sources** and press **Fetch all now**. Each source shows Working or Failing with the
   VPS's real error.
2. A source failing with HTTP 403 is blocking the VPS; leave it, since bypassing is out of scope.
3. "No job cards matched" means the site markup changed, and the adapter's selectors need updating.
4. Existing listings are classified and assessed on the first discovery tick (up to 300 per tick).

## G. Remaining work

- Confirm each source's live result from the VPS on the Sources page, and update BrighterMonday's
  selectors if it reports a markup mismatch.
- Add employer boards you want followed to `ATS_BOARDS`. Auto-discovery only finds boards that a
  listing links to.
- Existing listings ingested before this change carry no published apply routes (from Remote OK
  only); they gain them when the source next republishes them.
