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

// GetTemplateByID fetches a single template row for editing.
func (db *DB) GetTemplateByID(ctx context.Context, id int64) (*Template, error) {
	var t Template
	err := db.QueryRow(ctx,
		`SELECT id, segment, channel, role_filter, subject, body, display_only
		 FROM template WHERE id = $1`,
		id).Scan(&t.ID, &t.Segment, &t.Channel, &t.RoleFilter, &t.Subject, &t.Body, &t.DisplayOnly)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListTemplates returns every template row, ordered for a stable, readable editor listing —
// grouped by segment, real send-eligible templates before display-only ones within a segment.
func (db *DB) ListTemplates(ctx context.Context) ([]*Template, error) {
	rows, err := db.Query(ctx,
		`SELECT id, segment, channel, role_filter, subject, body, display_only
		 FROM template
		 ORDER BY segment, display_only, channel, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var templates []*Template
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.Segment, &t.Channel, &t.RoleFilter, &t.Subject, &t.Body, &t.DisplayOnly); err != nil {
			return nil, err
		}
		templates = append(templates, &t)
	}
	return templates, rows.Err()
}

// UpdateTemplate saves edited subject/body for one template row — takes effect on the very next
// draft (GetTemplate reads straight from this table, no caching), no restart needed. Kind and
// segment aren't editable here since they're what select which template a company gets; only the
// copy itself changes.
func (db *DB) UpdateTemplate(ctx context.Context, id int64, subject *string, body string) error {
	_, err := db.Exec(ctx,
		`UPDATE template SET subject = $2, body = $3 WHERE id = $1`,
		id, subject, body)
	return err
}
