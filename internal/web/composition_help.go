package web

import (
	"slices"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/store"
)

// findingFixes says, for each check the email can fail, what to change in the text. A check that
// is not listed falls back to its own explanation: the reviewer's codes are open-ended.
var findingFixes = map[string]string{
	"unapproved_technology":      "Remove the quoted sentence, or reword it without that technology. The email may only name technologies selected in the contact plan. A technology you do not have should simply be left out, even as an admitted gap. If you do have it, add it to your profile and the plan, then compose again.",
	"acknowledged_skill_gap":     "Remove the sentence about the gap. An honest gap is allowed but always needs review, so an email that keeps it cannot be approved. Leave it for the conversation.",
	"cv_not_configured":          "Delete the mention of your CV or résumé (for example \"My CV is available on request\"), or add a CV link to your candidate profile's resources and compose again.",
	"fabricated_attachment":      "Remove words like \"attached\", \"attachment\" or \"enclosed\". Emails carry no attachments; link a resource from your profile instead.",
	"resource_not_configured":    "Remove the quoted link, or add it to your candidate profile's resources first. Only links from your profile or the listing are allowed.",
	"unsupported_numeric_claim":  "Remove the quoted number, or write it in words only if your profile states it. Every number must come from a fact in your profile.",
	"application_title_mismatch": "Use the advertised title exactly as written, in the subject and in the sentence that says you are applying.",
	"application_intent_unclear": "Say plainly that you are applying, for example \"I am applying for the … role\".",
	"intent_contradiction":       "This plan is not an application. Remove \"apply\" or \"applying\" from the subject and body.",
	"unrelated_title_leakage":    "Remove the quoted job title. It is where the company was found, not the role this email is about.",
	"invented_recipient_name":    "Open with \"Hello\" or \"Hello hiring team\". No personal name is known for this address.",
	"formulaic_language":         "Replace the quoted stock phrase with a specific, true detail.",
	"length_guidance":            "Bring the body to roughly 100–220 words. The number quoted is the current word count.",
	"email_length_or_header":     "Keep the subject to one line under 160 characters and the body under 4000 characters.",
	"output_artifacts":           "Remove placeholders such as [Name], JSON, code fences or notes to yourself.",
	"fake_thread":                "Remove \"Re:\" or \"Fwd:\" from the subject. This is a first contact.",
	"empty_email":                "Fill in both the subject and the body.",
	"missing_claim_provenance":   "This text has no link to the facts in your profile, so it always needs review. Use Discard and compose again to get a draft tied to your profile.",
	"invalid_claim_reference":    "Use Discard and compose again. The draft refers to facts that are no longer in the plan.",
	"unselected_capability":      "Use Discard and compose again. The draft uses a capability that is not in the plan.",
	"unselected_project":         "Use Discard and compose again. The draft uses a project that is not in the plan.",
	"payment_subject_leakage":    "Remove payment wording from the subject.",
	"recipient_payment_problem":  "Remove any suggestion that their payments are broken or that you will fix them.",
	"invalid_snapshot":           "The pinned plan is no longer valid. Use Discard and compose again from the current plan.",
	"intent_changed":             "The writer changed what this email is for. Use Discard and compose again.",
	"recipient_changed":          "The writer changed the recipient. Use Discard and compose again.",
	"opportunity_changed":        "The writer changed the job. Use Discard and compose again.",
	"malformed_model_output":     "The writer returned unusable output for that attempt. A later attempt or your edit replaces it, so no action is needed for this one.",
}

// attemptOutcomes explains why the latest check did not finish, and what to do.
var attemptOutcomes = map[string]string{
	"review_unavailable":         "The AI review did not complete, so nothing has been checked yet. Your text is saved: click Save edit and review to run the review again. If it keeps failing, check the reviewer provider on the dashboard.",
	"review_budget_unavailable":  "The AI request budget is used up for now, so the review did not run. Wait for the budget to free up, then click Save edit and review.",
	"compose_budget_unavailable": "The AI request budget is used up for now, so no draft was written. Try Discard and compose again later.",
	"compose_failed":             "The writer failed to produce an email. Write it yourself in the editor and click Save edit and review, or try Discard and compose again.",
	"review_pending":             "The review is still running. Refresh this page in a minute.",
}

type compositionFix struct {
	Severity, Code, Problem, Fix string
	// Excerpts are every passage this check flagged; one check is listed once.
	Excerpts []string
	Reviewer bool
}

type compositionFixes struct {
	Items   []compositionFix
	Outcome string
	// ErrorCategory is the provider's error category when the review did not complete.
	ErrorCategory string
}

// currentFixes lists what stands between the latest version of the email and approval: the
// checks it failed, each with what to change, and why the last check did not finish if it did not.
func currentFixes(d *store.ComposedDraft) compositionFixes {
	out := compositionFixes{}
	if d == nil || len(d.Attempts) == 0 || d.Status == "reviewed" {
		return out
	}
	latest := d.Attempts[len(d.Attempts)-1]
	out.Outcome = attemptOutcomes[latest.Failure]
	out.ErrorCategory = latest.ReviewMetadata.ErrorCategory
	if latest.Failure == "compose_failed" || latest.Failure == "compose_budget_unavailable" {
		out.ErrorCategory = latest.ComposeMetadata.ErrorCategory
		return out
	}
	seen := map[string]int{}
	add := func(f composition.Finding, reviewer bool) {
		if f.Severity == "info" {
			return
		}
		if i, ok := seen[f.Code]; ok {
			if f.Excerpt != "" && !slices.Contains(out.Items[i].Excerpts, f.Excerpt) {
				out.Items[i].Excerpts = append(out.Items[i].Excerpts, f.Excerpt)
			}
			if f.Severity == "block" {
				out.Items[i].Severity = "block"
			}
			return
		}
		fix := findingFixes[f.Code]
		if fix == "" {
			fix = "Rewrite or remove the quoted text so that this no longer applies, then save."
		}
		item := compositionFix{Severity: f.Severity, Code: f.Code, Problem: f.Explanation, Fix: fix, Reviewer: reviewer}
		if f.Excerpt != "" {
			item.Excerpts = []string{f.Excerpt}
		}
		seen[f.Code] = len(out.Items)
		out.Items = append(out.Items, item)
	}
	for _, f := range latest.Validation {
		add(f, false)
	}
	if latest.Review != nil {
		for _, f := range latest.Review.Findings {
			add(f, true)
		}
	}
	return out
}
