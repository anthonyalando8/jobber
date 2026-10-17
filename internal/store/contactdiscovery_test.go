package store

import (
	"context"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/opportunity"
)

// discoveryFixture builds a company with one opportunity and the assessment that makes it a
// strong match, which is the shape the contact-discovery queue is meant to notice.
func discoveryFixture(t *testing.T, db *DB, name, domain, classification string) (companyID, opportunityID int64) {
	t.Helper()
	ctx := context.Background()
	company, err := db.CreateCompany(ctx, name, domain, "weworkremotely", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	document := `{"title":"Senior Go Engineer","source":"weworkremotely","url":"https://example.invalid/jobs/1"}`
	// The opportunity and its first revision reference each other, and the constraint between
	// them is deferred, so they have to be inserted in one transaction.
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO opportunity (company_id, source, source_key, external_id, url, title, normalized_title,
		                         remote_status, content_hash, current_revision, availability_status,
		                         description_status, selection_status)
		VALUES ($1,'weworkremotely',$2,$2,'https://example.invalid/jobs/1','Senior Go Engineer','senior go engineer',
		        'remote',$2,1,'open','complete','new')
		RETURNING id`, company.ID, name+"-key").Scan(&opportunityID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO opportunity_revision (opportunity_id, revision, document, content_hash)
		VALUES ($1,1,$2::jsonb,$3)`, opportunityID, document, name+"-hash"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	profileRevisionID := activeProfileFixture(t, db)
	// The assessment has to be written under the current matcher version and against the active
	// profile revision, or the browse views correctly refuse to see it — which is the same rule
	// the real matcher follows.
	//
	// A strong match must state a clear family confidence, and an unrelated one must not; the
	// schema enforces both, so the fixture states what each classification actually requires.
	confidence := "ambiguous"
	if classification == "strong_match" {
		confidence = "clear"
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO opportunity_match (opportunity_id, opportunity_revision, profile_revision_id,
		                               matcher_version, classification, score, family_confidence, result)
		VALUES ($1,1,$2,$5,$3,80,$4,'{}'::jsonb)`,
		opportunityID, profileRevisionID, classification, confidence, opportunity.MatcherVersion); err != nil {
		t.Fatal(err)
	}
	return company.ID, opportunityID
}

// activeProfileFixture creates and activates the minimum profile revision the schema requires.
func activeProfileFixture(t *testing.T, db *DB) int64 {
	t.Helper()
	ctx := context.Background()
	var existing *int64
	if err := db.QueryRow(ctx, `SELECT active_revision_id FROM candidate_profile WHERE id=1`).Scan(&existing); err == nil && existing != nil {
		return *existing
	}
	var revisionID int64
	if err := db.QueryRow(ctx, `
		INSERT INTO candidate_profile_revision (profile_id, revision, document, content_hash, status, approved_at)
		SELECT 1, COALESCE(max(revision),0)+1, '{"identity":"Backend engineer"}'::jsonb,
		       'profile-hash-' || (COALESCE(max(revision),0)+1)::text, 'approved', now()
		  FROM candidate_profile_revision WHERE profile_id=1
		RETURNING id`).Scan(&revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE candidate_profile SET active_revision_id=$1 WHERE id=1`, revisionID); err != nil {
		t.Fatal(err)
	}
	return revisionID
}

// A. A strong match with nothing usable stored is the highest-priority contact-discovery work.
func TestStrongMatchIsEligibleForContactDiscovery(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "Strong Fixture", "strongfixture.example", "strong_match")

	eligible, err := db.ListCompaniesForContactDiscovery(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := findEligible(eligible, companyID)
	if found == nil {
		t.Fatal("a strong match with no contact did not become eligible for contact discovery")
	}
	if found.Priority != PriorityStrongMatch {
		t.Fatalf("a strong match should be priority %d, got %d", PriorityStrongMatch, found.Priority)
	}
	if found.OpportunityID == 0 {
		t.Fatal("the eligible company does not carry the opportunity that made it eligible")
	}
}

// B. An unrelated opportunity is not worth a search merely because its employer publishes no
// address. This is the rule that keeps the budget on work that matters.
func TestUnrelatedOpportunityIsNotQueuedForMissingContact(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "Unrelated Fixture", "unrelatedfixture.example", "unrelated")

	eligible, err := db.ListCompaniesForContactDiscovery(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if findEligible(eligible, companyID) != nil {
		t.Fatal("an unrelated opportunity queued a contact search because its company had no email")
	}
}

// A possible match only becomes eligible once a person selects it.
func TestPossibleMatchIsQueuedOnlyWhenSelected(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, opportunityID := discoveryFixture(t, db, "Possible Fixture", "possiblefixture.example", "possible_match")

	eligible, err := db.ListCompaniesForContactDiscovery(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if findEligible(eligible, companyID) != nil {
		t.Fatal("an unselected possible match queued a contact search on its own")
	}
	if _, err := db.Exec(ctx, `UPDATE opportunity SET selection_status='selected' WHERE id=$1`, opportunityID); err != nil {
		t.Fatal(err)
	}
	eligible, err = db.ListCompaniesForContactDiscovery(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := findEligible(eligible, companyID)
	if found == nil {
		t.Fatal("a manually selected possible match did not become eligible")
	}
	if found.Priority != PrioritySelectedPossible {
		t.Fatalf("a selected possible match should rank below strong matches, got priority %d", found.Priority)
	}
}

// O. A company searched to no result is not asked again on the next tick.
func TestCooldownPreventsImmediateRepeatSearch(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "Cooldown Fixture", "cooldownfixture.example", "strong_match")

	eligible, _ := db.ListCompaniesForContactDiscovery(ctx, 50)
	input := findEligible(eligible, companyID)
	if input == nil {
		t.Fatal("fixture company was not eligible to begin with")
	}
	if _, err := db.EnqueueContactDiscovery(ctx, *input); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteContactDiscovery(ctx, companyID, ContactDiscoveryOutcome{
		State: DiscoveryNoResult, Cooldown: 7 * 24 * time.Hour,
	}); err != nil {
		t.Fatal(err)
	}

	eligible, err := db.ListCompaniesForContactDiscovery(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if findEligible(eligible, companyID) != nil {
		t.Fatal("a company searched to no result was immediately eligible again")
	}

	task, err := db.GetContactDiscoveryTask(ctx, companyID)
	if err != nil {
		t.Fatal(err)
	}
	if task.NextEligibleAt.Before(time.Now().Add(6 * 24 * time.Hour)) {
		t.Fatalf("the no-result cooldown is far shorter than asked: next eligible %s", task.NextEligibleAt)
	}
}

// P. The manual "Find contact" action uses the same queue entry the worker reads. Its one extra
// effect is to clear the cooldown, because an explicit request is new information.
func TestManualRequestUsesTheSameQueueAndClearsCooldown(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "Manual Fixture", "manualfixture.example", "strong_match")

	eligible, _ := db.ListCompaniesForContactDiscovery(ctx, 50)
	input := findEligible(eligible, companyID)
	if _, err := db.EnqueueContactDiscovery(ctx, *input); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteContactDiscovery(ctx, companyID, ContactDiscoveryOutcome{
		State: DiscoveryNoResult, Cooldown: 7 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}

	task, err := db.RequestContactDiscovery(ctx, *input)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != DiscoveryPending {
		t.Fatalf("a manual request did not return the company to the queue: state %q", task.State)
	}
	eligible, err = db.ListCompaniesForContactDiscovery(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if findEligible(eligible, companyID) == nil {
		t.Fatal("a manually requested company was not picked up by the same queue the worker reads")
	}
}

// Two workers must never search the same company at once.
func TestContactDiscoveryClaimIsExclusiveAndExpires(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "Claim Fixture", "claimfixture.example", "strong_match")
	eligible, _ := db.ListCompaniesForContactDiscovery(ctx, 50)
	if _, err := db.EnqueueContactDiscovery(ctx, *findEligible(eligible, companyID)); err != nil {
		t.Fatal(err)
	}

	first, err := db.ClaimContactDiscovery(ctx, companyID, "worker-a", time.Minute)
	if err != nil || !first {
		t.Fatalf("the first claim failed: %v", err)
	}
	second, err := db.ClaimContactDiscovery(ctx, companyID, "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second {
		t.Fatal("two workers claimed the same company's contact search at once")
	}
	if _, err := db.Exec(ctx, `UPDATE contact_discovery_task SET claim_expires_at = now() - interval '1 minute' WHERE company_id=$1`, companyID); err != nil {
		t.Fatal(err)
	}
	recovered, err := db.ClaimContactDiscovery(ctx, companyID, "worker-b", time.Minute)
	if err != nil || !recovered {
		t.Fatal("an expired claim was not recoverable, so a crashed worker would block this company forever")
	}
}

// N. One candidate, several observations — and the observations are all kept.
func TestCandidateKeepsEveryEvidenceRecord(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "Evidence Fixture", "evidencefixture.example", "strong_match")

	base := SaveContactCandidateInput{
		CompanyID: companyID, IdentityKey: "email:jane.doe@evidencefixture.example", IdentityBasis: "email",
		Kind: "person", PersonName: "Jane Doe", RoleTitle: "Technical Recruiter",
		Email: "jane.doe@evidencefixture.example", EmailProvenance: "employer_published",
		Verification: "published", RecipientFunction: "recruiting", EndpointContactable: true,
		Confidence: "high", BestSourceTrust: 80, EmployerOwned: true,
	}
	for _, evidence := range []SaveContactEvidenceInput{
		{SourceType: "employer_team_page", SourceTrust: 80, SourceURL: "https://evidencefixture.example/team",
			Excerpt: "Jane Doe, Technical Recruiter", ExcerptHash: "hash-team", Method: "page_extract_v1", Employer: true},
		{SourceType: "employer_careers_page", SourceTrust: 90, SourceURL: "https://evidencefixture.example/careers",
			Excerpt: "Write to jane.doe@evidencefixture.example", ExcerptHash: "hash-careers", Method: "page_extract_v1", Employer: true},
		{SourceType: "search_snippet", SourceTrust: 10, SourceURL: "https://directory.example/jane",
			Excerpt: "Jane Doe — Technical Recruiter", ExcerptHash: "hash-snippet", Method: "search_snippet_v1"},
	} {
		input := base
		input.Evidence = []SaveContactEvidenceInput{evidence}
		if _, err := db.SaveContactCandidate(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	// Re-observing the same source must refresh, not duplicate.
	repeat := base
	repeat.Evidence = []SaveContactEvidenceInput{{SourceType: "employer_team_page", SourceTrust: 80,
		SourceURL: "https://evidencefixture.example/team", Excerpt: "Jane Doe, Technical Recruiter",
		ExcerptHash: "hash-team", Method: "page_extract_v1", Employer: true}}
	if _, err := db.SaveContactCandidate(ctx, repeat); err != nil {
		t.Fatal(err)
	}

	candidates, err := db.ListContactCandidates(ctx, companyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("one person became %d candidates", len(candidates))
	}
	if len(candidates[0].Evidence) != 3 {
		t.Fatalf("expected three distinct observations, got %d", len(candidates[0].Evidence))
	}
	if candidates[0].Evidence[0].Trust != 90 {
		t.Fatalf("evidence is not ordered strongest first: %d", candidates[0].Evidence[0].Trust)
	}
}

// A candidate with no evidence is a claim with no source, which this pipeline does not create.
func TestCandidateWithoutEvidenceIsRefused(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "NoEvidence Fixture", "noevidencefixture.example", "strong_match")
	_, err := db.SaveContactCandidate(ctx, SaveContactCandidateInput{
		CompanyID: companyID, IdentityKey: "email:x@noevidencefixture.example", IdentityBasis: "email",
		Kind: "mailbox", Email: "x@noevidencefixture.example", EmailProvenance: "employer_published",
	})
	if err == nil {
		t.Fatal("a candidate with no supporting evidence was stored")
	}
}

// H. An inferred address cannot be stored as published and cannot be promoted. Both halves are
// enforced by the database, not only by the caller.
func TestInferredAddressCannotBePublishedOrPromoted(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "Inferred Fixture", "inferredfixture.example", "strong_match")

	if _, err := db.Exec(ctx, `
		INSERT INTO contact_candidate (company_id, identity_key, identity_basis, kind, email, email_provenance, verification)
		VALUES ($1,'email:jane.doe@inferredfixture.example','email','person','jane.doe@inferredfixture.example',
		        'inferred_pattern','published')`, companyID); err == nil {
		t.Fatal("an inferred address was stored as published")
	}

	candidateID, err := db.SaveContactCandidate(ctx, SaveContactCandidateInput{
		CompanyID: companyID, IdentityKey: "email:jane.doe@inferredfixture.example", IdentityBasis: "email",
		Kind: "person", PersonName: "Jane Doe", Email: "jane.doe@inferredfixture.example",
		EmailProvenance: "inferred_pattern", Verification: "inferred", Confidence: "low",
		Status: "review_required", EndpointContactable: true, BestSourceTrust: 30,
		Evidence: []SaveContactEvidenceInput{{SourceType: "third_party", SourceTrust: 30,
			Excerpt: "Address not published. Derived from the observed format.", ExcerptHash: "hash-inferred",
			Method: "pattern_inference_v1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PromoteContactCandidate(ctx, candidateID, 3); err == nil {
		t.Fatal("an inferred address was promoted into the contact table")
	}
}

// A promoted candidate becomes a real contact and keeps the link back to its evidence.
func TestPromotionCreatesAContactLinkedToItsEvidence(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "Promote Fixture", "promotefixture.example", "strong_match")

	candidateID, err := db.SaveContactCandidate(ctx, SaveContactCandidateInput{
		CompanyID: companyID, IdentityKey: "email:jane.doe@promotefixture.example", IdentityBasis: "email",
		Kind: "person", PersonName: "Jane Doe", RoleTitle: "Technical Recruiter",
		Email: "jane.doe@promotefixture.example", EmailProvenance: "employer_published",
		Verification: "published", RecipientFunction: "recruiting", EndpointContactable: true,
		Confidence: "high", BestSourceTrust: 90, EmployerOwned: true,
		Evidence: []SaveContactEvidenceInput{{SourceType: "employer_careers_page", SourceTrust: 90,
			SourceURL:   "https://promotefixture.example/careers",
			Excerpt:     "Jane Doe, Technical Recruiter — jane.doe@promotefixture.example",
			ExcerptHash: "hash-promote", Method: "page_extract_v1", Employer: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	contact, err := db.PromoteContactCandidate(ctx, candidateID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if contact.PersonName == nil || *contact.PersonName != "Jane Doe" {
		t.Fatal("the promoted contact lost the person's name")
	}
	candidates, err := db.ListContactCandidates(ctx, companyID)
	if err != nil {
		t.Fatal(err)
	}
	if candidates[0].Status != "promoted" || candidates[0].PromotedContactID == nil || *candidates[0].PromotedContactID != contact.ID {
		t.Fatalf("the candidate does not name the contact it became: %+v", candidates[0])
	}
}

// 26. Promoting a named recruiter does not remove the generic mailbox that was already there.
func TestPromotionDoesNotRemoveAnExistingGenericMailbox(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "Retain Fixture", "retainfixture.example", "strong_match")
	if _, err := db.CreateContact(ctx, companyID, "email", "info@retainfixture.example", nil, nil, 1); err != nil {
		t.Fatal(err)
	}
	candidateID, err := db.SaveContactCandidate(ctx, SaveContactCandidateInput{
		CompanyID: companyID, IdentityKey: "email:jane.doe@retainfixture.example", IdentityBasis: "email",
		Kind: "person", PersonName: "Jane Doe", RoleTitle: "Technical Recruiter",
		Email: "jane.doe@retainfixture.example", EmailProvenance: "employer_published",
		Verification: "published", RecipientFunction: "recruiting", EndpointContactable: true,
		Confidence: "high", BestSourceTrust: 90, EmployerOwned: true,
		Evidence: []SaveContactEvidenceInput{{SourceType: "employer_careers_page", SourceTrust: 90,
			Excerpt: "Jane Doe — jane.doe@retainfixture.example", ExcerptHash: "hash-retain",
			Method: "page_extract_v1", Employer: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PromoteContactCandidate(ctx, candidateID, 3); err != nil {
		t.Fatal(err)
	}
	contacts, err := db.GetContactsByCompany(ctx, companyID)
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, c := range contacts {
		if c.Value == "info@retainfixture.example" {
			kept = true
		}
	}
	if !kept {
		t.Fatal("promoting a named recruiter deleted the company's generic mailbox")
	}
}

// Evidence is the provenance record. It is appended to, never rewritten or deleted.
func TestStoredEvidenceIsImmutable(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "Immutable Fixture", "immutablefixture.example", "strong_match")
	candidateID, err := db.SaveContactCandidate(ctx, SaveContactCandidateInput{
		CompanyID: companyID, IdentityKey: "email:a@immutablefixture.example", IdentityBasis: "email",
		Kind: "mailbox", Email: "a@immutablefixture.example", EmailProvenance: "employer_published",
		Verification: "published", BestSourceTrust: 70,
		Evidence: []SaveContactEvidenceInput{{SourceType: "employer_page", SourceTrust: 70,
			Excerpt: "a@immutablefixture.example", ExcerptHash: "hash-immutable", Method: "page_extract_v1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE contact_candidate_evidence SET excerpt='rewritten' WHERE candidate_id=$1`, candidateID); err == nil {
		t.Fatal("a stored source excerpt was rewritten")
	}
	if _, err := db.Exec(ctx, `DELETE FROM contact_candidate_evidence WHERE candidate_id=$1`, candidateID); err == nil {
		t.Fatal("a stored source excerpt was deleted")
	}
	if _, err := db.Exec(ctx, `UPDATE contact_candidate SET email='other@immutablefixture.example' WHERE id=$1`, candidateID); err == nil {
		t.Fatal("a candidate's address was rewritten in place")
	}
}

func findEligible(list []DiscoveryCandidateInput, companyID int64) *DiscoveryCandidateInput {
	for i := range list {
		if list[i].CompanyID == companyID {
			return &list[i]
		}
	}
	return nil
}

// S, seen from the browse layer. A discovered employer-published apply route must count as a
// destination in the opportunity filters too, or the dashboard keeps reporting a company as
// blocked after the pipeline has resolved it.
func TestDiscoveredApplicationRouteCountsInTheOpportunityFilters(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "Route Filter Fixture", "routefilterfixture.example", "strong_match")

	missing := OpportunityListFilter{Fit: "strong_match", Recipient: "missing"}
	before, err := db.CountOpportunityList(ctx, missing)
	if err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("a strong match with no destination was not reported as missing one")
	}

	if _, err := db.SaveContactCandidate(ctx, SaveContactCandidateInput{
		CompanyID: companyID, IdentityKey: "route:jobs.ashbyhq.com/routefilter", IdentityBasis: "route_url",
		Kind: "application_url", ApplicationURL: "https://jobs.ashbyhq.com/routefilter", ATSProvider: "Ashby",
		Verification: "published", Confidence: "high", BestSourceTrust: 90, EmployerOwned: true,
		Evidence: []SaveContactEvidenceInput{{SourceType: "employer_careers_page", SourceTrust: 90,
			SourceURL:   "https://routefilterfixture.example/careers",
			Excerpt:     "Browse our roles [link target: https://jobs.ashbyhq.com/routefilter]",
			ExcerptHash: "hash-route", Method: "route_extract_v1", Employer: true}},
	}); err != nil {
		t.Fatal(err)
	}

	after, err := db.CountOpportunityList(ctx, missing)
	if err != nil {
		t.Fatal(err)
	}
	if after != before-1 {
		t.Fatalf("a discovered apply route did not clear the company from the missing-destination filter (%d then %d)", before, after)
	}

	acceptable, err := db.CountOpportunityList(ctx, OpportunityListFilter{Fit: "strong_match", Recipient: "acceptable"})
	if err != nil {
		t.Fatal(err)
	}
	if acceptable == 0 {
		t.Fatal("the company with a published apply route is in neither the missing nor the acceptable list")
	}
}

// The contact-search filter finds the companies waiting on a search.
func TestContactSearchStateFilterSelectsQueuedCompanies(t *testing.T) {
	db := phase3DB(t)
	ctx := context.Background()
	companyID, _ := discoveryFixture(t, db, "Queue Filter Fixture", "queuefilterfixture.example", "strong_match")

	if count, err := db.CountOpportunityList(ctx, OpportunityListFilter{ContactSearch: "pending"}); err != nil {
		t.Fatal(err)
	} else if count != 0 {
		t.Fatalf("a company that was never queued matched contact_search=pending: %d", count)
	}

	eligible, err := db.ListCompaniesForContactDiscovery(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnqueueContactDiscovery(ctx, *findEligible(eligible, companyID)); err != nil {
		t.Fatal(err)
	}
	count, err := db.CountOpportunityList(ctx, OpportunityListFilter{ContactSearch: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("a queued company did not appear under contact_search=pending")
	}
}
