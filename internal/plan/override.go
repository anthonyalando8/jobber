package plan

import (
	"fmt"
	"strings"

	"github.com/anthonyalando8/prospector/internal/explain"
)

// overridable lists the blockers that are judgements about whether contact is worthwhile, which
// the operator may overrule with a stated reason. The others are not judgements but missing
// inputs (no active profile, no vacancy chosen, an invalid intent), and each has its own fix.
var overridable = map[string]bool{
	"no_company_relevance":       true,
	"service_work_not_permitted": true,
}

// OperatorOverrideCode marks the operator's own reason for contacting the company. It travels
// with the plan into composition, where the writer may use it as the purpose of the message.
const OperatorOverrideCode = "operator_override"

// MinOverrideReason is the shortest reason accepted; a reason is the record of the decision.
const MinOverrideReason = 15

// Overridable reports whether every blocker on this decision is one the operator may overrule,
// for a chosen intent.
func (d Decision) Overridable() bool {
	if d.Ready() || d.Intent == "" || len(d.Blockers) == 0 {
		return false
	}
	for _, b := range d.Blockers {
		if !overridable[b.Code] {
			return false
		}
	}
	return true
}

// Override records the operator's decision to proceed despite the blockers. Nothing is hidden:
// each blocker stays on the plan as an overridden reason, alongside the operator's own reason,
// and the plan always requires review.
func (d Decision) Override(reason, by string) (Decision, error) {
	reason = strings.Join(strings.Fields(reason), " ")
	if !d.Overridable() {
		return d, fmt.Errorf("these blockers cannot be overridden; each lists its own fix")
	}
	if len(reason) < MinOverrideReason {
		return d, fmt.Errorf("say why you are contacting this company (at least %d characters)", MinOverrideReason)
	}
	if strings.TrimSpace(by) == "" {
		by = "operator"
	}
	out := d
	out.Reasons = append(explain.Reasons{}, d.Reasons...)
	out.Reasons = out.Reasons.Add(OperatorOverrideCode, reason)
	for _, b := range d.Blockers {
		out.Reasons = out.Reasons.Add("overridden_"+b.Code,
			"Overridden by "+by+". The rule said: "+b.Text, b.Refs...)
	}
	out.Blockers = nil
	out.Outcome = OutcomeReviewRequired
	out.RequiresReview = true
	return out, nil
}
