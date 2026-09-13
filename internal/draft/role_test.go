package draft

import "testing"

func TestExtractRole_KnownKeywordMatches(t *testing.T) {
	got := ExtractRole("We're hiring a Backend Engineer to join our Nairobi team")
	if got != "Backend Engineer" {
		t.Errorf("got %q, want %q", got, "Backend Engineer")
	}
}

func TestExtractRole_UnrecognizedTitleUsesSuffixPattern(t *testing.T) {
	// This is the bug from a real sent draft: the fixed keyword list doesn't know
	// "Checkoff Product Scheme Manager", and the old code silently fell back to the generic
	// "Backend Developer" — which then contradicted the AI-written observation elsewhere in the
	// same email, which correctly named the real role from the same signal text.
	got := ExtractRole("Mwananchi Credit Ltd is recruiting a Checkoff Product Scheme Manager for TSC, NPS, and County Government Schemes")
	if got != "Checkoff Product Scheme Manager" {
		t.Errorf("got %q, want %q", got, "Checkoff Product Scheme Manager")
	}
}

func TestExtractRole_NoTitleShapeRemainsUnknown(t *testing.T) {
	got := ExtractRole("come build cool stuff with us, apply now")
	if got != "" {
		t.Errorf("got %q, want unknown title", got)
	}
}
