package store

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/anthonyalando8/prospector/internal/profile"
)

// ProfileComparison presents a stored profile revision exactly as written, next to the last
// approved revision. It derives nothing and adds nothing: every value shown is read straight
// from the stored document, because the whole point of the review is to see what is actually
// there rather than a rendering of what it might mean.
type ProfileComparison struct {
	Latest   *ProfileRevision
	Approved *ProfileRevision
	// Differences are section-by-section changes between the two, stated as additions and
	// removals of identified items. Nothing is summarized away.
	Differences []ProfileDifference
}

// ProfileDifference is one section's change between two revisions.
type ProfileDifference struct {
	Section string
	Added   []string
	Removed []string
	Changed []string
}

func (d ProfileDifference) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// CompareProfileRevisions returns the newest revision and the most recent approved one, with the
// differences between them. When no approved revision exists the latest is reported alone.
func (db *DB) CompareProfileRevisions(ctx context.Context) (*ProfileComparison, error) {
	revisions, err := db.ListProfileRevisions(ctx)
	if err != nil {
		return nil, err
	}
	if len(revisions) == 0 {
		return nil, fmt.Errorf("no candidate profile revision is stored")
	}
	// ListProfileRevisions returns newest first.
	comparison := &ProfileComparison{Latest: &revisions[0]}
	for i := range revisions {
		if revisions[i].ID == comparison.Latest.ID {
			continue
		}
		if revisions[i].Status == "approved" {
			comparison.Approved = &revisions[i]
			break
		}
	}
	if comparison.Approved != nil {
		comparison.Differences = diffDocuments(comparison.Approved.Document, comparison.Latest.Document)
	}
	return comparison, nil
}

// diffDocuments compares two stored documents section by section. Items are matched by their
// stable ids where they have one, so a renamed capability reads as a change rather than as an
// unrelated removal and addition.
func diffDocuments(before, after profile.Document) []ProfileDifference {
	diffs := []ProfileDifference{}

	diffs = append(diffs, diffScalars(before, after))

	diffs = append(diffs, diffIdentified("Desired roles",
		jobFamilyMap(before.DesiredRoles), jobFamilyMap(after.DesiredRoles)))
	diffs = append(diffs, diffIdentified("Technologies",
		technologyMap(before.Technologies), technologyMap(after.Technologies)))
	diffs = append(diffs, diffIdentified("Capabilities",
		capabilityMap(before.Capabilities), capabilityMap(after.Capabilities)))
	diffs = append(diffs, diffIdentified("Projects",
		projectMap(before.Projects), projectMap(after.Projects)))
	diffs = append(diffs, diffIdentified("Domains",
		factMap(before.Domains), factMap(after.Domains)))
	diffs = append(diffs, diffIdentified("Specializations",
		factMap(before.Specializations), factMap(after.Specializations)))
	diffs = append(diffs, diffIdentified("Resources",
		resourceMap(before.Resources), resourceMap(after.Resources)))
	diffs = append(diffs, diffEngagement(before.Engagement, after.Engagement))
	diffs = append(diffs, diffSignature(before.Signature, after.Signature))

	out := []ProfileDifference{}
	for _, d := range diffs {
		if !d.Empty() {
			out = append(out, d)
		}
	}
	return out
}

func diffScalars(before, after profile.Document) ProfileDifference {
	d := ProfileDifference{Section: "Identity and positioning"}
	for _, field := range []struct{ name, was, now string }{
		{"identity", before.Identity, after.Identity},
		{"primary strength", before.PrimaryStrength, after.PrimaryStrength},
		{"secondary position", before.SecondaryPosition, after.SecondaryPosition},
	} {
		if field.was != field.now {
			d.Changed = append(d.Changed, fmt.Sprintf("%s: %q -> %q", field.name, field.was, field.now))
		}
	}
	return d
}

func diffIdentified(section string, before, after map[string]string) ProfileDifference {
	d := ProfileDifference{Section: section}
	for id, text := range after {
		was, existed := before[id]
		switch {
		case !existed:
			d.Added = append(d.Added, text)
		case was != text:
			d.Changed = append(d.Changed, fmt.Sprintf("%s: %q -> %q", id, was, text))
		}
	}
	for id, text := range before {
		if _, still := after[id]; !still {
			d.Removed = append(d.Removed, text)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Changed)
	return d
}

func diffEngagement(before, after *profile.Engagement) ProfileDifference {
	d := ProfileDifference{Section: "Engagement preferences"}
	describe := func(e *profile.Engagement) string {
		if e == nil {
			return "(none stated)"
		}
		parts := []string{}
		if len(e.EmploymentTypes) > 0 {
			parts = append(parts, "employment: "+strings.Join(e.EmploymentTypes, ", "))
		}
		if e.ServiceWork != nil {
			parts = append(parts, fmt.Sprintf("service work: %v", *e.ServiceWork))
		}
		if len(e.Locations) > 0 {
			parts = append(parts, "locations: "+strings.Join(e.Locations, ", "))
		}
		if e.Remote != nil {
			parts = append(parts, fmt.Sprintf("remote: %v", *e.Remote))
		}
		if e.Availability != "" {
			parts = append(parts, "availability: "+e.Availability)
		}
		if e.Pricing != "" {
			parts = append(parts, "pricing: "+e.Pricing)
		}
		if len(parts) == 0 {
			return "(stated but empty)"
		}
		return strings.Join(parts, "; ")
	}
	was, now := describe(before), describe(after)
	if was != now {
		d.Changed = append(d.Changed, was+"  ->  "+now)
	}
	return d
}

func diffSignature(before, after profile.Signature) ProfileDifference {
	d := ProfileDifference{Section: "Signature"}
	describe := func(s profile.Signature) string {
		parts := []string{s.Name, s.Descriptor}
		if s.Location != "" {
			parts = append(parts, s.Location)
		}
		if s.Phone != "" {
			parts = append(parts, "phone present")
		}
		if len(s.ResourceIDs) > 0 {
			parts = append(parts, "resources: "+strings.Join(s.ResourceIDs, ", "))
		}
		return strings.Join(parts, " | ")
	}
	was, now := describe(before), describe(after)
	if was != now {
		d.Changed = append(d.Changed, was+"  ->  "+now)
	}
	return d
}

func jobFamilyMap(items []profile.JobFamily) map[string]string {
	out := map[string]string{}
	for _, i := range items {
		out[i.ID] = i.Name + aliasSuffix(i.Aliases)
	}
	return out
}

func technologyMap(items []profile.Technology) map[string]string {
	out := map[string]string{}
	for _, i := range items {
		out[i.ID] = i.Name + aliasSuffix(i.Aliases)
	}
	return out
}

func capabilityMap(items []profile.Capability) map[string]string {
	out := map[string]string{}
	for _, i := range items {
		out[i.ID] = i.Name + " — " + i.Description
	}
	return out
}

func projectMap(items []profile.Project) map[string]string {
	out := map[string]string{}
	for _, i := range items {
		out[i.ID] = fmt.Sprintf("%s — %s (%d responsibilities, %d outcomes)",
			i.Name, i.Description, len(i.Responsibilities), len(i.Outcomes))
	}
	return out
}

func factMap(items []profile.Fact) map[string]string {
	out := map[string]string{}
	for _, i := range items {
		out[i.ID] = i.Text
	}
	return out
}

func resourceMap(items []profile.Resource) map[string]string {
	out := map[string]string{}
	for _, i := range items {
		value := i.URL
		if value == "" && i.AvailableOnRequest {
			value = "available on request"
		}
		out[i.ID] = fmt.Sprintf("%s [%s] %s", i.Label, i.Kind, value)
	}
	return out
}

func aliasSuffix(aliases []string) string {
	if len(aliases) == 0 {
		return ""
	}
	return " (aliases: " + strings.Join(aliases, ", ") + ")"
}
