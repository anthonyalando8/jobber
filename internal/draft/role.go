package draft

import (
	"regexp"
	"strings"
)

var roleKeywords = []string{
	"Backend Engineer", "Backend Developer", "Golang Developer", "Go Developer",
	"Software Engineer", "Software Developer", "Full Stack Developer", "Fullstack Developer",
	"DevOps Engineer", "Systems Engineer", "API Developer", "Django Developer", "Node.js Developer",
}

// titleSuffixPattern catches a real job title the fixed keyword list above doesn't recognize —
// a run of capitalized words immediately followed by a common title-ending word (e.g. "Checkoff
// Product Scheme Manager"). Without this, an unrecognized title fell back to the generic
// "Backend Developer" guess, which then contradicted the AI-written observation elsewhere in the
// same email — that text independently describes the real role from the same signal, so a
// recipient would see two different job titles in one message.
var titleSuffixPattern = regexp.MustCompile(`(?:[A-Z][A-Za-z/&\-]*\s+){0,4}(?:Manager|Engineer|Developer|Officer|Analyst|Specialist|Lead|Administrator|Architect|Coordinator|Consultant|Director|Programmer|Designer)\b`)

// ExtractRole pulls a job title out of a hiring_backend signal's verbatim text. Per spec this
// comes from the signal itself, never the model. Tries the known backend-role keywords first
// (cheap, exact), then a general title-shaped pattern for anything else, before giving up and
// returning the generic fallback.
func ExtractRole(jobText string) string {
	lower := strings.ToLower(jobText)
	for _, kw := range roleKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return kw
		}
	}
	if m := titleSuffixPattern.FindString(jobText); m != "" {
		return strings.TrimSpace(m)
	}
	return "Backend Developer"
}
