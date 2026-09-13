package profile

import "testing"

func TestProfileReferencesAndUnapprovedFacts(t *testing.T) {
	d := Document{Identity: "Full-Stack Software Developer", PrimaryStrength: "Backend Engineering", SecondaryPosition: "Full-Stack Development"}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.Capabilities = []Capability{{ID: "synthetic-cap", Name: "Synthetic", ProjectIDs: []string{"missing"}}}
	if err := d.Validate(); err == nil {
		t.Fatal("dangling reference")
	}
	d.Projects = []Project{{ID: "missing", Name: "Synthetic project"}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.Technologies = []Technology{{ID: "synthetic-cap", Name: "Duplicate"}}
	if err := d.Validate(); err == nil {
		t.Fatal("duplicate item ID")
	}
	if _, err := Decode([]byte(`{"identity":"Full-Stack Software Developer","unexpected":true}`)); err == nil {
		t.Fatal("unknown schema accepted")
	}
}
