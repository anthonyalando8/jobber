package store

import "context"

// IsSuppressed reports whether an email address or domain has asked to stop being contacted.
func (db *DB) IsSuppressed(ctx context.Context, value string) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM suppression WHERE value = $1)`, value).Scan(&exists)
	return exists, err
}

// AddSuppression records a value (email or domain) that asked to stop.
func (db *DB) AddSuppression(ctx context.Context, value, reason string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO suppression (value, reason) VALUES ($1, $2) ON CONFLICT (value) DO NOTHING`,
		value, reason)
	return err
}
