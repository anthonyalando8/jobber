package tonecheck

import "testing"

func TestWarnings_CatchesBannedPhrases(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"generic greeting", "I hope this email finds you well."},
		{"generic compliment", "I came across your impressive company."},
		{"fake enthusiasm", "I was thrilled to see what you're building."},
		{"corporate jargon", "We offer innovative solutions leveraging cutting-edge technology."},
		{"marketing buzzword", "a truly seamless, robust experience"},
		{"generic CTA", "Let's connect sometime."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Warnings(c.text); len(got) == 0 {
				t.Errorf("Warnings(%q) = empty, want at least one warning", c.text)
			}
		})
	}
}

func TestWarnings_CleanTextHasNoWarnings(t *testing.T) {
	clean := "Hi Jane, I noticed you're hiring a Django backend developer. Worth a 15-minute call?"
	if got := Warnings(clean); len(got) != 0 {
		t.Errorf("Warnings(%q) = %v, want none — plain factual text shouldn't be flagged", clean, got)
	}
}

func TestWarnings_PlaceholderText(t *testing.T) {
	if got := Warnings("Hi [First Name], following up on {{observation}}."); len(got) == 0 {
		t.Error("Warnings() on text with unresolved placeholders = empty, want a warning")
	}
}

func TestWarnings_ExcessivePunctuation(t *testing.T) {
	if got := Warnings("This is exciting! Really great! Let's talk!"); len(got) == 0 {
		t.Error("Warnings() on text with multiple '!' = empty, want excessive_punctuation")
	}
}
