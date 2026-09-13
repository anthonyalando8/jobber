package store

import (
	"context"
	"time"
)

// Contact represents an email or phone contact for a company
type Contact struct {
	ID         int64
	CompanyID  int64
	Kind       string // email|phone|whatsapp|form_url
	Value      string
	PersonName *string
	Role       *string
	Quality    int    // named 3 > role-based 2 > info@ 1
	Status     string // active|bad — "bad" means auto-pick (send-time recipient selection,
	// GetBestEmailContact) skips it, without losing the record
	BadReason *string
	BadAt     *time.Time
}

// CreateContact inserts a new contact, or on a duplicate (company_id, kind, value) refreshes its
// name/role/quality. status/bad_reason/bad_at are left untouched on conflict — a human's "this
// is dead" mark on an existing contact must survive re-enrichment finding the same address again.
func (db *DB) CreateContact(ctx context.Context, companyID int64, kind, value string, personName, role *string, quality int) (*Contact, error) {
	// Normalize and validate before anything is stored. A value that is not an address never
	// becomes an email contact, so the downstream checks are defence in depth rather than the
	// only thing standing between a scraped fragment and a recipient list.
	clean, err := NormalizeContactValue(kind, value)
	if err != nil {
		return nil, err
	}
	value = clean
	c := &Contact{CompanyID: companyID, Kind: kind, Value: value, PersonName: personName, Role: role, Quality: quality}

	err = db.QueryRow(ctx,
		`INSERT INTO contact (company_id, kind, value, person_name, role, quality)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (company_id, kind, value) DO UPDATE SET person_name = EXCLUDED.person_name, role = EXCLUDED.role, quality = EXCLUDED.quality
		 RETURNING id, status, bad_reason, bad_at`,
		companyID, kind, value, personName, role, quality).Scan(&c.ID, &c.Status, &c.BadReason, &c.BadAt)

	if err != nil {
		return nil, err
	}

	return c, nil
}

// GetContact fetches a single contact by id — used by the edit/delete/mark-bad handlers, which
// only get a contact id from the form and need the company id back for the redirect.
func (db *DB) GetContact(ctx context.Context, id int64) (*Contact, error) {
	var c Contact
	err := db.QueryRow(ctx,
		`SELECT id, company_id, kind, value, person_name, role, quality, status, bad_reason, bad_at
		 FROM contact WHERE id = $1`,
		id).Scan(&c.ID, &c.CompanyID, &c.Kind, &c.Value, &c.PersonName, &c.Role, &c.Quality, &c.Status, &c.BadReason, &c.BadAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetContactsByCompany retrieves all contacts for a company, active ones before bad ones within
// each kind so the good addresses surface first in the UI.
func (db *DB) GetContactsByCompany(ctx context.Context, companyID int64) ([]*Contact, error) {
	rows, err := db.Query(ctx,
		`SELECT id, company_id, kind, value, person_name, role, quality, status, bad_reason, bad_at
		 FROM contact WHERE company_id = $1 ORDER BY kind, (status = 'bad'), quality DESC`,
		companyID)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []*Contact
	for rows.Next() {
		var contact Contact
		err := rows.Scan(
			&contact.ID, &contact.CompanyID, &contact.Kind, &contact.Value,
			&contact.PersonName, &contact.Role, &contact.Quality, &contact.Status, &contact.BadReason, &contact.BadAt)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, &contact)
	}

	return contacts, rows.Err()
}

// GetBestEmailContact returns the highest quality *active* email for a company — a contact
// marked bad (dead mailbox) is never auto-picked, even if it's the highest quality one on file.
func (db *DB) GetBestEmailContact(ctx context.Context, companyID int64) (*Contact, error) {
	var contact Contact

	err := db.QueryRow(ctx,
		`SELECT id, company_id, kind, value, person_name, role, quality, status, bad_reason, bad_at
		 FROM contact WHERE company_id = $1 AND kind = 'email' AND status = 'active'
		 ORDER BY quality DESC LIMIT 1`,
		companyID).Scan(
		&contact.ID, &contact.CompanyID, &contact.Kind, &contact.Value,
		&contact.PersonName, &contact.Role, &contact.Quality, &contact.Status, &contact.BadReason, &contact.BadAt)

	if err != nil {
		return nil, err
	}

	return &contact, nil
}

// UpdateContact edits a contact's value/name/role — a human's fix for what enrichment got wrong
// (a dead email swapped for a working one, a name/role correction). Kind and company aren't
// editable here; delete and re-add for those.
func (db *DB) UpdateContact(ctx context.Context, id int64, value string, personName, role *string) error {
	_, err := db.Exec(ctx,
		`UPDATE contact SET value = $2, person_name = $3, role = $4 WHERE id = $1`,
		id, value, personName, role)
	return err
}

// DeleteContact removes a contact outright — for a duplicate or a completely wrong entry, as
// opposed to MarkContactBad which keeps the record but stops it being auto-picked.
func (db *DB) DeleteContact(ctx context.Context, id int64) error {
	_, err := db.Exec(ctx, `DELETE FROM contact WHERE id = $1`, id)
	return err
}

// MarkContactBad flags a contact as dead — a bounce noticed after the fact, or an SMTP
// rejection at send time — so GetBestEmailContact and executeSend's auto-pick both skip it, but
// the record and the reason stay visible rather than being deleted outright.
func (db *DB) MarkContactBad(ctx context.Context, id int64, reason string) error {
	_, err := db.Exec(ctx,
		`UPDATE contact SET status = 'bad', bad_reason = $2, bad_at = now() WHERE id = $1`,
		id, reason)
	return err
}

// ReactivateContact undoes MarkContactBad — for when a contact was flagged in error.
func (db *DB) ReactivateContact(ctx context.Context, id int64) error {
	_, err := db.Exec(ctx,
		`UPDATE contact SET status = 'active', bad_reason = NULL, bad_at = NULL WHERE id = $1`,
		id)
	return err
}

// Signal represents evidence about a company
type Signal struct {
	ID        int64
	CompanyID int64
	Kind      string
	Detail    string
	URL       *string
	CreatedAt time.Time
}

// CreateSignal inserts a new signal
func (db *DB) CreateSignal(ctx context.Context, companyID int64, kind, detail string, url *string) (*Signal, error) {
	var id int64
	var createdAt time.Time

	err := db.QueryRow(ctx,
		`INSERT INTO signal (company_id, kind, detail, url)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, created_at`,
		companyID, kind, detail, url).Scan(&id, &createdAt)

	if err != nil {
		return nil, err
	}

	return &Signal{
		ID:        id,
		CompanyID: companyID,
		Kind:      kind,
		Detail:    detail,
		URL:       url,
		CreatedAt: createdAt,
	}, nil
}

// GetSignalsByCompany retrieves all signals for a company
func (db *DB) GetSignalsByCompany(ctx context.Context, companyID int64) ([]*Signal, error) {
	rows, err := db.Query(ctx,
		`SELECT id, company_id, kind, detail, url, created_at
		 FROM signal WHERE company_id = $1 ORDER BY kind, created_at DESC`,
		companyID)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var signals []*Signal
	for rows.Next() {
		var signal Signal
		err := rows.Scan(
			&signal.ID, &signal.CompanyID, &signal.Kind, &signal.Detail,
			&signal.URL, &signal.CreatedAt)
		if err != nil {
			return nil, err
		}
		signals = append(signals, &signal)
	}

	return signals, rows.Err()
}
