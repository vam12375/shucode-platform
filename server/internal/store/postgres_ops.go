package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vam12375/shucode-platform/server/internal/credits"
)

func creditMicroString(micro int64) string { return credits.FormatMicroCredits(micro) }

func parseMicroString(value string) (int64, error) { return credits.ParseCreditsToMicro(value) }

// Postgres 运维域：渠道池、额度预留状态机、小时 rollup、ops 指标与告警。

// ---- upstream routes ----

const upstreamRouteCols = `r.id::text, r.model_id, r.provider_id::text, COALESCE(p.name, ''),
	r.upstream_model, r.priority, r.weight, r.enabled, r.created_at, r.updated_at`

func scanUpstreamRoute(rows pgx.Rows) (*UpstreamRouteRecord, error) {
	var r UpstreamRouteRecord
	if err := rows.Scan(&r.ID, &r.ModelID, &r.ProviderID, &r.ProviderName,
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
	var inserted string
	err = tx.QueryRow(ctx, `
		INSERT INTO quota_reservations (id, team_id, request_id, reserved_credits, status, created_at, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4::numeric, 'reserved', $5::timestamptz, $6::timestamptz)
		ON CONFLICT (request_id) DO NOTHING
		RETURNING id::text`,
		id, input.TeamID, input.RequestID, creditMicroString(input.Micro),
		createdAt, expiresAt).Scan(&inserted)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ReserveDuplicate, nil
		}
		return "", err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE teams
		SET reserved_credits = reserved_credits + $2::numeric
		WHERE id = $1::uuid
			AND disabled_at IS NULL
			AND (unlimited OR used_credits + reserved_credits + $2::numeric <= quota_credits)`,
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

func (s *PostgresStore) SettleReservation(ctx context.Context, reservationID string, record UsageRecord) error {
	if record.ID == "" {
		record.ID = newUUID()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var (
		teamID   string
		reserved string
		status   string
	)
	err = tx.QueryRow(ctx, `
		SELECT team_id::text, reserved_credits::text, status
		FROM quota_reservations WHERE id = $1::uuid FOR UPDATE`, reservationID).
		Scan(&teamID, &reserved, &status)
	if err != nil {
		return err
	}
	if status == string(ReservationSettled) || status == string(ReservationReleased) {
		return tx.Commit(ctx)
	}
	// reserved 才需要扣回预留；expired 的额度已被 sweeper 释放，只记实扣。
	if status == string(ReservationReserved) {
		if _, err := tx.Exec(ctx, `
			UPDATE teams SET
				reserved_credits = GREATEST(reserved_credits - $2::numeric, 0),
				used_credits = used_credits + $3::numeric
			WHERE id = $1::uuid`, teamID, reserved, creditMicroString(record.CreditsMicro)); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE teams SET used_credits = used_credits + $2::numeric WHERE id = $1::uuid`,
			teamID, creditMicroString(record.CreditsMicro)); err != nil {
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
	return tx.Commit(ctx)
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
	)
	err = tx.QueryRow(ctx, `
		SELECT team_id::text, reserved_credits::text, status
		FROM quota_reservations WHERE id = $1::uuid FOR UPDATE`, reservationID).
		Scan(&teamID, &reserved, &status)
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
	if _, err := tx.Exec(ctx, `
		UPDATE teams SET reserved_credits = GREATEST(reserved_credits - $2::numeric, 0)
		WHERE id = $1::uuid`, teamID, reserved); err != nil {
		return err
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
		WHERE status = 'reserved' AND expires_at < $1::timestamptz
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
		SELECT id::text, team_id::text, request_id, reserved_credits::text, status,
			created_at, expires_at, settled_at, released_at, COALESCE(usage_id::text, '')
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
		if err := rows.Scan(&r.ID, &r.TeamID, &r.RequestID, &reserved, &status,
			&r.CreatedAt, &r.ExpiresAt, &r.SettledAt, &r.ReleasedAt, &r.UsageID); err != nil {
			return nil, err
		}
		r.ReservedMicro, _ = parseMicroString(reserved)
		r.Status = QuotaReservationStatus(status)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- usage export ----

func (s *PostgresStore) ListUsageForExport(ctx context.Context, filter UsageFilter, max int64) ([]UsageRecord, error) {
	if max <= 0 {
		max = 10_000
	}
	if max > 100_000 {
		max = 100_000
	}
	where, args := listUsageWhere(filter)
	rows, err := s.pool.Query(ctx,
		`SELECT `+usageCols+` FROM usage_records `+where+` ORDER BY created_at DESC LIMIT $11`,
		argsList(args, max)...)
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
	_, err := tx.Exec(ctx, `
		INSERT INTO usage_records (
			id, request_id, user_id, team_id, device_token_id, model, upstream_model,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			credits_micro, status, error_type, client_ip, latency_ms, provider_id, created_at
		) VALUES (
			$1::uuid, $2, $3::uuid, $4::uuid, $5::uuid, $6, $7,
			$8, $9, $10, $11, $12, $13, $14, $15, $16, $17::uuid, $18::timestamptz
		)`,
		record.ID, record.RequestID, record.UserID, record.TeamID, nullIfEmpty(record.DeviceTokenID),
		record.Model, record.UpstreamModel, record.InputTokens, record.OutputTokens,
		record.CacheReadTokens, record.CacheCreationTokens, record.CreditsMicro, string(record.Status),
		emptyToNil(record.ErrorType), emptyToNil(record.ClientIP), record.LatencyMs,
		nullIfEmpty(record.ProviderID), record.CreatedAt)
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
			(SELECT count(*) FROM quota_reservations WHERE status = 'reserved')::bigint
		FROM usage_records WHERE created_at >= $1`, since).
		Scan(&out.Requests, &out.SuccessRequests, &out.ErrorRequests, &out.AbortedRequests,
			&out.InputTokens, &out.OutputTokens, &out.CacheReadTokens, &out.CacheCreationTokens,
			&out.CreditsMicro, &out.P50LatencyMs, &out.P95LatencyMs, &out.P99LatencyMs,
			&out.ActiveDevices, &out.OpenReservations)
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
