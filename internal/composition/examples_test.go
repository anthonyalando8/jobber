package composition

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/profile"
)

// These are authored deterministic provider fixtures, never claims about Anthony
// or a measurement of a live model's writing/review quality.
func TestRepresentativeOfflineDrafts(t *testing.T) {
	var report strings.Builder
	report.WriteString("# Phase 4 synthetic offline drafts\n\nThese outputs come from deterministic provider fixtures in `internal/composition/examples_test.go`. All candidate, employer, role and project facts are synthetic. They are not Anthony's profile or live model results. The real deterministic validator runs on every message; the separate fixture reviewer returns a scripted clean response to exercise the review contract. This does not establish real semantic-review accuracy.\n\n")
	for _, kind := range []string{"Backend application", "Full-Stack application", "Speculative enquiry from Marketing Manager", "Backend proof from payments-domain work", "Genuine payment-engineering application"} {
		t.Run(kind, func(t *testing.T) {
			s := syntheticSnapshot(plan.SpecificJobApplication)
			s.Sources = []Source{{ID: "opportunity:1", Kind: "job_description", Text: "Synthetic Studio seeks a Backend Engineer to build Go and PostgreSQL APIs with relational schemas and transaction handling."}}
			e := syntheticEmail(s)
			if kind == "Full-Stack application" {
				s.AdvertisedTitle = "Full-Stack Developer"
				s.Match.OriginalTitle = s.AdvertisedTitle
				s.Technologies = append(s.Technologies, profile.Technology{ID: "react", Name: "React"}, profile.Technology{ID: "next", Name: "Next.js"})
				s.AllowedClaims = append(s.AllowedClaims, Claim{ID: "frontend", Kind: "candidate", Text: "Built React and Next.js interfaces connected to backend APIs"})
				s.Sources[0].Text = "Synthetic Studio seeks a Full-Stack Developer using Go, PostgreSQL, React and Next.js."
				e.Subject = "Application: Full-Stack Developer"
				e.Body = `Hello team,

I'd like to apply for the Full-Stack Developer role. I'm a full-stack developer with backend engineering as my main strength, and the role's combination of backend APIs and connected interfaces fits the work I can discuss.

In Synthetic Ledger, I owned Go and PostgreSQL backend implementation, including API contracts, relational schemas and transaction boundaries. I wrote automated tests around that behavior. I have also built React and Next.js interfaces connected to backend APIs, so I can explain work on both sides of the interface and where my backend experience is strongest.

I'd be happy to share my CV and walk through the implementation. Would you consider my application?`
				e.UsedClaimIDs = append(e.UsedClaimIDs, "frontend")
			}
			if kind == "Speculative enquiry from Marketing Manager" {
				s = syntheticSnapshot(plan.SpeculativeJobEnquiry)
				s.ProvenanceTitle = "Marketing Manager"
				s.Sources = []Source{{ID: "evidence:1", Kind: "company_service", Text: "Synthetic Studio designs and builds business software."}}
				e = syntheticEmail(s)
				e.Subject = "Backend and full-stack engineering opportunities"
				e.Body = `Hello team,

I'm reaching out to ask about backend or full-stack engineering opportunities at Synthetic Studio. Your work building business software is the reason for contacting you; my main strength is backend implementation within full-stack development.

I build Go and PostgreSQL APIs, including relational schemas, validation and transaction boundaries. In Synthetic Ledger, I owned the backend implementation and its automated tests. I can walk through those decisions and the parts I implemented to give your engineering team a concrete view of my work.

I'd be happy to share my CV. Is there an engineering contact I could speak with about current or future opportunities?`
			}
			if kind == "Backend proof from payments-domain work" || kind == "Genuine payment-engineering application" {
				s.Projects[0].Description = "Payments-domain Go and PostgreSQL backend with transaction and retry handling"
				s.AllowedClaims = append(s.AllowedClaims, Claim{ID: "payment-domain", Kind: "candidate", Text: s.Projects[0].Description})
				e.UsedClaimIDs = append(e.UsedClaimIDs, "payment-domain")
				e.Body = strings.Replace(e.Body, "In Synthetic Ledger,", "In Synthetic Ledger, a payments-domain project,", 1)
			}
			if kind == "Genuine payment-engineering application" {
				s.AdvertisedTitle = "Payments Backend Engineer"
				s.Match.OriginalTitle = s.AdvertisedTitle
				s.ForbiddenClaims = nil
				s.Sources[0].Text = "Synthetic Studio seeks a Payments Backend Engineer for Go/PostgreSQL payment transaction workflows, API contracts and retries. No defect or existing problem is stated."
				e.Subject = "Application: Payments Backend Engineer"
				e.Body = strings.ReplaceAll(e.Body, "Backend Engineer", "Payments Backend Engineer")
			}
			s.InputHash = s.Hash()
			writer := writerFunc(func(_ context.Context, r WriteRequest) (Email, Metadata, error) {
				if r.Snapshot.InputHash != s.InputHash {
					t.Fatal("plan changed")
				}
				return e, Metadata{Provider: "synthetic", Model: "fixture-writer"}, nil
			})
			result, err := (Engine{Composer: writer, Reviewer: reviewerFunc(cleanReview)}).Run(context.Background(), s, nil)
			if err != nil || result.Status != "reviewed" || len(result.Attempts) != 1 {
				t.Fatalf("fixture failed: %v %+v", err, result)
			}
			forbidden := []string{"unapproved technologies, experience duration, metrics, availability, achievements, invented vacancies, names or attachments"}
			for _, claim := range s.ForbiddenClaims {
				forbidden = append(forbidden, claim.Text)
			}
			if kind == "Genuine payment-engineering application" {
				forbidden = append(forbidden, "A payment role is not evidence that the employer's payments are broken")
			}
			fmt.Fprintf(&report, "## %s\n\nPlan: `%s`; recipient `jobs@synthetic.example`; advertised role `%s`; provenance-only title `%s`. Synthetic profile/plan revision 1. Possible engineering fit remains subject to human review.\n\nSelected capabilities: backend API development%s. Selected project: Synthetic Ledger.\n\nForbidden: %s.\n\n**Subject:** %s\n\n%s\n\nDeterministic result: no block/review findings. Independent synthetic reviewer: complete, scripted pass, no findings. Overall: content reviewed; sending disabled and no human send approval.\n\n", kind, s.Intent, s.AdvertisedTitle, s.ProvenanceTitle, map[bool]string{true: "; approved React/Next.js interface experience"}[kind == "Full-Stack application"], strings.Join(forbidden, "; "), result.Email.Subject, result.Email.Body)
		})
	}
	if destination := os.Getenv("PHASE4_SYNTHETIC_REPORT"); destination != "" {
		if err := os.WriteFile(destination, []byte(report.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
