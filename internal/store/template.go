package store

import "context"

// Template is one row of literal outreach copy the draft package fills with
// {{observation}}/{{hypothesis}}/etc. See OUTREACH_TEMPLATES.md for the source text.
type Template struct {
	ID          int64
	Segment     string
	Channel     string
	RoleFilter  *string
	Subject     *string
	Body        string
	DisplayOnly bool
}

// GetTemplate returns the send-eligible (non display-only) template for a segment+channel pair.
// When a segment has more than one email variant (e.g. incubator has "programme manager" and
// "founder direct"), the lowest id wins — there's no signal yet to pick between them.
func (db *DB) GetTemplate(ctx context.Context, segment, channel string) (*Template, error) {
	var t Template
	err := db.QueryRow(ctx,
		`SELECT id, segment, channel, role_filter, subject, body, display_only
		 FROM template
		 WHERE segment = $1 AND channel = $2 AND display_only = false
		 ORDER BY id ASC LIMIT 1`,
		segment, channel).Scan(&t.ID, &t.Segment, &t.Channel, &t.RoleFilter, &t.Subject, &t.Body, &t.DisplayOnly)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
