package store

import (
	"context"
	"os"
	"testing"
)

// testDB connects to DATABASE_URL for a real integration test and cleans up everything it
// creates. Skips gracefully when no dev database is configured.
func testDB(t *testing.T) *DB {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set — skipping store integration test")
	}
	db, err := NewDB(context.Background(), url)
	if err != nil {
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestNextPendingDraft_ExcludesBlocked guards the review.md requirement that a BLOCKED draft
// must never be offered to a human as if it were a normal thing to send — review.md: "blocked
// items should remain visible in logs/history", not in the actionable review queue.
func TestNextPendingDraft_ExcludesBlocked(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	company, err := db.CreateCompany(ctx, "Test Co "+t.Name(), "test-"+t.Name()+".example", "manual", "", "manual")
	if err != nil {
		t.Fatalf("CreateCompany() failed: %v", err)
	}
	t.Cleanup(func() { db.Exec(ctx, `DELETE FROM company WHERE id = $1`, company.ID) })

	dft, err := db.CreateDraft(ctx, company.ID, nil, "email", "subject", "body", "", "pending")
	if err != nil {
		t.Fatalf("CreateDraft() failed: %v", err)
	}

	if err := db.UpdateDraftDecision(ctx, dft.ID, "blocked", "test", 0); err != nil {
		t.Fatalf("UpdateDraftDecision() failed: %v", err)
	}

	next, err := db.NextPendingDraft(ctx, []int64{})
	if err != nil {
		t.Fatalf("NextPendingDraft() failed: %v", err)
	}
	if next != nil && next.ID == dft.ID {
		t.Errorf("NextPendingDraft() returned a BLOCKED draft (id=%d) — it must be excluded from the review queue", dft.ID)
	}
}

// TestNextPendingDraft_ReturnsReviewRequiredDraft is the companion case: a draft that isn't
// blocked must still be offered normally, so the exclusion above isn't accidentally too broad.
func TestNextPendingDraft_ReturnsReviewRequiredDraft(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	company, err := db.CreateCompany(ctx, "Test Co "+t.Name(), "test-"+t.Name()+".example", "manual", "", "manual")
	if err != nil {
		t.Fatalf("CreateCompany() failed: %v", err)
	}
	// Give it a very high score so it sorts first regardless of whatever else is in the dev DB.
	if err := db.UpdateCompanyScore(ctx, company.ID, 100, "sme", nil); err != nil {
		t.Fatalf("UpdateCompanyScore() failed: %v", err)
	}
	t.Cleanup(func() { db.Exec(ctx, `DELETE FROM company WHERE id = $1`, company.ID) })

	dft, err := db.CreateDraft(ctx, company.ID, nil, "email", "subject", "body", "", "pending")
	if err != nil {
		t.Fatalf("CreateDraft() failed: %v", err)
	}
	if err := db.UpdateDraftDecision(ctx, dft.ID, "review_required", "test", 100); err != nil {
		t.Fatalf("UpdateDraftDecision() failed: %v", err)
	}

	next, err := db.NextPendingDraft(ctx, []int64{})
	if err != nil {
		t.Fatalf("NextPendingDraft() failed: %v", err)
	}
	if next == nil || next.ID != dft.ID {
		gotID := int64(-1)
		if next != nil {
			gotID = next.ID
		}
		t.Errorf("NextPendingDraft() = draft id %d, want the review_required draft (id=%d, score=100)", gotID, dft.ID)
	}
}

// TestListCompaniesForDrafting_RequiresEmailContact guards review.md §7/§8: a company with no
// way to actually reach it must never be offered to the AI as a draft candidate.
func TestListCompaniesForDrafting_RequiresEmailContact(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	company, err := db.CreateCompany(ctx, "Test Co "+t.Name(), "test-"+t.Name()+".example", "manual", "", "manual")
	if err != nil {
		t.Fatalf("CreateCompany() failed: %v", err)
	}
	t.Cleanup(func() { db.Exec(ctx, `DELETE FROM company WHERE id = $1`, company.ID) })

	if err := db.UpdateCompanyStatus(ctx, company.ID, "enriched"); err != nil {
		t.Fatalf("UpdateCompanyStatus() failed: %v", err)
	}
	if err := db.UpdateCompanyScore(ctx, company.ID, 100, "sme", nil); err != nil {
		t.Fatalf("UpdateCompanyScore() failed: %v", err)
	}

	candidates, err := db.ListCompaniesForDrafting(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("ListCompaniesForDrafting() failed: %v", err)
	}
	for _, c := range candidates {
		if c.ID == company.ID {
			t.Errorf("ListCompaniesForDrafting() included a company with no email contact (id=%d) — it can never be sent to", company.ID)
		}
	}

	// Now give it an email contact and confirm it DOES become eligible — proves the exclusion
	// above is actually about the contact, not some other reason this test's fixture might
	// accidentally be failing on.
	if _, err := db.CreateContact(ctx, company.ID, "email", "test@example.com", nil, nil, 1); err != nil {
		t.Fatalf("CreateContact() failed: %v", err)
	}
	candidates, err = db.ListCompaniesForDrafting(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("ListCompaniesForDrafting() failed: %v", err)
	}
	found := false
	for _, c := range candidates {
		if c.ID == company.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("ListCompaniesForDrafting() excluded company id=%d even after it got an email contact", company.ID)
	}
}
