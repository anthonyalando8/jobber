package store

import (
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/profile"
	"github.com/anthonyalando8/prospector/internal/relevance"
)

func boolPtr(v bool) *bool { return &v }

// The profile diff must report what actually changed, item by item, and never summarize a
// removal away — the whole purpose is to let a person see exactly what they would be approving.
func TestProfileDiffReportsEveryChange(t *testing.T) {
	before := profile.Document{
		Identity: "Full-Stack Software Developer", PrimaryStrength: "Backend Engineering",
		DesiredRoles: []profile.JobFamily{{ID: "role-backend", Name: "Backend Engineering",
			Aliases: []string{"Backend Engineer"}}},
		Technologies: []profile.Technology{{ID: "tech-go", Name: "Go"}},
		Capabilities: []profile.Capability{{ID: "cap-api", Name: "API development", Description: "Builds APIs"}},
		Signature:    profile.Signature{Name: "A. Synthetic"},
	}
	after := profile.Document{
		Identity: "Full-Stack Software Developer", PrimaryStrength: "Backend Engineering",
		SecondaryPosition: "Full-Stack Development",
		DesiredRoles: []profile.JobFamily{{ID: "role-backend", Name: "Backend Engineering",
			Aliases: []string{"Backend Engineer", "Backend Developer"}}},
		Technologies: []profile.Technology{{ID: "tech-pg", Name: "PostgreSQL"}},
		Capabilities: []profile.Capability{{ID: "cap-api", Name: "API development",
			Description: "Designs and builds REST and gRPC APIs"}},
		Engagement: &profile.Engagement{EmploymentTypes: []string{"full-time"}, ServiceWork: boolPtr(true)},
		Signature:  profile.Signature{Name: "A. Synthetic", Descriptor: "Backend"},
	}

	sections := map[string]ProfileDifference{}
	for _, d := range diffDocuments(before, after) {
		sections[d.Section] = d
	}

	if got := sections["Identity and positioning"]; len(got.Changed) != 1 ||
		!strings.Contains(got.Changed[0], "secondary position") {
		t.Fatalf("a changed positioning field was not reported: %+v", got)
	}
	// An alias added to an existing role is a change to that role, not a new role.
	if got := sections["Desired roles"]; len(got.Changed) != 1 || len(got.Added) != 0 || len(got.Removed) != 0 {
		t.Fatalf("an alias change was not reported as a change: %+v", got)
	}
	// A removed technology must be visible as a removal, never quietly dropped.
	tech := sections["Technologies"]
	if len(tech.Added) != 1 || !strings.Contains(tech.Added[0], "PostgreSQL") {
		t.Fatalf("added technology missing: %+v", tech)
	}
	if len(tech.Removed) != 1 || !strings.Contains(tech.Removed[0], "Go") {
		t.Fatalf("removed technology missing: %+v", tech)
	}
	// A reworded capability with the same id is a change, not a removal plus an addition.
	if got := sections["Capabilities"]; len(got.Changed) != 1 || len(got.Added) != 0 || len(got.Removed) != 0 {
		t.Fatalf("a reworded capability was not reported as a change: %+v", got)
	}
	if got := sections["Engagement preferences"]; len(got.Changed) != 1 ||
		!strings.Contains(got.Changed[0], "none stated") {
		t.Fatalf("newly stated engagement preferences were not reported: %+v", got)
	}
	if got := sections["Signature"]; len(got.Changed) != 1 {
		t.Fatalf("a signature change was not reported: %+v", got)
	}
}

// Identical documents produce no differences at all, so "no changes" is a real answer rather
// than a rendering artifact.
func TestIdenticalProfilesDifferInNothing(t *testing.T) {
	doc := profile.Document{
		Identity: "Full-Stack Software Developer", PrimaryStrength: "Backend Engineering",
		DesiredRoles: []profile.JobFamily{{ID: "role-backend", Name: "Backend Engineering"}},
		Signature:    profile.Signature{Name: "A. Synthetic"},
	}
	if diffs := diffDocuments(doc, doc); len(diffs) != 0 {
		t.Fatalf("identical documents reported %d differences: %+v", len(diffs), diffs)
	}
}

// The contact investigation judges by the same rules the send path uses, and never promotes a
// generic, telemetry or no-reply address into an acceptable recruiting contact.
func TestStrongMatchGapDescriptions(t *testing.T) {
	for name, tc := range map[string]struct {
		report StrongMatchContactReport
		want   string
	}{
		"no contacts at all": {
			StrongMatchContactReport{}, "no contact of any kind is stored",
		},
		"only machine endpoints": {
			StrongMatchContactReport{Contacts: []ContactCandidate{
				{Address: "key@sentry.io", Status: "active", Contactable: false},
			}}, "machine endpoint",
		},
		"only generic mailboxes": {
			StrongMatchContactReport{Contacts: []ContactCandidate{
				{Address: "info@example.test", Status: "active", Contactable: true,
					Function: relevance.FunctionGeneric},
			}}, "only generic mailboxes",
		},
		"unidentified functions": {
			StrongMatchContactReport{Contacts: []ContactCandidate{
				{Address: "jm@example.test", Status: "active", Contactable: true,
					Function: relevance.FunctionUnknown},
			}}, "no stored contact has an identified recruiting or engineering function",
		},
		"all marked bad": {
			StrongMatchContactReport{Contacts: []ContactCandidate{
				{Address: "old@example.test", Status: "bad", Contactable: true},
			}}, "marked bad or unusable",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := describeGap(tc.report); !strings.Contains(got, tc.want) {
				t.Fatalf("gap = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

// Source metadata is read verbatim: an application route counts only when the posting published
// one, never when a plausible-looking address could be constructed.
func TestApplicationRouteReadsOnlyPublishedMetadata(t *testing.T) {
	metadata := map[string]string{"apply_url": " https://jobs.example.test/apply ", "other": "ignored"}
	if got := firstNonEmpty(metadata, "apply_url", "application_url"); got != "https://jobs.example.test/apply" {
		t.Fatalf("apply url = %q", got)
	}
	if got := firstNonEmpty(map[string]string{}, "apply_email"); got != "" {
		t.Fatalf("an absent application email produced %q; it must produce nothing", got)
	}
	if got := firstNonEmpty(map[string]string{"apply_email": "   "}, "apply_email"); got != "" {
		t.Fatal("a whitespace-only value was treated as a published route")
	}
}
