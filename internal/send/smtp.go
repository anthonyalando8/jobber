package send

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"strings"
	"time"
)

// SMTPTransport drives the SMTP conversation step by step rather than calling smtp.SendMail,
// because the only way to tell "definitely not sent" from "possibly accepted" is to know how far
// the conversation got. smtp.SendMail collapses every failure into one opaque error.
type SMTPTransport struct {
	Host, Port  string
	Username    string
	Password    string
	FromAddress string
	FromName    string
	ReplyTo     string
	Timeout     time.Duration
	// Dial is injectable so tests can drive failure at an exact stage without a network.
	Dial func(network, address string, timeout time.Duration) (net.Conn, error)
}

func (t *SMTPTransport) Name() string { return "smtp" }

// SenderDomain is taken from the configured From address. An unconfigured transport reports an
// empty domain rather than a plausible-looking default, so readiness can refuse to send at all
// instead of stamping messages with a domain nobody owns.
func (t *SMTPTransport) SenderDomain() string {
	address := strings.TrimSpace(t.FromAddress)
	at := strings.LastIndex(address, "@")
	if at < 0 || at == len(address)-1 {
		return ""
	}
	return strings.ToLower(address[at+1:])
}

// NewSMTPFromEnv builds the transport from configuration. It reports why it is unusable rather
// than returning a transport that fails at the worst moment.
func NewSMTPFromEnv() (*SMTPTransport, string) {
	t := &SMTPTransport{
		Host: os.Getenv("SMTP_HOST"), Port: os.Getenv("SMTP_PORT"),
		Username: os.Getenv("SMTP_USER"), Password: os.Getenv("SMTP_PASS"),
		FromAddress: os.Getenv("FROM_EMAIL"), FromName: os.Getenv("FROM_NAME"),
		ReplyTo: os.Getenv("REPLY_TO"), Timeout: 30 * time.Second,
	}
	if t.Port == "" {
		t.Port = "587"
	}
	if t.FromAddress == "" {
		t.FromAddress = t.Username
	}
	switch {
	case t.Host == "":
		return t, "SMTP_HOST is not set"
	case t.Username == "" || t.Password == "":
		return t, "SMTP credentials are not set"
	case t.FromAddress == "":
		return t, "FROM_EMAIL is not set"
	}
	return t, ""
}

// Conversation stages, recorded on every result. The stage is what makes the uncertain case
// principled: before the terminating dot the message cannot have been accepted; at the dot the
// server may have accepted it even if the connection then failed.
const (
	StageDial     = "dial"
	StageGreeting = "greeting"
	StageStartTLS = "starttls"
	StageAuth     = "auth"
	StageMailFrom = "mail_from"
	StageRcptTo   = "rcpt_to"
	StageData     = "data_open"
	StageBody     = "data_write"
	StageCommit   = "data_terminate"
	StageQuit     = "quit"
)

func (t *SMTPTransport) Send(ctx Context, m Message) Result {
	r := Result{Provider: t.Name(), Stage: StageDial}
	if t.Host == "" || t.Username == "" || t.Password == "" {
		r.Outcome, r.ErrorCategory = OutcomeConfig, ErrorAuthConfig
		r.Response = "SMTP transport is not configured"
		return r
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	dial := t.Dial
	if dial == nil {
		dial = net.DialTimeout
	}

	conn, err := dial("tcp", net.JoinHostPort(t.Host, t.Port), timeout)
	if err != nil {
		return fail(r, OutcomeTransient, ErrorConnection, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	r.Stage = StageGreeting
	client, err := smtp.NewClient(conn, t.Host)
	if err != nil {
		return fail(r, OutcomeTransient, ErrorConnection, err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		r.Stage = StageStartTLS
		if err := client.StartTLS(&tls.Config{ServerName: t.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fail(r, OutcomeTransient, ErrorConnection, err)
		}
	}

	r.Stage = StageAuth
	if err := client.Auth(smtp.PlainAuth("", t.Username, t.Password, t.Host)); err != nil {
		// Bad credentials are a configuration fault. Retrying them in a loop achieves nothing
		// and can lock the account.
		return fail(r, OutcomeConfig, ErrorAuthConfig, err)
	}

	r.Stage = StageMailFrom
	if err := client.Mail(t.FromAddress); err != nil {
		return classifyReply(r, err, ErrorProviderReject)
	}

	r.Stage = StageRcptTo
	if err := client.Rcpt(m.ToAddress); err != nil {
		return classifyReply(r, err, ErrorRecipient)
	}

	r.Stage = StageData
	writer, err := client.Data()
	if err != nil {
		return classifyReply(r, err, ErrorProviderReject)
	}

	r.Stage = StageBody
	if _, err := writer.Write([]byte(t.render(m))); err != nil {
		// The body did not finish. The terminating dot was never sent, so the server cannot
		// have accepted the message.
		return fail(r, OutcomeTransient, ErrorConnection, err)
	}

	// Close writes the terminating dot and reads the server's verdict. This is the commit
	// point, and the only place where a network error genuinely leaves the outcome unknown.
	r.Stage = StageCommit
	if err := writer.Close(); err != nil {
		var protoErr *textproto.Error
		if errors.As(err, &protoErr) {
			// The server answered. Its answer is authoritative, so this is not uncertain.
			if protoErr.Code >= 500 {
				return fail(r, OutcomeRejected, ErrorProviderReject, err)
			}
			return fail(r, OutcomeTransient, ErrorTransient, err)
		}
		// No answer arrived. The message may already have been accepted.
		return fail(r, OutcomeUncertain, ErrorTimeoutUncertain, err)
	}

	r.Stage = StageQuit
	r.Outcome = OutcomeAccepted
	r.AcceptedAt = time.Now().UTC()
	r.ProviderMessageID = m.MessageID
	r.Response = "accepted by " + t.Host
	// A failure to QUIT after acceptance changes nothing: the server already took the message.
	_ = client.Quit()
	return r
}

func fail(r Result, outcome, category string, err error) Result {
	r.Outcome, r.ErrorCategory = outcome, category
	if err != nil {
		r.Response = truncate(err.Error(), 500)
	}
	return r
}

// classifyReply separates a permanent refusal from a temporary one using the SMTP reply code,
// and treats a missing reply as a connection failure at a stage before the message could have
// been accepted.
func classifyReply(r Result, err error, permanentCategory string) Result {
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) {
		if protoErr.Code >= 500 {
			return fail(r, OutcomeRejected, permanentCategory, err)
		}
		if protoErr.Code == 421 || protoErr.Code == 450 || protoErr.Code == 451 || protoErr.Code == 452 {
			return fail(r, OutcomeTransient, ErrorRateLimit, err)
		}
		return fail(r, OutcomeTransient, ErrorTransient, err)
	}
	return fail(r, OutcomeTransient, ErrorConnection, err)
}

// render builds the exact bytes transmitted. It performs no content decisions: subject and body
// arrive already approved, and only headers are added.
func (t *SMTPTransport) render(m Message) string {
	from := m.FromAddress
	if from == "" {
		from = t.FromAddress
	}
	name := m.FromName
	if name == "" {
		name = t.FromName
	}
	replyTo := m.ReplyTo
	if replyTo == "" {
		replyTo = t.ReplyTo
	}
	var b strings.Builder
	if name != "" {
		fmt.Fprintf(&b, "From: %s <%s>\r\n", mime.QEncoding.Encode("utf-8", name), from)
	} else {
		fmt.Fprintf(&b, "From: %s\r\n", from)
	}
	fmt.Fprintf(&b, "To: %s\r\n", m.ToAddress)
	if replyTo != "" {
		fmt.Fprintf(&b, "Reply-To: %s\r\n", replyTo)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	if m.MessageID != "" {
		fmt.Fprintf(&b, "Message-ID: %s\r\n", m.MessageID)
	}
	if m.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", m.InReplyTo)
	}
	if m.References != "" {
		fmt.Fprintf(&b, "References: %s\r\n", m.References)
	}
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(m.Body, "\n", "\r\n"))
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// VerifyResult reports what an SMTP configuration check established, without sending anything.
type VerifyResult struct {
	Stage       string `json:"stage"`
	OK          bool   `json:"ok"`
	Greeting    string `json:"greeting,omitempty"`
	STARTTLS    bool   `json:"starttls_used"`
	TLSVersion  string `json:"tls_version,omitempty"`
	TLSCipher   string `json:"tls_cipher,omitempty"`
	Authed      bool   `json:"authenticated"`
	From        string `json:"from_address,omitempty"`
	ReplyTo     string `json:"reply_to,omitempty"`
	MessageIDAt string `json:"message_id_domain,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Verify proves the transport can reach the server, negotiate TLS and authenticate — and then
// stops. It deliberately never issues MAIL FROM, so no message can be created by running it and
// no recipient is contacted. That makes it safe to run at any time, which is the point: the
// configuration should be provable without spending a real send.
func (t *SMTPTransport) Verify(ctx Context) VerifyResult {
	r := VerifyResult{Stage: StageDial, From: t.FromAddress, ReplyTo: t.ReplyTo, MessageIDAt: t.SenderDomain()}
	if t.Host == "" || t.Username == "" || t.Password == "" {
		r.Error = "SMTP transport is not configured"
		return r
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	dial := t.Dial
	if dial == nil {
		dial = net.DialTimeout
	}
	conn, err := dial("tcp", net.JoinHostPort(t.Host, t.Port), timeout)
	if err != nil {
		r.Error = truncate(err.Error(), 300)
		return r
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	r.Stage = StageGreeting
	client, err := smtp.NewClient(conn, t.Host)
	if err != nil {
		r.Error = truncate(err.Error(), 300)
		return r
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		r.Stage = StageStartTLS
		if err := client.StartTLS(&tls.Config{ServerName: t.Host, MinVersion: tls.VersionTLS12}); err != nil {
			r.Error = truncate(err.Error(), 300)
			return r
		}
		r.STARTTLS = true
		if state, ok := client.TLSConnectionState(); ok {
			r.TLSVersion = tlsVersionName(state.Version)
			r.TLSCipher = tls.CipherSuiteName(state.CipherSuite)
		}
	}

	r.Stage = StageAuth
	if err := client.Auth(smtp.PlainAuth("", t.Username, t.Password, t.Host)); err != nil {
		r.Error = truncate(err.Error(), 300)
		return r
	}
	r.Authed = true
	r.Stage = StageQuit
	r.OK = true
	_ = client.Quit()
	return r
}

func tlsVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS13:
		return "TLS 1.3"
	case tls.VersionTLS12:
		return "TLS 1.2"
	}
	return "unrecognized"
}
