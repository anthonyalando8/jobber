package web

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/opportunity"
)

func TestPhase2FollowupAndRetryInterlockBeforeStorage(t *testing.T) {
	if LegacySendEnabled {
		t.Fatal("the legacy send route must stay permanently closed")
	}
	h := &Handler{}
	for name, handler := range map[string]http.HandlerFunc{"followup_preview": h.Followups, "followup_send": h.ReviewFollowupSend, "retry": h.SentResend} {
		recorder := httptest.NewRecorder()
		handler(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
		// 410 Gone, not 503: these routes are permanently closed rather than temporarily
		// unavailable, and they refuse before touching the database.
		if recorder.Code != http.StatusGone {
			t.Fatalf("%s escaped the permanent legacy closure: %d", name, recorder.Code)
		}
	}
}

func TestPhase2ConstructorDoesNotInitializeAI(t *testing.T) {
	t.Setenv("AUTO_SEND_ENABLED", "true")
	h := NewHandler(nil, nil)
	if h.aiProvider != nil || h.sendPolicy.Enabled {
		t.Fatal("construction initialized AI or enabled automatic sends")
	}
}

func TestOpportunityInspectionEscapesUntrustedSource(t *testing.T) {
	tmpl, err := template.ParseFiles("templates/opportunity.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = tmpl.Execute(&out, map[string]any{"ID": 1, "Revision": 1, "Facts": opportunity.DiscoveredOpportunity{Title: "<script>source</script>", Description: "Synthetic first paragraph\n\nSecond paragraph", URL: "javascript:alert(1)"}, "Documents": []string{`{"title":"<script>"}`}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "<script>") || strings.Contains(out.String(), `href="javascript:`) || !strings.Contains(out.String(), "Second paragraph") {
		t.Fatal("unsafe or missing source rendering")
	}
	for _, file := range []string{"templates/profile.html", "templates/opportunities.html"} {
		if _, err = template.ParseFiles(file); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProfileActivationRequiresExplicitConfirmation(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodPost, "/profile/activate", strings.NewReader("revision_id=1&active_id=0&hash=test"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ProfileActivate(w, r)
	if w.Code != 400 {
		t.Fatal("unconfirmed profile approval reached storage")
	}
}
