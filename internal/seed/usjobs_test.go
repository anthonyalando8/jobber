package seed

import "testing"

func TestIsRelevantJobTitle(t *testing.T) {
	relevant := []string{
		"Senior Backend Developer (Python)", "Golang Developer", "Full Stack Engineer",
		"DevOps Engineer", "Site Reliability Engineer", "Backend Engineer",
	}
	for _, title := range relevant {
		if !isRelevantJobTitle(title) {
			t.Errorf("isRelevantJobTitle(%q) = false, want true", title)
		}
	}

	// The exact real-world false positives that made tag-based filtering unreliable: none of
	// these are software roles at all, and must not match on title text either.
	irrelevant := []string{
		"General Cleaner", "Store Manager", "Front Office Executive", "Sales Advisor",
		"Content Reviewer - English US",
	}
	for _, title := range irrelevant {
		if isRelevantJobTitle(title) {
			t.Errorf("isRelevantJobTitle(%q) = true, want false", title)
		}
	}
}

func TestUsJobSignalText(t *testing.T) {
	got := usJobSignalText("Backend Developer", "Acme Co", "We need a Go developer.")
	want := "Backend Developer at Acme Co. We need a Go developer."
	if got != want {
		t.Errorf("usJobSignalText() = %q, want %q", got, want)
	}

	// No extra text — must still read as a complete sentence, not "Position at Company. ".
	got2 := usJobSignalText("Backend Developer", "Acme Co", "")
	want2 := "Backend Developer at Acme Co"
	if got2 != want2 {
		t.Errorf("usJobSignalText() with no extra = %q, want %q", got2, want2)
	}

	// Truncated to 300 chars, matching the Kenyan job-board seeder's signal length cap.
	longExtra := ""
	for i := 0; i < 400; i++ {
		longExtra += "a"
	}
	got3 := usJobSignalText("Backend Developer", "Acme Co", longExtra)
	if len(got3) != 300 {
		t.Errorf("usJobSignalText() length = %d, want 300 (truncated)", len(got3))
	}
}
