package opportunity

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizePreservesSourceTitleAndBoundsUTF8(t *testing.T) {
	for _, title := range []string{"Full-Stack Developer", "Full–Stack Developer", "Full—Stack Developer", "Fullstack Developer"} {
		d, err := Normalize(DiscoveredOpportunity{Source: "synthetic", Title: title, Description: strings.Repeat("café ", 20000)})
		if err != nil || d.Title != title || d.NormalizedTitle != "full stack developer" || !utf8.ValidString(d.Description) || len(d.Description) > MaxDescriptionBytes || d.DescriptionStatus != "partial" {
			t.Fatalf("normalization: %+v %v", d, err)
		}
	}
	d, err := Normalize(DiscoveredOpportunity{Source: "synthetic"})
	if err != nil || d.Title != "" || d.Identity() != "" {
		t.Fatal("invented missing data")
	}
}

func TestCanonicalIdentityPreservesMeaningfulParameters(t *testing.T) {
	got := CanonicalURL("https://Jobs.Example/view?job=42&utm_source=test#details")
	if got != "https://jobs.example/view?job=42" {
		t.Fatal(got)
	}
	if CanonicalURL("https://user:secret@jobs.example/42") != "" {
		t.Fatal("credential URL accepted")
	}
}
