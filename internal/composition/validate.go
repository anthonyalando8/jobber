package composition

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
)

var urlPattern = regexp.MustCompile(`https?://[^\s<>"\)]+`)
var numberPattern = regexp.MustCompile(`\b[0-9]+(?:[.,][0-9]+)*\b`)
var placeholderPattern = regexp.MustCompile(`(?i)\{\{|\}\}|\[(?:your|company|name|insert|role|link)[^\]]*\]|<(?:name|company|role)>`)
var attachmentPattern = regexp.MustCompile(`(?i)\b(?:attached|attachment|enclosed)\b`)
var applicationPattern = regexp.MustCompile(`(?i)\b(?:apply|applying|application|candidacy)\b`)
var interestPattern = regexp.MustCompile(`(?i)\binterest(?:ed)? in\b`)
var paymentPattern = regexp.MustCompile(`(?i)\b(?:payment|payments|m-pesa|mpesa|checkout|reconciliation|billing|collections)\b`)
var paymentOfferPattern = regexp.MustCompile(`(?i)(?:\byour\b|\byou need\b|\bhelp (?:you|with)\b|\bfix\b|\bbroken\b|\bfailing\b|\boutsourc|\bimprov|\bintegrat.*for you)`)

// gapPattern recognises a sentence that describes a technology as missing, contrasted away or
// still being learned, rather than claiming it.
var gapPattern = regexp.MustCompile(`(?i)(?:haven't|have not|has not|not yet|no experience|no direct|no professional|not used|not worked|\bgap\b|still learning|rather than|instead of|do not have|don't have|\black\b|limited experience|less experience|new to|unfamiliar)`)

// technologyApproval answers whether a vocabulary technology is inside the snapshot's approved
// technologies, under its canonical name or any alias.
func technologyApproval(s Snapshot) func(canonical string, aliases []string) bool {
	allowed := map[string]bool{}
	for _, t := range s.Technologies {
		for _, name := range append([]string{t.Name}, t.Aliases...) {
			allowed[strings.ToLower(name)] = true
		}
	}
	return func(canonical string, aliases []string) bool {
		ok := allowed[strings.ToLower(canonical)]
		for _, alias := range aliases {
			ok = ok || allowed[strings.ToLower(alias)]
		}
		return ok
	}
}

func mentionsTechnology(text, canonical string, aliases []string) bool {
	for _, alias := range aliases {
		if canonical == "Go" && alias == "Go" {
			if regexp.MustCompile(`\bGo\b`).MatchString(text) {
				return true
			}
		} else if phrase(text, alias) {
			return true
		}
	}
	return false
}

// ApprovedTechnologies are the technology names the email may use.
func ApprovedTechnologies(s Snapshot) []string {
	out := []string{}
	for _, t := range s.Technologies {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

// UnapprovedTechnologies lists the technologies named in text that the snapshot does not approve:
// exactly the ones Validate would flag if the email named them.
func UnapprovedTechnologies(s Snapshot, text string) []string {
	approved := technologyApproval(s)
	out := []string{}
	for canonical, aliases := range opportunity.TechnologyVocabulary() {
		if !approved(canonical, aliases) && mentionsTechnology(text, canonical, aliases) {
			out = append(out, canonical)
		}
	}
	sort.Strings(out)
	return out
}

// operatorAskedForPayments reports whether the sender's own override reason or notes raise a
// payment or checkout topic.
func operatorAskedForPayments(s Snapshot) bool {
	for _, r := range s.ReviewReasons {
		if (r.Code == plan.OperatorNoteCode || r.Code == plan.OperatorOverrideCode) && paymentPattern.MatchString(r.Text) {
			return true
		}
	}
	return false
}

func phrase(text, term string) bool {
	if strings.TrimSpace(term) == "" {
		return false
	}
	pattern := `(?i)(?:^|[^\pL\pN])` + regexp.QuoteMeta(term) + `(?:$|[^\pL\pN])`
	return regexp.MustCompile(pattern).MatchString(text)
}
func normalized(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.NewReplacer("—", "-", "–", "-", "’", "'").Replace(s)), " "))
}
func HasBlock(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == "block" {
			return true
		}
	}
	return false
}
func NeedsReview(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity != "info" {
			return true
		}
	}
	return false
}

func Validate(s Snapshot, e Email) []Finding {
	findings := []Finding{}
	add := func(severity, code, why, excerpt string) {
		findings = append(findings, Finding{Severity: severity, Code: code, Explanation: why, Excerpt: excerpt, Repairable: true})
	}
	if err := s.Validate(); err != nil {
		add("block", "invalid_snapshot", err.Error(), "")
		return findings
	}
	if e.Intent != s.Intent {
		add("block", "intent_changed", "The writer changed the authoritative intent.", string(e.Intent))
	}
	if e.Recipient != s.Recipient.Address {
		add("block", "recipient_changed", "The recipient must remain pinned.", e.Recipient)
	}
	if e.OpportunityID != s.OpportunityID {
		add("block", "opportunity_changed", "The writer changed the selected vacancy.", fmt.Sprint(e.OpportunityID))
	}
	if strings.TrimSpace(e.Subject) == "" || strings.TrimSpace(e.Body) == "" {
		add("block", "empty_email", "Subject and body are required.", "")
	}
	mainBody := strings.TrimSuffix(e.Body, "\n\n"+Signature(s))
	if strings.ContainsAny(e.Subject, "\r\n") || utf8.RuneCountInString(e.Subject) > 160 || utf8.RuneCountInString(mainBody) > 4000 {
		add("block", "email_length_or_header", "Subject must be one line <=160 characters; body excluding approved signature <=4000.", "")
	}
	text := e.Subject + "\n" + e.Body
	if placeholderPattern.MatchString(text) || strings.Contains(text, "```") || strings.HasPrefix(strings.TrimSpace(e.Body), "{") || strings.Contains(text, `"used_claim_ids"`) || strings.Contains(text, "<think>") {
		add("block", "output_artifacts", "Email contains placeholders, JSON, fences or reasoning artifacts.", "")
	}
	if strings.HasPrefix(normalized(e.Subject), "re:") || strings.HasPrefix(normalized(e.Subject), "fwd:") {
		add("block", "fake_thread", "Initial contact cannot claim an existing thread.", e.Subject)
	}
	if s.Intent == plan.SpecificJobApplication {
		if !strings.Contains(normalized(e.Subject), normalized(s.AdvertisedTitle)) || !strings.Contains(normalized(e.Body), normalized(s.AdvertisedTitle)) {
			add("block", "application_title_mismatch", "Use the exact selected advertised title in subject and application declaration.", s.AdvertisedTitle)
		}
		// Naming the exact advertised role together with interest in it ("my interest in the
		// Senior Backend Engineer role") states the purpose as clearly as the word "apply".
		statedInterest := interestPattern.MatchString(e.Body) && strings.Contains(normalized(e.Body), normalized(s.AdvertisedTitle))
		if !applicationPattern.MatchString(e.Body) && !statedInterest {
			add("review", "application_intent_unclear", "The body does not clearly state application intent.", "")
		}
	} else {
		if applicationPattern.MatchString(e.Subject) || regexp.MustCompile(`(?i)\b(?:I am|I'm|I’m|I'd like to|I would like to)\s+(?:applying|apply)\b`).MatchString(e.Body) {
			add("block", "intent_contradiction", "A non-application plan cannot apply for a vacancy.", e.Subject)
		}
		if s.ProvenanceTitle != "" && phrase(text, s.ProvenanceTitle) {
			add("block", "unrelated_title_leakage", "The discovered title is provenance, not the requested opportunity.", s.ProvenanceTitle)
		}
	}
	claims := map[string]Claim{}
	caps, projects := map[string]bool{}, map[string]bool{}
	for _, c := range s.AllowedClaims {
		claims[c.ID] = c
	}
	for _, c := range s.Capabilities {
		caps[c.ID] = true
	}
	for _, p := range s.Projects {
		projects[p.ID] = true
	}
	for _, id := range e.UsedClaimIDs {
		if _, ok := claims[id]; !ok {
			add("block", "invalid_claim_reference", "Unknown or unselected claim ID.", id)
		}
	}
	for _, id := range e.UsedCapabilityIDs {
		if !caps[id] {
			add("block", "unselected_capability", "Capability is outside the plan.", id)
		}
	}
	for _, id := range e.UsedProjectIDs {
		if !projects[id] {
			add("block", "unselected_project", "Project is outside the plan.", id)
		}
	}
	if len(e.UsedClaimIDs) == 0 {
		add("review", "missing_claim_provenance", "Candidate claims need source references; reviewer must inspect all prose.", "")
	}
	allowedURLs := map[string]bool{}
	cv := false
	for _, r := range s.Resources {
		if r.URL != "" {
			allowedURLs[r.URL] = true
		}
		if r.Kind == "cv" {
			cv = true
		}
	}
	for _, source := range s.Sources {
		if source.URL != "" {
			allowedURLs[source.URL] = true
		}
	}
	for _, raw := range urlPattern.FindAllString(text, -1) {
		url := strings.TrimRight(raw, ".,;!")
		if !allowedURLs[url] {
			add("block", "resource_not_configured", "URL is not an approved resource or selected source.", url)
		}
	}
	if attachmentPattern.MatchString(text) {
		add("block", "fabricated_attachment", "Phase 4 has no attachment payload. Offer configured resources truthfully instead.", "")
	}
	if !cv && regexp.MustCompile(`(?i)\b(?:my CV|my resume|my résumé)\b`).MatchString(text) {
		add("block", "cv_not_configured", "No CV resource is approved in this snapshot.", "")
	}
	if s.Recipient.Name == "" {
		first := strings.Split(strings.TrimSpace(e.Body), "\n")[0]
		if regexp.MustCompile(`(?i)^(?:dear|hi|hello)\s+`).MatchString(first) && !regexp.MustCompile(`(?i)^(?:dear|hi|hello)\s+(?:team|hiring team|recruiting team|there)[,!\s]*$`).MatchString(first) {
			add("block", "invented_recipient_name", "Generic recipient has no approved personal name.", first)
		}
	}
	approved := technologyApproval(s)
	for canonical, aliases := range opportunity.TechnologyVocabulary() {
		if approved(canonical, aliases) {
			continue
		}
		for _, sentence := range strings.Split(strings.ReplaceAll(text, "\n", ". "), ". ") {
			if !mentionsTechnology(sentence, canonical, aliases) {
				continue
			}
			if gapPattern.MatchString(sentence) {
				add("review", "acknowledged_skill_gap", "Confirm the technology is described only as an honest gap.", sentence)
			} else {
				add("block", "unapproved_technology", "Technology is outside the approved selected factual envelope.", sentence)
			}
		}
	}
	paymentForbidden := false
	for _, claim := range s.ForbiddenClaims {
		if claim.Code == "recipient_payment_problem" {
			paymentForbidden = true
		}
	}
	if paymentForbidden {
		// When the sender's own override reason or notes ask for a payment or checkout topic, an
		// offer about it is the sender's decision, not an inference from a payments project: it
		// becomes a warning to confirm. Without that instruction it stays a critical finding.
		severity, why := "block", ""
		if operatorAskedForPayments(s) {
			severity, why = "review", " You asked for this topic in your notes; confirm the email offers help and does not claim their payments are broken."
		}
		if paymentPattern.MatchString(e.Subject) {
			add(severity, "payment_subject_leakage", "No payment need authorizes this subject."+why, e.Subject)
		}
		for _, paragraph := range strings.Split(e.Body, "\n\n") {
			if paymentPattern.MatchString(paragraph) && paymentOfferPattern.MatchString(paragraph) {
				add(severity, "recipient_payment_problem", "Payments proof cannot diagnose or offer to repair the recipient's payments."+why, paragraph)
			}
		}
	}
	numericSupport := ""
	for _, id := range e.UsedClaimIDs {
		if c, ok := claims[id]; ok && c.Kind == "candidate" {
			numericSupport += " " + c.Text
		}
	}
	scan := e.Body
	if signature := Signature(s); signature != "" {
		scan = strings.TrimSuffix(scan, "\n\n"+signature)
	}
	scan = urlPattern.ReplaceAllString(scan, "")
	for _, number := range numberPattern.FindAllString(scan, -1) {
		if !phrase(numericSupport, number) && !phrase(s.AdvertisedTitle, number) {
			add("block", "unsupported_numeric_claim", "Numeric claim lacks cited candidate support; employer quantities require explicit review.", number)
		}
	}
	for _, pattern := range []string{"I hope this email finds you well", "keen interest", "unique blend", "leverage", "dynamic team", "cutting-edge", "particularly impressed"} {
		if phrase(text, pattern) {
			add("review", "formulaic_language", "Replace generic praise or canned phrasing with supported specifics.", pattern)
		}
	}
	words := len(strings.Fields(e.Body))
	if words > 260 || words < 60 {
		add("info", "length_guidance", "Prefer a concise but informative first contact; roughly 100–220 words is guidance, not a quota.", fmt.Sprint(words))
	}
	return findings
}

func Signature(s Snapshot) string {
	lines := []string{}
	for _, value := range []string{s.Signature.Name, s.Signature.Descriptor, s.Signature.Location, s.Signature.Phone} {
		if strings.TrimSpace(value) != "" {
			lines = append(lines, value)
		}
	}
	for _, id := range s.Signature.ResourceIDs {
		for _, r := range s.Resources {
			if r.ID == id && r.URL != "" {
				lines = append(lines, r.Label+": "+r.URL)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func ValidateReview(review Review) error {
	if !review.Complete || review.Findings == nil {
		return fmt.Errorf("semantic review incomplete")
	}
	for _, f := range review.Findings {
		if (f.Severity != "block" && f.Severity != "review" && f.Severity != "info") || f.Code == "" || f.Explanation == "" {
			return fmt.Errorf("invalid semantic finding")
		}
	}
	if review.Passed && NeedsReview(review.Findings) {
		return fmt.Errorf("review passed flag contradicts findings")
	}
	if !review.Passed && !NeedsReview(review.Findings) {
		return fmt.Errorf("failed review must explain why")
	}
	return nil
}
