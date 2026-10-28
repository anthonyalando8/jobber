package plan

import (
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/explain"
)

func blockedService() Decision {
	return Decision{Outcome: OutcomeIgnore, Intent: ServiceOutreach,
		Blockers: explain.Reasons{{Code: "no_company_relevance", Text: "No engineering reason is established."}}}
}

func TestJudgementBlockersCanBeOverriddenWithAReason(t *testing.T) {
	d := blockedService()
	if !d.Overridable() {
		t.Fatal("a relevance judgement must be overridable")
	}
	if _, err := d.Override("ok", "operator"); err == nil {
		t.Fatal("an override without a real reason was accepted")
	}
	reason := "Online store; I want to offer to improve its speed and checkout."
	out, err := d.Override(reason, "anthony")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Ready() || !out.RequiresReview || len(out.Blockers) != 0 {
		t.Fatalf("override did not make a reviewable plan: %+v", out)
	}
	if !out.Reasons.Has(OperatorOverrideCode) || !out.Reasons.Has("overridden_no_company_relevance") {
		t.Fatalf("the override and the overruled rule must both stay on record: %v", out.Reasons.Codes())
	}
	for _, r := range out.Reasons {
		if r.Code == OperatorOverrideCode && r.Text != reason {
			t.Fatalf("reason not kept verbatim: %q", r.Text)
		}
		if r.Code == "overridden_no_company_relevance" && !strings.Contains(r.Text, "anthony") {
			t.Fatalf("who overrode it is not recorded: %q", r.Text)
		}
	}
}

func TestMissingInputsAreNotOverridable(t *testing.T) {
	for _, code := range []string{"unrelated_application_role", "profile_required", "no_opportunity_selected", "invalid_intent"} {
		d := Decision{Outcome: OutcomeBlocked, Intent: SpecificJobApplication, Blockers: explain.Reasons{{Code: code, Text: "x"}}}
		if d.Overridable() {
			t.Fatalf("%s must be resolved, not overridden", code)
		}
		if _, err := d.Override("a perfectly good reason here", "operator"); err == nil {
			t.Fatalf("%s was overridden", code)
		}
	}
	mixed := blockedService()
	mixed.Blockers = mixed.Blockers.Add("profile_required", "No active profile.")
	if mixed.Overridable() {
		t.Fatal("one non-overridable blocker must prevent the override")
	}
	noIntent := blockedService()
	noIntent.Intent = ""
	if noIntent.Overridable() {
		t.Fatal("an override needs a chosen intent")
	}
}
