# Broader opportunity discovery, and dismissing opportunities

Status: **implemented and verified locally; not deployed.** No database migration: dismissal
reuses the existing `opportunity_suppression` table. The matcher version is now `fit-v2`, so
stored jobs are re-assessed automatically. No AI is used anywhere in discovery or matching, so
there are no prompts or AI costs involved.

## 1. Why discovery looked narrow

The job feeds were not the main problem. We Work Remotely, Remote OK, Working Nomads and the
employer boards already read whole development feeds. The narrowness came from how jobs were
classified and ranked.

1. **The title alone decided a job's family.** A job was "clear" only when its title contained
   one of your approved family phrases (backend, full stack, web, api, golang, software).
   - "Platform Engineer", "Distributed Systems Engineer", "Payments Engineer", "Integration
     Engineer", "Systems/Cloud Engineer" and "Engineer – Platform" all stayed "ambiguous", however
     well their requirements matched your profile.
   - An ambiguous job can never be a strong match and scores 20 points lower.
   - Generic "Software Engineer" titles were clear through the Software Engineering family, so
     they took the top of the ranking, the strong-match count and the dashboard's priority list.
2. **Some hands-on engineering titles were classified unrelated.** Titles without an engineer or
   developer noun ("Software Architect", "SRE", "Backend Lead", "Tech Lead") were thrown out.
3. **"Go" in a title was not recognised as Golang.** "Go Developer" and "Senior Go Engineer" did
   not match your Golang aliases.
4. **A few source gaps.**
   - Remotive read only its software-dev category, not DevOps, where platform, infrastructure and
     SRE roles are filed.
   - Jobicy read one general feed of 50 jobs.
   - The Kenyan boards had no searches for full stack, web, API or DevOps.

## 2. Changes

### Matching (`internal/opportunity/match.go`)

**The family can now come from the requirements.** For a title with an engineering noun that
names no approved family, the family is taken from the job's own requirements.
- At least two of your approved capabilities must be supported by actual requirement excerpts.
- The family comes from those capabilities' own `role_family_ids` in your profile.
- The job is recorded with the reason `family_established_by_requirements`.
- It scores 10 points below a family the title states, so "Backend Engineer" still ranks above
  an equally matched "Platform Engineer".
- All other strong-match rules still apply: enough requirements, a complete description, no
  unmet mandatory requirement, and a score of at least 80.

**Some titles never get an inferred family:**
- **Unrelated titles:** non-engineering functions and disciplines (sales, customer support,
  mechanical, marketing and the rest of the existing list) stay unrelated.
- **Management titles:** titles naming a manager, director, head of, VP, chief or CTO stay
  ambiguous.

**More titles are recognised as engineering:**
- "architect", "SRE" and "SWE" count as engineering role nouns.
- A family plus "lead" ("Backend Lead") is clear.
- "Tech/Technical/Engineering/Development Lead" is ambiguous rather than unrelated.
- "Go" directly before an engineering word is read as Golang. "Go-to-market" is untouched.

### Sources (`internal/seed`)

**More feeds and searches:**
- Remotive also reads its DevOps category.
- Jobicy adds `tag=backend` and `tag=golang` feeds.
- The Kenyan boards add the searches `full stack`, `web developer`, `api developer` and `devops`.

**No new duplicates:**
- The same listing returned by several feeds or searches is stored once, keyed by its source
  ID or canonical URL as before.
- One failing feed never stops the others.
- A feed that answers with unreadable data is counted as failed, instead of failing the whole
  source.

### Keeping existing drafts and plans usable

Bumping the matcher version previously marked every composed job-application draft stale
(`matcher_changed`), including drafts in progress.
- **Only a changed conclusion counts:** a version change makes a draft stale only if the job's
  assessment actually changes. Same family, classification, requirements and reasons mean the
  draft stays valid.
- **Jobs with plans go first:** they are re-assessed before anything else, so their drafts are
  stale for as short a time as possible.

## 3. Dismissing opportunities

The app already had a soft-delete convention for opportunities, **suppression**.
- It records a reason, who decided, when, and an optional note.
- Nothing is deleted.
- Restoring ("reinstate") puts the job's previous selection status back.
- It is transactional and locks the row, so two concurrent clicks cannot both write a decision.
- It survives re-ingestion.

A dismissal is a suppression with the existing reason **Not pursuing**, so there is no new
table, flag or migration.

**What's new:**
- **List actions:** each row has **Dismiss**, in one click with no form. In the Dismissed view
  each row has **Restore**. Both return you to the same page, with the same search, filters, sort
  and page.
- **Notices:**
  - "Dismissed "Title at Company"", with an **Undo** button.
  - "Restored …".
  - A clear failure message if nothing changed.
- **Duplicate requests are safe:** a double click or a second tab shows the outcome that already
  happened.
- **Active / Dismissed tabs** sit above the list, and the Dismissed tab shows its count. Switching
  tab keeps your search and filters.
  - The Dismissed view shows dismissed jobs plus those taken out automatically (expired, filled,
    removed by the source), each with its reason badge and a shaded row.
  - The quick-view pills are hidden there, because their counts are for active jobs.
- **Filter:** the "Suppressed" filter under More filters is now **Dismissed**: Active only,
  Dismissed only, or both.

**Places that no longer show dismissed jobs:**
- the opportunity list, which already excluded them;
- global search;
- a company's opportunity count and its list of advertised roles;
- the contact plan's vacancy list and the company-relevance input;
- the fit, review, unassessed and saved counts in the filters.

The dashboard already excluded them.

**Nothing attached to a job is touched:**
- Dismissing and restoring only add or close a suppression record.
- Notes, saved/applied progress, contact plans, drafts, reviews and send records stay as they
  were, and the job's revision history stays complete.

**Rediscovery:** if a source publishes a dismissed job again, it updates the same record as a new
revision. It does not create a duplicate and it stays dismissed. The job page already notes "the
source has published revision N since this was suppressed", so you can restore it if it's live
again.

## 4. API and data changes

- No new table or column, and no migration.
- `POST /opportunities/{id}/suppress` and `POST /opportunities/{id}/reinstate` accept
  `from=list` and `return=<list URL>`. With `from=list`, the reason defaults to `not_pursuing`,
  and the response redirects to the list with `notice=dismissed|restored|failed|restore_failed`
  and `notice_id`.
- Default queries now exclude live suppressions in search, the company opportunity count, the
  company and plan job lists (`ListActiveOpportunities`) and the filter facets.

## 5. Verification

- `gofmt` and `go vet` are clean. `go test ./...` passes in all packages, against isolated
  PostgreSQL databases.
- **New tests:**
  - **Matcher (`TestFamilyEstablishedByRequirements`):** 20 titles. Platform, distributed
    systems, payments, integration, systems, cloud and "Engineer – Platform" are inferred as
    backend. Go Developer, Senior Go Engineer, Backend Lead, Software Architect and Software
    Engineer are clear from the title. Platform Engineer and Tech Lead with unrelated
    requirements stay ambiguous. Engineering Manager stays ambiguous. Sales Engineer, Customer
    Support Engineer, Mechanical Engineer, Marketing Manager and Go-To-Market Lead stay
    unrelated.
  - **Matcher, scoring:** an inferred family scores below a stated one.
  - **Sources:** two Remotive categories returning the same listing store it once, and a
    DevOps-only listing is added. Jobicy keeps its other feeds when one is down. The existing
    multi-source test shows Jobicy's three feeds storing a listing once.
  - **Staleness:** a version-only change does not stale a draft; a changed or missing assessment
    does.
  - **Dismissal, on a real store:** the job is hidden from the list, search, company list,
    paging and counts. A repeat dismissal is harmless. Saved progress and history survive.
    Rediscovery is the same record with a new revision, no duplicate, and stays dismissed.
    Restore works, and a repeat restore is harmless.
  - **Updated tests:** two matcher tests now use requirements that do not establish a family,
    and the list test checks the Dismissed tab count instead of the old sentence.
- **Local run:**
  - **Re-assessment:** running the same step the scheduler runs re-assessed all 21 local jobs
    under `fit-v2`. Strong matches went from 5 to 8. "Go Platform Engineer", "Contract Go
    Engineer" and "Go Backend Engineer" were recognised as backend by title, "Senior Python
    Developer" got its family from its requirements, and "Tax Advisor" stayed unrelated.
  - **Dismissal in a browser (Playwright):** dismiss from the list, with the notice and Undo.
    The job disappeared from the list and search and stayed hidden after a reload. It appeared
    in the Dismissed view with its badge. Restore and Undo both worked.
- **Not verified:** live job-board responses. The build environment cannot reach the boards, so
  the new Remotive category and Jobicy tag feeds are written to those APIs' documented
  parameters but have not been seen working live. The Sources page shows each feed's real result
  after deploying.

## 6. Remaining issues and recommendations

- **Unassessed jobs right after deploying.** Every stored job shows *Unassessed* until the first
  discovery pass after the deploy re-assesses it, 300 per pass, jobs with plans first. **Fetch
  all now** on Sources does it straight away, 500 at a time.
- **Profile aliases.** The cleanest way to make a title "clear" is your profile's family
  aliases. Consider adding titles you want treated as stated rather than inferred, such as
  "Platform Engineer", "Payments Engineer", "Integration Engineer" or "Go Engineer", in a new
  profile revision. I have not changed your profile.
- **Frontend titles.** A "Frontend Engineer" job whose requirements match your React/Next.js
  capabilities is inferred as Full-Stack (possible, not strong). If you don't want frontend
  roles, dismiss them, or move those capabilities' role families.
- **Cross-source duplicates.** The same job on two boards is still two rows. Use **Dismiss**, or
  the existing "Duplicate" reason on the job page. Automatic cross-source merging was not added.
- **No bulk dismiss.** Each row is dismissed separately. A bulk "dismiss all unrelated on this
  page" action would be a small follow-up if you want it.

## Deploying

```bash
cd /opt/apps/prospector
git pull --ff-only
bash deploy/deploy.sh      # no migration in this change
```

Then click **Sources → Fetch all now** to re-assess every stored job straight away.
