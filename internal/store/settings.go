package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Operator settings are durable switches a person flips from the dashboard: pausing automatic
// sending, turning automatic drafting on, pausing triage. They live in the database so they hold
// across restarts and apply to every worker at its next check, without a deploy or a code change.
const (
	// SettingAutoSend is "on" when the operator has turned automatic sending on. The deployment
	// must also permit it (EMAIL_PIPELINE_MODE=v2_auto and AUTO_SEND_INTENTS).
	SettingAutoSend = "sending.auto"
	// SettingAutoSendSince is when automatic sending was last turned on. Only drafts reviewed
	// after that moment are considered, so turning it on never sends a historical backlog.
	SettingAutoSendSince = "sending.auto_since"
	// SettingSendingPaused stops every automatic send immediately. Discovery, research, triage and
	// drafting keep running.
	SettingSendingPaused = "sending.paused"
	// SettingAutoCompose is "on" when the outreach worker may create plans and drafts by itself.
	SettingAutoCompose = "outreach.auto_compose"
	// SettingTriagePaused stops the triage worker without stopping anything else.
	SettingTriagePaused = "triage.paused"
	// SettingTriageSuppress is "on" when triage may take high-confidence unrelated postings out of
	// the list. Off, it only records what it would do, so the rules can be checked first.
	SettingTriageSuppress = "triage.suppress"
	// SettingAddressAI is "on" when waiting contact addresses may get an AI opinion in the
	// background. The free deterministic check runs regardless.
	SettingAddressAI = "contacts.ai_check"
)

// Setting returns a setting's value, or fallback when it has never been set.
func (db *DB) Setting(ctx context.Context, key, fallback string) (string, error) {
	var value string
	err := db.QueryRow(ctx, `SELECT value FROM operator_setting WHERE key=$1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return fallback, nil
	}
	return value, err
}

// SettingOn reports whether a boolean setting is "on". Anything unreadable counts as off, which
// for every switch here is the safe side.
func (db *DB) SettingOn(ctx context.Context, key string) bool {
	value, err := db.Setting(ctx, key, "off")
	return err == nil && value == "on"
}

// SetSetting records a setting and who changed it.
func (db *DB) SetSetting(ctx context.Context, key, value, by string) error {
	if strings.TrimSpace(by) == "" {
		return fmt.Errorf("a setting change must record who made it")
	}
	_, err := db.Exec(ctx, `INSERT INTO operator_setting(key,value,updated_by) VALUES($1,$2,$3)
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_by=EXCLUDED.updated_by, updated_at=now()`, key, value, by)
	return err
}
