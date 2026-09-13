package store

import "testing"

// The two defects found on live data during activation readiness.
func TestNormalizeRejectsTheRealDefects(t *testing.T) {
	// An address whose leading `>` was JSON-escaped and then mangled onto the local part.
	cleaned, err := NormalizeContactValue("email", "u003emgmt@stickermule.com")
	if err != nil {
		t.Fatalf("the escaped address could not be recovered: %v", err)
	}
	if cleaned != "mgmt@stickermule.com" {
		t.Fatalf("cleaned = %q, want mgmt@stickermule.com", cleaned)
	}

	// A phone number stored under kind='email'.
	if _, err := NormalizeContactValue("email", "+254123456789"); err != ErrNotAnEmailAddress {
		t.Fatalf("a phone number was accepted as an email address: %v", err)
	}
}

func TestNormalizeUnescapesAndTrims(t *testing.T) {
	for raw, want := range map[string]string{
		"  Jane@Example.Test  ":       "jane@example.test",
		"&lt;jane@example.test&gt;":   "jane@example.test",
		"<jane@example.test>":         "jane@example.test",
		"mailto:jane@example.test":    "jane@example.test",
		"jane@example.test,":          "jane@example.test",
		"\\u003ejane@example.test":    "jane@example.test",
		"careers@sub.example.co.uk":   "careers@sub.example.co.uk",
		"first.last+tag@example.test": "first.last+tag@example.test",
	} {
		got, err := NormalizeContactValue("email", raw)
		if err != nil {
			t.Fatalf("NormalizeContactValue(%q) failed: %v", raw, err)
		}
		if got != want {
			t.Fatalf("NormalizeContactValue(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestNormalizeRejectsNonAddresses(t *testing.T) {
	for _, raw := range []string{
		"", "   ", "not-an-address", "@example.test", "jane@", "jane@@example.test",
		"jane@example", "jane@.example.test", "jane@example..test",
		"jane doe@example.test", "jane<@example.test", "jane;@example.test",
		"+254 712 345 678", "0712345678@", "254712345678@example.test",
	} {
		if got, err := NormalizeContactValue("email", raw); err == nil {
			t.Fatalf("NormalizeContactValue(%q) accepted %q; it is not a usable address", raw, got)
		}
	}
}

// Non-email contacts keep their original handling: this guard is about what enters the email
// path, not about rewriting every kind of contact.
func TestNormalizeLeavesOtherKindsAlone(t *testing.T) {
	got, err := NormalizeContactValue("phone", "  +254 712 345 678  ")
	if err != nil {
		t.Fatalf("a phone contact was rejected: %v", err)
	}
	if got != "+254 712 345 678" {
		t.Fatalf("phone value = %q", got)
	}
	if _, err := NormalizeContactValue("phone", "   "); err == nil {
		t.Fatal("an empty value was accepted")
	}
}

// A valid address containing a "u00" sequence mid-string must survive untouched; the
// lost-backslash repair applies only at the start of a value.
func TestNormalizeDoesNotCorruptValidAddresses(t *testing.T) {
	for _, address := range []string{
		"mau0041ce@example.test", "u0041@example.test", "jane.u003e@example.test",
	} {
		got, err := NormalizeContactValue("email", address)
		if err != nil {
			t.Fatalf("%q was rejected: %v", address, err)
		}
		if address == "mau0041ce@example.test" && got != address {
			t.Fatalf("a mid-string escape-looking sequence was rewritten: %q -> %q", address, got)
		}
	}
}
