package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vam12375/shucode-platform/server/internal/credits"
)

func creditMicroString(micro int64) string { return credits.FormatMicroCredits(micro) }

func parseMicroString(value string) (int64, error) { return credits.ParseCreditsToMicro(value) }

// Postgres 运维域：渠道池、额度预留状态机、小时 rollup、ops 指标与告警。

// ---- upstream routes ----

const upstreamRouteCols = `r.id::text, r.model_id, r.provider_id::text, COALESCE(p.name, ''), p.enabled,
	r.upstream_model, r.priority, r.weight, r.enabled, r.created_at, r.updated_at`

func scanUpstreamRoute(rows pgx.Rows) (*UpstreamRouteRecord, error) {
	var r UpstreamRouteRecord
	if err := rows.Scan(&r.ID, &r.ModelID, &r.ProviderID, &r.ProviderName, &r.ProviderEnabled,
		&r.UpstreamModel, &r.Priority, &r.Weight, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *PostgresStore) ListUpstreamRoutes(ctx context.Context, modelID string, includeDisabled bool) ([]UpstreamRouteRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+upstreamRouteCols+`
		FROM upstream_routes r
		JOIN upstream_providers p ON p.id = r.provider_id
		WHERE ($1::text IS NULL OR r.model_id = $1)
			AND ($2::boolean OR (r.enabled AND p.enabled))
		ORDER BY r.priority ASC, r.created_at ASC`, nullIfEmpty(modelID), includeDisabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UpstreamRouteRecord{}
	for rows.Next() {
		r, err := scanUpstreamRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) getUpstreamRoute(ctx context.Context, id string) (*UpstreamRouteRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+upstreamRouteCols+`
		FROM upstream_routes r JOIN upstream_providers p ON p.id = r.provider_id
		WHERE r.id = $1::uuid LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanUpstreamRoute(rows)
}

func (s *PostgresStore) CreateUpstreamRoute(ctx context.Context, input CreateUpstreamRouteInput) (*UpstreamRouteRecord, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO upstream_routes (model_id, provider_id, upstream_model, priority, weight, enabled)
		VALUES ($1, $2::uuid, $3, $4, $5, $6)
		RETURNING id::text`,
		input.ModelID, input.ProviderID, input.UpstreamModel, input.Priority, input.Weight, input.Enabled).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.getUpstreamRoute(ctx, id)
}

func (s *PostgresStore) UpdateUpstreamRoute(ctx context.Context, id string, patch UpdateUpstreamRoutePatch) (*UpstreamRouteRecord, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE upstream_routes SET
			upstream_model = COALESCE($2, upstream_model),
			priority = COALESCE($3::integer, priority),
			weight = COALESCE($4::integer, weight),
			enabled = COALESCE($5::boolean, enabled),
			updated_at = now()
		WHERE id = $1::uuid`,
		id, patch.UpstreamModel, patch.Priority, patch.Weight, patch.Enabled)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}
	return s.getUpstreamRoute(ctx, id)
}

func (s *PostgresStore) DeleteUpstreamRoute(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM upstream_routes WHERE id = $1::uuid`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PostgresStore) RecoverUpstreamProvider(ctx context.Context, id string, _ time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE upstream_providers SET
			enabled = true,
			disabled_mode = NULL,
			auto_disabled_at = NULL,
			auto_disabled_reason = NULL,
			consecutive_failures = 0
		WHERE id = $1::uuid AND disabled_mode = 'auto' AND NOT enabled`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ---- quota reservations ----

func (s *PostgresStore) ReserveQuota(ctx context.Context, input ReserveQuotaInput) (ReserveResult, error) {
	input.Postpaid = false
	return s.reserveQuota(ctx, input)
}

func (s *PostgresStore) AdmitUsage(ctx context.Context, input ReserveQuotaInput) (ReserveResult, error) {
	input.Postpaid = true
	input.ReserveAllAvailable = false
	input.RetainUntilSettled = true
	return s.reserveQuota(ctx, input)
}

func (s *PostgresStore) reserveQuota(ctx context.Context, input ReserveQuotaInput) (ReserveResult, error) {
	if input.Micro < 0 || input.Micro > maxCreditFieldMicro {
		return ReserveDenied, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	id := input.ID
	if id == "" {
		id = newUUID()
	}
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	ttl := input.TTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	expiresAt := createdAt.Add(ttl)
	// 团队行锁使准入读取已提交的账本；准入不会占款，并发请求可基于同一正余额获准。
	active, err := lockActiveTeam(ctx, tx, input.TeamID)
	if err != nil {
		return "", err
	}
	if !active {
		return ReserveDeniedInactive, nil
	}
	var duplicate bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM quota_reservations WHERE team_id=$1::uuid AND request_id=$2)`, input.TeamID, input.RequestID).Scan(&duplicate); err != nil {
		return "", err
	}
	if duplicate {
		return ReserveDuplicate, nil
	}
	if input.ReserveAllAvailable {
		var available string
		var unlimited bool
		if err := tx.QueryRow(ctx, `SELECT GREATEST(quota_credits-used_credits-reserved_credits, 0)::text, unlimited FROM teams WHERE id=$1::uuid`, input.TeamID).Scan(&available, &unlimited); err != nil {
			return "", err
		}
		if !unlimited {
			input.Micro, err = parseMicroString(available)
			if err != nil {
				return "", err
			}
		}
	}
	decision, err := reservationBudgetAllowed(ctx, tx, input, createdAt)
	if err != nil {
		return "", err
	}
	if decision != ReserveReserved {
		return decision, nil
	}
	if input.Micro < 0 || input.Micro > maxCreditFieldMicro {
		return ReserveDenied, nil
	}
	reservedMicro := input.Micro
	if input.Postpaid {
		var used, quota string
		var unlimited bool
		if err := tx.QueryRow(ctx, `SELECT used_credits::text,quota_credits::text,unlimited FROM teams WHERE id=$1::uuid`, input.TeamID).Scan(&used, &quota, &unlimited); err != nil {
			return "", err
		}
		allowed, err := teamCreditAdmissionAllowed(used, quota, unlimited, input.Micro)
		if err != nil {
			return "", err
		}
		if !allowed {
			return ReserveDenied, nil
		}
		reservedMicro = 0
	}
	var inserted string
	err = tx.QueryRow(ctx, `
		INSERT INTO quota_reservations (id, team_id, request_id, reserved_credits, status, created_at, expires_at, user_id, held_by_task, retain_until_settled, postpaid)
		VALUES ($1::uuid, $2::uuid, $3, $4::numeric, 'reserved', $5::timestamptz, $6::timestamptz, $7::uuid, $8, $9, $10)
		ON CONFLICT DO NOTHING
		RETURNING id::text`,
		id, input.TeamID, input.RequestID, creditMicroString(reservedMicro),
		createdAt, expiresAt, nullIfEmpty(input.UserID), input.HeldByTask, input.RetainUntilSettled, input.Postpaid).Scan(&inserted)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ReserveDuplicate, nil
		}
		return "", err
	}
	if input.Postpaid {
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return ReserveReserved, nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE teams
		SET reserved_credits = reserved_credits + $2::numeric
		WHERE id = $1::uuid
			AND disabled_at IS NULL
			AND $2::numeric >= 0
			AND (unlimited OR (used_credits + reserved_credits < quota_credits
				AND used_credits + reserved_credits + $2::numeric <= quota_credits))`,
		input.TeamID, creditMicroString(input.Micro))
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return ReserveDenied, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return ReserveReserved, nil
}

func (s *PostgresStore) GetQuotaReservation(ctx context.Context, id string) (*QuotaReservationRecord, error) {
	var record QuotaReservationRecord
	var reserved string
	err := s.pool.QueryRow(ctx, `SELECT id::text,team_id::text,COALESCE(user_id::text,''),request_id,reserved_credits::text,status,
		created_at,expires_at,settled_at,released_at,COALESCE(usage_id::text,''),held_by_task,retain_until_settled,postpaid
		FROM quota_reservations WHERE id=$1::uuid`, id).Scan(&record.ID, &record.TeamID, &record.UserID, &record.RequestID,
		&reserved, &record.Status, &record.CreatedAt, &record.ExpiresAt, &record.SettledAt, &record.ReleasedAt,
		&record.UsageID, &record.HeldByTask, &record.RetainUntilSettled, &record.Postpaid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record.ReservedMicro, err = parseMicroString(reserved)
	return &record, err
}

func (s *PostgresStore) SettleReservation(ctx context.Context, reservationID string, record UsageRecord) error {
	if record.CreditsMicro < 0 || record.CreditsMicro > maxCreditFieldMicro {
		return ErrInvalidCreditCharge
	}
	if record.ID == "" {
		record.ID = newUUID()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var (
		teamID    string
		userID    string
		reserved  string
		status    string
		requestID string
		postpaid  bool
	)
	err = tx.QueryRow(ctx, `
		SELECT team_id::text, COALESCE(user_id::text,''), reserved_credits::text, status, request_id, postpaid
		FROM quota_reservations WHERE id = $1::uuid FOR UPDATE`, reservationID).
		Scan(&teamID, &userID, &reserved, &status, &requestID, &postpaid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrReservationNotFound
		}
		return err
	}
	if record.TeamID != teamID || (userID != "" && record.UserID != userID) || (postpaid && record.RequestID != requestID) {
		return ErrReservationMismatch
	}
	if status == string(ReservationSettled) || status == string(ReservationReleased) {
		return tx.Commit(ctx)
	}
	var quota, used, teamReserved string
	var unlimited bool
	if err := tx.QueryRow(ctx, `
		SELECT quota_credits::text, used_credits::text, reserved_credits::text, unlimited
		FROM teams WHERE id = $1::uuid FOR UPDATE`, teamID).
		Scan(&quota, &used, &teamReserved, &unlimited); err != nil {
		return err
	}
	releasedMicro := int64(0)
	if !postpaid && status == string(ReservationReserved) {
		releasedMicro, err = parseMicroString(reserved)
		if err != nil {
			return err
		}
	}
	var charge int64
	var capped bool
	if postpaid {
		charge, err = fullTeamCreditCharge(used, record.CreditsMicro)
	} else {
		charge, capped, err = capTeamCreditCharge(used, teamReserved, quota, unlimited, releasedMicro, record.CreditsMicro)
	}
	if err != nil {
		return err
	}
	record.CreditsMicro = charge
	if capped {
		markCreditChargeCapped(&record)
	}
	// 后付费身份没有占款；仅历史 reserved 扣回原预留，expired 只记实扣。
	if !postpaid && status == string(ReservationReserved) {
		if _, err := tx.Exec(ctx, `
			UPDATE teams SET
				reserved_credits = GREATEST(reserved_credits - $2::numeric, 0),
				used_credits = used_credits + $3::numeric
			WHERE id = $1::uuid`, teamID, reserved, creditMicroString(charge)); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE teams SET used_credits = used_credits + $2::numeric WHERE id = $1::uuid`,
			teamID, creditMicroString(charge)); err != nil {
			return err
		}
	}
	if err := insertUsageExec(ctx, tx, record); err != nil {
		return err
	}
	// usage_id 有外键：必须先写 usage_records，再回填预留。
	if _, err := tx.Exec(ctx, `
		UPDATE quota_reservations
		SET status = 'settled', settled_at = $2::timestamptz, usage_id = $3::uuid
		WHERE id = $1::uuid`, reservationID, record.CreatedAt, nullIfEmpty(record.ID)); err != nil {
		return err
	}
	if err := upsertRollupExec(ctx, tx, record); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if capped {
		return ErrCreditChargeCapped
	}
	return nil
}

func (s *PostgresStore) ReleaseReservationByID(ctx context.Context, reservationID string, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var (
		teamID   string
		reserved string
		status   string
		postpaid bool
	)
	err = tx.QueryRow(ctx, `
		SELECT team_id::text, reserved_credits::text, status, postpaid
		FROM quota_reservations WHERE id = $1::uuid FOR UPDATE`, reservationID).
		Scan(&teamID, &reserved, &status, &postpaid)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	if status != string(ReservationReserved) {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE quota_reservations SET status = 'released', released_at = $2::timestamptz
		WHERE id = $1::uuid`, reservationID, at); err != nil {
		return err
	}
	if !postpaid {
		if _, err := tx.Exec(ctx, `
			UPDATE teams SET reserved_credits = GREATEST(reserved_credits - $2::numeric, 0)
			WHERE id = $1::uuid`, teamID, reserved); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ExpireStaleReservations(ctx context.Context, before time.Time) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		UPDATE quota_reservations
		SET status = 'expired', released_at = $1::timestamptz
		WHERE status = 'reserved' AND expires_at < $1::timestamptz AND held_by_task = false AND retain_until_settled = false AND postpaid = false
		RETURNING team_id::text, reserved_credits::text`, before)
	if err != nil {
		return 0, err
	}
	perTeam := map[string]int64{}
	var count int64
	for rows.Next() {
		var teamID, reserved string
		if err := rows.Scan(&teamID, &reserved); err != nil {
			rows.Close()
			return 0, err
		}
		micro, parseErr := parseMicroString(reserved)
		if parseErr != nil {
			rows.Close()
			return 0, parseErr
		}
		perTeam[teamID] += micro
		count++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for teamID, micro := range perTeam {
		if _, err := tx.Exec(ctx, `
			UPDATE teams SET reserved_credits = GREATEST(reserved_credits - $2::numeric, 0)
			WHERE id = $1::uuid`, teamID, creditMicroString(micro)); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *PostgresStore) ListOpenReservations(ctx context.Context, limit int64) ([]QuotaReservationRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, team_id::text, COALESCE(user_id::text,''), request_id, reserved_credits::text, status,
			created_at, expires_at, settled_at, released_at, COALESCE(usage_id::text, ''), held_by_task, retain_until_settled, postpaid
		FROM quota_reservations
		WHERE status = 'reserved'
		ORDER BY created_at ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QuotaReservationRecord{}
	for rows.Next() {
		var (
			r        QuotaReservationRecord
			reserved string
			status   string
		)
		if err := rows.Scan(&r.ID, &r.TeamID, &r.UserID, &r.RequestID, &reserved, &status,
			&r.CreatedAt, &r.ExpiresAt, &r.SettledAt, &r.ReleasedAt, &r.UsageID, &r.HeldByTask, &r.RetainUntilSettled, &r.Postpaid); err != nil {
			return nil, err
		}
		r.ReservedMicro, _ = parseMicroString(reserved)
		r.Status = QuotaReservationStatus(status)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateBackgroundGeneration(ctx context.Context, job BackgroundGenerationJob) error {
	if job.ReservationID == "" || job.ResponseID == "" || (job.ProviderID == "" && job.BaseURL == "") {
		return ErrInvalidInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var teamID, userID string
	var status QuotaReservationStatus
	if err := tx.QueryRow(ctx, `SELECT team_id::text,COALESCE(user_id::text,''),status FROM quota_reservations WHERE id=$1::uuid FOR SHARE`, job.ReservationID).Scan(&teamID, &userID, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrReservationMismatch
		}
		return err
	}
	if teamID != job.TeamID || userID != job.UserID || status != ReservationReserved {
		return ErrReservationMismatch
	}
	record, err := json.Marshal(backgroundRecordTemplate{UsageRecord: job.Record, UsageObservedAt: job.UsageObservedAt})
	if err != nil {
		return err
	}
	rates, err := json.Marshal(job.Rates)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO gateway_background_generations (
			reservation_id, response_id, provider_id, base_url, team_id, user_id,
			record_template, charge_rates, limited, next_poll_at, attempts, last_error, created_at
		) VALUES ($1::uuid, $2, $3::uuid, $4, $5::uuid, $6::uuid, $7::jsonb, $8::jsonb, $13, $9, $10, $11, $12)
		ON CONFLICT (reservation_id) DO NOTHING`,
		job.ReservationID, job.ResponseID, nullIfEmpty(job.ProviderID), job.BaseURL, job.TeamID, job.UserID,
		record, rates, job.NextPollAt, job.Attempts, emptyToNil(job.LastError), job.CreatedAt, job.Limited)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) UpdateBackgroundGenerationUsage(ctx context.Context, reservationID string, record UsageRecord) (*BackgroundGenerationJob, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	job, err := scanBackgroundGeneration(tx.QueryRow(ctx, `SELECT `+backgroundGenerationCols+` FROM gateway_background_generations WHERE reservation_id=$1::uuid FOR UPDATE`, reservationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrReservationNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := updateBackgroundUsage(job, record); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(backgroundRecordTemplate{UsageRecord: job.Record, UsageObservedAt: job.UsageObservedAt})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE gateway_background_generations SET record_template=$2::jsonb WHERE reservation_id=$1::uuid`, reservationID, encoded); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return job, nil
}

func scanBackgroundGeneration(row pgx.Row) (*BackgroundGenerationJob, error) {
	var job BackgroundGenerationJob
	var recordJSON, ratesJSON []byte
	var lastError *string
	var providerID *string
	if err := row.Scan(&job.ReservationID, &job.ResponseID, &providerID, &job.BaseURL, &job.TeamID, &job.UserID,
		&recordJSON, &ratesJSON, &job.Limited, &job.NextPollAt, &job.Attempts, &lastError, &job.CreatedAt,
		&job.FinalResponse, &job.FinalStatus, &job.FinalType, &job.CompletedAt); err != nil {
		return nil, err
	}
	job.ProviderID = derefString2(providerID)
	var record backgroundRecordTemplate
	if err := json.Unmarshal(recordJSON, &record); err != nil {
		return nil, err
	}
	job.Record = record.UsageRecord
	job.UsageObservedAt = record.UsageObservedAt
	if err := json.Unmarshal(ratesJSON, &job.Rates); err != nil {
		return nil, err
	}
	job.LastError = derefString2(lastError)
	return &job, nil
}

const backgroundGenerationCols = `reservation_id::text, response_id, provider_id::text, base_url, team_id::text, user_id::text,
	record_template, charge_rates, limited, next_poll_at, attempts, last_error, created_at,
	COALESCE(final_response, ''::bytea), COALESCE(final_status, 0), COALESCE(final_content_type, ''), completed_at`

func (s *PostgresStore) FindBackgroundGeneration(ctx context.Context, teamID, responseID string) (*BackgroundGenerationJob, error) {
	job, err := scanBackgroundGeneration(s.pool.QueryRow(ctx,
		`SELECT `+backgroundGenerationCols+` FROM gateway_background_generations WHERE team_id=$1::uuid AND response_id=$2`,
		teamID, responseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return job, err
}

func (s *PostgresStore) ClaimBackgroundGenerations(ctx context.Context, now, leaseUntil time.Time, limit int64) ([]BackgroundGenerationJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT reservation_id FROM gateway_background_generations
			WHERE completed_at IS NULL AND next_poll_at <= $1
			ORDER BY next_poll_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE gateway_background_generations j
		SET next_poll_at=$3, attempts=j.attempts+1
		FROM due WHERE j.reservation_id=due.reservation_id
		RETURNING `+strings.ReplaceAll(backgroundGenerationCols, "reservation_id::text", "j.reservation_id::text"), now, limit, leaseUntil)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]BackgroundGenerationJob, 0, limit)
	for rows.Next() {
		job, err := scanBackgroundGeneration(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *PostgresStore) RetryBackgroundGeneration(ctx context.Context, reservationID string, nextPollAt time.Time, lastError string) error {
	_, err := s.pool.Exec(ctx, `UPDATE gateway_background_generations SET next_poll_at=$2, last_error=$3 WHERE reservation_id=$1::uuid`,
		reservationID, nextPollAt, emptyToNil(lastError))
	return err
}

func (s *PostgresStore) DeleteBackgroundGeneration(ctx context.Context, reservationID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM gateway_background_generations WHERE reservation_id=$1::uuid`, reservationID)
	return err
}

func (s *PostgresStore) FinishBackgroundGeneration(ctx context.Context, reservationID string, response []byte, status int, contentType string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE gateway_background_generations
		SET final_response=$2, final_status=$3, final_content_type=$4, completed_at=$5, last_error=NULL
		WHERE reservation_id=$1::uuid`, reservationID, response, status, contentType, at)
	return err
}

// ---- 结算失败补偿 ----

func (s *PostgresStore) EnqueuePendingSettlement(ctx context.Context, reservationID string, record UsageRecord) error {
	var snapshotJSON []byte
	if record.PriceSnapshot != nil {
		var err error
		snapshotJSON, err = json.Marshal(record.PriceSnapshot)
		if err != nil {
			return err
		}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO pending_settlements (
			reservation_id, request_id, user_id, team_id, device_token_id, model, upstream_model,
			provider_id, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			credits_micro, price_snapshot, usage_status, error_type, client_ip, latency_ms,
			usage_created_at, reasoning_effort, reasoning_budget_tokens
		) VALUES (
			$1::uuid, $2, $3::uuid, $4::uuid, $5::uuid, $6, $7,
			$8::uuid, $9, $10, $11, $12, $13, $14::jsonb, $15, $16, $17, $18, $19::timestamptz, $20, $21
		)
		ON CONFLICT (reservation_id) DO NOTHING`,
		reservationID, record.RequestID, record.UserID, record.TeamID, nullIfEmpty(record.DeviceTokenID),
		record.Model, record.UpstreamModel, nullIfEmpty(record.ProviderID),
		record.InputTokens, record.OutputTokens, record.CacheReadTokens, record.CacheCreationTokens,
		record.CreditsMicro, nullableBytes(snapshotJSON), string(record.Status),
		nullIfEmpty(record.ErrorType), nullIfEmpty(record.ClientIP), record.LatencyMs, record.CreatedAt,
		nullIfEmpty(record.ReasoningEffort), record.ReasoningBudgetTokens)
	return err
}

func (s *PostgresStore) ListPendingSettlements(ctx context.Context, filter PendingSettlementFilter) ([]PendingSettlementRecord, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT reservation_id::text, request_id, user_id::text, team_id::text,
			COALESCE(device_token_id::text, ''), model, upstream_model,
			COALESCE(provider_id::text, ''), input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens, credits_micro, price_snapshot,
			usage_status, COALESCE(error_type, ''), COALESCE(client_ip, ''), latency_ms,
			usage_created_at, status, attempts, COALESCE(last_error, ''),
			next_attempt_at, created_at, updated_at, COALESCE(reasoning_effort, ''), reasoning_budget_tokens
		FROM pending_settlements
		WHERE ($1::text = '' OR status = $1)
			AND ($2::timestamptz IS NULL OR next_attempt_at <= $2::timestamptz)
		ORDER BY next_attempt_at ASC, created_at ASC
		LIMIT $3`, string(filter.Status), nullableTime(filter.DueBefore), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PendingSettlementRecord{}
	for rows.Next() {
		var (
			item       PendingSettlementRecord
			record     UsageRecord
			snapshot   []byte
			status     string
			usageState string
		)
		if err := rows.Scan(&item.ReservationID, &record.RequestID, &record.UserID, &record.TeamID,
			&record.DeviceTokenID, &record.Model, &record.UpstreamModel, &record.ProviderID,
			&record.InputTokens, &record.OutputTokens, &record.CacheReadTokens, &record.CacheCreationTokens,
			&record.CreditsMicro, &snapshot, &usageState, &record.ErrorType, &record.ClientIP,
			&record.LatencyMs, &record.CreatedAt, &status, &item.Attempts, &item.LastError,
			&item.NextAttemptAt, &item.CreatedAt, &item.UpdatedAt, &record.ReasoningEffort, &record.ReasoningBudgetTokens); err != nil {
			return nil, err
		}
		record.Status = UsageStatus(usageState)
		if len(snapshot) > 0 {
			if err := json.Unmarshal(snapshot, &record.PriceSnapshot); err != nil {
				return nil, err
			}
		}
		item.Record = record
		item.Status = PendingSettlementStatus(status)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PostgresStore) MarkPendingSettlementApplied(ctx context.Context, reservationID string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE pending_settlements
		SET status = 'applied', last_error = NULL, updated_at = $2::timestamptz
		WHERE reservation_id = $1::uuid`, reservationID, at)
	return err
}

func (s *PostgresStore) MarkPendingSettlementRetry(ctx context.Context, reservationID string, attempts int64, lastError string, nextAttemptAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE pending_settlements
		SET status = 'pending', attempts = $2, last_error = $3,
			next_attempt_at = $4::timestamptz, updated_at = now()
		WHERE reservation_id = $1::uuid`, reservationID, attempts, lastError, nextAttemptAt)
	return err
}

func (s *PostgresStore) MarkPendingSettlementFailed(ctx context.Context, reservationID string, attempts int64, lastError string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE pending_settlements
		SET status = 'failed', attempts = $2, last_error = $3, updated_at = now()
		WHERE reservation_id = $1::uuid`, reservationID, attempts, lastError)
	return err
}

func (s *PostgresStore) CountPendingSettlements(ctx context.Context, status PendingSettlementStatus) (int64, error) {
	var count int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)::bigint FROM pending_settlements
		WHERE ($1::text = '' OR status = $1)`, string(status)).Scan(&count)
	return count, err
}

func (s *PostgresStore) InsertGatewayAttempt(ctx context.Context, record GatewayAttemptRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO gateway_attempts (
			request_id, reservation_id, attempt, provider_id, upstream_model,
			base_host, status_code, error_type, provider_request_id,
			started_at, duration_ms, final
		) VALUES (
			$1, $2::uuid, $3, $4::uuid, $5, $6, $7, $8, $9,
			$10::timestamptz, $11, $12
		)`,
		record.RequestID, record.ReservationID, record.Attempt,
		nullIfEmpty(record.ProviderID), record.UpstreamModel, record.BaseHost,
		record.StatusCode, nullIfEmpty(record.ErrorType), nullIfEmpty(record.ProviderRequestID),
		record.StartedAt, record.DurationMs, record.Final)
	return err
}

// ---- usage export ----

func (s *PostgresStore) ListUsageForExport(ctx context.Context, filter UsageFilter, max int64) ([]UsageRecord, error) {
	if max <= 0 {
		max = 10_000
	}
	if max > 100_000 {
		max = 100_000
	}
	query, args := listUsageQuery(filter, max)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRecord{}
	for rows.Next() {
		r, err := scanUsage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ---- rollup helpers ----

func insertUsageExec(ctx context.Context, tx pgx.Tx, record UsageRecord) error {
	var snapshotJSON []byte
	if record.PriceSnapshot != nil {
		var err error
		snapshotJSON, err = json.Marshal(record.PriceSnapshot)
		if err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO usage_records (
			id, request_id, user_id, team_id, device_token_id, model, upstream_model,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			credits_micro, status, error_type, client_ip, latency_ms, provider_id, created_at, price_snapshot,
			reasoning_effort, reasoning_budget_tokens
		) VALUES (
			$1::uuid, $2, $3::uuid, $4::uuid, $5::uuid, $6, $7,
			$8, $9, $10, $11, $12, $13, $14, $15, $16, $17::uuid, $18::timestamptz, $19::jsonb, $20, $21
		)`,
		record.ID, record.RequestID, record.UserID, record.TeamID, nullIfEmpty(record.DeviceTokenID),
		record.Model, record.UpstreamModel, record.InputTokens, record.OutputTokens,
		record.CacheReadTokens, record.CacheCreationTokens, record.CreditsMicro, string(record.Status),
		emptyToNil(record.ErrorType), emptyToNil(record.ClientIP), record.LatencyMs,
		nullIfEmpty(record.ProviderID), record.CreatedAt, nullableBytes(snapshotJSON), nullIfEmpty(record.ReasoningEffort), record.ReasoningBudgetTokens)
	return err
}

func upsertRollupExec(ctx context.Context, tx pgx.Tx, record UsageRecord) error {
	var latency int64
	if record.LatencyMs != nil {
		latency = *record.LatencyMs
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO usage_hourly_rollups (
			bucket_start, requests, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens, credits_micro, latency_ms_sum
		) VALUES (
			date_trunc('hour', $1::timestamptz), 1, $2, $3, $4, $5, $6, $7
		)
		ON CONFLICT (bucket_start) DO UPDATE SET
			requests = usage_hourly_rollups.requests + 1,
			input_tokens = usage_hourly_rollups.input_tokens + EXCLUDED.input_tokens,
			output_tokens = usage_hourly_rollups.output_tokens + EXCLUDED.output_tokens,
			cache_read_tokens = usage_hourly_rollups.cache_read_tokens + EXCLUDED.cache_read_tokens,
			cache_creation_tokens = usage_hourly_rollups.cache_creation_tokens + EXCLUDED.cache_creation_tokens,
			credits_micro = usage_hourly_rollups.credits_micro + EXCLUDED.credits_micro,
			latency_ms_sum = usage_hourly_rollups.latency_ms_sum + EXCLUDED.latency_ms_sum`,
		record.CreatedAt, record.InputTokens, record.OutputTokens,
		record.CacheReadTokens, record.CacheCreationTokens, record.CreditsMicro, latency)
	return err
}

// ---- ops overview / series ----

func (s *PostgresStore) OpsOverview(ctx context.Context, since time.Time) (*OpsOverview, error) {
	var out OpsOverview
	err := s.pool.QueryRow(ctx, `
		SELECT
			count(*)::bigint,
			count(*) FILTER (WHERE status = 'ok')::bigint,
			count(*) FILTER (WHERE status = 'error')::bigint,
			count(*) FILTER (WHERE status = 'aborted')::bigint,
			COALESCE(sum(input_tokens), 0)::bigint,
			COALESCE(sum(output_tokens), 0)::bigint,
			COALESCE(sum(cache_read_tokens), 0)::bigint,
			COALESCE(sum(cache_creation_tokens), 0)::bigint,
			COALESCE(sum(credits_micro), 0)::bigint,
			percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE latency_ms IS NOT NULL),
			percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE latency_ms IS NOT NULL),
			percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE latency_ms IS NOT NULL),
			(SELECT count(DISTINCT device_token_id) FROM usage_records WHERE created_at >= $1 AND device_token_id IS NOT NULL)::bigint,
			(SELECT count(*) FROM quota_reservations WHERE status = 'reserved')::bigint,
			(SELECT count(*) FROM pending_settlements WHERE status IN ('pending', 'failed'))::bigint
		FROM usage_records WHERE created_at >= $1`, since).
		Scan(&out.Requests, &out.SuccessRequests, &out.ErrorRequests, &out.AbortedRequests,
			&out.InputTokens, &out.OutputTokens, &out.CacheReadTokens, &out.CacheCreationTokens,
			&out.CreditsMicro, &out.P50LatencyMs, &out.P95LatencyMs, &out.P99LatencyMs,
			&out.ActiveDevices, &out.OpenReservations, &out.PendingSettlements)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(u.provider_id::text, ''), COALESCE(p.name, '网关默认上游'),
			count(*)::bigint,
			count(*) FILTER (WHERE u.status = 'error')::bigint,
			COALESCE(count(*) FILTER (WHERE u.status = 'error')::float8 / NULLIF(count(*), 0), 0),
			percentile_cont(0.95) WITHIN GROUP (ORDER BY u.latency_ms) FILTER (WHERE u.latency_ms IS NOT NULL)
		FROM usage_records u
		LEFT JOIN upstream_providers p ON p.id = u.provider_id
		WHERE u.created_at >= $1
		GROUP BY 1, 2
		ORDER BY count(*) DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out.ProviderStats = []OpsProviderStat{}
	for rows.Next() {
		var stat OpsProviderStat
		if err := rows.Scan(&stat.ProviderID, &stat.ProviderName, &stat.Requests, &stat.Errors, &stat.ErrorRate, &stat.P95LatencyMs); err != nil {
			return nil, err
		}
		out.ProviderStats = append(out.ProviderStats, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *PostgresStore) OpsSeries(ctx context.Context, since time.Time, bucketMinutes int64) ([]OpsSeriesPoint, error) {
	if bucketMinutes < 1 {
		bucketMinutes = 5
	}
	if bucketMinutes > 1440 {
		bucketMinutes = 1440
	}
	interval := fmt.Sprintf("%d minutes", bucketMinutes)
	rows, err := s.pool.Query(ctx, `
		SELECT
			to_char(
				date_bin($2::interval, created_at, '1970-01-01'::timestamptz) AT TIME ZONE 'Asia/Shanghai',
				'YYYY-MM-DD"T"HH24:MI:SS'
			) AS bucket_start,
			count(*)::bigint,
			count(*) FILTER (WHERE status = 'error')::bigint,
			COALESCE(sum(credits_micro), 0)::bigint,
			percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE latency_ms IS NOT NULL)
		FROM usage_records
		WHERE created_at >= $1
		GROUP BY bucket_start
		ORDER BY bucket_start ASC`, since, interval)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpsSeriesPoint{}
	for rows.Next() {
		var p OpsSeriesPoint
		if err := rows.Scan(&p.BucketStart, &p.Requests, &p.Errors, &p.CreditsMicro, &p.P95LatencyMs); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- alerts ----

const alertRuleCols = `id::text, name, metric, threshold::text, window_minutes, enabled, created_at, updated_at`

func scanAlertRule(rows pgx.Rows) (*AlertRuleRecord, error) {
	var r AlertRuleRecord
	if err := rows.Scan(&r.ID, &r.Name, &r.Metric, &r.Threshold, &r.WindowMinutes, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *PostgresStore) ListAlertRules(ctx context.Context) ([]AlertRuleRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+alertRuleCols+` FROM admin_alert_rules ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertRuleRecord{}
	for rows.Next() {
		r, err := scanAlertRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpsertAlertRule(ctx context.Context, input UpsertAlertRuleInput) (*AlertRuleRecord, error) {
	window := input.WindowMinutes
	if window <= 0 {
		window = 15
	}
	var id string
	if input.ID == "" {
		err := s.pool.QueryRow(ctx, `
			INSERT INTO admin_alert_rules (name, metric, threshold, window_minutes, enabled)
			VALUES ($1, $2, $3::numeric, $4, $5) RETURNING id::text`,
			input.Name, input.Metric, input.Threshold, window, input.Enabled).Scan(&id)
		if err != nil {
			return nil, err
		}
	} else {
		tag, err := s.pool.Exec(ctx, `
			UPDATE admin_alert_rules SET name = $2, metric = $3, threshold = $4::numeric,
				window_minutes = $5, enabled = $6, updated_at = now()
			WHERE id = $1::uuid`,
			input.ID, input.Name, input.Metric, input.Threshold, window, input.Enabled)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, nil
		}
		id = input.ID
	}
	rows, err := s.pool.Query(ctx, `SELECT `+alertRuleCols+` FROM admin_alert_rules WHERE id = $1::uuid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanAlertRule(rows)
}

func (s *PostgresStore) DeleteAlertRule(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM admin_alert_rules WHERE id = $1::uuid`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

const alertEventCols = `e.id::text, e.rule_id::text, COALESCE(r.name, ''), e.metric,
	e.value::text, e.threshold::text, e.message, e.status, e.created_at, e.resolved_at`

func scanAlertEvent(rows pgx.Rows) (*AlertEventRecord, error) {
	var e AlertEventRecord
	if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.Metric, &e.Value, &e.Threshold,
		&e.Message, &e.Status, &e.CreatedAt, &e.ResolvedAt); err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *PostgresStore) OpenAlertEventForRule(ctx context.Context, ruleID string) (*AlertEventRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+alertEventCols+`
		FROM admin_alert_events e LEFT JOIN admin_alert_rules r ON r.id = e.rule_id
		WHERE e.rule_id = $1::uuid AND e.status = 'open'
		ORDER BY e.created_at DESC LIMIT 1`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanAlertEvent(rows)
}

func (s *PostgresStore) InsertAlertEvent(ctx context.Context, rule AlertRuleRecord, value, message string, at time.Time) (*AlertEventRecord, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO admin_alert_events (rule_id, metric, value, threshold, message, status, created_at)
		VALUES ($1::uuid, $2, $3::numeric, $4::numeric, $5, 'open', $6::timestamptz)
		RETURNING id::text`,
		rule.ID, rule.Metric, value, rule.Threshold, message, at).Scan(&id)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+alertEventCols+`
		FROM admin_alert_events e LEFT JOIN admin_alert_rules r ON r.id = e.rule_id
		WHERE e.id = $1::uuid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanAlertEvent(rows)
}

func (s *PostgresStore) ResolveAlertEvent(ctx context.Context, id string, at time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE admin_alert_events SET status = 'resolved', resolved_at = $2::timestamptz
		WHERE id = $1::uuid AND status = 'open'`, id, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PostgresStore) ResolveOpenAlertEventsForRule(ctx context.Context, ruleID string, at time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE admin_alert_events SET status = 'resolved', resolved_at = $2::timestamptz
		WHERE rule_id = $1::uuid AND status = 'open'`, ruleID, at)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *PostgresStore) ListAlertEvents(ctx context.Context, status string, limit int64) ([]AlertEventRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+alertEventCols+`
		FROM admin_alert_events e LEFT JOIN admin_alert_rules r ON r.id = e.rule_id
		WHERE ($1::text IS NULL OR e.status = $1)
		ORDER BY e.created_at DESC LIMIT $2`, nullIfEmpty(status), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertEventRecord{}
	for rows.Next() {
		e, err := scanAlertEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func newUUID() string { return uuid.NewString() }
