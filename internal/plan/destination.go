package plan

import (
	"strings"

	"github.com/anthonyalando8/prospector/internal/relevance"
)

// Destination kinds. A destination is where a message or an application actually goes, which is
// not always a mailbox.
const (
	// DestinationApplicationURL is an employer-published apply form or applicant tracking route.
	DestinationApplicationURL = "application_url"
	// DestinationApplicationEmail is an address the job source published as where to apply.
	DestinationApplicationEmail = "application_email"
	// DestinationContact is a stored contact chosen by the recipient rules.
	DestinationContact = "contact"
)

// Destination is the resolved answer to "where does this actually go".
type Destination struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	// Label is how the destination should be described to an operator.
	Label string `json:"label"`
	// Relevance is the recipient tier, for a mailbox destination. An application route has no
	// recipient tier because there is no recipient: there is a form.
	Relevance string `json:"relevance,omitempty"`
	Reason    string `json:"reason"`
	ContactID int64  `json:"contact_id,omitempty"`
	// SourcePublished marks a destination the employer or the job source itself named.
	SourcePublished bool `json:"source_published"`
}

// Acceptable reports whether this destination can be used without a person first resolving
// something about it. A review_required recipient is a real destination and is not acceptable
// unattended; that distinction is what separates "we have somewhere to send this" from "we have
// somewhere to send this that nobody needs to look at first".
func (d Destination) Acceptable() bool {
	switch d.Kind {
	case DestinationApplicationURL, DestinationApplicationEmail:
		return true
	case DestinationContact:
		return d.Relevance == RecipientPreferred || d.Relevance == RecipientAcceptable
	}
	return false
}

// DestinationInput is everything destination resolution reads.
type DestinationInput struct {
	Intent   Intent
	Contacts []relevance.ContactAssessment
	// Source is what the job posting published about where to apply.
	Source SourceApplicationAddress
}

// ResolveDestination answers where an approach to this company would actually go, for this
// purpose, and returns false when there is nowhere.
//
// The important correction this function makes is to the definition of the problem. "No contact"
// was previously understood as "no email address", which is wrong in a way that discards real
// destinations: a specific job application to an employer that publishes a Greenhouse link has
// somewhere perfectly good to go, and demanding a mailbox instead would reject the exact route
// the employer asked candidates to use. So the blocker is no acceptable application or contact
// destination — and for an application, an employer-published apply route is one.
//
// The preference order differs by purpose, because the purposes are different. An application
// follows the employer's stated route first. A speculative enquiry and a service approach have
// no such route to follow — an applicant tracking system is where candidates apply, so it is
// never a destination for outreach that is not an application — and go to a person instead.
func ResolveDestination(in DestinationInput) (Destination, bool) {
	options := RankRecipients(in.Intent, in.Contacts, in.Source)

	if in.Intent == SpecificJobApplication {
		// 1. The job-specific application route. The employer named it; nothing outranks that.
		if route := strings.TrimSpace(in.Source.URL); route != "" {
			return Destination{
				Kind: DestinationApplicationURL, Value: route, SourcePublished: true,
				Label:  "Published application route",
				Reason: "The job source published this as where to apply. No email address is needed to apply through it.",
			}, true
		}
		// 2. The published application address, when it is a mailbox a person reads.
		if address := strings.TrimSpace(in.Source.Email); address != "" {
			if relevance.ClassifyEndpoint(address).Contactable {
				return Destination{
					Kind: DestinationApplicationEmail, Value: address, SourcePublished: true,
					Label:  "Published application address",
					Reason: "The job source published this address as its application destination.",
				}, true
			}
		}
	}

	// 3. The best stored contact the recipient rules will accept for this purpose. A
	// review_required contact is returned as a destination, so the caller can distinguish "no
	// destination at all" from "a destination that needs a person to confirm it" — two states
	// that call for completely different action.
	if best, ok := BestRecipient(options); ok {
		return Destination{
			Kind: DestinationContact, Value: best.Assessment.Contact.Value,
			ContactID: best.Assessment.Contact.ID, Relevance: best.Relevance,
			Label:           destinationLabel(best),
			Reason:          best.Reason,
			SourcePublished: best.SourcePublished,
		}, true
	}
	return Destination{}, false
}

// HasAcceptableDestination is the eligibility question contact discovery asks: is this company
// actually blocked on contact data, or does it already have somewhere to go?
func HasAcceptableDestination(in DestinationInput) bool {
	destination, ok := ResolveDestination(in)
	return ok && destination.Acceptable()
}

// DestinationGap states, in the words an operator should see, why there is nowhere acceptable to
// send this. It distinguishes the several different situations that all previously read as
// "no contact", because they call for different things: a company with only machine endpoints
// needs a search, a company with only generic mailboxes needs a named person, and a company with
// nothing at all needs both.
func DestinationGap(in DestinationInput) string {
	if HasAcceptableDestination(in) {
		return ""
	}
	usable, generic, machine, bad := 0, 0, 0, 0
	for _, a := range in.Contacts {
		if a.Contact.Kind != "email" {
			continue
		}
		if a.Contact.Status == "bad" {
			bad++
			continue
		}
		if !relevance.ClassifyEndpoint(a.Contact.Value).Contactable {
			machine++
			continue
		}
		usable++
		if a.Function == relevance.FunctionGeneric || a.Function == relevance.FunctionUnknown {
			generic++
		}
	}
	switch {
	case usable+machine+bad == 0:
		return "No contact of any kind is stored for this company, and the source published no application route."
	case usable == 0 && machine > 0:
		return "Every stored address is a machine endpoint rather than a mailbox a person reads."
	case usable == 0:
		return "Every stored contact is marked bad or unusable."
	case generic == usable:
		return "Only generic mailboxes are stored. No recruiting or engineering contact is identified, and no application route was published."
	default:
		return "No stored contact reaches an identified recruiting or engineering function for this purpose."
	}
}

func destinationLabel(option RecipientOption) string {
	if name := strings.TrimSpace(option.Assessment.Contact.Person); name != "" {
		if role := strings.TrimSpace(option.Assessment.Contact.Role); role != "" {
			return name + " — " + role
		}
		return name
	}
	switch option.Assessment.Function {
	case relevance.FunctionRecruiting:
		return "Recruiting mailbox"
	case relevance.FunctionEngineering:
		return "Engineering mailbox"
	case relevance.FunctionLeadership:
		return "Leadership mailbox"
	case relevance.FunctionGeneric:
		return "Generic mailbox"
	}
	return "Unidentified mailbox"
}
