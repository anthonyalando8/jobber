// prospector-rehearse answers one question without changing anything: if a given candidate
// profile revision were the active one, what would the deterministic pipeline conclude about
// the opportunities already stored?
//
// It opens the database read-only in practice — it issues no INSERT, UPDATE or DELETE — and it
// never activates a profile, never calls an AI provider and never contacts a mail server. That
// matters because matching is authoritative only against the active revision: while none is
// active the real pipeline correctly answers profile_required, and the only honest way to see
// what activation would produce is to compute it in memory and say so.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/assess"
	"github.com/anthonyalando8/prospector/internal/opportunity"
	"github.com/anthonyalando8/prospector/internal/profile"
	"github.com/anthonyalando8/prospector/internal/relevance"
	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/joho/godotenv"
)

// Rehearsal is the whole report. Counts first, then the examples a person actually reads.
type Rehearsal struct {
	CapturedAt        time.Time       `json:"captured_at"`
	ProfileRevisionID int64           `json:"profile_revision_id"`
	ProfileStatus     string          `json:"profile_status"`
	ProfileActive     bool            `json:"profile_active_in_database"`
	Authoritative     bool            `json:"authoritative"`
	Note              string          `json:"note"`
	Assessed          int             `json:"opportunities_assessed"`
	Classification    map[string]int  `json:"classification_counts"`
	DescriptionStatus map[string]int  `json:"description_status_counts"`
	TopReasons        []CountedReason `json:"most_common_reasons"`
	TopBlockers       []CountedReason `json:"most_common_blockers"`
	// BlockedOnlyByExperience counts opportunities that clear every other strong-match gate and
	// fail solely because the posting states a years-of-experience requirement. The approved
	// profile deliberately makes no experience claim, so this is a standing policy question
	// rather than a matching defect.
	BlockedOnlyByExperience int                `json:"possible_matches_blocked_only_by_experience"`
	Examples                []RehearsedExample `json:"examples"`
	// Walks record what the real, stored workflow answers today for the same companies. They go
	// through assess.Service exactly as the web UI does, so they show the authoritative path
	// rather than the in-memory rehearsal.
	Walks []WorkflowWalk `json:"workflow_walks"`
	// Automation is the Phase 7 question: of everything stored, how much would selective
	// automation actually touch, and what stops the rest.
	Automation AutomationRehearsal `json:"automation"`
	// StrongMatchContacts is the contact situation at exactly the companies that reached a
	// strong match — the bottleneck the Phase 7 rehearsal identified.
	StrongMatchContacts []store.StrongMatchContactReport `json:"strong_match_contacts"`
}

// AutomationRehearsal funnels the whole corpus through the automation gates that can be decided
// without composing anything, and says what blocks the rest. It writes nothing and sends nothing.
type AutomationRehearsal struct {
	Considered int `json:"opportunities_considered"`
	// StrongMatches is the first gate: only a strong match can ever be automated.
	StrongMatches int `json:"reach_strong_match"`
	// WithContactableRecipient survives the recipient-quality gates: a real human mailbox with
	// an identified recruiting or engineering function, or an employer-published address.
	WithContactableRecipient int `json:"survive_recipient_quality"`
	// SurviveDeterministicGates additionally have an open vacancy and a complete description.
	SurviveDeterministicGates int            `json:"survive_all_deterministic_gates"`
	Blockers                  map[string]int `json:"blocking_reasons"`
	// AwaitingComposition is what a real run would still have to do before any of these could
	// be eligible: compose, validate and review. Automation never skips that.
	AwaitingComposition int `json:"still_require_composition_and_review"`
}

// WorkflowWalk is one company taken through the live read-only pipeline: stored relevance,
// stored assessment, resolved intent, ranked recipients and selected proof.
type WorkflowWalk struct {
	CompanyID        int64    `json:"company_id"`
	Company          string   `json:"company"`
	OpportunityID    int64    `json:"opportunity_id"`
	Title            string   `json:"title"`
	RelevanceScore   int      `json:"relevance_score"`
	StoredAssessment bool     `json:"stored_assessment_present"`
	Outcome          string   `json:"intent_outcome"`
	Intent           string   `json:"intent"`
	RequiresReview   bool     `json:"requires_review"`
	Blockers         []string `json:"blockers"`
	Reasons          []string `json:"reasons"`
	TopRecipient     string   `json:"top_recipient"`
	RecipientReason  string   `json:"top_recipient_reason"`
	SelectedProof    []string `json:"selected_proof"`
}

type CountedReason struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
	Text  string `json:"example_text"`
}

type RehearsedExample struct {
	OpportunityID  int64    `json:"opportunity_id"`
	Company        string   `json:"company"`
	Title          string   `json:"title"`
	Classification string   `json:"classification"`
	Score          int      `json:"score"`
	DescriptionLen int      `json:"description_bytes"`
	Matched        []string `json:"matched_requirements"`
	Unmet          []string `json:"unmet_requirements"`
	Unknown        []string `json:"unknown_requirements"`
	Reasons        []string `json:"reasons"`
	// Blockers are why a classification is not stronger than it is. Without them a
	// "possible_match score 100" looks like a scoring bug rather than a gate doing its job.
	Blockers []string `json:"blockers"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("prospector-rehearse", flag.ContinueOnError)
	revisionID := flags.Int64("profile-revision", 0, "candidate profile revision to rehearse; defaults to the newest")
	out := flags.String("out", "", "write the full JSON report here")
	perClass := flags.Int("examples", 3, "examples to show per classification")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return errors.New("could not load environment file")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	db, err := store.NewDB(ctx, dsn)
	if err != nil {
		return errors.New("database connection failed (connection string withheld)")
	}
	defer db.Close()

	revisions, err := db.ListProfileRevisions(ctx)
	if err != nil {
		return err
	}
	if len(revisions) == 0 {
		return errors.New("no candidate profile revision exists to rehearse")
	}
	chosen := revisions[0]
	if *revisionID > 0 {
		found := false
		for _, r := range revisions {
			if r.ID == *revisionID {
				chosen, found = r, true
			}
		}
		if !found {
			return fmt.Errorf("profile revision %d does not exist", *revisionID)
		}
	}

	report := Rehearsal{
		CapturedAt: time.Now().UTC(), ProfileRevisionID: chosen.ID, ProfileStatus: chosen.Status,
		ProfileActive: chosen.Active, Authoritative: chosen.Active && chosen.Status == "approved",
		Classification: map[string]int{}, DescriptionStatus: map[string]int{},
	}
	if !report.Authoritative {
		report.Note = "This is a rehearsal, not a stored assessment. No profile was activated and nothing was " +
			"written. While no approved revision is active the live pipeline correctly answers profile_required."
	} else {
		report.Note = "This revision is the active approved profile, so these results match what the stored " +
			"assessments would say. Nothing was written by this command regardless."
	}

	document := chosen.Document
	if err := validateDocument(document); err != nil {
		return err
	}

	// The whole corpus, not the UI's capped list: a rehearsal that quietly skipped a third of
	// the opportunities would misreport the distribution it exists to measure.
	ids, err := db.AllOpportunityIDs(ctx)
	if err != nil {
		return err
	}
	reasonCounts := map[string]int{}
	reasonText := map[string]string{}
	blockerCounts := map[string]int{}
	blockerText := map[string]string{}
	byClass := map[string][]RehearsedExample{}
	strongIDs := []int64{}

	for _, id := range ids {
		row, err := db.GetOpportunity(ctx, id)
		if err != nil {
			return err
		}
		result := opportunity.Assess(opportunity.AssessInput{
			OpportunityID: row.ID, OpportunityRevision: row.Revision, Facts: row.Facts,
			Profile: &document, ProfileRevisionID: chosen.ID,
		})
		report.Assessed++
		report.Classification[result.Classification]++
		report.DescriptionStatus[row.Facts.DescriptionStatus]++
		for _, reason := range result.Reasons {
			reasonCounts[reason.Code]++
			if _, seen := reasonText[reason.Code]; !seen {
				reasonText[reason.Code] = reason.Text
			}
		}
		example := RehearsedExample{
			OpportunityID: row.ID, Company: companyName(ctx, db, row.CompanyID), Title: row.Facts.Title,
			Classification: result.Classification, Score: result.Score,
			DescriptionLen: len(row.Facts.Description),
		}
		for _, m := range result.Matched {
			example.Matched = append(example.Matched, m.Requirement.Term)
		}
		for _, g := range result.Gaps {
			example.Unmet = append(example.Unmet, g.Requirement.Term+" ("+g.Outcome+")")
		}
		for _, u := range result.Unparsed {
			// An unparsed requirement has no matched term by definition; its excerpt is the
			// only thing that says what was actually in the posting.
			example.Unknown = append(example.Unknown, truncate(u.Excerpt, 80))
		}
		for _, reason := range result.Reasons {
			example.Reasons = append(example.Reasons, reason.Code+": "+reason.Text)
		}
		experienceOnly := len(result.Blockers) > 0
		for _, blocker := range result.Blockers {
			example.Blockers = append(example.Blockers, blocker.Code+": "+blocker.Text)
			blockerCounts[blocker.Code]++
			if _, seen := blockerText[blocker.Code]; !seen {
				blockerText[blocker.Code] = blocker.Text
			}
			if !strings.Contains(strings.ToLower(blocker.Text), "years of experience") {
				experienceOnly = false
			}
		}
		if experienceOnly && result.Classification == opportunity.ClassificationPossible {
			report.BlockedOnlyByExperience++
		}
		byClass[result.Classification] = append(byClass[result.Classification], example)
		if result.Classification == opportunity.ClassificationStrong {
			strongIDs = append(strongIDs, row.ID)
		}
	}

	// Show the best-evidenced example of each classification rather than whichever happened to
	// be first, so the report reflects the strongest case the matcher can actually make.
	for class := range byClass {
		list := byClass[class]
		sort.Slice(list, func(i, j int) bool {
			if list[i].Score != list[j].Score {
				return list[i].Score > list[j].Score
			}
			return list[i].DescriptionLen > list[j].DescriptionLen
		})
		if len(list) > *perClass {
			list = list[:*perClass]
		}
		report.Examples = append(report.Examples, list...)
	}
	sort.Slice(report.Examples, func(i, j int) bool {
		if report.Examples[i].Classification != report.Examples[j].Classification {
			return report.Examples[i].Classification < report.Examples[j].Classification
		}
		return report.Examples[i].Score > report.Examples[j].Score
	})
	for code, count := range reasonCounts {
		report.TopReasons = append(report.TopReasons, CountedReason{Code: code, Count: count, Text: reasonText[code]})
	}
	for code, count := range blockerCounts {
		report.TopBlockers = append(report.TopBlockers, CountedReason{Code: code, Count: count, Text: blockerText[code]})
	}
	sort.Slice(report.TopBlockers, func(i, j int) bool { return report.TopBlockers[i].Count > report.TopBlockers[j].Count })
	sort.Slice(report.TopReasons, func(i, j int) bool { return report.TopReasons[i].Count > report.TopReasons[j].Count })
	if len(report.TopReasons) > 12 {
		report.TopReasons = report.TopReasons[:12]
	}

	// The contact investigation: for every opportunity that reaches strong match, what routes to
	// a real person already exist in collected data? Nothing here searches or invents.
	contactReports, err := db.StrongMatchContacts(ctx, strongIDs)
	if err != nil {
		return err
	}
	report.StrongMatchContacts = contactReports

	// §23: the automation question, over the whole corpus. Everything here is computed from
	// stored facts; nothing is composed, no AI is called and nothing is written.
	report.Automation = rehearseAutomation(ctx, db, document, chosen.ID, ids)

	// The same companies, taken through the live workflow the web UI uses. This is the half
	// that shows what the system actually does today, as opposed to what it would conclude if
	// the profile were active.
	service := assess.New(db)
	walked := map[int64]bool{}
	for _, e := range report.Examples {
		row, err := db.GetOpportunity(ctx, e.OpportunityID)
		if err != nil || walked[row.CompanyID] {
			continue
		}
		walked[row.CompanyID] = true
		walk := WorkflowWalk{CompanyID: row.CompanyID, Company: e.Company,
			OpportunityID: e.OpportunityID, Title: e.Title}
		proposal, err := service.Propose(ctx, row.CompanyID, 0, "")
		if err != nil {
			walk.Outcome = "error: " + err.Error()
			report.Walks = append(report.Walks, walk)
			continue
		}
		walk.RelevanceScore = proposal.View.Relevance.Score
		walk.StoredAssessment = proposal.Match != nil
		walk.Outcome, walk.Intent = string(proposal.Decision.Outcome), string(proposal.Decision.Intent)
		walk.RequiresReview = proposal.Decision.RequiresReview
		for _, b := range proposal.Decision.Blockers {
			walk.Blockers = append(walk.Blockers, b.Code+": "+b.Text)
		}
		for _, reason := range proposal.Decision.Reasons {
			walk.Reasons = append(walk.Reasons, reason.Code+": "+reason.Text)
		}
		if len(proposal.Recipients) > 0 {
			walk.TopRecipient = proposal.Recipients[0].Assessment.Contact.Value
			walk.RecipientReason = proposal.Recipients[0].Relevance + ": " + proposal.Recipients[0].Reason
		}
		for _, item := range proposal.Selection.Capabilities {
			walk.SelectedProof = append(walk.SelectedProof, "capability "+item.Name)
		}
		for _, item := range proposal.Selection.Projects {
			walk.SelectedProof = append(walk.SelectedProof, "project "+item.Name)
		}
		report.Walks = append(report.Walks, walk)
	}

	if *out != "" {
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out, raw, 0o600); err != nil {
			return err
		}
	}
	printSummary(report)
	return nil
}

func validateDocument(d profile.Document) error {
	if strings.TrimSpace(d.Identity) == "" {
		return errors.New("the chosen profile revision has no identity; nothing can be matched against it")
	}
	return nil
}

func companyName(ctx context.Context, db *store.DB, id int64) string {
	company, err := db.GetCompany(ctx, id)
	if err != nil {
		return fmt.Sprintf("company %d", id)
	}
	return company.Name
}

func printSummary(r Rehearsal) {
	fmt.Printf("Rehearsed profile revision %d (status %s, active in database: %v)\n",
		r.ProfileRevisionID, r.ProfileStatus, r.ProfileActive)
	fmt.Println(r.Note)
	fmt.Printf("\nAssessed %d opportunities.\n", r.Assessed)
	fmt.Println("\nClassification:")
	for _, class := range sortedCountKeys(r.Classification) {
		fmt.Printf("  %-16s %5d\n", class, r.Classification[class])
	}
	fmt.Println("\nStored description status:")
	for _, status := range sortedCountKeys(r.DescriptionStatus) {
		fmt.Printf("  %-16s %5d\n", status, r.DescriptionStatus[status])
	}
	fmt.Println("\nMost common matcher reasons:")
	for _, reason := range r.TopReasons {
		fmt.Printf("  %-34s %5d  %s\n", reason.Code, reason.Count, truncate(reason.Text, 70))
	}
	fmt.Println("\nMost common strong-match blockers:")
	for _, blocker := range r.TopBlockers {
		fmt.Printf("  %-36s %5d  %s\n", blocker.Code, blocker.Count, truncate(blocker.Text, 68))
	}
	fmt.Printf("\nPossible matches blocked only by a years-of-experience requirement: %d\n", r.BlockedOnlyByExperience)

	a := r.Automation
	fmt.Println("\nSelective-automation rehearsal (deterministic gates only, nothing composed or sent):")
	fmt.Printf("  considered                         %5d\n", a.Considered)
	fmt.Printf("  reach strong match                 %5d\n", a.StrongMatches)
	fmt.Printf("  survive recipient quality          %5d\n", a.WithContactableRecipient)
	fmt.Printf("  survive all deterministic gates    %5d\n", a.SurviveDeterministicGates)
	fmt.Printf("  still need composition and review  %5d\n", a.AwaitingComposition)
	fmt.Println("  what blocks the rest:")
	for _, code := range sortedCountKeys(a.Blockers) {
		fmt.Printf("    %-34s %5d\n", code, a.Blockers[code])
	}

	fmt.Println("\nContact situation at the strong-match companies:")
	for _, c := range r.StrongMatchContacts {
		fmt.Printf("\n  %s (company %d) — %s [opportunity %d]\n", c.Company, c.CompanyID, c.Title, c.OpportunityID)
		fmt.Printf("    domain: %s\n", orNone(c.Domain))
		fmt.Printf("    posting: %s\n", orNone(c.SourceURL))
		fmt.Printf("    published application URL:   %s\n", orNone(c.ApplicationURL))
		fmt.Printf("    published application email: %s\n", orNone(c.ApplicationEmail))
		if len(c.Contacts) == 0 {
			fmt.Println("    stored contacts: none")
		}
		for _, contact := range c.Contacts {
			mark := " "
			if contact.Acceptable {
				mark = "*"
			}
			fmt.Printf("    %s %-44s %-14s %-11s q%d %s\n", mark, contact.Address, contact.Function,
				contact.Status, contact.Quality, contact.Endpoint)
			if contact.Role != "" {
				fmt.Printf("        stored role: %s\n", contact.Role)
			}
		}
		if c.MeetsPolicy {
			fmt.Println("    MEETS current recipient policy")
		} else {
			fmt.Printf("    does NOT meet recipient policy: %s\n", c.PolicyGap)
		}
	}

	fmt.Println("\nLive workflow walk (what the stored pipeline answers today):")
	for _, wk := range r.Walks {
		fmt.Printf("\n  %s (company %d) — relevance %d, stored assessment: %v\n",
			wk.Company, wk.CompanyID, wk.RelevanceScore, wk.StoredAssessment)
		fmt.Printf("    intent outcome: %s%s, requires review: %v\n", wk.Outcome,
			optional(" -> ", wk.Intent), wk.RequiresReview)
		for _, b := range wk.Blockers {
			fmt.Printf("    BLOCKER: %s\n", truncate(b, 140))
		}
		for _, reason := range wk.Reasons {
			fmt.Printf("    reason:  %s\n", truncate(reason, 140))
		}
		if wk.TopRecipient != "" {
			fmt.Printf("    recipient: %s (%s)\n", wk.TopRecipient, truncate(wk.RecipientReason, 90))
		}
		for _, proof := range wk.SelectedProof {
			fmt.Printf("    proof:   %s\n", proof)
		}
	}

	fmt.Println("\nExamples:")
	for _, e := range r.Examples {
		fmt.Printf("\n  [%s score %d] %s — %s (opportunity %d, %d bytes)\n",
			e.Classification, e.Score, e.Title, e.Company, e.OpportunityID, e.DescriptionLen)
		if len(e.Matched) > 0 {
			fmt.Printf("    matched: %s\n", truncate(strings.Join(e.Matched, "; "), 150))
		}
		if len(e.Unmet) > 0 {
			fmt.Printf("    unmet:   %s\n", truncate(strings.Join(e.Unmet, "; "), 150))
		}
		if len(e.Unknown) > 0 {
			fmt.Printf("    unknown: %s\n", truncate(strings.Join(e.Unknown, "; "), 150))
		}
		for _, blocker := range e.Blockers {
			fmt.Printf("    BLOCKER: %s\n", truncate(blocker, 140))
		}
		for _, reason := range e.Reasons {
			fmt.Printf("    reason:  %s\n", truncate(reason, 140))
		}
	}
}

func sortedCountKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func optional(prefix, value string) string {
	if value == "" {
		return ""
	}
	return prefix + value
}

// rehearseAutomation walks every stored opportunity through the automation gates that can be
// decided from stored facts alone: the match, the vacancy's state, and the quality of the best
// available recipient. It deliberately stops short of composition — a real run still has to
// compose, validate and review before anything could be eligible — and says how many are in
// that position.
func rehearseAutomation(ctx context.Context, db *store.DB, document profile.Document, profileRevision int64, ids []int64) AutomationRehearsal {
	result := AutomationRehearsal{Blockers: map[string]int{}}
	for _, id := range ids {
		row, err := db.GetOpportunity(ctx, id)
		if err != nil {
			continue
		}
		result.Considered++
		match := opportunity.Assess(opportunity.AssessInput{
			OpportunityID: row.ID, OpportunityRevision: row.Revision, Facts: row.Facts,
			Profile: &document, ProfileRevisionID: profileRevision,
		})
		if match.Classification != opportunity.ClassificationStrong {
			result.Blockers["not_a_strong_match"]++
			continue
		}
		result.StrongMatches++

		if row.Facts.Availability == "closed" {
			result.Blockers["opportunity_closed"]++
			continue
		}
		if row.Facts.DescriptionStatus != "complete" {
			result.Blockers["description_not_complete"]++
			continue
		}

		// The best recipient this company actually has, judged exactly as the automation policy
		// would judge it.
		contacts, err := db.GetContactsByCompany(ctx, row.CompanyID)
		if err != nil {
			result.Blockers["recipient_lookup_failed"]++
			continue
		}
		blocker := bestRecipientBlocker(contacts)
		if blocker != "" {
			result.Blockers[blocker]++
			continue
		}
		result.WithContactableRecipient++
		result.SurviveDeterministicGates++
		// Surviving the deterministic gates is not eligibility. Every one of these still needs a
		// contact plan, a composition, clean deterministic validation and a clean semantic
		// review before the policy would say yes.
		result.AwaitingComposition++
	}
	return result
}

// bestRecipientBlocker returns the reason the best available contact would be refused, or empty
// when at least one contact would be accepted.
func bestRecipientBlocker(contacts []*store.Contact) string {
	if len(contacts) == 0 {
		return "no_contact_on_file"
	}
	blocker := "recipient_function_unknown"
	for _, c := range contacts {
		if c.Kind != "email" || c.Status == "bad" {
			continue
		}
		if !relevance.ClassifyEndpoint(c.Value).Contactable {
			if blocker == "recipient_function_unknown" {
				blocker = "recipient_not_a_human_mailbox"
			}
			continue
		}
		contact := relevance.Contact{ID: c.ID, Kind: c.Kind, Value: c.Value, Quality: c.Quality, Status: c.Status}
		if c.PersonName != nil {
			contact.Person = *c.PersonName
		}
		if c.Role != nil {
			contact.Role = *c.Role
		}
		switch relevance.ClassifyContact(contact).Function {
		case relevance.FunctionRecruiting, relevance.FunctionEngineering:
			return ""
		case relevance.FunctionGeneric:
			blocker = "recipient_generic_mailbox"
		}
	}
	return blocker
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none published)"
	}
	return value
}
