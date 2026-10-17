package relevance

import "strings"

// Contact functions inferred from stored evidence only. Nothing here invents a person.
const (
	FunctionRecruiting  = "recruiting"
	FunctionEngineering = "engineering"
	FunctionLeadership  = "leadership"
	FunctionCommercial  = "commercial"
	FunctionGeneric     = "generic_mailbox"
	FunctionUnknown     = "unknown"
)

// Contact is the stored contact record as the Phase 3 rules see it.
type Contact struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	Person    string `json:"person,omitempty"`
	Role      string `json:"role,omitempty"`
	Quality   int    `json:"quality"`
	Status    string `json:"status"`
	Suppliedy string `json:"-"`
}

// ContactAssessment records what function a contact appears to serve and how that was decided.
type ContactAssessment struct {
	Contact  Contact `json:"contact"`
	Function string  `json:"function"`
	// Named is true only when a real person name is stored. A name is never reconstructed from
	// an email local part.
	Named    bool   `json:"named"`
	Usable   bool   `json:"usable"`
	Evidence string `json:"evidence"`
}

var functionKeywords = []struct {
	function string
	words    []string
}{
	{FunctionRecruiting, []string{"recruit", "talent", "hiring", "careers", "career", "jobs", "job", "hr", "human resource", "people ops", "peopleops", "people operations", "people team"}},
	{FunctionEngineering, []string{"cto", "engineer", "engineering", "developer", "dev", "tech", "technical", "technology", "it", "devops", "architect", "product engineering"}},
	{FunctionLeadership, []string{"ceo", "founder", "co-founder", "cofounder", "managing director", "director", "owner", "principal", "partner", "md"}},
	{FunctionCommercial, []string{"sales", "marketing", "billing", "accounts", "finance", "invoice", "procurement", "business development"}},
}

// genericLocalParts are mailboxes that reach no identified person or function.
var genericLocalParts = map[string]bool{
	"info": true, "hello": true, "contact": true, "admin": true, "support": true,
	"team": true, "general": true, "office": true, "help": true, "enquiries": true,
	"enquiry": true, "inquiries": true, "mail": true, "email": true, "sales@": false,
	"reception": true, "hi": true, "ask": true, "service": true, "customercare": true,
}

// ClassifyContact infers a contact's function from the stored role and the mailbox local part.
// An unrecognized local part stays unknown: guessing that "jm@" is a hiring manager, or that it
// belongs to a person called "Jm", would be inventing evidence.
func ClassifyContact(c Contact) ContactAssessment {
	assessment := ContactAssessment{Contact: c, Function: FunctionUnknown}
	assessment.Named = strings.TrimSpace(c.Person) != ""
	assessment.Usable = c.Kind == "email" && c.Status != "bad" && strings.Contains(c.Value, "@")

	role := strings.ToLower(strings.TrimSpace(c.Role))
	local := strings.ToLower(c.Value)
	if at := strings.Index(local, "@"); at >= 0 {
		local = local[:at]
	}
	if role != "" {
		for _, group := range functionKeywords {
			for _, word := range group.words {
				if strings.Contains(role, word) {
					assessment.Function = group.function
					assessment.Evidence = "stored contact role \"" + c.Role + "\""
					return assessment
				}
			}
		}
	}
	if genericLocalParts[local] {
		assessment.Function = FunctionGeneric
		assessment.Evidence = "generic mailbox \"" + local + "@\" reaching no identified person or function"
		return assessment
	}
	for _, group := range functionKeywords {
		for _, word := range group.words {
			if localPartHas(local, word) {
				assessment.Function = group.function
				assessment.Evidence = "mailbox local part contains \"" + word + "\""
				return assessment
			}
		}
	}
	assessment.Evidence = "no stored role and no recognizable function in the mailbox local part"
	return assessment
}

// localPartHas matches a whole segment of a local part, so "dev" matches dev@ and jobs.dev@ but
// not "davidevans@".
func localPartHas(local, word string) bool {
	for _, segment := range strings.FieldsFunc(local, func(r rune) bool {
		return r == '.' || r == '-' || r == '_' || r == '+'
	}) {
		if segment == word {
			return true
		}
	}
	return false
}

// ClassifyContacts assesses every stored contact for a company.
func ClassifyContacts(contacts []Contact) []ContactAssessment {
	out := make([]ContactAssessment, 0, len(contacts))
	for _, c := range contacts {
		out = append(out, ClassifyContact(c))
	}
	return out
}
