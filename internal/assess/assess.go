// Package assess orchestrates Phase 3: it loads stored facts, runs the deterministic matcher,
// company relevance, intent, recipient and proof selection, and persists the results. It makes
// no AI request, opens no SMTP connection and starts no worker.
package assess

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/anthonyalando8/prospector/internal/evidence"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/profile"
	"github.com/anthonyalando8/prospector/internal/relevance"
	"github.com/anthonyalando8/prospector/internal/store"
)

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// ProfileState reports whether real matching is possible at all.
type ProfileState struct {
	Active     bool
	RevisionID int64
	Revision   int
	Document   *profile.Document
	// Reason explains an inactive profile in the words the UI should show.
	Reason string
}

// Profile loads the active approved revision. An approved-but-inactive or draft revision is
// never returned: matching may only ever use facts the operator has explicitly activated.
func (s *Service) Profile(ctx context.Context) (ProfileState, error) {
	active, err := s.db.ActiveProfile(ctx)
	if err != nil {
		return ProfileState{}, err
	}
	if active == nil {
		return ProfileState{Reason: "No candidate profile revision is active. Approve and activate a revision on /profile before matching can state a fit. Nothing is inferred from legacy templates or code in the meantime."}, nil
	}
	doc := active.Document
	return ProfileState{Active: true, RevisionID: active.ID, Revision: active.Revision, Document: &doc}, nil
}

// OpportunityAssessment is one advertised vacancy with its current fit assessment.
type OpportunityAssessment struct {
	Opportunity *store.Opportunity
	Match       *opportunity.MatchResult
	Stale       bool
	StaleWhy    string
	Assessed    string
}

// Classification is the classification to display, including the non-persisted
// profile_required state.
func (a OpportunityAssessment) Classification() string {
	if a.Match == nil {
		return "not_assessed"
	}
	return a.Match.Classification
}

// AssessOpportunity recomputes and stores the assessment for one vacancy against the currently
// active approved profile.
func (s *Service) AssessOpportunity(ctx context.Context, opportunityID int64) (*OpportunityAssessment, error) {
	row, err := s.db.GetOpportunity(ctx, opportunityID)
	if err != nil {
		return nil, err
	}
	state, err := s.Profile(ctx)
	if err != nil {
		return nil, err
	}
	result := opportunity.Assess(opportunity.AssessInput{
		OpportunityID:       row.ID,
		OpportunityRevision: row.Revision,
		Facts:               row.Facts,
		Profile:             state.Document,
		ProfileRevisionID:   state.RevisionID,
	})
	assessment := &OpportunityAssessment{Opportunity: row, Match: &result}
	if result.Classification == opportunity.ClassificationProfileRequired {
		// Nothing authoritative is stored without an active approved profile.
		return assessment, nil
	}
	if err = s.db.SaveOpportunityMatch(ctx, result); err != nil {
		return nil, err
	}
	return assessment, nil
}

// AssessCompanyOpportunities recomputes every vacancy at a company.
func (s *Service) AssessCompanyOpportunities(ctx context.Context, companyID int64) ([]OpportunityAssessment, error) {
	rows, err := s.db.ListOpportunities(ctx, "", companyID)
	if err != nil {
		return nil, err
	}
	out := make([]OpportunityAssessment, 0, len(rows))
	for i := range rows {
		assessment, err := s.AssessOpportunity(ctx, rows[i].ID)
		if err != nil {
			return nil, err
		}
		out = append(out, *assessment)
	}
	return out, nil
}

// CompanyView is everything Phase 3 knows about one company: every vacancy assessed separately,
// grouped evidence, capped relevance, ranked contacts and existing plans.
type CompanyView struct {
	Company       *store.Company
	Profile       ProfileState
	Opportunities []OpportunityAssessment
	Groups        []evidence.Group
	Relevance     relevance.Result
	Contacts      []relevance.ContactAssessment
	Plans         []*store.ContactPlan
}

// LoadCompany reads stored assessments without recomputing them, flagging any that are stale.
func (s *Service) LoadCompany(ctx context.Context, companyID int64) (*CompanyView, error) {
	return s.loadCompany(ctx, companyID, false)
}

// RefreshCompany recomputes matching and relevance for a company and stores the results.
func (s *Service) RefreshCompany(ctx context.Context, companyID int64) (*CompanyView, error) {
	return s.loadCompany(ctx, companyID, true)
}

func (s *Service) loadCompany(ctx context.Context, companyID int64, refresh bool) (*CompanyView, error) {
	company, err := s.db.GetCompany(ctx, companyID)
	if err != nil {
		return nil, err
	}
	state, err := s.Profile(ctx)
	if err != nil {
		return nil, err
	}
	view := &CompanyView{Company: company, Profile: state}

	if refresh {
		if view.Opportunities, err = s.AssessCompanyOpportunities(ctx, companyID); err != nil {
			return nil, err
		}
	} else {
		rows, err := s.db.ListOpportunities(ctx, "", companyID)
		if err != nil {
			return nil, err
		}
		stored, err := s.db.LatestMatchesForCompany(ctx, companyID)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			assessment := OpportunityAssessment{Opportunity: &rows[i]}
			if m, ok := stored[rows[i].ID]; ok {
				result := m.Result
				assessment.Match, assessment.Stale, assessment.StaleWhy, assessment.Assessed = &result, m.Stale, m.StaleWhy, m.Assessed
			}
			view.Opportunities = append(view.Opportunities, assessment)
		}
	}

	if view.Groups, err = s.db.EvidenceGroups(ctx, companyID); err != nil {
		return nil, err
	}
	contacts, err := s.db.GetContactsByCompany(ctx, companyID)
	if err != nil {
		return nil, err
	}
	view.Contacts = relevance.ClassifyContacts(toRelevanceContacts(contacts))

	summaries := make([]relevance.OpportunitySummary, 0, len(view.Opportunities))
	for _, a := range view.Opportunities {
		summary := relevance.OpportunitySummary{
			ID: a.Opportunity.ID, Title: displayTitle(a.Opportunity), Classification: a.Classification(),
		}
		// A stale assessment is never treated as current relevance evidence.
		if a.Stale {
			summary.Classification = "not_assessed"
		} else if a.Match != nil {
			summary.PaymentRequirement = a.Match.StatesPaymentRequirement()
		}
		summaries = append(summaries, summary)
	}
	view.Relevance = relevance.Assess(relevance.Input{
		CompanyID: companyID, Profile: state.Document, ProfileRevisionID: state.RevisionID,
		Groups: view.Groups, Opportunities: summaries, Contacts: view.Contacts,
	})
	if refresh {
		if err = s.db.SaveCompanyRelevance(ctx, view.Relevance); err != nil {
			return nil, err
		}
	}
	if view.Plans, err = s.db.ListContactPlansForCompany(ctx, companyID); err != nil {
		return nil, err
	}
	return view, nil
}

func displayTitle(o *store.Opportunity) string {
	if strings.TrimSpace(o.Facts.Title) != "" {
		return o.Facts.Title
	}
	return "Unknown advertised title"
}

func toRelevanceContacts(contacts []*store.Contact) []relevance.Contact {
	out := make([]relevance.Contact, 0, len(contacts))
	for _, c := range contacts {
		item := relevance.Contact{ID: c.ID, Kind: c.Kind, Value: c.Value, Quality: c.Quality, Status: c.Status}
		if c.PersonName != nil {
			item.Person = *c.PersonName
		}
		if c.Role != nil {
			item.Role = *c.Role
		}
		out = append(out, item)
	}
	return out
}

// Proposal is the complete answer to "who are we contacting, why, and what may the message
// say", computed before any wording exists.
type Proposal struct {
	View          *CompanyView
	SelectedID    int64
	Match         *opportunity.MatchResult
	MatchStale    bool
	MatchStaleWhy string
	Decision      plan.Decision
	Recipients    []plan.RecipientOption
	Selection     plan.Selection
}

// Propose resolves intent, ranks recipients and selects allowed proof for a company, optionally
// against one chosen opportunity.
func (s *Service) Propose(ctx context.Context, companyID, opportunityID int64, requested plan.Intent) (*Proposal, error) {
	view, err := s.LoadCompany(ctx, companyID)
	if err != nil {
		return nil, err
	}
	p := &Proposal{View: view, SelectedID: opportunityID}
	var unrelatedDiscovery *opportunity.MatchResult
	for i := range view.Opportunities {
		a := view.Opportunities[i]
		if a.Opportunity.ID == opportunityID && a.Match != nil {
			p.Match, p.MatchStale, p.MatchStaleWhy = a.Match, a.Stale, a.StaleWhy
		}
		if a.Match != nil && !a.Stale && !a.Match.AllowsSpecificApplication() && unrelatedDiscovery == nil {
			result := *a.Match
			unrelatedDiscovery = &result
		}
	}
	if opportunityID != 0 && p.Match == nil {
		return nil, fmt.Errorf("opportunity %d has no stored assessment for this company; run the assessment first", opportunityID)
	}
	// A stale assessment is never accepted as a current fit.
	usable := p.Match
	if p.MatchStale {
		usable = nil
	}
	p.Decision = plan.ResolveIntent(plan.IntentInput{
		Profile: view.Profile.Document, Match: usable, UnrelatedDiscovery: unrelatedDiscovery,
		Relevance: view.Relevance, Requested: requested,
	})
	if p.MatchStale && p.Match != nil {
		p.Decision.Blockers = p.Decision.Blockers.Add("stale_assessment",
			"The stored assessment for the selected opportunity is stale: "+p.MatchStaleWhy+". Rerun matching before selecting a plan.")
		if p.Decision.Outcome == plan.OutcomeSuggested {
			p.Decision.Outcome, p.Decision.RequiresReview = plan.OutcomeReviewRequired, true
		}
	}
	intent := p.Decision.Intent
	if intent == "" {
		intent = requested
	}
	p.Recipients = plan.RankRecipients(intent, view.Contacts, sourceApplicationAddress(p.Match, view))
	p.Selection = plan.SelectProof(plan.SelectInput{
		Profile: view.Profile.Document, Intent: intent, Match: usable, Relevance: view.Relevance,
	})
	return p, nil
}

// sourceApplicationAddress reads an application destination the job source itself published.
func sourceApplicationAddress(match *opportunity.MatchResult, view *CompanyView) plan.SourceApplicationAddress {
	if match == nil {
		return plan.SourceApplicationAddress{}
	}
	for _, a := range view.Opportunities {
		if a.Opportunity.ID != match.OpportunityID {
			continue
		}
		meta := a.Opportunity.Facts.Metadata
		return plan.SourceApplicationAddress{Email: meta["apply_email"], URL: meta["apply_url"]}
	}
	return plan.SourceApplicationAddress{}
}

// CreatePlanRequest is an operator's explicit plan selection.
type CreatePlanRequest struct {
	CompanyID     int64
	OpportunityID int64
	Intent        plan.Intent
	ContactID     int64
	Origin        string
	// OverrideReason, when set, records the operator's decision to proceed despite blockers
	// that are judgements (see plan.Decision.Overridable), and why.
	OverrideReason string
	OverriddenBy   string
}

// CreatePlan records an explicit contact plan. It refuses without an active approved profile,
// refuses a blocked or ignorable purpose, and can never record a specific application against a
// role the matcher classified unrelated.
func (s *Service) CreatePlan(ctx context.Context, req CreatePlanRequest) (*store.ContactPlan, error) {
	if !req.Intent.Valid() {
		return nil, fmt.Errorf("choose one of the three authoritative intents")
	}
	p, err := s.Propose(ctx, req.CompanyID, req.OpportunityID, req.Intent)
	if err != nil {
		return nil, err
	}
	if !p.View.Profile.Active {
		return nil, fmt.Errorf("no candidate profile revision is active: %s", p.View.Profile.Reason)
	}
	if !p.Decision.Ready() && strings.TrimSpace(req.OverrideReason) != "" {
		if p.Decision, err = p.Decision.Override(req.OverrideReason, req.OverriddenBy); err != nil {
			return nil, err
		}
	}
	if !p.Decision.Ready() {
		return nil, fmt.Errorf("cannot record this plan: %s", firstReason(p.Decision))
	}
	recipient, err := chooseRecipient(p.Recipients, req.ContactID)
	if err != nil {
		return nil, err
	}
	record := store.ContactPlan{
		CompanyID:           req.CompanyID,
		Intent:              string(req.Intent),
		MatchClassification: "not_assessed",
		TargetFamilies:      p.Decision.TargetFamilies,
		RecipientContactID:  recipient.Assessment.Contact.ID,
		RecipientEmail:      recipient.Assessment.Contact.Value,
		RecipientName:       recipient.Assessment.Contact.Person,
		RecipientRelevance:  recipient.Relevance,
		RecipientReason:     recipient.Reason,
		ProfileRevisionID:   p.View.Profile.RevisionID,
		SelectionOrigin:     req.Origin,
		Status:              "selected",
		RequiresReview:      p.Decision.RequiresReview || recipient.Relevance == plan.RecipientReview,
		Reasons:             append(append(p.Decision.Reasons, p.Decision.Blockers...), p.Selection.Notes...),
		Selection:           p.Selection,
	}
	if record.SelectionOrigin != "manual" && record.SelectionOrigin != "suggested" {
		record.SelectionOrigin = "manual"
	}
	if p.Match != nil {
		record.MatchClassification = p.Match.Classification
	}
	switch req.Intent {
	case plan.SpecificJobApplication:
		if p.Match == nil {
			return nil, fmt.Errorf("a specific job application must pin a selected opportunity")
		}
		if p.MatchStale {
			return nil, fmt.Errorf("the selected opportunity's assessment is stale: %s", p.MatchStaleWhy)
		}
		if !p.Match.AllowsSpecificApplication() {
			return nil, fmt.Errorf("%q is classified %s and can never become a specific job application; no score or override changes this",
				p.Match.OriginalTitle, p.Match.Classification)
		}
		id, revision := p.Match.OpportunityID, p.Match.OpportunityRevision
		record.OpportunityID, record.OpportunityRevision = &id, &revision
	default:
		// A discovered listing may be kept as provenance, never as the role claimed.
		if req.OpportunityID != 0 && p.Match != nil {
			id := p.Match.OpportunityID
			record.ProvenanceOpportunityID = &id
		}
	}
	return s.db.CreateContactPlan(ctx, record)
}

func chooseRecipient(options []plan.RecipientOption, contactID int64) (plan.RecipientOption, error) {
	if contactID != 0 {
		for _, o := range options {
			if o.Assessment.Contact.ID == contactID {
				if o.Relevance == plan.RecipientUnusable {
					return o, fmt.Errorf("contact %d cannot receive email: %s", contactID, o.Reason)
				}
				return o, nil
			}
		}
		return plan.RecipientOption{}, fmt.Errorf("contact %d does not belong to this company", contactID)
	}
	best, ok := plan.BestRecipient(options)
	if !ok {
		return plan.RecipientOption{}, fmt.Errorf("no active email contact is on file for this company")
	}
	return best, nil
}

func firstReason(d plan.Decision) string {
	if len(d.Blockers) > 0 {
		return d.Blockers[0].Text
	}
	if len(d.Reasons) > 0 {
		return d.Reasons[0].Text
	}
	return d.Outcome
}

// ProcessNew classifies how each new or changed listing can be pursued, then assesses fit for
// listings with no current assessment, up to limit of each. Both steps are deterministic and local
// (no network, no AI), so discovery runs them straight away: a listing should arrive ready to
// filter and rank, not "unassessed" until someone presses a button.
func (s *Service) ProcessNew(ctx context.Context, limit int) (classified, assessed int, err error) {
	if classified, err = s.db.RefreshOpportunityAccess(ctx, limit); err != nil {
		return 0, 0, err
	}
	if marked, err := s.db.ClassifyBusinessSegments(ctx); err != nil {
		log.Printf("assess: classifying business segments: %v", err)
	} else if marked > 0 {
		log.Printf("assess: marked %d compan(ies) as agencies from their name or listings", marked)
	}
	ids, err := s.db.ListUnassessedOpportunityIDs(ctx, limit)
	if err != nil {
		return classified, 0, err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return classified, assessed, ctx.Err()
		}
		if _, err := s.AssessOpportunity(ctx, id); err != nil {
			log.Printf("assess: opportunity %d: %v", id, err)
			continue
		}
		assessed++
	}
	// Relevance answers "is there a truthful reason to approach this company", which is what lifts
	// a prospect from "general" to "technical need found". It is per company and only needs
	// recomputing after research or a profile change, so a small batch per pass keeps it current.
	companies, err := s.db.ListCompaniesNeedingRelevance(ctx, relevance.AssessorVersion, relevanceBatch)
	if err != nil {
		log.Printf("assess: companies needing relevance: %v", err)
		return classified, assessed, nil
	}
	for _, id := range companies {
		if ctx.Err() != nil {
			break
		}
		if _, err := s.RefreshCompany(ctx, id); err != nil {
			log.Printf("assess: relevance for company %d: %v", id, err)
		}
	}
	return classified, assessed, nil
}

// relevanceBatch bounds how many companies one processing pass re-assesses.
const relevanceBatch = 50
