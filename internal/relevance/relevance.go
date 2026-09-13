// Package relevance answers one question about a company: is there a truthful engineering
// reason to contact it? It never answers whether a particular job suits the candidate — that is
// the matcher's job — and it counts each distinct fact once no matter how often it was observed.
package relevance

import (
	"sort"
	"strings"

	"github.com/anthonyalando8/prospector/internal/evidence"
	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/profile"
)

// AssessorVersion changes whenever the categories or caps below change.
const AssessorVersion = "relevance-v1"

// Category caps. Each category is bounded so no amount of repeated evidence can dominate, and
// the totals are a prioritization aid rather than any kind of permission.
const (
	CapOpportunity = 25
	CapNeed        = 30
	CapCapability  = 30
	CapContact     = 15
	capLegacyNeed  = 10
)

// Category is one capped, explainable component of company relevance.
type Category struct {
	Key           string          `json:"key"`
	Label         string          `json:"label"`
	Points        int             `json:"points"`
	Max           int             `json:"max"`
	CountedGroups []string        `json:"counted_groups,omitempty"`
	Reasons       explain.Reasons `json:"reasons,omitempty"`
	// LegacyOnly marks a category whose every point came from a pre-v2 keyword detection. Such
	// a category still contributes to the ranking score, but it cannot on its own establish
	// that there is a truthful reason to contact the company.
	LegacyOnly bool `json:"legacy_only,omitempty"`
}

// OpportunitySummary is the per-opportunity input relevance is allowed to see. Relevance reads
// only the authoritative classification, never a score.
type OpportunitySummary struct {
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	Classification string `json:"classification"`
	// PaymentRequirement is true when the vacancy itself states a payment-engineering demand.
	PaymentRequirement bool `json:"payment_requirement"`
}

// Result is the stored company relevance assessment.
type Result struct {
	AssessorVersion   string          `json:"assessor_version"`
	CompanyID         int64           `json:"company_id"`
	ProfileRevisionID int64           `json:"profile_revision_id"`
	Score             int             `json:"score"`
	Relevant          bool            `json:"relevant"`
	Categories        []Category      `json:"categories"`
	Reasons           explain.Reasons `json:"reasons"`
	// PaymentNeedEvidenced is the only thing that may justify a payment-specific approach.
	// Accepting payments, having a checkout, or the candidate owning a payments project never
	// set it.
	PaymentNeedEvidenced bool     `json:"payment_need_evidenced"`
	PaymentNeedRefs      []string `json:"payment_need_refs,omitempty"`
	// UsableContact reports whether any contact could receive a message at all.
	UsableContact bool `json:"usable_contact"`
	// GroupCount is how many distinct fact groups were considered, and ObservationCount how
	// many raw rows they came from. A large gap between the two is repetition, not evidence.
	GroupCount       int `json:"group_count"`
	ObservationCount int `json:"observation_count"`
}

// Input is everything the company-relevance rules may read.
type Input struct {
	CompanyID         int64
	Profile           *profile.Document
	ProfileRevisionID int64
	Groups            []evidence.Group
	Opportunities     []OpportunitySummary
	Contacts          []ContactAssessment
}

// Assess computes explainable company relevance. Every category is capped and every counted
// fact group is named, so a reader can see exactly which evidence produced the number.
func Assess(in Input) Result {
	result := Result{
		AssessorVersion:   AssessorVersion,
		CompanyID:         in.CompanyID,
		ProfileRevisionID: in.ProfileRevisionID,
		Reasons:           explain.Reasons{},
		GroupCount:        len(in.Groups),
	}
	for _, g := range in.Groups {
		result.ObservationCount += g.Observations
	}
	if result.ObservationCount > result.GroupCount {
		result.Reasons = result.Reasons.Add("evidence_grouped",
			itoa(result.ObservationCount)+" stored observations reduce to "+itoa(result.GroupCount)+
				" distinct fact groups. Repeated observations of the same fact are counted once.")
	}

	opportunityCategory := assessOpportunities(in.Opportunities)
	needCategory, paymentRefs := assessNeed(in.Groups, in.Opportunities)
	capabilityCategory := assessCapability(in.Groups, in.Profile)
	contactCategory := assessContacts(in.Contacts)

	result.Categories = []Category{opportunityCategory, needCategory, capabilityCategory, contactCategory}
	for _, c := range result.Categories {
		result.Score += c.Points
		result.Reasons = append(result.Reasons, c.Reasons...)
	}
	if result.Score > 100 {
		result.Score = 100
	}
	result.PaymentNeedRefs = paymentRefs
	result.PaymentNeedEvidenced = len(paymentRefs) > 0
	for _, c := range in.Contacts {
		if c.Usable {
			result.UsableContact = true
		}
	}
	// A pre-v2 keyword detection is a lead worth re-researching, not an established reason to
	// write to someone. Calibration against the live estate found 392 companies carrying a
	// legacy hiring_backend detection, which would otherwise all have become contactable on
	// that alone.
	establishedNeed := needCategory.Points > 0 && !needCategory.LegacyOnly
	result.Relevant = opportunityCategory.Points > 0 || establishedNeed || capabilityCategory.Points > 0
	if !result.Relevant {
		if needCategory.LegacyOnly && needCategory.Points > 0 {
			result.Reasons = result.Reasons.Add("legacy_detection_needs_reresearch",
				"The only engineering evidence for this company is a pre-v2 keyword detection, which is a detection summary rather than anything the company stated. Re-research the company before treating it as contactable.")
		} else {
			result.Reasons = result.Reasons.Add("no_engineering_reason_established",
				"No relevant vacancy, no stated engineering need and no product or service evidence matching an approved capability. There is no truthful engineering reason to contact this company yet.")
		}
	}
	if !result.PaymentNeedEvidenced {
		result.Reasons = result.Reasons.Add("payment_need_not_evidenced",
			"Nothing in the stored evidence states a payment-engineering need or request. Payment acceptance, a checkout, or the candidate's own payments experience are not evidence of a payment problem.")
	}
	return result
}

func assessOpportunities(opportunities []OpportunitySummary) Category {
	c := Category{Key: "relevant_opportunity", Label: "A current advertised vacancy in an approved desired family", Max: CapOpportunity, Reasons: explain.Reasons{}}
	best := ""
	var bestOpportunity OpportunitySummary
	for _, o := range opportunities {
		switch o.Classification {
		case opportunity.ClassificationStrong:
			if best != opportunity.ClassificationStrong {
				best, bestOpportunity = o.Classification, o
			}
		case opportunity.ClassificationPossible:
			if best == "" {
				best, bestOpportunity = o.Classification, o
			}
		}
	}
	switch best {
	case opportunity.ClassificationStrong:
		c.Points = CapOpportunity
		c.Reasons = c.Reasons.Add("relevant_vacancy_strong",
			"The company advertises \""+bestOpportunity.Title+"\", assessed as a strong match against the approved profile.",
			"opportunity:"+itoa64(bestOpportunity.ID))
	case opportunity.ClassificationPossible:
		c.Points = 15
		c.Reasons = c.Reasons.Add("relevant_vacancy_possible",
			"The company advertises \""+bestOpportunity.Title+"\", assessed as a possible match requiring review.",
			"opportunity:"+itoa64(bestOpportunity.ID))
	default:
		if len(opportunities) > 0 {
			c.Reasons = c.Reasons.Add("no_relevant_vacancy",
				itoa(len(opportunities))+" advertised vacancies are stored, none in an approved desired engineering family. They may still identify an interesting company, but none is a role to apply for.")
		}
	}
	return c
}

// assessNeed counts only evidence that the company itself stated a need or request. Detecting a
// technology on a page is not a need, and a legacy keyword detection carries reduced weight
// because its text is a detection summary, not the company's own words.
func assessNeed(groups []evidence.Group, opportunities []OpportunitySummary) (Category, []string) {
	c := Category{Key: "engineering_need", Label: "An engineering need or request the company stated itself", Max: CapNeed, Reasons: explain.Reasons{}, LegacyOnly: true}
	var paymentRefs []string
	legacyPoints := 0
	for _, g := range groups {
		if !g.Semantics.NeedEvidence {
			continue
		}
		if g.Legacy {
			if legacyPoints >= capLegacyNeed {
				continue
			}
			legacyPoints += 5
			c.Points += 5
			c.CountedGroups = append(c.CountedGroups, g.Key)
			c.Reasons = c.Reasons.Add("legacy_need_detection",
				"Legacy detection \""+g.Detail+"\" suggests an engineering need. It is a keyword detection, not the company's own words, and carries reduced weight.",
				g.Cited()...)
			continue
		}
		if c.Points >= CapNeed {
			continue
		}
		c.Points += 15
		c.LegacyOnly = false
		c.CountedGroups = append(c.CountedGroups, g.Key)
		c.Reasons = c.Reasons.Add("stated_engineering_need",
			"The company states: "+quote(g.Excerpt, g.Detail), g.Cited()...)
		if g.Semantics.PaymentNeed {
			paymentRefs = append(paymentRefs, g.Cited()...)
		}
	}
	for _, o := range opportunities {
		if o.PaymentRequirement && o.Classification != opportunity.ClassificationUnrelated {
			paymentRefs = append(paymentRefs, "opportunity:"+itoa64(o.ID))
		}
	}
	if c.Points > CapNeed {
		c.Points = CapNeed
	}
	sort.Strings(paymentRefs)
	return c, paymentRefs
}

// assessCapability credits evidence describing what the company actually builds or sells, but
// only where it matches an approved capability or technology. A generic technology detection is
// explicitly excluded: finding "react" in page source says nothing about a need.
func assessCapability(groups []evidence.Group, doc *profile.Document) Category {
	c := Category{Key: "capability_relevance", Label: "Company products or services matching an approved capability", Max: CapCapability, Reasons: explain.Reasons{}}
	if doc == nil {
		c.Reasons = c.Reasons.Add("profile_required",
			"No approved profile is active, so company work cannot be matched to approved capabilities.")
		return c
	}
	for _, g := range groups {
		if !g.Semantics.ServiceRelevance || c.Points >= CapCapability {
			continue
		}
		text := normalize(g.Detail + " " + g.Excerpt)
		var refs []string
		for _, capability := range doc.Capabilities {
			for _, token := range strings.Fields(normalize(capability.Name)) {
				if len(token) > 3 && strings.Contains(text, token) {
					refs = append(refs, "profile:capability:"+capability.ID)
					break
				}
			}
		}
		for _, tech := range doc.Technologies {
			for _, name := range append([]string{tech.Name}, tech.Aliases...) {
				if n := normalize(name); len(n) > 2 && strings.Contains(text, n) {
					refs = append(refs, "profile:technology:"+tech.ID)
					break
				}
			}
		}
		if len(refs) == 0 {
			c.Reasons = c.Reasons.Add("company_work_unmatched",
				"Company work described as \""+g.Detail+"\" does not match an approved capability, so it earns no relevance.", g.Cited()...)
			continue
		}
		c.Points += 15
		c.CountedGroups = append(c.CountedGroups, g.Key)
		c.Reasons = c.Reasons.Add("company_work_matches_capability",
			"Company work described as "+quote(g.Excerpt, g.Detail)+" matches approved capabilities.",
			append(refs, g.Cited()...)...)
	}
	if c.Points > CapCapability {
		c.Points = CapCapability
	}
	return c
}

func assessContacts(contacts []ContactAssessment) Category {
	c := Category{Key: "usable_contact", Label: "A contact who could plausibly act on the message", Max: CapContact, Reasons: explain.Reasons{}}
	genericOnly := false
	for _, a := range contacts {
		if !a.Usable {
			continue
		}
		switch a.Function {
		case FunctionEngineering, FunctionRecruiting, FunctionLeadership:
			c.Points = CapContact
			c.Reasons = c.Reasons.Add("relevant_contact_available",
				"A "+a.Function+" contact is on file ("+a.Evidence+").", "contact:"+itoa64(a.Contact.ID))
			return c
		case FunctionGeneric, FunctionUnknown:
			genericOnly = true
		}
	}
	if genericOnly {
		c.Points = 5
		c.Reasons = c.Reasons.Add("only_generic_contact",
			"Only a generic or unidentified mailbox is on file. It can receive a message, but its relevance to any purpose is unverified.")
		return c
	}
	c.Reasons = c.Reasons.Add("no_usable_contact", "No active email contact is on file.")
	return c
}

func quote(excerpt, detail string) string {
	if strings.TrimSpace(excerpt) != "" {
		return "\"" + excerpt + "\""
	}
	return "\"" + detail + "\""
}

func normalize(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func itoa(v int) string { return itoa64(int64(v)) }

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
