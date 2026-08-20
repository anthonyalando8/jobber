package enrich

import "testing"

// TestExtractEmails_SkipsPlaceholders guards a real production bug: a company's contact was
// scraped as "you@example.com" — a form placeholder from the site's own HTML, not a real
// inbox — and a draft got generated to send to it.
func TestExtractEmails_SkipsPlaceholders(t *testing.T) {
	e := &Enricher{}
	html := `<form><input placeholder="you@example.com"></form>
	<p>Contact us at hello@acme.co.ke or fill the form (e.g. name@domain.com)</p>`

	got := e.extractEmails(html)

	for _, addr := range got {
		if addr == "you@example.com" || addr == "name@domain.com" {
			t.Errorf("extractEmails() included a placeholder address %q; got %v", addr, got)
		}
	}

	found := false
	for _, addr := range got {
		if addr == "hello@acme.co.ke" {
			found = true
		}
	}
	if !found {
		t.Errorf("extractEmails() dropped the real address hello@acme.co.ke; got %v", got)
	}
}

// TestEmailQuality_GenericLocalPartsAreNotTopQuality guards the second half of the same real
// bug: "you@example.com" scored as quality 3 ("named") because the old logic only checked a
// few role-based keywords and defaulted everything else to "must be a real name" — so it
// outranked and got picked over the company's real info@ address.
func TestEmailQuality_GenericLocalPartsAreNotTopQuality(t *testing.T) {
	e := &Enricher{}
	generic := []string{"you@example.com", "your@acme.com", "test@acme.com", "admin@acme.co.ke", "support@acme.co.ke"}
	for _, addr := range generic {
		if got := e.emailQuality(addr); got == 3 {
			t.Errorf("emailQuality(%q) = 3 (named, top quality), want a lower score — this is a generic word, not a person's name", addr)
		}
	}
}

func TestEmailQuality_RealNameIsTopQuality(t *testing.T) {
	e := &Enricher{}
	if got := e.emailQuality("jane.wanjiru@acme.co.ke"); got != 3 {
		t.Errorf("emailQuality(%q) = %d, want 3 (named) for what looks like a real person's name", "jane.wanjiru@acme.co.ke", got)
	}
}

func TestExtractEmails_SkipsNoreplyAndAssetFilenames(t *testing.T) {
	e := &Enricher{}
	html := `noreply@acme.co.ke logo@2x.png hello@acme.co.ke`
	got := e.extractEmails(html)
	for _, addr := range got {
		if addr == "noreply@acme.co.ke" {
			t.Error("extractEmails() included a noreply address")
		}
	}
	if len(got) != 1 || got[0] != "hello@acme.co.ke" {
		t.Errorf("extractEmails() = %v, want only [hello@acme.co.ke]", got)
	}
}
