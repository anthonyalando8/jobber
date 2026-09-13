package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
)

// BuildCompositionSnapshot consumes a selected Phase 3 plan. It does not call the
// assessor, change selection, activate a profile, or infer a different intent.
func (db *DB) BuildCompositionSnapshot(ctx context.Context, planID int64) (composition.Snapshot, error) {
	var s composition.Snapshot
	p, err := db.GetContactPlan(ctx, planID)
	if err != nil {
		return s, err
	}
	if p.Status != "selected" {
		return s, fmt.Errorf("select the contact plan before composition")
	}
	active, err := db.ActiveProfile(ctx)
	if err != nil {
		return s, err
	}
	if active == nil || active.ID != p.ProfileRevisionID {
		return s, fmt.Errorf("plan profile is inactive or stale; create a current plan")
	}
	company, err := db.GetCompany(ctx, p.CompanyID)
	if err != nil {
		return s, err
	}
	contact, err := db.GetContact(ctx, p.RecipientContactID)
	if err != nil {
		return s, err
	}
	if contact.CompanyID != p.CompanyID || contact.Kind != "email" || contact.Status != "active" || contact.Value != p.RecipientEmail {
		return s, fmt.Errorf("pinned recipient is stale or unusable")
	}
	name, function := "", ""
	if contact.PersonName != nil {
		name = *contact.PersonName
	}
	if contact.Role != nil {
		function = *contact.Role
	}
	if name != p.RecipientName {
		return s, fmt.Errorf("recipient name changed; review the plan")
	}
	doc := active.Document
	s = composition.Snapshot{PolicyVersion: composition.PolicyVersion, PlanID: p.ID, PlanRevision: p.Revision, PlanHash: composition.Hash(p), ProfileRevisionID: active.ID, ProfileHash: active.ContentHash, CompanyID: company.ID, CompanyName: company.Name, CompanyDomain: company.Domain, Intent: plan.Intent(p.Intent), Recipient: composition.Recipient{ContactID: contact.ID, Address: contact.Value, Name: name, Function: function, Relevance: p.RecipientRelevance}, TargetFamilies: p.TargetFamilies, Identity: doc.Identity, PrimaryStrength: doc.PrimaryStrength, SecondaryPosition: doc.SecondaryPosition, Selection: p.Selection, ForbiddenClaims: p.Selection.ForbiddenClaims, ReviewReasons: p.Reasons, Signature: doc.Signature}
	if s.Intent == plan.SpecificJobApplication {
		if p.OpportunityID == nil || p.OpportunityRevision == nil {
			return s, fmt.Errorf("unrelated_application_role: no selected vacancy")
		}
		o, err := db.GetOpportunity(ctx, *p.OpportunityID)
		if err != nil {
			return s, err
		}
		if o.CompanyID != p.CompanyID || o.Revision != *p.OpportunityRevision || o.Facts.Availability == "closed" {
			return s, fmt.Errorf("selected vacancy is stale, closed or belongs to another company")
		}
		match, err := db.GetCurrentOpportunityMatch(ctx, o.ID)
		if err != nil {
			return s, err
		}
		if match == nil || !match.AllowsSpecificApplication() || match.Classification != p.MatchClassification || match.OriginalTitle != o.Facts.Title {
			return s, fmt.Errorf("unrelated_application_role or stale match: reassess before recording a plan")
		}
		s.OpportunityID, s.OpportunityRevision, s.AdvertisedTitle, s.Match = o.ID, o.Revision, o.Facts.Title, match
		s.Sources = append(s.Sources, composition.Source{ID: fmt.Sprintf("opportunity:%d", o.ID), URL: o.Facts.URL, Text: o.Facts.Description, Kind: "job_description", Revision: o.Revision})
	} else if p.ProvenanceOpportunityID != nil {
		o, err := db.GetOpportunity(ctx, *p.ProvenanceOpportunityID)
		if err != nil {
			return s, err
		}
		if o.CompanyID != p.CompanyID {
			return s, fmt.Errorf("provenance belongs to another company")
		}
		s.ProvenanceTitle = o.Facts.Title
	}
	techIDs, resourceIDs := map[string]bool{}, map[string]bool{}
	addClaim := func(id, text string) {
		s.AllowedClaims = append(s.AllowedClaims, composition.Claim{ID: id, Text: text, Kind: "candidate", SourceRefs: []string{id}})
	}
	addClaim("profile:identity", doc.Identity+"; primary strength: "+doc.PrimaryStrength+"; secondary positioning: "+doc.SecondaryPosition)
	for _, selected := range p.Selection.Capabilities {
		found := false
		for _, capability := range doc.Capabilities {
			if capability.ID == selected.ID {
				found = true
				s.Capabilities = append(s.Capabilities, capability)
				addClaim("profile:capability:"+capability.ID, capability.Name+": "+capability.Description)
				for _, id := range capability.TechnologyIDs {
					techIDs[id] = true
				}
			}
		}
		if !found {
			return s, fmt.Errorf("selected capability %s is not in the approved profile", selected.ID)
		}
	}
	for _, selected := range p.Selection.Projects {
		found := false
		for _, project := range doc.Projects {
			if project.ID == selected.ID {
				found = true
				s.Projects = append(s.Projects, project)
				addClaim("profile:project:"+project.ID, project.Name+": "+project.Description)
				for _, id := range project.TechnologyIDs {
					techIDs[id] = true
				}
				for _, id := range project.ResourceIDs {
					resourceIDs[id] = true
				}
				for _, f := range project.Responsibilities {
					addClaim("profile:fact:"+f.ID, f.Text)
				}
				for _, f := range project.Outcomes {
					addClaim("profile:fact:"+f.ID, f.Text)
				}
			}
		}
		if !found {
			return s, fmt.Errorf("selected project %s is not in the approved profile", selected.ID)
		}
	}
	for _, technology := range doc.Technologies {
		if techIDs[technology.ID] {
			s.Technologies = append(s.Technologies, technology)
			addClaim("profile:technology:"+technology.ID, technology.Name)
		}
	}
	for _, id := range doc.Signature.ResourceIDs {
		resourceIDs[id] = true
	}
	for _, resource := range doc.Resources {
		if resourceIDs[resource.ID] || resource.Kind == "cv" || resource.Kind == "portfolio" {
			s.Resources = append(s.Resources, resource)
			s.AllowedClaims = append(s.AllowedClaims, composition.Claim{ID: "profile:resource:" + resource.ID, Kind: "resource", Text: resource.Label, SourceRefs: []string{"profile:resource:" + resource.ID}})
		}
	}
	refs := map[string]bool{}
	for _, item := range append(append([]plan.Selected{}, p.Selection.Capabilities...), p.Selection.Projects...) {
		for _, ref := range item.SupportRefs {
			refs[ref] = true
		}
	}
	for _, reason := range p.Reasons {
		for _, ref := range reason.Refs {
			refs[ref] = true
		}
	}
	evidence, err := db.ListEvidence(ctx, p.CompanyID)
	if err != nil {
		return s, err
	}
	for _, fact := range evidence {
		id := fmt.Sprintf("evidence:%d", fact.SignalID)
		if refs[id] && len(s.Sources) < 12 {
			text := fact.Excerpt
			if strings.TrimSpace(text) == "" {
				text = fact.Detail
			}
			s.Sources = append(s.Sources, composition.Source{ID: id, URL: fact.SourceURL, Text: text, Kind: fact.Kind})
		}
	}
	s.InputHash = s.Hash()
	if err = s.Validate(); err != nil {
		return s, err
	}
	reasons, err := db.CompositionStaleReasons(ctx, s)
	if err != nil {
		return s, err
	}
	if len(reasons) > 0 {
		return s, fmt.Errorf("inputs changed while preparing snapshot: %v", reasons)
	}
	return s, nil
}

func (db *DB) CompositionStaleReasons(ctx context.Context, s composition.Snapshot) ([]string, error) {
	reasons := []string{}
	if s.PolicyVersion != composition.PolicyVersion {
		reasons = append(reasons, "composition_policy_changed")
	}
	p, err := db.GetContactPlan(ctx, s.PlanID)
	if err != nil {
		return nil, err
	}
	if p.Revision != s.PlanRevision || composition.Hash(p) != s.PlanHash || p.Status != "selected" {
		reasons = append(reasons, "contact_plan_changed")
	}
	active, err := db.ActiveProfile(ctx)
	if err != nil {
		return nil, err
	}
	if active == nil || active.ID != s.ProfileRevisionID || active.ContentHash != s.ProfileHash {
		reasons = append(reasons, "profile_changed")
	}
	contact, err := db.GetContact(ctx, s.Recipient.ContactID)
	if err != nil {
		return nil, err
	}
	name, function := "", ""
	if contact.PersonName != nil {
		name = *contact.PersonName
	}
	if contact.Role != nil {
		function = *contact.Role
	}
	if contact.CompanyID != s.CompanyID || contact.Status != "active" || contact.Value != s.Recipient.Address || name != s.Recipient.Name || function != s.Recipient.Function {
		reasons = append(reasons, "recipient_changed")
	}
	company, err := db.GetCompany(ctx, s.CompanyID)
	if err != nil {
		return nil, err
	}
	if company.Name != s.CompanyName || company.Domain != s.CompanyDomain {
		reasons = append(reasons, "company_identity_changed")
	}
	if s.OpportunityID != 0 {
		o, err := db.GetOpportunity(ctx, s.OpportunityID)
		if err != nil {
			return nil, err
		}
		if o.Revision != s.OpportunityRevision || o.Facts.Availability == "closed" {
			reasons = append(reasons, "opportunity_changed")
		}
	}
	if s.Match != nil && s.Match.MatcherVersion != opportunity.MatcherVersion {
		reasons = append(reasons, "matcher_changed")
	}
	if s.Match != nil {
		current, err := db.GetCurrentOpportunityMatch(ctx, s.OpportunityID)
		if err != nil {
			return nil, err
		}
		if current == nil {
			reasons = append(reasons, "match_changed")
		} else {
			before, after := *s.Match, *current
			// Refresh timestamps and priority scores alone do not change the plan's facts.
			before.AssessedAt, after.AssessedAt = time.Time{}, time.Time{}
			before.Score, after.Score = 0, 0
			if composition.Hash(before) != composition.Hash(after) {
				reasons = append(reasons, "match_changed")
			}
		}
	}
	if s.Selection.SelectorVersion != plan.SelectorVersion {
		reasons = append(reasons, "selection_policy_changed")
	}
	evidence, err := db.ListEvidence(ctx, s.CompanyID)
	if err != nil {
		return nil, err
	}
	for _, source := range s.Sources {
		if !strings.HasPrefix(source.ID, "evidence:") {
			continue
		}
		found := false
		for _, fact := range evidence {
			if source.ID == fmt.Sprintf("evidence:%d", fact.SignalID) {
				text := fact.Excerpt
				if strings.TrimSpace(text) == "" {
					text = fact.Detail
				}
				found = text == source.Text && fact.SourceURL == source.URL
			}
		}
		if !found {
			reasons = append(reasons, "evidence_changed:"+source.ID)
		}
	}
	return reasons, nil
}
