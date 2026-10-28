package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/store"
)

func TestCompositionStageInWords(t *testing.T) {
	for _, tc := range []struct {
		d    store.ComposedDraft
		want string
	}{
		{store.ComposedDraft{Status: "queued"}, "Waiting to start"},
		{store.ComposedDraft{Status: "running"}, "Writing the email"},
		{store.ComposedDraft{Status: "running", Attempts: []composition.Attempt{{Failure: "review_pending"}}}, "Reviewing the email"},
		{store.ComposedDraft{Status: "running", Attempts: []composition.Attempt{{Failure: "validation_failed"}}}, "Rewriting the email after a failed check"},
		{store.ComposedDraft{Status: "reviewed"}, ""},
	} {
		if got := compositionStage(&tc.d); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.d.Status, got, tc.want)
		}
	}
}

// While a run is in progress the page explains what is happening, polls for itself and does not
// show an empty editor or a confusing send blocker.
func TestRunningCompositionPageUpdatesItself(t *testing.T) {
	tmpl, err := template.ParseFiles("templates/composition.html")
	if err != nil {
		t.Fatal(err)
	}
	draft := store.ComposedDraft{ID: 6, Status: "running", Snapshot: composition.Snapshot{Intent: plan.ServiceOutreach, Recipient: composition.Recipient{Address: "x@example.test"}}}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, map[string]any{"Draft": draft, "Email": composition.Email{}, "Send": sendView{}, "Running": true, "Stage": "Writing the email", "AttemptCount": 0}); err != nil {
		t.Fatal(err)
	}
	page := b.String()
	for _, want := range []string{`id="run-status"`, "Writing the email…", "/status", "location.reload()", "The email appears here when the writer finishes", "become available once writing and review finish"} {
		if !strings.Contains(page, want) {
			t.Fatalf("running page missing %q", want)
		}
	}
	if strings.Contains(page, `action="/compositions/6/edit"`) {
		t.Fatal("an empty editor was shown while nothing has been written")
	}
	b.Reset()
	if err := tmpl.Execute(&b, map[string]any{"Draft": store.ComposedDraft{ID: 6, Status: "review_required"}, "Email": composition.Email{Subject: "S", Body: "B"}, "Send": sendView{}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), `id="run-status"`) {
		t.Fatal("a finished draft should not poll")
	}
}
