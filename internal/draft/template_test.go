package draft

import (
	"strings"
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
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Acme", "")
	if ok {
		t.Error("rejectionReason() ok=true for an INSUFFICIENT response, want ok=false (must reject)")
	}
}

func TestRejectionReason_EmptySignalIDs(t *testing.T) {
	obs := &ObserveResult{Observation: "hiring a backend developer", SignalIDs: nil}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Acme", "")
	if ok {
		t.Error("rejectionReason() ok=true with no signal_ids cited, want ok=false — an ungrounded draft must never pass")
	}
}

func TestRejectionReason_CitesNonexistentSignal(t *testing.T) {
	obs := &ObserveResult{Observation: "hiring a backend developer", SignalIDs: []int64{999}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true, 2: true}, testSignals(), "Acme", "")
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
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Acme", "")
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
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Acme", "")
	if ok {
		t.Error("rejectionReason() ok=true for a 25-word hypothesis, want ok=false (limit is 20 words)")
	}
}

func TestRejectionReason_InventedNumberIsRejected(t *testing.T) {
	// The signals mention "12" (team of 12) — a number that doesn't appear anywhere in the
	// signals at all must be caught as a fabrication.
	obs := &ObserveResult{Observation: "the company has 500 employees", Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Acme", "")
	if ok {
		t.Error("rejectionReason() ok=true for an observation stating a number (500) not present in any signal, want ok=false")
	}
}

func TestRejectionReason_NumberPresentInSignalsIsAccepted(t *testing.T) {
	obs := &ObserveResult{Observation: "hiring a team of 12 backend developers", Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Acme", "")
	if !ok {
		t.Error("rejectionReason() ok=false for an observation citing a number (12) that's verbatim in the cited signal, want ok=true")
	}
}

func TestRejectionReason_CleanObservationPasses(t *testing.T) {
	// Role is deliberately empty here — "hiring a Django backend developer" is a legitimate,
	// informative fact for a template whose framing sentence never already says "hiring" (sme/
	// agency), so it must not trip the hiring-restatement guard, which is role-gated for exactly
	// that reason.
	obs := &ObserveResult{Observation: "hiring a Django backend developer", Hypothesis: "likely needs backend help", SignalIDs: []int64{1}}
	reason, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Acme", "")
	if !ok {
		t.Errorf("rejectionReason() ok=false for a clean, grounded observation, want ok=true (reason: %q)", reason)
	}
}

func TestRejectionReason_RestatesHiringFactIsRejectedForRoleTemplate(t *testing.T) {
	// The AAR Insurance case that motivated this guard: "I saw AAR Insurance is hiring for Data
	// Transformations Lead — are recruiting for an AI & Data Transformations Lead at AAR
	// Insurance." — the observation just restates what the template's own lead-in already said.
	obs := &ObserveResult{Observation: "are recruiting for an AI & Data Transformations Lead at AAR Insurance", Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "AAR Insurance", "Data Transformations Lead")
	if ok {
		t.Error("rejectionReason() ok=true for an observation restating the hiring fact for a role-based template, want ok=false")
	}
}

func TestRejectionReason_HiringLanguageAllowedWithoutRole(t *testing.T) {
	// Same wording, but role="" (sme/agency templates never state "hiring" ahead of the
	// observation) — the phrase itself isn't the problem, restating an already-stated fact is.
	obs := &ObserveResult{Observation: "recruiting for a data role after a recent funding round", Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Acme", "")
	if !ok {
		t.Error("rejectionReason() ok=false for hiring language when role=\"\" (nothing already stated it), want ok=true")
	}
}

func TestRejectionReason_RestatesCompanyNameIsRejected(t *testing.T) {
	obs := &ObserveResult{Observation: "AAR Insurance recently posted a new job listing", Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "AAR Insurance", "")
	if ok {
		t.Error("rejectionReason() ok=true for an observation restating the company name, want ok=false")
	}
}

func TestRejectionReason_DroppedSubjectFragmentIsRejected(t *testing.T) {
	// Real example that motivated this guard: "I saw you're hiring a Software Engineer — are
	// building a fully integrated stack..." — the model dropped "they're" and left a bare
	// auxiliary verb where the framing sentence needed a subject.
	obs := &ObserveResult{Observation: "are building a fully integrated stack that combines software, manufacturing, and AI", Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Acme", "Software Engineer")
	if ok {
		t.Error("rejectionReason() ok=true for an observation starting with a bare auxiliary verb (dropped subject), want ok=false")
	}
}

func TestRejectionReason_SubjectPresentIsAccepted(t *testing.T) {
	// Same fact, subject restored — must pass.
	obs := &ObserveResult{Observation: "they're building a fully integrated stack that combines software, manufacturing, and AI", Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Acme", "Software Engineer")
	if !ok {
		t.Error("rejectionReason() ok=false for an observation with a proper subject, want ok=true")
	}
}

func TestRejectionReason_HiringLogisticsObservationIsRejected(t *testing.T) {
	// Real example that motivated this guard: "I'd like to apply for the Backend Developer role
	// at Co-operative Bank of Kenya — the recruitment drive is managed through MyJobMag rather
	// than an internal applicant tracking system." — technically grounded, but says nothing
	// about the role or company, and reads oddly in an application or a contract pitch alike.
	obs := &ObserveResult{Observation: "the recruitment drive is managed through MyJobMag rather than an internal applicant tracking system", Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Co-operative Bank of Kenya", "Backend Developer")
	if ok {
		t.Error("rejectionReason() ok=true for an observation about hiring logistics/platform, want ok=false")
	}
}

func TestRejectionReason_RoleContentObservationIsAccepted(t *testing.T) {
	// A genuine fact about the role itself must still pass.
	obs := &ObserveResult{Observation: "the listing calls for experience with distributed transaction systems", Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Co-operative Bank of Kenya", "Backend Developer")
	if !ok {
		t.Error("rejectionReason() ok=false for a genuine role-content observation, want ok=true")
	}
}

func TestTrimTrailingPeriod(t *testing.T) {
	cases := []struct{ in, want string }{
		{"an internal applicant tracking system.", "an internal applicant tracking system"},
		{"no trailing period here", "no trailing period here"},
		{"trailing period and space. ", "trailing period and space"},
	}
	for _, c := range cases {
		if got := trimTrailingPeriod(c.in); got != c.want {
			t.Errorf("trimTrailingPeriod(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRejectionReason_ShortCompanyNameNotFalsePositive(t *testing.T) {
	// "Aha" is under the 4-character trust threshold — too likely to coincidentally appear as a
	// normal English substring ("...team aha moment...") to reject on.
	obs := &ObserveResult{Observation: "posted a checkout error on their support forum", Hypothesis: "short", SignalIDs: []int64{1}}
	_, ok := rejectionReason(obs, map[int64]bool{1: true}, testSignals(), "Aha", "")
	if !ok {
		t.Error("rejectionReason() ok=false for a 3-character company name with no real restatement, want ok=true")
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

// TestRelevantSignalsForSegment_DropsOwnSiteNoiseForAgency guards a real sent draft that read
// "Your website has not been updated since 2022 and is currently running on WordPress" — sent to
// a company that itself sells web design/software development as a service. Pointing at their
// own site's tech stack or staleness is never a useful hook for that audience; it reads as
// irrelevant at best, condescending at worst.
func TestRelevantSignalsForSegment_DropsOwnSiteNoiseForAgency(t *testing.T) {
	signals := []*store.Signal{
		{ID: 1, Kind: "tech:wordpress", Detail: "Found wordpress in site code"},
		{ID: 2, Kind: "stale_site", Detail: "Outdated site (2021-2022)"},
		{ID: 3, Kind: "checkout_present", Detail: "E-commerce or payment functionality detected"},
		{ID: 4, Kind: "offers_tech_services", Detail: "Site markets web design as a service"},
		{ID: 5, Kind: "paybill_listed", Detail: "Accepts M-Pesa payments"},
	}

	got := relevantSignalsForSegment("agency", signals)

	var kinds []string
	for _, s := range got {
		kinds = append(kinds, s.Kind)
	}
	for _, dropped := range []string{"tech:wordpress", "stale_site", "checkout_present"} {
		for _, k := range kinds {
			if k == dropped {
				t.Errorf("relevantSignalsForSegment(agency) kept %q, want it dropped unconditionally — kinds: %v", dropped, kinds)
			}
		}
	}
	foundOffered, foundPaybill := false, false
	for _, k := range kinds {
		if k == "offers_tech_services" {
			foundOffered = true
		}
		if k == "paybill_listed" {
			foundPaybill = true
		}
	}
	if !foundOffered {
		t.Error("relevantSignalsForSegment(agency) dropped offers_tech_services — real grounding, not own-site noise")
	}
	if !foundPaybill {
		t.Error("relevantSignalsForSegment(agency) dropped paybill_listed — a real business signal, should be kept")
	}
}

func TestRelevantSignalsForSegment_UntouchedForNonTechnicalSegments(t *testing.T) {
	signals := []*store.Signal{
		{ID: 1, Kind: "hiring_backend", Detail: "hiring a Backend Developer"},
		{ID: 2, Kind: "tech:react", Detail: "Found react in site code"},
	}
	for _, segment := range []string{"incubator", "sme"} {
		got := relevantSignalsForSegment(segment, signals)
		if len(got) != len(signals) {
			t.Errorf("relevantSignalsForSegment(%s) = %v, want untouched — tech-stack signals are often the only grounding available for a non-technical business", segment, got)
		}
	}
}

// TestServicePitch_PaymentSiteSignalLeadsWithPayments guards the concrete evidence a company's
// own site (a paybill number, a checkout, a payment complaint) is the right cue to lead with the
// M-Pesa/payments pitch.
func TestServicePitch_PaymentSiteSignalLeadsWithPayments(t *testing.T) {
	signals := []*store.Signal{{ID: 1, Kind: "paybill_listed", Detail: "Accepts M-Pesa payments"}}
	if got := servicePitch(signals, ""); got != servicePitchPayments {
		t.Errorf("servicePitch() = %q, want the payments pitch", got)
	}
}

// TestServicePitch_PaymentRoleTextLeadsWithPayments guards a real example: a job ad for
// "Checkoff Product Scheme Manager" (a payroll-deduction role) has nothing to do with the
// company's own site — the site had no paybill/checkout signal at all — but is a far stronger,
// more specific cue for the payments pitch than any generic site signal.
func TestServicePitch_PaymentRoleTextLeadsWithPayments(t *testing.T) {
	signals := []*store.Signal{
		{ID: 1, Kind: "hiring_backend", Detail: "recruiting a Checkoff Product Scheme Manager for TSC, NPS, and County Government Schemes"},
	}
	if got := servicePitch(signals, ""); got != servicePitchPayments {
		t.Errorf("servicePitch() = %q, want the payments pitch for a payroll/checkoff role", got)
	}
}

// TestServicePitch_BackendGapSignalLeadsWithBackend guards the other real complaint: a generic
// "Backend Developer" hiring ad with no payments angle at all was getting the identical
// M-Pesa-first pitch as a genuinely payments-relevant company.
func TestServicePitch_BackendGapSignalLeadsWithBackend(t *testing.T) {
	cases := [][]*store.Signal{
		{{ID: 1, Kind: "offers_tech_services", Detail: "Site markets web design as a service"}},
		{{ID: 1, Kind: "tech:react", Detail: "Found react in site code"}},
	}
	for _, signals := range cases {
		if got := servicePitch(signals, ""); got != servicePitchBackend {
			t.Errorf("servicePitch(%v) = %q, want the backend pitch", signals, got)
		}
	}
}

// TestServicePitch_NoStrongSignalFallsBackToGeneral guards the neutral default for a generic
// hiring ad with no payments angle and no frontend/tech-service signal either.
func TestServicePitch_NoStrongSignalFallsBackToGeneral(t *testing.T) {
	signals := []*store.Signal{{ID: 1, Kind: "hiring_backend", Detail: "hiring a Backend Developer"}}
	if got := servicePitch(signals, ""); got != servicePitchGeneral {
		t.Errorf("servicePitch() = %q, want the general fallback pitch", got)
	}
}

// TestServicePitch_USMarketDropsMPesaBranding guards the CAN-SPAM/US-market fix: an American
// remote employer has no reason to care about M-Pesa specifically — market == "us" must swap in
// the generic payment-integration wording instead, for both the payments and fallback cases.
func TestServicePitch_USMarketDropsMPesaBranding(t *testing.T) {
	paymentSignals := []*store.Signal{{ID: 1, Kind: "paybill_listed", Detail: "Accepts M-Pesa payments"}}
	if got := servicePitch(paymentSignals, "us"); got != servicePitchPaymentsUS {
		t.Errorf("servicePitch(us) = %q, want the US payments pitch", got)
	}
	if strings.Contains(servicePitch(paymentSignals, "us"), "M-Pesa") {
		t.Error("servicePitch(us) mentions M-Pesa — meaningless to a US/international employer")
	}

	genericSignals := []*store.Signal{{ID: 1, Kind: "hiring_backend", Detail: "hiring a Backend Developer"}}
	if got := servicePitch(genericSignals, "us"); got != servicePitchGeneralUS {
		t.Errorf("servicePitch(us) = %q, want the US general fallback pitch", got)
	}
	if strings.Contains(servicePitch(genericSignals, "us"), "M-Pesa") {
		t.Error("servicePitch(us) fallback mentions M-Pesa — meaningless to a US/international employer")
	}
}

// TestServicePitch_PaymentSignalTakesPriorityOverBackendGap guards the priority order when both
// kinds of signal are present — payments evidence is more specific and should win.
func TestServicePitch_PaymentSignalTakesPriorityOverBackendGap(t *testing.T) {
	signals := []*store.Signal{
		{ID: 1, Kind: "tech:react", Detail: "Found react in site code"},
		{ID: 2, Kind: "paybill_listed", Detail: "Accepts M-Pesa payments"},
	}
	if got := servicePitch(signals, ""); got != servicePitchPayments {
		t.Errorf("servicePitch() = %q, want payments to take priority over a backend-gap signal", got)
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

// TestNormalizeDashes guards the "lots of dashes" complaint: the model kept reaching for an em
// dash despite the prompt asking it not to — normalized to the comma a person would actually type.
func TestNormalizeDashes(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"getting payments working — the kind where nobody's double-charged", "getting payments working, the kind where nobody's double-charged"},
		{"no em dash here at all", "no em dash here at all"},
		{"a—b", "a,b"}, // no surrounding spaces — still caught
	}
	for _, c := range cases {
		if got := normalizeDashes(c.in); got != c.want {
			t.Errorf("normalizeDashes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeSentence(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// The bug from a real sent draft: a lowercase, period-less hypothesis ran straight into
		// the template's next sentence with no separator ("...bottlenecks Two questions...").
		{"integrating and automating backend payment deductions with government payroll systems is causing operational bottlenecks",
			"Integrating and automating backend payment deductions with government payroll systems is causing operational bottlenecks."},
		{"this could be slowing down your team", "This could be slowing down your team."},
		{"already ends with a period.", "Already ends with a period."},
		{"already a question?", "Already a question?"},
		{"already excited!", "Already excited!"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeSentence(c.in); got != c.want {
			t.Errorf("normalizeSentence(%q) = %q, want %q", c.in, got, c.want)
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
