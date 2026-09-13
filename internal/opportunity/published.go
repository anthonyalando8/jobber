package opportunity

import (
	"regexp"
	"strings"
)

// Metadata keys for routes the job source itself published. These are facts stated by the
// employer or the board, not inferences, and they carry their provenance so a later reader can
// tell a published address from a guessed one.
const (
	// MetaEmployerURL is the employer's own website, where the source gave one.
	MetaEmployerURL = "employer_url"
	// MetaApplyURL is where the source says to apply.
	MetaApplyURL = "apply_url"
	// MetaApplyEmail is an application address the posting published.
	MetaApplyEmail = "apply_email"
	// MetaApplyEmailSource records where the address was found, because an address quoted in a
	// description is weaker evidence than one in a structured field.
	MetaApplyEmailSource = "apply_email_source"
	// MetaRouteProvenance names the adapter and field that supplied these values.
	MetaRouteProvenance = "route_provenance"
)

// applicationCue words that make a nearby address an application route rather than an incidental
// mention. "Email our support team at …" is not an application route; "send your CV to …" is.
var applicationCues = []string{
	"apply", "application", "applications", "send your", "send us", "submit",
	"cv", "résumé", "resume", "cover letter", "hiring", "recruit", "careers", "career",
	"interested candidates", "to apply", "reach out to", "get in touch",
}

// addressPattern is deliberately conservative: it will miss obfuscated addresses rather than
// invent malformed ones.
var addressPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// boardDomains are aggregators and applicant-tracking systems. An address at one of these is the
// board's own, never the employer's application route.
var boardDomains = []string{
	"remoteok.com", "remoteok.io", "weworkremotely.com", "remotive.com", "remotive.io",
	"himalayas.app", "linkedin.com", "indeed.com", "glassdoor.com", "greenhouse.io",
	"lever.co", "workable.com", "ashbyhq.com", "breezy.hr", "bamboohr.com", "recruitee.com",
	"smartrecruiters.com", "jobvite.com", "workday.com", "myworkdayjobs.com",
	"example.com", "example.org", "sentry.io", "sentry.wixpress.com",
}

// PublishedApplicationEmail finds an application address the posting states in its own text.
//
// It requires an application cue within a short window before the address, because a description
// commonly contains support, press or privacy addresses that are not where a candidate applies.
// Writing to the wrong one is not a small error: it is an unsolicited message to a mailbox that
// never invited it. When in doubt this returns nothing and the recipient stays unresolved, which
// the pipeline already handles.
func PublishedApplicationEmail(description string) string {
	if description == "" {
		return ""
	}
	lower := strings.ToLower(description)
	for _, location := range addressPattern.FindAllStringIndex(description, -1) {
		address := strings.ToLower(strings.Trim(description[location[0]:location[1]], ".,;:"))
		if isBoardAddress(address) {
			continue
		}
		// Look back a bounded distance for a reason to believe this is where to apply.
		start := location[0] - 120
		if start < 0 {
			start = 0
		}
		window := lower[start:location[0]]
		for _, cue := range applicationCues {
			if strings.Contains(window, cue) {
				return address
			}
		}
	}
	return ""
}

func isBoardAddress(address string) bool {
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return true
	}
	host := address[at+1:]
	for _, board := range boardDomains {
		if host == board || strings.HasSuffix(host, "."+board) {
			return true
		}
	}
	return false
}

// RecordPublishedRoute stores what a source published, with its provenance. Empty values are not
// stored at all, so an absent route stays absent rather than becoming an empty string that later
// code has to second-guess.
func RecordPublishedRoute(metadata map[string]string, provenance, employerURL, applyURL, applyEmail, emailSource string) {
	if metadata == nil {
		return
	}
	set := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			metadata[key] = value
		}
	}
	set(MetaEmployerURL, employerURL)
	set(MetaApplyURL, applyURL)
	set(MetaApplyEmail, applyEmail)
	if strings.TrimSpace(applyEmail) != "" {
		set(MetaApplyEmailSource, emailSource)
	}
	if metadata[MetaEmployerURL] != "" || metadata[MetaApplyURL] != "" || metadata[MetaApplyEmail] != "" {
		set(MetaRouteProvenance, provenance)
	}
}
