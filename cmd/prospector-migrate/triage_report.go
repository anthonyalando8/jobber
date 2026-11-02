package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/anthonyalando8/prospector/internal/store"
	"github.com/anthonyalando8/prospector/internal/triage"
)

// triageReportVersion never matches a stored triage row, so every live unrelated posting is
// evaluated afresh. Nothing is written.
const triageReportVersion = "triage-report-dry-run"

type triageBucket struct {
	Outcome    string         `json:"outcome"`
	Confidence string         `json:"confidence"`
	Count      int            `json:"count"`
	BySource   map[string]int `json:"by_source"`
	Examples   []string       `json:"examples"`
}

type triageReportResult struct {
	Unrelated int            `json:"unrelated_live_postings"`
	Buckets   []triageBucket `json:"buckets"`
}

func triageReport(ctx context.Context, db *store.DB) (triageReportResult, error) {
	out := triageReportResult{}
	active, err := db.ActiveProfile(ctx)
	if err != nil || active == nil {
		return out, fmt.Errorf("an active candidate profile is required: %v", err)
	}
	doc := active.Document
	candidates, err := db.TriageCandidates(ctx, triageReportVersion, false, 100000)
	if err != nil {
		return out, err
	}
	buckets := map[string]*triageBucket{}
	for _, c := range candidates {
		d := triage.Decide(triage.Input{Title: c.Title, Description: c.Description, DescriptionStatus: c.DescriptionStatus,
			Source: c.Source, Match: c.Match, Profile: &doc})
		key := d.Outcome + "/" + d.Confidence
		b := buckets[key]
		if b == nil {
			b = &triageBucket{Outcome: d.Outcome, Confidence: d.Confidence, BySource: map[string]int{}}
			buckets[key] = b
		}
		b.Count++
		b.BySource[c.Source]++
		if len(b.Examples) < 12 {
			b.Examples = append(b.Examples, fmt.Sprintf("#%d %s [%s]", c.OpportunityID, c.Title, c.Source))
		}
		out.Unrelated++
	}
	for _, b := range buckets {
		out.Buckets = append(out.Buckets, *b)
	}
	sort.Slice(out.Buckets, func(i, j int) bool { return out.Buckets[i].Count > out.Buckets[j].Count })
	return out, nil
}

func printTriageReport(r triageReportResult) {
	fmt.Printf("%d live postings are classified unrelated under the current matcher and profile.\n", r.Unrelated)
	labels := map[string]string{
		"suppressed/high":            "would be hidden (high confidence)",
		"confirmed_unrelated/medium": "would stay visible as unrelated (medium)",
		"manual_review/low":          "would wait for a person (contradictory evidence)",
	}
	for _, b := range r.Buckets {
		label := labels[b.Outcome+"/"+b.Confidence]
		if label == "" {
			label = b.Outcome + "/" + b.Confidence
		}
		fmt.Printf("\n%d %s\n", b.Count, label)
		sources := make([]string, 0, len(b.BySource))
		for s := range b.BySource {
			sources = append(sources, s)
		}
		sort.Slice(sources, func(i, j int) bool { return b.BySource[sources[i]] > b.BySource[sources[j]] })
		for _, s := range sources {
			fmt.Printf("    %-24s %d\n", s, b.BySource[s])
		}
		fmt.Println("  examples:")
		for _, e := range b.Examples {
			fmt.Println("    " + e)
		}
	}
	fmt.Println("\nNothing was changed. Turn triage suppression on from the dashboard when the 'would be hidden' examples look right.")
}
