package plan

import (
	"sort"
	"strings"

	"github.com/anthonyalando8/prospector/internal/relevance"
)

// Recipient relevance tiers. review_required is not a failure: it means the contact can receive
// the message but its relevance to this purpose is unverified, so a human must confirm it.
const (
	RecipientPreferred  = "preferred"
	RecipientAcceptable = "acceptable"
	RecipientReview     = "review_required"
	RecipientUnusable   = "unusable"
)

// RecipientOption is one candidate recipient ranked for a specific intent.
type RecipientOption struct {
	Assessment relevance.ContactAssessment `json:"assessment"`
	Relevance  string                      `json:"relevance"`
	Reason     string                      `json:"reason"`
	// SourcePublished marks the address the employer itself named as the place to apply. It
	// outranks every inferred contact, because it is stated rather than deduced.
	SourcePublished bool `json:"source_published,omitempty"`
}

// preferenceByIntent encodes section 14: which contact function suits which purpose. A function
// that is merely reachable is never promoted to preferred.
var preferenceByIntent = map[Intent]map[string]string{
	SpecificJobApplication: {
		relevance.FunctionRecruiting:  RecipientPreferred,
		relevance.FunctionEngineering: RecipientPreferred,
		relevance.FunctionLeadership:  RecipientAcceptable,
		relevance.FunctionCommercial:  RecipientReview,
		relevance.FunctionGeneric:     RecipientReview,
		relevance.FunctionUnknown:     RecipientReview,
	},
	SpeculativeJobEnquiry: {
		relevance.FunctionRecruiting:  RecipientPreferred,
		relevance.FunctionEngineering: RecipientPreferred,
		relevance.FunctionLeadership:  RecipientAcceptable,
		relevance.FunctionCommercial:  RecipientReview,
		relevance.FunctionGeneric:     RecipientReview,
		relevance.FunctionUnknown:     RecipientReview,
	},
	ServiceOutreach: {
		relevance.FunctionEngineering: RecipientPreferred,
		relevance.FunctionLeadership:  RecipientPreferred,
		relevance.FunctionRecruiting:  RecipientReview,
		relevance.FunctionCommercial:  RecipientReview,
		relevance.FunctionGeneric:     RecipientReview,
		relevance.FunctionUnknown:     RecipientReview,
	},
}

var functionPurpose = map[string]string{
	relevance.FunctionRecruiting:  "handles hiring, so it can act on an application or an opportunities enquiry",
	relevance.FunctionEngineering: "is a technical contact who can judge engineering relevance",
	relevance.FunctionLeadership:  "is a decision maker who can route or act on the message",
	relevance.FunctionCommercial:  "serves a commercial function, which is not the audience for this purpose",
	relevance.FunctionGeneric:     "is a generic mailbox reaching no identified person or function",
	relevance.FunctionUnknown:     "has no stored role and no recognizable function",
}

// SourceApplicationAddress is an application address the job source itself published. When one
// exists it outranks every inferred contact, because the employer stated where to apply.
type SourceApplicationAddress struct {
	Email string
	URL   string
}

// RankRecipients orders candidate recipients for one intent. It never renames a mailbox into a
// person and never silently promotes a generic address to a recruiter.
func RankRecipients(intent Intent, contacts []relevance.ContactAssessment, source SourceApplicationAddress) []RecipientOption {
	preferences := preferenceByIntent[intent]
	options := make([]RecipientOption, 0, len(contacts))
	for _, a := range contacts {
		option := RecipientOption{Assessment: a}
		if !a.Usable {
			option.Relevance = RecipientUnusable
			option.Reason = "Not a usable active email contact."
			options = append(options, option)
			continue
		}
		if intent == SpecificJobApplication && source.Email != "" &&
			strings.EqualFold(strings.TrimSpace(a.Contact.Value), strings.TrimSpace(source.Email)) {
			option.Relevance = RecipientPreferred
			option.SourcePublished = true
			option.Reason = "The job source published this address as its application destination."
			options = append(options, option)
			continue
		}
		tier, ok := preferences[a.Function]
		if !ok {
			tier = RecipientReview
		}
		option.Relevance = tier
		option.Reason = "This contact " + functionPurpose[a.Function] + " (" + a.Evidence + ")."
		if tier == RecipientReview {
			option.Reason += " Its relevance to this purpose is unverified, so it requires review."
		}
		if !a.Named {
			option.Reason += " No person name is on file; none is inferred from the address."
		}
		options = append(options, option)
	}
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].SourcePublished != options[j].SourcePublished {
			return options[i].SourcePublished
		}
		if ri, rj := tierRank(options[i].Relevance), tierRank(options[j].Relevance); ri != rj {
			return ri > rj
		}
		if options[i].Assessment.Named != options[j].Assessment.Named {
			return options[i].Assessment.Named
		}
		if options[i].Assessment.Contact.Quality != options[j].Assessment.Contact.Quality {
			return options[i].Assessment.Contact.Quality > options[j].Assessment.Contact.Quality
		}
		return options[i].Assessment.Contact.ID < options[j].Assessment.Contact.ID
	})
	return options
}

func tierRank(tier string) int {
	switch tier {
	case RecipientPreferred:
		return 3
	case RecipientAcceptable:
		return 2
	case RecipientReview:
		return 1
	}
	return 0
}

// BestRecipient returns the highest-ranked usable option, or false when none can receive the
// message at all. A returned option may still be review_required.
func BestRecipient(options []RecipientOption) (RecipientOption, bool) {
	for _, o := range options {
		if o.Relevance != RecipientUnusable {
			return o, true
		}
	}
	return RecipientOption{}, false
}
