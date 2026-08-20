package sendpolicy

import (
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/store"
)

func strp(s string) *string { return &s }

func testCompany(score int, domainStatus string) *store.Company {
	return &store.Company{ID: 1, Name: "Acme Ltd", Domain: "acme.co.ke", Score: score, DomainStatus: domainStatus}
}

var emailContact = []*store.Contact{{Kind: "email", Value: "hi@acme.co.ke"}}

func freshSignals() []*store.Signal {
	return []*store.Signal{{ID: 5, Kind: "hiring_backend", Detail: "hiring a Django developer", CreatedAt: time.Now().Add(-2 * time.Hour)}}
}

func staleSignals() []*store.Signal {
	return []*store.Signal{{ID: 5, Kind: "hiring_backend", Detail: "hiring a Django developer", CreatedAt: time.Now().Add(-200 * time.Hour)}}
}

var cleanDraft = &store.Draft{Body: "Hi Jane,\n\nI noticed you're hiring a Django backend developer.\n\nAnthony", EvidenceUsed: strp("5")}
var sloppyDraft = &store.Draft{Body: "I hope this email finds you well! I came across your innovative solutions.", EvidenceUsed: strp("5")}
var editedDraft = &store.Draft{Body: cleanDraft.Body, EvidenceUsed: strp("5"), EditedByHuman: true}
var placeholderDraft = &store.Draft{Body: "Hi [First Name], noticed {{observation}}.", EvidenceUsed: strp("5")}

func TestEvaluate(t *testing.T) {
	cfg := DefaultConfig()

	cases := []struct {
		name string
		in   Input
		want Decision
	}{
		{
			name: "score below review floor is blocked",
			in:   Input{Company: testCompany(40, "verified"), Contacts: emailContact, Signals: freshSignals(), Draft: cleanDraft},
			want: Blocked,
		},
		{
			name: "score in the review band is review_required",
			in:   Input{Company: testCompany(70, "verified"), Contacts: emailContact, Signals: freshSignals(), Draft: cleanDraft},
			want: ReviewRequired,
		},
		{
			name: "high score, clean, fresh, manual domain auto-sends",
			in:   Input{Company: testCompany(85, "manual"), Contacts: emailContact, Signals: freshSignals(), Draft: cleanDraft},
			want: AutoSend,
		},
		{
			name: "high score, clean, fresh, verified domain auto-sends",
			in:   Input{Company: testCompany(85, "verified"), Contacts: emailContact, Signals: freshSignals(), Draft: cleanDraft},
			want: AutoSend,
		},
		{
			name: "unverified domain is blocked regardless of score",
			in:   Input{Company: testCompany(85, "candidate"), Contacts: emailContact, Signals: freshSignals(), Draft: cleanDraft},
			want: Blocked,
		},
		{
			name: "no email contact is blocked",
			in:   Input{Company: testCompany(85, "verified"), Contacts: nil, Signals: freshSignals(), Draft: cleanDraft},
			want: Blocked,
		},
		{
			name: "suppressed is blocked",
			in:   Input{Company: testCompany(85, "verified"), Contacts: emailContact, Signals: freshSignals(), Draft: cleanDraft, Suppressed: true},
			want: Blocked,
		},
		{
			name: "recent contact to same domain is blocked",
			in:   Input{Company: testCompany(85, "verified"), Contacts: emailContact, Signals: freshSignals(), Draft: cleanDraft, RecentSameDomain: true},
			want: Blocked,
		},
		{
			name: "leftover placeholder is blocked",
			in:   Input{Company: testCompany(85, "verified"), Contacts: emailContact, Signals: freshSignals(), Draft: placeholderDraft},
			want: Blocked,
		},
		{
			name: "AI-tone warning downgrades a high score to review_required",
			in:   Input{Company: testCompany(85, "verified"), Contacts: emailContact, Signals: freshSignals(), Draft: sloppyDraft},
			want: ReviewRequired,
		},
		{
			name: "stale evidence downgrades a high score to review_required",
			in:   Input{Company: testCompany(85, "verified"), Contacts: emailContact, Signals: staleSignals(), Draft: cleanDraft},
			want: ReviewRequired,
		},
		{
			name: "a human-edited draft never auto-sends even at a high score",
			in:   Input{Company: testCompany(85, "verified"), Contacts: emailContact, Signals: freshSignals(), Draft: editedDraft},
			want: ReviewRequired,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Evaluate(cfg, c.in)
			if got.Decision != c.want {
				t.Errorf("Evaluate() = %s (reasons %v), want %s", got.Decision, got.Reasons, c.want)
			}
			if got.Decision != AutoSend && len(got.Reasons) == 0 {
				t.Errorf("Evaluate() = %s with no reasons — every non-AUTO_SEND decision must explain why (review.md §1)", got.Decision)
			}
		})
	}
}

func TestEvaluate_DisabledMeansReviewRequired(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false

	got := Evaluate(cfg, Input{Company: testCompany(95, "verified"), Contacts: emailContact, Signals: freshSignals(), Draft: cleanDraft})
	if got.Decision != ReviewRequired {
		t.Errorf("Evaluate() with policy disabled = %s, want %s — disabling auto-send must never silently block or send", got.Decision, ReviewRequired)
	}
}

// TestEvaluate_NeverAutoSendsOnAmbiguity is the single most important property of this
// package: when any signal is uncertain rather than cleanly wrong, the result must be
// REVIEW_REQUIRED, never AUTO_SEND. review.md §11: "if uncertain, REVIEW_REQUIRED, not
// AUTO_SEND." This sweeps every case above rather than re-asserting it per case.
func TestEvaluate_NeverAutoSendsOnAmbiguity(t *testing.T) {
	cfg := DefaultConfig()
	ambiguous := []Input{
		{Company: testCompany(85, "verified"), Contacts: emailContact, Signals: freshSignals(), Draft: sloppyDraft},
		{Company: testCompany(85, "verified"), Contacts: emailContact, Signals: staleSignals(), Draft: cleanDraft},
		{Company: testCompany(85, "verified"), Contacts: emailContact, Signals: freshSignals(), Draft: editedDraft},
		{Company: testCompany(70, "verified"), Contacts: emailContact, Signals: freshSignals(), Draft: cleanDraft},
	}
	for i, in := range ambiguous {
		if got := Evaluate(cfg, in).Decision; got == AutoSend {
			t.Errorf("case %d: Evaluate() = AUTO_SEND for an ambiguous input, want anything else", i)
		}
	}
}
