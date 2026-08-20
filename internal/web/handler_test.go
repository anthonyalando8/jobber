package web

import "testing"

// TestBuildEmailMessage_FromAndReplyTo guards against the exact bug fixed in an earlier pass:
// sendSMTP used to send with the raw SMTP account as the visible From and no Reply-To header
// at all, even when FROM_NAME/FROM_EMAIL/REPLY_TO were configured.
func TestBuildEmailMessage_FromAndReplyTo(t *testing.T) {
	msg := buildEmailMessage("Anthony Alando", "anthony@example.com", "reply@example.com", "smtpuser@gmail.com", "prospect@acme.co.ke", "Hello", "body text")

	if !containsLine(msg, `From: "Anthony Alando" <anthony@example.com>`) {
		t.Errorf("buildEmailMessage() missing the formatted From header with display name; got:\n%s", msg)
	}
	if !containsLine(msg, "Reply-To: reply@example.com") {
		t.Errorf("buildEmailMessage() missing the Reply-To header; got:\n%s", msg)
	}
	if containsLine(msg, "From: smtpuser@gmail.com") {
		t.Errorf("buildEmailMessage() used the raw SMTP account as the visible From instead of the configured display name; got:\n%s", msg)
	}
}

func TestBuildEmailMessage_FallsBackToSMTPUserWhenUnconfigured(t *testing.T) {
	msg := buildEmailMessage("", "", "", "smtpuser@gmail.com", "prospect@acme.co.ke", "Hello", "body text")

	if !containsLine(msg, "From: smtpuser@gmail.com") {
		t.Errorf("buildEmailMessage() with no FROM_NAME/FROM_EMAIL configured should fall back to the SMTP user; got:\n%s", msg)
	}
	if containsLine(msg, "Reply-To:") {
		t.Errorf("buildEmailMessage() with no REPLY_TO configured should omit the header entirely; got:\n%s", msg)
	}
}

func TestBuildEmailMessage_FromEmailWithoutNameIsUnquoted(t *testing.T) {
	msg := buildEmailMessage("", "anthony@example.com", "", "smtpuser@gmail.com", "prospect@acme.co.ke", "Hello", "body")
	if !containsLine(msg, "From: anthony@example.com") {
		t.Errorf("buildEmailMessage() with FROM_EMAIL but no FROM_NAME should use the plain address; got:\n%s", msg)
	}
}

func containsLine(s, line string) bool {
	for _, l := range splitLines(s) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, r := range s {
		if r == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}
