package store

import (
	"context"
	"fmt"

	"github.com/anthonyalando8/prospector/internal/relevance"
)

// NonContactEndpoint is one stored contact that is not a mailbox for humans.
type NonContactEndpoint struct {
	ContactID   int64
	CompanyID   int64
	CompanyName string
	Address     string
	Status      string
	Purpose     string
	Evidence    string
}

// FindNonContactEndpoints scans stored contacts for addresses that are machine endpoints rather
// than places a person can be written to: error-ingestion keys, no-reply mailboxes, ticketing
// and alerting integrations. Phase 6 found one of these on live data — a Sentry ingest key
// scraped from a page and stored as a company contact.
//
// It only reads. Deciding what to do about a historical contact is a separate, explicit act.
func (db *DB) FindNonContactEndpoints(ctx context.Context, includeAlreadyBad bool) ([]NonContactEndpoint, error) {
	filter := ` AND c.status <> 'bad'`
	if includeAlreadyBad {
		filter = ""
	}
	rows, err := db.Query(ctx,
		`SELECT c.id, c.company_id, co.name, c.value, c.status
		   FROM contact c JOIN company co ON co.id = c.company_id
		  WHERE c.kind = 'email'`+filter+`
		  ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := []NonContactEndpoint{}
	for rows.Next() {
		var e NonContactEndpoint
		if err := rows.Scan(&e.ContactID, &e.CompanyID, &e.CompanyName, &e.Address, &e.Status); err != nil {
			return nil, err
		}
		assessment := relevance.ClassifyEndpoint(e.Address)
		if assessment.Contactable {
			continue
		}
		e.Purpose, e.Evidence = assessment.Purpose, assessment.Evidence
		found = append(found, e)
	}
	return found, rows.Err()
}

// MarkNonContactEndpoints marks machine endpoints unusable, with the reason that identified
// them. It never deletes a contact: the row is part of what research found, and removing it
// would only mean the next crawl rediscovers it with no memory of the judgement.
//
// The contact's status becomes 'bad', which every downstream gate already respects — recipient
// ranking, the send preflight and the automation policy all refuse a bad contact.
func (db *DB) MarkNonContactEndpoints(ctx context.Context, dryRun bool) ([]NonContactEndpoint, error) {
	found, err := db.FindNonContactEndpoints(ctx, false)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return found, nil
	}
	for _, e := range found {
		reason := fmt.Sprintf("not a human mailbox (%s): %s", e.Purpose, e.Evidence)
		if err := db.MarkContactBad(ctx, e.ContactID, reason); err != nil {
			return found, err
		}
	}
	return found, nil
}
