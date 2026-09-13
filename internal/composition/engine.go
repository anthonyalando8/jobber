package composition

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Attempt struct {
	Number          int       `json:"number"`
	Source          string    `json:"source"` // generated, repair, human
	Email           Email     `json:"email"`
	Validation      []Finding `json:"validation"`
	Review          *Review   `json:"review,omitempty"`
	ComposeMetadata Metadata  `json:"compose_metadata"`
	ReviewMetadata  Metadata  `json:"review_metadata"`
	Failure         string    `json:"failure,omitempty"`
}
type Result struct {
	Email    Email     `json:"email"`
	Status   string    `json:"status"`
	Attempts []Attempt `json:"attempts"`
}
type Engine struct {
	Composer   Composer
	Reviewer   Reviewer
	BeforeCall func(context.Context, string) error
	Record     func(context.Context, Attempt) error
}

func cloneSnapshot(s Snapshot) Snapshot {
	raw, _ := json.Marshal(s)
	var out Snapshot
	_ = json.Unmarshal(raw, &out)
	return out
}
func (engine Engine) Run(ctx context.Context, s Snapshot, edit *Email) (Result, error) {
	result := Result{Status: "review_required", Attempts: []Attempt{}}
	if err := s.Validate(); err != nil {
		return result, err
	}
	if engine.Reviewer == nil || (edit == nil && engine.Composer == nil) {
		return result, fmt.Errorf("compose/review provider is not configured")
	}
	var previous *Email
	var findings []Finding
	limit := MaxRepairs + 1
	if edit != nil {
		limit = 1
	}
	record := func(a Attempt) error {
		result.Attempts = append(result.Attempts, a)
		if engine.Record != nil {
			return engine.Record(ctx, a)
		}
		return nil
	}
	call := func(stage string) error {
		if engine.BeforeCall != nil {
			return engine.BeforeCall(ctx, stage)
		}
		return nil
	}
	for i := 0; i < limit; i++ {
		a := Attempt{Number: i + 1, Source: "generated"}
		if i > 0 {
			a.Source = "repair"
		}
		if edit != nil {
			a.Email = *edit
			a.Source = "human"
		} else {
			if err := call("compose"); err != nil {
				a.Failure = "compose_budget_unavailable"
				if err2 := record(a); err2 != nil {
					return result, err2
				}
				return result, nil
			}
			request := WriteRequest{Snapshot: cloneSnapshot(s), Previous: previous, Findings: append([]Finding{}, findings...)}
			callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			email, meta, err := engine.Composer.Compose(callCtx, request)
			cancel()
			a.ComposeMetadata = meta
			if err != nil {
				a.Failure = "compose_failed"
				findings = []Finding{{Severity: "block", Code: "malformed_model_output", Explanation: "Return one valid JSON email object within the original factual envelope.", Repairable: true}}
				a.Validation = findings
				if err2 := record(a); err2 != nil {
					return result, err2
				}
				continue
			}
			a.Email = email
			if signature := Signature(s); signature != "" {
				a.Email.Body = strings.TrimSpace(a.Email.Body) + "\n\n" + signature
			}
		}
		result.Email = a.Email
		a.Validation = Validate(s, a.Email)
		// Persist successful composition before review begins, so a crash or timeout
		// cannot erase the text whose provider call was already consumed.
		if edit == nil && engine.Record != nil {
			pending := a
			pending.Failure = "review_pending"
			if err := engine.Record(ctx, pending); err != nil {
				return result, err
			}
		}
		// Even unannotated prose must reach independent full-email review. Deterministic
		// findings are never erased by the semantic pass or by a repair attempt.
		if err := call("review"); err != nil {
			a.Failure = "review_budget_unavailable"
			if err2 := record(a); err2 != nil {
				return result, err2
			}
			return result, nil
		}
		callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		review, meta, err := engine.Reviewer.Review(callCtx, cloneSnapshot(s), a.Email)
		cancel()
		a.ReviewMetadata = meta
		if err == nil {
			err = ValidateReview(review)
		}
		if err != nil {
			a.Failure = "review_unavailable"
			if err2 := record(a); err2 != nil {
				return result, err2
			}
			return result, nil
		}
		a.Review = &review
		if err := record(a); err != nil {
			return result, err
		}
		findings = append(append([]Finding{}, a.Validation...), review.Findings...)
		if !NeedsReview(findings) && review.Passed {
			result.Status = "reviewed"
			return result, nil
		}
		prior := a.Email
		previous = &prior
	}
	return result, nil
}
