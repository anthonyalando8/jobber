package plan

import (
	"sort"
	"strings"

	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/profile"
	"github.com/anthonyalando8/prospector/internal/relevance"
)

// SelectorVersion changes whenever the selection rules or caps below change.
const SelectorVersion = "selection-v1"

// Bounded selection. A later composer may use this subset and nothing else.
const (
	MaxCapabilities = 3
	MaxProjects     = 2
)

// UseAs values describe the role an approved item plays in the future message.
const (
	UseAsCapability     = "capability"
	UseAsProof          = "proof"
	UseAsSpecialization = "optional_specialization"
)

// Positioning values. Weak evidence yields broad truthful positioning, never a narrow
// diagnosis of the recipient's problems.
const (
	PositioningTargeted = "targeted_to_stated_requirements"
	PositioningBroad    = "broad_backend_full_stack_introduction"
)

// Selected is one approved profile item the composer is allowed to use, with the reason it was
// chosen and the exact requirement or evidence that supports it.
type Selected struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	UseAs       string   `json:"use_as"`
	Reason      string   `json:"reason"`
	SupportRefs []string `json:"support_refs,omitempty"`
}

// Claim is something the composer must not assert, with the reason it is forbidden.
type Claim struct {
	Code string   `json:"code"`
	Text string   `json:"text"`
	Refs []string `json:"refs,omitempty"`
}

// Selection is the complete statement of what the future email is allowed to say about the
// candidate, and what it may never say about the recipient.
type Selection struct {
	SelectorVersion string          `json:"selector_version"`
	Intent          Intent          `json:"intent"`
	Identity        string          `json:"identity"`
	PrimaryStrength string          `json:"primary_strength"`
	Positioning     string          `json:"positioning"`
	Capabilities    []Selected      `json:"capabilities"`
	Projects        []Selected      `json:"projects"`
	AllowedRefs     []string        `json:"allowed_refs"`
	ForbiddenClaims []Claim         `json:"forbidden_claims"`
	Notes           explain.Reasons `json:"notes"`
}

// SelectInput is everything proof selection may read.
type SelectInput struct {
	Profile   *profile.Document
	Intent    Intent
	Match     *opportunity.MatchResult
	Relevance relevance.Result
}

// paymentTokens identify payments-domain work. Their presence in an approved profile item says
// what the candidate has done; it says nothing whatsoever about the recipient.
var paymentTokens = []string{
	"payment", "payments", "m-pesa", "mpesa", "paybill", "till number", "checkout",
	"billing", "reconciliation", "stripe", "paypal", "settlement", "disbursement",
}

// SelectProof chooses the bounded set of approved capabilities and projects a later composer
// may use, and states the claims it may never make.
func SelectProof(in SelectInput) Selection {
	s := Selection{
		SelectorVersion: SelectorVersion,
		Intent:          in.Intent,
		Positioning:     PositioningBroad,
		Capabilities:    []Selected{},
		Projects:        []Selected{},
		ForbiddenClaims: []Claim{},
		Notes:           explain.Reasons{},
	}
	if in.Profile == nil {
		s.Notes = s.Notes.Add("profile_required",
			"No approved candidate profile revision is active, so no capability or proof may be selected.")
		return s
	}
	doc := *in.Profile
	s.Identity, s.PrimaryStrength = doc.Identity, doc.PrimaryStrength

	requirementSupport := map[string][]string{}
	if in.Match != nil {
		for _, rm := range in.Match.Matched {
			for _, ref := range rm.ProfileRefs {
				if id, ok := strings.CutPrefix(ref, "profile:capability:"); ok {
					requirementSupport[id] = append(requirementSupport[id],
						"opportunity:requirement:"+rm.Requirement.ID)
				}
			}
		}
	}
	relevanceSupport := map[string][]string{}
	for _, category := range in.Relevance.Categories {
		if category.Key != "capability_relevance" {
			continue
		}
		for _, reason := range category.Reasons {
			for _, ref := range reason.Refs {
				if id, ok := strings.CutPrefix(ref, "profile:capability:"); ok {
					relevanceSupport[id] = append(relevanceSupport[id], reason.Refs...)
				}
			}
		}
	}

	paymentNeed := in.Relevance.PaymentNeedEvidenced || (in.Match != nil && in.Match.StatesPaymentRequirement())
	if paymentNeed {
		s.Notes = s.Notes.Add("payment_need_evidenced",
			"A payment-engineering need or requirement is actually evidenced, so payments work may be offered directly.",
			in.Relevance.PaymentNeedRefs...)
	}

	chosen := map[string]bool{}
	// 1. Direct requirement match first.
	for _, id := range sortedBySupport(requirementSupport) {
		if len(s.Capabilities) >= MaxCapabilities {
			break
		}
		capability, ok := findCapability(doc, id)
		if !ok {
			continue
		}
		item := Selected{ID: id, Kind: "capability", Name: capability.Name, UseAs: UseAsCapability,
			Reason:      "Directly matches stated requirements in this vacancy.",
			SupportRefs: requirementSupport[id]}
		s.applyPaymentRule(&item, capability.Name+" "+capability.Description, paymentNeed)
		s.Capabilities = append(s.Capabilities, item)
		chosen[id] = true
		s.Positioning = PositioningTargeted
	}
	// 2. Then actual company/product engineering relevance.
	for _, id := range sortedKeys(relevanceSupport) {
		if len(s.Capabilities) >= MaxCapabilities || chosen[id] {
			continue
		}
		capability, ok := findCapability(doc, id)
		if !ok {
			continue
		}
		item := Selected{ID: id, Kind: "capability", Name: capability.Name, UseAs: UseAsCapability,
			Reason:      "Matches what the company itself describes building or selling.",
			SupportRefs: dedupe(relevanceSupport[id])}
		s.applyPaymentRule(&item, capability.Name+" "+capability.Description, paymentNeed)
		s.Capabilities = append(s.Capabilities, item)
		chosen[id] = true
	}
	// 3. Finally broad approved backend/full-stack ability, which is truthful positioning
	//    rather than a claim about the recipient.
	for _, capability := range broadCapabilities(doc) {
		if len(s.Capabilities) >= MaxCapabilities || chosen[capability.ID] {
			continue
		}
		if isPaymentsDomain(capability.Name+" "+capability.Description) && !paymentNeed {
			continue
		}
		s.Capabilities = append(s.Capabilities, Selected{
			ID: capability.ID, Kind: "capability", Name: capability.Name, UseAs: UseAsCapability,
			Reason: "Broad approved " + doc.PrimaryStrength + " ability, offered as general positioning rather than a diagnosis.",
		})
		chosen[capability.ID] = true
	}

	maxProjects := MaxProjects
	if in.Intent == ServiceOutreach {
		maxProjects = 1
	}
	for _, item := range selectProjects(doc, in.Match, chosen, maxProjects) {
		project, _ := findProject(doc, item.ID)
		s.applyPaymentRule(&item, projectText(project), paymentNeed)
		s.Projects = append(s.Projects, item)
	}

	if len(s.Capabilities) == 0 {
		s.Positioning = PositioningBroad
		s.Notes = s.Notes.Add("weak_evidence_broad_positioning",
			"No approved capability is supported by a stated requirement or by company evidence. The message may only give broad, truthful positioning; it must not offer a narrow service or diagnose a problem.")
	}
	s.AllowedRefs = allowedRefs(s)
	s.ForbiddenClaims = append(s.ForbiddenClaims, standardForbiddenClaims(in, paymentNeed)...)
	return s
}

// applyPaymentRule is the payments invariant. A payments project or capability may prove
// backend ability, but until an actual payment need is evidenced it is demoted to an optional
// specialization and can never imply the recipient has payment trouble.
func (s *Selection) applyPaymentRule(item *Selected, text string, paymentNeed bool) {
	if !isPaymentsDomain(text) || paymentNeed {
		return
	}
	if item.Kind == "capability" {
		item.UseAs = UseAsSpecialization
	} else {
		item.UseAs = UseAsProof
	}
	item.Reason += " Its payments domain is incidental: it is usable only as evidence of the underlying backend abilities, never as a suggestion that the recipient needs payment work."
	s.Notes = s.Notes.Add("payments_item_as_backend_proof_only",
		"\""+item.Name+"\" is payments-domain work selected purely as backend proof. No payment need is evidenced for this recipient.")
}

func standardForbiddenClaims(in SelectInput, paymentNeed bool) []Claim {
	claims := []Claim{{
		Code: "unselected_profile_item",
		Text: "Only the approved capabilities and projects listed in this selection may be used. No other skill, technology, project, outcome, resource, availability or price may be introduced.",
	}}
	if !paymentNeed {
		claims = append(claims, Claim{
			Code: "recipient_payment_problem",
			Text: "No payment-engineering need is evidenced for this recipient. The message must not state or imply that their payment flow is broken, that reconciliation is failing, that they need an M-Pesa or gateway integration, or that they want payment work outsourced.",
		})
	}
	if in.Match != nil {
		for _, rm := range in.Match.Gaps {
			if rm.Outcome != opportunity.MatchUnmet {
				continue
			}
			claims = append(claims, Claim{
				Code: "unapproved_requirement_" + slug(rm.Requirement.Term),
				Text: rm.Requirement.Term + " is required by this vacancy and is not in the approved profile. It may be acknowledged honestly as a gap; experience with it must never be claimed.",
				Refs: []string{"opportunity:requirement:" + rm.Requirement.ID},
			})
		}
		if in.Intent == SpeculativeJobEnquiry && !in.Match.AllowsSpecificApplication() {
			claims = append(claims, Claim{
				Code: "unrelated_title_as_target_role",
				Text: "\"" + in.Match.OriginalTitle + "\" is provenance only. The message must not present it as the role sought, must not apply for it, and must not imply an engineering vacancy is open.",
				Refs: []string{"opportunity:" + itoa64(in.Match.OpportunityID)},
			})
		}
	}
	return claims
}

func allowedRefs(s Selection) []string {
	refs := []string{}
	for _, item := range s.Capabilities {
		refs = append(refs, "profile:capability:"+item.ID)
	}
	for _, item := range s.Projects {
		refs = append(refs, "profile:project:"+item.ID)
	}
	sort.Strings(refs)
	return refs
}

func selectProjects(doc profile.Document, match *opportunity.MatchResult, chosenCapabilities map[string]bool, limit int) []Selected {
	var out []Selected
	seen := map[string]bool{}
	add := func(id, reason string, refs []string) {
		if len(out) >= limit || seen[id] {
			return
		}
		project, ok := findProject(doc, id)
		if !ok {
			return
		}
		seen[id] = true
		out = append(out, Selected{ID: id, Kind: "project", Name: project.Name, UseAs: UseAsProof, Reason: reason, SupportRefs: refs})
	}
	if match != nil {
		for _, rm := range match.Matched {
			for _, ref := range rm.ProfileRefs {
				if id, ok := strings.CutPrefix(ref, "profile:project:"); ok {
					add(id, "Approved responsibilities on this project support a stated requirement of the vacancy.",
						[]string{"opportunity:requirement:" + rm.Requirement.ID})
				}
			}
		}
	}
	for _, project := range doc.Projects {
		for _, id := range project.CapabilityIDs {
			if chosenCapabilities[id] {
				add(project.ID, "Supports a selected approved capability.", []string{"profile:capability:" + id})
			}
		}
	}
	return out
}

func broadCapabilities(doc profile.Document) []profile.Capability {
	approved := map[string]bool{}
	for _, family := range doc.DesiredRoles {
		approved[family.ID] = true
	}
	var primary, rest []profile.Capability
	for _, capability := range doc.Capabilities {
		matched := false
		for _, id := range capability.RoleFamilyIDs {
			if approved[id] {
				matched = true
			}
		}
		if matched {
			primary = append(primary, capability)
		} else {
			rest = append(rest, capability)
		}
	}
	return append(primary, rest...)
}

func isPaymentsDomain(text string) bool {
	lower := strings.ToLower(text)
	for _, token := range paymentTokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func findCapability(doc profile.Document, id string) (profile.Capability, bool) {
	for _, c := range doc.Capabilities {
		if c.ID == id {
			return c, true
		}
	}
	return profile.Capability{}, false
}

func findProject(doc profile.Document, id string) (profile.Project, bool) {
	for _, p := range doc.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return profile.Project{}, false
}

func projectText(p profile.Project) string {
	parts := []string{p.Name, p.Description}
	for _, f := range append(append([]profile.Fact{}, p.Responsibilities...), p.Outcomes...) {
		parts = append(parts, f.Text)
	}
	return strings.Join(append(parts, p.Domains...), " ")
}

func sortedBySupport(support map[string][]string) []string {
	keys := sortedKeys(support)
	sort.SliceStable(keys, func(i, j int) bool {
		return len(support[keys[i]]) > len(support[keys[j]])
	})
	return keys
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dedupe(items []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
