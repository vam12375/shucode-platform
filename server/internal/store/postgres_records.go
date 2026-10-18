package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Postgres 实现:设备凭据 / 登录流 / Release / 模型目录 / 上游供应商。

const deviceTokenCols = `id::text, user_id::text, name, platform, token_hash, token_ciphertext, token_hint, created_at, last_used_at, revoked_at`

func scanDeviceToken(rows pgx.Rows) (*DeviceTokenRecord, error) {
	var (
		t        DeviceTokenRecord
		platform *string
		userID   *string
	)
	if err := rows.Scan(&t.ID, &userID, &t.Name, &platform, &t.TokenHash, &t.TokenCiphertext, &t.TokenHint, &t.CreatedAt, &t.LastUsedAt, &t.RevokedAt); err != nil {
		return nil, err
	}
	t.UserID = derefString2(userID)
	t.Platform = derefString2(platform)
	return &t, nil
}

func (s *PostgresStore) CreateDeviceToken(ctx context.Context, record DeviceTokenRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO device_tokens (id, user_id, name, platform, token_hash, token_ciphertext, token_hint, created_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8::timestamptz)`,
		record.ID, record.UserID, record.Name, emptyToNil(record.Platform),
		record.TokenHash, record.TokenCiphertext, record.TokenHint, record.CreatedAt)
	return err
}

func (s *PostgresStore) FindDeviceTokenByHash(ctx context.Context, tokenHash string) (*DeviceTokenRecord, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+deviceTokenCols+` FROM device_tokens WHERE token_hash = $1 LIMIT 1`, tokenHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanDeviceToken(rows)
}

func (s *PostgresStore) FindDeviceTokenByID(ctx context.Context, id string) (*DeviceTokenRecord, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+deviceTokenCols+` FROM device_tokens WHERE id = $1::uuid LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanDeviceToken(rows)
}

func (s *PostgresStore) ListDeviceTokens(ctx context.Context, userID string) ([]DeviceTokenRecord, error) {
	var rows pgx.Rows
	var err error
	if userID != "" {
		rows, err = s.pool.Query(ctx,
			`SELECT `+deviceTokenCols+` FROM device_tokens WHERE user_id = $1::uuid ORDER BY created_at DESC`, userID)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT `+deviceTokenCols+` FROM device_tokens ORDER BY created_at DESC LIMIT 500`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeviceTokenRecord{}
	for rows.Next() {
		t, err := scanDeviceToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *PostgresStore) TouchDeviceToken(ctx context.Context, id string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE device_tokens SET last_used_at = $2::timestamptz WHERE id = $1::uuid`, id, at)
	return err
}

func (s *PostgresStore) RevokeDeviceToken(ctx context.Context, id string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE device_tokens SET revoked_at = $2::timestamptz WHERE id = $1::uuid`, id, at)
	return err
}

func (s *PostgresStore) RevokeDeviceTokensForUser(ctx context.Context, userID string, at time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE device_tokens SET revoked_at = $2::timestamptz
		WHERE user_id = $1::uuid AND revoked_at IS NULL`, userID, at)
	return tag.RowsAffected(), err
}

// ---- login flows ----

const loginFlowCols = `id::text, poll_token_hash, status, user_id::text, device_name, device_platform, created_at, expires_at, authorized_at, consumed_at, token_id::text, last_polled_at`

func scanLoginFlow(rows pgx.Rows) (*LoginFlowRecord, error) {
	var (
		f        LoginFlowRecord
		userID   *string
		platform *string
		tokenID  *string
		status   string
	)
	if err := rows.Scan(&f.ID, &f.PollTokenHash, &status, &userID, &f.DeviceName, &platform, &f.CreatedAt, &f.ExpiresAt, &f.AuthorizedAt, &f.ConsumedAt, &tokenID, &f.LastPolledAt); err != nil {
		return nil, err
	}
	f.Status = LoginFlowStatus(status)
	f.UserID = derefString2(userID)
	f.DevicePlatform = derefString2(platform)
	f.TokenID = derefString2(tokenID)
	return &f, nil
}

func (s *PostgresStore) CreateLoginFlow(ctx context.Context, record LoginFlowRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO login_flows (id, poll_token_hash, status, device_name, device_platform, created_at, expires_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::timestamptz, $7::timestamptz)`,
		record.ID, record.PollTokenHash, string(record.Status),
		record.DeviceName, emptyToNil(record.DevicePlatform), record.CreatedAt, record.ExpiresAt)
	return err
}

func (s *PostgresStore) GetLoginFlow(ctx context.Context, id string) (*LoginFlowRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+loginFlowCols+` FROM login_flows WHERE id = $1::uuid LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanLoginFlow(rows)
}

func (s *PostgresStore) UpdateLoginFlow(ctx context.Context, id string, patch UpdateLoginFlowPatch) (*LoginFlowRecord, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE login_flows SET
			status = COALESCE($2, status),
			user_id = COALESCE($3::uuid, user_id),
			authorized_at = COALESCE($4::timestamptz, authorized_at),
			consumed_at = COALESCE($5::timestamptz, consumed_at),
			token_id = COALESCE($6::uuid, token_id),
			last_polled_at = COALESCE($7::timestamptz, last_polled_at)
		WHERE id = $1::uuid
		RETURNING `+loginFlowCols,
		id, nullableFlowStatus(patch.Status), nullableStrPtr(patch.UserID),
		nullableTime(patch.AuthorizedAt), nullableTime(patch.ConsumedAt),
		nullableStrPtr(patch.TokenID), nullableTime(patch.LastPolledAt))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanLoginFlow(rows)
}

func (s *PostgresStore) ConsumeLoginFlow(ctx context.Context, id string, consumedAt time.Time) (*LoginFlowRecord, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE login_flows SET status = 'consumed', consumed_at = $2::timestamptz
		WHERE id = $1::uuid AND status = 'authorized'
		RETURNING `+loginFlowCols, id, consumedAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanLoginFlow(rows)
}

func nullableFlowStatus(s *LoginFlowStatus) any {
	if s == nil {
		return nil
	}
	return string(*s)
}

func nullableStrPtr(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func emptyToNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---- releases ----

const releaseCols = `revision, content, published_at, published_by::text, is_active`

func scanRelease(rows pgx.Rows) (*ReleaseRecord, error) {
	var (
		r           ReleaseRecord
		publishedBy *string
	)
	if err := rows.Scan(&r.Revision, &r.Content, &r.PublishedAt, &publishedBy, &r.IsActive); err != nil {
		return nil, err
	}
	r.PublishedBy = derefString2(publishedBy)
	return &r, nil
}

func (s *PostgresStore) InsertRelease(ctx context.Context, input InsertReleaseInput) (*ReleaseRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		INSERT INTO builtin_releases (revision, content, published_by, is_active)
		VALUES ($1, $2, $3::uuid, NOT EXISTS (SELECT 1 FROM builtin_releases WHERE is_active))
		RETURNING `+releaseCols,
		input.Revision, input.Content, emptyToNil(input.PublishedBy))
	if err != nil {
		if uniqueViolation(err) {
			return nil, fmt.Errorf("%w: %d", ErrReleaseTaken, input.Revision)
		}
		return nil, err
	}
	if !rows.Next() {
		err := rows.Err()
		rows.Close()
		if err != nil && uniqueViolation(err) {
			return nil, fmt.Errorf("%w: %d", ErrReleaseTaken, input.Revision)
		}
		return nil, err
	}
	record, err := scanRelease(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *PostgresStore) ListReleases(ctx context.Context) ([]ReleaseRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+releaseCols+` FROM builtin_releases ORDER BY revision DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReleaseRecord{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetRelease(ctx context.Context, revision int64) (*ReleaseRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+releaseCols+` FROM builtin_releases WHERE revision = $1 LIMIT 1`, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanRelease(rows)
}

func (s *PostgresStore) GetActiveRelease(ctx context.Context) (*ReleaseRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+releaseCols+` FROM builtin_releases WHERE is_active LIMIT 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanRelease(rows)
}

func (s *PostgresStore) ActivateRelease(ctx context.Context, revision int64) (*ReleaseRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE builtin_releases SET is_active = false WHERE is_active AND revision <> $1`, revision); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `UPDATE builtin_releases SET is_active = true WHERE revision = $1 RETURNING `+releaseCols, revision)
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		rows.Close()
		return nil, rows.Err()
	}
	record, err := scanRelease(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *PostgresStore) GetMaxReleaseRevision(ctx context.Context) (*int64, error) {
	var max *int64
	if err := s.pool.QueryRow(ctx, `SELECT max(revision) AS max_revision FROM builtin_releases`).Scan(&max); err != nil {
		return nil, err
	}
	return max, nil
}

// ---- models ----

const modelCols = `model_id, display_name, upstream_model, upstream_provider_id::text,
	input_price_per_mtok::text, output_price_per_mtok::text,
	cache_read_price_per_mtok::text, cache_write_price_per_mtok::text,
	off_peak_multiplier::text, enabled, sort_order, created_at, updated_at`

func scanModel(rows pgx.Rows) (*PlatformModelRecord, error) {
	var (
		m          PlatformModelRecord
		upstream   *string
		providerID *string
		inP, outP  *string
		crP, cwP   *string
		offPeak    *string
	)
	if err := rows.Scan(&m.ModelID, &m.DisplayName, &upstream, &providerID, &inP, &outP, &crP, &cwP, &offPeak, &m.Enabled, &m.SortOrder, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.UpstreamModel = derefString2(upstream)
	m.UpstreamProviderID = derefString2(providerID)
	m.InputPricePerMtok, m.OutputPricePerMtok = derefString2(inP), derefString2(outP)
	m.CacheReadPricePerMtok, m.CacheWritePricePerMtok = derefString2(crP), derefString2(cwP)
	m.OffPeakMultiplier = derefString2(offPeak)
	if m.OffPeakMultiplier == "" {
		m.OffPeakMultiplier = "1.000000"
	}
	return &m, nil
}

func (s *PostgresStore) ListModels(ctx context.Context, includeDisabled bool) ([]PlatformModelRecord, error) {
	cond := " WHERE enabled"
	if includeDisabled {
		cond = ""
	}
	rows, err := s.pool.Query(ctx, `SELECT `+modelCols+` FROM platform_models`+cond+` ORDER BY sort_order ASC, model_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlatformModelRecord{}
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetModel(ctx context.Context, modelID string) (*PlatformModelRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+modelCols+` FROM platform_models WHERE model_id = $1 LIMIT 1`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanModel(rows)
}

func (s *PostgresStore) UpsertModel(ctx context.Context, input UpsertModelInput) (*PlatformModelRecord, error) {
	cacheRead := "0"
	if input.CacheReadPricePerMtok != nil {
		cacheRead = *input.CacheReadPricePerMtok
	}
	cacheWrite := "0"
	if input.CacheWritePricePerMtok != nil {
		cacheWrite = *input.CacheWritePricePerMtok
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	sortOrder := int64(0)
	if input.SortOrder != nil {
		sortOrder = *input.SortOrder
	}
	offPeak := "1"
	if input.OffPeakMultiplier != nil {
		offPeak = *input.OffPeakMultiplier
	}
	// upstreamModel / upstreamProviderId:缺省=保持、显式=覆盖(可为空串清除)。
	rows, err := s.pool.Query(ctx, `
		INSERT INTO platform_models (
			model_id, display_name, upstream_model, upstream_provider_id,
			input_price_per_mtok, output_price_per_mtok,
			cache_read_price_per_mtok, cache_write_price_per_mtok,
			off_peak_multiplier, enabled, sort_order
		) VALUES (
			$1, $2, $3, $4::uuid,
			$5::numeric, $6::numeric, $7::numeric, $8::numeric, $9::numeric, $10, $11
		)
		ON CONFLICT (model_id) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			upstream_model = CASE WHEN $12::boolean THEN EXCLUDED.upstream_model ELSE platform_models.upstream_model END,
			upstream_provider_id = CASE WHEN $13::boolean THEN EXCLUDED.upstream_provider_id ELSE platform_models.upstream_provider_id END,
			input_price_per_mtok = EXCLUDED.input_price_per_mtok,
			output_price_per_mtok = EXCLUDED.output_price_per_mtok,
			cache_read_price_per_mtok = EXCLUDED.cache_read_price_per_mtok,
			cache_write_price_per_mtok = EXCLUDED.cache_write_price_per_mtok,
			off_peak_multiplier = EXCLUDED.off_peak_multiplier,
			enabled = EXCLUDED.enabled,
			sort_order = EXCLUDED.sort_order,
			updated_at = now()
		RETURNING `+modelCols,
		input.ModelID, input.DisplayName,
		nullIfEmpty(input.UpstreamModel), nullIfEmpty(input.UpstreamProviderID),
		input.InputPricePerMtok, input.OutputPricePerMtok, cacheRead, cacheWrite,
		offPeak, enabled, sortOrder,
		input.ProvideUpstreamModel, input.ProvideUpstreamProviderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanModel(rows)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *PostgresStore) DeleteModel(ctx context.Context, modelID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM platform_models WHERE model_id = $1 RETURNING model_id`, modelID)
	return tag.RowsAffected() > 0, err
}

// ---- upstream providers ----

const providerCols = `id::text, name, base_url, protocol, api_key_ciphertext, api_key_hint, anthropic_version, timeout_ms, enabled,
	COALESCE(disabled_mode, '') AS disabled_mode, auto_disabled_at, COALESCE(auto_disabled_reason, ''),
	consecutive_failures, last_test_at, last_test_ok, last_test_status, last_test_latency_ms,
	COALESCE(last_test_model, ''), COALESCE(last_test_detail, '')`

func scanProvider(rows pgx.Rows) (*UpstreamProviderRecord, error) {
	var (
		p          UpstreamProviderRecord
		apiKey     *string
		hint       *string
		version    *string
		timeout    *int64
		autoReason *string
		lastModel  *string
		lastDetail *string
		protocol   string
	)
	if err := rows.Scan(&p.ID, &p.Name, &p.BaseURL, &protocol, &apiKey, &hint, &version, &timeout, &p.Enabled,
		&p.DisabledMode, &p.AutoDisabledAt, &autoReason, &p.ConsecutiveFailures,
		&p.LastTestAt, &p.LastTestOk, &p.LastTestStatus, &p.LastTestLatencyMs, &lastModel, &lastDetail); err != nil {
		return nil, err
	}
	p.Protocol = UpstreamProtocol(protocol)
	p.APIKeyCiphertext, p.APIKeyHint = derefString2(apiKey), derefString2(hint)
	p.AnthropicVersion = derefString2(version)
	p.AutoDisabledReason = derefString2(autoReason)
	p.LastTestModel, p.LastTestDetail = derefString2(lastModel), derefString2(lastDetail)
	p.TimeoutMs = derefInt64Ptr(timeout)
	return &p, nil
}

func derefInt64Ptr(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func (s *PostgresStore) ListUpstreamProviders(ctx context.Context, includeDisabled bool) ([]UpstreamProviderRecord, error) {
	cond := " WHERE enabled"
	if includeDisabled {
		cond = ""
	}
	rows, err := s.pool.Query(ctx, `SELECT `+providerCols+` FROM upstream_providers`+cond+` ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UpstreamProviderRecord{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetUpstreamProvider(ctx context.Context, id string) (*UpstreamProviderRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+providerCols+` FROM upstream_providers WHERE id = $1::uuid LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanProvider(rows)
}

func (s *PostgresStore) CreateUpstreamProvider(ctx context.Context, input CreateUpstreamProviderInput) (*UpstreamProviderRecord, error) {
	rows, err := s.pool.Query(ctx, `
		INSERT INTO upstream_providers (
			name, base_url, protocol, api_key_ciphertext, api_key_hint,
			anthropic_version, timeout_ms, enabled
		) VALUES (
			trim($1), $2, $3, $4, $5, $6, $7, $8
		)
		RETURNING `+providerCols,
		input.Name, input.BaseURL, string(input.Protocol),
		emptyToNil(input.APIKeyCiphertext), emptyToNil(input.APIKeyHint),
		input.AnthropicVersion, input.TimeoutMs, derefBool(input.Enabled, true))
	if err != nil {
		if uniqueViolation(err) {
			return nil, fmt.Errorf("%w: %s", ErrProviderNameTaken, strings.TrimSpace(input.Name))
		}
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			if uniqueViolation(err) {
				return nil, fmt.Errorf("%w: %s", ErrProviderNameTaken, strings.TrimSpace(input.Name))
			}
			return nil, err
		}
		return nil, nil
	}
	return scanProvider(rows)
}

func (s *PostgresStore) UpdateUpstreamProvider(ctx context.Context, id string, patch UpdateUpstreamProviderPatch) (*UpstreamProviderRecord, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE upstream_providers SET
			name = COALESCE($2, name),
			base_url = COALESCE($3, base_url),
			protocol = COALESCE($4, protocol),
			api_key_ciphertext = CASE WHEN $5::boolean THEN $6 ELSE api_key_ciphertext END,
			api_key_hint = CASE WHEN $5::boolean THEN $7 ELSE api_key_hint END,
			anthropic_version = CASE WHEN $8::boolean THEN $9 ELSE anthropic_version END,
			timeout_ms = CASE WHEN $10::boolean THEN $11 ELSE timeout_ms END,
			enabled = COALESCE($12::boolean, enabled)
		WHERE id = $1::uuid
		RETURNING `+providerCols,
		id, patch.Name, patch.BaseURL, nullableProtocol(patch.Protocol),
		patch.ProvideAPIKey, emptyToNil(patch.APIKeyCiphertext), emptyToNil(patch.APIKeyHint),
		patch.ProvideAnthropicVersion, patch.AnthropicVersion,
		patch.ProvideTimeoutMs, patch.TimeoutMs,
		patch.Enabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanProvider(rows)
}

func nullableProtocol(p *UpstreamProtocol) any {
	if p == nil {
		return nil
	}
	return string(*p)
}

func (s *PostgresStore) DeleteUpstreamProvider(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM upstream_providers WHERE id = $1::uuid RETURNING id`, id)
	return tag.RowsAffected() > 0, err
}

func (s *PostgresStore) RecordProviderTestResult(ctx context.Context, id string, result ProviderTestOutcome) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE upstream_providers SET
			last_test_at = now(), last_test_ok = $2, last_test_kind = $3, last_test_status = $4,
			last_test_latency_ms = $5, last_test_model = $6, last_test_detail = $7
		WHERE id = $1::uuid`,
		id, result.OK, result.Kind, result.Status, result.LatencyMs,
		emptyToNil(result.Model), emptyToNil(result.Detail))
	return err
}

func (s *PostgresStore) ReportUpstreamFailure(ctx context.Context, id, reason string, threshold int64) (bool, error) {
	// 单语句原子「计数 + 条件熔断」;threshold>0 闸门对应文档口径(0=关闭熔断)。
	// 返回值必须是「本次调用触发的停用」,而不是 disabled_mode 的现值:
	// 重新启用后 PG 会保留历史 auto 标记(TS PG 实现的返回 bug),用 enabled 一起判定。
	rows, err := s.pool.Query(ctx, `
		UPDATE upstream_providers SET
			consecutive_failures = consecutive_failures + 1,
			enabled = CASE WHEN $3::boolean AND consecutive_failures + 1 >= $2 THEN false ELSE enabled END,
			disabled_mode = CASE WHEN $3::boolean AND consecutive_failures + 1 >= $2 THEN 'auto' ELSE disabled_mode END,
			auto_disabled_at = CASE WHEN $3::boolean AND consecutive_failures + 1 >= $2 THEN now() ELSE auto_disabled_at END,
			auto_disabled_reason = CASE WHEN $3::boolean AND consecutive_failures + 1 >= $2 THEN $4 ELSE auto_disabled_reason END
		WHERE id = $1::uuid AND enabled
		RETURNING (COALESCE(disabled_mode, '') = 'auto' AND NOT enabled) AS fused`,
		id, threshold, threshold > 0, reason)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	var fused bool
	if err := rows.Scan(&fused); err != nil {
		return false, err
	}
	return fused, nil
}

func (s *PostgresStore) ReportUpstreamSuccess(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE upstream_providers SET consecutive_failures = 0 WHERE id = $1::uuid`, id)
	return err
}
