package draft

import "strings"

var roleKeywords = []string{
	"Backend Engineer", "Backend Developer", "Golang Developer", "Go Developer",
	"Software Engineer", "Software Developer", "Full Stack Developer", "Fullstack Developer",
	"DevOps Engineer", "Systems Engineer", "API Developer", "Django Developer", "Node.js Developer",
}

// ExtractRole pulls a job title out of a hiring_backend signal's verbatim text. Per spec this
// comes from the signal itself, never from the model.
func ExtractRole(jobText string) string {
	lower := strings.ToLower(jobText)
	for _, kw := range roleKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return kw
		}
	}
	return "Backend Developer"
}
