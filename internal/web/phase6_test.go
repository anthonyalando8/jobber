package web

import (
	"bytes"
	"html/template"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
)

// A. A legacy unsent draft is clearly marked and offers no way to send it.
func TestLegacyDraftPageOffersNoSendPath(t *testing.T) {
	tmpl, err := template.ParseFiles("templates/legacy_drafts.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]any{
		"Explanation": legacyDraftExplanation,
		"Drafts": []store.LegacyDraft{{ID: 7, CompanyID: 3, CompanyName: "Synthetic Ltd",
			Subject: "Synthetic legacy subject", Body: "Synthetic legacy body.",
			State: "pending", Decision: "review_required", CreatedAt: time.Now()}},
	}); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	// No control anywhere on the page may submit a legacy draft into any v2 route.
	for _, forbidden := range []string{"/send", "/approval", "/compositions/7", "/review/send"} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("the legacy draft page exposes %q", forbidden)
		}
	}
	for _, want := range []string{"not sendable", "legacy pipeline", "contact plan", "Synthetic legacy body."} {
		if !strings.Contains(page, want) {
			t.Fatalf("the legacy draft page is missing %q", want)
		}
	}
}

// A, continued. The single explanation actually tells the operator what to do instead.
func TestLegacyExplanationIsActionable(t *testing.T) {
	for _, want := range []string{"legacy pipeline", "contact plan", "reviewed"} {
		if !strings.Contains(legacyDraftExplanation, want) {
			t.Fatalf("the legacy explanation does not mention %q", want)
		}
	}
	reason := legacyDraftReviewReason()
	if reason.Code != "legacy_draft" {
		t.Fatalf("legacy drafts must have their own review reason code, got %q", reason.Code)
	}
	if reason.Action == "" {
		t.Fatal("a review reason without an action is not actionable")
	}
}

// B. A legacy sent message keeps its content and reports unknown metadata as unknown.
func TestLegacyThreadPageReportsUnknownMetadataHonestly(t *testing.T) {
	tmpl, err := template.ParseFiles("templates/legacy_threads.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]any{
		"Unclassified": 1, "OnlyUnclassified": false,
		"Intents": []string{string(plan.SpecificJobApplication), store.LegacyIntentUnknown},
		"Threads": []store.LegacyThread{{SendLogID: 5, CompanyID: 2, CompanyName: "Synthetic Ltd",
			ToValue: "someone@example.test", SentAt: time.Now(),
			Subject: "Original subject", Body: "Original body, unchanged."}},
	}); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	if !strings.Contains(page, "Original body, unchanged.") {
		t.Fatal("the original sent body is not shown as written")
	}
	for _, want := range []string{"unknown", "not yet classified", "never guessed"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the legacy thread page is missing %q", want)
		}
	}
}

// C. Legacy templates are visible but cannot drive v2 generation, and cannot be edited.
func TestLegacyTemplatesAreReadOnly(t *testing.T) {
	tmpl, err := template.ParseFiles("templates/templates_admin.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]any{
		"templates": []struct {
			ID          int64
			Segment     string
			Channel     string
			RoleFilter  string
			Subject     string
			Body        string
			DisplayOnly bool
			Vars        []string
		}{{ID: 1, Segment: "hiring", Channel: "email", Subject: "Legacy subject",
			Body: "Legacy payments copy.", Vars: []string{"{{company}}"}}},
		"Mode": send.ModeReview, "AutomaticSending": false, "AutoSendIntents": []string{},
	}); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	if strings.Contains(page, `action="/templates/1/update"`) {
		t.Fatal("the templates page still offers a legacy edit form")
	}
	if strings.Contains(page, "<textarea") {
		t.Fatal("the templates page still offers an editable legacy body")
	}
	if !strings.Contains(page, "Legacy payments copy.") {
		t.Fatal("legacy template text must remain visible as history")
	}
	for _, want := range []string{"read-only", "no longer produce any outgoing message", "internal/composition/prompts"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the templates page does not explain %q", want)
		}
	}
}

// C, continued. The route itself refuses, not merely the page.
func TestLegacyTemplateEditRouteIsClosed(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	h.LegacyTemplateEditClosed(w, httptest.NewRequest("POST", "/templates/1/update", nil))
	if w.Code != 410 {
		t.Fatalf("legacy template editing returned %d, want 410", w.Code)
	}
	if !strings.Contains(w.Body.String(), "read-only") {
		t.Fatal("the refusal does not say why")
	}
}

// D. The old application-mode control cannot imply a v2 intent. It now writes nothing at all.
func TestApplyModeCannotSetV2Intent(t *testing.T) {
	source, err := os.ReadFile("company_actions.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	start := strings.Index(body, "func (h *Handler) CompanyApplyMode(")
	if start < 0 {
		t.Fatal("CompanyApplyMode no longer exists; update this test deliberately")
	}
	end := strings.Index(body[start:], "\n}\n")
	handler := body[start : start+end]
	// The point is not that it stopped setting a segment, but that it writes nothing: any
	// write here would be a company-wide flag standing in for a per-message decision.
	for _, forbidden := range []string{"SetCompanySegment", "CreatePlan", "Intent", "Insert", "Update"} {
		if strings.Contains(handler, forbidden) {
			t.Fatalf("CompanyApplyMode still performs %q; it must only redirect to the contact plan", forbidden)
		}
	}
	if !strings.Contains(handler, "/plan") {
		t.Fatal("CompanyApplyMode does not redirect to the contact plan workspace")
	}
}

// E and F are properties of the matcher and intent resolver, which own them. What the web layer
// must not do is offer a route that bypasses either, so no legacy generation route survives.
func TestLegacyGenerationRoutesAreClosed(t *testing.T) {
	h := &Handler{}
	for _, path := range []string{"/companies/1/generate-draft", "/admin/draft"} {
		w := httptest.NewRecorder()
		h.LegacyGenerateClosed(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 410 {
			t.Fatalf("%s returned %d, want 410", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "contact plan") {
			t.Fatalf("%s does not point at the v2 replacement", path)
		}
	}
}

// The router must not wire any handler that generates or sends outside the v2 path. This reads
// the route table itself, because the risk is a future re-wiring, not today's code.
func TestRouterWiresNoLegacyGenerationOrSendHandler(t *testing.T) {
	routes, err := os.ReadFile("../../cmd/prospector/main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(routes)
	for handler, why := range map[string]string{
		"handler.CompanyDraft":   "legacy per-company AI generation",
		"handler.AdminDraft":     "legacy batch AI generation",
		"handler.TemplateUpdate": "legacy template editing",
	} {
		if strings.Contains(body, handler+")") {
			t.Fatalf("the router still wires %s (%s)", handler, why)
		}
	}
}

// L. Review-only rollout: automatic sending is impossible and the allowlist is empty.
func TestReviewOnlyRolloutHasNoAutomation(t *testing.T) {
	t.Setenv("AUTO_SEND_INTENTS", "")
	if intents := autoSendIntents(); len(intents) != 0 {
		t.Fatalf("auto-send intents must be empty in Phase 6, got %v", intents)
	}
	if automationUnderDefaultConfig().Enabled {
		t.Fatal("automatic sending must remain impossible")
	}
	// Even a value set by accident cannot switch anything on, because nothing consults the
	// allowlist to send; it is surfaced so the mistake is visible.
	t.Setenv("AUTO_SEND_INTENTS", "specific_job_application")
	if intents := autoSendIntents(); len(intents) != 1 {
		t.Fatalf("the allowlist parser must surface what is configured, got %v", intents)
	}
	if automationUnderDefaultConfig().Enabled {
		t.Fatal("a configured allowlist must not enable automatic sending")
	}
}

// L, continued. v2_review is the default rollout mode when nothing is configured.
func TestReviewIsTheDefaultRolloutMode(t *testing.T) {
	t.Setenv("EMAIL_PIPELINE_MODE", "")
	d := newDispatcher(nil)
	if d.Mode() != send.ModeReview {
		t.Fatalf("default rollout mode is %q, want %q", d.Mode(), send.ModeReview)
	}
	if d.Readiness().Ready {
		t.Fatal("the review-only mode reported the send path ready")
	}
}

// M and N. The legacy send and follow-up routes stay closed, and no automatic follow-up exists.
func TestLegacySendAndFollowupRemainClosed(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	h.ReviewFollowupSend(w, httptest.NewRequest("POST", "/followups/send", nil))
	if w.Code != 410 {
		t.Fatalf("legacy follow-up send returned %d, want 410", w.Code)
	}
	if LegacySendEnabled {
		t.Fatal("the legacy send path must stay permanently closed")
	}
	if automationUnderDefaultConfig().Enabled {
		t.Fatal("no automatic follow-up may be possible")
	}
}

// Review reasons must stay specific. Collapsing them would destroy the information Phase 7
// needs in order to decide what could ever be automated.
func TestReviewReasonsAreSpecificAndActionable(t *testing.T) {
	draft := &store.ComposedDraft{
		Status: "review_required", Revision: 1, StaleReasons: []string{"profile_changed"},
		Attempts: []composition.Attempt{{
			Number: 1,
			Validation: []composition.Finding{
				{Severity: "block", Code: "unapproved_technology", Explanation: "Names a technology outside the profile."},
				{Severity: "info", Code: "cosmetic", Explanation: "Ignore me."},
			},
			Review: &composition.Review{Complete: true, Passed: false, Findings: []composition.Finding{
				{Severity: "review", Code: "fit_uncertain", Explanation: "The claimed fit is not clearly supported."},
			}},
		}},
	}
	reasons := compositionReviewReasons(draft)
	codes := map[string]bool{}
	for _, r := range reasons {
		codes[r.Code] = true
		if r.Text == "" || r.Action == "" {
			t.Fatalf("reason %q has no text or no action", r.Code)
		}
	}
	for _, want := range []string{"stale_profile_changed", "unapproved_technology", "fit_uncertain"} {
		if !codes[want] {
			t.Fatalf("review reasons lost %q: %v", want, codes)
		}
	}
	if codes["cosmetic"] {
		t.Fatal("informational findings must not be reported as review reasons")
	}
	if len(codes) < 3 {
		t.Fatalf("review reasons were collapsed into %d categories", len(codes))
	}
}

// H. A missing semantic review is its own reason and is never treated as a pass.
func TestMissingSemanticReviewIsItsOwnReason(t *testing.T) {
	draft := &store.ComposedDraft{Status: "review_required", Revision: 1,
		Attempts: []composition.Attempt{{Number: 1, Validation: []composition.Finding{}}}}
	found := false
	for _, r := range compositionReviewReasons(draft) {
		if r.Code == "review_unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatal("a composition with no review result must report review_unavailable")
	}

	incomplete := &store.ComposedDraft{Status: "review_required", Revision: 1,
		Attempts: []composition.Attempt{{Number: 1, Validation: []composition.Finding{},
			Review: &composition.Review{Complete: false, Passed: true}}}}
	found = false
	for _, r := range compositionReviewReasons(incomplete) {
		if r.Code == "review_incomplete" {
			found = true
		}
	}
	if !found {
		t.Fatal("an incomplete review must not be reported as a pass")
	}
}

// I. Stale v2 drafts are identifiable, one reason per changed input.
func TestStaleDraftsAreIdentifiablePerInput(t *testing.T) {
	draft := &store.ComposedDraft{Status: "reviewed", Revision: 1,
		StaleReasons: []string{"profile_changed", "opportunity_changed", "recipient_changed"},
		Attempts: []composition.Attempt{{Number: 1, Validation: []composition.Finding{},
			Review: &composition.Review{Complete: true, Passed: true}}}}
	codes := map[string]bool{}
	for _, r := range compositionReviewReasons(draft) {
		codes[r.Code] = true
	}
	for _, want := range []string{"stale_profile_changed", "stale_opportunity_changed", "stale_recipient_changed"} {
		if !codes[want] {
			t.Fatalf("stale input %q is not separately identifiable: %v", want, codes)
		}
	}
}

// Legacy intent classification accepts only the three real intents plus unknown, and nothing
// infers one from legacy state.
func TestLegacyIntentVocabulary(t *testing.T) {
	for _, valid := range []string{
		string(plan.SpecificJobApplication), string(plan.SpeculativeJobEnquiry),
		string(plan.ServiceOutreach), store.LegacyIntentUnknown,
	} {
		if !store.ValidLegacyIntent(valid) {
			t.Fatalf("%q should be a storable legacy classification", valid)
		}
	}
	for _, invalid := range []string{"application", "hiring", "hiring_us", "agency", "enterprise", "", "sme"} {
		if store.ValidLegacyIntent(invalid) {
			t.Fatalf("legacy segment value %q must never be storable as an intent", invalid)
		}
	}
}

// An unclassified legacy thread cannot be treated as ready for a v2 follow-up.
func TestUnclassifiedLegacyThreadIsNotFollowupReady(t *testing.T) {
	unclassified := store.LegacyThread{SendLogID: 1}
	if unclassified.Classified() {
		t.Fatal("a thread with no classification must not be follow-up ready")
	}
	unknown := store.LegacyThread{SendLogID: 1,
		Classification: &store.LegacyClassification{Intent: store.LegacyIntentUnknown}}
	if unknown.Classified() {
		t.Fatal("an explicitly unknown thread must not be follow-up ready")
	}
	known := store.LegacyThread{SendLogID: 1,
		Classification: &store.LegacyClassification{Intent: string(plan.SpecificJobApplication)}}
	if !known.Classified() {
		t.Fatal("a classified thread should be eligible for a reviewed v2 follow-up")
	}
}

// The dashboard states the operating mode and the profile blocker rather than implying the
// pipeline is running normally.
func TestDashboardStatesRolloutAndProfileBlocker(t *testing.T) {
	v := dashboardTestView()
	v.Rollout = rolloutView{Mode: send.ModeReview, AutomationReasons: []string{"rollout mode v2_review is not v2_auto"}, ReadinessReasons: []string{"rollout mode v2_review does not permit sending"}}
	v.Problems = []string{"Activate a candidate profile before planning outreach"}
	page := renderDashboardTest(t, v)
	for _, want := range []string{"v2_review", "Automation off", "Activate a candidate profile", "Sending unavailable"} {
		if !strings.Contains(page, want) {
			t.Fatalf("dashboard missing %q", want)
		}
	}
}

// O and the preservation guarantee: the inventory reports dispositions, never deletions.
func TestLegacyInventoryNeverReportsDeletion(t *testing.T) {
	for _, disposition := range []string{
		store.DispositionPreserved, store.DispositionReadOnly, store.DispositionStaleBlocked,
		store.DispositionNeedsClassifying, store.DispositionActionableInV2, store.DispositionMigratedReference,
	} {
		if strings.Contains(disposition, "delete") || strings.Contains(disposition, "remov") {
			t.Fatalf("disposition %q implies data loss", disposition)
		}
	}
}

// Opportunity classification values used by the dashboard must match the matcher's own, or the
// counts silently read zero.
func TestDashboardClassificationNamesMatchTheMatcher(t *testing.T) {
	source, err := os.ReadFile("../store/dashboard_v2.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	for _, class := range []string{
		opportunity.ClassificationStrong, opportunity.ClassificationPossible, opportunity.ClassificationUnrelated,
	} {
		if !strings.Contains(body, "'"+class+"'") {
			t.Fatalf("the dashboard does not count %q as the matcher spells it", class)
		}
	}
}
