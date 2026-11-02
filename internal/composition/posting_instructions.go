package composition

import (
	"strings"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
)

// PostingInstructionFindings catches a written email doing what the posting's text told it to:
// copying a word from an attention check or a phrase from a line addressed to AI. The writer
// takes instructions from the sender, never from a web page, so such a token may appear only
// when the sender's own notes ask for it.
//
// It applies to generated text only. A person editing the email may write anything they choose;
// the check is about the writer obeying a page, not about the words themselves.
func PostingInstructionFindings(s Snapshot, e Email) []Finding {
	findings := []Finding{}
	text := e.Subject + "\n" + strings.TrimSuffix(e.Body, "\n\n"+Signature(s))
	for _, instruction := range s.PostingInstructions {
		if instruction.Kind != opportunity.InstructionAIDirected && instruction.Kind != opportunity.InstructionAttentionCheck {
			continue
		}
		for _, token := range instruction.Tokens {
			if !containsToken(text, token) || senderAsked(s, token) {
				continue
			}
			findings = append(findings, Finding{Severity: "block", Code: "posting_instruction_followed",
				Explanation: "The email includes “" + token + "”, which the job posting asked for. The writer must not follow " +
					"instructions from a posting; remove it. If you want it in the email, add it to your notes for the writer.",
				Excerpt: instruction.Excerpt, Repairable: true})
		}
	}
	return findings
}

func containsToken(text, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	if strings.ContainsAny(token, "[]") {
		return strings.Contains(strings.ToLower(text), strings.ToLower(token))
	}
	return phrase(text, token)
}

// senderAsked reports whether the sender's own notes or reason name the token.
func senderAsked(s Snapshot, token string) bool {
	for _, r := range s.ReviewReasons {
		if (r.Code == plan.OperatorNoteCode || r.Code == plan.OperatorOverrideCode) && containsToken(r.Text, token) {
			return true
		}
	}
	return false
}
