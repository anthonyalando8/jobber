package plan

import (
	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/profile"
	"github.com/anthonyalando8/prospector/internal/relevance"
)

// IntentInput is everything intent selection may read. Legacy company source, legacy segment,
// legacy hiring score, hiring_backend signals, old service pitches and old templates are
// deliberately absent: none of them may determine a v2 intent.
type IntentInput struct {
	Profile *profile.Document
	// Match is the assessment of the operator-selected opportunity, or nil when none is chosen.
	Match *opportunity.MatchResult
	// UnrelatedDiscovery is an unrelated listing that led us to this company. It may be recorded
	// as provenance for a speculative enquiry; it is never the role being claimed.
	UnrelatedDiscovery *opportunity.MatchResult
	Relevance          relevance.Result
	// Requested is an explicit operator choice. Empty means "propose one".
	Requested Intent
}

func (in IntentInput) employmentConfirmed() bool {
	return in.Profile != nil && in.Profile.Engagement != nil && len(in.Profile.Engagement.EmploymentTypes) > 0
}

func (in IntentInput) serviceAllowed() bool {
	return in.Profile != nil && in.Profile.Engagement != nil &&
		in.Profile.Engagement.ServiceWork != nil && *in.Profile.Engagement.ServiceWork
}

func (in IntentInput) serviceRefused() bool {
	return in.Profile != nil && in.Profile.Engagement != nil &&
		in.Profile.Engagement.ServiceWork != nil && !*in.Profile.Engagement.ServiceWork
}

func (in IntentInput) engagementUnknown() bool {
	return in.Profile == nil || in.Profile.Engagement == nil
}

// ResolveIntent chooses the authoritative reason for contact, or refuses to choose. It never
// guesses an unstated preference and never lets a score override the unrelated-role invariant.
func ResolveIntent(in IntentInput) Decision {
	d := Decision{Reasons: explain.Reasons{}, Blockers: explain.Reasons{}, RequiresReview: true}
	if in.Profile == nil {
		d.Outcome = OutcomeBlocked
		d.Blockers = d.Blockers.Add("profile_required",
			"No approved candidate profile revision is active. No authoritative contact plan can become ready.")
		return d
	}
	d.TargetFamilies = targetFamilies(in.Profile)

	switch in.Requested {
	case SpecificJobApplication:
		return resolveApplication(in, d)
	case SpeculativeJobEnquiry:
		return resolveSpeculative(in, d)
	case ServiceOutreach:
		return resolveService(in, d)
	case "":
	default:
		d.Outcome = OutcomeBlocked
		d.Blockers = d.Blockers.Add("invalid_intent", "\""+string(in.Requested)+"\" is not one of the three authoritative intents.")
		return d
	}
	return proposeIntent(in, d)
}

// resolveApplication enforces the unrelated-role invariant. This is the only place a specific
// application can be authorized, and no score, company score, manual segment, checkout signal
// or legacy application status is consulted.
func resolveApplication(in IntentInput, d Decision) Decision {
	d.Intent = SpecificJobApplication
	if in.Match == nil {
		d.Outcome = OutcomeBlocked
		d.Blockers = d.Blockers.Add("no_opportunity_selected",
			"A specific job application must pin a selected opportunity revision. None was selected.")
		return d
	}
	if in.Match.Classification == opportunity.ClassificationProfileRequired {
		d.Outcome = OutcomeBlocked
		d.Blockers = d.Blockers.Add("profile_required",
			"The selected opportunity has no assessment against an active approved profile.")
		return d
	}
	if !in.Match.AllowsSpecificApplication() {
		d.Outcome = OutcomeBlocked
		d.Blockers = d.Blockers.Add("unrelated_application_role",
			"\""+in.Match.OriginalTitle+"\" is classified "+in.Match.Classification+
				" against the approved desired families. It can never become a specific job application. "+
				"No score, manual override, company segment or legacy status changes this. "+
				"Select a relevant opportunity, choose a speculative enquiry instead, or correct the source or profile facts and rerun matching.",
			"opportunity:"+itoa64(in.Match.OpportunityID))
		return d
	}
	d.Outcome = OutcomeReviewRequired
	if in.Match.Classification == opportunity.ClassificationStrong {
		d.Reasons = d.Reasons.Add("strong_fit_supports_application",
			"\""+in.Match.OriginalTitle+"\" is a strong match, so a specific application is proposed automatically.",
			"opportunity:"+itoa64(in.Match.OpportunityID))
		d.Outcome = OutcomeSuggested
		d.RequiresReview = false
	} else {
		d.Reasons = d.Reasons.Add("possible_fit_requires_review",
			"\""+in.Match.OriginalTitle+"\" is a possible match. An application may be truthful, but the stated gaps must be reviewed and acknowledged first; it is never proposed unattended.",
			"opportunity:"+itoa64(in.Match.OpportunityID))
	}
	if !in.employmentConfirmed() {
		d.Outcome = OutcomeReviewRequired
		d.RequiresReview = true
		d.Reasons = d.Reasons.Add("employment_preference_unknown",
			"The approved profile confirms no employment types, so employment interest cannot be assumed. Approve the preference or accept this application explicitly.")
	}
	return d
}

func resolveSpeculative(in IntentInput, d Decision) Decision {
	d.Intent = SpeculativeJobEnquiry
	d.Outcome = OutcomeReviewRequired
	if in.Match != nil && in.Match.AllowsSpecificApplication() {
		d.Reasons = d.Reasons.Add("relevant_vacancy_available",
			"A relevant advertised vacancy exists. A speculative enquiry is still permitted, but a specific application to \""+
				in.Match.OriginalTitle+"\" may be the more truthful choice.",
			"opportunity:"+itoa64(in.Match.OpportunityID))
	}
	if in.UnrelatedDiscovery != nil {
		d.Reasons = d.Reasons.Add("unrelated_discovery_is_provenance_only",
			"\""+in.UnrelatedDiscovery.OriginalTitle+"\" is recorded as the listing that led here. It is provenance only. "+
				"The enquiry asks about the approved desired engineering families and must never present that title as the role sought or imply an engineering vacancy exists.",
			"opportunity:"+itoa64(in.UnrelatedDiscovery.OpportunityID))
	}
	if !in.Relevance.Relevant {
		d.Outcome = OutcomeIgnore
		d.Blockers = d.Blockers.Add("no_company_relevance",
			"No truthful engineering reason to contact this company has been established, so there is nothing honest to enquire about.")
		return d
	}
	d.Reasons = d.Reasons.Add("company_relevance_established",
		"Company relevance is established, so an honest enquiry about engineering opportunities is possible.")
	if !in.employmentConfirmed() {
		d.Reasons = d.Reasons.Add("employment_preference_unknown",
			"The approved profile confirms no employment types. Employment interest is not assumed and must be confirmed at review.")
		return d
	}
	d.Reasons = d.Reasons.Add("employment_interest_confirmed", "Approved engagement preferences confirm employment interest.")
	d.Outcome = OutcomeSuggested
	d.RequiresReview = false
	return d
}

func resolveService(in IntentInput, d Decision) Decision {
	d.Intent = ServiceOutreach
	d.Outcome = OutcomeReviewRequired
	if in.serviceRefused() {
		d.Outcome = OutcomeBlocked
		d.Blockers = d.Blockers.Add("service_work_not_permitted",
			"The approved profile states that service work is not accepted.")
		return d
	}
	if !in.Relevance.Relevant {
		d.Outcome = OutcomeIgnore
		d.Blockers = d.Blockers.Add("no_company_relevance",
			"No engineering reason to contact this company is established. Having a website, a checkout or a payment method is not a reason to offer services.")
		return d
	}
	if !in.serviceAllowed() {
		d.Reasons = d.Reasons.Add("service_permission_unknown",
			"The approved profile does not confirm that service work is accepted. This is not assumed; confirm the preference or accept this outreach explicitly.")
		return d
	}
	d.Reasons = d.Reasons.Add("service_engagement_permitted", "Approved engagement preferences permit service work.")
	d.Reasons = d.Reasons.Add("company_relevance_established",
		"Company relevance is established from evidence rather than from the presence of a website or payment method.")
	d.Outcome = OutcomeSuggested
	d.RequiresReview = false
	return d
}

// proposeIntent is the default table applied when the operator has not chosen. Where both an
// employment and a service purpose are plausible, or the preferences that would separate them
// are unknown, it refuses to guess.
func proposeIntent(in IntentInput, d Decision) Decision {
	relevantVacancy := in.Match != nil && in.Match.AllowsSpecificApplication()
	employment := in.employmentConfirmed()
	service := in.serviceAllowed()

	if in.engagementUnknown() && (relevantVacancy || in.Relevance.Relevant) {
		d.Outcome = OutcomeReviewRequired
		d.Reasons = d.Reasons.Add("engagement_preferences_unknown",
			"The approved profile states no engagement preferences, so employment interest and service permission are both unknown. Intent must be chosen explicitly rather than guessed.")
		if relevantVacancy {
			d.Intent = SpecificJobApplication
			d.Reasons = d.Reasons.Add("relevant_vacancy_available",
				"\""+in.Match.OriginalTitle+"\" is a "+in.Match.Classification+" and is the candidate intent to review.",
				"opportunity:"+itoa64(in.Match.OpportunityID))
		}
		return d
	}
	if employment && service && in.Relevance.Relevant {
		d.Outcome = OutcomeReviewRequired
		d.Reasons = d.Reasons.Add("ambiguous_purpose",
			"Both an employment purpose and a service purpose are plausible for this company. Intent is not chosen from a company segment; select one explicitly.")
		return d
	}
	if relevantVacancy && employment {
		return resolveApplication(in, d)
	}
	if in.Relevance.Relevant && employment {
		if in.Match != nil && !in.Match.AllowsSpecificApplication() {
			in.UnrelatedDiscovery = in.Match
			in.Match = nil
		}
		return resolveSpeculative(in, d)
	}
	if in.Relevance.Relevant && service {
		return resolveService(in, d)
	}
	if relevantVacancy {
		return resolveApplication(in, d)
	}
	d.Outcome = OutcomeIgnore
	d.Reasons = d.Reasons.Add("no_reason_to_contact",
		"No relevant vacancy and no established company relevance. Keep this company in research review rather than contacting it because an address exists.")
	return d
}

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
