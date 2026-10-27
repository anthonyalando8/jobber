package store

import (
	"context"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/opportunity"
)

func TestLeadCategoriesAndAgencyDetectionReadOnlyStoredEvidence(t *testing.T) {
	db := phase2DB(t)
	ctx := context.Background()
	// A prospect added by hand, with nothing published: a general lead.
	prospect, err := db.CreateCompany(ctx, "Northwind Studio", "northwind-studio.example", "manual", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	// A company with a contract listing.
	contract := accessFixture(t, db, "Freelance Django Developer", "Worldwide", "", time.Now())
	// A company with an ordinary listing whose description says it is an agency.
	d := opportunity.DiscoveredOpportunity{Source: "remotive", CompanyName: "Plainname Ltd", DomainCandidate: "plainname.example",
		Title: "Backend Engineer", URL: "https://remotive.com/jobs/plain", Description: "We are a digital agency building stores for clients.",
		DescriptionStatus: "complete", RemoteStatus: "remote", EmploymentType: "Full-time"}
	if _, err := db.IngestOpportunity(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RefreshOpportunityAccess(ctx, 50); err != nil {
		t.Fatal(err)
	}
	marked, err := db.ClassifyBusinessSegments(ctx)
	if err != nil || marked != 2 {
		t.Fatalf("expected the studio and the self-described agency to be marked, got %d %v", marked, err)
	}
	// An operator's own classification is never overwritten.
	if err := db.SetBusinessSegment(ctx, prospect.ID, "sme"); err != nil {
		t.Fatal(err)
	}
	if again, _ := db.ClassifyBusinessSegments(ctx); again != 0 {
		t.Fatalf("re-classification changed %d rows", again)
	}
	if lead, business, err := db.CompanyLead(ctx, prospect.ID); err != nil || lead != "prospect" || business != "sme" {
		t.Fatalf("prospect: %s %s %v", lead, business, err)
	}
	var contractCompany int64
	if err := db.QueryRow(ctx, `SELECT company_id FROM opportunity WHERE id=$1`, contract).Scan(&contractCompany); err != nil {
		t.Fatal(err)
	}
	if lead, _, _ := db.CompanyLead(ctx, contractCompany); lead != "contract_work" {
		t.Fatalf("contract listing company: %s", lead)
	}
	agencies, err := db.ListCompanies(ctx, CompanyFilter{Business: "agency"}, 10, 0)
	if err != nil || len(agencies) != 1 || agencies[0].Name != "Plainname Ltd" || agencies[0].LeadCategory != "hiring" {
		t.Fatalf("agency filter: %+v %v", agencies, err)
	}
	all, err := db.ListCompanies(ctx, CompanyFilter{}, 10, 0)
	if err != nil || len(all) != 3 || all[0].LeadCategory != "contract_work" || all[2].LeadCategory != "prospect" {
		t.Fatalf("default order should lead with contract work and end with general prospects: %+v %v", all, err)
	}
}
