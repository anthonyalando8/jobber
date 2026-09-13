package web

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every template must parse. The Phase 3 pages carry the reasoning the operator relies on, so a
// parse error here would silently blank the one screen that explains a decision.
func TestAllTemplatesParse(t *testing.T) {
	entries, err := os.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".html" {
			continue
		}
		found[entry.Name()] = true
		if _, err := template.ParseFiles(filepath.Join("templates", entry.Name())); err != nil {
			t.Fatalf("template %s does not parse: %v", entry.Name(), err)
		}
	}
	for _, required := range []string{"plan.html", "opportunity.html", "company.html"} {
		if !found[required] {
			t.Fatalf("missing Phase 3 template %s", required)
		}
	}
}

// Phase 3 decides what may be said and to whom. It must not have re-enabled generation or
// delivery on the way.
func TestPhase3KeepsConstructionInterlock(t *testing.T) {
	if LegacySendEnabled {
		t.Fatal("the legacy send route must stay permanently closed")
	}
	h := &Handler{}
	if err := h.executeSend(t.Context(), 1, 1, false, "", false); err == nil {
		t.Fatal("sending must refuse before touching the database")
	} else if !strings.Contains(err.Error(), "permanently closed") {
		t.Fatalf("unexpected send refusal: %v", err)
	}
	if h.HasAIProvider() {
		t.Fatal("no AI provider may be configured during construction")
	}
}

// The contact-plan workspace must state that recording a plan does not authorize sending.
func TestPlanTemplateStatesItDoesNotAuthorizeSending(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("templates", "plan.html"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{
		"authorizes draft generation only",
		"all sending remain disabled",
		"Claims the composer must never make",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("the plan workspace must state %q", required)
		}
	}
}
