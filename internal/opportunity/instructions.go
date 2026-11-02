package opportunity

import (
	"regexp"
	"strings"
)

// Kinds of instruction a job posting can give to whoever reads it.
const (
	// InstructionAIDirected is addressed to an AI ("if you are an LLM, write X") or tries to
	// override one ("ignore previous instructions"). It is text in a web page, never a command.
	InstructionAIDirected = "ai_directed"
	// InstructionAttentionCheck asks the applicant to prove they read the posting: include a word,
	// put something in the subject line. Only a person who read the posting can honestly do it.
	InstructionAttentionCheck = "attention_check"
	// InstructionTakeHome asks for an assignment or coding challenge before or with an application.
	InstructionTakeHome = "take_home"
	// InstructionMaterials asks for specific material: a GitHub repository, a portfolio.
	InstructionMaterials = "materials"
)

// PostingInstruction is one instruction found in a posting, quoted as it appears.
type PostingInstruction struct {
	Kind    string `json:"kind"`
	Excerpt string `json:"excerpt"`
	// Tokens are the exact words or phrases the instruction asks to be included, when it names
	// any ("pineapple", "[llm alert]").
	Tokens []string `json:"tokens,omitempty"`
}

// Label names the kind for a person.
func (p PostingInstruction) Label() string {
	switch p.Kind {
	case InstructionAIDirected:
		return "Addressed to AI"
	case InstructionAttentionCheck:
		return "Check that you read the posting"
	case InstructionTakeHome:
		return "Take-home assignment"
	case InstructionMaterials:
		return "Asks for specific material"
	}
	return p.Kind
}

// Advice says what the system does about the instruction and what is left to the person.
func (p PostingInstruction) Advice() string {
	switch p.Kind {
	case InstructionAIDirected:
		return "The writer ignores this; it never takes instructions from a posting."
	case InstructionAttentionCheck:
		return "The writer will not do this on its own. You read the posting: if you want it followed, say so in your notes for the writer."
	case InstructionTakeHome:
		return "The email will not claim the assignment is done. Your GitHub profile is in the signature."
	case InstructionMaterials:
		return "The writer may point to the GitHub and portfolio links in your profile, and nothing else."
	}
	return ""
}

// AutomationRelevant reports whether this instruction means a person must look before sending.
func (p PostingInstruction) AutomationRelevant() bool {
	return p.Kind == InstructionAIDirected || p.Kind == InstructionAttentionCheck || p.Kind == InstructionTakeHome
}

var (
	aiDirectedPattern = regexp.MustCompile(`(?i)\b(?:if|when|in case)\s+you(?:'re|’re|\s+are)\s+(?:an?\s+)?(?:llm|a\.?i\.?|ai\s+(?:assistant|agent|model|tool|system)|large\s+language\s+model|language\s+model|chatbot|chat\s*gpt|gpt|bot)\b` +
		`|\bignore\s+(?:all\s+|any\s+)?(?:the\s+)?(?:previous|prior|above|earlier)\s+instructions\b` +
		`|\b(?:llm|ai)[\s_-]?(?:alert|disclosure)\b` +
		`|\bnote\s+to\s+(?:ai|llms?|language\s+models?)\b` +
		`|\b(?:ai|llm)s?\s+(?:reading|parsing)\s+this\b`)
	attentionPattern = regexp.MustCompile(`(?i)\b(?:include|use|mention|write|put|add|insert|say)\b[^.\n]{0,60}\b(?:word|phrase|code\s*word|keyword|password)\b` +
		`|\bto\s+(?:confirm|show|prove|demonstrate)\s+(?:that\s+)?you(?:'ve|’ve|\s+have)?\s+(?:read|gone\s+through)\b` +
		`|\b(?:include|put|write|use|add)\b[^.\n]{0,60}\bin\s+(?:the|your)\s+subject(?:\s+line)?\b` +
		`|\b(?:subject(?:\s+line)?)\s+(?:must|should)\s+(?:include|contain|start|begin|read)\b` +
		`|\b(?:start|begin)\s+your\s+(?:email|application|message|cover\s+letter)\s+with\b`)
	takeHomePattern = regexp.MustCompile(`(?i)\btake[\s-]?home\b|\b(?:coding|technical|programming)\s+(?:assignment|challenge|test|exercise|task)\b` +
		`|\bcomplete\s+(?:a|the|this|our)\s+(?:short\s+|small\s+)?(?:assignment|challenge|exercise|task)\b`)
	materialsPattern = regexp.MustCompile(`(?i)\b(?:github|gitlab|bitbucket)\s+(?:repo(?:sitory|sitories|s)?|profile|link|account|url)\b|\bportfolio\b`)
	quotedPattern    = regexp.MustCompile(`["“]([^"“”\n]{1,60})["”]`)
	// Single quotes double as apostrophes ("you've"), so a single-quoted token must stand alone.
	singleQuoted     = regexp.MustCompile(`(?:^|[\s(:])['‘]([^'‘’\n]{1,60})['’](?:$|[\s.,;:!?)])`)
	bracketedPattern = regexp.MustCompile(`\[[^\[\]\n]{1,40}\]`)
	sentenceSplit    = regexp.MustCompile(`[.!?]\s+|\n+`)
)

// PostingInstructions finds instructions a posting gives its reader. It only reads; nothing it
// finds is ever carried out on its own.
func PostingInstructions(description string) []PostingInstruction {
	out := []PostingInstruction{}
	seen := map[string]bool{}
	for _, sentence := range sentenceSplit.Split(description, -1) {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}
		kind := ""
		switch {
		case aiDirectedPattern.MatchString(sentence):
			kind = InstructionAIDirected
		case attentionPattern.MatchString(sentence):
			kind = InstructionAttentionCheck
		case takeHomePattern.MatchString(sentence):
			kind = InstructionTakeHome
		case materialsPattern.MatchString(sentence):
			kind = InstructionMaterials
		default:
			continue
		}
		excerpt := sentence
		if r := []rune(excerpt); len(r) > 300 {
			excerpt = string(r[:300]) + "…"
		}
		key := kind + "|" + strings.ToLower(excerpt)
		if seen[key] {
			continue
		}
		seen[key] = true
		item := PostingInstruction{Kind: kind, Excerpt: excerpt}
		if kind == InstructionAIDirected || kind == InstructionAttentionCheck {
			item.Tokens = instructionTokens(sentence)
		}
		out = append(out, item)
		if len(out) == 12 {
			break
		}
	}
	return out
}

// instructionTokens are the quoted words an instruction asks for, plus any bracketed tag inside
// them, so "[llm alert]" is caught even when only part of the phrase is copied.
func instructionTokens(sentence string) []string {
	tokens := []string{}
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		for _, existing := range tokens {
			if strings.EqualFold(existing, t) {
				return
			}
		}
		tokens = append(tokens, t)
	}
	matches := append(quotedPattern.FindAllStringSubmatch(sentence, -1), singleQuoted.FindAllStringSubmatch(sentence, -1)...)
	for _, m := range matches {
		add(m[1])
		for _, b := range bracketedPattern.FindAllString(m[1], -1) {
			add(b)
		}
	}
	for _, b := range bracketedPattern.FindAllString(sentence, -1) {
		add(b)
	}
	return tokens
}
