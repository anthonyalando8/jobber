package composition

import (
	"regexp"
	"strings"
)

// writerGuidance is what the writer is told in advance about the checks its email will face, so
// it does not produce text the validator is certain to reject. Both lists come from the same code
// the validator runs.
func writerGuidance(s Snapshot) (approved, avoid []string) {
	approved = ApprovedTechnologies(s)
	requirements := []string{}
	for _, source := range s.Sources {
		requirements = append(requirements, source.Text)
	}
	if s.Match != nil {
		for _, gap := range s.Match.Gaps {
			requirements = append(requirements, gap.Requirement.Term+" "+gap.Requirement.Excerpt)
		}
	}
	avoid = UnapprovedTechnologies(s, strings.Join(requirements, "\n"))
	return approved, avoid
}

var sentenceEnd = regexp.MustCompile(`^[.!?]*[ \t]*`)

// DropGapSentences removes, from a generated email, each sentence the validator flagged for
// naming a technology the candidate does not have as a gap or a contrast ("rather than Node.js").
// Removing such a sentence never makes the email less true: it only stops it volunteering a
// weakness. Sentences that claim the technology are left alone; those are a factual problem the
// writer must repair, not something to delete silently. The removed sentences are returned so the
// change is visible.
func DropGapSentences(e Email, findings []Finding) (Email, []string) {
	removed := []string{}
	for _, f := range findings {
		if f.Code != "unapproved_technology" && f.Code != "acknowledged_skill_gap" {
			continue
		}
		sentence := strings.TrimSpace(f.Excerpt)
		if sentence == "" || !gapPattern.MatchString(sentence) {
			continue
		}
		i := strings.Index(e.Body, sentence)
		if i < 0 {
			continue
		}
		end := i + len(sentence)
		end += len(sentenceEnd.FindString(e.Body[end:]))
		e.Body = e.Body[:i] + e.Body[end:]
		removed = append(removed, sentence)
	}
	if len(removed) > 0 {
		e.Body = tidyParagraphs(e.Body)
	}
	return e, removed
}

func tidyParagraphs(body string) string {
	paragraphs := []string{}
	for _, p := range strings.Split(body, "\n\n") {
		lines := []string{}
		for _, line := range strings.Split(p, "\n") {
			if line = strings.TrimSpace(strings.Join(strings.Fields(line), " ")); line != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) > 0 {
			paragraphs = append(paragraphs, strings.Join(lines, "\n"))
		}
	}
	return strings.Join(paragraphs, "\n\n")
}
