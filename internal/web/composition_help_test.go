package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/store"
)

// The case that prompted this: a blocked technology mention and a review that did not finish.
// The page must say what to change and how to run the check again, not just name the codes.
func TestCompositionShowsHowToFixEachFinding(t *testing.T) {
	gap := "While I bring practical Go backend and concurrency experience, I do not have direct Node.js experience"
	d := &store.ComposedDraft{ID: 2, Status: "review_required", Revision: 3, Attempts: []composition.Attempt{
		{Source: "generated", Failure: "compose_failed", Validation: []composition.Finding{{Severity: "block", Code: "malformed_model_output", Explanation: "old attempt"}}},
		{Source: "repair", Failure: "review_unavailable", Email: composition.Email{Subject: "Application for Senior Backend Engineer", Body: gap},
			ReviewMetadata: composition.Metadata{ErrorCategory: "invalid_review"},
			Validation: []composition.Finding{
				{Severity: "block", Code: "unapproved_technology", Explanation: "Technology is outside the approved selected factual envelope.", Excerpt: gap},
				{Severity: "block", Code: "unapproved_technology", Explanation: "Technology is outside the approved selected factual envelope.", Excerpt: "second sentence"},
				{Severity: "info", Code: "note", Explanation: "informational"},
			}},
	}}
	fixes := currentFixes(d)
	if len(fixes.Items) != 1 || len(fixes.Items[0].Excerpts) != 2 || fixes.Items[0].Code != "unapproved_technology" || !strings.Contains(fixes.Items[0].Fix, "Remove the quoted sentence") {
		t.Fatalf("fixes: %+v", fixes.Items)
	}
	if !strings.Contains(fixes.Outcome, "Save edit and review") || fixes.ErrorCategory != "invalid_review" {
		t.Fatalf("outcome: %+v", fixes)
	}

	tmpl, err := template.ParseFiles("templates/composition.html")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, map[string]any{"Draft": d, "Email": d.Attempts[1].Email, "Fixes": fixes, "Existing": true}); err != nil {
		t.Fatal(err)
	}
	page := b.String()
	for _, want := range []string{"stopping this email", "How to fix:", "Must fix", "invalid_review", "This plan already has an email", "/compositions/2/recompose", "Discard only"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page missing %q", want)
		}
	}

	if got := currentFixes(&store.ComposedDraft{Status: "reviewed", Attempts: d.Attempts}); len(got.Items) != 0 || got.Outcome != "" {
		t.Fatal("a reviewed email should show nothing to fix")
	}
	for code := range findingFixes {
		if strings.TrimSpace(findingFixes[code]) == "" {
			t.Fatalf("empty fix for %s", code)
		}
	}
}
