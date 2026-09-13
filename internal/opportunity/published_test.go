package opportunity

import "testing"

// A published application address is a real route and should be captured. An incidental address
// in the same text is not, and writing to it would be an unsolicited message to a mailbox that
// never invited one.
func TestPublishedApplicationEmailRequiresAnApplicationCue(t *testing.T) {
	for description, want := range map[string]string{
		"To apply, send your CV to careers@acme.test before Friday.": "careers@acme.test",
		"Interested candidates should email jobs@acme.test":          "jobs@acme.test",
		"Applications to hiring@acme.test please.":                   "hiring@acme.test",
		"Send your resume to Recruitment@Acme.Test":                  "recruitment@acme.test",
		// No cue: these must not be treated as application routes.
		"Questions about our privacy policy? privacy@acme.test":              "",
		"Our support team is at support@acme.test and answers within a day.": "",
		"Press enquiries: press@acme.test":                                   "",
		"":                                                                   "",
		"No addresses here at all.":                                          "",
	} {
		if got := PublishedApplicationEmail(description); got != want {
			t.Fatalf("PublishedApplicationEmail(%q) = %q, want %q", description, got, want)
		}
	}
}

// An address at the job board or an applicant-tracking system is the board's, never a route to
// the employer.
func TestPublishedApplicationEmailIgnoresBoardAddresses(t *testing.T) {
	for _, description := range []string{
		"To apply, email apply@remoteok.com",
		"Send your CV to jobs@weworkremotely.com",
		"Apply via careers@acme.greenhouse.io",
		"Apply at noreply@example.com",
	} {
		if got := PublishedApplicationEmail(description); got != "" {
			t.Fatalf("PublishedApplicationEmail(%q) = %q; a board address is not an employer route", description, got)
		}
	}
}

// Provenance travels with the values, so a reader can tell a published route from a guess.
func TestRecordPublishedRouteKeepsProvenance(t *testing.T) {
	metadata := map[string]string{}
	RecordPublishedRoute(metadata, "remoteok:feed", "https://acme.test", "https://acme.test/apply", "", "")
	if metadata[MetaEmployerURL] != "https://acme.test" || metadata[MetaApplyURL] != "https://acme.test/apply" {
		t.Fatalf("published URLs were not stored: %v", metadata)
	}
	if metadata[MetaRouteProvenance] != "remoteok:feed" {
		t.Fatalf("provenance = %q", metadata[MetaRouteProvenance])
	}
	if _, present := metadata[MetaApplyEmail]; present {
		t.Fatal("an absent application email was stored as an empty value")
	}

	// An email carries where it was found, because a description quote is weaker than a
	// structured field.
	withEmail := map[string]string{}
	RecordPublishedRoute(withEmail, "remoteok:description", "", "", "careers@acme.test", "description")
	if withEmail[MetaApplyEmail] != "careers@acme.test" || withEmail[MetaApplyEmailSource] != "description" {
		t.Fatalf("application email provenance missing: %v", withEmail)
	}

	// Nothing published means nothing recorded, including no provenance for values that do not
	// exist.
	empty := map[string]string{}
	RecordPublishedRoute(empty, "remoteok:feed", "", "", "", "")
	if len(empty) != 0 {
		t.Fatalf("empty route recorded %v", empty)
	}
	// A nil map must not panic.
	RecordPublishedRoute(nil, "x", "y", "z", "", "")
}
