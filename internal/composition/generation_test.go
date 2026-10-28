package composition

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/profile"
)

// duckDuckGoSnapshot is the Senior Backend Engineer application the defensive email was written
// for: Go, Python, Django, PostgreSQL and Redis are approved; the job also asks for Node.js, which
// the candidate does not have.
func duckDuckGoSnapshot() Snapshot {
	s := syntheticSnapshot(plan.SpecificJobApplication)
	s.CompanyName, s.CompanyDomain = "DuckDuckGo", "duckduckgo.com"
	s.Recipient.Address = "careers@duckduckgo.com"
	s.AdvertisedTitle = "Senior Backend Engineer"
	s.Match.OriginalTitle = s.AdvertisedTitle
	s.Match.Gaps = []opportunity.RequirementMatch{{Requirement: opportunity.Requirement{Term: "Node.js", Excerpt: "Experience with Node.js and TypeScript services"}, Outcome: "unmet"}}
	s.Technologies = []profile.Technology{{ID: "go", Name: "Go", Aliases: []string{"Golang"}}, {ID: "pg", Name: "PostgreSQL", Aliases: []string{"Postgres"}},
		{ID: "redis", Name: "Redis"}, {ID: "python", Name: "Python"}, {ID: "django", Name: "Django"}}
	s.Resources = append(s.Resources, profile.Resource{ID: "github", Kind: "portfolio", Label: "GitHub profile", URL: "https://github.com/anthonyalando8"})
	s.Sources = []Source{{ID: "job", Kind: "opportunity", Text: "Senior Backend Engineer. You will build Go and Node.js services with TypeScript."}}
	s.InputHash = s.Hash()
	return s
}

const duckDuckGoOriginal = `I am applying for the Senior Backend Engineer role at DuckDuckGo. My backend engineering work centers on Go services and structured API design built for stability and performance.

On the Bingwa platform, I built the Go REST API using Gin, pgx, PostgreSQL, and Redis. I designed an identity and access control schema featuring permissions, token verification, and audit logging, and wrote concurrent Go routines with context timeouts for request management. In another project, MediaConverter, I built background task pipelines with Django, Celery, and Redis, streaming real-time status updates over WebSockets.

While my primary backend work is in Go and Python rather than Node.js, my experience building testable API services and concurrent backend systems aligns closely with your team's objectives.

I would welcome the chance to discuss the role further and can share my CV upon request.`

const duckDuckGoCorrected = `Dear Hiring Team,

I am writing to express my strong interest in the Senior Backend Engineer role at DuckDuckGo. My backend engineering work centers on Go services and structured API design built for high performance, stability, and scale.

On the Bingwa platform, I built a robust Go REST API using Gin, pgx, PostgreSQL, and Redis. I designed an identity and access control schema with fine-grained permissions, token verification, and audit logging, while leveraging concurrent Go routines with context timeouts for reliable request management. Additionally, on the MediaConverter project, I built background task processing pipelines using Django, Celery, and Redis with real-time status streaming over WebSockets.

My hands-on experience building concurrent, highly testable backend systems and scalable API architectures aligns directly with DuckDuckGo's engineering objectives.

I would welcome the opportunity to discuss how my background fits your team's needs. My CV is attached for your review, and additional work can be found on my GitHub profile.

Best regards,
Anthony Alando
Full-Stack Software Developer | Backend Engineering
Nairobi, Kenya
GitHub: https://github.com/anthonyalando8`

func duckDuckGoEmail(s Snapshot, body string) Email {
	e := syntheticEmail(s)
	e.Subject, e.Body = "Application for Senior Backend Engineer", body
	return e
}

// Regression: the generator's defensive Node.js sentence never reaches the person. The writer is
// told in advance that Node.js may not be mentioned, and if it writes the sentence anyway, the
// sentence is removed before review, with the removal on record.
func TestDuckDuckGoDefensiveSentenceIsNotShown(t *testing.T) {
	s := duckDuckGoSnapshot()
	var request WriteRequest
	calls, reviews := 0, 0
	engine := Engine{
		Composer: writerFunc(func(_ context.Context, r WriteRequest) (Email, Metadata, error) {
			calls++
			request = r
			return duckDuckGoEmail(s, duckDuckGoOriginal), Metadata{Provider: "synthetic", Model: "fixture-writer"}, nil
		}),
		Reviewer: reviewerFunc(func(ctx context.Context, s Snapshot, e Email) (Review, Metadata, error) {
			reviews++
			return cleanReview(ctx, s, e)
		}),
	}
	result, err := engine.Run(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(request.DoNotMention, "Node.js") || slices.Contains(request.ApprovedTechnologies, "Node.js") || !slices.Contains(request.ApprovedTechnologies, "Go") {
		t.Fatalf("writer was not told the technology rules: approved=%v avoid=%v", request.ApprovedTechnologies, request.DoNotMention)
	}
	body := result.Email.Body
	for _, banned := range []string{"Node.js", "rather than", "While my primary backend work"} {
		if strings.Contains(body, banned) {
			t.Fatalf("defensive text reached the draft (%q):\n%s", banned, body)
		}
	}
	for _, kept := range []string{"Bingwa platform", "MediaConverter", "can share my CV upon request"} {
		if !strings.Contains(body, kept) {
			t.Fatalf("relevant experience was lost (%q):\n%s", kept, body)
		}
	}
	if strings.Contains(body, "\n\n\n") {
		t.Fatalf("removal left an empty paragraph:\n%q", body)
	}
	last := result.Attempts[len(result.Attempts)-1]
	if !hasCode(last.Validation, "removed_gap_sentence") || NeedsReview(last.Validation) {
		t.Fatalf("removal not recorded, or findings remain: %v", codes(last.Validation))
	}
	if result.Status != "reviewed" || calls != 1 || reviews != 1 {
		t.Fatalf("status %q after %d writes and %d reviews; a removable sentence should not cost a repair", result.Status, calls, reviews)
	}
}

// The corrected email is rejected for one real reason: it says the CV is attached, and emails
// carry no attachments. Its opening states the application clearly enough.
func TestDuckDuckGoCorrectedEmailFailsOnlyOnTheAttachmentClaim(t *testing.T) {
	s := duckDuckGoSnapshot()
	findings := Validate(s, duckDuckGoEmail(s, duckDuckGoCorrected))
	blocking := []string{}
	for _, f := range findings {
		if f.Severity != "info" {
			blocking = append(blocking, f.Code)
		}
	}
	if !slices.Equal(blocking, []string{"fabricated_attachment"}) {
		t.Fatalf("want only fabricated_attachment, got %v", blocking)
	}
	fixed := strings.Replace(duckDuckGoCorrected, "My CV is attached for your review, and additional work can be found on my GitHub profile.", "I can share my CV on request, and more of my work is on my GitHub profile.", 1)
	if findings := Validate(s, duckDuckGoEmail(s, fixed)); NeedsReview(findings) {
		t.Fatalf("the corrected email without the attachment claim should pass: %v", codes(findings))
	}
}

// A sentence that claims an unapproved technology is a factual problem: it is not quietly
// removed, it is repaired by the writer or left for the person, and the reviewer is never asked
// to bless it.
func TestClaimedUnsupportedTechnologyIsHeldNotRemoved(t *testing.T) {
	s := duckDuckGoSnapshot()
	claim := strings.Replace(duckDuckGoOriginal, "While my primary backend work is in Go and Python rather than Node.js, my", "I have also shipped Node.js services in production, and my", 1)
	reviews := 0
	engine := Engine{
		Composer: writerFunc(func(context.Context, WriteRequest) (Email, Metadata, error) {
			return duckDuckGoEmail(s, claim), Metadata{Provider: "synthetic", Model: "fixture-writer"}, nil
		}),
		Reviewer: reviewerFunc(func(ctx context.Context, s Snapshot, e Email) (Review, Metadata, error) {
			reviews++
			return cleanReview(ctx, s, e)
		}),
	}
	result, err := engine.Run(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	last := result.Attempts[len(result.Attempts)-1]
	if result.Status != "review_required" || !hasCode(last.Validation, "unapproved_technology") || last.Failure != "validation_failed" {
		t.Fatalf("status %q, failure %q, findings %v", result.Status, last.Failure, codes(last.Validation))
	}
	if !strings.Contains(result.Email.Body, "Node.js services in production") {
		t.Fatal("a false claim must be shown for repair, not silently deleted")
	}
	if reviews != 0 {
		t.Fatalf("the reviewer was asked %d times about an email that cannot pass", reviews)
	}
	if len(result.Attempts) != MaxRepairs+1 {
		t.Fatalf("expected the bounded repair attempts, got %d", len(result.Attempts))
	}
}

// A person's edit is reviewed exactly as written, gap sentence included.
func TestHumanEditIsNeverRewritten(t *testing.T) {
	s := duckDuckGoSnapshot()
	edit := duckDuckGoEmail(s, duckDuckGoOriginal)
	engine := Engine{Reviewer: reviewerFunc(cleanReview)}
	result, err := engine.Run(context.Background(), s, &edit)
	if err != nil {
		t.Fatal(err)
	}
	if result.Email.Body != duckDuckGoOriginal {
		t.Fatal("the person's text was changed")
	}
	if last := result.Attempts[len(result.Attempts)-1]; !hasCode(last.Validation, "acknowledged_skill_gap") || hasCode(last.Validation, "removed_gap_sentence") {
		t.Fatalf("the gap should be a warning on the person's own text: %v", codes(last.Validation))
	}
}

func TestDropGapSentencesKeepsParagraphs(t *testing.T) {
	e := Email{Body: "First paragraph.\n\nI build Go APIs. I have not used Node.js yet. I test everything.\n\nLast."}
	out, removed := DropGapSentences(e, []Finding{{Code: "acknowledged_skill_gap", Excerpt: "I have not used Node.js yet"}})
	if len(removed) != 1 || out.Body != "First paragraph.\n\nI build Go APIs. I test everything.\n\nLast." {
		t.Fatalf("got %q", out.Body)
	}
	whole := Email{Body: "Intro.\n\nWhile my work is in Go rather than Node.js, it fits.\n\nEnd."}
	out, _ = DropGapSentences(whole, []Finding{{Code: "unapproved_technology", Excerpt: "While my work is in Go rather than Node.js, it fits"}})
	if out.Body != "Intro.\n\nEnd." {
		t.Fatalf("got %q", out.Body)
	}
	claim := Email{Body: "I ship Node.js services."}
	if out, removed := DropGapSentences(claim, []Finding{{Code: "unapproved_technology", Excerpt: "I ship Node.js services"}}); len(removed) != 0 || out.Body != claim.Body {
		t.Fatal("a claim must not be removed as if it were a gap")
	}
}

// The payment firewall: an offer about checkout is critical when nobody asked for it, and a
// warning the sender can approve when their own notes or override reason ask for that topic.
func TestCheckoutOfferFollowsTheSendersInstruction(t *testing.T) {
	s := syntheticSnapshot(plan.ServiceOutreach)
	e := syntheticEmail(s)
	e.Subject = "Faster pages for your online store"
	e.Body = "Hello team,\n\nI had a look at your online store and would be glad to help make product pages and checkout faster.\n\n" + backendBody[strings.Index(backendBody, "In Synthetic Ledger"):]
	severity := func(s Snapshot) string {
		for _, f := range Validate(s, e) {
			if f.Code == "recipient_payment_problem" {
				return f.Severity
			}
		}
		return ""
	}
	if got := severity(s); got != "block" {
		t.Fatalf("an unrequested checkout offer should be critical, got %q", got)
	}
	asked := s
	asked.ReviewReasons = explain.Reasons{{Code: plan.OperatorNoteCode, Text: "E-commerce store; offer to speed up product pages and checkout."}}
	asked.InputHash = asked.Hash()
	if got := severity(asked); got != "review" {
		t.Fatalf("a checkout offer the sender asked for should be a warning, got %q", got)
	}
	unrelated := s
	unrelated.ReviewReasons = explain.Reasons{{Code: plan.OperatorNoteCode, Text: "Keep it short and friendly."}}
	unrelated.InputHash = unrelated.Hash()
	if got := severity(unrelated); got != "block" {
		t.Fatalf("notes that do not mention payments must not relax the rule, got %q", got)
	}
}
