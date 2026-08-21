package draft

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AIProvider generates only what spec section 7 allows the model to write: one observation and
// one hypothesis, grounded in the signals given to it. Everything else in a draft — the ask,
// price, proof links, signature — is literal template text filled in Go.
type AIProvider interface {
	Observe(ctx context.Context, req ObserveRequest) (*ObserveResult, error)
	// MinInterval is the minimum time the caller should leave between Observe calls to stay
	// under this provider's rate limit. Providers with generous limits (or that pool multiple
	// keys) return a smaller value; 0 means no pacing needed.
	MinInterval() time.Duration
}

// SignalInput is the evidence shown to the model for one company.
type SignalInput struct {
	ID     int64
	Kind   string
	Detail string
	URL    string
}

// ObserveRequest asks the model to ground an observation in real, provided signals.
type ObserveRequest struct {
	CompanyName string
	Domain      string
	Segment     string
	Signals     []SignalInput
}

// ObserveResult is the model's structured reply. Insufficient is true when the model literally
// replied INSUFFICIENT because the signals didn't support a concrete, specific claim.
type ObserveResult struct {
	Insufficient bool
	Observation  string
	Hypothesis   string
	SignalIDs    []int64
}

const systemPrompt = `You write one-line openers for a Nairobi backend engineer's cold outreach. You may
only state facts present in the SIGNALS provided. If the signals do not support a
specific, concrete observation, reply exactly: INSUFFICIENT.
Never invent clients, revenue, team size, or technology. No flattery, no adjectives
like "impressive" or "exciting". Kenyan business English, plain and direct.
The observation is inserted into a sentence that already starts with "I noticed", "I came
across you", or "I saw you're hiring" — write only the bare fact that follows, with no
leading "I noticed"/"I saw"/"I came across"/"I found" of your own, or it reads doubled
("I noticed I noticed...").
If segment is "agency", the recipient is itself a technical company — a web/software/app
development shop. Never point at their own site's tech stack, age, or polish as if it were a
gap ("your site runs WordPress", "hasn't been updated since 2022") — to a company that
professionally does this work, that reads as irrelevant at best and condescending at worst
("you don't know your own site is outdated?"). Ground the observation in what they build or
offer for clients instead, or in a genuine business signal (a payment complaint, a paybill
number). If neither is present in the signals, reply INSUFFICIENT rather than critique their
site — a skipped draft is better than one that opens by insulting the reader's own work.`

// buildUserPrompt renders the section 7 USER block: company facts, verbatim signals, and the
// exact JSON shape the model must reply with.
//
// Each signal's numeric id is shown explicitly and signal_ids is spelled out as "the numeric id
// column, not the kind" — without this the model has nothing to copy from but the kind/detail
// text, and reliably invents a signal_ids value like "checkout_present" or a kind:detail:url
// composite instead of a real id, which fails validation against the database ids every time.
func buildUserPrompt(req ObserveRequest) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Company: %s · %s · segment %s\nSignals:\n", req.CompanyName, req.Domain, req.Segment)
	for _, s := range req.Signals {
		fmt.Fprintf(&sb, "- id=%d %s: %q (%s)\n", s.ID, s.Kind, s.Detail, s.URL)
	}
	sb.WriteString(`Return JSON only:
{"observation": "<=25 words, the bare fact only — do not start with I noticed/I saw/I came across/I found, no compliment",
 "hypothesis":  "<=20 words, the likely problem this implies for them",
 "signal_ids":  [the numeric id value(s) from "id=" above that you used, e.g. [3, 7] — never the kind name or text]}`)
	return sb.String()
}

// ParseError means the model's reply couldn't be understood as the section 7 contract requires
// (bad JSON, or a signal_ids entry that isn't a number). Callers should treat this as
// needs_manual, not a hard failure — it's a model-output-quality problem, not a system error, and
// the whole point of the contract is to never guess when the response can't be trusted.
type ParseError struct {
	Raw string
	Err error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse model response: %v (raw: %s)", e.Err, e.Raw)
}
func (e *ParseError) Unwrap() error { return e.Err }

// parseObserveResponse turns raw model text into an ObserveResult. Handles the literal
// INSUFFICIENT reply and defensively strips a markdown code fence in case the model wraps its
// JSON despite being told not to.
func parseObserveResponse(text string) (*ObserveResult, error) {
	text = strings.TrimSpace(text)
	if text == "INSUFFICIENT" {
		return &ObserveResult{Insufficient: true}, nil
	}

	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var parsed struct {
		Observation string  `json:"observation"`
		Hypothesis  string  `json:"hypothesis"`
		SignalIDs   []int64 `json:"signal_ids"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, &ParseError{Raw: text, Err: err}
	}

	return &ObserveResult{
		Observation: parsed.Observation,
		Hypothesis:  parsed.Hypothesis,
		SignalIDs:   parsed.SignalIDs,
	}, nil
}
