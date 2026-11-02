package addresscheck

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/store"
)

//go:embed prompt.txt
var prompt string

// PromptVersion names the AI prompt, recorded with each opinion.
const PromptVersion = "address-ai-v1"

// AI verdicts, as stored.
const (
	AIPlausible   = "plausible"
	AIDoubtful    = "doubtful"
	AIImplausible = "implausible"
	AIFailed      = "failed"
)

// Opinion is the AI's answer.
type Opinion struct {
	Verdict string   `json:"verdict"`
	Reasons []string `json:"reasons"`
}

// Judge gives an AI opinion on one address.
type Judge interface {
	Judge(ctx context.Context, s store.AddressCheckSubject, deterministic Result) (Opinion, string, error)
}

// ProviderJudge asks the configured review provider.
type ProviderJudge struct {
	Provider *composition.JSONProvider
}

// Judge sends the facts and returns the opinion and the model that gave it.
func (j ProviderJudge) Judge(ctx context.Context, s store.AddressCheckSubject, deterministic Result) (Opinion, string, error) {
	if j.Provider == nil {
		return Opinion{}, "", fmt.Errorf("no AI provider is configured")
	}
	input := map[string]any{
		"address": s.Email, "company": s.CompanyName, "company_domain": s.CompanyDomain,
		"person": s.PersonName, "role": s.RoleTitle, "how_found": provenanceLabel(s.Provenance),
		"known_company_addresses": s.KnownAddresses, "evidence_excerpts": s.Evidence,
		"deterministic_checks": deterministic.Checks,
	}
	raw, meta, err := j.Provider.CallJSON(ctx, prompt, input, PromptVersion)
	if err != nil {
		return Opinion{}, meta.Model, err
	}
	var o Opinion
	if err := composition.Decode(raw, &o); err != nil {
		return Opinion{}, meta.Model, err
	}
	return Clean(o, deterministic), meta.Model, nil
}

// Clean bounds an opinion and enforces the one rule the AI is not trusted with: a hard
// deterministic fault can never be called plausible.
func Clean(o Opinion, deterministic Result) Opinion {
	o.Verdict = strings.ToLower(strings.TrimSpace(o.Verdict))
	switch o.Verdict {
	case AIPlausible, AIDoubtful, AIImplausible:
	default:
		return Opinion{Verdict: AIFailed, Reasons: []string{"The AI answered with something other than a verdict."}}
	}
	if o.Verdict == AIPlausible && deterministic.Verdict == store.AddressWillNotReceive {
		o.Verdict = AIDoubtful
		o.Reasons = append(o.Reasons, "Lowered from plausible: a deterministic check found a hard fault.")
	}
	reasons := []string{}
	for _, r := range o.Reasons {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if len([]rune(r)) > 300 {
			r = string([]rune(r)[:300])
		}
		reasons = append(reasons, r)
		if len(reasons) == 5 {
			break
		}
	}
	o.Reasons = reasons
	return o
}

func provenanceLabel(p string) string {
	switch p {
	case "employer_published":
		return "published by the employer"
	case "source_published":
		return "published by the job source"
	case "third_party_published":
		return "published by a third party, not the employer"
	case "inferred_pattern":
		return "inferred from the company's address format; nobody published it"
	}
	return "unknown"
}
