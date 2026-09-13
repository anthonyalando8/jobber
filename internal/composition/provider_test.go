package composition

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/plan"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProvidersUseIndependentContractsAndRejectTruncation(t *testing.T) {
	s := syntheticSnapshot(plan.SpecificJobApplication)
	for _, provider := range []string{"claude", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			stage := "compose"
			truncated := false
			p := &JSONProvider{Provider: provider, Model: "synthetic-model", Keys: []string{"synthetic-key"}, Client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				payload, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				var request map[string]any
				if err := json.Unmarshal(payload, &request); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(r.URL.RawQuery, "synthetic-key") {
					t.Fatal("credential in URL")
				}
				if stage == "review" && (strings.Contains(string(payload), "repair_findings") || strings.Contains(string(payload), `previous`)) {
					t.Fatal("review depends on composer repair reasoning")
				}
				var output any = syntheticEmail(s)
				if stage == "review" {
					output = Review{Complete: true, Passed: true, Findings: []Finding{}}
				}
				raw, _ := json.Marshal(output)
				var envelope any
				if provider == "claude" {
					stop := "end_turn"
					if truncated {
						stop = "max_tokens"
					}
					envelope = map[string]any{"id": "fixture-id", "stop_reason": stop, "content": []any{map[string]string{"type": "text", "text": string(raw)}}}
				} else {
					stop := "STOP"
					if truncated {
						stop = "MAX_TOKENS"
					}
					envelope = map[string]any{"candidates": []any{map[string]any{"finishReason": stop, "content": map[string]any{"parts": []any{map[string]string{"text": string(raw)}}}}}}
				}
				body, _ := json.Marshal(envelope)
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}}
			email, meta, err := p.Compose(context.Background(), WriteRequest{Snapshot: s})
			if err != nil || email.Intent != s.Intent || meta.PromptVersion != PolicyVersion {
				t.Fatalf("compose: %v %+v", err, meta)
			}
			stage = "review"
			review, meta, err := p.Review(context.Background(), s, email)
			if err != nil || !review.Complete || meta.PromptVersion != ReviewVersion {
				t.Fatalf("review: %v %+v", err, meta)
			}
			truncated = true
			if _, meta, err := p.Review(context.Background(), s, email); err == nil || meta.ErrorCategory != "incomplete" {
				t.Fatalf("truncated accepted: %v %+v", err, meta)
			}
		})
	}
}

func TestIndependentProviderConfiguration(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "synthetic")
	t.Setenv("GEMINI_API_KEY", "synthetic")
	t.Setenv("EMAIL_COMPOSE_PROVIDER", "claude")
	t.Setenv("EMAIL_COMPOSE_MODEL", "fixture-writer")
	t.Setenv("EMAIL_REVIEW_PROVIDER", "gemini")
	t.Setenv("EMAIL_REVIEW_MODEL", "fixture-reviewer")
	writer, err := NewConfigured("COMPOSE")
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := NewConfigured("REVIEW")
	if err != nil {
		t.Fatal(err)
	}
	if writer.Provider == reviewer.Provider || writer.Model == reviewer.Model {
		t.Fatal("routing coupled")
	}
	for _, intent := range []plan.Intent{plan.SpecificJobApplication, plan.SpeculativeJobEnquiry, plan.ServiceOutreach} {
		if _, err := CompositionPrompt(string(intent)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := CompositionPrompt("marketing_application"); err == nil {
		t.Fatal("unsupported intent prompt")
	}
}

// GEMINI_API_KEY is documented as a comma-separated pool because Google's quota is per key.
// Passing the whole string as one credential authenticates against nothing, so the pool must be
// split and rotated the way the legacy provider already does.
func TestGeminiKeyPoolIsSplitAndRotated(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", " key-one , key-two ,")
	t.Setenv("EMAIL_COMPOSE_PROVIDER", "gemini")
	t.Setenv("EMAIL_COMPOSE_MODEL", "fixture-writer")
	p, err := NewConfigured("COMPOSE")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Keys) != 2 || p.Keys[0] != "key-one" || p.Keys[1] != "key-two" {
		t.Fatalf("pool = %v, want two trimmed keys", p.Keys)
	}
	seen := []string{p.key(), p.key(), p.key()}
	if seen[0] != "key-one" || seen[1] != "key-two" || seen[2] != "key-one" {
		t.Fatalf("keys did not rotate round-robin: %v", seen)
	}

	t.Setenv("GEMINI_API_KEY", "  ,  ")
	if _, err := NewConfigured("COMPOSE"); err == nil {
		t.Fatal("an empty pool must be refused rather than sending a blank credential")
	}
}

// Quota is per key, so one exhausted credential in the pool must not fail every other call.
// The provider rotates past a rate-limited key at most one full pass; it never reissues the
// same rejected request against the same credential and never sleeps in a loop.
func TestRateLimitedKeyRotatesToTheNextCredential(t *testing.T) {
	used := []string{}
	p := &JSONProvider{Provider: "gemini", Model: "fixture", Keys: []string{"spent", "fresh"},
		Client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			key := r.Header.Get("x-goog-api-key")
			used = append(used, key)
			if key == "spent" {
				return &http.Response{StatusCode: http.StatusTooManyRequests,
					Body: io.NopCloser(strings.NewReader(`{"error":{"code":429}}`)), Header: http.Header{}}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(
					`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"{\"complete\":true,\"passed\":true,\"findings\":[]}"}]}}]}`))}, nil
		})}}
	review, meta, err := p.Review(context.Background(), syntheticSnapshot(plan.SpecificJobApplication), Email{})
	if err != nil {
		t.Fatalf("rotation should have reached the fresh key: %v (meta %s)", err, meta.ErrorCategory)
	}
	if !review.Complete {
		t.Fatal("expected the fresh key's response")
	}
	if len(used) != 2 || used[0] != "spent" || used[1] != "fresh" {
		t.Fatalf("credentials used = %v, want one rotation", used)
	}

	// Every credential exhausted is a bounded, explicit failure rather than a loop.
	all := &JSONProvider{Provider: "gemini", Model: "fixture", Keys: []string{"a", "b"},
		Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(`{"error":{"code":429}}`))}, nil
		})}}
	if _, meta, err := all.Review(context.Background(), syntheticSnapshot(plan.SpecificJobApplication), Email{}); err == nil {
		t.Fatal("a fully exhausted pool must fail")
	} else if meta.ErrorCategory != "http_429" && meta.ErrorCategory != "rate_limited" {
		t.Fatalf("error category = %q, want a rate-limit category", meta.ErrorCategory)
	}
}

// The opening guidance must never be able to weaken application intent or the exact-title
// requirement. Prompt text is not covered by the scripted-provider tests, so these
// non-negotiables are asserted directly against the shipped prompt files.
func TestPromptsRetainNonNegotiableInstructions(t *testing.T) {
	// Prompts are hard-wrapped, so compare with whitespace collapsed.
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	application, err := CompositionPrompt("specific_job_application")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"State application intent and the EXACT pinned advertised title in both subject and",
		"must be in no doubt that this is an application for that exact role",
	} {
		if !strings.Contains(flat(application), flat(required)) {
			t.Fatalf("the application prompt no longer requires %q", required)
		}
	}
	// The opening rule must not have been written as a ban on saying you are applying.
	common, err := CompositionPrompt("speculative_job_enquiry")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flat(common), "Stating your purpose plainly is not") {
		t.Fatal("the opening rule must explicitly preserve stating the purpose plainly")
	}
	for _, forbidden := range []string{"never say you are applying", "do not use the word apply", "avoid the word \"apply\""} {
		if strings.Contains(flat(strings.ToLower(common)), forbidden) {
			t.Fatalf("the opening rule must not suppress application language: %q", forbidden)
		}
	}
	speculative, err := CompositionPrompt("speculative_job_enquiry")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flat(speculative), "provenance only") {
		t.Fatal("the speculative prompt must keep the provenance-only rule")
	}
	service, err := CompositionPrompt("service_outreach")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flat(service), "never establish a payment problem") {
		t.Fatal("the service prompt must keep the payment firewall rule")
	}
	review, err := prompts.ReadFile("prompts/review.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flat(string(review)), "contradicts its intent even when every") {
		t.Fatal("the review prompt must treat an intent contradiction as a hard failure")
	}
}

// The reviewer must separate throat-clearing from a direct statement of purpose. Flagging
// "I'm applying for the X role at Y" as a stock opening put seven of eight drafts into
// review_required during the 2026-09-13 validation run; declaring the application is exactly
// what an application is required to do.
func TestReviewPromptSeparatesThroatClearingFromDirectPurpose(t *testing.T) {
	raw, err := prompts.ReadFile("prompts/review.txt")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(string(raw)), " ")

	// Still flagged: content-free announcements of the act of writing.
	for _, required := range []string{
		`"I am writing to ..."`,
		"I am reaching out to",
		"announces contact and adds nothing",
	} {
		if !strings.Contains(text, strings.Join(strings.Fields(required), " ")) {
			t.Fatalf("the review prompt must still flag content-free openings: missing %q", required)
		}
	}

	// Never flagged for that reason alone: direct application declarations.
	for _, protected := range []string{
		"Directly declaring the purpose is NOT a stock opening",
		`"I am applying for ..."`,
		`"I would like to apply for ..."`,
		`"Please consider my application for ..."`,
		"Never raise a stock-opening or templated-opening finding merely because the email says plainly that it is an application",
	} {
		if !strings.Contains(text, strings.Join(strings.Fields(protected), " ")) {
			t.Fatalf("the review prompt must protect direct application declarations: missing %q", protected)
		}
	}

	// The correction must not have removed tone review altogether.
	for _, kept := range []string{
		"generic or robotic phrasing", "repetition", "weak CTA",
		"reads as a filled-in template",
		"genuinely content-free, generic or formulaic in context",
	} {
		if !strings.Contains(text, strings.Join(strings.Fields(kept), " ")) {
			t.Fatalf("tone review must survive the correction: missing %q", kept)
		}
	}
}
