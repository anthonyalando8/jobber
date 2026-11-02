package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/store"
)

// A waiting address shows what the check found, the AI's opinion labelled as an opinion, and the
// accept and reject buttons. A decided address shows no buttons.
func TestContactPageShowsAddressCheckAndDecisions(t *testing.T) {
	tmpl, err := template.ParseFiles("templates/contact_discovery.html")
	if err != nil {
		t.Fatal(err)
	}
	check := &store.AddressCheck{Verdict: store.AddressDoubtful, AIVerdict: "plausible", AIModel: "fake-model",
		AIReasons: []string{"The format matches careers@acmepay.com."}, CheckedBy: "address-check:address-v1", CheckedAt: time.Now(),
		Checks: []store.AddressCheckItem{{Code: "mail_servers", Result: "pass", Text: "acmepay.com publishes mail servers."},
			{Code: "provenance", Result: "warn", Text: "Nobody published this address."}}}
	waiting := contactCandidateView{ContactCandidateRow: store.ContactCandidateRow{ID: 7, Email: "jane@acmepay.com",
		Status: "review_required", EmailProvenance: "inferred_pattern"}, Headline: "Jane", Check: check}
	decided := contactCandidateView{ContactCandidateRow: store.ContactCandidateRow{ID: 8, Email: "john@acmepay.com",
		Status: "promoted"}, Headline: "John"}
	view := &contactDiscoveryView{Company: &store.Company{ID: 3, Name: "AcmePay"},
		Candidates: []contactCandidateView{waiting, decided}, Notice: addressNoticeText("accepted", "")}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, map[string]any{"View": view, "CompanyID": int64(3), "BackURL": "/companies/3"}); err != nil {
		t.Fatal(err)
	}
	page := b.String()
	for _, want := range []string{"Doubtful", "AI: plausible", "acmepay.com publishes mail servers.", "Nobody published this address.",
		"The format matches careers@acmepay.com.", "Accept address", "Ask AI", "Reject", "/companies/3/candidates/7",
		"cannot prove the mailbox exists", "Address accepted."} {
		if !strings.Contains(page, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	if strings.Contains(page, "/companies/3/candidates/8") {
		t.Fatal("a decided address still offers accept and reject")
	}
	if addressNoticeText("made-up", "") != "" {
		t.Fatal("an unknown notice was rendered")
	}
}
