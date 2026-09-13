// Package plan decides why a recipient is being contacted, who that recipient is, and exactly
// which approved facts a later composer is allowed to use. It writes no email, calls no model
// and sends nothing: it fixes the plan before any wording exists.
package plan

import (
	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/profile"
)

// Intent is the authoritative reason for contact. There are exactly three, and neither a
// company segment nor a model ever chooses one.
type Intent string

const (
	SpecificJobApplication Intent = "specific_job_application"
	SpeculativeJobEnquiry  Intent = "speculative_job_enquiry"
	ServiceOutreach        Intent = "service_outreach"
)

func (i Intent) Valid() bool {
	switch i {
	case SpecificJobApplication, SpeculativeJobEnquiry, ServiceOutreach:
		return true
	}
	return false
}

func (i Intent) Label() string {
	switch i {
	case SpecificJobApplication:
		return "SPECIFIC_JOB_APPLICATION"
	case SpeculativeJobEnquiry:
		return "SPECULATIVE_JOB_ENQUIRY"
	case ServiceOutreach:
		return "SERVICE_OUTREACH"
	}
	return string(i)
}

// Decision outcomes. Nothing here authorizes sending; a suggested plan authorizes future draft
// generation only.
const (
	OutcomeSuggested      = "suggested"
	OutcomeReviewRequired = "review_required"
	OutcomeIgnore         = "ignore"
	OutcomeBlocked        = "blocked"
)

// TargetFamily is an approved desired engineering family. For a speculative enquiry this is
// what is being asked about — never the unrelated advertised title that led us here.
type TargetFamily struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func targetFamilies(doc *profile.Document) []TargetFamily {
	if doc == nil {
		return nil
	}
	out := make([]TargetFamily, 0, len(doc.DesiredRoles))
	for _, f := range doc.DesiredRoles {
		out = append(out, TargetFamily{ID: f.ID, Name: f.Name})
	}
	return out
}

// Decision is the resolved intent with its reasoning.
type Decision struct {
	Outcome        string          `json:"outcome"`
	Intent         Intent          `json:"intent,omitempty"`
	TargetFamilies []TargetFamily  `json:"target_families,omitempty"`
	RequiresReview bool            `json:"requires_review"`
	Reasons        explain.Reasons `json:"reasons"`
	Blockers       explain.Reasons `json:"blockers,omitempty"`
}

// Ready reports whether an authoritative plan may be recorded from this decision.
func (d Decision) Ready() bool {
	return d.Outcome == OutcomeSuggested || d.Outcome == OutcomeReviewRequired
}
