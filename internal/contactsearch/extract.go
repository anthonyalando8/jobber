package contactsearch

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/anthonyalando8/prospector/internal/enrich"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
)

// Discovery methods, recorded on every observation so a reader can tell how a fact was obtained
// rather than being told only that it was.
const (
	MethodPageExtract    = "page_extract_v1"
	MethodRouteExtract   = "route_extract_v1"
	MethodPersonExtract  = "person_extract_v1"
	MethodSearchSnippet  = "search_snippet_v1"
	MethodSourceMetadata = "source_metadata_v1"
	MethodPatternInfer   = "pattern_inference_v1"
	MethodAIExtract      = "ai_extract_v1"
)

// atsHosts are applicant tracking systems. A link to one of these is an application route — a
// real destination for a specific job application, and one that needs no email address at all.
// The value is the provider name shown to the operator.
var atsHosts = map[string]string{
	"boards.greenhouse.io":     "Greenhouse",
	"job-boards.greenhouse.io": "Greenhouse",
	"greenhouse.io":            "Greenhouse",
	"jobs.lever.co":            "Lever",
	"lever.co":                 "Lever",
	"jobs.ashbyhq.com":         "Ashby",
	"ashbyhq.com":              "Ashby",
	"myworkdayjobs.com":        "Workday",
	"workday.com":              "Workday",
	"apply.workable.com":       "Workable",
	"workable.com":             "Workable",
	"smartrecruiters.com":      "SmartRecruiters",
	"jobs.jobvite.com":         "Jobvite",
	"jobvite.com":              "Jobvite",
	"recruitee.com":            "Recruitee",
	"breezy.hr":                "Breezy",
	"bamboohr.com":             "BambooHR",
	"teamtailor.com":           "Teamtailor",
	"pinpointhq.com":           "Pinpoint",
	"rippling.com":             "Rippling",
	"personio.de":              "Personio",
	"join.com":                 "Join",
}

// recruitingTitles are the publicly stated roles that make someone a hiring contact. Matching is
// on the stated title only: nobody is assumed to be a recruiter because of their mailbox.
var recruitingTitles = []string{
	"technical recruiter", "senior recruiter", "lead recruiter", "recruiter",
	"talent acquisition partner", "talent acquisition manager", "talent acquisition specialist",
	"talent acquisition", "talent partner", "talent specialist", "talent manager",
	"head of talent", "head of people", "people partner", "people operations",
	"recruitment manager", "recruitment lead", "recruitment specialist", "recruitment",
	"hiring manager", "head of recruiting", "recruiting manager", "recruiting lead",
	"hr manager", "hr business partner", "human resources manager",
}

// engineeringTitles are technical leadership roles that can judge an engineering approach. This
// list is not permission to write to every CTO: it decides what a discovered person is, and the
// existing recipient policy decides separately whether writing to them is appropriate.
var engineeringTitles = []string{
	"head of engineering", "vp of engineering", "vp engineering", "vice president of engineering",
	"director of engineering", "engineering director", "engineering manager",
	"software engineering manager", "technical hiring manager", "head of technology",
	"chief technology officer", "cto", "chief technical officer",
	"head of platform", "head of backend", "director of technology",
}

// hrOnlyTitles are human-resources roles that are only a hiring contact where the surrounding
// text says so. A generalist HR role is not automatically a recruiting contact.
var hrOnlyTitles = []string{"human resources", "hr generalist", "hr officer", "people team"}

// hiringContext are words that make a generalist HR mention a hiring one.
var hiringContext = []string{"hiring", "recruit", "candidate", "vacanc", "job", "career", "talent", "apply", "application"}

// nonNameWords are capitalized words that routinely sit next to a job title on a web page but
// are not part of anybody's name. Without this the extractor reads "Our Engineering Manager" as
// a person called "Our Engineering".
var nonNameWords = map[string]bool{
	"our": true, "the": true, "a": true, "an": true, "as": true, "at": true, "and": true,
	"for": true, "with": true, "meet": true, "team": true, "contact": true, "email": true,
	"about": true, "we": true, "us": true, "your": true, "new": true, "senior": true,
	"lead": true, "head": true, "chief": true, "global": true, "regional": true, "group": true,
	"is": true, "was": true, "join": true, "hello": true, "hi": true, "from": true, "by": true,
	"read": true, "more": true, "view": true, "profile": true, "linkedin": true, "twitter": true,
	"apply": true, "now": true, "open": true, "roles": true, "careers": true, "jobs": true,
	"please": true, "send": true, "to": true, "cv": true, "resume": true, "here": true,
}

var (
	addressPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	hrefPattern    = regexp.MustCompile(`(?i)href\s*=\s*["']([^"'>\s]+)["']`)
	mailtoPattern  = regexp.MustCompile(`(?i)mailto:([A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,})`)
)

// Page is one fetched public document, with the classification of what kind of source it is.
type Page struct {
	URL        string
	HTML       string
	SourceType string
	// Provider names the search engine that led here, where one did. Empty for a page reached
	// directly from the company's own domain or from the job posting.
	Provider string
}

// ExtractFromPage reads one public page and returns every candidate it can support with an exact
// quotation from that page. It never returns a candidate whose claim is not literally present in
// the source text: the grounding check at the end of this function is the single place that
// guarantee is enforced, and it applies to deterministic extraction exactly as it applies to
// anything an AI extractor might later propose.
func ExtractFromPage(page Page, companyName, companyDomain string) []Candidate {
	text := enrich.PageText(page.HTML)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	observed := time.Now().UTC()
	employerOwned := EmployerOwned(page.SourceType)

	var found []Candidate
	found = append(found, extractApplicationRoutes(page, text, companyDomain, observed)...)
	found = append(found, extractPublishedAddresses(page, text, companyName, companyDomain, employerOwned, observed)...)
	found = append(found, extractPeople(page, text, companyName, observed)...)

	return Ground(found)
}

// extractApplicationRoutes finds destinations a candidate can actually apply through. These
// matter more than they look: for a specific job application an employer-published apply route
// is a complete answer, and demanding an email address instead would reject the very route the
// employer asked people to use.
func extractApplicationRoutes(page Page, text, companyDomain string, observed time.Time) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	for _, match := range hrefPattern.FindAllStringSubmatch(page.HTML, -1) {
		href := strings.TrimSpace(match[1])
		if href == "" || strings.HasPrefix(strings.ToLower(href), "mailto:") {
			continue
		}
		absolute := resolveHref(href, page.URL)
		provider, isATS := atsProviderFor(absolute)
		employerForm := !isATS && SameEmployerHost(absolute, companyDomain) && looksLikeApplyPath(absolute)
		if !isATS && !employerForm {
			continue
		}
		if seen[normalizeURLKey(absolute)] {
			continue
		}
		seen[normalizeURLKey(absolute)] = true

		// The excerpt has to quote the page, not the URL we just built. Anchor on the raw href
		// as it appears in the markup, which is what the page actually contains.
		// The evidence has to be readable and still carry the URL it claims. A raw slice of
		// markup satisfies the second and fails the first, so the visible text around the link is
		// quoted and the link target stated alongside it.
		excerpt := routeExcerpt(page.HTML, href, absolute)
		if excerpt == "" {
			continue
		}
		label := "an application form on the employer's own site"
		if isATS {
			label = provider
		}
		out = append(out, Candidate{
			Kind: KindApplicationURL, ApplicationURL: absolute, ATSProvider: provider,
			EmailProvenance: ProvenanceNone, Verification: VerificationPublished,
			RoleTitle: "Published application route (" + label + ")",
			Evidence: []Evidence{{SourceType: page.SourceType, SourceURL: page.URL,
				Excerpt: excerpt, Method: MethodRouteExtract, Provider: page.Provider, ObservedAt: observed}},
		})
	}
	return out
}

// extractPublishedAddresses finds mailboxes the page publishes. An address near an application
// cue becomes an application route; any other address on an employer-owned page is recorded as a
// mailbox whose function the existing classifiers will judge.
func extractPublishedAddresses(page Page, text, companyName, companyDomain string, employerOwned bool, observed time.Time) []Candidate {
	var out []Candidate
	seen := map[string]bool{}

	// An address the page states as the place to apply, decided by the same cue-gated rule the
	// opportunity ingest already uses. One idea of "this is where to apply", not two.
	if applyAddress := opportunity.PublishedApplicationEmail(text); applyAddress != "" {
		if excerpt := excerptAround(text, applyAddress, 260); excerpt != "" {
			provenance := ProvenanceThirdParty
			if employerOwned {
				provenance = ProvenanceEmployer
			}
			if page.SourceType == SourceJobPosting || page.SourceType == SourceMetadata {
				provenance = ProvenanceSource
			}
			seen[strings.ToLower(applyAddress)] = true
			out = append(out, Candidate{
				Kind: KindApplicationEmail, Email: strings.ToLower(applyAddress),
				EmailProvenance: provenance, Verification: VerificationPublished,
				RoleTitle:       "Published application address",
				EmployerClaimed: companyName,
				Evidence: []Evidence{{SourceType: page.SourceType, SourceURL: page.URL,
					Excerpt: excerpt, Method: MethodRouteExtract, Provider: page.Provider, ObservedAt: observed}},
			})
		}
	}

	// Every other address the page publishes. Only from a page the employer owns: an address
	// found on a third party's page is that third party publishing somebody else's mail, which
	// is a far weaker claim and not one this pipeline turns into a mailbox candidate.
	if !employerOwned {
		return out
	}
	addresses := addressPattern.FindAllString(text, -1)
	addresses = append(addresses, mailtoAddresses(page.HTML)...)
	for _, raw := range addresses {
		address, err := store.NormalizeContactValue("email", raw)
		if err != nil || seen[address] {
			continue
		}
		// An address on somebody else's domain found on this employer's page is usually a
		// vendor, a board or a tracking endpoint, not this employer's mailbox.
		if !SameEmployerHost("https://"+hostPart(address), companyDomain) {
			continue
		}
		excerpt := excerptAround(text, address, 220)
		if excerpt == "" {
			excerpt = excerptAround(page.HTML, address, 220)
		}
		if excerpt == "" {
			continue
		}
		seen[address] = true
		out = append(out, Candidate{
			Kind: KindMailbox, Email: address, EmailProvenance: ProvenanceEmployer,
			Verification: VerificationPublished, EmployerClaimed: companyName,
			Evidence: []Evidence{{SourceType: page.SourceType, SourceURL: page.URL,
				Excerpt: excerpt, Method: MethodPageExtract, Provider: page.Provider, ObservedAt: observed}},
		})
	}
	return out
}

// extractPeople finds named people with a publicly stated hiring-relevant role.
//
// It produces a person and a title, and nothing else. It never attaches an address: a name found
// next to a job title establishes who works there, which is a different fact from where their
// mail goes, and merging the two is exactly how a fabricated recipient gets created.
func extractPeople(page Page, text, companyName string, observed time.Time) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	lower := strings.ToLower(text)

	for _, entry := range []struct {
		titles      []string
		requireHint bool
	}{{recruitingTitles, false}, {engineeringTitles, false}, {hrOnlyTitles, true}} {
		for _, title := range entry.titles {
			for _, index := range allIndexes(lower, title) {
				window, windowStart := boundedWindow(text, index, len(title), 160)
				if entry.requireHint && !containsAny(strings.ToLower(window), hiringContext) {
					continue
				}
				name := personNameNear(text, window, windowStart, index, len(title))
				if name == "" {
					continue
				}
				key := NormalizePersonName(name) + "|" + title
				if seen[key] {
					continue
				}
				seen[key] = true
				candidate := Candidate{
					Kind: KindPerson, PersonName: name, RoleTitle: displayTitle(text, index, len(title)),
					EmployerClaimed: companyName, EmailProvenance: ProvenanceNone,
					Verification: VerificationUnverified,
					ProfileURL:   profileURLFor(page),
					Evidence: []Evidence{{SourceType: page.SourceType, SourceURL: page.URL,
						Excerpt: safeText(strings.TrimSpace(window)), Method: MethodPersonExtract,
						Provider: page.Provider, ObservedAt: observed}},
				}
				// An address printed in the same breath as the person and their title is that
				// person's published address, and the excerpt proves it. This is the one way a name
				// and an address become one fact here: because a source stated them together, not
				// because a pattern connected them afterwards.
				if address := addressWithin(window); address != "" && EmployerOwned(page.SourceType) {
					candidate.Email = address
					candidate.EmailProvenance = ProvenanceEmployer
					candidate.Verification = VerificationPublished
				}
				out = append(out, candidate)
			}
		}
	}
	return out
}

// ExtractFromSnippet records what a search result itself said, and nothing more.
//
// A snippet is a search engine's summary of a page. It is a reason to read that page, not a
// statement by the employer, so anything derived from one is marked as the weakest source there
// is and can never reach the trust floor a published address must clear.
func ExtractFromSnippet(result SearchResult, companyName, companyDomain string) []Candidate {
	text := strings.TrimSpace(result.Title + ". " + result.Snippet)
	if text == "" {
		return nil
	}
	page := Page{URL: result.URL, HTML: text, SourceType: SourceSearchSnippet, Provider: result.Provider}
	var out []Candidate

	// A person named in a snippet alongside a hiring title is a genuine lead worth keeping.
	for _, candidate := range extractPeople(page, text, companyName, result.ObservedAt) {
		candidate.Evidence[0].Method = MethodSearchSnippet
		out = append(out, candidate)
	}
	// An address appearing in a snippet is recorded, but as an unverified claim by a search
	// engine. Section 9 of the brief is explicit that this alone must not produce a usable
	// recipient, and the trust floor in Classify is what enforces that.
	for _, raw := range addressPattern.FindAllString(text, -1) {
		address, err := store.NormalizeContactValue("email", raw)
		if err != nil {
			continue
		}
		excerpt := excerptAround(text, address, 240)
		if excerpt == "" {
			continue
		}
		out = append(out, Candidate{
			Kind: KindMailbox, Email: address, EmailProvenance: ProvenanceThirdParty,
			Verification: VerificationUnverified, EmployerClaimed: companyName,
			Evidence: []Evidence{{SourceType: SourceSearchSnippet, SourceURL: result.URL,
				Excerpt: excerpt, Method: MethodSearchSnippet, Provider: result.Provider,
				ObservedAt: result.ObservedAt}},
		})
	}
	return Ground(out)
}

// Ground drops any candidate whose asserted facts are not literally present in its own evidence.
//
// This is the check that makes the pipeline's central promise enforceable rather than merely
// intended. It applies to every candidate from every origin — the deterministic extractors
// above, a search snippet, or an AI extractor — because the guarantee wanted is not "our
// extractors are careful" but "a contact cannot exist here without a source that says so". An
// extractor that returns a plausible address nobody published fails this check and is discarded.
func Ground(candidates []Candidate) []Candidate {
	kept := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if len(c.Evidence) == 0 {
			continue
		}
		supported := true
		for _, evidence := range c.Evidence {
			if strings.TrimSpace(evidence.Excerpt) == "" {
				supported = false
				break
			}
		}
		if !supported {
			continue
		}
		// An inferred address is the one fact deliberately not present in its source — that is
		// what makes it inferred. It carries the evidence of the pattern it was derived from
		// instead, which InferAddress attaches and this check must not demand of it.
		if c.Inferred() {
			kept = append(kept, c)
			continue
		}
		if c.Email != "" && !anyEvidenceContains(c.Evidence, c.Email) {
			continue
		}
		if c.PersonName != "" && !anyEvidenceContains(c.Evidence, c.PersonName) {
			continue
		}
		if c.ApplicationURL != "" && !anyEvidenceMentionsURL(c.Evidence, c.ApplicationURL) {
			continue
		}
		kept = append(kept, c)
	}
	return kept
}

func anyEvidenceContains(evidence []Evidence, fact string) bool {
	fact = strings.ToLower(strings.TrimSpace(fact))
	if fact == "" {
		return true
	}
	for _, e := range evidence {
		if strings.Contains(strings.ToLower(e.Excerpt), fact) {
			return true
		}
	}
	return false
}

// anyEvidenceMentionsURL accepts either the full URL or its distinctive path, because a page
// commonly writes a relative href that the extractor then resolved to an absolute one.
func anyEvidenceMentionsURL(evidence []Evidence, target string) bool {
	key := normalizeURLKey(target)
	if key == "" {
		return false
	}
	path := key
	if slash := strings.Index(key, "/"); slash >= 0 {
		path = key[slash:]
	}
	for _, e := range evidence {
		body := strings.ToLower(e.Excerpt)
		if strings.Contains(body, key) || (len(path) > 3 && strings.Contains(body, path)) {
			return true
		}
	}
	return false
}

// personNameNear finds a capitalized personal name adjacent to a job title.
//
// It is deliberately conservative and misses rather than guesses. The name must be one or two
// capitalized words that are not ordinary page furniture, immediately before the title or
// immediately after it — the two orderings a team page or a byline actually uses. Anything
// looser starts producing people called "Our Engineering" and "Contact The".
func personNameNear(full, window string, windowStart, titleIndex, titleLength int) string {
	before := strings.TrimSpace(full[windowStart:titleIndex])
	if name := trailingName(before); name != "" {
		return name
	}
	// Past the end of the title, not past its first space. Reading from the first space meant a
	// two-word title supplied its own second word as a name: "Talent Acquisition Professional"
	// produced a person called "Acquisition Professional", which is a job description with
	// capital letters, not anybody who works there.
	after := titleIndex + titleLength
	if after >= len(full) || after-windowStart >= len(window) {
		return ""
	}
	return leadingName(window[after-windowStart:])
}

func trailingName(text string) string {
	words := splitWords(text)
	for size := 3; size >= 2; size-- {
		if len(words) < size {
			continue
		}
		if name, ok := joinIfName(words[len(words)-size:]); ok {
			return name
		}
	}
	return ""
}

func leadingName(text string) string {
	words := splitWords(text)
	for size := 3; size >= 2; size-- {
		if len(words) < size {
			continue
		}
		if name, ok := joinIfName(words[:size]); ok {
			return name
		}
	}
	return ""
}

// joinIfName accepts a run of words only when every one of them looks like part of a personal
// name: capitalized, alphabetic, not page furniture, and not itself a job-title word.
func joinIfName(words []string) (string, bool) {
	for _, word := range words {
		if !looksLikeNameWord(word) {
			return "", false
		}
	}
	return strings.Join(words, " "), true
}

func looksLikeNameWord(word string) bool {
	word = strings.Trim(word, ".,;:!?()[]{}\"'")
	if len([]rune(word)) < 2 || len([]rune(word)) > 24 {
		return false
	}
	if nonNameWords[strings.ToLower(word)] {
		return false
	}
	runes := []rune(word)
	if !unicode.IsUpper(runes[0]) {
		return false
	}
	upper := 0
	for _, r := range runes {
		if !unicode.IsLetter(r) && r != '-' && r != '\'' {
			return false
		}
		if unicode.IsUpper(r) {
			upper++
		}
	}
	// An all-capitals run is a heading or an acronym, not a name as written on a page.
	return upper < len(runes)
}

func splitWords(text string) []string {
	fields := strings.Fields(text)
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.Trim(field, ".,;:!?()[]{}\"'|-–—·"); field != "" {
			out = append(out, field)
		}
	}
	return out
}

// displayTitle returns the title as the page actually wrote it, rather than the lowercase form
// the matcher searched for.
func displayTitle(text string, index, length int) string {
	if index < 0 || index+length > len(text) {
		return ""
	}
	return strings.TrimSpace(text[index : index+length])
}

func boundedWindow(text string, index, length, radius int) (string, int) {
	start := max(0, index-radius)
	end := min(len(text), index+length+radius)
	return text[start:end], start
}

// excerptAround quotes the source around a term, so stored provenance contains the words the
// claim rests on rather than a paraphrase of them.
func excerptAround(text, term string, radius int) string {
	idx := strings.Index(strings.ToLower(text), strings.ToLower(strings.TrimSpace(term)))
	if idx < 0 {
		return ""
	}
	window, _ := boundedWindow(text, idx, len(term), radius)
	window = strings.Join(strings.Fields(window), " ")
	if len(window) > 800 {
		window = window[:800]
	}
	return safeText(strings.TrimSpace(window))
}

// safeText makes a scraped fragment safe to store as text.
//
// Live pages are not all valid UTF-8 — a page served as Latin-1 puts a bare 0xa0 where a
// non-breaking space belongs, and Postgres refuses the whole insert rather than storing a byte
// that is not a character. The substitution is visible rather than silent: a replacement
// character in a stored excerpt says the source had a byte we could not read, which is true and
// is better than a lost observation or a failed run.
func safeText(text string) string {
	if utf8.ValidString(text) {
		return text
	}
	return strings.ToValidUTF8(text, "\uFFFD")
}

// allIndexes finds every whole-word occurrence of a title in page text.
//
// Whole-word is not a refinement here, it is the difference between working and not. A plain
// substring search for "cto" matches inside "Contractors", and the live corpus duly produced a
// person called "Features Cosuno" whose title was "cto", read out of a navigation menu. Short
// titles are the most valuable ones to match and the most dangerous to match loosely, so a match
// must not have a letter or digit on either side of it.
func allIndexes(haystack, needle string) []int {
	var out []int
	for offset := 0; ; {
		idx := strings.Index(haystack[offset:], needle)
		if idx < 0 {
			return out
		}
		at := offset + idx
		if wholeWordAt(haystack, at, len(needle)) {
			out = append(out, at)
		}
		offset = at + len(needle)
		if len(out) >= 12 {
			return out // one page cannot usefully contribute more than this
		}
	}
}

// wholeWordAt reports whether the run at [index, index+length) is bounded by something other than
// a letter or a digit on both sides.
func wholeWordAt(text string, index, length int) bool {
	if index > 0 {
		before, _ := utf8.DecodeLastRuneInString(text[:index])
		if unicode.IsLetter(before) || unicode.IsDigit(before) {
			return false
		}
	}
	if end := index + length; end < len(text) {
		after, _ := utf8.DecodeRuneInString(text[end:])
		if unicode.IsLetter(after) || unicode.IsDigit(after) {
			return false
		}
	}
	return true
}

func containsAny(text string, words []string) bool {
	for _, word := range words {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

// routeExcerpt renders the evidence for a discovered application route: the words a reader would
// see near the link, followed by the link target the markup actually contains.
func routeExcerpt(html, href, absolute string) string {
	window := excerptAround(html, href, 260)
	if window == "" {
		return ""
	}
	visible := strings.TrimSpace(enrich.PageText(window))
	if len(visible) > 300 {
		visible = strings.TrimSpace(visible[:300])
	}
	if visible == "" {
		visible = "(no visible link text)"
	}
	return safeText(visible + " [link target: " + absolute + "]")
}

// addressWithin returns the single address inside a window, or empty when there is none or more
// than one. Two addresses in one window means it is ambiguous which belongs to the person named
// there, and an ambiguous attribution is the kind of small error that ends up writing to the
// wrong colleague.
func addressWithin(window string) string {
	matches := addressPattern.FindAllString(window, 2)
	if len(matches) != 1 {
		return ""
	}
	address, err := store.NormalizeContactValue("email", matches[0])
	if err != nil {
		return ""
	}
	return address
}

func mailtoAddresses(html string) []string {
	var out []string
	for _, match := range mailtoPattern.FindAllStringSubmatch(html, -1) {
		out = append(out, match[1])
	}
	return out
}

func atsProviderFor(rawURL string) (string, bool) {
	host := normalizeHost(hostOf(rawURL))
	if host == "" {
		return "", false
	}
	for known, provider := range atsHosts {
		if host == known || strings.HasSuffix(host, "."+known) {
			return provider, true
		}
	}
	return "", false
}

// looksLikeApplyPath recognizes an employer's own application form. It requires an application
// word in the path, so a link to the careers *index* is not mistaken for a route to apply.
func looksLikeApplyPath(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	for _, cue := range []string{"/apply", "apply=", "/application", "/job-application", "/jobs/apply", "/careers/apply"} {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

func resolveHref(href, base string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "//") {
		return "https:" + href
	}
	if strings.Contains(href, "://") {
		return href
	}
	baseHost := hostOf(base)
	if baseHost == "" {
		return href
	}
	if strings.HasPrefix(href, "/") {
		return "https://" + baseHost + href
	}
	return "https://" + baseHost + "/" + strings.TrimPrefix(href, "./")
}

// profileURLFor returns the page URL when it is a public professional profile, so a person found
// there gets a stable identity that is stronger than their name.
func profileURLFor(page Page) string {
	if page.SourceType == SourcePublicProfile {
		return page.URL
	}
	return ""
}

func hostPart(address string) string {
	if at := strings.LastIndex(address, "@"); at >= 0 {
		return address[at+1:]
	}
	return ""
}
