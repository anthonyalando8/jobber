// Package triage decides what to do with an opportunity the matcher classified unrelated: take it
// out of the working list, keep it visible, or ask a person. It is deterministic and reads only
// what is already stored (the assessment, the title, the description and the approved profile),
// so it costs no AI requests and the same inputs always give the same answer.
//
// Triage never changes an assessment. Recovering a relevant job that was classified unrelated is
// the matcher's job (see opportunity.rescueUntitledWork); triage only acts on what remains.
package triage

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/profile"
)

// Version changes whenever the rules or thresholds below change, so every opportunity is
// triaged again under the new rules and never judged by a mix of old and new.
const Version = "triage-v1"

// Outcomes, matching opportunity_triage.outcome.
const (
	Suppress           = "suppressed"
	ConfirmedUnrelated = "confirmed_unrelated"
	ManualReview       = "manual_review"
	Kept               = "kept"
	Failed             = "failed"
)

// Confidence that the opportunity is unrelated.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
)

// Thresholds. Each one is tied to a rule the rest of the system already uses, rather than tuned
// by eye, and each is explained where it is applied.
const (
	// ContradictionCapabilities is the matcher's own bar for "the job's requirements establish an
	// engineering family" (opportunity.minInferenceCapabilities). A title the matcher rejected but
	// whose requirements clear that bar is contradictory evidence, so a person decides.
	ContradictionCapabilities = 2
	// IncidentalTechnologies is how many approved technologies a non-engineering posting may
	// mention and still be confidently unrelated: one passing mention ("experience with SQL
	// reports" in a finance role) is normal in business roles; two or more suggests technical
	// work worth keeping visible.
	IncidentalTechnologies = 1
	// MinKnownDescription is the shortest description that counts as evidence of what a job
	// involves. Below it, absence of technical content proves nothing, so triage keeps the job
	// visible rather than hiding it.
	MinKnownDescription = 200
)

// Input is everything triage may read about one opportunity.
type Input struct {
	Title             string
	Description       string
	DescriptionStatus string
	Source            string
	Match             opportunity.MatchResult
	Profile           *profile.Document
	// ReversedByOperator is true when a person has already restored a triage suppression of this
	// same revision. Triage never overrules that.
	ReversedByOperator bool
}

// Decision is the answer and every reason for it.
type Decision struct {
	Outcome    string          `json:"outcome"`
	Confidence string          `json:"confidence"`
	Reasons    explain.Reasons `json:"reasons"`
}

// Decide applies the triage policy. It only ever answers for an opportunity the matcher
// classified unrelated; anything else is not triage's business.
func Decide(in Input) Decision {
	d := Decision{Reasons: explain.Reasons{}}
	if in.Match.Classification != opportunity.ClassificationUnrelated {
		d.Outcome, d.Confidence = ConfirmedUnrelated, "none"
		d.Reasons = d.Reasons.Add("not_unrelated", "The current assessment is "+in.Match.Classification+"; triage only handles unrelated opportunities.")
		return d
	}
	if in.ReversedByOperator {
		d.Outcome, d.Confidence = Kept, Medium
		d.Reasons = d.Reasons.Add("operator_restored",
			"A person restored this posting after triage suppressed it. Triage does not overrule that for the same revision.")
		return d
	}

	capabilities := len(in.Match.SupportedCapability)
	technologies := approvedTechnologiesMentioned(in.Description, in.Profile)
	function, nonEngineering := opportunity.NonEngineeringFunction(in.Title)
	known := in.DescriptionStatus == "complete" && len(strings.TrimSpace(in.Description)) >= MinKnownDescription

	d.Reasons = d.Reasons.Add("evidence",
		strconv.Itoa(capabilities)+" approved capabilities supported by requirements; "+
			strconv.Itoa(len(technologies))+" approved technologies mentioned"+listed(technologies)+
			"; description "+describe(in.DescriptionStatus, known)+".")

	switch {
	case capabilities >= ContradictionCapabilities:
		// The title says not engineering (otherwise the matcher would have kept it), but the job
		// asks for work this profile does. Neither side wins automatically.
		d.Outcome, d.Confidence = ManualReview, Low
		d.Reasons = d.Reasons.Add("contradictory_evidence",
			"The title is not an engineering role, but the requirements support "+strconv.Itoa(capabilities)+
				" approved capabilities. A person should decide whether this is worth pursuing.")
	case nonEngineering && len(technologies) <= IncidentalTechnologies:
		// capabilities < ContradictionCapabilities here; a single supported capability comes from
		// the same single passing technology mention.
		d.Outcome, d.Confidence = Suppress, High
		d.Reasons = d.Reasons.Add("non_engineering_role",
			"The role is \""+function+"\" work and at most one approved technology is mentioned in passing.")
	case !nonEngineering && !opportunity.HasEngineeringRoleNoun(in.Title) && capabilities == 0 && len(technologies) == 0 && known:
		d.Outcome, d.Confidence = Suppress, High
		d.Reasons = d.Reasons.Add("no_engineering_content",
			"The title names no engineering role and a complete description mentions none of the approved technologies or capabilities.")
	default:
		d.Outcome, d.Confidence = ConfirmedUnrelated, Medium
		d.Reasons = d.Reasons.Add("some_technical_signal",
			"Unrelated by the matcher, but with some technical signal or an incomplete description. It stays visible as unrelated and is triaged again if the posting, profile or rules change.")
	}
	return d
}

func approvedTechnologiesMentioned(description string, doc *profile.Document) []string {
	if doc == nil || strings.TrimSpace(description) == "" {
		return nil
	}
	text := strings.ToLower(description)
	found := []string{}
	for _, tech := range doc.Technologies {
		for _, name := range append([]string{tech.Name}, tech.Aliases...) {
			name = strings.ToLower(strings.TrimSpace(name))
			// Very short names ("Go", "C") only count as their own word, and "Go" is too common an
			// English word to count at all without a qualifier.
			if len(name) < 2 || name == "go" {
				continue
			}
			if regexp.MustCompile(`(^|[^a-z0-9+#.])` + regexp.QuoteMeta(name) + `($|[^a-z0-9+#])`).MatchString(text) {
				found = append(found, tech.Name)
				break
			}
		}
	}
	return found
}

func listed(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return " (" + strings.Join(items, ", ") + ")"
}

func describe(status string, known bool) string {
	if known {
		return "complete"
	}
	if status == "" {
		return "unknown"
	}
	return status + " or too short to rule anything out"
}
