package verify

import (
	"strings"
	"testing"
)

func TestCandidateDomains_PrefersCoKe(t *testing.T) {
	cands := CandidateDomains("Equity Bank Kenya")
	if len(cands) == 0 {
		t.Fatal("CandidateDomains() = empty for a normal company name")
	}
	if !strings.HasSuffix(cands[0], ".co.ke") {
		t.Errorf("CandidateDomains()[0] = %q, want the first candidate to try .co.ke — the dominant Kenyan TLD, not .com", cands[0])
	}
}

func TestCandidateDomains_StripsParentheticalAsides(t *testing.T) {
	cands := CandidateDomains("CFAO Motors Kenya limited (Formerly Toyota Kenya)")
	for _, c := range cands {
		if strings.Contains(c, "formerly") {
			t.Errorf("CandidateDomains() = %v, a parenthetical aside leaked into a candidate domain: %q", cands, c)
		}
	}
	// The real fix this test guards: the old single-guess heuristic produced
	// "cfao-motors-kenya-limited-formerly-toyota-kenya.com", which never existed.
	if len(cands) == 0 || !strings.HasPrefix(cands[0], "cfaomotors") {
		t.Errorf("CandidateDomains()[0] = %v, want it to start with the cleaned-up 'cfaomotors'", cands)
	}
}

func TestCandidateDomains_DropsLegalSuffixesAndNoiseWords(t *testing.T) {
	cands := CandidateDomains("Rapidtech Networks Limited")
	for _, c := range cands {
		if strings.Contains(c, "limited") {
			t.Errorf("CandidateDomains() = %v, the legal suffix 'limited' should have been stripped before guessing", cands)
		}
	}
}

func TestCandidateDomains_NoUsableTokensReturnsNil(t *testing.T) {
	// A name that's entirely noise words/short fragments after cleaning shouldn't produce a
	// domain guess out of nothing.
	if got := CandidateDomains("The Co Ltd"); got != nil {
		t.Errorf("CandidateDomains(%q) = %v, want nil — nothing significant left after stripping noise words", "The Co Ltd", got)
	}
}

func TestCandidateDomains_NoDuplicatesWhenDashedEqualsConcatenated(t *testing.T) {
	// A single-word name produces the same string dashed or concatenated — the candidate list
	// must not contain the same domain twice.
	cands := CandidateDomains("Zetech")
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c] {
			t.Errorf("CandidateDomains(%q) = %v, contains a duplicate: %q", "Zetech", cands, c)
		}
		seen[c] = true
	}
}

func TestNameMatchConfidence_ExactMatch(t *testing.T) {
	got := nameMatchConfidence("Acme Software Kenya", "Welcome to Acme Software — we build things.")
	if got != 1.0 {
		t.Errorf("nameMatchConfidence() = %v, want 1.0 when every significant token appears on the page", got)
	}
}

func TestNameMatchConfidence_NoMatch(t *testing.T) {
	got := nameMatchConfidence("Acme Software Kenya", "This page is about something completely unrelated.")
	if got != 0 {
		t.Errorf("nameMatchConfidence() = %v, want 0 when nothing matches", got)
	}
}

func TestNameMatchConfidence_NoTokensLeftIsZeroNotNaN(t *testing.T) {
	// "ABC Ltd" reduces to nothing after stripping short/noise words — must not divide by zero.
	got := nameMatchConfidence("The Co Ltd", "any page text at all")
	if got != 0 {
		t.Errorf("nameMatchConfidence() with no significant tokens = %v, want 0", got)
	}
}
