package relevance

import (
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/evidence"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/profile"
)

// syntheticProfile is a fixture. None of these facts are approved on any live database.
func syntheticProfile() profile.Document {
	return profile.Document{
		Identity:          "Full-Stack Software Developer",
		PrimaryStrength:   "Backend Engineering",
		SecondaryPosition: "Full-Stack Development",
		DesiredRoles: []profile.JobFamily{
			{ID: "role-backend", Name: "Backend Engineering", Aliases: []string{"Backend Engineer"}},
		},
		Technologies: []profile.Technology{{ID: "tech-go", Name: "Go", Aliases: []string{"Golang"}}},
		Capabilities: []profile.Capability{
			{ID: "cap-api", Name: "API development", Description: "Designs and builds REST APIs"},
			{ID: "cap-software", Name: "Software development", Description: "Builds custom software"},
		},
	}
}

func groupsFrom(facts ...evidence.Fact) []evidence.Group { return evidence.GroupFacts(facts) }

func fact(id int64, kind, detail, excerpt string) evidence.Fact {
	now := time.Now()
	return evidence.Fact{SignalID: id, CompanyID: 1, Kind: kind, Detail: detail, Excerpt: excerpt,
		SourceURL: "https://example.test/", ExtractionMethod: "site_text_v1", Confidence: "medium",
		Observations: 1, FirstSeen: now, LastSeen: now, CreatedAt: now}
}

// G. A checkout on a website establishes presence and nothing else.
func TestCheckoutAloneEstablishesNoRelevanceAndNoPaymentNeed(t *testing.T) {
	doc := syntheticProfile()
	result := Assess(Input{
		CompanyID: 1, Profile: &doc, ProfileRevisionID: 7,
		Groups: groupsFrom(
			fact(1, "checkout_present", "Checkout or payment functionality is present on the site", ""),
			fact(2, "paybill_listed", "The site lists an M-Pesa paybill or till", ""),
			fact(3, "tech:react", "Found react in site code", ""),
		),
	})
	if result.PaymentNeedEvidenced {
		t.Fatal("a checkout and an M-Pesa till must never evidence a payment need")
	}
	if result.Relevant {
		t.Fatalf("presence detections alone must not establish a reason to contact; score %d", result.Score)
	}
	if !result.Reasons.Has("payment_need_not_evidenced") {
		t.Fatalf("the absence of payment-need evidence must be stated: %v", result.Reasons.Codes())
	}
	if !result.Reasons.Has("no_engineering_reason_established") {
		t.Fatalf("missing no_engineering_reason_established: %v", result.Reasons.Codes())
	}
}

// J. Repeated observations of one fact cannot inflate relevance.
func TestRepeatedEvidenceDoesNotInflateRelevance(t *testing.T) {
	doc := syntheticProfile()
	single := groupsFrom(fact(1, "offers_tech_services", "Site markets software development as a service", "We build custom software for clients"))
	repeated := groupsFrom(
		fact(1, "offers_tech_services", "Site markets software development as a service", "We build custom software for clients"),
		fact(2, "offers_tech_services", "Site markets software development as a service", "We build custom software for clients"),
		fact(3, "offers_tech_services", "Site markets software development as a service", "We build custom software for clients"),
	)
	one := Assess(Input{CompanyID: 1, Profile: &doc, ProfileRevisionID: 7, Groups: single})
	many := Assess(Input{CompanyID: 1, Profile: &doc, ProfileRevisionID: 7, Groups: repeated})
	if one.Score != many.Score {
		t.Fatalf("three copies of one fact scored %d against %d for a single copy", many.Score, one.Score)
	}
	if many.GroupCount != 1 || many.ObservationCount != 3 {
		t.Fatalf("group/observation counts = %d/%d, want 1/3", many.GroupCount, many.ObservationCount)
	}
	if !many.Reasons.Has("evidence_grouped") {
		t.Fatalf("grouping should be stated in the reasons: %v", many.Reasons.Codes())
	}
}

// Company work matching an approved capability is a real reason to make contact.
func TestDescribedCompanyWorkMatchingCapabilityEstablishesRelevance(t *testing.T) {
	doc := syntheticProfile()
	result := Assess(Input{
		CompanyID: 1, Profile: &doc, ProfileRevisionID: 7,
		Groups: groupsFrom(fact(1, "offers_tech_services", "Site markets software development as a service",
			"We build custom software and APIs for clients")),
	})
	if !result.Relevant {
		t.Fatalf("described engineering work matching an approved capability should establish relevance: %v", result.Reasons.Codes())
	}
	if !result.Reasons.Has("company_work_matches_capability") {
		t.Fatalf("missing capability match reason: %v", result.Reasons.Codes())
	}
}

// An unrelated vacancy contributes no relevance of its own but is reported honestly.
func TestUnrelatedVacanciesContributeNoRelevance(t *testing.T) {
	doc := syntheticProfile()
	result := Assess(Input{
		CompanyID: 1, Profile: &doc, ProfileRevisionID: 7,
		Opportunities: []OpportunitySummary{
			{ID: 1, Title: "Marketing Manager", Classification: opportunity.ClassificationUnrelated},
			{ID: 2, Title: "Finance Officer", Classification: opportunity.ClassificationUnrelated},
		},
	})
	if result.Relevant {
		t.Fatal("unrelated vacancies alone do not make a company relevant")
	}
	if !result.Reasons.Has("no_relevant_vacancy") {
		t.Fatalf("missing no_relevant_vacancy reason: %v", result.Reasons.Codes())
	}
}

// A vacancy that itself demands payment engineering is the evidence that permits a payment
// approach.
func TestVacancyStatingPaymentRequirementEvidencesPaymentNeed(t *testing.T) {
	doc := syntheticProfile()
	result := Assess(Input{
		CompanyID: 1, Profile: &doc, ProfileRevisionID: 7,
		Opportunities: []OpportunitySummary{
			{ID: 9, Title: "Backend Engineer", Classification: opportunity.ClassificationStrong, PaymentRequirement: true},
		},
	})
	if !result.PaymentNeedEvidenced {
		t.Fatal("a vacancy requiring payment integration should evidence a payment need")
	}
}

// M. A generic mailbox is reachable, but its relevance to any purpose is unverified and no
// person is invented from the address.
func TestGenericMailboxIsClassifiedGenericAndUnnamed(t *testing.T) {
	assessment := ClassifyContact(Contact{ID: 1, Kind: "email", Value: "info@example.test", Quality: 1, Status: "active"})
	if assessment.Function != FunctionGeneric {
		t.Fatalf("function = %q, want generic_mailbox", assessment.Function)
	}
	if assessment.Named {
		t.Fatal("a generic mailbox has no person name on file")
	}
	unknown := ClassifyContact(Contact{ID: 2, Kind: "email", Value: "jm@example.test", Quality: 3, Status: "active"})
	if unknown.Function != FunctionUnknown {
		t.Fatalf("function = %q, want unknown for an unrecognizable local part", unknown.Function)
	}
	if unknown.Named {
		t.Fatal("a name must never be reconstructed from an email local part")
	}
}

// A stored role decides the function ahead of any guess from the address.
func TestStoredRoleDecidesContactFunction(t *testing.T) {
	assessment := ClassifyContact(Contact{ID: 1, Kind: "email", Value: "info@example.test",
		Person: "A Person", Role: "Head of Engineering", Quality: 3, Status: "active"})
	if assessment.Function != FunctionEngineering {
		t.Fatalf("function = %q, want engineering from the stored role", assessment.Function)
	}
	if !assessment.Named {
		t.Fatal("a stored person name should be reported as named")
	}
	bad := ClassifyContact(Contact{ID: 2, Kind: "email", Value: "dev@example.test", Status: "bad"})
	if bad.Usable {
		t.Fatal("a contact marked bad is not usable")
	}
}

// Phase 3.5 calibration, from the live estate: 392 companies carry a legacy hiring_backend
// keyword detection. A detection summary from the old pipeline is a lead worth re-researching,
// not an established reason to write to anyone, so it must not on its own make a company
// contactable. It still contributes to the ranking score.
func TestLegacyDetectionAloneDoesNotMakeACompanyContactable(t *testing.T) {
	doc := syntheticProfile()
	legacy := fact(1, "hiring_backend", "Backend Developer at Example Ltd. Some truncated card text", "")
	legacy.ExtractionMethod = evidence.LegacyExtraction
	result := Assess(Input{CompanyID: 1, Profile: &doc, ProfileRevisionID: 7, Groups: groupsFrom(legacy)})
	if result.Relevant {
		t.Fatal("a pre-v2 keyword detection alone must not establish a reason to contact")
	}
	if result.Score == 0 {
		t.Fatal("the legacy detection should still contribute to the ranking score")
	}
	if !result.Reasons.Has("legacy_detection_needs_reresearch") {
		t.Fatalf("the reason must say the lead needs re-research: %v", result.Reasons.Codes())
	}

	// The same company, once it actually states a need in its own words, becomes contactable.
	stated := fact(2, "engineering_request", "The company states an engineering hiring need",
		"We are hiring backend developers to grow our platform team")
	result = Assess(Input{CompanyID: 1, Profile: &doc, ProfileRevisionID: 7, Groups: groupsFrom(legacy, stated)})
	if !result.Relevant {
		t.Fatalf("a need the company stated itself should establish relevance: %v", result.Reasons.Codes())
	}
}
