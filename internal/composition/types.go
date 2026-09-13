// Package composition expresses an already selected contact plan. It cannot
// select strategy, load a database, approve candidate facts, or send mail.
package composition

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/profile"
)

const PolicyVersion = "composition-v1"
const ReviewVersion = "email-review-v1"
const MaxRepairs = 2
const MaxInputBytes = 256 * 1024
const MaxResponseBytes = 32 * 1024

type Recipient struct {
	ContactID int64  `json:"contact_id"`
	Address   string `json:"address"`
	Name      string `json:"name"`
	Function  string `json:"function"`
	Relevance string `json:"relevance"`
}
type Claim struct {
	ID         string   `json:"id"`
	Text       string   `json:"text"`
	Kind       string   `json:"kind"` // candidate, resource, source
	SourceRefs []string `json:"source_refs,omitempty"`
}
type Source struct {
	ID       string `json:"id"`
	URL      string `json:"url,omitempty"`
	Text     string `json:"text"`
	Kind     string `json:"kind"`
	Revision int    `json:"revision,omitempty"`
}

// Snapshot holds copied facts, never a handle for fetching different current data.
// InputHash is computed over canonical JSON with InputHash itself empty.
type Snapshot struct {
	PolicyVersion       string                   `json:"policy_version"`
	PlanID              int64                    `json:"plan_id"`
	PlanRevision        int                      `json:"plan_revision"`
	PlanHash            string                   `json:"plan_hash"`
	ProfileRevisionID   int64                    `json:"profile_revision_id"`
	ProfileHash         string                   `json:"profile_hash"`
	CompanyID           int64                    `json:"company_id"`
	CompanyName         string                   `json:"company_name"`
	CompanyDomain       string                   `json:"company_domain"`
	Intent              plan.Intent              `json:"intent"`
	Recipient           Recipient                `json:"recipient"`
	OpportunityID       int64                    `json:"opportunity_id,omitempty"`
	OpportunityRevision int                      `json:"opportunity_revision,omitempty"`
	AdvertisedTitle     string                   `json:"advertised_title,omitempty"`
	ProvenanceTitle     string                   `json:"provenance_title,omitempty"`
	TargetFamilies      []plan.TargetFamily      `json:"target_families"`
	Match               *opportunity.MatchResult `json:"match,omitempty"`
	Identity            string                   `json:"identity"`
	PrimaryStrength     string                   `json:"primary_strength"`
	SecondaryPosition   string                   `json:"secondary_position"`
	Selection           plan.Selection           `json:"selection"`
	Capabilities        []profile.Capability     `json:"capabilities"`
	Projects            []profile.Project        `json:"projects"`
	Technologies        []profile.Technology     `json:"technologies"`
	Resources           []profile.Resource       `json:"resources"`
	Signature           profile.Signature        `json:"signature"`
	AllowedClaims       []Claim                  `json:"allowed_claims"`
	ForbiddenClaims     []plan.Claim             `json:"forbidden_claims"`
	Sources             []Source                 `json:"untrusted_sources"`
	ReviewReasons       explain.Reasons          `json:"review_reasons"`
	InputHash           string                   `json:"input_hash"`
}

type Email struct {
	Subject           string      `json:"subject"`
	Body              string      `json:"body"`
	Intent            plan.Intent `json:"intent"`
	Recipient         string      `json:"recipient"`
	OpportunityID     int64       `json:"opportunity_id"`
	UsedClaimIDs      []string    `json:"used_claim_ids"`
	UsedCapabilityIDs []string    `json:"used_capability_ids"`
	UsedProjectIDs    []string    `json:"used_project_ids"`
}
type Finding struct {
	Severity    string `json:"severity"` // block, review, info
	Code        string `json:"code"`
	Explanation string `json:"explanation"`
	Excerpt     string `json:"excerpt"`
	ClaimID     string `json:"claim_id,omitempty"`
	Repairable  bool   `json:"repairable"`
}
type Review struct {
	Complete bool      `json:"complete"`
	Passed   bool      `json:"passed"`
	Findings []Finding `json:"findings"`
}
type Metadata struct {
	InputHash     string          `json:"request_hash,omitempty"`
	PromptHash    string          `json:"prompt_hash,omitempty"`
	OutputText    string          `json:"output_text,omitempty"`
	Usage         json.RawMessage `json:"usage,omitempty"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	PromptVersion string          `json:"prompt_version"`
	RequestID     string          `json:"request_id,omitempty"`
	AttemptedAt   time.Time       `json:"attempted_at"`
	DurationMS    int64           `json:"duration_ms"`
	ErrorCategory string          `json:"error_category,omitempty"`
}
type WriteRequest struct {
	Snapshot Snapshot  `json:"snapshot"`
	Previous *Email    `json:"previous,omitempty"`
	Findings []Finding `json:"repair_findings,omitempty"`
}

// Independent interfaces permit different providers/models without coupling the
// reviewer to the composer's explanation or private reasoning.
type Composer interface {
	Compose(context.Context, WriteRequest) (Email, Metadata, error)
}
type Reviewer interface {
	Review(context.Context, Snapshot, Email) (Review, Metadata, error)
}

func Hash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (s Snapshot) Hash() string { s.InputHash = ""; return Hash(s) }
func Decode(raw []byte, v any) error {
	if len(raw) > MaxResponseBytes {
		return fmt.Errorf("model response exceeds limit")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object without commentary")
	}
	return nil
}

func (s Snapshot) Validate() error {
	if s.PolicyVersion != PolicyVersion || s.InputHash == "" || s.Hash() != s.InputHash {
		return fmt.Errorf("invalid or obsolete composition snapshot")
	}
	if !s.Intent.Valid() || s.Selection.Intent != s.Intent || s.PlanID <= 0 || s.PlanRevision <= 0 || s.ProfileRevisionID <= 0 || s.CompanyID <= 0 || s.Recipient.ContactID <= 0 || s.Recipient.Address == "" {
		return fmt.Errorf("snapshot lacks authoritative plan/profile/recipient")
	}
	if s.Intent == plan.SpecificJobApplication {
		if s.OpportunityID <= 0 || s.OpportunityRevision <= 0 || s.AdvertisedTitle == "" || s.Match == nil || !s.Match.AllowsSpecificApplication() || s.Match.OpportunityID != s.OpportunityID || s.Match.OpportunityRevision != s.OpportunityRevision || s.Match.ProfileRevisionID != s.ProfileRevisionID || s.Match.OriginalTitle != s.AdvertisedTitle {
			return fmt.Errorf("unrelated_application_role or invalid pinned opportunity")
		}
	} else if s.OpportunityID != 0 || s.AdvertisedTitle != "" {
		return fmt.Errorf("non-application must not carry an applied-for vacancy")
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(b) > MaxInputBytes {
		return fmt.Errorf("composition context exceeds limit; select less evidence")
	}
	return nil
}
