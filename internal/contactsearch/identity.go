package contactsearch

import (
	"net/url"
	"strings"
)

// Identity blocker codes.
const (
	// BlockerIdentityUncertain is the refusal this whole gate exists for: we do not go looking
	// for the employees of a company we cannot confidently identify.
	BlockerIdentityUncertain  = "company_identity_uncertain"
	BlockerNoDomain           = "company_domain_missing"
	BlockerDomainUnverified   = "company_domain_unverified"
	BlockerDomainIsAggregator = "company_domain_is_aggregator"
	BlockerNameDomainMismatch = "company_name_domain_mismatch"
)

// Identity is the answer to "do we know who this employer actually is". Established is the only
// state that permits contact discovery to run.
type Identity struct {
	Established bool   `json:"established"`
	Domain      string `json:"domain"`
	// Basis names what established the identity, so an operator can see whether it rests on a
	// human confirmation, a verification pass, or the employer URL the job source published.
	Basis   string `json:"basis,omitempty"`
	Blocker string `json:"blocker,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// CompanyIdentity is the subset of a company record this gate reads, plus what the job source
// published about the employer.
type CompanyIdentity struct {
	Name         string
	Domain       string
	DomainStatus string
	// SourceEmployerURL is the employer's own site as named by the posting, which is a stronger
	// statement of identity than any domain the system guessed from the company's name.
	SourceEmployerURL string
}

// aggregatorHosts can never be an employer's own domain. A contact found "at" one of these is a
// job board's staff or a social network's, not the employer's.
var aggregatorHosts = []string{
	"remoteok.com", "remoteok.io", "weworkremotely.com", "remotive.com", "remotive.io",
	"himalayas.app", "linkedin.com", "indeed.com", "glassdoor.com", "facebook.com",
	"twitter.com", "x.com", "instagram.com", "youtube.com", "medium.com", "github.io",
	"greenhouse.io", "lever.co", "workable.com", "ashbyhq.com", "workday.com",
	"myworkdayjobs.com", "smartrecruiters.com", "jobvite.com", "recruitee.com",
	"breezy.hr", "bamboohr.com", "teamtailor.com", "crunchbase.com", "wikipedia.org",
	"example.com", "example.org", "example.net",
}

// nameDomainSuffixes are the suffixes a company name can itself end in when the name *is* a
// domain. "Wakacje.pl" is not a company called Wakacje that happens to have a dot in it: the
// name states the domain, and that statement is evidence.
var nameDomainSuffixes = []string{
	".com", ".net", ".org", ".io", ".co", ".ai", ".app", ".dev", ".pl", ".de", ".fr", ".nl",
	".es", ".it", ".se", ".no", ".fi", ".dk", ".cz", ".ro", ".hu", ".gr", ".pt", ".ie",
	".uk", ".co.uk", ".ke", ".co.ke", ".za", ".co.za", ".ng", ".in", ".com.au", ".ca",
	".ch", ".at", ".be", ".br", ".com.br", ".mx", ".jp", ".cn", ".ru", ".ua", ".tr",
}

// EstablishIdentity decides whether contact discovery may run for a company.
//
// The rule that matters most here came from production. A company named "Wakacje.pl" was stored
// with the domain "wakacje.com", and that domain passed verification — the verifier checks that
// the company's name appears on the page, and "wakacje" appears on both. Searching for the
// recruiters of wakacje.com would have been searching a different company's staff, and the
// resulting contacts would have looked perfectly well-evidenced while being about the wrong
// employer entirely.
//
// So when the company's own name states a domain, that domain is authoritative and a stored
// domain that disagrees with it is a mismatch rather than a detail. This is a narrow rule that
// fires on a specific, checkable condition; it is not an attempt to prove every domain correct.
func EstablishIdentity(c CompanyIdentity) Identity {
	domain := normalizeHost(c.Domain)
	if domain == "" {
		return Identity{Blocker: BlockerNoDomain,
			Reason: "No domain is stored for this company, so there is no employer to research."}
	}
	if isAggregatorHost(domain) {
		return Identity{Domain: domain, Blocker: BlockerDomainIsAggregator,
			Reason: "\"" + domain + "\" is a job board, applicant tracking system or social platform, " +
				"not an employer's own site. Contacts found there would belong to that platform, not to this employer."}
	}

	// What the posting itself said the employer's site is, where it said anything.
	sourceDomain := normalizeHost(hostOf(c.SourceEmployerURL))
	if sourceDomain != "" && isAggregatorHost(sourceDomain) {
		sourceDomain = ""
	}

	// A name that is itself a domain is a statement by the employer about its own identity.
	if named := domainStatedByName(c.Name); named != "" && !sameRegistrableDomain(named, domain) {
		return Identity{Domain: domain, Blocker: BlockerNameDomainMismatch,
			Reason: "The company's own name states the domain \"" + named + "\", but \"" + domain +
				"\" is stored. These are different registrations, so the employer is not established. " +
				"Contact discovery is refused rather than searching for the staff of whoever owns \"" + domain + "\". " +
				"Correct the domain, then run the search again."}
	}

	// A published employer URL that disagrees with the stored domain is the same problem seen
	// from the other side, and it is the stronger statement of the two.
	if sourceDomain != "" && !sameRegistrableDomain(sourceDomain, domain) {
		return Identity{Domain: domain, Blocker: BlockerNameDomainMismatch,
			Reason: "The job source published \"" + sourceDomain + "\" as the employer's site, but \"" +
				domain + "\" is stored against this company. Until they agree, the employer is not established."}
	}

	switch strings.ToLower(strings.TrimSpace(c.DomainStatus)) {
	case "manual":
		return Identity{Established: true, Domain: domain, Basis: "domain confirmed by a person"}
	case "verified":
		basis := "domain verified against the company name"
		if sourceDomain != "" {
			basis = "domain verified and agrees with the employer site the job source published"
		}
		return Identity{Established: true, Domain: domain, Basis: basis}
	default:
		return Identity{Domain: domain, Blocker: BlockerDomainUnverified,
			Reason: "This company's domain is still \"" + c.DomainStatus + "\": it is a candidate, not a confirmed " +
				"employer identity. A guessed domain is not a company. Verify the domain before searching for its people."}
	}
}

// domainStatedByName returns the domain a company name itself states, or empty when the name is
// an ordinary name. It requires the whole name to be a single domain-shaped token, so
// "Acme Inc." and "Sticker Mule" are unaffected and only "Wakacje.pl"-shaped names qualify.
func domainStatedByName(name string) string {
	candidate := strings.ToLower(strings.TrimSpace(name))
	if candidate == "" || strings.ContainsAny(candidate, " \t/@") {
		return ""
	}
	for _, suffix := range nameDomainSuffixes {
		if strings.HasSuffix(candidate, suffix) && len(candidate) > len(suffix) {
			return candidate
		}
	}
	return ""
}

// sameRegistrableDomain compares two hosts at the registration boundary, so "careers.acme.com"
// and "acme.com" are the same employer while "acme.com" and "acme.pl" are not.
func sameRegistrableDomain(a, b string) bool {
	a, b = normalizeHost(a), normalizeHost(b)
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

// SameEmployerHost reports whether a URL belongs to the employer's own domain — the test that
// separates the employer speaking about itself from somebody else speaking about the employer.
func SameEmployerHost(rawURL, companyDomain string) bool {
	return sameRegistrableDomain(hostOf(rawURL), companyDomain)
}

// IsAggregatorHost reports whether a URL's host is a board, ATS or social platform.
func IsAggregatorHost(rawURL string) bool { return isAggregatorHost(normalizeHost(hostOf(rawURL))) }

func isAggregatorHost(host string) bool {
	for _, known := range aggregatorHosts {
		if host == known || strings.HasSuffix(host, "."+known) {
			return true
		}
	}
	return false
}

func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	if idx := strings.IndexAny(host, "/?#"); idx >= 0 {
		host = host[:idx]
	}
	host = strings.TrimPrefix(host, "www.")
	return strings.TrimSuffix(host, ".")
}
