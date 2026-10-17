package contactsearch

import (
	"strings"
	"time"
	"unicode"
)

// Email format patterns a company can be observed to use. These are recognized from addresses
// the company actually published; none of them is ever assumed.
const (
	FormatFirstDotLast = "firstname.lastname"
	FormatFirstLast    = "firstnamelastname"
	FormatFirstInitial = "firstinitiallastname"
	FormatFirst        = "firstname"
)

// EmailFormat is a company's observed address shape, together with the address that demonstrated
// it. The example is not decoration: it is the evidence, and an inferred address that cannot
// name the published address its pattern came from should not exist.
type EmailFormat struct {
	Pattern string `json:"pattern"`
	Domain  string `json:"domain"`
	// Example is the real published address the pattern was read from.
	Example string `json:"example"`
	// ExampleSourceURL is where that address was published.
	ExampleSourceURL string `json:"example_source_url"`
}

// InferenceEnabled reports whether pattern inference is switched on at all. It is off by default
// and is a separate decision from whether discovery runs, because inference is the one part of
// this pipeline that produces a fact nobody published.
type InferenceConfig struct {
	Enabled bool
}

// DetectEmailFormat reads a company's address format from addresses it actually published,
// matched against people whose names are actually known.
//
// It requires both halves. An address alone cannot demonstrate a format — "jane.doe@acme.com"
// only shows the firstname.lastname shape once you know a Jane Doe works there. Without that
// pairing the "pattern" would be a guess about a guess, which is the failure mode section 9 of
// the brief is warning about.
func DetectEmailFormat(known []Candidate, companyDomain string) (EmailFormat, bool) {
	domain := normalizeHost(companyDomain)
	if domain == "" {
		return EmailFormat{}, false
	}
	names := map[string][2]string{} // normalized full name -> {first, last}
	for _, c := range known {
		first, last, ok := splitPersonName(c.PersonName)
		if ok {
			names[NormalizePersonName(c.PersonName)] = [2]string{first, last}
		}
	}
	if len(names) == 0 {
		return EmailFormat{}, false
	}
	for _, c := range known {
		address := strings.ToLower(strings.TrimSpace(c.Email))
		if address == "" || c.Inferred() || !publishedProvenance(c.EmailProvenance) {
			continue
		}
		at := strings.LastIndex(address, "@")
		if at <= 0 || !sameRegistrableDomain(address[at+1:], domain) {
			continue
		}
		local := address[:at]
		for _, parts := range names {
			first, last := parts[0], parts[1]
			pattern := ""
			switch local {
			case first + "." + last:
				pattern = FormatFirstDotLast
			case first + last:
				pattern = FormatFirstLast
			case first[:1] + last:
				pattern = FormatFirstInitial
			case first:
				pattern = FormatFirst
			}
			if pattern == "" {
				continue
			}
			source := ""
			if len(c.Evidence) > 0 {
				source = c.Evidence[0].SourceURL
			}
			return EmailFormat{Pattern: pattern, Domain: address[at+1:], Example: address, ExampleSourceURL: source}, true
		}
	}
	return EmailFormat{}, false
}

// InferAddress derives a candidate address for a known person from an observed company format.
//
// Everything about the result is marked as what it is. The provenance is inferred_pattern, the
// verification is inferred, the confidence is low, and the evidence attached is the published
// address the pattern was read from — not a source claiming this person's address, because no
// such source exists. A database CHECK constraint refuses to store this row as published or as
// promoted, so the marking is not merely a convention this code agrees to follow.
//
// It returns false when inference is disabled, when no format was observed, or when the person's
// name cannot be split into the parts the format needs. Returning nothing is the correct answer
// far more often than producing something is.
func InferAddress(person Candidate, format EmailFormat, cfg InferenceConfig) (Candidate, bool) {
	if !cfg.Enabled || format.Pattern == "" || format.Example == "" || format.Domain == "" {
		return Candidate{}, false
	}
	if strings.TrimSpace(person.Email) != "" {
		return Candidate{}, false // a published address is already known; nothing to infer
	}
	first, last, ok := splitPersonName(person.PersonName)
	if !ok {
		return Candidate{}, false
	}
	local := ""
	switch format.Pattern {
	case FormatFirstDotLast:
		local = first + "." + last
	case FormatFirstLast:
		local = first + last
	case FormatFirstInitial:
		local = first[:1] + last
	case FormatFirst:
		// A first-name-only format cannot distinguish two people who share a first name, so an
		// inference under it would be as likely to reach the wrong colleague as the right one.
		return Candidate{}, false
	default:
		return Candidate{}, false
	}

	inferred := person
	inferred.Kind = KindPerson
	inferred.Email = local + "@" + format.Domain
	inferred.EmailProvenance = ProvenanceInferred
	inferred.Verification = VerificationInferred
	inferred.Confidence = ConfidenceLow
	// The evidence is the observed format and the published address behind it, stated plainly.
	// It says what was actually established and does not claim anybody published this address.
	inferred.Evidence = append(append([]Evidence{}, person.Evidence...), Evidence{
		SourceType: SourceThirdParty,
		SourceURL:  format.ExampleSourceURL,
		Excerpt: "Address not published. Derived from this employer's observed address format \"" +
			format.Pattern + "\", read from the published address " + format.Example +
			", applied to the separately evidenced name \"" + person.PersonName + "\".",
		Method:     MethodPatternInfer,
		ObservedAt: time.Now().UTC(),
	})
	return inferred, true
}

// splitPersonName reduces a name to the first and last parts an address format uses. It refuses
// anything it cannot split confidently — a single name, an initial-only surname, or a name with
// characters an address cannot carry.
func splitPersonName(name string) (first, last string, ok bool) {
	parts := strings.Fields(NormalizePersonName(name))
	if len(parts) < 2 {
		return "", "", false
	}
	first, last = asciiLower(parts[0]), asciiLower(parts[len(parts)-1])
	if len(first) < 2 || len(last) < 2 {
		return "", "", false
	}
	return first, last, true
}

// asciiLower keeps only the letters an address local part can carry, so a name with an accent or
// a hyphen does not silently become a malformed address.
func asciiLower(word string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(word) {
		if r < unicode.MaxASCII && (r >= 'a' && r <= 'z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
