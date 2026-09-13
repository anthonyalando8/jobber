// Package send owns the one canonical outbound path. It decides whether an already reviewed and
// explicitly approved draft may be transmitted, identifies that transmission so it can happen at
// most once, and classifies what the provider did about it. It selects no recipient, writes no
// content and makes no strategic decision: all of that is frozen upstream.
package send

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/plan"
)

// PolicyVersion changes whenever the preflight rules below change.
const PolicyVersion = "send-v1"

// Attempt states. Provider errors are never collapsed into one "failed".
const (
	// StateReserved is written before the provider is contacted, so a crash leaves evidence.
	StateReserved = "reserved"
	// StateSending means transmission is in flight.
	StateSending = "sending"
	// StateAccepted means the provider took responsibility for the message.
	StateAccepted = "accepted"
	// StateRejected means the provider refused it permanently. Retrying changes nothing.
	StateRejected = "rejected"
	// StateTransientFailed means the message definitely did not leave. Retry is permitted.
	StateTransientFailed = "transient_failed"
	// StateUncertain means the provider may or may not have accepted it. This never retries
	// automatically, because doing so is how duplicates are sent.
	StateUncertain = "uncertain"
	// StateCancelled means a reservation was abandoned before transmission.
	StateCancelled = "cancelled"
)

// blockingStates hold the idempotency key: while an attempt is in one of these, no further
// transmission of the same logical message may be reserved. transient_failed and cancelled
// deliberately release the key so a bounded retry is possible.
func BlockingState(state string) bool {
	switch state {
	case StateReserved, StateSending, StateAccepted, StateUncertain, StateRejected:
		return true
	}
	return false
}

// Thread states.
const (
	ThreadActive       = "active"
	ThreadReplied      = "replied"
	ThreadClosed       = "closed"
	ThreadBounced      = "bounced"
	ThreadDoNotContact = "do_not_contact"
)

// Message kinds.
const (
	KindFirstContact = "first_contact"
	KindFollowup     = "followup"
)

// Outcome categories returned by a transport, independent of SMTP wording.
const (
	OutcomeAccepted  = "accepted"
	OutcomeRejected  = "rejected"
	OutcomeTransient = "transient"
	OutcomeUncertain = "uncertain"
	OutcomeConfig    = "config_error"
)

// Error categories. Retryability is a property of the category, not a guess at the call site.
const (
	ErrorNone             = ""
	ErrorAuthConfig       = "auth_or_config"
	ErrorRateLimit        = "rate_limit"
	ErrorConnection       = "connection_before_transmission"
	ErrorTimeoutUncertain = "timeout_after_possible_acceptance"
	ErrorRecipient        = "recipient_rejected"
	ErrorProviderReject   = "provider_rejected"
	ErrorTransient        = "transient_provider_failure"
	ErrorMalformed        = "malformed_provider_response"
	ErrorPermanentConfig  = "permanent_configuration_failure"
)

// Retryable reports whether a category may be attempted again at all. Uncertainty is never
// retryable: only a human or a provider-side lookup can resolve it.
func Retryable(category string) bool {
	switch category {
	case ErrorConnection, ErrorRateLimit, ErrorTransient:
		return true
	}
	return false
}

// Message is exactly what will be transmitted. Every field is copied from the approved
// composition revision; nothing here is re-derived at send time.
type Message struct {
	FromName, FromAddress, ReplyTo string
	ToName, ToAddress              string
	Subject, Body                  string
	// MessageID is generated once per attempt and stored, so a real thread can be built from
	// In-Reply-To/References rather than from a "Re:" prefix on the subject.
	MessageID  string
	InReplyTo  string
	References string
}

// Result normalizes what a provider did. Workflow state never reads SMTP strings directly.
type Result struct {
	Provider          string    `json:"provider"`
	ProviderMessageID string    `json:"provider_message_id,omitempty"`
	Response          string    `json:"response,omitempty"`
	AcceptedAt        time.Time `json:"accepted_at,omitempty"`
	Outcome           string    `json:"outcome"`
	ErrorCategory     string    `json:"error_category,omitempty"`
	// Stage records how far the conversation got, which is what makes uncertainty principled
	// rather than a guess.
	Stage string `json:"stage,omitempty"`
}

// State maps a provider outcome onto the durable attempt state.
func (r Result) State() string {
	switch r.Outcome {
	case OutcomeAccepted:
		return StateAccepted
	case OutcomeRejected:
		return StateRejected
	case OutcomeUncertain:
		return StateUncertain
	case OutcomeTransient, OutcomeConfig:
		return StateTransientFailed
	}
	return StateUncertain // an unrecognized outcome is treated as unsafe, never as a clean failure
}

// Transport is the sending provider. One implementation talks SMTP; tests use a fake.
type Transport interface {
	Send(ctx Context, m Message) Result
	Name() string
	// SenderDomain is the domain this transport actually sends from. It belongs to the
	// transport because only the transport knows the configured sender identity, and it must be
	// the sender's domain rather than the recipient's: a Message-ID stamped with the recipient's
	// domain claims to originate from a host we do not control, which breaks threading and
	// reads as forgery to a receiving mail server.
	SenderDomain() string
}

// Context is the minimal context surface a transport needs, kept as an interface so this
// package does not force a particular cancellation shape on callers.
type Context interface {
	Deadline() (time.Time, bool)
	Done() <-chan struct{}
	Err() error
	Value(any) any
}

// Identity is the pinned description of one logical outbound message. Every field is frozen
// upstream; none of them is a presentation detail that a rerender could change.
type Identity struct {
	DraftID             int64
	CompositionRevision int
	ContentHash         string
	RecipientContactID  int64
	RecipientEmail      string
	PlanID              int64
	PlanRevision        int
	Intent              plan.Intent
	Kind                string
	FollowupStep        int
}

// Key is the idempotency key. Two send requests describing the same approved revision, to the
// same pinned recipient, under the same plan revision and intent, produce the same key and
// therefore at most one transmission.
//
// Deliberately included: the draft, the exact composition revision, the approved content hash,
// the pinned recipient (both id and address), the plan revision, the intent and the message kind
// with its follow-up step.
//
// Deliberately excluded: subject and body text (they are already covered by the content hash),
// timestamps, initiator, company name, and anything else a rerender or a UI refresh could
// change. Regenerating a draft produces a new composition revision and content hash, so a newly
// approved revision is a distinct send candidate rather than a duplicate of the old one.
func (i Identity) Key() string {
	kind := i.Kind
	if kind == "" {
		kind = KindFirstContact
	}
	parts := []string{
		"send-v1",
		itoa64(i.DraftID),
		itoa(i.CompositionRevision),
		i.ContentHash,
		itoa64(i.RecipientContactID),
		strings.ToLower(strings.TrimSpace(i.RecipientEmail)),
		itoa64(i.PlanID),
		itoa(i.PlanRevision),
		string(i.Intent),
		kind,
		itoa(i.FollowupStep),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func (i Identity) Validate() error {
	if i.DraftID <= 0 || i.CompositionRevision <= 0 || i.ContentHash == "" {
		return fmt.Errorf("send identity needs a pinned draft revision and content hash")
	}
	if i.RecipientContactID <= 0 || strings.TrimSpace(i.RecipientEmail) == "" {
		return fmt.Errorf("send identity needs a pinned recipient")
	}
	if i.PlanID <= 0 || i.PlanRevision <= 0 || !i.Intent.Valid() {
		return fmt.Errorf("send identity needs a pinned plan revision and authoritative intent")
	}
	if i.Kind != KindFirstContact && i.Kind != KindFollowup {
		return fmt.Errorf("send identity needs a message kind")
	}
	if i.Kind == KindFollowup && i.FollowupStep <= 0 {
		return fmt.Errorf("a follow-up needs its step")
	}
	return nil
}

// NewMessageID builds an RFC-style Message-ID from the send key, so the identifier written into
// the headers is derivable from the attempt and not from a clock alone.
func NewMessageID(key, domain string) string {
	if domain == "" {
		domain = "localhost"
	}
	if len(key) > 32 {
		key = key[:32]
	}
	return "<" + key + "@" + domain + ">"
}

func itoa(v int) string { return itoa64(int64(v)) }

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func reason(code, text string) explain.Reason { return explain.New(code, text) }
