package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vam12375/shucode-platform/server/internal/credits"
)

const quotaRequestCols = `id::text,team_id::text,requested_by::text,amount_credits::text,reason,status,COALESCE(reviewed_by::text,''),review_note,created_at,reviewed_at`

func scanQuotaRequest(row pgx.Row) (*QuotaRequestRecord, error) {
	var record QuotaRequestRecord
	err := row.Scan(&record.ID, &record.TeamID, &record.RequestedBy, &record.AmountCredits, &record.Reason, &record.Status, &record.ReviewedBy, &record.ReviewNote, &record.CreatedAt, &record.ReviewedAt)
	if err != nil {
		return nil, err
	}
	return &record, nil
}
func (s *PostgresStore) CreateQuotaRequest(ctx context.Context, record QuotaRequestRecord) error {
	normalized, err := normalizeQuotaRequest(record)
	if err != nil {
		return err
	}
	if normalized.ID == "" {
		normalized.ID = newUUID()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ok, err := lockTeam(ctx, tx, normalized.TeamID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrQuotaRequestInvalid
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM teams t JOIN users u ON u.id=$2::uuid WHERE t.id=$1::uuid AND t.disabled_at IS NULL AND u.disabled_at IS NULL AND (u.role='root' OR EXISTS(SELECT 1 FROM team_members m WHERE m.team_id=t.id AND m.user_id=u.id AND m.disabled_at IS NULL AND m.role IN ('owner','admin'))))`, normalized.TeamID, normalized.RequestedBy).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrQuotaRequestInvalid
	}
	_, err = tx.Exec(ctx, `INSERT INTO team_quota_requests(id,team_id,requested_by,amount_credits,reason,status,created_at) VALUES($1::uuid,$2::uuid,$3::uuid,$4::numeric,$5,'pending',$6)`, normalized.ID, normalized.TeamID, normalized.RequestedBy, normalized.AmountCredits, normalized.Reason, normalized.CreatedAt)
	if uniqueViolation(err) {
		return ErrQuotaRequestInvalid
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *PostgresStore) ListQuotaRequests(ctx context.Context, teamID string) ([]QuotaRequestRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+quotaRequestCols+` FROM team_quota_requests WHERE ($1::uuid IS NULL OR team_id=$1::uuid) ORDER BY created_at DESC,id DESC`, nullIfEmpty(teamID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QuotaRequestRecord{}
	for rows.Next() {
		record, err := scanQuotaRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *record)
	}
	return out, rows.Err()
}
func (s *PostgresStore) ReviewQuotaRequest(ctx context.Context, id, reviewerID, decision, note string, at time.Time) (*QuotaRequestRecord, error) {
	note = strings.TrimSpace(note)
	if !validQuotaReview(decision, note) {
		return nil, ErrQuotaRequestInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var root bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1::uuid AND role='root' AND disabled_at IS NULL)`, reviewerID).Scan(&root); err != nil {
		return nil, err
	}
	if !root {
		return nil, ErrQuotaRequestInvalid
	}
	record, err := scanQuotaRequest(tx.QueryRow(ctx, `SELECT `+quotaRequestCols+` FROM team_quota_requests WHERE id=$1::uuid FOR UPDATE`, id))
	if queryErrNoRows(err) {
		return nil, ErrQuotaRequestInvalid
	}
	if err != nil {
		return nil, err
	}
	if record.Status != "pending" {
		return nil, ErrQuotaRequestReviewed
	}
	if decision == "approve" {
		ok, err := lockTeam(ctx, tx, record.TeamID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrQuotaRequestInvalid
		}
		var oldQuota string
		var unlimited bool
		var disabled *time.Time
		if err := tx.QueryRow(ctx, `SELECT quota_credits::text,unlimited,disabled_at FROM teams WHERE id=$1::uuid`, record.TeamID).Scan(&oldQuota, &unlimited, &disabled); err != nil {
			return nil, err
		}
		if disabled != nil {
			return nil, ErrQuotaRequestInvalid
		}
		oldMicro, err := parseQuotaRequestMicro(oldQuota)
		if err != nil {
			return nil, ErrQuotaRequestInvalid
		}
		amountMicro, err := parseQuotaRequestMicro(record.AmountCredits)
		if err != nil || oldMicro > maxQuotaRequestMicro-amountMicro {
			return nil, ErrQuotaRequestInvalid
		}
		newQuota := credits.FormatMicroCredits(oldMicro + amountMicro)
		if _, err := tx.Exec(ctx, `UPDATE teams SET quota_credits=$2::numeric WHERE id=$1::uuid`, record.TeamID, newQuota); err != nil {
			return nil, err
		}
		// 审批状态、额度增量和数值前后流水必须一起提交，重复审批不能产生重复授信。
		if _, err := tx.Exec(ctx, `INSERT INTO team_quota_adjustments(team_id,actor_user_id,action,old_quota_credits,new_quota_credits,old_unlimited,new_unlimited,created_at) VALUES($1::uuid,$2::uuid,'approve_quota_request',$3::numeric,$4::numeric,$5,$5,$6)`, record.TeamID, reviewerID, oldQuota, newQuota, unlimited, at); err != nil {
			return nil, err
		}
		record.Status = "approved"
	} else {
		record.Status = "rejected"
	}
	if _, err := tx.Exec(ctx, `UPDATE team_quota_requests SET status=$2,reviewed_by=$3::uuid,review_note=$4,reviewed_at=$5 WHERE id=$1::uuid AND status='pending'`, record.ID, record.Status, reviewerID, note, at); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	record.ReviewedBy = reviewerID
	record.ReviewNote = note
	record.ReviewedAt = copyTime(&at)
	return record, nil
}
