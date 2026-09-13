package opportunity

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Requirement types. Anything the bounded dictionary does not recognize stays TypeUnknown and
// remains visible: failing to parse a requirement is never evidence that none exists.
const (
	TypeTechnology     = "technology"
	TypeDatabase       = "database"
	TypeCloud          = "cloud_infrastructure"
	TypeIntegrationAPI = "integration_api"
	TypeBackend        = "backend_capability"
	TypeFrontend       = "frontend_capability"
	TypeExperience     = "experience"
	TypeEligibility    = "employment_eligibility"
	TypeUnknown        = "unknown"
)

const (
	maxRequirements   = 200
	maxExcerptRunes   = 400
	maxScannedLines   = 600
	minSubstantiveReq = 3
)

// Requirement is one conservatively extracted demand from a stored opportunity revision,
// always carrying the verbatim source line that supports it.
type Requirement struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Term      string   `json:"term"`
	Concepts  []string `json:"concepts,omitempty"`
	Excerpt   string   `json:"excerpt"`
	Offset    int      `json:"offset"`
	Mandatory bool     `json:"mandatory"`
	Section   string   `json:"section,omitempty"`
}

type dictEntry struct {
	phrases   []string
	canonical string
	kind      string
	concepts  []string
}

// TechnologyVocabulary exposes the existing bounded dictionary for composition
// claim checks; it does not classify roles or infer candidate proficiency.
func TechnologyVocabulary() map[string][]string {
	out := map[string][]string{}
	for _, entry := range dictionary {
		if entry.kind == TypeTechnology || entry.kind == TypeDatabase || entry.kind == TypeCloud || (entry.kind == TypeFrontend && entry.canonical != "Frontend development") {
			out[entry.canonical] = append([]string{entry.canonical}, entry.phrases...)
		}
	}
	return out
}

// dictionary is deliberately bounded and hand-checked. It recognizes demands; it does not
// judge them, and it never grows itself from source text.
var dictionary = []dictEntry{
	{[]string{"golang", "go programming", "go language"}, "Go", TypeTechnology, []string{"go", "golang"}},
	{[]string{"python"}, "Python", TypeTechnology, []string{"python"}},
	{[]string{"java"}, "Java", TypeTechnology, []string{"java"}},
	{[]string{"kotlin"}, "Kotlin", TypeTechnology, []string{"kotlin"}},
	{[]string{"typescript"}, "TypeScript", TypeTechnology, []string{"typescript"}},
	{[]string{"javascript"}, "JavaScript", TypeTechnology, []string{"javascript"}},
	{[]string{"node.js", "nodejs", "node js"}, "Node.js", TypeTechnology, []string{"node", "nodejs", "node.js"}},
	{[]string{"ruby on rails", "rails"}, "Ruby on Rails", TypeTechnology, []string{"rails", "ruby"}},
	{[]string{"ruby"}, "Ruby", TypeTechnology, []string{"ruby"}},
	{[]string{"php"}, "PHP", TypeTechnology, []string{"php"}},
	{[]string{"laravel"}, "Laravel", TypeTechnology, []string{"laravel", "php"}},
	{[]string{"django"}, "Django", TypeTechnology, []string{"django", "python"}},
	{[]string{"rust"}, "Rust", TypeTechnology, []string{"rust"}},
	{[]string{"elixir"}, "Elixir", TypeTechnology, []string{"elixir"}},
	{[]string{"c#", ".net", "dotnet"}, ".NET", TypeTechnology, []string{"c#", ".net", "dotnet"}},
	{[]string{"react native"}, "React Native", TypeFrontend, []string{"react native", "mobile"}},
	{[]string{"react.js", "reactjs", "react"}, "React", TypeFrontend, []string{"react"}},
	{[]string{"vue.js", "vuejs", "vue"}, "Vue", TypeFrontend, []string{"vue"}},
	{[]string{"angular"}, "Angular", TypeFrontend, []string{"angular"}},
	{[]string{"next.js", "nextjs"}, "Next.js", TypeFrontend, []string{"next.js", "react"}},
	{[]string{"tailwind"}, "Tailwind CSS", TypeFrontend, []string{"tailwind", "css"}},
	{[]string{"html", "css"}, "HTML/CSS", TypeFrontend, []string{"html", "css"}},
	{[]string{"flutter"}, "Flutter", TypeFrontend, []string{"flutter", "mobile"}},
	{[]string{"responsive design", "ui development", "front-end development", "frontend development"}, "Frontend development", TypeFrontend, []string{"frontend", "front-end", "ui"}},

	{[]string{"postgresql", "postgres"}, "PostgreSQL", TypeDatabase, []string{"postgresql", "postgres", "sql", "relational database", "database"}},
	{[]string{"mysql"}, "MySQL", TypeDatabase, []string{"mysql", "sql", "database"}},
	{[]string{"mongodb", "mongo"}, "MongoDB", TypeDatabase, []string{"mongodb", "nosql", "database"}},
	{[]string{"redis"}, "Redis", TypeDatabase, []string{"redis", "cache"}},
	{[]string{"elasticsearch"}, "Elasticsearch", TypeDatabase, []string{"elasticsearch", "search"}},
	{[]string{"sql"}, "SQL", TypeDatabase, []string{"sql", "database", "relational database"}},
	{[]string{"database design", "schema design", "data modelling", "data modeling"}, "Database design", TypeDatabase, []string{"database design", "schema", "data model", "database"}},

	{[]string{"kubernetes", "k8s"}, "Kubernetes", TypeCloud, []string{"kubernetes", "containers"}},
	{[]string{"docker"}, "Docker", TypeCloud, []string{"docker", "containers"}},
	{[]string{"terraform"}, "Terraform", TypeCloud, []string{"terraform", "infrastructure"}},
	{[]string{"aws", "amazon web services"}, "AWS", TypeCloud, []string{"aws", "cloud"}},
	{[]string{"gcp", "google cloud"}, "Google Cloud", TypeCloud, []string{"gcp", "cloud"}},
	{[]string{"azure"}, "Azure", TypeCloud, []string{"azure", "cloud"}},
	{[]string{"ci/cd", "continuous integration", "continuous delivery"}, "CI/CD", TypeCloud, []string{"ci/cd", "continuous integration", "deployment"}},
	{[]string{"linux"}, "Linux", TypeCloud, []string{"linux"}},

	{[]string{"graphql"}, "GraphQL", TypeIntegrationAPI, []string{"graphql", "api"}},
	{[]string{"grpc"}, "gRPC", TypeIntegrationAPI, []string{"grpc", "api", "rpc"}},
	{[]string{"rest api", "restful api", "rest apis", "restful apis", "restful", "rest endpoints"}, "REST APIs", TypeIntegrationAPI, []string{"rest", "api", "apis"}},
	{[]string{"api development", "api design", "apis", "api"}, "API development", TypeIntegrationAPI, []string{"api", "apis"}},
	{[]string{"webhook", "webhooks"}, "Webhooks", TypeIntegrationAPI, []string{"webhook", "api"}},
	{[]string{"third-party integration", "third party integration", "system integration"}, "Third-party integrations", TypeIntegrationAPI, []string{"integration", "api"}},
	{[]string{"m-pesa", "mpesa", "daraja"}, "M-Pesa integration", TypeIntegrationAPI, []string{"m-pesa", "mpesa", "payments", "payment"}},
	{[]string{"stripe"}, "Stripe integration", TypeIntegrationAPI, []string{"stripe", "payments", "payment"}},
	{[]string{"paypal"}, "PayPal integration", TypeIntegrationAPI, []string{"paypal", "payments", "payment"}},
	{[]string{"payment gateway", "payment integration", "payment integrations", "payments integration"}, "Payment integration", TypeIntegrationAPI, []string{"payments", "payment", "payment integration"}},
	{[]string{"payment reconciliation", "payments reconciliation", "settlement reconciliation"}, "Payment reconciliation", TypeIntegrationAPI, []string{"reconciliation", "payments", "payment"}},

	{[]string{"microservices", "micro-services"}, "Microservices", TypeBackend, []string{"microservices", "distributed systems", "services"}},
	{[]string{"distributed systems", "distributed system"}, "Distributed systems", TypeBackend, []string{"distributed systems", "distributed"}},
	{[]string{"message queue", "message queues", "kafka", "rabbitmq", "event-driven", "event driven"}, "Asynchronous messaging", TypeBackend, []string{"queue", "messaging", "event-driven", "distributed systems"}},
	{[]string{"concurrency", "concurrent programming", "goroutines"}, "Concurrency", TypeBackend, []string{"concurrency", "concurrent"}},
	{[]string{"backend development", "back-end development", "server-side development", "server side"}, "Backend development", TypeBackend, []string{"backend", "back-end", "server-side"}},
	{[]string{"authentication", "authorization", "oauth", "jwt"}, "Authentication and authorization", TypeBackend, []string{"authentication", "authorization", "auth"}},
	{[]string{"scalability", "scalable systems", "high availability"}, "Scalability", TypeBackend, []string{"scalability", "scalable", "availability"}},
	{[]string{"background jobs", "cron jobs", "job queue", "worker queue", "worker pool", "sidekiq", "celery"}, "Background processing", TypeBackend, []string{"background jobs", "workers", "queue"}},
	{[]string{"unit tests", "unit testing", "automated testing", "test automation", "integration tests", "test coverage"}, "Automated testing", TypeBackend, []string{"testing", "tests"}},
	{[]string{"observability", "monitoring", "logging"}, "Observability", TypeBackend, []string{"monitoring", "logging", "observability"}},
	{[]string{"transaction", "transactions", "acid"}, "Transactional correctness", TypeBackend, []string{"transaction", "transactions"}},
}

// goContext keeps the two-letter language name from matching ordinary English. Bare "go" is
// only read as the language when the same line is unmistakably about software.
var goContext = []string{
	"developer", "development", "engineer", "engineering", "programming", "language",
	"backend", "back-end", "server", "api", "apis", "microservices", "concurrency",
	"goroutine", "postgres", "sql", "docker", "kubernetes", "grpc", "software",
}

// entryVetoes are phrases that make a dictionary term mean something other than the demand it
// normally names. "Work Authorization" is an eligibility heading, not a requirement to build
// authorization; it was matching in real stored descriptions.
var entryVetoes = map[string][]string{
	"Authentication and authorization": {"work authorization", "employment authorization", "right to work", "authorization to work"},
}

// goIdiom lists the ordinary English phrases that would otherwise be read as the language.
var goIdiom = []string{
	"go the extra mile", "go-to", "go to market", "go-getter", "go live", "on the go",
	"go above", "go beyond", "ready to go", "go-live", "go the extra",
}

// goPrepositions precede a named technology rather than a verb: "experience with Go",
// "proficient in Go", "built using Go".
var goPrepositions = map[string]bool{
	"with": true, "in": true, "using": true, "of": true, "and": true, "or": true,
	"like": true, "including": true, "e.g.": true, "eg": true, "ideally": true, "preferably": true,
}

// goFollowers turn the word into an unmistakable technology reference on their own.
var goFollowers = map[string]bool{
	"developer": true, "developers": true, "engineer": true, "engineers": true,
	"programming": true, "language": true, "backend": true, "services": true,
	"microservices": true, "routines": true, "modules": true, "codebase": true, "code": true,
}

// looksLikeGoLanguage decides whether a bare "go" on this line is the programming language.
// A line about going the extra mile is not a Go requirement, and treating it as one would
// invent a matched technology out of ordinary English.
func looksLikeGoLanguage(scan string, otherTechOnLine, experienceCue bool) bool {
	if containsAny(scan, goIdiom) || !containsPhrase(scan, "go") {
		return false
	}
	if containsAny(scan, goContext) {
		return true
	}
	words := strings.Fields(scan)
	for i, word := range words {
		if strings.Trim(word, ",.;:()") != "go" {
			continue
		}
		if i+1 < len(words) && goFollowers[strings.Trim(words[i+1], ",.;:()")] {
			return true
		}
		if i > 0 && goPrepositions[strings.Trim(words[i-1], ",.;:()")] && (otherTechOnLine || experienceCue) {
			return true
		}
	}
	return false
}

var (
	bulletPrefix   = regexp.MustCompile(`^\s*(?:[-*\x{2022}\x{00b7}\x{2023}\x{25cf}\x{25aa}\x{2043}\x{2013}\x{2014}]|\d+[.)])\s+`)
	yearsOfExp     = regexp.MustCompile(`(\d{1,2})\s*\+?\s*(?:-\s*\d{1,2}\s*)?\s*(?:\+\s*)?years?`)
	hyphenVariants = strings.NewReplacer(
		"‐", "-", "‑", "-", "‒", "-", "–", "-",
		"—", "-", "―", "-", "−", "-", "­", "-", "⁃", "-",
	)
)

// mandatorySections and optionalSections classify requirement headings so a "nice to have"
// gap is never treated as an unmet mandatory requirement.
var mandatorySections = []string{
	"requirement", "required", "qualification", "must have", "must-have", "minimum",
	"what we are looking for", "what we're looking for", "skills", "essential", "you have",
	"who you are", "experience", "technical skills",
}

var optionalSections = []string{
	"nice to have", "nice-to-have", "bonus", "preferred", "plus", "desirable",
	"good to have", "advantageous", "would be a plus",
}

var responsibilitySections = []string{
	"responsibilit", "what you will do", "what you'll do", "the role", "duties",
	"about the role", "your role", "day to day", "day-to-day",
}

// ignoredSections never contain candidate requirements. Reading them would manufacture demands
// out of perks and boilerplate.
var ignoredSections = []string{
	"benefit", "perks", "how to apply", "about us", "about the company", "our story",
	"equal opportunity", "compensation", "salary", "why join", "we offer", "our values",
	"disclaimer", "privacy",
}

var experienceCues = []string{
	"experience", "proficien", "knowledge of", "expertise", "skilled in",
	"strong understanding", "years of", "must have", "ability to", "comfortable with",
	"familiar",
	"hands-on", "hands on", "working with", "solid grasp", "track record",
	// Spanish and Portuguese, because the remote boards genuinely carry both.
	"experiencia", "experiência", "conocimiento", "conhecimento", "dominio",
}

// stackSignals mark a sentence that states what is built, used or operated, as opposed to a
// sentence about the company. Real stored descriptions put the whole stack in ordinary prose —
// "We use Go, TypeScript, React, Expo, GraphQL, Postgres, and run on GCP" — and reading only
// bullets and requirement sections lost all of it.
var stackSignals = []string{
	"we use", "we work with", "our stack", "stack is", "tech stack", "runs on", "run on",
	"own the", "owns the", "owning the",
	"written in", "built with", "built on", "built using", "you will work", "you'll work",
	"you will build", "you'll build", "you will own", "you'll own", "you will be building",
	"responsible for", "you will", "you'll",
	"build", "building", "develop", "developing", "design", "designing", "implement",
	"implementing", "maintain", "maintaining", "architect", "operate", "deploy", "deploying",
	"manage", "managing", "optimize", "optimise", "automate", "integrate", "integrating",
	"migrate", "scale", "scaling", "using", "utilizing", "utilising", "work across",
	// Spanish and Portuguese equivalents seen in real listings.
	"desarrollar", "construir", "implementar", "mantener", "utilizando", "trabajar",
	"desenvolver", "manter", "utilizar",
}

// labelledStackLine matches a short "Core:", "Cloud:", "Data:" style label introducing a list of
// technologies, which real listings use instead of a sentence.
var labelledStackLine = regexp.MustCompile(`^\s*[A-Za-zÀ-ÿ][A-Za-zÀ-ÿ /&-]{1,24}:\s*\S`)

// SectionProse marks a requirement recovered from ordinary prose rather than from a bullet or a
// requirements section. Such a requirement is real evidence of a demand, but it is never treated
// as mandatory, because prose does not say whether it is essential or merely nice to have.
const SectionProse = "prose"

var eligibilityCues = map[string][]string{
	"location":      {"must be based", "based in", "located in", "relocat", "on-site", "onsite", "in-office", "hybrid", "remote"},
	"authorization": {"work authorization", "eligible to work", "right to work", "visa", "work permit", "citizen"},
	"employment":    {"full-time", "full time", "part-time", "part time", "contract", "contractor", "freelance", "internship", "permanent", "temporary"},
}

// normalizeForScan lowercases, unifies Unicode hyphen variants and collapses whitespace so the
// dictionary sees one spelling of each term.
func normalizeForScan(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(hyphenVariants.Replace(s)), " "))
}

func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// containsPhrase matches a whole term rather than a substring, so "go" never matches "ongoing"
// and "rest" never matches "interested".
func containsPhrase(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for start := 0; ; {
		i := strings.Index(haystack[start:], needle)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(needle)
		leftOK := i == 0 || !isWordChar(haystack[i-1])
		rightOK := end == len(haystack) || !isWordChar(haystack[end])
		if leftOK && rightOK {
			return true
		}
		start = i + 1
		if start >= len(haystack) {
			return false
		}
	}
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

func boundedExcerpt(line string) string {
	runes := []rune(line)
	if len(runes) <= maxExcerptRunes {
		return line
	}
	return string(runes[:maxExcerptRunes]) + "…"
}

func sectionKind(line string) string {
	trimmed := normalizeForScan(strings.TrimSuffix(strings.TrimSpace(line), ":"))
	if trimmed == "" || len([]rune(trimmed)) > 60 {
		return ""
	}
	for _, s := range ignoredSections {
		if strings.Contains(trimmed, s) {
			return "ignored"
		}
	}
	for _, s := range optionalSections {
		if strings.Contains(trimmed, s) {
			return "optional"
		}
	}
	for _, s := range responsibilitySections {
		if strings.Contains(trimmed, s) {
			return "responsibilities"
		}
	}
	for _, s := range mandatorySections {
		if strings.Contains(trimmed, s) {
			return "mandatory"
		}
	}
	return ""
}

// ExtractRequirements reads demands from a stored description. It only reads lines that are
// plausibly requirements, keeps the exact source line behind every extracted term, and leaves
// anything it cannot parse visible as an unknown requirement.
func ExtractRequirements(description string) []Requirement {
	if strings.TrimSpace(description) == "" {
		return nil
	}
	var requirements []Requirement
	section := ""
	offset := 0
	seen := map[string]bool{}
	lines := strings.Split(description, "\n")
	if len(lines) > maxScannedLines {
		lines = lines[:maxScannedLines]
	}
	for _, raw := range lines {
		lineOffset := offset
		offset += len(raw) + 1
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		bulleted := bulletPrefix.MatchString(raw)
		body := bulletPrefix.ReplaceAllString(raw, "")
		// Only an unbulleted line can be a section heading. A bullet is always a requirement,
		// even a short one like "- Strong experience with Go", which an earlier version of this
		// scanner silently swallowed as a heading.
		headingLine := false
		if !bulleted {
			if kind := sectionKind(line); kind != "" {
				section = kind
				headingLine = true
				// A heading that is only a heading contributes no requirement of its own.
				if strings.HasSuffix(line, ":") {
					continue
				}
			}
		}
		if section == "ignored" {
			continue
		}
		scan := normalizeForScan(body)
		if scan == "" {
			continue
		}
		cued := containsAny(scan, experienceCues)
		prose := false
		if !bulleted && section == "" && !cued {
			// A prose sentence is read only when it both states what is built or used and
			// actually names something the dictionary recognizes. That keeps "We use Go,
			// TypeScript and Postgres" while leaving company and benefits prose alone.
			if !stackSentence(scan, line) || !lineNamesKnownTerm(scan, cued) {
				continue
			}
			prose = true
		}
		mandatory := section == "mandatory" || section == "responsibilities" ||
			strings.Contains(scan, "must have") || strings.Contains(scan, "required") ||
			strings.Contains(scan, "minimum")
		if section == "optional" || prose {
			mandatory = false
		}
		lineSection := section
		if prose {
			lineSection = SectionProse
		}
		excerpt := boundedExcerpt(strings.TrimSpace(body))
		found := false
		record := func(entry dictEntry, term string) {
			found = true
			key := entry.kind + "|" + term
			if seen[key] {
				return
			}
			seen[key] = true
			requirements = append(requirements, Requirement{
				Type: entry.kind, Term: term, Concepts: entry.concepts,
				Excerpt: excerpt, Offset: lineOffset, Mandatory: mandatory, Section: lineSection,
			})
		}
		// Everything except the bare language name first, so the "go" decision below can see
		// whether this line names any other technology at all.
		otherTechOnLine := false
		var goEntry dictEntry
		for _, entry := range dictionary {
			if entry.canonical == "Go" {
				goEntry = entry
				if matchPhrases(entry, scan) {
					record(entry, entry.canonical)
					goEntry = dictEntry{}
				}
				continue
			}
			if matchPhrases(entry, scan) {
				otherTechOnLine = true
				record(entry, entry.canonical)
			}
		}
		if goEntry.canonical == "Go" && looksLikeGoLanguage(scan, otherTechOnLine, cued) {
			record(goEntry, goEntry.canonical)
		}
		if m := yearsOfExp.FindStringSubmatch(scan); m != nil {
			if years, err := strconv.Atoi(m[1]); err == nil && years > 0 && years < 40 {
				key := TypeExperience + "|" + m[1]
				if !seen[key] {
					seen[key] = true
					found = true
					requirements = append(requirements, Requirement{
						Type: TypeExperience, Term: m[1] + " years of experience", Concepts: []string{"experience"},
						Excerpt: excerpt, Offset: lineOffset, Mandatory: mandatory, Section: section,
					})
				}
			}
		}
		for kind, cues := range eligibilityCues {
			for _, cue := range cues {
				if !strings.Contains(scan, cue) {
					continue
				}
				key := TypeEligibility + "|" + kind + "|" + cue
				if seen[key] {
					break
				}
				seen[key] = true
				found = true
				requirements = append(requirements, Requirement{
					Type: TypeEligibility, Term: kind + ": " + cue, Concepts: []string{kind},
					Excerpt: excerpt, Offset: lineOffset, Mandatory: mandatory, Section: section,
				})
				break
			}
		}
		// A heading that also reads as prose stays eligible for dictionary terms, but it is not
		// reported as an unparsed requirement of its own.
		if !found && (bulleted || cued) && !headingLine {
			requirements = append(requirements, Requirement{
				Type: TypeUnknown, Term: "", Excerpt: excerpt, Offset: lineOffset,
				Mandatory: mandatory, Section: section,
			})
		}
		if len(requirements) >= maxRequirements {
			break
		}
	}
	for i := range requirements {
		requirements[i].ID = "req-" + strconv.Itoa(i+1)
	}
	return requirements
}

// stackSentence reports whether a line states what is built, used or operated.
func stackSentence(scan, raw string) bool {
	return containsAny(scan, stackSignals) || labelledStackLine.MatchString(strings.TrimSpace(raw))
}

// lineNamesKnownTerm reports whether the dictionary recognizes anything on this line, so a
// stack-shaped sentence that names nothing concrete is still ignored.
func lineNamesKnownTerm(scan string, cued bool) bool {
	otherTech := false
	for _, entry := range dictionary {
		if entry.canonical == "Go" {
			continue
		}
		if matchPhrases(entry, scan) {
			otherTech = true
			break
		}
	}
	return otherTech || looksLikeGoLanguage(scan, otherTech, cued)
}

// matchPhrases reports whether any explicit dictionary spelling appears on this line, unless a
// veto phrase shows the term means something else here.
func matchPhrases(entry dictEntry, scan string) bool {
	if containsAny(scan, entryVetoes[entry.canonical]) {
		return false
	}
	for _, phrase := range entry.phrases {
		if containsPhrase(scan, phrase) {
			return true
		}
	}
	return false
}

// Substantive counts requirements the dictionary actually recognized. Unknown lines are
// deliberately excluded: they are visible gaps in parsing, not established demands.
func Substantive(requirements []Requirement) int {
	n := 0
	for _, r := range requirements {
		if r.Type != TypeUnknown {
			n++
		}
	}
	return n
}

// SkippedLine is a source line the extractor deliberately did not read, together with any
// recognized terms it nonetheless contains. It exists so a parsing gap can be demonstrated
// against real stored descriptions before any rule is broadened.
type SkippedLine struct {
	Line    string   `json:"line"`
	Offset  int      `json:"offset"`
	Reason  string   `json:"reason"`
	Section string   `json:"section,omitempty"`
	Terms   []string `json:"terms,omitempty"`
}

// Diagnostics reports what the extractor read, what it skipped, and which recognized terms are
// present in the source but never became requirements.
type Diagnostics struct {
	Requirements []Requirement `json:"requirements"`
	Skipped      []SkippedLine `json:"skipped"`
	// MissedTerms are dictionary terms present in a skipped line and in no extracted
	// requirement. Each one is a concrete, reproducible extraction gap.
	MissedTerms []string `json:"missed_terms,omitempty"`
	Lines       int      `json:"lines"`
	BulletLines int      `json:"bullet_lines"`
	ProseLines  int      `json:"prose_lines"`
}

// Diagnose runs the same line decisions as ExtractRequirements and records the ones that reject
// a line, so calibration can show exactly which real requirements are being missed and why.
// It changes no behavior and is read-only.
func Diagnose(description string) Diagnostics {
	d := Diagnostics{Requirements: ExtractRequirements(description)}
	extracted := map[string]bool{}
	for _, r := range d.Requirements {
		if r.Term != "" {
			extracted[r.Term] = true
		}
	}
	missed := map[string]bool{}
	section := ""
	offset := 0
	lines := strings.Split(description, "\n")
	if len(lines) > maxScannedLines {
		lines = lines[:maxScannedLines]
	}
	for _, raw := range lines {
		lineOffset := offset
		offset += len(raw) + 1
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		d.Lines++
		bulleted := bulletPrefix.MatchString(raw)
		if bulleted {
			d.BulletLines++
		} else {
			d.ProseLines++
		}
		body := bulletPrefix.ReplaceAllString(raw, "")
		reason := ""
		if !bulleted {
			if kind := sectionKind(line); kind != "" {
				section = kind
				if strings.HasSuffix(line, ":") {
					continue // a bare heading carries no requirement of its own
				}
			}
		}
		if section == "ignored" {
			reason = "ignored_section"
		}
		scan := normalizeForScan(body)
		if scan == "" {
			continue
		}
		cued := containsAny(scan, experienceCues)
		if reason == "" && !bulleted && section == "" && !cued &&
			!(stackSentence(scan, line) && lineNamesKnownTerm(scan, cued)) {
			reason = "prose_outside_any_requirement_section"
		}
		if reason == "" {
			continue // the line was read
		}
		skipped := SkippedLine{Line: boundedExcerpt(strings.TrimSpace(body)), Offset: lineOffset,
			Reason: reason, Section: section}
		for _, entry := range dictionary {
			if entry.canonical == "Go" {
				if looksLikeGoLanguage(scan, len(skipped.Terms) > 0, cued) {
					skipped.Terms = append(skipped.Terms, entry.canonical)
				}
				continue
			}
			if matchPhrases(entry, scan) {
				skipped.Terms = append(skipped.Terms, entry.canonical)
			}
		}
		if reason == "prose_outside_any_requirement_section" && len(skipped.Terms) == 0 {
			continue // ordinary prose with no recognized demand is correctly ignored
		}
		for _, term := range skipped.Terms {
			if !extracted[term] {
				missed[term] = true
			}
		}
		d.Skipped = append(d.Skipped, skipped)
	}
	d.MissedTerms = sortedStrings(missed)
	return d
}

func sortedStrings(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
