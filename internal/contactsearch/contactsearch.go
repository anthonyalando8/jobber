// Package contactsearch is the second-stage contact discovery pipeline: when a relevant
// opportunity has no acceptable place to send anything, it looks beyond the company's own
// website for an application route, a recruiting contact or an engineering hiring contact, and
// stores what it finds with the exact source that supports it.
//
// Three boundaries define this package, and none of them is negotiable by a caller.
//
// It discovers; it does not decide. Every candidate is classified by the existing
// internal/relevance classifiers and judged by the existing internal/plan recipient rules. This
// package deliberately contains no second opinion about what makes a valid recruiter contact,
// because two such opinions would eventually disagree and the weaker one would win by accident.
//
// It records facts; it does not invent them. A candidate exists only where a source excerpt
// literally contains the fact being claimed. Finding a person's name never produces an address:
// those are separate facts with separate provenance, and the pattern-inference path is fenced
// off from publication at the type level here and by a CHECK constraint in the database.
//
// It supplies data; it authorizes nothing. Finding a recruiter does not reclassify an
// opportunity, create a contact plan, change intent or permit a send. Every Phase 3 to Phase 7
// gate stays exactly where it was.
package contactsearch

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Source types, ordered by how much a claim from them is worth. The employer stating something
// about itself outranks anyone describing the employer, and a search engine's own summary of a
// page it indexed is the weakest thing here — it is a reason to go and read the page, not a fact.
const (
	SourceJobPosting    = "job_posting"
	SourceCareersPage   = "employer_careers_page"
	SourceTeamPage      = "employer_team_page"
	SourceEmployerPage  = "employer_page"
	SourcePublicProfile = "public_profile"
	SourceThirdParty    = "third_party"
	SourceSearchSnippet = "search_snippet"
	// SourceMetadata is what the job board itself recorded in a structured field, which is a
	// statement by the source rather than text scraped out of a description.
	SourceMetadata = "source_metadata"
)

// sourceTrust is the numeric ranking stored alongside every observation. Changing these numbers
// changes what future discovery concludes; it never rewrites what a stored observation was
// worth, because the value in force at the time is persisted with the row.
var sourceTrust = map[string]int{
	SourceJobPosting:    100,
	SourceMetadata:      95,
	SourceCareersPage:   90,
	SourceTeamPage:      80,
	SourceEmployerPage:  70,
	SourcePublicProfile: 50,
	SourceThirdParty:    30,
	SourceSearchSnippet: 10,
}

// TrustFloorForPublishedEmail is the trust an address must reach before it may be treated as
// published by the employer and promoted into the operational contact table. It sits at the
// employer-owned tier deliberately: a third party asserting someone's work address, and a search
// engine's snippet claiming one, are both below it.
const TrustFloorForPublishedEmail = 70

// SnippetOnlyTrust is the trust of a candidate supported by nothing but a search result summary.
// Such a candidate is kept — it is a real lead — but it can never be high confidence and never
// becomes a contact row on its own.
const SnippetOnlyTrust = 10

// SourceTrust reports the trust of a source type. An unknown type is worth nothing rather than
// being charitably assumed to be worth something.
func SourceTrust(sourceType string) int { return sourceTrust[sourceType] }

// EmployerOwned reports whether a source type is the employer speaking about itself.
func EmployerOwned(sourceType string) bool {
	switch sourceType {
	case SourceJobPosting, SourceMetadata, SourceCareersPage, SourceTeamPage, SourceEmployerPage:
		return true
	}
	return false
}

// Candidate kinds. A route is a destination that may need no mailbox at all; a person may have
// no address; a mailbox may have no person. Collapsing these into one shape is what produces
// invented people, so they stay distinct all the way to the database.
const (
	KindApplicationEmail = "application_email"
	KindApplicationURL   = "application_url"
	KindPerson           = "person"
	KindMailbox          = "mailbox"
)

// Email provenance. The distinction this encodes is the one section 8 of the brief is about:
// an address someone published and an address a pattern suggests are not the same kind of fact,
// and no downstream code is permitted to treat them as one.
const (
	ProvenanceNone       = "none"
	ProvenanceSource     = "source_published"
	ProvenanceEmployer   = "employer_published"
	ProvenanceThirdParty = "third_party_published"
	ProvenanceInferred   = "inferred_pattern"
)

// Verification states. None of these is an assertion that a mailbox exists: this pipeline does
// not probe mailboxes, so the strongest thing it can honestly say is who published the address
// and where.
const (
	VerificationPublished       = "published"
	VerificationSourceConfirm   = "source_confirmed"
	VerificationInferred        = "inferred"
	VerificationUnverified      = "unverified"
	VerificationBad             = "bad"
	VerificationGeneric         = "generic"
	VerificationNotHumanMailbox = "not_human_mailbox"
)

// Confidence levels.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Identity bases, strongest first. See DeduplicateKey.
const (
	IdentityEmail      = "email"
	IdentityProfileURL = "profile_url"
	IdentityPersonName = "person_name"
	IdentityRouteURL   = "route_url"
)

// Evidence is one observation supporting one candidate, carrying the exact words that support
// it. A candidate with no evidence is not created.
type Evidence struct {
	SourceType string    `json:"source_type"`
	SourceURL  string    `json:"source_url"`
	Excerpt    string    `json:"excerpt"`
	Method     string    `json:"discovery_method"`
	Provider   string    `json:"provider,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// Trust is the ranking of this observation's source type.
func (e Evidence) Trust() int { return SourceTrust(e.SourceType) }

// EmployerOwned reports whether this observation is the employer speaking about itself.
func (e Evidence) EmployerOwned() bool { return EmployerOwned(e.SourceType) }

// ExcerptHash identifies the observation for deduplication, so the same sentence read twice
// refreshes one row rather than accumulating rows that look like corroboration.
func (e Evidence) ExcerptHash() string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(strings.ToLower(e.Excerpt)), " ")))
	return hex.EncodeToString(sum[:])
}

// Candidate is one discovered lead before the recipient policy has judged it. The fields are
// deliberately independent: PersonName may be set with no Email, Email may be set with no
// PersonName, and ApplicationURL may be the only useful thing here.
type Candidate struct {
	Kind            string `json:"kind"`
	PersonName      string `json:"person_name,omitempty"`
	RoleTitle       string `json:"role_title,omitempty"`
	EmployerClaimed string `json:"employer_claimed,omitempty"`
	Email           string `json:"email,omitempty"`
	EmailProvenance string `json:"email_provenance"`
	Verification    string `json:"verification"`
	ApplicationURL  string `json:"application_url,omitempty"`
	ATSProvider     string `json:"ats_provider,omitempty"`
	// Function, FunctionEvidence, EndpointPurpose and EndpointContactable are written from
	// internal/relevance by Classify. They are never set by an extractor.
	Function            string     `json:"recipient_function"`
	FunctionEvidence    string     `json:"function_evidence"`
	EndpointPurpose     string     `json:"endpoint_purpose"`
	EndpointContactable bool       `json:"endpoint_contactable"`
	Confidence          string     `json:"confidence"`
	Evidence            []Evidence `json:"evidence"`
	// ProfileURL is a public professional profile the candidate was found on, used as a
	// deduplication identity when no address is known.
	ProfileURL string `json:"profile_url,omitempty"`
}

// BestTrust is the trust of the strongest observation behind this candidate.
func (c Candidate) BestTrust() int {
	best := 0
	for _, e := range c.Evidence {
		if t := e.Trust(); t > best {
			best = t
		}
	}
	return best
}

// HasEmployerOwnedEvidence reports whether any observation is the employer speaking about itself.
func (c Candidate) HasEmployerOwnedEvidence() bool {
	for _, e := range c.Evidence {
		if e.EmployerOwned() {
			return true
		}
	}
	return false
}

// Inferred reports an address produced by pattern inference rather than by publication.
func (c Candidate) Inferred() bool { return c.EmailProvenance == ProvenanceInferred }

// DeduplicateKey is the identity two observations must share to be the same candidate.
//
// The keys are ranked by how badly a false merge would go wrong. A normalized address identifies
// a mailbox exactly. A source-published profile URL identifies a person exactly. A name plus a
// company is the weakest, because two people at one employer can share a name — so it is used
// only when nothing stronger exists, and it never merges a named person into an existing
// candidate that already has a different address.
func DeduplicateKey(c Candidate, companyDomain string) (key, basis string) {
	if address := strings.ToLower(strings.TrimSpace(c.Email)); address != "" {
		return "email:" + address, IdentityEmail
	}
	if profile := normalizeURLKey(c.ProfileURL); profile != "" {
		return "profile:" + profile, IdentityProfileURL
	}
	if route := normalizeURLKey(c.ApplicationURL); route != "" {
		return "route:" + route, IdentityRouteURL
	}
	if name := NormalizePersonName(c.PersonName); name != "" {
		return "person:" + name + "@" + strings.ToLower(strings.TrimSpace(companyDomain)), IdentityPersonName
	}
	return "", ""
}

// NormalizePersonName reduces a name to a comparison form without discarding the distinction
// between two different people. It lowercases and collapses whitespace and punctuation; it does
// not strip surnames, initials or particles, because "Jan de Vries" and "Jan de Vroom" must not
// collapse into one person.
func NormalizePersonName(name string) string {
	fields := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(name)), func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == '.'
	})
	cleaned := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.Trim(field, "-'`"); field != "" {
			cleaned = append(cleaned, field)
		}
	}
	return strings.Join(cleaned, " ")
}

func normalizeURLKey(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	raw = strings.TrimSuffix(raw, "/")
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimPrefix(raw, "www.")
	if idx := strings.IndexAny(raw, "?#"); idx >= 0 {
		raw = raw[:idx]
	}
	return raw
}
