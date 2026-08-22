package seed

import "testing"

// TestCleanCompanyName_ExtractsRealNameFromHeadlineText guards a real production bug: a scraped
// "company" field was actually the whole listing headline ("<job title> at <Company>"), which got
// domain-guessed into nonsense like "procurementofficercityeyehospital.co.ke".
func TestCleanCompanyName_ExtractsRealNameFromHeadlineText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Procurement Officer at City Eye Hospital", "City Eye Hospital"},
		{"Manager at Nutrawell Africa Limited", "Nutrawell Africa Limited"},
		{"Safal Group", "Safal Group"}, // no " at " — untouched
	}
	for _, c := range cases {
		if got := cleanCompanyName(c.in); got != c.want {
			t.Errorf("cleanCompanyName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCleanCompanyName_SkipsConfidentialEmployerPlaceholders guards the other real case: some
// listings show a placeholder client name when a recruiting agency withholds the real employer's
// identity. There's no real company to find a domain for — must return "" so the caller skips it.
func TestCleanCompanyName_SkipsConfidentialEmployerPlaceholders(t *testing.T) {
	cases := []string{
		"Accounts Assistant at a Reputable Company",
		"Mechanical Engineer-Production at a Reputable Company",
		"Sales Lead at Our Client",
	}
	for _, in := range cases {
		if got := cleanCompanyName(in); got != "" {
			t.Errorf("cleanCompanyName(%q) = %q, want \"\" (confidential employer, no real company to extract)", in, got)
		}
	}
}
