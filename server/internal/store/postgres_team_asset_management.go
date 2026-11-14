package store

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) QueryTeamAssets(ctx context.Context, teamID, actorID string, q TeamAssetQuery) (TeamAssetQueryPage, error) {
	if !validTeamAssetQuery(teamID, actorID, q) {
		return TeamAssetQueryPage{}, ErrInvalidInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return TeamAssetQueryPage{}, err
	}
	defer tx.Rollback(ctx)
	page := TeamAssetQueryPage{Records: []TeamAssetManagedRecord{}, Facets: TeamAssetFacets{Kinds: []TeamAssetKindFacet{}, Uploaders: []TeamAssetUploaderFacet{}, Tags: []string{}}}
	var revision int64
	err = tx.QueryRow(ctx, `SELECT t.team_asset_revision,t.team_asset_used_bytes FROM teams t JOIN team_members m ON m.team_id=t.id JOIN users u ON u.id=m.user_id WHERE t.id=$1::uuid AND u.id=$2::uuid AND t.disabled_at IS NULL AND m.disabled_at IS NULL AND u.disabled_at IS NULL`, teamID, actorID).Scan(&revision, &page.UsedBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return page, ErrTeamAssetForbidden
	}
	if err != nil {
		return page, err
	}
	page.Revision = strconv.FormatInt(revision, 10)
	if q.ExpectedRevision != "" && q.ExpectedRevision != page.Revision {
		return page, ErrTeamAssetSnapshotChanged
	}
	if q.KnownRevision == page.Revision {
		page.NotModified = true
		return page, tx.Commit(ctx)
	}
	order := "a.created_at DESC,a.id DESC"
	switch q.Sort {
	case "oldest":
		order = "a.created_at ASC,a.id DESC"
	case "name":
		order = `a.name COLLATE "C" ASC,a.id DESC`
	case "size":
		order = "a.size DESC,a.id DESC"
	}
	rows, err := tx.Query(ctx, `SELECT a.id::text,a.name,a.kind,a.mime,a.size,a.created_at,a.uploaded_by_id::text,a.uploaded_by_name,a.sha256,a.blob_id::text,a.tags,(f.asset_id IS NOT NULL) FROM team_assets a LEFT JOIN team_asset_favorites f ON f.team_id=a.team_id AND f.asset_id=a.id AND f.user_id=$2::uuid WHERE a.team_id=$1::uuid AND ($3='' OR strpos(lower(a.name || ' ' || a.uploaded_by_name),lower($3))>0) AND ($4='' OR a.kind=$4) AND ($5='' OR a.uploaded_by_id::text=$5) AND ($6='' OR $6=ANY(a.tags)) AND (NOT $7 OR f.asset_id IS NOT NULL) ORDER BY `+order+` LIMIT $8 OFFSET $9`, teamID, actorID, q.Q, q.Kind, q.UploadedBy, q.Tag, q.FavoritesOnly, q.Limit, q.Offset)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var r TeamAssetManagedRecord
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Mime, &r.Size, &r.CreatedAt, &r.UploadedByID, &r.UploadedByName, &r.SHA256, &r.BlobID, &r.Tags, &r.Favorite); err != nil {
			rows.Close()
			return page, err
		}
		page.Records = append(page.Records, r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return page, err
	}
	rows, err = tx.Query(ctx, `SELECT kind,count(*),sum(size) FROM team_assets WHERE team_id=$1::uuid GROUP BY kind ORDER BY kind`, teamID)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var f TeamAssetKindFacet
		if err := rows.Scan(&f.Kind, &f.Count, &f.Bytes); err != nil {
			rows.Close()
			return page, err
		}
		page.Facets.Total += f.Count
		page.Facets.Kinds = append(page.Facets.Kinds, f)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return page, err
	}
	rows, err = tx.Query(ctx, `SELECT uploaded_by_id::text,min(uploaded_by_name COLLATE "C"),count(*) FROM team_assets WHERE team_id=$1::uuid GROUP BY uploaded_by_id ORDER BY uploaded_by_id LIMIT 1001`, teamID)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var f TeamAssetUploaderFacet
		if err := rows.Scan(&f.ID, &f.Name, &f.Count); err != nil {
			rows.Close()
			return page, err
		}
		page.Facets.Uploaders = append(page.Facets.Uploaders, f)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return page, err
	}
	rows, err = tx.Query(ctx, `SELECT DISTINCT unnest(tags) COLLATE "C" AS tag FROM team_assets WHERE team_id=$1::uuid ORDER BY tag LIMIT 1001`, teamID)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			rows.Close()
			return page, err
		}
		page.Facets.Tags = append(page.Facets.Tags, tag)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return page, err
	}
	boundTeamAssetFacets(&page.Facets)
	return page, tx.Commit(ctx)
}

func (s *PostgresStore) SetTeamAssetFavorite(ctx context.Context, teamID, actorID, id string, favorite bool) error {
	if !canonicalUUID(teamID) || !canonicalUUID(actorID) || !canonicalUUID(id) {
		return ErrInvalidInput
	}
	tx, _, _, err := s.beginTeamAssetActor(ctx, teamID, actorID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM team_assets WHERE team_id=$1::uuid AND id=$2::uuid)`, teamID, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrTeamAssetNotFound
	}
	query := `DELETE FROM team_asset_favorites WHERE team_id=$1::uuid AND user_id=$2::uuid AND asset_id=$3::uuid`
	if favorite {
		query = `INSERT INTO team_asset_favorites(team_id,user_id,asset_id) VALUES($1::uuid,$2::uuid,$3::uuid) ON CONFLICT DO NOTHING`
	}
	result, err := tx.Exec(ctx, query, teamID, actorID, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() > 0 {
		if _, err := tx.Exec(ctx, `UPDATE teams SET team_asset_revision=team_asset_revision+1 WHERE id=$1::uuid`, teamID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) SetTeamAssetTags(ctx context.Context, teamID, actorID, id string, tags []string) error {
	if !canonicalUUID(teamID) || !canonicalUUID(actorID) || !canonicalUUID(id) {
		return ErrInvalidInput
	}
	normalized, err := normalizeTeamAssetTags(tags)
	if err != nil {
		return err
	}
	tx, role, _, err := s.beginTeamAssetActor(ctx, teamID, actorID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var uploader string
	err = tx.QueryRow(ctx, `SELECT uploaded_by_id::text FROM team_assets WHERE team_id=$1::uuid AND id=$2::uuid`, teamID, id).Scan(&uploader)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTeamAssetNotFound
	}
	if err != nil {
		return err
	}
	if uploader != actorID && !IsTeamAdmin(role) {
		return ErrTeamAssetForbidden
	}
	result, err := tx.Exec(ctx, `UPDATE team_assets SET tags=$3 WHERE team_id=$1::uuid AND id=$2::uuid AND tags IS DISTINCT FROM $3`, teamID, id, normalized)
	if err != nil {
		return err
	}
	if result.RowsAffected() > 0 {
		if _, err := tx.Exec(ctx, `UPDATE teams SET team_asset_revision=team_asset_revision+1 WHERE id=$1::uuid`, teamID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
