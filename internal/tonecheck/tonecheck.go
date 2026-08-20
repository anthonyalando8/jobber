// Package tonecheck is the deterministic quality gate from review.md §22: a local check for
// obvious AI/electronic writing patterns, not a magical AI detector. review.md §14 is explicit
// that a matched phrase is only one signal among several a human or the send policy weighs —
// never the sole reason to block a message outright.
package tonecheck

import "strings"

// bannedPhrases are the marketing/AI-slop phrases review.md §16/§23 call out by name. Matched
// as case-insensitive substrings against the AI-authored text (the observation/hypothesis, or
// the filled draft body — either way this only ever sees a handful of short sentences, not a
// full essay, so a substring scan is cheap and sufficient).
var bannedPhrases = []string{
	"i hope this email finds you well",
	"i came across",
	"i was impressed",
	"i was thrilled",
	"i wanted to reach out",
	"exciting opportunity",
	"innovative solution",
	"cutting-edge",
	"cutting edge",
	"seamless",
	"robust",
	"leverage",
	"leveraging",
	"ecosystem",
	"empower",
	"unlock",
	"revolutionize",
	"transform your",
	"game-changing",
	"game changing",
	"circle back",
	"let's connect",
	"synerg",
}

// Warnings returns one label per problem found in text — empty when nothing looks off. Labels
// are meant to be logged/stored (review.md §8's decision_reason), not shown verbatim to a
// reader.
func Warnings(text string) []string {
	var warnings []string
	lower := strings.ToLower(text)

	for _, phrase := range bannedPhrases {
		if strings.Contains(lower, phrase) {
			warnings = append(warnings, "banned_phrase:"+phrase)
		}
	}

	if strings.Count(text, "!") > 1 {
		warnings = append(warnings, "excessive_punctuation")
	}
	if strings.Count(text, "—")+strings.Count(text, "--") > 2 {
		warnings = append(warnings, "excessive_em_dashes")
	}
	if strings.Contains(text, "[") || strings.Contains(text, "{{") {
		warnings = append(warnings, "placeholder_text")
	}

	return warnings
}
