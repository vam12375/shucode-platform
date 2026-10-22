package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const teamAssetDeletionCols = `id::text,asset_id::text,asset_name,requested_by_id::text,requested_by_name,created_at,status,COALESCE(reviewed_by_id::text,''),reviewed_by_name,reviewed_at,withdrawn_at,blob_id::text,size,blob_cleaned_at`

func (s *PostgresStore) ListTeamAssetBlobCleanups(ctx context.Context, teamID string) ([]TeamAssetDeletionRecord, error) {
	if !canonicalUUID(teamID) {
		return nil, ErrInvalidInput
	}
	rows, err := s.pool.Query(ctx, `SELECT `+teamAssetDeletionCols+` FROM team_asset_deletion_requests WHERE team_id=$1::uuid AND status='approved' AND blob_cleaned_at IS NULL`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamAssetDeletionRecord{}
	for rows.Next() {
		r, err := scanTeamAssetDeletion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanTeamAssetDeletion(row pgx.Row) (TeamAssetDeletionRecord, error) {
	var r TeamAssetDeletionRecord
	err := row.Scan(&r.ID, &r.AssetID, &r.AssetName, &r.RequestedByID, &r.RequestedByName, &r.CreatedAt, &r.Status, &r.ReviewedByID, &r.ReviewedByName, &r.ReviewedAt, &r.WithdrawnAt, &r.BlobID, &r.Size, &r.BlobCleanedAt)
	return r, err
}

func (s *PostgresStore) beginTeamAssetActor(ctx context.Context, teamID, actorID string) (pgx.Tx, TeamMemberRole, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, "", "", err
	}
	fail := func(err error) (pgx.Tx, TeamMemberRole, string, error) { _ = tx.Rollback(ctx); return nil, "", "", err }
	exists, err := lockTeam(ctx, tx, teamID)
	if err != nil {
		return fail(err)
	}
	if !exists {
		return fail(ErrTeamAssetForbidden)
	}
	// 权限在团队锁内重读；角色变更/停用同样持有团队锁，不能沿用路由层旧快照。
	var role TeamMemberRole
	var name string
	err = tx.QueryRow(ctx, `SELECT m.role,u.display_name FROM team_members m JOIN users u ON u.id=m.user_id JOIN teams t ON t.id=m.team_id WHERE m.team_id=$1::uuid AND m.user_id=$2::uuid AND m.disabled_at IS NULL AND u.disabled_at IS NULL AND t.disabled_at IS NULL`, teamID, actorID).Scan(&role, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(ErrTeamAssetForbidden)
	}
	if err != nil {
		return fail(err)
	}
	return tx, role, teamAssetDeletionDisplayName(name), nil
}

func (s *PostgresStore) RequestTeamAssetDeletion(ctx context.Context, teamID, actorID, assetID, requestID string, at time.Time) (TeamAssetDeletionRecord, error) {
	if !canonicalUUID(teamID) || !canonicalUUID(actorID) || !canonicalUUID(assetID) || !canonicalUUID(requestID) || at.IsZero() {
		return TeamAssetDeletionRecord{}, ErrInvalidInput
	}
	tx, _, name, err := s.beginTeamAssetActor(ctx, teamID, actorID)
	if err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	defer tx.Rollback(ctx)
	asset, err := scanTeamAsset(tx.QueryRow(ctx, `SELECT `+teamAssetCols+` FROM team_assets WHERE team_id=$1::uuid AND id=$2::uuid`, teamID, assetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamAssetDeletionRecord{}, ErrTeamAssetNotFound
	}
	if err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	if asset.UploadedByID != actorID {
		return TeamAssetDeletionRecord{}, ErrTeamAssetForbidden
	}
	r, err := scanTeamAssetDeletion(tx.QueryRow(ctx, `SELECT `+teamAssetDeletionCols+` FROM team_asset_deletion_requests WHERE team_id=$1::uuid AND asset_id=$2::uuid AND status='pending'`, teamID, assetID))
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TeamAssetDeletionRecord{}, err
	}
	var reused bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM team_asset_deletion_requests WHERE team_id=$1::uuid AND id=$2::uuid)`, teamID, requestID).Scan(&reused); err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	if reused {
		return TeamAssetDeletionRecord{}, ErrTeamAssetConflict
	}
	r, err = scanTeamAssetDeletion(tx.QueryRow(ctx, `INSERT INTO team_asset_deletion_requests(team_id,id,asset_id,asset_name,requested_by_id,requested_by_name,created_at,status,blob_id,size) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6,$7,'pending',$8::uuid,$9) RETURNING `+teamAssetDeletionCols, teamID, requestID, assetID, asset.Name, actorID, name, at.UTC(), asset.BlobID, asset.Size))
	if err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	return r, nil
}

func (s *PostgresStore) ListTeamAssetDeletionRequests(ctx context.Context, teamID, actorID string, beforeAt *time.Time, beforeID string) (TeamAssetDeletionPage, bool, error) {
	if !canonicalUUID(teamID) || !canonicalUUID(actorID) || (beforeAt == nil && beforeID != "") || (beforeAt != nil && !canonicalUUID(beforeID)) {
		return TeamAssetDeletionPage{}, false, ErrInvalidInput
	}
	tx, role, _, err := s.beginTeamAssetActor(ctx, teamID, actorID)
	if err != nil {
		return TeamAssetDeletionPage{}, false, err
	}
	defer tx.Rollback(ctx)
	canReview := IsTeamAdmin(role)
	page := TeamAssetDeletionPage{Records: []TeamAssetDeletionRecord{}}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM team_asset_deletion_requests WHERE team_id=$1::uuid AND ($3::boolean OR requested_by_id=$2::uuid) AND status='pending'`, teamID, actorID, canReview).Scan(&page.PendingCount); err != nil {
		return TeamAssetDeletionPage{}, false, err
	}
	query := `SELECT ` + teamAssetDeletionCols + ` FROM team_asset_deletion_requests WHERE team_id=$1::uuid AND ($3::boolean OR requested_by_id=$2::uuid) AND status='pending'`
	args := []any{teamID, actorID, canReview}
	if beforeAt != nil {
		query += ` AND (created_at,id)<($4::timestamptz,$5::uuid)`
		args = append(args, *beforeAt, beforeID)
	}
	rows, err := tx.Query(ctx, query+` ORDER BY created_at DESC,id DESC LIMIT 101`, args...)
	if err != nil {
		return TeamAssetDeletionPage{}, false, err
	}
	for rows.Next() {
		r, err := scanTeamAssetDeletion(rows)
		if err != nil {
			rows.Close()
			return TeamAssetDeletionPage{}, false, err
		}
		page.Records = append(page.Records, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return TeamAssetDeletionPage{}, false, err
	}
	page.HasMore = len(page.Records) > 100
	if page.HasMore {
		page.Records = page.Records[:100]
	}
	if beforeAt == nil {
		rows, err = tx.Query(ctx, `SELECT `+teamAssetDeletionCols+` FROM team_asset_deletion_requests WHERE team_id=$1::uuid AND ($3::boolean OR requested_by_id=$2::uuid) AND status<>'pending' ORDER BY COALESCE(withdrawn_at,reviewed_at) DESC,id DESC LIMIT 100`, teamID, actorID, canReview)
		if err != nil {
			return TeamAssetDeletionPage{}, false, err
		}
		for rows.Next() {
			r, err := scanTeamAssetDeletion(rows)
			if err != nil {
				rows.Close()
				return TeamAssetDeletionPage{}, false, err
			}
			page.Records = append(page.Records, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return TeamAssetDeletionPage{}, false, err
		}
	}
	return page, canReview, nil
}

func (s *PostgresStore) WithdrawTeamAssetDeletion(ctx context.Context, teamID, actorID, requestID string, at time.Time) (TeamAssetDeletionRecord, error) {
	if !canonicalUUID(teamID) || !canonicalUUID(actorID) || !canonicalUUID(requestID) || at.IsZero() {
		return TeamAssetDeletionRecord{}, ErrInvalidInput
	}
	tx, _, _, err := s.beginTeamAssetActor(ctx, teamID, actorID)
	if err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	defer tx.Rollback(ctx)
	r, err := scanTeamAssetDeletion(tx.QueryRow(ctx, `SELECT `+teamAssetDeletionCols+` FROM team_asset_deletion_requests WHERE team_id=$1::uuid AND id=$2::uuid`, teamID, requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamAssetDeletionRecord{}, ErrTeamAssetNotFound
	}
	if err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	if r.RequestedByID != actorID {
		return TeamAssetDeletionRecord{}, ErrTeamAssetForbidden
	}
	if r.Status == "withdrawn" {
		return r, nil
	}
	if r.Status != "pending" {
		return TeamAssetDeletionRecord{}, ErrTeamAssetConflict
	}
	// 与审批和角色变更使用同一团队锁；条件更新保留 pending 的单次终态边界。
	r, err = scanTeamAssetDeletion(tx.QueryRow(ctx, `UPDATE team_asset_deletion_requests SET status='withdrawn',withdrawn_at=$3 WHERE team_id=$1::uuid AND id=$2::uuid AND status='pending' RETURNING `+teamAssetDeletionCols, teamID, requestID, at.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamAssetDeletionRecord{}, ErrTeamAssetConflict
	}
	if err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	return r, nil
}

func (s *PostgresStore) ReviewTeamAssetDeletion(ctx context.Context, teamID, actorID, requestID, decision string, at time.Time) (TeamAssetDeletionRecord, error) {
	if !canonicalUUID(teamID) || !canonicalUUID(actorID) || !canonicalUUID(requestID) || at.IsZero() || (decision != "approve" && decision != "reject") {
		return TeamAssetDeletionRecord{}, ErrInvalidInput
	}
	tx, role, name, err := s.beginTeamAssetActor(ctx, teamID, actorID)
	if err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	defer tx.Rollback(ctx)
	if !IsTeamAdmin(role) {
		return TeamAssetDeletionRecord{}, ErrTeamAssetForbidden
	}
	r, err := scanTeamAssetDeletion(tx.QueryRow(ctx, `SELECT `+teamAssetDeletionCols+` FROM team_asset_deletion_requests WHERE team_id=$1::uuid AND id=$2::uuid`, teamID, requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamAssetDeletionRecord{}, ErrTeamAssetNotFound
	}
	if err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	status := "rejected"
	if decision == "approve" {
		status = "approved"
	}
	if r.Status != "pending" {
		if r.Status != status {
			return TeamAssetDeletionRecord{}, ErrTeamAssetConflict
		}
		return r, nil
	}
	if status == "approved" {
		tag, err := tx.Exec(ctx, `DELETE FROM team_assets WHERE team_id=$1::uuid AND id=$2::uuid AND blob_id=$3::uuid AND uploaded_by_id=$4::uuid AND size=$5`, teamID, r.AssetID, r.BlobID, r.RequestedByID, r.Size)
		if err != nil {
			return TeamAssetDeletionRecord{}, err
		}
		if tag.RowsAffected() != 1 {
			return TeamAssetDeletionRecord{}, ErrTeamAssetConflict
		}
		tag, err = tx.Exec(ctx, `UPDATE teams SET team_asset_used_bytes=team_asset_used_bytes-$2 WHERE id=$1::uuid AND team_asset_used_bytes >= $2`, teamID, r.Size)
		if err != nil {
			return TeamAssetDeletionRecord{}, err
		}
		if tag.RowsAffected() != 1 {
			return TeamAssetDeletionRecord{}, ErrTeamAssetConflict
		}
	}
	r, err = scanTeamAssetDeletion(tx.QueryRow(ctx, `UPDATE team_asset_deletion_requests SET status=$3,reviewed_by_id=$4::uuid,reviewed_by_name=$5,reviewed_at=$6 WHERE team_id=$1::uuid AND id=$2::uuid RETURNING `+teamAssetDeletionCols, teamID, requestID, status, actorID, name, at.UTC()))
	if err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TeamAssetDeletionRecord{}, err
	}
	return r, nil
}

func (s *PostgresStore) MarkTeamAssetDeletionBlobCleaned(ctx context.Context, teamID, requestID string, at time.Time) error {
	if !canonicalUUID(teamID) || !canonicalUUID(requestID) || at.IsZero() {
		return ErrInvalidInput
	}
	tag, err := s.pool.Exec(ctx, `UPDATE team_asset_deletion_requests SET blob_cleaned_at=COALESCE(blob_cleaned_at,$3) WHERE team_id=$1::uuid AND id=$2::uuid AND status='approved'`, teamID, requestID, at.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM team_asset_deletion_requests WHERE team_id=$1::uuid AND id=$2::uuid)`, teamID, requestID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrTeamAssetConflict
	}
	return ErrTeamAssetNotFound
}
