package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/anthonyalando8/prospector/internal/relevance"
)

// StrongMatchContactReport is one company that reached a strong match, with everything already
// known about how it could legitimately be contacted. It answers a narrow question: does a real
// recruiting, engineering or published application route exist in data already collected?
//
// It finds nothing new and guesses nothing. In particular it never derives a person's name from
// a mailbox local part, and it never promotes a generic, telemetry or no-reply address to a
// recruiting contact.
type StrongMatchContactReport struct {
	CompanyID     int64  `json:"company_id"`
	Company       string `json:"company"`
	Domain        string `json:"domain"`
	OpportunityID int64  `json:"opportunity_id"`
	Title         string `json:"title"`
	SourceURL     string `json:"source_url,omitempty"`
	// ApplicationURL and ApplicationEmail are what the posting itself published, if anything.
	ApplicationURL   string `json:"application_url,omitempty"`
	ApplicationEmail string `json:"application_email,omitempty"`
	// Contacts is every stored contact for the company, classified.
	Contacts []ContactCandidate `json:"contacts"`
	// MeetsPolicy is true when at least one route satisfies the current recipient rules.
	MeetsPolicy bool   `json:"meets_recipient_policy"`
	PolicyGap   string `json:"policy_gap,omitempty"`
}

// ContactCandidate is one stored contact judged by the same rules the send path uses.
type ContactCandidate struct {
	ContactID   int64  `json:"contact_id"`
	Address     string `json:"address"`
	PersonName  string `json:"person_name,omitempty"`
	Role        string `json:"role,omitempty"`
	Quality     int    `json:"quality"`
	Status      string `json:"status"`
	Function    string `json:"function"`
	Evidence    string `json:"function_evidence"`
	Contactable bool   `json:"is_human_mailbox"`
	Endpoint    string `json:"endpoint_purpose"`
	// Acceptable means this contact would satisfy the recipient rules for a job application.
	Acceptable bool `json:"acceptable_for_application"`
}

// StrongMatchContacts reports the contact situation for the given opportunities. The caller
// supplies the opportunity ids, because which ones are strong matches is the matcher's answer,
// not this function's.
func (db *DB) StrongMatchContacts(ctx context.Context, opportunityIDs []int64) ([]StrongMatchContactReport, error) {
	reports := []StrongMatchContactReport{}
	for _, id := range opportunityIDs {
		row, err := db.GetOpportunity(ctx, id)
		if err != nil {
			return nil, err
		}
		company, err := db.GetCompany(ctx, row.CompanyID)
		if err != nil {
			return nil, err
		}
		report := StrongMatchContactReport{
			CompanyID: company.ID, Company: company.Name, Domain: company.Domain,
			OpportunityID: row.ID, Title: row.Facts.Title, SourceURL: row.Facts.URL,
		}
		// Only what the source itself published counts as an application route. A guessed
		// address would be exactly the invention this investigation is meant to avoid.
		report.ApplicationURL = firstNonEmpty(row.Facts.Metadata, "apply_url", "application_url", "applyUrl")
		report.ApplicationEmail = firstNonEmpty(row.Facts.Metadata, "apply_email", "application_email", "applyEmail")

		contacts, err := db.GetContactsByCompany(ctx, company.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range contacts {
			candidate := ContactCandidate{ContactID: c.ID, Address: c.Value,
				Quality: c.Quality, Status: c.Status}
			if c.PersonName != nil {
				candidate.PersonName = *c.PersonName
			}
			if c.Role != nil {
				candidate.Role = *c.Role
			}
			endpoint := relevance.ClassifyEndpoint(c.Value)
			candidate.Contactable, candidate.Endpoint = endpoint.Contactable, endpoint.Purpose
			assessment := relevance.ClassifyContact(relevance.Contact{
				ID: c.ID, Kind: c.Kind, Value: c.Value, Person: candidate.PersonName,
				Role: candidate.Role, Quality: c.Quality, Status: c.Status,
			})
			candidate.Function, candidate.Evidence = assessment.Function, assessment.Evidence
			candidate.Acceptable = c.Kind == "email" && c.Status != "bad" && endpoint.Contactable &&
				(assessment.Function == relevance.FunctionRecruiting ||
					assessment.Function == relevance.FunctionEngineering)
			report.Contacts = append(report.Contacts, candidate)
			if candidate.Acceptable {
				report.MeetsPolicy = true
			}
		}
		sort.SliceStable(report.Contacts, func(i, j int) bool {
			if report.Contacts[i].Acceptable != report.Contacts[j].Acceptable {
				return report.Contacts[i].Acceptable
			}
			return report.Contacts[i].Quality > report.Contacts[j].Quality
		})

		// A published application address counts as an acceptable route on its own, provided it
		// is a mailbox a person reads.
		if !report.MeetsPolicy && report.ApplicationEmail != "" &&
			relevance.ClassifyEndpoint(report.ApplicationEmail).Contactable {
			report.MeetsPolicy = true
		}
		if !report.MeetsPolicy {
			report.PolicyGap = describeGap(report)
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func describeGap(r StrongMatchContactReport) string {
	if len(r.Contacts) == 0 {
		return "no contact of any kind is stored for this company"
	}
	usable := 0
	generic := 0
	machine := 0
	for _, c := range r.Contacts {
		if c.Status == "bad" {
			continue
		}
		if !c.Contactable {
			machine++
			continue
		}
		usable++
		if c.Function == relevance.FunctionGeneric {
			generic++
		}
	}
	switch {
	case usable == 0 && machine > 0:
		return "every stored address is a machine endpoint, not a mailbox a person reads"
	case usable == 0:
		return "every stored contact is marked bad or unusable"
	case generic == usable:
		return "only generic mailboxes are stored; no recruiting or engineering contact is identified"
	default:
		return "no stored contact has an identified recruiting or engineering function"
	}
}

// firstNonEmpty reads the first present key from stored source metadata. Source metadata is
// untrusted text, so the value is trimmed and returned as-is rather than interpreted.
func firstNonEmpty(metadata map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			return value
		}
	}
	return ""
}

// RawOpportunityMetadata returns the stored source metadata for one opportunity, so an operator
// can see exactly what the job board published without the application having interpreted it.
func (db *DB) RawOpportunityMetadata(ctx context.Context, opportunityID int64) (map[string]string, error) {
	row, err := db.GetOpportunity(ctx, opportunityID)
	if err != nil {
		return nil, err
	}
	if row.Facts.Metadata == nil {
		return map[string]string{}, nil
	}
	return row.Facts.Metadata, nil
}

// MarshalIndent is a small convenience so callers can print a report without importing encoding/json.
func (r StrongMatchContactReport) MarshalIndent() string {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw)
}
