package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anthonyalando8/prospector/internal/profile"
)

type ProfileRevision struct {
	ID          int64
	Revision    int
	Document    profile.Document
	ContentHash string
	Status      string
	Active      bool
	CreatedAt   time.Time
}

// ActiveProfile never exposes a saved-but-unapproved draft as candidate evidence.
func (db *DB) ActiveProfile(ctx context.Context) (*ProfileRevision, error) {
	rows, err := db.ListProfileRevisions(ctx)
	if err != nil {
		return nil, err
	}
	for _, revision := range rows {
		if revision.Active && revision.Status == "approved" {
			return &revision, nil
		}
	}
	return nil, nil
}

func (db *DB) ListProfileRevisions(ctx context.Context) ([]ProfileRevision, error) {
	rows, err := db.Query(ctx, `SELECT r.id,r.revision,r.document,r.content_hash,r.status,COALESCE(p.active_revision_id=r.id,false),r.created_at FROM candidate_profile_revision r JOIN candidate_profile p ON p.id=r.profile_id ORDER BY r.revision DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProfileRevision{}
	for rows.Next() {
		var r ProfileRevision
		var raw []byte
		if err = rows.Scan(&r.ID, &r.Revision, &raw, &r.ContentHash, &r.Status, &r.Active, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Document, err = profile.Decode(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid stored profile revision %d: %w", r.ID, err)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// Saving never mutates an existing document, including an unapproved draft.
func (db *DB) CreateProfileDraft(ctx context.Context, raw []byte) (int64, error) {
	document, err := profile.Decode(raw)
	if err != nil {
		return 0, err
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return 0, err
	}
	hash := sha256.Sum256(canonical)
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM candidate_profile WHERE id=1 FOR UPDATE`); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO candidate_profile_revision(profile_id,revision,document,content_hash,status) SELECT 1,COALESCE(MAX(revision),0)+1,$1,$2,'draft' FROM candidate_profile_revision WHERE profile_id=1 RETURNING id`, canonical, hex.EncodeToString(hash[:])).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// Approval binds to the displayed immutable document and the observed head.
// Concurrent tabs cannot silently replace a newly activated profile.
func (db *DB) ActivateProfile(ctx context.Context, id int64, hash string, expectedActive int64) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var active int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(active_revision_id,0) FROM candidate_profile WHERE id=1 FOR UPDATE`).Scan(&active); err != nil {
		return err
	}
	if active != expectedActive {
		return fmt.Errorf("active profile changed; reload and review before approval")
	}
	var raw []byte
	var storedHash, status string
	if err = tx.QueryRow(ctx, `SELECT document,content_hash,status FROM candidate_profile_revision WHERE profile_id=1 AND id=$1`, id).Scan(&raw, &storedHash, &status); err != nil {
		return err
	}
	if hash == "" || hash != storedHash {
		return fmt.Errorf("profile content changed; reload before approval")
	}
	if _, err = profile.Decode(raw); err != nil {
		return err
	}
	if status == "draft" {
		if _, err = tx.Exec(ctx, `UPDATE candidate_profile_revision SET status='approved',approved_at=now() WHERE id=$1`, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE candidate_profile SET active_revision_id=$1 WHERE id=1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
