# Navigation, search and drill-down

## Problem and resulting behavior

Dashboard metrics previously opened generic lists and lost the state represented by their counts. Lists lacked whole-corpus search, reliable filter pagination, and contextual return links. The existing dashboard design is retained; changes add real links, search, visible filters and result totals.

## Dashboard destinations

| Entry | Destination and scope |
| --- | --- |
| Companies | `/companies` |
| Opportunities | `/opportunities` |
| Strong matches | `/opportunities?fit=strong_match` |
| Possible matches | `/opportunities?fit=possible_match` |
| Unrelated | `/opportunities?fit=unrelated` |
| Not yet assessed | `/opportunities?assessment=unassessed` |
| Drafts needing attention | `/compositions?attention=required` |
| Sent | `/messages?state=accepted` ? accepted send attempts |
| Replies | `/threads?event=replied` ? distinct threads with recorded replies |
| Research queue | `/companies?research=queue` |
| Legacy drafts | `/legacy/drafts` |

Metric cards use real anchors with focus/hover styles. The Opportunities card has a separate accessible unassessed link rather than nested anchors. Non-actionable runtime status remains informational.

The opportunity panel is now **Priority opportunities**. Its existing scope includes nonclosed, nonignored opportunities with current strong/possible matches or review-required selection. Both Open all and the footer use `/opportunities?view=priority&sort=priority`, with the same predicate/order as the snapshot. Each row links to its actual opportunity ID and carries that list context.

## URL and filter model

All browsing uses normal GET forms, server rendering and shareable query URLs. Opportunities supports:

- `q`: title, company, location, source, source key and external identifier.
- `fit`: `strong_match`, `possible_match`, `unrelated`.
- `assessment`: `assessed`, `unassessed`, `review_required`.
- `source`, `company_id` (existing `company` alias accepted), and existing `title` filter.
- `availability`: `open`, `closed`, `unknown`.
- `description`: `complete`, `partial`, `unavailable`.
- `selection`: `new`, `selected`, `review_required`, `ignored`.
- `sort`: `updated`, `newest`, `best`, `company`, `title`, `priority`.
- `view=priority` and `page`.

For example: `/opportunities?fit=possible_match&q=engineer&page=2`.

Assessment joins require the current opportunity revision, active profile revision and current matcher version. Old assessments are unassessed for current browsing. Best-match sorting ranks classification before numerical score, so unrelated score 100 cannot outrank a strong match. Without an explicit sort, searches favor title matches, then company, then other searchable fields. Description bodies are excluded to reduce noise.

Enums are trimmed and lowercased; invalid values are ignored with visible warnings. IDs must be positive integers. Text is bounded to 200 characters. SQL values remain bound parameters; sorting uses fixed whitelisted expressions. Unknown source text is a valid exact source filter, not an enum substitution.

Pages contain 50 rows. Count and data queries apply identical predicates. Pagination preserves every normalized filter. Filter forms omit page, resetting to page 1; out-of-range pages are clamped. Active chips, controls and result totals expose the scope. Empty results retain the active filters and a Clear filters action. Fit and assessment facet counts come from a single aggregate and are explicitly labeled as whole-workspace counts rather than contextual counts.

## Companies and entity navigation

Company browsing retains existing search/domain/location/industry filters and adds source, research state, has opportunities, and has current strong match. Research choices include queue, researched, unresearched and recently researched (24 hours). Pagination and detail links preserve these filters.

Opportunity rows/details link company names to the actual company. Company details link individual opportunities, an authoritative company opportunity count to `company_id`, current v2 compositions to their IDs, and recorded contact plans to their anchored rows in the existing company plan workspace. This avoids a new plan-route framework.

Contact readiness filters were not added: acceptable recipients depend on the selected plan, recipient policy and suppression/preflight state. A company contact-quality approximation would misrepresent readiness. No recipient decisions changed.

## Review, sending and research semantics

Opportunity human review uses `assessment=review_required` (the stored review-required selection state); possible match remains a distinct fit classification. Composition semantic review uses `attention=semantic_review`; outstanding human send approval uses `attention=approval_pending`. The combined attention destination includes unsent, undiscarded compositions needing semantic review or reviewed compositions without live approval for their current revision. Expired running work is displayed as failed using existing effective-status semantics. Legacy review remains in legacy pages.

The Sent count represents accepted send attempts, so a read-only Send records list was added rather than incorrectly treating thread count as sent count. Reply browsing uses EXISTS over recorded reply events, includes subsequently closed threads, and avoids duplicate counting.

Research queue browsing shares the dashboard's existing store predicate: new companies with verified/manual domains whose next attempt is due. Cooldown/future attempts are excluded. Queue UI explains this eligibility and links to existing live worker status for currently researching/failures. Recently researched uses existing completion timestamps. No second worker-state model or scheduler change was introduced.

## Context, sidebar and activity

Details carry an encoded `return` URL through ordinary links. Back links return to the exact originating filter/page, including company and plan drill-downs. Lightweight breadcrumbs identify the current entity. Browser Back/Forward works naturally without client routing.

Return URLs are limited to 3000 characters and allow only known internal list/entity paths. External schemes/hosts, control characters, backslashes and unknown paths are rejected; fragments are stripped. Missing/invalid context falls back to the appropriate section.

The common shell maps nested opportunity, company/plan, composition and thread templates to their parent active sidebar section. Search and Send records have working sidebar destinations.

Persisted activity links use structured opportunity/composition/thread IDs from their source records. Redis worker messages lack reliable entity IDs and remain unlinked; IDs are never inferred from display text.

## Global search and query design

The dashboard search submits to `/search?q=...`. PostgreSQL searches companies and opportunities across the full corpus, displaying up to 20 previews per group plus authoritative counts and links to each full filtered list. No new service or frontend dependency was added.

New store filters use bounded count-plus-page queries. Company coverage uses EXISTS; opportunity current-match joins avoid per-row queries. Opportunity facets use one aggregate and source enumeration. Global search uses two count/page pairs only on its endpoint. No migrations or speculative indexes were added. Dashboard snapshot polling/cache behavior is unchanged.

## Verification

Store integration tests use isolated scratch databases and cover classification-only results, current-revision unassessed state, company intersections, searches beyond unfiltered page 1, filtered pagination, class-before-score sorting, availability/description/selection, facets, priority scope, stale profile assessments, parameterized hostile text, queue eligibility/count agreement, composition attention/current approval semantics, and distinct recorded replies including closed threads.

Web tests cover metric destinations, exact opportunity rows, panel scope, preservation of all query parameters, invalid normalization, unsafe return rejection, company filters and nested active sidebar sections.

Browser verification used headless Microsoft Edge against a GET-only local preview wired to the existing database. Flows passed: dashboard Strong/Possible/Research/Legacy/Attention; exact priority opportunity; opportunity to company to opportunity; search plus fit; page 2; detail; explicit contextual return; browser Back/Forward; Clear filters; global search. The live data yielded 6 strong, 94 possible and 67 possible engineer search matches, with 17 on page 2. Current empty attention/queue states were verified visually; populated semantics were covered in scratch tests. Network capture recorded only GET requests. No workers or sending routes were started by the preview.

Validation completed successfully: `go test ./...`, `go vet ./...`, targeted scratch-database integration tests, and `git diff --check`. The nested-sidebar regression also passes in `go test ./internal/web`.

## Files changed

- Routes: `cmd/prospector/main.go`.
- Store: `company.go`, `dashboard_snapshot.go`, new `opportunity_browse.go`, `workflow_browse.go`.
- Web: `dashboard.go`, `handler.go`, `opportunities.go`, `composition.go`, `dispatch.go`, `phase3.go`, `shell.go`, new `navigation.go`, `workflow_lists.go`.
- Templates: dashboard, sidebar, companies/company/plan, opportunities/opportunity, compositions/composition, threads/thread; new messages and search templates.
- Styling: new `internal/web/static/lists.css`.
- Tests: new store navigation/search and workflow tests, web navigation tests, updated pagination test in `phase7_test.go`.

## Explicit answers

1. **Strong matches:** `/opportunities?fit=strong_match`.
2. **Possible matches:** `/opportunities?fit=possible_match`.
3. **Unrelated:** `/opportunities?fit=unrelated`.
4. **Research queue:** `/companies?research=queue`, using the dashboard queue predicate.
5. **Drafts needing attention:** `/compositions?attention=required`, the exact combined semantic-review/approval set.
6. **Whole-corpus opportunity search?** Yes, PostgreSQL searches before pagination.
7. **Server-side filters?** Yes, bound SQL predicates in the store layer.
8. **Filters in URLs?** Yes, normal GET query parameters.
9. **Pagination preserves filters?** Yes; changing controls resets page and out-of-range pages clamp.
10. **Back preserves working context?** Yes, validated explicit return URLs and natural browser history.
11. **Exact dashboard opportunity deep links?** Yes, `/opportunities/{id}`.
12. **Filtered company to its opportunities?** Yes, both exact rows and a company-scoped count/list link.
13. **Review concepts separated?** Yes: opportunity selection review, composition semantic review, human approval and legacy review have distinct destinations.
14. **Matching/send/automation behavior changed?** No. Matching, scores, profile, recipient policy, prompts, semantic decisions, approval, preflight, SMTP, suppression and scheduler behavior are unchanged.
15. **Emails sent?** No. Verification used read-only GET browsing and isolated database fixtures, without sending transports.

The running application must be restarted normally to load the new Go handlers/templates; implementation did not restart the user's process or change `.env`.
