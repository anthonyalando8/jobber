package opportunity

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/profile"
)

// MatcherVersion changes whenever the rules below change, so an assessment stored by an older
// binary is never silently read as current.
const MatcherVersion = "fit-v2"

// Authoritative classifications. The numeric score is a ranking and debugging aid only; it
// never decides any of these and can never override ClassificationUnrelated.
const (
	ClassificationStrong          = "strong_match"
	ClassificationPossible        = "possible_match"
	ClassificationUnrelated       = "unrelated"
	ClassificationProfileRequired = "profile_required"
)

// Requirement match outcomes.
const (
	MatchSupported = "matched"
	MatchUnmet     = "unmet"
	MatchUnknown   = "unknown"
	MatchConflict  = "conflict"
)

const (
	FamilyClear     = "clear"
	FamilyAmbiguous = "ambiguous"
	FamilyNone      = "none"
)

// RequirementMatch is one requirement resolved against the approved profile, keeping both the
// source excerpt and the exact approved items that support it.
type RequirementMatch struct {
	Requirement Requirement `json:"requirement"`
	Outcome     string      `json:"outcome"`
	ProfileRefs []string    `json:"profile_refs,omitempty"`
	Note        string      `json:"note,omitempty"`
}

// MatchResult is the stored, explainable answer to "does this advertised job fit the approved
// profile, and why".
type MatchResult struct {
	MatcherVersion      string             `json:"matcher_version"`
	Classification      string             `json:"classification"`
	Score               int                `json:"score"`
	OpportunityID       int64              `json:"opportunity_id"`
	OpportunityRevision int                `json:"opportunity_revision"`
	ProfileRevisionID   int64              `json:"profile_revision_id"`
	OriginalTitle       string             `json:"original_title"`
	NormalizedTitle     string             `json:"normalized_title"`
	FamilyID            string             `json:"family_id,omitempty"`
	FamilyName          string             `json:"family_name,omitempty"`
	FamilyConfidence    string             `json:"family_confidence"`
	DescriptionStatus   string             `json:"description_status"`
	Matched             []RequirementMatch `json:"matched"`
	Gaps                []RequirementMatch `json:"gaps"`
	Unparsed            []Requirement      `json:"unparsed,omitempty"`
	SupportedCapability []string           `json:"supported_capability_ids,omitempty"`
	SupportingProjects  []string           `json:"supporting_project_ids,omitempty"`
	Reasons             explain.Reasons    `json:"reasons"`
	Blockers            explain.Reasons    `json:"blockers,omitempty"`
	AssessedAt          time.Time          `json:"assessed_at"`
}

// AllowsSpecificApplication is the single authoritative gate for the unrelated-role invariant.
// A specific job application is possible only when the advertised role really is in an approved
// desired family. No score, override or company status is consulted here, because none of them
// may participate in this decision.
func (m MatchResult) AllowsSpecificApplication() bool {
	return (m.Classification == ClassificationStrong || m.Classification == ClassificationPossible) &&
		m.FamilyConfidence != FamilyNone
}

// AutoSuggestsApplication reports whether the fit is strong enough to propose an application
// without the operator selecting it first. Possible matches always require explicit review.
func (m MatchResult) AutoSuggestsApplication() bool {
	return m.Classification == ClassificationStrong
}

// StatesPaymentRequirement reports whether the vacancy itself demands payment-engineering work.
// This is the only opportunity-side fact that may justify a payment-specific approach; a
// checkout on the company website is not one.
func (m MatchResult) StatesPaymentRequirement() bool {
	if m.Classification == ClassificationUnrelated || m.Classification == ClassificationProfileRequired {
		return false
	}
	for _, rm := range append(append([]RequirementMatch{}, m.Matched...), m.Gaps...) {
		for _, concept := range rm.Requirement.Concepts {
			switch concept {
			case "payment", "payments", "mpesa", "m-pesa", "payment integration", "reconciliation":
				return true
			}
		}
	}
	return false
}

// Stale reports whether this assessment still describes the current inputs.
func (m MatchResult) Stale(currentRevision int, activeProfileRevisionID int64) bool {
	return m.OpportunityRevision != currentRevision ||
		m.ProfileRevisionID != activeProfileRevisionID ||
		m.MatcherVersion != MatcherVersion
}

// roleNouns are the words that make a title a hands-on engineering job title. The Spanish and
// Portuguese forms are here because the remote boards genuinely carry them: a real stored
// listing, "DESARROLLADOR FULL STACK" advertising Python/Angular/Go, was rejected on language
// alone. Recognizing the noun does not relax any other gate — the title still needs an approved
// desired specialization to reach a clear family.
var roleNouns = map[string]bool{
	"engineer": true, "engineers": true, "engineering": true,
	"developer": true, "developers": true, "development": true,
	// Architects and site-reliability engineers are hands-on engineering titles too. A title
	// without one of these nouns is never promoted, whatever its description says.
	"architect": true, "architects": true, "sre": true, "swe": true,
	"programmer": true, "programmers": true, "programming": true, "coder": true,
	// Spanish
	"desarrollador": true, "desarrolladora": true, "desarrolladores": true,
	"ingeniero": true, "ingeniera": true, "programador": true, "programadora": true,
	"desarrollo": true,
	// Portuguese
	"desenvolvedor": true, "desenvolvedora": true, "desenvolvedores": true,
	"engenheiro": true, "engenheira": true, "desenvolvimento": true,
}

// disqualifyingFunctions are business functions and non-software disciplines. A title whose
// role designation names one of these is not an engineering vacancy, and no amount of
// incidental engineering vocabulary in the description may change that.
var disqualifyingFunctions = []string{
	"marketing", "sales", "business development", "finance", "financial", "accountant",
	"accounting", "accounts payable", "accounts receivable", "bookkeep", "procurement",
	"supply chain", "logistics", "human resource", "recruitment", "recruiter",
	"talent acquisition", "legal", "counsel", "compliance", "audit", "customer service",
	"customer support", "customer success", "call center", "call centre", "receptionist",
	"secretary", "driver", "nurse", "clinical", "teacher", "tutor", "lecturer", "cleaner",
	"security guard", "cashier", "teller", "storekeeper", "warehouse", "administrative",
	"office administrator", "public relations", "communications officer", "graphic design",
	"content writer", "copywriter", "social media", "brand", "merchandis", "underwriter",
	"actuar", "loan officer", "credit officer", "relationship officer", "claims",
	"product manager", "project manager", "program manager", "programme manager",
	"scrum master", "business analyst", "data entry", "operations manager",
	"mechanical", "civil engineer", "electrical", "electronic engineer", "chemical",
	"structural", "industrial engineer", "quantity survey", "surveyor", "technician",
}

var titleSeparators = []string{" - ", " — ", " – ", ",", "(", "|", ":", " at ", " @ "}

// listingQualifiers are posting decorations that some boards put in front of the actual role.
// Real example seen in stored data: "FULL TIME: Software Engineer Position - React and Rest",
// where reading the first segment as the role designation yields "full time" and downgrades a
// clear Software Engineer title to ambiguous. Only these exact qualifier segments are skipped;
// any other leading noun still counts as the role designation, so "Manager, Software
// Engineering" is not promoted.
var listingQualifiers = map[string]bool{
	"full time": true, "fulltime": true, "full-time": true, "part time": true,
	"parttime": true, "part-time": true, "contract": true, "freelance": true,
	"urgent": true, "urgently": true, "hiring": true, "now hiring": true,
	"we are hiring": true, "remote": true, "onsite": true, "on site": true,
	"hybrid": true, "new": true, "open": true, "job": true, "jobs": true,
	"vacancy": true, "vacancies": true, "career": true, "careers": true,
	"apply now": true, "immediate": true, "immediately": true, "featured": true,
}

// normalizeTitleForMatch unifies case, whitespace, Unicode hyphen variants and the several
// spellings of full-stack, while the original advertised title is always preserved separately.
func normalizeTitleForMatch(title string) string {
	s := strings.ToLower(hyphenVariants.Replace(title))
	s = strings.Join(strings.Fields(s), " ")
	s = strings.NewReplacer("full-stack", "full stack", "fullstack", "full stack",
		"back-end", "backend", "front-end", "frontend").Replace(s)
	// "Go" is the language's own name and is how most boards write it; profiles say "Golang".
	// Only the word directly before an engineering word is rewritten, so "go-to-market" and
	// "on the go" are untouched.
	return goBeforeRole.ReplaceAllString(s, "${1}golang ${2}")
}

var goBeforeRole = regexp.MustCompile(`(^|[^a-z0-9.])go (developer|engineer|programmer|backend|software|services|platform)`)

// leadPhrases make a title a hands-on engineering lead even without an engineer/developer noun.
var leadPhrases = []string{"tech lead", "technical lead", "engineering lead", "development lead"}

// managementWords mark titles whose work is managing people rather than building software. Such
// a title may still be ambiguous engineering, but its family is never inferred from requirements.
var managementWords = []string{"manager", "director", "head of", "vp", "vice president", "chief", "cto"}

func hasAnyPhrase(text string, phrases []string) bool {
	for _, p := range phrases {
		if containsPhrase(text, p) {
			return true
		}
	}
	return false
}

// headPhrase is the role designation: what comes before the first separator, skipping a leading
// posting qualifier such as "FULL TIME:". This is what the disqualifying-function check reads,
// so "Software Engineer - Marketing Platform" stays an engineering title while "Product
// Marketing Manager" does not.
func headPhrase(normalized string) string {
	remainder := normalized
	for range titleSeparators {
		head, rest := splitHead(remainder)
		if head == "" {
			return strings.TrimSpace(remainder)
		}
		if !listingQualifiers[head] || rest == "" {
			return head
		}
		remainder = rest
	}
	return strings.TrimSpace(remainder)
}

// splitHead returns the first separated segment and whatever follows it.
func splitHead(normalized string) (string, string) {
	head, rest := normalized, ""
	cut := len(normalized)
	width := 0
	for _, sep := range titleSeparators {
		if i := strings.Index(normalized, sep); i > 0 && i < cut {
			cut, width = i, len(sep)
		}
	}
	if cut < len(normalized) {
		head, rest = normalized[:cut], normalized[cut+width:]
	}
	return strings.TrimSpace(head), strings.TrimSpace(rest)
}

type familySpec struct {
	id, name string
	// specializations are the approved family words with the interchangeable role noun
	// stripped: "Backend Engineer" and "Backend Engineering" both yield "backend".
	specializations []string
}

// familySpecs derives matchable specializations from the approved desired families only. A
// family the operator has not approved can never match, and nothing here is inferred from
// legacy templates or code.
func familySpecs(families []profile.JobFamily) []familySpec {
	var specs []familySpec
	for _, f := range families {
		spec := familySpec{id: f.ID, name: f.Name}
		seen := map[string]bool{}
		for _, phrase := range append([]string{f.Name}, f.Aliases...) {
			normalized := normalizeTitleForMatch(phrase)
			words := strings.Fields(normalized)
			for len(words) > 0 && roleNouns[words[len(words)-1]] {
				words = words[:len(words)-1]
			}
			if len(words) == 0 {
				continue
			}
			specialization := strings.Join(words, " ")
			if !seen[specialization] {
				seen[specialization] = true
				spec.specializations = append(spec.specializations, specialization)
			}
		}
		if len(spec.specializations) > 0 {
			specs = append(specs, spec)
		}
	}
	// Longest specialization wins, so "full stack" is preferred over "software".
	sort.SliceStable(specs, func(i, j int) bool {
		return longest(specs[i].specializations) > longest(specs[j].specializations)
	})
	return specs
}

func longest(items []string) int {
	n := 0
	for _, item := range items {
		if len(item) > n {
			n = len(item)
		}
	}
	return n
}

// hasRoleNoun looks inside slash-joined words too, so the gendered forms these boards actually
// print — "Desarrollador/a", "Engineer/Developer" — are recognized.
func hasRoleNoun(phrase string) bool {
	for _, word := range strings.Fields(phrase) {
		for _, token := range strings.Split(strings.Trim(word, ".,;:()"), "/") {
			if roleNouns[token] {
				return true
			}
		}
	}
	return false
}

type gateResult struct {
	confidence, familyID, familyName, matched string
	// inferred is true when the family came from the requirements rather than the title.
	inferred bool
	reasons  explain.Reasons
}

// gateTitle is the first and decisive role-family check. The advertised title decides the
// family; the description is never allowed to promote an unrelated title.
func gateTitle(originalTitle string, specs []familySpec) gateResult {
	normalized := normalizeTitleForMatch(originalTitle)
	if strings.TrimSpace(normalized) == "" {
		return gateResult{confidence: FamilyNone, reasons: explain.Reasons{}.Add(
			"advertised_title_missing",
			"The source did not supply an advertised title, so no role family is established.")}
	}
	head := headPhrase(normalized)
	for _, fn := range disqualifyingFunctions {
		if strings.Contains(head, fn) {
			return gateResult{confidence: FamilyNone, reasons: explain.Reasons{}.Add(
				"unrelated_advertised_title",
				"The advertised role designation \""+head+"\" names the non-engineering function \""+fn+
					"\". Engineering vocabulary elsewhere in the description cannot change this.")}
		}
	}
	// "Backend Lead" and "Golang Lead" name the family and a hands-on lead role.
	if spec, phrase, ok := matchSpecialization(head, specs); ok && (hasRoleNoun(head) || containsPhrase(head, "lead")) {
		return gateResult{confidence: FamilyClear, familyID: spec.id, familyName: spec.name, matched: phrase,
			reasons: explain.Reasons{}.Add("desired_role_family_matched",
				"The advertised title states the approved desired family "+spec.name+" (\""+phrase+"\" with an engineering role noun).",
				"profile:role:"+spec.id)}
	}
	if hasRoleNoun(head) || hasAnyPhrase(head, leadPhrases) {
		return gateResult{confidence: FamilyAmbiguous, reasons: explain.Reasons{}.Add(
			"ambiguous_engineering_title",
			"The advertised title \""+head+"\" is an engineering role noun without an approved desired specialization, so the family is ambiguous.")}
	}
	// Only a title that is neither disqualified nor matched at the head is re-read in full,
	// which keeps "Manager, Software Engineering" reviewable without rescuing "Marketing
	// Manager, Software Campaign".
	if spec, phrase, ok := matchSpecialization(normalized, specs); ok && hasRoleNoun(normalized) {
		return gateResult{confidence: FamilyAmbiguous, familyID: spec.id, familyName: spec.name, matched: phrase,
			reasons: explain.Reasons{}.Add("engineering_family_outside_role_designation",
				"The approved family "+spec.name+" appears in the title but not in its role designation \""+head+"\", so the family is ambiguous.",
				"profile:role:"+spec.id)}
	}
	return gateResult{confidence: FamilyNone, reasons: explain.Reasons{}.Add(
		"unrelated_advertised_title",
		"The advertised title \""+head+"\" names no approved desired engineering family and no engineering role.")}
}

// inferFamily establishes the family of an engineering title that does not name one ("Platform
// Engineer", "Distributed Systems Engineer", "Engineer, Payments") from what the job actually
// asks for. It only applies to an ambiguous engineering title, never to an unrelated one or to a
// management title, and it needs at least two approved capabilities that real requirement
// excerpts support, with the family taken from those capabilities' own approved role families.
func inferFamily(gate gateResult, title string, capabilityIDs []string, doc profile.Document) (gateResult, bool) {
	if gate.confidence != FamilyAmbiguous || len(capabilityIDs) < minInferenceCapabilities {
		return gate, false
	}
	if hasAnyPhrase(normalizeTitleForMatch(title), managementWords) {
		return gate, false
	}
	desired := map[string]profile.JobFamily{}
	for _, f := range doc.DesiredRoles {
		desired[f.ID] = f
	}
	counts := map[string]int{}
	for _, id := range capabilityIDs {
		for _, c := range doc.Capabilities {
			if c.ID != id {
				continue
			}
			for _, family := range c.RoleFamilyIDs {
				if _, ok := desired[family]; ok {
					counts[family]++
				}
			}
		}
	}
	best, bestCount := "", 0
	for _, f := range doc.DesiredRoles { // profile order breaks ties, so the result is stable
		if counts[f.ID] > bestCount {
			best, bestCount = f.ID, counts[f.ID]
		}
	}
	if bestCount < minInferenceCapabilities {
		return gate, false
	}
	refs := []string{"profile:role:" + best}
	for _, id := range capabilityIDs {
		refs = append(refs, "profile:capability:"+id)
	}
	return gateResult{confidence: FamilyClear, inferred: true, familyID: best, familyName: desired[best].Name,
		reasons: explain.Reasons{}.Add("family_established_by_requirements",
			"The title does not name an approved family, but "+itoa(bestCount)+" approved "+desired[best].Name+
				" capabilities are supported by the job's own requirements, so the role is treated as "+desired[best].Name+".",
			refs...)}, true
}

// minInferenceCapabilities is how many supported capabilities establish a family from requirements.
const minInferenceCapabilities = 2

func matchSpecialization(phrase string, specs []familySpec) (familySpec, string, bool) {
	for _, spec := range specs {
		for _, specialization := range spec.specializations {
			if containsPhrase(phrase, specialization) {
				return spec, specialization, true
			}
		}
	}
	return familySpec{}, "", false
}

// AssessInput is everything the deterministic matcher is allowed to read.
type AssessInput struct {
	OpportunityID       int64
	OpportunityRevision int
	Facts               DiscoveredOpportunity
	// Profile is the active approved revision. A nil profile yields profile_required: real
	// matching may never run against an unapproved or inactive document.
	Profile           *profile.Document
	ProfileRevisionID int64
	Now               time.Time
}

// Assess is the deterministic, explainable matcher. It uses no AI and no company score.
func Assess(in AssessInput) MatchResult {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result := MatchResult{
		MatcherVersion:      MatcherVersion,
		OpportunityID:       in.OpportunityID,
		OpportunityRevision: in.OpportunityRevision,
		ProfileRevisionID:   in.ProfileRevisionID,
		OriginalTitle:       in.Facts.Title,
		NormalizedTitle:     normalizeTitleForMatch(in.Facts.Title),
		DescriptionStatus:   in.Facts.DescriptionStatus,
		FamilyConfidence:    FamilyNone,
		Matched:             []RequirementMatch{},
		Gaps:                []RequirementMatch{},
		AssessedAt:          now,
		Reasons:             explain.Reasons{},
	}
	if in.Profile == nil {
		result.Classification = ClassificationProfileRequired
		result.Reasons = result.Reasons.Add("profile_required",
			"No approved candidate profile revision is active. Matching cannot state a fit, and no contact plan may become ready.")
		return result
	}

	doc := *in.Profile
	gate := gateTitle(in.Facts.Title, familySpecs(doc.DesiredRoles))
	result.FamilyConfidence = gate.confidence
	result.FamilyID, result.FamilyName = gate.familyID, gate.familyName
	result.Reasons = append(result.Reasons, gate.reasons...)

	requirements := ExtractRequirements(in.Facts.Description)
	for _, req := range requirements {
		if req.Type == TypeUnknown {
			result.Unparsed = append(result.Unparsed, req)
			continue
		}
		rm := resolveRequirement(req, doc)
		switch rm.Outcome {
		case MatchSupported:
			result.Matched = append(result.Matched, rm)
		default:
			result.Gaps = append(result.Gaps, rm)
		}
	}

	capabilityIDs, projectIDs := supportedItems(result.Matched)
	result.SupportedCapability, result.SupportingProjects = capabilityIDs, projectIDs

	if inferred, ok := inferFamily(gate, in.Facts.Title, capabilityIDs, doc); ok {
		gate = inferred
		result.FamilyConfidence = gate.confidence
		result.FamilyID, result.FamilyName = gate.familyID, gate.familyName
		result.Reasons = append(result.Reasons, gate.reasons...)
	}

	if gate.confidence == FamilyNone {
		// An unrelated title is settled before any requirement or score is considered.
		result.Classification = ClassificationUnrelated
		result.Score = 0
		result.Blockers = result.Blockers.Add("unrelated_application_role",
			"This role is not in an approved desired engineering family, so it can never authorize a specific job application.")
		result.Reasons = append(result.Reasons, describeGaps(result.Gaps)...)
		if len(result.Unparsed) > 0 {
			result.Reasons = result.Reasons.Add("unparsed_requirements_present", unparsedNote(len(result.Unparsed)))
		}
		return result
	}

	result.Score, result.Reasons = scoreFit(gate, result, doc, result.Reasons)
	result.Reasons = append(result.Reasons, describeGaps(result.Gaps)...)
	if len(result.Unparsed) > 0 {
		result.Reasons = result.Reasons.Add("unparsed_requirements_present", unparsedNote(len(result.Unparsed)))
	}
	result.Classification, result.Blockers = classify(gate, result, requirements)
	return result
}

func unparsedNote(n int) string {
	return plural(n, "source requirement line", "source requirement lines") +
		" could not be matched to a known term and remain visible. Not recognizing a requirement is not evidence that it does not exist."
}

func plural(n int, one, many string) string {
	word := many
	if n == 1 {
		word = one
	}
	return itoa(n) + " " + word
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// resolveRequirement maps one extracted demand onto approved facts. It never adds anything to
// the profile: an unmatched technology stays an unmet requirement.
func resolveRequirement(req Requirement, doc profile.Document) RequirementMatch {
	rm := RequirementMatch{Requirement: req}
	if req.Type == TypeEligibility {
		return resolveEligibility(req, doc)
	}
	terms := append([]string{}, req.Concepts...)
	if normalized := normalizeForScan(req.Term); normalized != "" {
		terms = append(terms, normalized)
	}
	techIDs := map[string]bool{}
	for _, tech := range doc.Technologies {
		for _, name := range append([]string{tech.Name}, tech.Aliases...) {
			normalized := normalizeForScan(name)
			for _, term := range terms {
				if normalized == term || containsPhrase(normalized, term) || containsPhrase(term, normalized) {
					techIDs[tech.ID] = true
				}
			}
		}
	}
	// A capability is supported only when the requirement actually names what that capability
	// is about. Sharing a technology is not enough: a vacancy asking for Go does not thereby
	// demand double-entry accounting, and treating it as if it did would let one domain the
	// candidate has worked in speak for the whole profile.
	capIDs := map[string]bool{}
	capSupport := map[string]int{}
	for _, capability := range doc.Capabilities {
		searchable := normalizeForScan(capability.Name + " " + capability.Description)
		for _, term := range terms {
			if containsPhrase(searchable, term) {
				capIDs[capability.ID] = true
				capSupport[capability.ID]++
			}
		}
	}
	projIDs := map[string]bool{}
	for _, project := range doc.Projects {
		searchable := normalizeForScan(projectText(project))
		for _, term := range terms {
			if containsPhrase(searchable, term) {
				projIDs[project.ID] = true
			}
		}
		for _, id := range project.TechnologyIDs {
			if techIDs[id] {
				projIDs[project.ID] = true
			}
		}
		for _, id := range project.CapabilityIDs {
			if capIDs[id] {
				projIDs[project.ID] = true
			}
		}
	}
	rm.ProfileRefs = refs("profile:technology:", techIDs)
	rm.ProfileRefs = append(rm.ProfileRefs, refs("profile:capability:", capIDs)...)
	rm.ProfileRefs = append(rm.ProfileRefs, refs("profile:project:", projIDs)...)
	if len(techIDs) > 0 || len(capIDs) > 0 {
		rm.Outcome = MatchSupported
		rm.Note = "supported by approved profile facts"
		return rm
	}
	rm.Outcome = MatchUnmet
	rm.Note = req.Term + " is not present in the approved profile. It is recorded as a gap and must never be claimed."
	return rm
}

func projectText(p profile.Project) string {
	parts := []string{p.Name, p.Description}
	for _, f := range append(append([]profile.Fact{}, p.Responsibilities...), p.Outcomes...) {
		parts = append(parts, f.Text)
	}
	parts = append(parts, p.Domains...)
	return strings.Join(parts, " ")
}

// resolveEligibility never guesses an unstated preference. A conflict is only reported when the
// approved preferences are a confirmed enumeration that genuinely excludes the requirement.
func resolveEligibility(req Requirement, doc profile.Document) RequirementMatch {
	rm := RequirementMatch{Requirement: req, Outcome: MatchUnknown}
	engagement := doc.Engagement
	kind := ""
	if len(req.Concepts) > 0 {
		kind = req.Concepts[0]
	}
	cue := req.Term
	if i := strings.Index(cue, ": "); i >= 0 {
		cue = cue[i+2:]
	}
	switch kind {
	case "employment":
		if engagement == nil || len(engagement.EmploymentTypes) == 0 {
			rm.Note = "The approved profile states no employment-type preference, so this condition is unknown."
			return rm
		}
		for _, approved := range engagement.EmploymentTypes {
			if containsPhrase(normalizeForScan(approved), normalizeForScan(cue)) || normalizeForScan(approved) == normalizeForScan(cue) {
				rm.Outcome = MatchSupported
				rm.Note = "The approved employment preferences include " + approved + "."
				return rm
			}
		}
		rm.Outcome = MatchConflict
		rm.Note = "The approved employment preferences are a confirmed list that does not include \"" + cue + "\"."
		return rm
	case "location":
		if engagement != nil && engagement.Remote != nil && *engagement.Remote && normalizeForScan(cue) == "remote" {
			rm.Outcome = MatchSupported
			rm.Note = "Remote work is an approved preference."
			return rm
		}
		rm.Note = "Location eligibility is unknown: the approved profile does not state a preference that settles \"" + cue + "\"."
		return rm
	default:
		rm.Note = "Work-authorization eligibility is unknown and is not inferred."
		return rm
	}
}

func refs(prefix string, ids map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, prefix+id)
	}
	sort.Strings(out)
	return out
}

func supportedItems(matched []RequirementMatch) ([]string, []string) {
	caps, projects := map[string]int{}, map[string]int{}
	for _, rm := range matched {
		for _, ref := range rm.ProfileRefs {
			if id, ok := strings.CutPrefix(ref, "profile:capability:"); ok {
				caps[id]++
			}
			if id, ok := strings.CutPrefix(ref, "profile:project:"); ok {
				projects[id]++
			}
		}
	}
	return bySupport(caps), bySupport(projects)
}

// bySupport orders items by how many distinct requirements support them, then by ID so the
// result is stable and reproducible.
func bySupport(counts map[string]int) []string {
	out := make([]string, 0, len(counts))
	for id := range counts {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		if counts[out[i]] != counts[out[j]] {
			return counts[out[i]] > counts[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// scoreFit is the fit-v1 ranking aid. It exists to order a queue, not to classify: every gate
// in classify() takes precedence over whatever number this returns.
func scoreFit(gate gateResult, result MatchResult, doc profile.Document, reasons explain.Reasons) (int, explain.Reasons) {
	score := 0
	switch gate.confidence {
	case FamilyClear:
		// A family the title states outright counts for more than one read from requirements.
		score += 40
		if gate.inferred {
			score -= 10
		}
	case FamilyAmbiguous:
		score += 20
	}
	// Capped at two capabilities: repeating a technology across bullets adds nothing.
	capped := result.SupportedCapability
	if len(capped) > 2 {
		capped = capped[:2]
	}
	score += 20 * len(capped)
	for _, id := range capped {
		reasons = reasons.Add("capability_supported_by_requirement",
			"Approved capability "+capabilityName(doc, id)+" is supported by an actual requirement excerpt.",
			"profile:capability:"+id)
	}
	if len(result.SupportingProjects) > 0 && len(result.SupportedCapability) > 0 {
		score += 10
		reasons = reasons.Add("project_supports_capability",
			"Approved project "+projectName(doc, result.SupportingProjects[0])+" supports at least one matched capability.",
			"profile:project:"+result.SupportingProjects[0])
	}
	conditionsKnown := false
	for _, rm := range result.Matched {
		if rm.Requirement.Type == TypeEligibility {
			conditionsKnown = true
		}
	}
	if conditionsKnown {
		score += 10
	}
	if score > 100 {
		score = 100
	}
	return score, reasons
}

func capabilityName(doc profile.Document, id string) string {
	for _, c := range doc.Capabilities {
		if c.ID == id {
			return c.Name
		}
	}
	return id
}

func projectName(doc profile.Document, id string) string {
	for _, p := range doc.Projects {
		if p.ID == id {
			return p.Name
		}
	}
	return id
}

func describeGaps(gaps []RequirementMatch) explain.Reasons {
	reasons := explain.Reasons{}
	for _, rm := range gaps {
		code := "requirement_not_in_profile"
		if rm.Outcome == MatchUnknown {
			code = "condition_unknown"
		} else if rm.Outcome == MatchConflict {
			code = "known_preference_conflict"
		}
		reasons = reasons.Add(code, rm.Note, "opportunity:requirement:"+rm.Requirement.ID)
	}
	return reasons
}

// classify applies the authoritative gates. Score never promotes a classification on its own.
func classify(gate gateResult, result MatchResult, requirements []Requirement) (string, explain.Reasons) {
	blockers := explain.Reasons{}
	if gate.confidence == FamilyNone {
		return ClassificationUnrelated, blockers.Add("unrelated_application_role",
			"The advertised role is outside every approved desired engineering family.")
	}
	strong := true
	if gate.confidence != FamilyClear {
		strong = false
		blockers = blockers.Add("family_not_clear",
			"The advertised title does not clearly state an approved desired family, so this cannot be a strong match.")
	}
	if Substantive(requirements) < minSubstantiveReq || result.DescriptionStatus != "complete" {
		strong = false
		blockers = blockers.Add("insufficient_requirements",
			"The stored description is "+result.DescriptionStatus+" and yields "+itoa(Substantive(requirements))+
				" recognized requirements. A title alone is never sufficient for a strong match.")
	}
	if len(result.SupportedCapability) < 2 {
		strong = false
		blockers = blockers.Add("insufficient_supported_capabilities",
			"Fewer than two approved capabilities are supported by an actual requirement excerpt.")
	}
	for _, rm := range result.Gaps {
		if rm.Outcome == MatchConflict {
			strong = false
			blockers = blockers.Add("known_preference_conflict", rm.Note, "opportunity:requirement:"+rm.Requirement.ID)
			continue
		}
		if !rm.Requirement.Mandatory {
			continue
		}
		strong = false
		if rm.Outcome == MatchUnmet {
			blockers = blockers.Add("unresolved_mandatory_requirement",
				"Mandatory requirement "+rm.Requirement.Term+" is not supported by the approved profile.",
				"opportunity:requirement:"+rm.Requirement.ID)
		} else {
			blockers = blockers.Add("unresolved_mandatory_condition", rm.Note,
				"opportunity:requirement:"+rm.Requirement.ID)
		}
	}
	if result.Score < 80 {
		strong = false
	}
	if strong {
		return ClassificationStrong, explain.Reasons{}
	}
	return ClassificationPossible, blockers
}
