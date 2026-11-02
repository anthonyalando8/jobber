package triage

import (
	"context"
	"errors"
	"log"

	"github.com/anthonyalando8/prospector/internal/explain"
	"github.com/anthonyalando8/prospector/internal/profile"
	"github.com/anthonyalando8/prospector/internal/store"
)

// Actor is recorded as the decider of every triage conclusion and suppression.
const Actor = "triage:" + Version

// Result counts what one batch did.
type Result struct {
	Considered, Suppressed, WouldSuppress, Confirmed, ManualReview, Kept, Failed int
	Paused                                                                       bool
}

// Run triages one bounded batch. It reads the operator's switches each time, so pausing triage
// or turning suppression on takes effect at the next batch without a restart. One opportunity
// failing is recorded and skipped; it never stops the batch.
func Run(ctx context.Context, db *store.DB, limit int) (Result, error) {
	res := Result{}
	if db.SettingOn(ctx, store.SettingTriagePaused) {
		res.Paused = true
		return res, nil
	}
	suppressOn := db.SettingOn(ctx, store.SettingTriageSuppress)
	active, err := db.ActiveProfile(ctx)
	if err != nil {
		return res, err
	}
	if active == nil {
		return res, nil // nothing is assessed without an active profile, so nothing to triage
	}
	doc := active.Document
	candidates, err := db.TriageCandidates(ctx, Version, suppressOn, limit)
	if err != nil {
		return res, err
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		res.Considered++
		record, err := triageOne(ctx, db, c, &doc, suppressOn)
		if err != nil {
			record = store.TriageRecord{Outcome: Failed, Confidence: "none", DecidedBy: Actor, Error: err.Error(),
				Reasons: explain.Reasons{}.Add("triage_failed", "Triage could not complete for this posting: "+err.Error())}
			log.Printf("triage: opportunity %d: %v", c.OpportunityID, err)
		}
		if err := db.RecordTriage(ctx, c, Version, record); err != nil {
			log.Printf("triage: recording opportunity %d: %v", c.OpportunityID, err)
			continue
		}
		switch {
		case record.Outcome == Failed:
			res.Failed++
		case record.Outcome == Suppress:
			res.Suppressed++
		case record.Outcome == ManualReview:
			res.ManualReview++
		case record.Outcome == Kept:
			res.Kept++
		case record.Confidence == High:
			res.WouldSuppress++
		default:
			res.Confirmed++
		}
	}
	return res, nil
}

func triageOne(ctx context.Context, db *store.DB, c store.TriageCandidate, doc *profile.Document, suppressOn bool) (store.TriageRecord, error) {
	reversed, err := db.TriageSuppressionReversed(ctx, c.OpportunityID, c.Revision)
	if err != nil {
		return store.TriageRecord{}, err
	}
	d := Decide(Input{Title: c.Title, Description: c.Description, DescriptionStatus: c.DescriptionStatus, Source: c.Source,
		Match: c.Match, Profile: doc, ReversedByOperator: reversed})
	record := store.TriageRecord{Outcome: d.Outcome, Confidence: d.Confidence, Reasons: d.Reasons, DecidedBy: Actor}
	if d.Outcome != Suppress {
		return record, nil
	}
	if !suppressOn {
		// Recorded as what triage would do, so it can be checked before suppression is turned on.
		record.Outcome = ConfirmedUnrelated
		record.Reasons = record.Reasons.Add("suppression_off",
			"Triage would take this out of the list, but suppression is off. It stays visible until an operator turns suppression on.")
		return record, nil
	}
	evidence := map[string]any{"triage_version": Version, "reasons": d.Reasons, "matcher_version": c.Match.MatcherVersion}
	_, err = db.SuppressOpportunity(ctx, c.OpportunityID, store.SuppressNotRelevant, store.BasisTriage, "", Actor, evidence)
	if err != nil && !errors.Is(err, store.ErrAlreadySuppressed) {
		return store.TriageRecord{}, err
	}
	return record, nil
}
