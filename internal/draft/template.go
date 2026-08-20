package draft

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/anthonyalando8/prospector/internal/store"
)

// TemplateService implements the section 7 pipeline: ask the model for a grounded
// observation/hypothesis, validate it against the rejection rules, then fill the literal
// per-segment template text (loaded from the `template` table) around it.
type TemplateService struct {
	aiProvider AIProvider
	db         *store.DB
}

// NewTemplateService creates a template service.
func NewTemplateService(aiProvider AIProvider, db *store.DB) *TemplateService {
	return &TemplateService{aiProvider: aiProvider, db: db}
}

// DraftInput is everything needed to draft one company's message.
type DraftInput struct {
	Company   *store.Company
	FirstName string // "there" if no named contact
	Role      string // hiring segment only — from the job-ad signal, never the model
	Channel   string // email|whatsapp|phone_script
	Signals   []*store.Signal
}

// Result is either a ready-to-review draft (State == "pending") or a rejection
// (State == "needs_manual", Reason explains why — see spec section 7).
type Result struct {
	Subject      string
	Body         string
	Channel      string
	State        string
	Reason       string
	EvidenceUsed string // comma-separated signal ids the model cited
	// APICalled is false only for the pre-call cost guard (no signals to ground a draft in) —
	// true for every other path, including a rejected/unparseable model reply, since those
	// still spent a real call against the AI budget. Callers use this to decide whether to
	// record AI budget usage (internal/aibudget) — a needs_manual outcome must not look free
	// just because it didn't produce a sendable draft.
	APICalled bool
}

// GenerateDraft runs one company through the section 7 contract.
func (ts *TemplateService) GenerateDraft(ctx context.Context, in DraftInput) (*Result, error) {
	if ts.aiProvider == nil {
		return nil, fmt.Errorf("AI provider not configured")
	}

	segment := "sme"
	if in.Company.Segment != nil && *in.Company.Segment != "" {
		segment = *in.Company.Segment
	}

	// Cost guard: don't spend an API call on a company with nothing to ground a draft in.
	if len(in.Signals) == 0 {
		return &Result{Channel: in.Channel, State: "needs_manual", Reason: "no signals to ground a draft"}, nil
	}

	tmpl, err := ts.db.GetTemplate(ctx, segment, in.Channel)
	if err != nil {
		return nil, fmt.Errorf("no template for segment=%s channel=%s: %w", segment, in.Channel, err)
	}

	signalInputs := make([]SignalInput, len(in.Signals))
	validIDs := make(map[int64]bool, len(in.Signals))
	for i, s := range in.Signals {
		url := ""
		if s.URL != nil {
			url = *s.URL
		}
		signalInputs[i] = SignalInput{ID: s.ID, Kind: s.Kind, Detail: s.Detail, URL: url}
		validIDs[s.ID] = true
	}

	obs, err := ts.aiProvider.Observe(ctx, ObserveRequest{
		CompanyName: in.Company.Name,
		Domain:      in.Company.Domain,
		Segment:     segment,
		Signals:     signalInputs,
	})
	if err != nil {
		var parseErr *ParseError
		if errors.As(err, &parseErr) {
			return &Result{Channel: in.Channel, State: "needs_manual", Reason: "model reply didn't match the required format: " + parseErr.Err.Error(), APICalled: true}, nil
		}
		return nil, fmt.Errorf("observe failed: %w", err)
	}

	// The system prompt tells the model not to prefix the observation with "I noticed"/"I
	// saw"/etc — every template already supplies that framing — but a prompt instruction is
	// never 100% reliable. Strip it deterministically rather than trust compliance, since the
	// alternative is a literal "I noticed I noticed ..." landing in a sent email.
	obs.Observation = stripLeadingFraming(obs.Observation)

	if reason, ok := rejectionReason(obs, validIDs, in.Signals); !ok {
		return &Result{Channel: in.Channel, State: "needs_manual", Reason: reason, APICalled: true}, nil
	}

	vars := map[string]string{
		"company":     in.Company.Name,
		"first_name":  in.FirstName,
		"observation": obs.Observation,
		"hypothesis":  obs.Hypothesis,
		"role":        in.Role,
	}

	subject := ""
	if tmpl.Subject != nil {
		subject = fillVars(*tmpl.Subject, vars)
	}
	body := fillVars(tmpl.Body, vars)

	if strings.Contains(body, "[") || strings.Contains(body, "{{") || strings.Contains(subject, "{{") {
		return &Result{Channel: in.Channel, State: "needs_manual", Reason: "template still has an unresolved placeholder after fill", APICalled: true}, nil
	}

	idStrs := make([]string, len(obs.SignalIDs))
	for i, id := range obs.SignalIDs {
		idStrs[i] = fmt.Sprintf("%d", id)
	}

	return &Result{
		Subject:      subject,
		Body:         body,
		Channel:      in.Channel,
		State:        "pending",
		EvidenceUsed: strings.Join(idStrs, ","),
		APICalled:    true,
	}, nil
}

// rejectionReason applies the section 7 rejection rules. ok is false when the draft must become
// needs_manual.
func rejectionReason(obs *ObserveResult, validIDs map[int64]bool, signals []*store.Signal) (reason string, ok bool) {
	if obs.Insufficient {
		return "model returned INSUFFICIENT", false
	}
	if len(obs.SignalIDs) == 0 {
		return "model returned no signal_ids", false
	}
	for _, id := range obs.SignalIDs {
		if !validIDs[id] {
			return fmt.Sprintf("model cited signal_id %d not present on this company", id), false
		}
	}
	if wordCount(obs.Observation) > 25 {
		return "observation exceeds 25 words", false
	}
	if wordCount(obs.Hypothesis) > 20 {
		return "hypothesis exceeds 20 words", false
	}
	if n, found := foreignNumber(obs.Observation, signals); found {
		return fmt.Sprintf("observation states a number (%s) not present in the signals", n), false
	}
	return "", true
}

// leadingFramingPhrases are openers every template already supplies around {{observation}}
// ("I noticed {{observation}}.", "I came across {{company}} — {{observation}}.", "I saw
// you're hiring a {{role}} — {{observation}}."). If the model's own text starts with one of
// these too, checked longest-first so "i came across" matches before a shorter false
// prefix could, the result reads doubled ("I noticed I noticed...") or redundant.
var leadingFramingPhrases = []string{
	"i noticed that", "i noticed",
	"i came across your", "i came across",
	"i saw that", "i saw",
	"i found that", "i found",
}

// stripLeadingFraming removes one leading framing phrase (case-insensitively) and the
// punctuation/whitespace that follows it, leaving the bare fact the template's own framing
// text already introduces. Every template inserts {{observation}} mid-sentence ("I noticed
// {{observation}}.", "... hiring a {{role}} — {{observation}}."), so the remainder is
// lowercased to fit that position, not capitalized as if it started a new sentence.
func stripLeadingFraming(s string) string {
	trimmed := strings.TrimSpace(s)
	lower := strings.ToLower(trimmed)
	for _, phrase := range leadingFramingPhrases {
		if strings.HasPrefix(lower, phrase) {
			rest := trimmed[len(phrase):]
			rest = strings.TrimLeft(rest, " ,-—")
			if rest == "" {
				break // stripping would leave nothing — keep the original rather than emptying it
			}
			r := []rune(rest)
			r[0] = []rune(strings.ToLower(string(r[0])))[0]
			return string(r)
		}
	}
	return trimmed
}

func fillVars(text string, vars map[string]string) string {
	for k, v := range vars {
		text = strings.ReplaceAll(text, "{{"+k+"}}", v)
	}
	return text
}

func wordCount(s string) int {
	return len(strings.Fields(s))
}

// foreignNumber returns the first digit sequence in observation that doesn't appear verbatim in
// any signal detail — the guard against the model inventing a number (revenue, team size, etc).
func foreignNumber(observation string, signals []*store.Signal) (string, bool) {
	nums := extractNumbers(observation)
	if len(nums) == 0 {
		return "", false
	}

	var haystack strings.Builder
	for _, s := range signals {
		haystack.WriteString(s.Detail)
		haystack.WriteByte(' ')
	}
	hay := haystack.String()

	for _, n := range nums {
		if !strings.Contains(hay, n) {
			return n, true
		}
	}
	return "", false
}

func extractNumbers(s string) []string {
	var nums []string
	var cur strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else if cur.Len() > 0 {
			nums = append(nums, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		nums = append(nums, cur.String())
	}
	return nums
}

// ValidateTemplate checks a body for unresolved placeholders — used again at send time in case
// a human edit reintroduced one.
func ValidateTemplate(body string) error {
	if strings.Contains(body, "{{") || strings.Contains(body, "[") {
		return fmt.Errorf("template contains unresolved placeholders")
	}
	return nil
}
