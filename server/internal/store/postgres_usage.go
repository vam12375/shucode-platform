package store

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/pricing"
)

// Postgres 实现:定价文档 / 审计与额度流水 / 配置 KV / 用量与配额临界区。

// ---- model pricing(内容版本乐观锁,advisory 锁 + FOR UPDATE) ----

func (s *PostgresStore) GetModelPricing(ctx context.Context) (*PricingDocSnapshot, error) {
	var value []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM platform_config WHERE key = $1 LIMIT 1`, pricing.ModelPricingKVKey).Scan(&value)
	if err != nil {
		if queryErrNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return parsePricingBytes(value), nil
}

func parsePricingBytes(raw []byte) *PricingDocSnapshot {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return pricing.ParseDoc(value)
}

// nullableBytes:jsonb 可空参数(nil 原样写 null)。
func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func (s *PostgresStore) SaveModelPricing(ctx context.Context, entries []PricingEntry, expectedVersion string) (*PricingDocSnapshot, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// advising 锁 + FOR UPDATE:读版本、比版本、写文档在同一临界区,
	// 内容版本即锁依据(旧 KV 数据无 version 列也不影响,版本按内容现算)。
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, pricing.ModelPricingKVKey); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT value FROM platform_config WHERE key = $1 FOR UPDATE`, pricing.ModelPricingKVKey)
	if err != nil {
		return nil, err
	}
	var currentRaw []byte
	hasRow := rows.Next()
	if hasRow {
		if err := rows.Scan(&currentRaw); err != nil {
			rows.Close()
			return nil, err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// KV 现值缺省/非对象 ⇒ 乐观锁前置版本 = 空文档版本(与 TS 一致)。
	currentVersion := pricing.EmptyVersion
	if hasRow {
		if snapshot := parsePricingBytes(currentRaw); snapshot != nil {
			currentVersion = snapshot.Version
		}
	}
	if currentVersion != expectedVersion {
		return nil, nil
	}
	doc, err := pricingEntriesDoc(entries)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform_config (key, value, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		pricing.ModelPricingKVKey, doc); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &PricingDocSnapshot{Entries: entries, Version: pricing.ComputeDocVersion(entries)}, nil
}

// snapshotNilEmpty 判断 KV 现值是否视为「不存在」(非对象/缺 entries)。
func snapshotNilEmpty(raw []byte) bool {
	return parsePricingBytes(raw) == nil
}

func pricingEntriesDoc(entries []PricingEntry) ([]byte, error) {
	out := []byte{'{', '"', 'e', 'n', 't', 'r', 'i', 'e', 's', '"', ':', '['}
	for i, entry := range entries {
		if i > 0 {
			out = append(out, ',')
		}
		raw, err := entry.MarshalJSON()
		if err != nil {
			return nil, err
		}
		out = append(out, raw...)
	}
	out = append(out, ']', '}')
	return out, nil
}

// ---- admin audit ----

func (s *PostgresStore) InsertAuditLog(ctx context.Context, entry InsertAuditLogInput) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO admin_audit_logs (event_id, actor_user_id, action, target_type, target_id, detail, team_id)
		VALUES (NULLIF($7, ''), $1::uuid, $2, $3, $4, $5, $6::uuid)
		ON CONFLICT DO NOTHING`,
		emptyToNil(entry.ActorUserID), entry.Action, emptyToNil(entry.TargetType), emptyToNil(entry.TargetID),
		nullableBytes(entry.Detail), nullIfEmpty(entry.TeamID), entry.EventID)
	return err
}

func (s *PostgresStore) ListAuditLogs(ctx context.Context, filter AuditLogFilter) ([]AdminAuditLogView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id::text, COALESCE(a.team_id::text,''), a.actor_user_id::text, a.action, a.target_type, a.target_id, a.detail, a.created_at, u.email
		FROM admin_audit_logs a
		LEFT JOIN users u ON u.id = a.actor_user_id
		WHERE ($1::uuid IS NULL OR a.actor_user_id = $1::uuid)
			AND ($5::uuid IS NULL OR a.team_id=$5::uuid)
			AND (
				$2::timestamptz IS NULL
				OR (a.created_at, a.id) < ($2::timestamptz, $3::uuid)
			)
		ORDER BY a.created_at DESC
		LIMIT $4`,
		emptyToNil(filter.ActorUserID), nullableTime(filter.CursorCreatedAt), nullIfEmpty(filter.CursorID),
		int64(clampLimit(filter.Limit)), nullIfEmpty(filter.TeamID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminAuditLogView{}
	for rows.Next() {
		var (
			v          AdminAuditLogView
			actorID    *string
			email      *string
			targetType *string
			targetID   *string
			detail     []byte
		)
		if err := rows.Scan(&v.ID, &v.TeamID, &actorID, &v.Action, &targetType, &targetID, &detail, &v.CreatedAt, &email); err != nil {
			return nil, err
		}
		v.ActorUserID = derefString2(actorID)
		v.TargetType = derefString2(targetType)
		v.TargetID = derefString2(targetID)
		v.Detail = detail
		v.ActorEmail = derefString2(email)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *PostgresStore) InsertQuotaAdjustment(ctx context.Context, entry InsertQuotaAdjustmentInput) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO team_quota_adjustments (
			team_id, actor_user_id, action,
			old_quota_credits, new_quota_credits, old_unlimited, new_unlimited
		) VALUES (
			$1::uuid, $2::uuid, $3, $4::numeric, $5::numeric, $6, $7)`,
		entry.TeamID, emptyToNil(entry.ActorUserID), entry.Action,
		entry.OldQuotaCredits, entry.NewQuotaCredits, entry.OldUnlimited, entry.NewUnlimited)
	return err
}

func (s *PostgresStore) ListQuotaAdjustments(ctx context.Context, teamID string, limit int64) ([]TeamQuotaAdjustmentView, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT q.id::text, q.action, q.old_quota_credits::text, q.new_quota_credits::text, q.old_unlimited, q.new_unlimited, q.created_at, u.email
		FROM team_quota_adjustments q
		LEFT JOIN users u ON u.id = q.actor_user_id
		WHERE q.team_id = $1::uuid
		ORDER BY q.created_at DESC
		LIMIT $2`, teamID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamQuotaAdjustmentView{}
	for rows.Next() {
		var (
			v          TeamQuotaAdjustmentView
			email      *string
			oldQ, newQ *string
		)
		if err := rows.Scan(&v.ID, &v.Action, &oldQ, &newQ, &v.OldUnlimited, &v.NewUnlimited, &v.CreatedAt, &email); err != nil {
			return nil, err
		}
		v.OldQuotaCredits, v.NewQuotaCredits = derefString2(oldQ), derefString2(newQ)
		v.ActorEmail = derefString2(email)
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- config kv ----

func (s *PostgresStore) GetConfigValue(ctx context.Context, key string) ([]byte, error) {
	rows, err := s.pool.Query(ctx, `SELECT value FROM platform_config WHERE key = $1 LIMIT 1`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var value []byte
	if err := rows.Scan(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func (s *PostgresStore) SetConfigValue(ctx context.Context, key string, value []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO platform_config (key, value, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return err
}

// ---- usage ----

const usageCols = `id::text, request_id, user_id::text, team_id::text, COALESCE(device_token_id::text, '') AS device_token_id,
	model, upstream_model, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
	credits_micro, status, error_type, client_ip, latency_ms, COALESCE(provider_id::text, '') AS provider_id, created_at, price_snapshot,
	COALESCE(reasoning_effort, ''), reasoning_budget_tokens`

func scanUsage(rows pgx.Rows) (*UsageRecord, error) {
	var (
		u             UsageRecord
		errorType     *string
		clientIP      *string
		latency       *int64
		status        string
		priceSnapshot []byte
	)
	if err := rows.Scan(&u.ID, &u.RequestID, &u.UserID, &u.TeamID, &u.DeviceTokenID,
		&u.Model, &u.UpstreamModel, &u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheCreationTokens,
		&u.CreditsMicro, &status, &errorType, &clientIP, &latency, &u.ProviderID, &u.CreatedAt, &priceSnapshot,
		&u.ReasoningEffort, &u.ReasoningBudgetTokens); err != nil {
		return nil, err
	}
	u.Status = UsageStatus(status)
	u.ErrorType = derefString2(errorType)
	u.ClientIP = derefString2(clientIP)
	u.LatencyMs = latency
	if len(priceSnapshot) > 0 {
		if err := json.Unmarshal(priceSnapshot, &u.PriceSnapshot); err != nil {
			return nil, err
		}
	}
	return &u, nil
}

func (s *PostgresStore) InsertUsage(ctx context.Context, record UsageRecord) error {
	if record.ID == "" {
		record.ID = newUUID()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := insertUsageExec(ctx, tx, record); err != nil {
		return err
	}
	if err := upsertRollupExec(ctx, tx, record); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// listUsageWhere:TS postgres.ts 的 WHERE 拼装(可选条件全部用 NULL 短路)。
// 与 TS 的一致性口径:空字符串一律视为「未过滤」(api 层已做白名单/UUID 校验,
// 不会传空串;TS 两个实现在空串行为上本就不一致,此处取内存语义为规范)。
func listUsageWhere(filter UsageFilter) (string, []any) {
	args := []any{
		nullableTime(filter.From), nullableTime(filter.To),
		emptyToNil(filter.TeamID), emptyToNil(filter.UserID),
		emptyToNil(filter.Model), emptyToNil(filter.ProviderID),
		nullableUsageStatus(filter.Status),
		nullableTime(filter.CursorCreatedAt), nullIfEmpty(filter.CursorID),
		nullIfEmpty(filter.RequestID),
	}
	return `
		WHERE ($1::timestamptz IS NULL OR created_at >= $1::timestamptz)
			AND ($2::timestamptz IS NULL OR created_at <= $2::timestamptz)
			AND ($3::uuid IS NULL OR team_id = $3::uuid)
			AND ($4::uuid IS NULL OR user_id = $4::uuid)
			AND ($5::text IS NULL OR model = $5)
			AND ($6::uuid IS NULL OR provider_id = $6::uuid)
			AND ($7::text IS NULL OR status = $7)
			AND (
				$8::timestamptz IS NULL
				OR (created_at, id) < ($8::timestamptz, $9::uuid)
			)
			AND ($10::text IS NULL OR request_id = $10)`, args
}

func nullableUsageStatus(s UsageStatus) any {
	if s == "" {
		return nil
	}
	return string(s)
}

func (s *PostgresStore) ListUsage(ctx context.Context, filter UsageFilter) ([]UsageRecord, error) {
	where, args := listUsageWhere(filter)
	rows, err := s.pool.Query(ctx,
		`SELECT `+usageCols+` FROM usage_records `+where+` ORDER BY created_at DESC LIMIT $11`,
		argsList(args, int64(clampLimit(filter.Limit)))...)
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

func (s *PostgresStore) AggregateUsage(ctx context.Context, filter UsageFilter) ([]UsageAggregateRow, error) {
	where, args := listUsageWhere(filter)
	rows, err := s.pool.Query(ctx, `
		SELECT
			model,
			count(*)::int AS requests,
			COALESCE(sum(input_tokens), 0)::bigint AS input_tokens,
			COALESCE(sum(output_tokens), 0)::bigint AS output_tokens,
			COALESCE(sum(cache_read_tokens), 0)::bigint AS cache_read_tokens,
			COALESCE(sum(cache_creation_tokens), 0)::bigint AS cache_creation_tokens,
			COALESCE(sum(credits_micro), 0)::bigint AS credits_micro,
			COALESCE(sum(latency_ms), 0)::bigint AS latency_ms_sum
		FROM usage_records `+where+`
		GROUP BY model
		ORDER BY credits_micro DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageAggregateRow{}
	for rows.Next() {
		var r UsageAggregateRow
		if err := rows.Scan(&r.Model, &r.Requests, &r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheCreationTokens, &r.CreditsMicro, &r.LatencyMsSum); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) AggregateUsageCosts(ctx context.Context, filter UsageFilter) (*UsageCostSummary, error) {
	where, args := listUsageWhere(filter)
	rows, err := s.pool.Query(ctx, `
		SELECT
			model,
			price_snapshot->'cost'->'rates'->>'currency',
			count(*)::bigint,
			COALESCE(sum((price_snapshot->'cost'->>'estimatedMicro')::bigint), 0)::bigint,
			COALESCE(sum(credits_micro), 0)::bigint,
			count(*) FILTER (WHERE price_snapshot->'cost'->>'usageEstimated' = 'true')::bigint
		FROM usage_records `+where+`
			AND price_snapshot->'cost'->>'status' = 'estimated'
			AND price_snapshot->'cost'->'rates'->>'currency' <> ''
			AND price_snapshot->'cost'->>'estimatedMicro' ~ '^[0-9]+$'
		GROUP BY 1, 2
		ORDER BY 1, 2`, args...)
	if err != nil {
		return nil, err
	}
	summary := &UsageCostSummary{Groups: []UsageCostGroup{}, Totals: []UsageCostTotal{}}
	totals := map[string]*UsageCostTotal{}
	for rows.Next() {
		var group UsageCostGroup
		if err := rows.Scan(&group.Model, &group.Currency, &group.Requests,
			&group.CostMicro, &group.ChargedMicro, &group.EstimatedUsageRequests); err != nil {
			rows.Close()
			return nil, err
		}
		summary.Groups = append(summary.Groups, group)
		summary.EstimatedRequests += group.Requests
		summary.EstimatedUsageRequests += group.EstimatedUsageRequests
		total := totals[group.Currency]
		if total == nil {
			total = &UsageCostTotal{Currency: group.Currency}
			totals[group.Currency] = total
		}
		total.Requests += group.Requests
		total.CostMicro += group.CostMicro
		total.ChargedMicro += group.ChargedMicro
		total.EstimatedUsageRequests += group.EstimatedUsageRequests
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, total := range totals {
		summary.Totals = append(summary.Totals, *total)
	}
	sort.SliceStable(summary.Totals, func(i, j int) bool {
		return summary.Totals[i].Currency < summary.Totals[j].Currency
	})
	var totalRequests int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*)::bigint FROM usage_records `+where, args...).Scan(&totalRequests); err != nil {
		return nil, err
	}
	summary.MissingCostRequests = totalRequests - summary.EstimatedRequests
	if summary.MissingCostRequests < 0 {
		summary.MissingCostRequests = 0
	}
	return summary, nil
}

// isGlobalUsageFilter:无任何维度/游标条件时，趋势图可走小时 rollup。
func isGlobalUsageFilter(filter UsageFilter) bool {
	return filter.TeamID == "" && filter.UserID == "" && filter.Model == "" &&
		filter.ProviderID == "" && filter.Status == "" && filter.RequestID == "" &&
		filter.CursorCreatedAt == nil && filter.CursorID == ""
}

func (s *PostgresStore) AggregateUsageSeries(ctx context.Context, filter UsageFilter, granularity UsageSeriesGranularity) ([]UsageSeriesRow, error) {
	field := "day"
	if granularity == SeriesGranularityHour {
		field = "hour"
	}
	if isGlobalUsageFilter(filter) {
		rows, err := s.pool.Query(ctx, `
			SELECT
				to_char(
					date_trunc($3, bucket_start AT TIME ZONE 'Asia/Shanghai'),
					'YYYY-MM-DD"T"HH24:MI:SS'
				) AS bucket_start,
				COALESCE(sum(requests), 0)::bigint,
				COALESCE(sum(input_tokens), 0)::bigint,
				COALESCE(sum(output_tokens), 0)::bigint,
				COALESCE(sum(cache_read_tokens), 0)::bigint,
				COALESCE(sum(cache_creation_tokens), 0)::bigint,
				COALESCE(sum(credits_micro), 0)::bigint,
				COALESCE(sum(latency_ms_sum), 0)::bigint
			FROM usage_hourly_rollups
			WHERE ($1::timestamptz IS NULL OR bucket_start >= $1::timestamptz)
				AND ($2::timestamptz IS NULL OR bucket_start <= $2::timestamptz)
			GROUP BY bucket_start
			ORDER BY bucket_start ASC`,
			nullableTime(filter.From), nullableTime(filter.To), field)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []UsageSeriesRow{}
		for rows.Next() {
			var r UsageSeriesRow
			if err := rows.Scan(&r.BucketStart, &r.Requests, &r.InputTokens, &r.OutputTokens,
				&r.CacheReadTokens, &r.CacheCreationTokens, &r.CreditsMicro, &r.LatencyMsSum); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
	where, args := listUsageWhere(filter)
	rows, err := s.pool.Query(ctx, `
		SELECT
			to_char(
				date_trunc($11, created_at AT TIME ZONE 'Asia/Shanghai'),
				'YYYY-MM-DD"T"HH24:MI:SS'
			) AS bucket_start,
			count(*)::int AS requests,
			COALESCE(sum(input_tokens), 0)::bigint AS input_tokens,
			COALESCE(sum(output_tokens), 0)::bigint AS output_tokens,
			COALESCE(sum(cache_read_tokens), 0)::bigint AS cache_read_tokens,
			COALESCE(sum(cache_creation_tokens), 0)::bigint AS cache_creation_tokens,
			COALESCE(sum(credits_micro), 0)::bigint AS credits_micro,
			COALESCE(sum(latency_ms), 0)::bigint AS latency_ms_sum
		FROM usage_records `+where+`
		GROUP BY bucket_start
		ORDER BY bucket_start ASC`, append(args, field)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageSeriesRow{}
	for rows.Next() {
		var r UsageSeriesRow
		if err := rows.Scan(&r.BucketStart, &r.Requests, &r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheCreationTokens, &r.CreditsMicro, &r.LatencyMsSum); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func argsList(args []any, extra ...any) []any {
	out := make([]any, 0, len(args)+len(extra))
	out = append(out, args...)
	out = append(out, extra...)
	return out
}

// ---- quota(Gateway 临界区) ----

func (s *PostgresStore) TryReserveQuota(ctx context.Context, teamID string, micro int64) (bool, error) {
	if micro < 0 || micro > maxCreditFieldMicro {
		return false, nil
	}
	creditStr := credits.FormatMicroCredits(micro)
	tag, err := s.pool.Exec(ctx, `
		UPDATE teams
		SET reserved_credits = reserved_credits + $2::numeric
		WHERE id = $1::uuid
			AND disabled_at IS NULL
			AND $2::numeric >= 0
			AND (unlimited OR (used_credits + reserved_credits < quota_credits
				AND used_credits + reserved_credits + $2::numeric <= quota_credits))`,
		teamID, creditStr)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PostgresStore) SettleUsage(ctx context.Context, record UsageRecord, reservedMicro int64) error {
	if record.CreditsMicro < 0 || reservedMicro < 0 || reservedMicro > maxCreditFieldMicro {
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
	var quota, used, teamReserved string
	var unlimited bool
	if err := tx.QueryRow(ctx, `
		SELECT quota_credits::text, used_credits::text, reserved_credits::text, unlimited
		FROM teams WHERE id = $1::uuid FOR UPDATE`, record.TeamID).
		Scan(&quota, &used, &teamReserved, &unlimited); err != nil {
		return err
	}
	charge, capped, err := capTeamCreditCharge(used, teamReserved, quota, unlimited, reservedMicro, record.CreditsMicro)
	if err != nil {
		return err
	}
	record.CreditsMicro = charge
	if capped {
		markCreditChargeCapped(&record)
	}
	reservedCredits := credits.FormatMicroCredits(reservedMicro)
	actualCredits := credits.FormatMicroCredits(charge)
	// 扣减与结算在同一事务:更强一行的更新先做,再写 usage 账本。
	if _, err := tx.Exec(ctx, `
		UPDATE teams SET
			reserved_credits = GREATEST(reserved_credits - $2::numeric, 0),
			used_credits = used_credits + $3::numeric
		WHERE id = $1::uuid`,
		record.TeamID, reservedCredits, actualCredits); err != nil {
		return err
	}
	if err := insertUsageExec(ctx, tx, record); err != nil {
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

func (s *PostgresStore) ReleaseReservation(ctx context.Context, teamID string, micro int64) error {
	if micro < 0 || micro > maxCreditFieldMicro {
		return ErrInvalidCreditCharge
	}
	creditStr := credits.FormatMicroCredits(micro)
	_, err := s.pool.Exec(ctx, `
		UPDATE teams SET reserved_credits = GREATEST(reserved_credits - $2::numeric, 0) WHERE id = $1::uuid`,
		teamID, creditStr)
	return err
}
