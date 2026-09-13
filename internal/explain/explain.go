// Package explain carries the stated reason behind a decision. Every Phase 3 output is
// required to say why, so a bare number is never the whole answer.
package explain

// Reason is one machine-readable code with the human sentence and the exact records it cites.
type Reason struct {
	Code string   `json:"code"`
	Text string   `json:"text"`
	Refs []string `json:"refs,omitempty"`
}

// New builds a reason. Refs are stable references such as profile:capability:api,
// opportunity:requirement:req-3 or evidence:1204.
func New(code, text string, refs ...string) Reason {
	return Reason{Code: code, Text: text, Refs: refs}
}

// Reasons is an ordered list of reasons preserving the order they were decided in.
type Reasons []Reason

func (r Reasons) Has(code string) bool {
	for _, item := range r {
		if item.Code == code {
			return true
		}
	}
	return false
}

func (r Reasons) Codes() []string {
	codes := make([]string, 0, len(r))
	for _, item := range r {
		codes = append(codes, item.Code)
	}
	return codes
}

// Add appends a reason and returns the list, so decision code reads as a sequence of findings.
func (r Reasons) Add(code, text string, refs ...string) Reasons {
	return append(r, New(code, text, refs...))
}
