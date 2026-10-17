package contactsearch

import (
	"strings"

	"github.com/anthonyalando8/prospector/internal/relevance"
)

// Classify fills in what a candidate is, using the classifiers the send path already uses.
//
// This function contains no opinion of its own about what makes a valid recruiting or
// engineering contact. It calls internal/relevance and records the answer. That is the whole
// point: if this package formed a second opinion, the two would drift, and the looser of the
// two would eventually be the one a message was sent under.
func Classify(c Candidate) Candidate {
	contact := relevance.Contact{
		Kind: "person", Value: c.Email, Person: c.PersonName, Role: c.RoleTitle, Status: "active",
	}
	if c.Email != "" {
		contact.Kind = "email"
		endpoint := relevance.ClassifyEndpoint(c.Email)
		c.EndpointPurpose, c.EndpointContactable = endpoint.Purpose, endpoint.Contactable
		if !endpoint.Contactable {
			// An address that is not a mailbox a person reads is recorded as exactly that. It
			// is kept rather than dropped, because "we found the company's Sentry key" is a
			// useful thing for an operator to see once, instead of rediscovering it monthly.
			c.Verification = VerificationNotHumanMailbox
		}
	}
	assessment := relevance.ClassifyContact(contact)
	c.Function, c.FunctionEvidence = assessment.Function, assessment.Evidence

	// A generic mailbox stays a generic mailbox. Section 26 of the brief is explicit that these
	// are kept and classified honestly rather than discarded or quietly upgraded: a careers@
	// address is genuinely useful, and it is genuinely not a named recruiter.
	if c.Email != "" && c.EndpointContactable && c.Function == relevance.FunctionGeneric &&
		c.Verification != VerificationNotHumanMailbox {
		c.Verification = VerificationGeneric
	}
	// An application route needs no mailbox at all, so an endpoint verdict would be meaningless
	// for one.
	if c.Kind == KindApplicationURL {
		c.EndpointPurpose, c.EndpointContactable = "", false
		if c.Function == "" || c.Function == relevance.FunctionUnknown {
			c.Function = relevance.FunctionRecruiting
			c.FunctionEvidence = "an application route the employer published for this vacancy"
		}
	}
	c.Confidence = confidenceFor(c)
	return c
}

// confidenceFor states how much the evidence actually supports the claim.
//
// The rule that matters is the last one: a candidate resting only on a search-engine summary is
// never above low confidence, no matter how confidently the summary asserts an address. A
// snippet is a claim about a page, and this pipeline's standard is what the page says.
func confidenceFor(c Candidate) string {
	trust := c.BestTrust()
	switch {
	case trust <= SnippetOnlyTrust:
		return ConfidenceLow
	case c.Inferred():
		return ConfidenceLow
	case c.Kind == KindApplicationURL && trust >= TrustFloorForPublishedEmail:
		return ConfidenceHigh
	case c.Email != "" && c.EndpointContactable && publishedProvenance(c.EmailProvenance) && trust >= SourceTrust(SourceCareersPage):
		return ConfidenceHigh
	case c.Email != "" && publishedProvenance(c.EmailProvenance) && trust >= TrustFloorForPublishedEmail:
		return ConfidenceMedium
	case c.PersonName != "" && c.HasEmployerOwnedEvidence():
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

func publishedProvenance(provenance string) bool {
	switch provenance {
	case ProvenanceSource, ProvenanceEmployer, ProvenanceThirdParty:
		return true
	}
	return false
}

// Promotion outcomes. A candidate either becomes a row the send path can see, waits for a person
// to judge it, or stays a candidate.
const (
	PromoteToContact   = "promote"
	PromoteNeedsReview = "review_required"
	PromoteHold        = "hold"
)

// PromotionDecision decides whether a candidate may become a contact row, and says why.
//
// The bar is deliberately high, because the contact table is what the send path reads. Four
// things must all hold: there is an address, the employer or the job source published it, the
// evidence behind it is employer-owned rather than hearsay, and it is a mailbox a person reads.
// Everything else waits for a human — which is not a failure state, it is the difference between
// a tool that finds leads and a tool that quietly acquires recipients.
func PromotionDecision(c Candidate) (outcome, reason string) {
	switch {
	case c.Inferred():
		return PromoteNeedsReview,
			"This address was inferred from the company's apparent email format, not published anywhere. " +
				"It is never promoted automatically and never becomes send-eligible without a person accepting it."
	case c.Email == "":
		if c.Kind == KindApplicationURL {
			return PromoteHold,
				"This is an application route rather than a mailbox. It is a valid destination for a job " +
					"application and is offered as one; it is not a contact row."
		}
		return PromoteHold,
			"A person and role were found, but no address was published for them. No address is guessed from a name."
	case !c.EndpointContactable:
		return PromoteHold,
			"This address is not a mailbox a person reads (" + c.EndpointPurpose + "): " + c.FunctionEvidence
	case !publishedProvenance(c.EmailProvenance):
		return PromoteNeedsReview, "The provenance of this address is not a publication by the employer or the job source."
	case c.BestTrust() < TrustFloorForPublishedEmail:
		return PromoteNeedsReview,
			"The strongest evidence for this address is not the employer speaking about itself, so it is a lead " +
				"rather than a published contact. A person should confirm it before it is used."
	case c.EmailProvenance == ProvenanceThirdParty:
		return PromoteNeedsReview, "A third party published this address; the employer did not."
	default:
		return PromoteToContact, "The employer or the job source published this address on a page we read."
	}
}

// ContactQuality maps a promoted candidate onto the contact table's existing quality ranking:
// a named person outranks a role mailbox, which outranks a generic one. It states no new idea of
// quality; it translates into the scale the table already uses.
func ContactQuality(c Candidate) int {
	switch {
	case strings.TrimSpace(c.PersonName) != "":
		return 3
	case c.Function == relevance.FunctionGeneric:
		return 1
	default:
		return 2
	}
}
