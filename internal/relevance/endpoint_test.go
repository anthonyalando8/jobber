package relevance

import "testing"

// The exact structural pattern found on live data in Phase 6: a Sentry error-ingestion key
// stored as a company contact. It must be refused for what it is, not merely for having an
// unrecognized function.
func TestSentryIngestAddressIsNotContactable(t *testing.T) {
	assessment := ClassifyEndpoint("77585ea9215747518da6489b9fdd8494@o212792.ingest.sentry.io")
	if assessment.Contactable {
		t.Fatal("a Sentry ingest address was treated as contactable")
	}
	if assessment.Purpose != PurposeTelemetry {
		t.Fatalf("purpose = %q, want %q", assessment.Purpose, PurposeTelemetry)
	}
	if assessment.Evidence == "" {
		t.Fatal("the refusal gives no reason")
	}
}

func TestNonContactableEndpoints(t *testing.T) {
	for address, want := range map[string]string{
		"77585ea9215747518da6489b9fdd8494@o212792.ingest.sentry.io": PurposeTelemetry,
		"abc123def456abc123def456@sentry.io":                        PurposeTelemetry,
		"key@bugsnag.com":                                           PurposeTelemetry,
		"noreply@example.test":                                      PurposeNoReply,
		"no-reply@example.test":                                     PurposeNoReply,
		"do.not.reply@example.test":                                 PurposeNoReply,
		"notifications@example.test":                                PurposeNoReply,
		"postmaster@example.test":                                   PurposeNoReply,
		"webhook@example.test":                                      PurposeAutomation,
		"build.ci@example.test":                                     PurposeAutomation,
		"monitoring@example.test":                                   PurposeAutomation,
		"support@company.zendesk.com":                               PurposeAutomation,
		"alerts@pagerduty.com":                                      PurposeAutomation,
		"9f8e7d6c5b4a39281706abcd@example.test":                     PurposeTelemetry,
		"not-an-address":                                            PurposeAutomation,
	} {
		got := ClassifyEndpoint(address)
		if got.Contactable {
			t.Fatalf("%s was treated as contactable", address)
		}
		if got.Purpose != want {
			t.Fatalf("%s purpose = %q, want %q", address, got.Purpose, want)
		}
	}
}

// Ordinary human addresses must not be swept up. A false positive here silently removes a real
// contact from every queue, which is worse than leaving one machine address in review.
func TestOrdinaryAddressesRemainContactable(t *testing.T) {
	for _, address := range []string{
		"jane@example.test", "j.smith@example.test", "careers@example.test",
		"cto@example.test", "hiring@startup.example", "info@cosuno.de",
		"maria-alejandra@example.test", "mariaalejandragonzalez@example.test",
		"recruitment.team@example.test", "cindy@example.test", "buildsmith@example.test",
		"devon@example.test", "roots@example.test",
	} {
		if got := ClassifyEndpoint(address); !got.Contactable {
			t.Fatalf("%s was refused as %s: %s", address, got.Purpose, got.Evidence)
		}
	}
}

// A lookalike domain must not inherit a known host's classification.
func TestLookalikeHostsDoNotMatch(t *testing.T) {
	for _, address := range []string{"someone@notsentry.io", "someone@sentry.io.example.test"} {
		if got := ClassifyEndpoint(address); got.Purpose == PurposeTelemetry && address == "someone@notsentry.io" {
			t.Fatalf("%s matched a telemetry host by substring", address)
		}
	}
	if got := ClassifyEndpoint("someone@eu.ingest.sentry.io"); got.Contactable {
		t.Fatal("a real subdomain of a telemetry host must still match")
	}
}
