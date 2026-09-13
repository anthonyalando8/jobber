package composition

import (
	"context"
	"testing"

	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/profile"
)

// flawed is one deliberately defective complete email from the Phase 4 review brief, together
// with what the deterministic validator is expected to do about it on its own.
type flawed struct {
	name string
	// wantCode is the deterministic finding this defect must produce, or "" when the defect is
	// genuinely semantic and only the reviewing model can see it.
	wantCode string
	semantic string
	build    func(Snapshot) (Snapshot, Email)
}

func replaceBody(s Snapshot, body string) Email {
	e := syntheticEmail(s)
	e.Body = body
	return e
}

// adversarialCases is the shared corpus. The deterministic test below asserts what the
// validator catches without any model; the opt-in live evaluation in eval_test.go sends the
// same corpus through the real reviewer.
func adversarialCases() []flawed {
	return []flawed{
		{
			name: "invented skill", wantCode: "unapproved_technology",
			build: func(s Snapshot) (Snapshot, Email) {
				return s, replaceBody(s, `Hello team,

I'd like to apply for the Backend Engineer role. I have extensive Kubernetes experience and run large clusters day to day.

In Synthetic Ledger I owned the backend implementation, including API contracts and transaction boundaries.

Would you consider my application for the role?`)
			},
		},
		{
			name: "wrong advertised role", wantCode: "application_title_mismatch",
			build: func(s Snapshot) (Snapshot, Email) {
				e := replaceBody(s, `Hello team,

I am applying for the DevOps Engineer role. My background is backend implementation in Go and PostgreSQL.

In Synthetic Ledger I owned the backend implementation and its automated tests.

Would you consider my application?`)
				e.Subject = "Application: DevOps Engineer"
				return s, e
			},
		},
		{
			name: "unrelated provenance leakage", wantCode: "unrelated_title_leakage",
			build: func(_ Snapshot) (Snapshot, Email) {
				s := syntheticSnapshot(plan.SpeculativeJobEnquiry)
				s.ProvenanceTitle = "Marketing Manager"
				s.InputHash = s.Hash()
				e := syntheticEmail(s)
				e.Subject = "Engineering opportunities"
				e.Body = `Hello team,

I am interested in the Marketing Manager vacancy you advertised, and wondered whether there is engineering work alongside it.

I build Go and PostgreSQL APIs with transaction boundaries.

Is there an engineering contact I could speak with?`
				return s, e
			},
		},
		{
			name: "payment leakage", wantCode: "recipient_payment_problem",
			build: func(s Snapshot) (Snapshot, Email) {
				return s, replaceBody(s, `Hello team,

I'd like to apply for the Backend Engineer role. I noticed your payment reconciliation may need improvement, and I could help you fix that.

In Synthetic Ledger I owned the backend implementation and its transaction boundaries.

Would you consider my application for the role?`)
			},
		},
		{
			name: "service and application confusion", wantCode: "intent_contradiction",
			build: func(_ Snapshot) (Snapshot, Email) {
				s := syntheticSnapshot(plan.ServiceOutreach)
				s.InputHash = s.Hash()
				e := syntheticEmail(s)
				e.Subject = "Backend engineering support"
				e.Body = `Hello team,

I'd like to apply for a position on your engineering team.

I build Go and PostgreSQL APIs with relational schemas and transaction boundaries.

Would you be open to a short call?`
				return s, e
			},
		},
		{
			name: "attachment hallucination", wantCode: "fabricated_attachment",
			build: func(s Snapshot) (Snapshot, Email) {
				return s, replaceBody(s, `Hello team,

I'd like to apply for the Backend Engineer role. I've attached my CV for your review.

In Synthetic Ledger I owned the backend implementation, including API contracts and transaction boundaries.

Would you consider my application for the role?`)
			},
		},
		{
			name: "robotic AI prose", wantCode: "formulaic_language",
			build: func(s Snapshot) (Snapshot, Email) {
				return s, replaceBody(s, `Hello team,

I am writing to express my keen interest in this exciting opportunity at your innovative company. I believe my unique blend of skills would make me an excellent addition to your dynamic team.

I'd like to apply for the Backend Engineer role. In Synthetic Ledger I owned the backend implementation.

Would you consider my application for the role?`)
			},
		},
		// The remaining defects are factually accurate or structurally poor rather than
		// contract violations. No deterministic rule can see them; they are the reason the
		// semantic reviewer exists at all.
		{
			name: "payment project identity leakage", semantic: "capability or project relevance",
			build: func(s Snapshot) (Snapshot, Email) {
				return s, replaceBody(s, `Hello team,

I'd like to apply for the Backend Engineer role.

Synthetic Ledger is a payments platform. I worked on payment capture, payment settlement and the payment ledger. Most of my time went into payment state transitions, payment retries and the payment audit trail. The payment domain shaped every part of that system, and I can talk at length about payment flows.

Would you consider my application for the role?`)
			},
		},
		{
			name: "invented company praise", semantic: "unsupported company assertion",
			build: func(s Snapshot) (Snapshot, Email) {
				return s, replaceBody(s, `Hello team,

I'd like to apply for the Backend Engineer role. I've followed your impressive engineering culture and rapid growth for a while now, and your reputation for technical excellence is well deserved.

In Synthetic Ledger I owned the backend implementation, including API contracts and transaction boundaries.

Would you consider my application for the role?`)
			},
		},
		{
			name: "repetition", semantic: "repetition",
			build: func(s Snapshot) (Snapshot, Email) {
				return s, replaceBody(s, `Hello team,

I'd like to apply for the Backend Engineer role. I have backend experience building APIs in Go and PostgreSQL.

My backend experience covers building APIs in Go and PostgreSQL. I have worked on backend APIs with Go and PostgreSQL throughout.

Backend API work in Go and PostgreSQL is where my experience sits, and I have built backend APIs in Go and PostgreSQL.

Would you consider my application for the role?`)
			},
		},
		{
			name: "weak call to action", semantic: "weak CTA",
			build: func(s Snapshot) (Snapshot, Email) {
				return s, replaceBody(s, `Hello team,

I'd like to apply for the Backend Engineer role. The emphasis on Go, PostgreSQL and APIs fits the backend work I do.

In Synthetic Ledger I owned the backend implementation, including API contracts, relational schemas and transaction boundaries. I wrote the automated tests around that behaviour and handled retries deliberately.

Anyway, that is all from me. Thanks for reading this message.`)
			},
		},
	}
}

// The deterministic validator must catch every defect that is a contract violation, with no
// model involved at all. This is the floor beneath semantic review.
func TestDeterministicValidatorCatchesContractViolations(t *testing.T) {
	base := syntheticSnapshot(plan.SpecificJobApplication)
	deterministic, semantic := 0, 0
	for _, tc := range adversarialCases() {
		t.Run(tc.name, func(t *testing.T) {
			s, e := tc.build(base)
			findings := Validate(s, e)
			if tc.wantCode == "" {
				semantic++
				// A semantic defect need not be silent deterministically, but it must not be
				// mistaken for a passing email either: it is the reviewer's job.
				t.Logf("no deterministic rule covers %q (%s); deterministic findings: %v",
					tc.name, tc.semantic, codes(findings))
				return
			}
			deterministic++
			if !hasCode(findings, tc.wantCode) {
				t.Fatalf("%q: missing %s; got %v", tc.name, tc.wantCode, codes(findings))
			}
			if tc.wantCode != "formulaic_language" && !HasBlock(findings) {
				t.Fatalf("%q: contract violation must block, got %v", tc.name, codes(findings))
			}
		})
	}
	t.Logf("deterministic coverage: %d of %d adversarial cases; %d require semantic review",
		deterministic, deterministic+semantic, semantic)
}

// A semantic reviewer that wrongly returns a clean pass must never be able to release an email
// that deterministic validation rejected.
func TestCleanReviewCannotOverrideDeterministicBlock(t *testing.T) {
	for _, tc := range adversarialCases() {
		if tc.wantCode == "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			s, e := tc.build(syntheticSnapshot(plan.SpecificJobApplication))
			// The composer keeps returning the same defective email; the reviewer keeps
			// insisting it is perfect.
			engine := Engine{
				Composer: writerFunc(func(context.Context, WriteRequest) (Email, Metadata, error) {
					return e, Metadata{Provider: "synthetic", Model: "fixture-writer"}, nil
				}),
				Reviewer: reviewerFunc(cleanReview),
			}
			result, err := engine.Run(context.Background(), s, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "review_required" {
				t.Fatalf("%q was released as %q despite a deterministic finding", tc.name, result.Status)
			}
			last := result.Attempts[len(result.Attempts)-1]
			if !hasCode(last.Validation, tc.wantCode) {
				t.Fatalf("deterministic finding was lost after review: %v", codes(last.Validation))
			}
			if last.Review == nil || !last.Review.Passed {
				t.Fatal("this case requires the reviewer to have wrongly passed")
			}
		})
	}
}

// Stale pinned inputs are refused even when the reviewer is clean, because the snapshot no
// longer hashes to what was approved.
func TestStaleSnapshotBlocksEvenWithCleanReview(t *testing.T) {
	s := syntheticSnapshot(plan.SpecificJobApplication)
	e := syntheticEmail(s)
	s.ProfileRevisionID = 99 // an input changed after the snapshot was sealed
	findings := Validate(s, e)
	if !hasCode(findings, "invalid_snapshot") {
		t.Fatalf("a tampered snapshot must be refused, got %v", codes(findings))
	}
	engine := Engine{
		Composer: writerFunc(func(context.Context, WriteRequest) (Email, Metadata, error) {
			return e, Metadata{}, nil
		}),
		Reviewer: reviewerFunc(cleanReview),
	}
	if _, err := engine.Run(context.Background(), s, nil); err == nil {
		t.Fatal("the engine must refuse an obsolete snapshot before composing")
	}
}

// The bounded repair loop must feed findings back, stay inside the original factual envelope,
// and stop at the limit.
func TestRepairLoopFixtures(t *testing.T) {
	cases := []struct {
		name       string
		firstBody  string
		repairBody string
		wantCode   string
		wantStatus string
		repairAdds bool
	}{
		{
			name:     "robotic first draft is repaired",
			wantCode: "formulaic_language",
			firstBody: `Hello team,

I am writing to express my keen interest in this exciting opportunity. I believe my unique blend of skills suits your dynamic team.

I'd like to apply for the Backend Engineer role.

Would you consider my application for the role?`,
			repairBody: backendBody,
			wantStatus: "reviewed",
		},
		{
			name:     "unsupported payment diagnosis is repaired",
			wantCode: "recipient_payment_problem",
			firstBody: `Hello team,

I'd like to apply for the Backend Engineer role. Your payment reconciliation looks like it is failing and I can help you fix it.

In Synthetic Ledger I owned the backend implementation and its transaction boundaries.

Would you consider my application for the role?`,
			repairBody: backendBody,
			wantStatus: "reviewed",
		},
		{
			name:     "unapproved technology is repaired",
			wantCode: "unapproved_technology",
			firstBody: `Hello team,

I'd like to apply for the Backend Engineer role. I have deep Kubernetes and Terraform experience.

In Synthetic Ledger I owned the backend implementation and its transaction boundaries.

Would you consider my application for the role?`,
			repairBody: backendBody,
			wantStatus: "reviewed",
		},
		{
			name:     "repair that introduces a new unsupported fact is blocked",
			wantCode: "formulaic_language",
			firstBody: `Hello team,

I am writing to express my keen interest in this exciting opportunity at your innovative company.

I'd like to apply for the Backend Engineer role.

Would you consider my application for the role?`,
			// The "repair" fixes the tone but smuggles in a technology the plan never allowed.
			repairBody: `Hello team,

I'd like to apply for the Backend Engineer role. The emphasis on Go, PostgreSQL and APIs fits the backend work I do, and I also bring several years of AWS and Kubernetes operations experience.

In Synthetic Ledger I owned the backend implementation, including API contracts, relational schemas and transaction boundaries.

Would you consider my application for the role?`,
			wantStatus: "review_required",
			repairAdds: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := syntheticSnapshot(plan.SpecificJobApplication)
			var handedBack []Finding
			calls := 0
			engine := Engine{
				Composer: writerFunc(func(_ context.Context, r WriteRequest) (Email, Metadata, error) {
					calls++
					e := syntheticEmail(s)
					if calls == 1 {
						e.Body = tc.firstBody
						return e, Metadata{Provider: "synthetic", Model: "fixture-writer"}, nil
					}
					if handedBack == nil {
						handedBack = r.Findings
					}
					if r.Previous == nil {
						t.Fatal("repair must receive the previous email")
					}
					if len(r.Snapshot.AllowedClaims) != len(s.AllowedClaims) {
						t.Fatal("repair must not widen the factual envelope")
					}
					e.Body = tc.repairBody
					return e, Metadata{Provider: "synthetic", Model: "fixture-writer"}, nil
				}),
				Reviewer: reviewerFunc(cleanReview),
			}
			result, err := engine.Run(context.Background(), s, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (attempts %d)", result.Status, tc.wantStatus, len(result.Attempts))
			}
			if len(result.Attempts) < 2 {
				t.Fatalf("expected a repair attempt, got %d", len(result.Attempts))
			}
			if !hasCode(result.Attempts[0].Validation, tc.wantCode) {
				t.Fatalf("first draft should carry %s, got %v", tc.wantCode, codes(result.Attempts[0].Validation))
			}
			if !hasCode(handedBack, tc.wantCode) {
				t.Fatalf("the repair request did not receive the finding: %v", codes(handedBack))
			}
			last := result.Attempts[len(result.Attempts)-1]
			if tc.repairAdds {
				if !hasCode(last.Validation, "unapproved_technology") {
					t.Fatalf("a repair that adds an unsupported fact must be blocked, got %v", codes(last.Validation))
				}
				if len(result.Attempts) > MaxRepairs+1 {
					t.Fatalf("repair loop exceeded its bound: %d attempts", len(result.Attempts))
				}
			} else if NeedsReview(last.Validation) {
				t.Fatalf("the repaired draft should be clean, got %v", codes(last.Validation))
			}
		})
	}
}

// A payments project must be usable as backend proof with no payment vocabulary at all.
func TestPaymentsProjectUsableWithoutPaymentVocabulary(t *testing.T) {
	s := syntheticSnapshot(plan.SpecificJobApplication)
	s.Projects = []profile.Project{{
		ID: "ledger", Name: "Synthetic Ledger",
		Description: "Go and PostgreSQL services with transactional workflows",
		Domains:     []string{"payments"},
	}}
	s.InputHash = s.Hash()
	e := replaceBody(s, `Hello team,

I'd like to apply for the Backend Engineer role. The emphasis on Go, PostgreSQL and APIs matches what I build.

In Synthetic Ledger I designed the backend around API contracts, relational schemas and transaction boundaries, and wrote the automated tests that cover them.

I'd be happy to share my CV. Would a short call next week suit you?`)
	findings := Validate(s, e)
	if NeedsReview(findings) {
		t.Fatalf("a payments project described purely as backend work should validate cleanly: %v", codes(findings))
	}
	if paymentPattern.MatchString(e.Subject + "\n" + e.Body) {
		t.Fatal("this fixture must contain no payment vocabulary at all")
	}
}

// Where the vacancy genuinely asks for payment engineering, payment language is permitted.
func TestGenuinePaymentRoleMayUsePaymentLanguage(t *testing.T) {
	s := syntheticSnapshot(plan.SpecificJobApplication)
	s.AdvertisedTitle = "Payments Backend Engineer"
	s.Match.OriginalTitle = s.AdvertisedTitle
	s.Capabilities = append(s.Capabilities, profile.Capability{ID: "payments", Name: "Payment integrations"})
	s.AllowedClaims = append(s.AllowedClaims,
		Claim{ID: "payments", Kind: "candidate", Text: "Integrated payment providers, handled asynchronous payment outcomes, idempotency and reconciliation workflows"})
	// A genuine payment role evidences the need, so the firewall claim is absent.
	s.ForbiddenClaims = []plan.Claim{}
	s.InputHash = s.Hash()
	e := replaceBody(s, `Hello team,

I'd like to apply for the Payments Backend Engineer role. Handling asynchronous payment outcomes correctly is the part of this work I know best.

In Synthetic Ledger I integrated payment providers and made the capture path idempotent, so a repeated callback could not double-apply. I also built the reconciliation workflow that settles our records against the provider's.

I'd be happy to share my CV. Would a short call suit you?`)
	findings := Validate(s, e)
	if HasBlock(findings) {
		t.Fatalf("an evidenced payment role must permit payment language: %v", codes(findings))
	}
	if !hasCode(findings, "missing_claim_provenance") {
		e.UsedClaimIDs = append(e.UsedClaimIDs, "payments")
	}
}

func codes(findings []Finding) []string {
	out := []string{}
	for _, f := range findings {
		out = append(out, f.Severity+":"+f.Code)
	}
	return out
}
