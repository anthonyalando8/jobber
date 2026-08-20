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
	Quality    int // named 3 > role-based 2 > info@ 1
}

// CreateContact inserts a new contact
func (db *DB) CreateContact(ctx context.Context, companyID int64, kind, value string, personName, role *string, quality int) (*Contact, error) {
	var id int64

	err := db.QueryRow(ctx,
		`INSERT INTO contact (company_id, kind, value, person_name, role, quality)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (company_id, kind, value) DO UPDATE SET person_name = EXCLUDED.person_name, role = EXCLUDED.role, quality = EXCLUDED.quality
		 RETURNING id`,
		companyID, kind, value, personName, role, quality).Scan(&id)

	if err != nil {
		return nil, err
	}

	return &Contact{
		ID:         id,
		CompanyID:  companyID,
		Kind:       kind,
		Value:      value,
		PersonName: personName,
		Role:       role,
		Quality:    quality,
	}, nil
}

// GetContactsByCompany retrieves all contacts for a company
func (db *DB) GetContactsByCompany(ctx context.Context, companyID int64) ([]*Contact, error) {
	rows, err := db.Query(ctx,
		`SELECT id, company_id, kind, value, person_name, role, quality
		 FROM contact WHERE company_id = $1 ORDER BY kind, quality DESC`,
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
			&contact.PersonName, &contact.Role, &contact.Quality)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, &contact)
	}

	return contacts, rows.Err()
}

// GetBestEmailContact returns the highest quality email for a company
func (db *DB) GetBestEmailContact(ctx context.Context, companyID int64) (*Contact, error) {
	var contact Contact

	err := db.QueryRow(ctx,
		`SELECT id, company_id, kind, value, person_name, role, quality
		 FROM contact WHERE company_id = $1 AND kind = 'email'
		 ORDER BY quality DESC LIMIT 1`,
		companyID).Scan(
		&contact.ID, &contact.CompanyID, &contact.Kind, &contact.Value,
		&contact.PersonName, &contact.Role, &contact.Quality)

	if err != nil {
		return nil, err
	}

	return &contact, nil
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
