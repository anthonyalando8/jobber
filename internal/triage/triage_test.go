package triage

import (
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/profile"
)

func profileDoc() *profile.Document {
	return &profile.Document{
		Identity: "Full-Stack Software Developer", PrimaryStrength: "Backend Engineering",
		DesiredRoles: []profile.JobFamily{
			{ID: "role-backend", Name: "Backend Engineering", Aliases: []string{"Backend Engineer", "Backend Developer", "Golang Engineer"}},
			{ID: "role-fullstack", Name: "Full-Stack Development", Aliases: []string{"Full Stack Developer", "Web Developer"}},
			{ID: "role-software", Name: "Software Engineering", Aliases: []string{"Software Engineer", "Software Developer"}},
		},
		Technologies: []profile.Technology{
			{ID: "tech-go", Name: "Go", Aliases: []string{"Golang"}}, {ID: "tech-postgres", Name: "PostgreSQL", Aliases: []string{"Postgres"}},
			{ID: "tech-python", Name: "Python"}, {ID: "tech-django", Name: "Django"}, {ID: "tech-react", Name: "React"},
			{ID: "tech-sql", Name: "SQL"},
		},
		Capabilities: []profile.Capability{
			{ID: "cap-api", Name: "API development", Description: "Designs and builds REST APIs", TechnologyIDs: []string{"tech-go"}, RoleFamilyIDs: []string{"role-backend"}},
			{ID: "cap-data", Name: "Database design", Description: "Relational schema design", TechnologyIDs: []string{"tech-postgres"}, RoleFamilyIDs: []string{"role-backend"}},
		},
		Engagement: &profile.Engagement{EmploymentTypes: []string{"full-time"}},
	}
}

const backendWork = `Requirements:
- Strong experience with Go
- Experience with PostgreSQL and relational schema design
- Experience building REST APIs
`

func decide(t *testing.T, title, description, status string) Decision {
	t.Helper()
	doc := profileDoc()
	match := opportunity.Assess(opportunity.AssessInput{OpportunityID: 1, OpportunityRevision: 1, ProfileRevisionID: 1, Profile: doc,
		Facts: opportunity.DiscoveredOpportunity{Title: title, Description: description, DescriptionStatus: status}})
	return Decide(Input{Title: title, Description: description, DescriptionStatus: status, Match: match, Profile: doc})
}

var filler = strings.Repeat("You will work closely with our regional teams on day-to-day operations. ", 5)

func TestTriagePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, title, description, status string
		outcome, confidence              string
	}{
		{"clearly unrelated business role", "Senior Accountant", "Prepare monthly reports and reconciliations. " + filler, "complete", Suppress, High},
		{"business role with one incidental technology", "Marketing Analyst", "Build campaign dashboards with SQL. " + filler, "complete", Suppress, High},
		{"no engineering title, complete description, no tech", "Product Designer", "Own user research and visual design. " + filler, "complete", Suppress, High},
		{"no engineering title, incomplete description", "Product Designer", "See the posting.", "partial", ConfirmedUnrelated, Medium},
		{"no engineering title but some tech", "Data Scientist", "Modelling in Python and SQL. " + filler, "complete", ConfirmedUnrelated, Medium},
		{"business role with two technologies", "Marketing Operations Lead", "Automate reports with Python and SQL. " + filler, "complete", ConfirmedUnrelated, Medium},
		{"contradictory: non-engineering title, engineering requirements", "Sales Engineer", backendWork + filler, "complete", ManualReview, Low},
	} {
		got := decide(t, tc.title, tc.description, tc.status)
		if got.Outcome != tc.outcome || got.Confidence != tc.confidence {
			t.Errorf("%s (%q): got %s/%s, want %s/%s\n%v", tc.name, tc.title, got.Outcome, got.Confidence, tc.outcome, tc.confidence, got.Reasons)
		}
		if len(got.Reasons) < 2 {
			t.Errorf("%s: a decision must explain itself: %v", tc.name, got.Reasons)
		}
	}
}

// Relevant work with a task-style title is recovered by the matcher before triage ever sees it.
func TestRelevantWorkWithoutRoleTitleNeverReachesTriage(t *testing.T) {
	for _, title := range []string{"Golang", "Build our Django REST API", "Backend platform for a payments startup"} {
		got := decide(t, title, backendWork+filler, "complete")
		if got.Outcome != ConfirmedUnrelated || got.Confidence != "none" {
			t.Errorf("%q should not be unrelated at all: %+v", title, got)
		}
	}
}

func TestTriageNeverOverrulesAPersonOrTouchesOtherClassifications(t *testing.T) {
	doc := profileDoc()
	match := opportunity.Assess(opportunity.AssessInput{OpportunityID: 1, OpportunityRevision: 1, ProfileRevisionID: 1, Profile: doc,
		Facts: opportunity.DiscoveredOpportunity{Title: "Senior Accountant", Description: filler, DescriptionStatus: "complete"}})
	got := Decide(Input{Title: "Senior Accountant", Description: filler, DescriptionStatus: "complete", Match: match, Profile: doc, ReversedByOperator: true})
	if got.Outcome != Kept {
		t.Fatalf("a person's restore was overruled: %+v", got)
	}
	strong := opportunity.MatchResult{Classification: opportunity.ClassificationStrong}
	if got := Decide(Input{Title: "Backend Engineer", Match: strong, Profile: doc}); got.Outcome == Suppress {
		t.Fatal("triage acted on a strong match")
	}
}
