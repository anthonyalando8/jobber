package draft

import (
	"testing"

	"github.com/anthonyalando8/prospector/internal/store"
)

func testSignals() []*store.Signal {
	return []*store.Signal{
		{ID: 1, Kind: "hiring_backend", Detail: "hiring a Django developer, team of 12"},
		{ID: 2, Kind: "checkout_present", Detail: "checkout and M-Pesa paybill on site"},
	}
}

func TestRejectionReason_Insufficient(t *testing.T) {
	obs := &ObserveResult{Insufficient: true}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals())
	if ok {
		t.Error("rejectionReason() ok=true for an INSUFFICIENT response, want ok=false (must reject)")
	}
}

func TestRejectionReason_EmptySignalIDs(t *testing.T) {
	obs := &ObserveResult{Observation: "hiring a backend developer", SignalIDs: nil}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals())
	if ok {
		t.Error("rejectionReason() ok=true with no signal_ids cited, want ok=false — an ungrounded draft must never pass")
	}
}

func TestRejectionReason_CitesNonexistentSignal(t *testing.T) {
	obs := &ObserveResult{Observation: "hiring a backend developer", SignalIDs: []int64{999}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true, 2: true}, testSignals())
	if ok {
		t.Error("rejectionReason() ok=true citing a signal id that doesn't exist on this company, want ok=false")
	}
}

func TestRejectionReason_ObservationTooLong(t *testing.T) {
	longObs := ""
	for i := 0; i < 30; i++ {
		longObs += "word "
	}
	obs := &ObserveResult{Observation: longObs, Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals())
	if ok {
		t.Error("rejectionReason() ok=true for a 30-word observation, want ok=false (limit is 25 words)")
	}
}

func TestRejectionReason_HypothesisTooLong(t *testing.T) {
	longHyp := ""
	for i := 0; i < 25; i++ {
		longHyp += "word "
	}
	obs := &ObserveResult{Observation: "short observation", Hypothesis: longHyp, SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals())
	if ok {
		t.Error("rejectionReason() ok=true for a 25-word hypothesis, want ok=false (limit is 20 words)")
	}
}

func TestRejectionReason_InventedNumberIsRejected(t *testing.T) {
	// The signals mention "12" (team of 12) — a number that doesn't appear anywhere in the
	// signals at all must be caught as a fabrication.
	obs := &ObserveResult{Observation: "the company has 500 employees", Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals())
	if ok {
		t.Error("rejectionReason() ok=true for an observation stating a number (500) not present in any signal, want ok=false")
	}
}

func TestRejectionReason_NumberPresentInSignalsIsAccepted(t *testing.T) {
	obs := &ObserveResult{Observation: "hiring a team of 12 backend developers", Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals())
	if !ok {
		t.Error("rejectionReason() ok=false for an observation citing a number (12) that's verbatim in the cited signal, want ok=true")
	}
}

func TestRejectionReason_CleanObservationPasses(t *testing.T) {
	obs := &ObserveResult{Observation: "hiring a Django backend developer", Hypothesis: "likely needs backend help", SignalIDs: []int64{1}}
	reason, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals())
	if !ok {
		t.Errorf("rejectionReason() ok=false for a clean, grounded observation, want ok=true (reason: %q)", reason)
	}
}

func TestValidateTemplate(t *testing.T) {
	if err := ValidateTemplate("Hi Jane, following up."); err != nil {
		t.Errorf("ValidateTemplate() on clean text = %v, want nil", err)
	}
	if err := ValidateTemplate("Hi [First Name],"); err == nil {
		t.Error("ValidateTemplate() on text with a [bracket] placeholder = nil, want an error")
	}
	if err := ValidateTemplate("Hi {{first_name}},"); err == nil {
		t.Error("ValidateTemplate() on text with an unresolved {{template}} var = nil, want an error")
	}
}

func TestFillVars(t *testing.T) {
	got := fillVars("Hi {{first_name}}, re {{company}}.", map[string]string{"first_name": "Jane", "company": "Acme"})
	want := "Hi Jane, re Acme."
	if got != want {
		t.Errorf("fillVars() = %q, want %q", got, want)
	}
}

// TestRelevantSignalsForSegment_DropsTechStackNoiseWhenHiring guards the real production bug
// behind a nonsensical draft: a company hiring a Backend Developer got pitched a hypothesis
// about "managing transactions across a split WordPress and React setup" — generic tech-stack
// signals detected off their marketing site, cited as if they were evidence of a gap, even
// though the company had just demonstrated real engineering awareness by posting the job ad.
func TestRelevantSignalsForSegment_DropsTechStackNoiseWhenHiring(t *testing.T) {
	signals := []*store.Signal{
		{ID: 1, Kind: "hiring_backend", Detail: "hiring a Backend Developer"},
		{ID: 2, Kind: "tech:react", Detail: "Found react in site code"},
		{ID: 3, Kind: "tech:wordpress", Detail: "Found wordpress in site code"},
		{ID: 4, Kind: "checkout_present", Detail: "E-commerce or payment functionality detected"},
		{ID: 5, Kind: "paybill_listed", Detail: "Accepts M-Pesa payments"},
	}

	got := relevantSignalsForSegment("hiring", signals)

	var kinds []string
	for _, s := range got {
		kinds = append(kinds, s.Kind)
	}
	for _, kind := range []string{"tech:react", "tech:wordpress", "checkout_present"} {
		for _, k := range kinds {
			if k == kind {
				t.Errorf("relevantSignalsForSegment(hiring) kept generic signal %q, want it dropped once a hiring_backend signal exists — kinds: %v", kind, kinds)
			}
		}
	}
	foundHiring, foundPaybill := false, false
	for _, k := range kinds {
		if k == "hiring_backend" {
			foundHiring = true
		}
		if k == "paybill_listed" {
			foundPaybill = true
		}
	}
	if !foundHiring {
		t.Error("relevantSignalsForSegment(hiring) dropped the hiring_backend signal — that's the whole premise of the pitch")
	}
	if !foundPaybill {
		t.Error("relevantSignalsForSegment(hiring) dropped paybill_listed — a real business signal, not generic tech-stack noise, should be kept")
	}
}

func TestRelevantSignalsForSegment_UntouchedWithoutHiringSignal(t *testing.T) {
	signals := []*store.Signal{{ID: 1, Kind: "tech:react", Detail: "Found react in site code"}}
	got := relevantSignalsForSegment("hiring", signals)
	if len(got) != 1 {
		t.Errorf("relevantSignalsForSegment(hiring) with no hiring_backend signal = %v, want the original untouched (nothing to prefer it over)", got)
	}
}

func TestRelevantSignalsForSegment_UntouchedForOtherSegments(t *testing.T) {
	signals := []*store.Signal{
		{ID: 1, Kind: "hiring_backend", Detail: "hiring a Backend Developer"},
		{ID: 2, Kind: "tech:react", Detail: "Found react in site code"},
	}
	got := relevantSignalsForSegment("agency", signals)
	if len(got) != len(signals) {
		t.Errorf("relevantSignalsForSegment(agency) = %v, want untouched — the filter is scoped to hiring/enterprise only", got)
	}
}

// TestStripLeadingFraming guards a real production bug: a sent email read "I noticed I
// noticed your website has active e-commerce checkout functionality..." because the model's
// observation started with "I noticed" despite the template already supplying that opener.
func TestStripLeadingFraming(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"I noticed your website has active e-commerce checkout functionality", "your website has active e-commerce checkout functionality"},
		{"I noticed that your site runs WordPress", "your site runs WordPress"},
		{"I came across your portfolio built with React", "portfolio built with React"},
		{"I saw you're hiring for three roles", "you're hiring for three roles"},
		{"I found a paybill number listed on your contact page", "a paybill number listed on your contact page"},
		{"your website is running React", "your website is running React"}, // no leading phrase — unchanged
		{"I noticed", "I noticed"}, // nothing left after stripping — keep the original rather than emptying it
	}
	for _, c := range cases {
		if got := stripLeadingFraming(c.in); got != c.want {
			t.Errorf("stripLeadingFraming(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractNumbers(t *testing.T) {
	got := extractNumbers("we have 12 people and 3 offices, est. 2019")
	want := []string{"12", "3", "2019"}
	if len(got) != len(want) {
		t.Fatalf("extractNumbers() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("extractNumbers()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
