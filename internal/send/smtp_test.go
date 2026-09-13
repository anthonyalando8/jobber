package send

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// script drives one side of an SMTP conversation. Each entry is a line the fake server sends
// after reading one client command, so a test can fail at an exact stage. The special value
// hangUp closes the connection instead of replying, which is how a genuinely unknown outcome
// is produced.
const hangUp = "\x00hangup"

func scriptedServer(t *testing.T, script []string) func(network, address string, timeout time.Duration) (net.Conn, error) {
	t.Helper()
	return func(string, string, time.Duration) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			reader := bufio.NewReader(server)
			next := 0
			// reply sends the next scripted line. It reports false when the script is exhausted
			// or calls for a hangup, at which point the connection simply closes.
			reply := func() bool {
				if next >= len(script) || script[next] == hangUp {
					return false
				}
				line := script[next]
				next++
				_, err := server.Write([]byte(line + "\r\n"))
				return err == nil
			}
			if !reply() { // the greeting, which no command precedes
				return
			}
			for {
				command, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if !reply() {
					return
				}
				if !strings.HasPrefix(strings.ToUpper(command), "DATA") {
					continue
				}
				// The body follows the 354. Consume it to the terminating dot, which is the
				// commit point, and only then give the verdict the script calls for.
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimRight(line, "\r\n") == "." {
						break
					}
				}
				if !reply() {
					return
				}
			}
		}()
		return client, nil
	}
}

// The fake server speaks plain text, and net/smtp will not hand credentials to an unencrypted
// connection unless the host is loopback — so these tests dial "localhost". That restriction is
// itself asserted below, in TestSMTPRefusesPlaintextCredentialsToARemoteHost.
func testTransport(t *testing.T, script []string) *SMTPTransport {
	return &SMTPTransport{
		Host: "localhost", Port: "587", Username: "sender@example.test", Password: "secret",
		FromAddress: "sender@example.test", FromName: "Synthetic Sender", Timeout: 5 * time.Second,
		Dial: scriptedServer(t, script),
	}
}

func testMessage() Message {
	return Message{ToAddress: "recipient@example.test", ToName: "Synthetic Recipient",
		Subject: "Synthetic subject", Body: "Synthetic body.", MessageID: "<synthetic@prospector.local>"}
}

// The conversation reaches the terminating dot and the server accepts. Only here is the message
// definitely sent.
func TestSMTPAcceptedOnlyAfterTheTerminatingDot(t *testing.T) {
	tr := testTransport(t, []string{
		"220 localhost ESMTP",
		"250-smtp.example.test\r\n250 AUTH PLAIN", // EHLO
		"235 authenticated",                       // AUTH
		"250 sender ok",                           // MAIL FROM
		"250 recipient ok",                        // RCPT TO
		"354 go ahead",                            // DATA
		"250 2.0.0 queued as ABC123",              // terminating dot
		"221 bye",                                 // QUIT
	})
	r := tr.Send(context.Background(), testMessage())
	if r.Outcome != OutcomeAccepted {
		t.Fatalf("outcome = %q (%s) at stage %q: %s", r.Outcome, r.ErrorCategory, r.Stage, r.Response)
	}
	if r.Stage != StageQuit {
		t.Fatalf("stage = %q, want %q", r.Stage, StageQuit)
	}
	if r.AcceptedAt.IsZero() {
		t.Fatal("an accepted send recorded no acceptance time")
	}
	if Result(r).State() != StateAccepted {
		t.Fatalf("state = %q", Result(r).State())
	}
}

// The connection dies at the commit point. The server may already have taken the message, so
// this must be uncertain — the one outcome that must never be retried automatically.
func TestSMTPHangupAtCommitPointIsUncertain(t *testing.T) {
	tr := testTransport(t, []string{
		"220 localhost ESMTP",
		"250-smtp.example.test\r\n250 AUTH PLAIN",
		"235 authenticated",
		"250 sender ok",
		"250 recipient ok",
		"354 go ahead",
		hangUp, // no verdict after the terminating dot
	})
	r := tr.Send(context.Background(), testMessage())
	if r.Outcome != OutcomeUncertain {
		t.Fatalf("outcome = %q at stage %q; a hangup at the commit point must be uncertain", r.Outcome, r.Stage)
	}
	if r.Stage != StageCommit {
		t.Fatalf("stage = %q, want %q", r.Stage, StageCommit)
	}
	if r.ErrorCategory != ErrorTimeoutUncertain {
		t.Fatalf("category = %q", r.ErrorCategory)
	}
	if Retryable(r.ErrorCategory) {
		t.Fatal("an uncertain outcome must never be classified retryable")
	}
	if r.AcceptedAt.IsZero() == false {
		t.Fatal("an uncertain send must not claim an acceptance time")
	}
}

// A failure before the terminating dot cannot have been accepted, however late it is.
func TestSMTPFailuresBeforeCommitAreDefinitelyNotSent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		script   []string
		stage    string
		outcome  string
		category string
	}{
		{
			name: "recipient refused permanently",
			script: []string{"220 localhost ESMTP", "250-smtp.example.test\r\n250 AUTH PLAIN",
				"235 authenticated", "250 sender ok", "550 5.1.1 no such user"},
			stage: StageRcptTo, outcome: OutcomeRejected, category: ErrorRecipient,
		},
		{
			name: "sender refused permanently",
			script: []string{"220 localhost ESMTP", "250-smtp.example.test\r\n250 AUTH PLAIN",
				"235 authenticated", "550 5.7.1 sender rejected"},
			stage: StageMailFrom, outcome: OutcomeRejected, category: ErrorProviderReject,
		},
		{
			name: "throttled",
			script: []string{"220 localhost ESMTP", "250-smtp.example.test\r\n250 AUTH PLAIN",
				"235 authenticated", "250 sender ok", "451 4.7.1 too many messages"},
			stage: StageRcptTo, outcome: OutcomeTransient, category: ErrorRateLimit,
		},
		{
			name: "bad credentials",
			script: []string{"220 localhost ESMTP", "250-smtp.example.test\r\n250 AUTH PLAIN",
				"535 5.7.8 bad credentials"},
			stage: StageAuth, outcome: OutcomeConfig, category: ErrorAuthConfig,
		},
		{
			name:   "connection lost at the greeting",
			script: []string{hangUp},
			stage:  StageGreeting, outcome: OutcomeTransient, category: ErrorConnection,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testTransport(t, tc.script).Send(context.Background(), testMessage())
			if r.Stage != tc.stage {
				t.Fatalf("stage = %q, want %q (%s)", r.Stage, tc.stage, r.Response)
			}
			if r.Outcome != tc.outcome {
				t.Fatalf("outcome = %q, want %q", r.Outcome, tc.outcome)
			}
			if r.ErrorCategory != tc.category {
				t.Fatalf("category = %q, want %q", r.ErrorCategory, tc.category)
			}
			if r.Outcome == OutcomeUncertain {
				t.Fatal("a failure before the commit point must never be reported as uncertain")
			}
		})
	}
}

// Credentials are never handed to an unencrypted remote server. This is what forces the tests
// above onto loopback, and it is worth pinning: a downgraded or misconfigured server must cost
// a refused send, not a leaked password.
func TestSMTPRefusesPlaintextCredentialsToARemoteHost(t *testing.T) {
	tr := testTransport(t, []string{
		"220 smtp.example.test ESMTP",
		"250-smtp.example.test\r\n250 AUTH PLAIN",
		"235 authenticated",
	})
	tr.Host = "smtp.example.test"
	r := tr.Send(context.Background(), testMessage())
	if r.Stage != StageAuth || r.Outcome != OutcomeConfig {
		t.Fatalf("plaintext credentials were not refused: stage %q outcome %q", r.Stage, r.Outcome)
	}
	if strings.Contains(r.Response, "secret") {
		t.Fatal("the failure response leaked the password")
	}
}

// An unconfigured transport says so instead of dialing.
func TestSMTPUnconfiguredDoesNotDial(t *testing.T) {
	tr := &SMTPTransport{Host: "", Dial: func(string, string, time.Duration) (net.Conn, error) {
		t.Fatal("an unconfigured transport tried to open a connection")
		return nil, nil
	}}
	r := tr.Send(context.Background(), testMessage())
	if r.Outcome != OutcomeConfig || r.ErrorCategory != ErrorAuthConfig {
		t.Fatalf("outcome = %q / %q", r.Outcome, r.ErrorCategory)
	}
}

// The transmitted bytes are the approved bytes plus headers, and nothing else.
func TestRenderedMessageCarriesApprovedContentOnly(t *testing.T) {
	tr := testTransport(t, nil)
	m := testMessage()
	rendered := tr.render(m)
	for _, want := range []string{"Subject: Synthetic subject", "Synthetic body.",
		"Message-ID: <synthetic@prospector.local>", "sender@example.test", "recipient@example.test"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the rendered message is missing %q:\n%s", want, rendered)
		}
	}
	headers, body, found := strings.Cut(rendered, "\r\n\r\n")
	if !found {
		headers, body, found = strings.Cut(rendered, "\n\n")
	}
	if !found {
		t.Fatalf("the rendered message has no header/body separation:\n%s", rendered)
	}
	if strings.TrimSpace(body) != m.Body {
		t.Fatalf("the body was altered in transit:\n%q", body)
	}
	if strings.Contains(headers, "\n\n") {
		t.Fatal("the header block is malformed")
	}
}

// A follow-up is threaded to its root message rather than starting a new conversation.
func TestRenderedFollowupThreadsToItsRoot(t *testing.T) {
	tr := testTransport(t, nil)
	m := testMessage()
	m.InReplyTo, m.References = "<root@prospector.local>", "<root@prospector.local>"
	rendered := tr.render(m)
	for _, want := range []string{"In-Reply-To: <root@prospector.local>", "References: <root@prospector.local>"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the follow-up is not threaded: missing %q", want)
		}
	}
}

// The Message-ID domain must come from the configured sender, not from anywhere else. Stamping
// a message with a domain we do not control breaks threading and looks like forgery.
func TestSenderDomainComesFromConfiguredFromAddress(t *testing.T) {
	tr := testTransport(t, nil)
	tr.FromAddress = "Outreach@Example.Test"
	if got := tr.SenderDomain(); got != "example.test" {
		t.Fatalf("sender domain = %q, want the normalized configured domain", got)
	}
	for _, unusable := range []string{"", "not-an-address", "trailing@"} {
		tr.FromAddress = unusable
		if got := tr.SenderDomain(); got != "" {
			t.Fatalf("an unusable from address %q produced domain %q; it must produce none", unusable, got)
		}
	}
}

// Verify proves the configuration without creating a message. The important property is the
// negative one: it must never issue MAIL FROM, so running it can never contact a recipient.
func TestVerifyAuthenticatesWithoutSending(t *testing.T) {
	tr := testTransport(t, []string{
		"220 localhost ESMTP",
		"250-localhost\r\n250 AUTH PLAIN",
		"235 authenticated",
		"221 bye", // QUIT — the next command after AUTH, not MAIL FROM
	})
	r := tr.Verify(context.Background())
	if !r.OK {
		t.Fatalf("verification failed at %s: %s", r.Stage, r.Error)
	}
	if !r.Authed {
		t.Fatal("verification did not authenticate")
	}
	if r.Stage != StageQuit {
		t.Fatalf("stage = %q, want %q", r.Stage, StageQuit)
	}
	if r.From != "sender@example.test" || r.MessageIDAt != "example.test" {
		t.Fatalf("verification reported from=%q domain=%q", r.From, r.MessageIDAt)
	}
}

func TestVerifyReportsFailureWithoutClaimingSuccess(t *testing.T) {
	badCredentials := testTransport(t, []string{
		"220 localhost ESMTP",
		"250-localhost\r\n250 AUTH PLAIN",
		"535 5.7.8 bad credentials",
	})
	r := badCredentials.Verify(context.Background())
	if r.OK || r.Authed {
		t.Fatal("verification reported success with rejected credentials")
	}
	if r.Stage != StageAuth {
		t.Fatalf("stage = %q", r.Stage)
	}
	if r.Error == "" {
		t.Fatal("no error was reported")
	}
	if strings.Contains(r.Error, "secret") {
		t.Fatal("the error leaked the password")
	}

	unconfigured := &SMTPTransport{Dial: func(string, string, time.Duration) (net.Conn, error) {
		t.Fatal("an unconfigured transport tried to connect during verification")
		return nil, nil
	}}
	if unconfigured.Verify(context.Background()).OK {
		t.Fatal("an unconfigured transport verified successfully")
	}
}
