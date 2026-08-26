package seed

import (
	"strings"
)

// usRelevantTitlePhrases narrows a US/international remote job feed (every category — design,
// marketing, sales, hospitality, ...) down to roles this tool can credibly pitch backend/
// full-stack contract work against. Matched against the job *title* only, deliberately — a spot
// check of RemoteOK's own `tags` field found "golang" attached to postings like "General
// Cleaner" and "Front Office Executive" that have nothing to do with software (site-wide browse
// tags, not per-posting relevance). Pitching a cleaning job "you're hiring a Backend Developer"
// would be a far worse credibility hit than a missed lead — title text is what a poster actually
// wrote about the role, not an auto-suggested category. Shared by every US-source seeder
// (RemoteOK, We Work Remotely, Remotive, Himalayas) so the bar for "relevant" can't drift
// between them.
var usRelevantTitlePhrases = []string{
	"backend", "back-end", "back end", "full stack", "fullstack", "golang", "go developer",
	"python developer", "api developer", "software engineer", "software developer",
	"backend engineer", "backend developer", "django developer", "node developer",
	"javascript developer", "typescript developer", "platform engineer", "devops engineer",
	"site reliability", "infrastructure engineer",
}

// isRelevantJobTitle reports whether a job title is worth pitching backend/full-stack contract
// work against.
func isRelevantJobTitle(title string) bool {
	lower := strings.ToLower(title)
	for _, phrase := range usRelevantTitlePhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// usJobSignalText builds the hiring_backend signal detail every US-source seeder stores,
// capped to the same 300 chars the Kenyan job-board seeder uses.
func usJobSignalText(position, company, extra string) string {
	text := position + " at " + company
	if extra != "" {
		text = text + ". " + extra
	}
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}
