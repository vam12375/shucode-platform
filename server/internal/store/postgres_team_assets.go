package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const teamAssetCols = `id::text, name, kind, mime, size, created_at, uploaded_by_id::text, uploaded_by_name, sha256, blob_id::text`

func scanTeamAsset(row pgx.Row) (*TeamAssetRecord, error) {
	var record TeamAssetRecord
	err := row.Scan(
		&record.ID, &record.Name, &record.Kind, &record.Mime, &record.Size,
		&record.CreatedAt, &record.UploadedByID, &record.UploadedByName,
		&record.SHA256, &record.BlobID,
	)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *PostgresStore) CommitTeamAsset(ctx context.Context, teamID string, record TeamAssetRecord, limitBytes int64) (TeamAssetRecord, bool, error) {
	return s.commitTeamAsset(ctx, teamID, record, limitBytes, false)
}

func (s *PostgresStore) CommitTeamAssetForActiveMember(ctx context.Context, teamID string, record TeamAssetRecord, limitBytes int64) (TeamAssetRecord, bool, error) {
	return s.commitTeamAsset(ctx, teamID, record, limitBytes, true)
}

func (s *PostgresStore) commitTeamAsset(ctx context.Context, teamID string, record TeamAssetRecord, limitBytes int64, activeMember bool) (TeamAssetRecord, bool, error) {
	if err := validateTeamAssetRecord(teamID, record, limitBytes); err != nil {
		return TeamAssetRecord{}, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TeamAssetRecord{}, false, err
	}
	defer tx.Rollback(ctx)

	var lockedTeamID string
	var used int64
	if err := tx.QueryRow(ctx, `SELECT id::text, team_asset_used_bytes FROM teams WHERE id=$1::uuid FOR UPDATE`, teamID).Scan(&lockedTeamID, &used); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TeamAssetRecord{}, false, ErrTeamAssetTeamNotFound
		}
		return TeamAssetRecord{}, false, err
	}
	if activeMember {
		// 团队锁串行成员变更，用户共享锁串行账号停用；不能沿用后台启动时的权限。
		err := tx.QueryRow(ctx, `SELECT u.display_name FROM team_members m JOIN users u ON u.id=m.user_id JOIN teams t ON t.id=m.team_id WHERE m.team_id=$1::uuid AND m.user_id=$2::uuid AND m.disabled_at IS NULL AND u.disabled_at IS NULL AND t.disabled_at IS NULL FOR SHARE OF m,u`, teamID, record.UploadedByID).Scan(&record.UploadedByName)
		if errors.Is(err, pgx.ErrNoRows) {
			return TeamAssetRecord{}, false, ErrTeamAssetForbidden
		}
		if err != nil {
			return TeamAssetRecord{}, false, err
		}
	}
	// 墓碑与清理事实永久保留，迟到重试不能重新引用已批准删除的身份或字节。
	var retired bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM team_asset_deletion_requests WHERE team_id=$1::uuid AND status='approved' AND (asset_id=$2::uuid OR blob_id=$3::uuid))`, teamID, record.ID, record.BlobID).Scan(&retired); err != nil {
		return TeamAssetRecord{}, false, err
	}
	if retired {
		return TeamAssetRecord{}, false, ErrTeamAssetConflict
	}

	existing, err := scanTeamAsset(tx.QueryRow(ctx, `SELECT `+teamAssetCols+` FROM team_assets WHERE team_id=$1::uuid AND id=$2::uuid`, teamID, record.ID))
	if err == nil {
		if !sameTeamAssetUpload(*existing, record) {
			return TeamAssetRecord{}, false, ErrTeamAssetConflict
		}
		return *existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TeamAssetRecord{}, false, err
	}
	var existingBlobID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM team_assets WHERE team_id=$1::uuid AND blob_id=$2::uuid`, teamID, record.BlobID).Scan(&existingBlobID)
	if err == nil {
		return TeamAssetRecord{}, false, ErrTeamAssetConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TeamAssetRecord{}, false, err
	}

	if used > limitBytes || record.Size > limitBytes-used {
		return TeamAssetRecord{}, false, ErrTeamAssetStorageLimit
	}

	saved, err := scanTeamAsset(tx.QueryRow(ctx, `
		INSERT INTO team_assets (team_id, id, name, kind, mime, size, created_at, uploaded_by_id, uploaded_by_name, sha256, blob_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8::uuid, $9, $10, $11::uuid)
		RETURNING `+teamAssetCols,
		teamID, record.ID, record.Name, record.Kind, record.Mime, record.Size,
		record.CreatedAt, record.UploadedByID, record.UploadedByName, record.SHA256, record.BlobID,
	))
	if err != nil {
		return TeamAssetRecord{}, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE teams SET team_asset_used_bytes=$2 WHERE id=$1::uuid`, teamID, used+record.Size); err != nil {
		return TeamAssetRecord{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TeamAssetRecord{}, false, err
	}
	return *saved, true, nil
}

func (s *PostgresStore) IsTeamAssetDeleted(ctx context.Context, teamID, id string) (bool, error) {
	if !canonicalUUID(teamID) || !canonicalUUID(id) {
		return false, ErrInvalidInput
	}
	var deleted bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM team_asset_deletion_requests WHERE team_id=$1::uuid AND asset_id=$2::uuid AND status='approved')`, teamID, id).Scan(&deleted)
	return deleted, err
}

func (s *PostgresStore) GetTeamAsset(ctx context.Context, teamID, id string) (*TeamAssetRecord, error) {
	if !canonicalUUID(teamID) || !canonicalUUID(id) {
		return nil, ErrInvalidInput
	}
	var exists string
	err := s.pool.QueryRow(ctx, `SELECT id::text FROM teams WHERE id=$1::uuid`, teamID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTeamAssetTeamNotFound
	}
	if err != nil {
		return nil, err
	}
	record, err := scanTeamAsset(s.pool.QueryRow(ctx, `SELECT `+teamAssetCols+` FROM team_assets WHERE team_id=$1::uuid AND id=$2::uuid`, teamID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

func (s *PostgresStore) ListTeamAssetPage(ctx context.Context, teamID string, beforeCreatedAt *time.Time, beforeID string, limit int) (TeamAssetPage, error) {
	if !canonicalUUID(teamID) || limit < 1 || (beforeCreatedAt == nil && beforeID != "") ||
		(beforeCreatedAt != nil && !canonicalUUID(beforeID)) {
		return TeamAssetPage{}, ErrInvalidInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return TeamAssetPage{}, err
	}
	defer tx.Rollback(ctx)

	var page TeamAssetPage
	var exists string
	if err := tx.QueryRow(ctx, `SELECT id::text, team_asset_used_bytes FROM teams WHERE id=$1::uuid`, teamID).Scan(&exists, &page.UsedBytes); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TeamAssetPage{}, ErrTeamAssetTeamNotFound
		}
		return TeamAssetPage{}, err
	}
	query := `SELECT ` + teamAssetCols + ` FROM team_assets WHERE team_id=$1::uuid ORDER BY created_at DESC, id DESC LIMIT $2`
	args := []any{teamID, limit}
	if beforeCreatedAt != nil {
		query = `SELECT ` + teamAssetCols + ` FROM team_assets WHERE team_id=$1::uuid AND (created_at, id) < ($2::timestamptz, $3::uuid) ORDER BY created_at DESC, id DESC LIMIT $4`
		args = []any{teamID, *beforeCreatedAt, beforeID, limit}
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return TeamAssetPage{}, err
	}
	defer rows.Close()
	page.Records = make([]TeamAssetRecord, 0, limit)
	for rows.Next() {
		record, err := scanTeamAsset(rows)
		if err != nil {
			return TeamAssetPage{}, err
		}
		page.Records = append(page.Records, *record)
	}
	if err := rows.Err(); err != nil {
		return TeamAssetPage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TeamAssetPage{}, fmt.Errorf("commit team asset snapshot: %w", err)
	}
	return page, nil
}
