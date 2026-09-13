package web

import (
	"bytes"
	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/store"
	"html/template"
	"net/http/httptest"
	"testing"
)

func TestManualCompositionDisabledBeforeDatabaseAccess(t *testing.T) {
	t.Setenv("EMAIL_COMPOSITION_ENABLED", "false")
	h := &Handler{}
	w := httptest.NewRecorder()
	h.CompositionGenerate(w, httptest.NewRequest("POST", "/plans/1/compose", nil))
	if w.Code != 503 {
		t.Fatalf("status %d", w.Code)
	}
	if LegacySendEnabled {
		t.Fatal("delivery interlock removed")
	}
}
func TestCompositionReviewTemplateRendersPinnedFacts(t *testing.T) {
	tmpl, err := template.ParseFiles("templates/composition.html")
	if err != nil {
		t.Fatal(err)
	}
	d := store.ComposedDraft{ID: 1, Status: "review_required", Revision: 2, StaleReasons: []string{"profile_changed"}, Snapshot: composition.Snapshot{ReviewReasons: explain.Reasons{{Code: "gap", Text: "Synthetic missing requirement"}}, Match: &opportunity.MatchResult{Classification: "possible_match"}}, Attempts: []composition.Attempt{{Source: "human", Email: composition.Email{Subject: "<script>attack</script>", Body: "Synthetic edit"}, Review: &composition.Review{Complete: true, Passed: false, Findings: []composition.Finding{{Severity: "block", Code: "unapproved_technology", Explanation: "Unsupported synthetic fact"}}}}}}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, map[string]any{"Draft": d, "Email": d.Attempts[0].Email}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"profile_changed", "possible_match", "unapproved_technology", "Synthetic missing requirement", "&lt;script&gt;"} {
		if !bytes.Contains(output.Bytes(), []byte(want)) {
			t.Fatalf("missing %q", want)
		}
	}
}
