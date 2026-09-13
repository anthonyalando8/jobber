package relevance

import (
	"strings"
)

// Endpoint purposes. These answer a different question from Function: not "who does this reach"
// but "is this address meant to reach a person at all". A telemetry endpoint can look like a
// perfectly valid mailbox and still be somewhere no human will ever read.
const (
	// PurposeHuman means nothing marks this address as machine-only. It is not a positive
	// finding: an ordinary-looking address whose function is unknown is still unknown.
	PurposeHuman = "human"
	// PurposeTelemetry is an error/metrics/log ingestion endpoint scraped from a page.
	PurposeTelemetry = "telemetry"
	// PurposeNoReply explicitly refuses replies, so a conversation cannot happen.
	PurposeNoReply = "no_reply"
	// PurposeAutomation is a machine integration mailbox: ticketing, CI, alerting, webhooks.
	PurposeAutomation = "automation"
	// PurposeRoleAccount is a shared functional mailbox handled by the generic rules.
	PurposeRoleAccount = "role_account"
)

// EndpointAssessment says whether an address is a place a person could be written to.
type EndpointAssessment struct {
	Purpose string `json:"purpose"`
	// Contactable is false whenever the address is not intended for human correspondence.
	Contactable bool   `json:"contactable"`
	Evidence    string `json:"evidence,omitempty"`
}

// telemetryHosts are host suffixes whose whole purpose is machine ingestion. Matching is on a
// domain-label boundary so "notsentry.io" does not match "sentry.io".
var telemetryHosts = []string{
	"ingest.sentry.io", "ingest.de.sentry.io", "ingest.us.sentry.io", "sentry.io",
	"bugsnag.com", "rollbar.com", "airbrake.io", "raygun.io", "honeybadger.io",
	"loggly.com", "papertrailapp.com", "datadoghq.com", "newrelic.com",
	"sentry.dev", "glitchtip.com",
}

// automationHosts are integration endpoints: a message here becomes a ticket or a webhook
// payload, not a conversation.
var automationHosts = []string{
	"zendesk.com", "freshdesk.com", "helpscout.net", "intercom-mail.com",
	"atlassian.net", "jira.com", "servicedesk.com", "statuspage.io",
	"pagerduty.com", "opsgenie.net", "hooks.slack.com", "zapier.com",
	"mailgun.org", "sendgrid.net", "amazonses.com",
}

// noReplyLocalParts refuse replies outright.
var noReplyLocalParts = []string{
	"noreply", "no-reply", "no_reply", "donotreply", "do-not-reply", "do_not_reply",
	"notifications", "notification", "alerts", "alert", "mailer-daemon", "postmaster",
	"bounce", "bounces", "automated", "autoreply", "auto-reply",
}

// automationLocalParts are machine mailboxes even on an ordinary company domain.
var automationLocalParts = []string{
	"webhook", "webhooks", "ci", "jenkins", "build", "builds", "monitoring", "monitor",
	"nagios", "zabbix", "cron", "daemon", "root", "sysadmin", "logs", "logging",
	"metrics", "telemetry", "errors", "crash", "crashes", "ticket", "tickets",
	"jira", "issues", "bugs", "bugzilla", "abuse", "dmarc", "dkim", "spf",
}

// ClassifyEndpoint reports whether an address is somewhere a person can actually be written to.
//
// This exists because Phase 6 found a Sentry error-ingestion address stored as a company contact:
// a 32-hex local part at o212792.ingest.sentry.io, scraped from a page. It was correctly refused
// downstream for having no identified function, but "no identified function" is a weaker claim
// than the truth, which is that the address is not a mailbox for humans at all. Automation must
// never depend on the weaker check happening to fire.
func ClassifyEndpoint(address string) EndpointAssessment {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return EndpointAssessment{Purpose: PurposeAutomation, Contactable: false,
			Evidence: "not a usable email address"}
	}
	local, host := address[:at], address[at+1:]

	if match, ok := hostSuffix(host, telemetryHosts); ok {
		return EndpointAssessment{Purpose: PurposeTelemetry, Contactable: false,
			Evidence: "error or telemetry ingestion endpoint at " + match + ", not a human mailbox"}
	}
	if match, ok := hostSuffix(host, automationHosts); ok {
		return EndpointAssessment{Purpose: PurposeAutomation, Contactable: false,
			Evidence: "machine integration endpoint at " + match + "; a message here becomes a ticket or payload"}
	}
	for _, word := range noReplyLocalParts {
		if localSegmentHas(local, word) {
			return EndpointAssessment{Purpose: PurposeNoReply, Contactable: false,
				Evidence: "the mailbox \"" + local + "@\" does not accept replies"}
		}
	}
	for _, word := range automationLocalParts {
		if localSegmentHas(local, word) {
			return EndpointAssessment{Purpose: PurposeAutomation, Contactable: false,
				Evidence: "the mailbox \"" + local + "@\" is a machine or service account"}
		}
	}
	// A long opaque identifier is the structural shape of the Sentry key that prompted this
	// check: an address nobody typed and nobody reads. The threshold is deliberately high so an
	// ordinary name, initials or a department never trips it.
	if opaqueIdentifier(local) {
		return EndpointAssessment{Purpose: PurposeTelemetry, Contactable: false,
			Evidence: "the mailbox local part is a " + itoa(len(local)) +
				"-character opaque identifier, which is a machine key rather than a name"}
	}
	return EndpointAssessment{Purpose: PurposeHuman, Contactable: true}
}

// hostSuffix matches on a label boundary so a lookalike domain cannot impersonate a known host.
func hostSuffix(host string, suffixes []string) (string, bool) {
	for _, suffix := range suffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return suffix, true
		}
	}
	return "", false
}

// separators are the characters people use inside a mailbox local part.
var separators = func(r rune) bool { return r == '.' || r == '-' || r == '_' || r == '+' }

// localSegmentHas matches a whole dot/dash/underscore-separated segment, so "ci" matches "ci@"
// and "build.ci@" but never "cindy@".
//
// It also compares with every separator removed, because the same mailbox is written
// "noreply@", "no-reply@" and "do.not.reply@". Comparing the stripped forms catches all of them
// without loosening the segment rule that keeps "cindy@" out.
func localSegmentHas(local, word string) bool {
	for _, segment := range strings.FieldsFunc(local, separators) {
		if segment == word {
			return true
		}
	}
	if local == word {
		return true
	}
	return stripSeparators(local) == stripSeparators(word)
}

func stripSeparators(s string) string {
	return strings.Map(func(r rune) rune {
		if separators(r) {
			return -1
		}
		return r
	}, s)
}

// opaqueIdentifier reports a local part that is a long machine key: at least 24 characters, all
// hexadecimal or all base32/base64-ish, with no separator a person would use in a name.
func opaqueIdentifier(local string) bool {
	if len(local) < 24 {
		return false
	}
	if strings.ContainsAny(local, ".-_+") {
		return false
	}
	digits, letters := 0, 0
	for _, r := range local {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'a' && r <= 'z':
			letters++
		default:
			return false
		}
	}
	// A real word of this length has few digits; a key has many. Requiring some digits keeps a
	// long concatenated human name ("mariaalejandragonzalez") out of this branch.
	return digits >= 4 && letters > 0
}
