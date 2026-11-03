package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/credits"
)

const creditLedgerDefaultLimit int64 = 50
const creditLedgerMaxLimit int64 = 100

func normalizeCreditLedgerFilter(filter CreditLedgerFilter) (CreditLedgerFilter, error) {
	if !canonicalUUID(filter.TeamID) || (filter.UsageUserID != "" && !canonicalUUID(filter.UsageUserID)) {
		return filter, ErrInvalidInput
	}
	if (filter.CursorCreatedAt == nil) != (filter.CursorSource == "" && filter.CursorID == "") {
		return filter, ErrInvalidInput
	}
	if filter.CursorCreatedAt != nil &&
		((filter.CursorSource != "usage" && filter.CursorSource != "quota") || !canonicalUUID(filter.CursorID)) {
		return filter, ErrInvalidInput
	}
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		return filter, ErrInvalidInput
	}
	if filter.Limit <= 0 {
		filter.Limit = creditLedgerDefaultLimit
	}
	if filter.Limit > creditLedgerMaxLimit {
		filter.Limit = creditLedgerMaxLimit
	}
	return filter, nil
}

func ledgerSummary(items []CreditLedgerItem) CreditLedgerSummary {
	in, out, net := new(big.Int), new(big.Int), new(big.Int)
	var count int64
	for _, item := range items {
		delta := big.NewInt(item.DeltaMicro)
		net.Add(net, delta)
		if item.DeltaMicro > 0 {
			in.Add(in, delta)
		} else if item.DeltaMicro < 0 {
			out.Sub(out, delta)
		}
		count++
	}
	return CreditLedgerSummary{
		TotalCreditsInMicro:  in.String(),
		TotalCreditsOutMicro: out.String(),
		NetCreditsMicro:      net.String(),
		EntryCount:           count,
	}
}

func ledgerItemOlder(a, b CreditLedgerItem) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	if a.Source != b.Source {
		return a.Source < b.Source
	}
	return a.ID < b.ID
}

func ledgerItemBeforeCursor(item CreditLedgerItem, filter CreditLedgerFilter) bool {
	if filter.CursorCreatedAt == nil {
		return true
	}
	cursor := CreditLedgerItem{CreatedAt: *filter.CursorCreatedAt, Source: filter.CursorSource, ID: filter.CursorID}
	return ledgerItemOlder(item, cursor)
}

func (s *memoryStore) ListCreditLedger(_ context.Context, rawFilter CreditLedgerFilter) (CreditLedgerPage, error) {
	filter, err := normalizeCreditLedgerFilter(rawFilter)
	if err != nil {
		return CreditLedgerPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	all := make([]CreditLedgerItem, 0)
	for _, usage := range s.usage {
		// 封顶结算会标记请求失败但仍实际扣费；流水按非零实扣筛选，不能按请求成功状态漏账。
		if usage.TeamID != filter.TeamID || usage.CreditsMicro <= 0 ||
			(filter.UsageUserID != "" && usage.UserID != filter.UsageUserID) ||
			(filter.From != nil && usage.CreatedAt.Before(*filter.From)) ||
			(filter.To != nil && usage.CreatedAt.After(*filter.To)) {
			continue
		}
		copy := cloneUsage(usage)
		var actorName *string
		if user, ok := s.users[usage.UserID]; ok {
			name := user.DisplayName
			actorName = &name
		}
		all = append(all, CreditLedgerItem{Source: "usage", ID: usage.ID, CreatedAt: usage.CreatedAt, Usage: &copy, DeltaMicro: -usage.CreditsMicro, ActorName: actorName})
	}
	for _, adjustment := range s.quotaAdjusts {
		if adjustment.TeamID != filter.TeamID ||
			(filter.From != nil && adjustment.CreatedAt.Before(*filter.From)) ||
			(filter.To != nil && adjustment.CreatedAt.After(*filter.To)) {
			continue
		}
		oldQuota, oldErr := credits.ParseCreditsToMicro(adjustment.OldQuotaCredits)
		newQuota, newErr := credits.ParseCreditsToMicro(adjustment.NewQuotaCredits)
		if oldErr != nil || newErr != nil {
			return CreditLedgerPage{}, ErrInvalidInput
		}
		delta := newQuota - oldQuota
		if delta == 0 {
			continue
		}
		var actorName *string
		if actor, ok := s.users[adjustment.ActorUserID]; ok && adjustment.ActorUserID != "" {
			name := actor.DisplayName
			actorName = &name
		}
		all = append(all, CreditLedgerItem{Source: "quota", ID: adjustment.ID, CreatedAt: adjustment.CreatedAt, QuotaAction: adjustment.Action, DeltaMicro: delta, ActorName: actorName})
	}

	summary := ledgerSummary(all)
	page := make([]CreditLedgerItem, 0, len(all))
	for _, item := range all {
		if ledgerItemBeforeCursor(item, filter) {
			page = append(page, item)
		}
	}
	sort.Slice(page, func(i, j int) bool { return ledgerItemOlder(page[j], page[i]) })
	hasMore := len(page) > int(filter.Limit)
	if hasMore {
		page = page[:filter.Limit]
	}
	return CreditLedgerPage{Items: page, HasMore: hasMore, Summary: summary}, nil
}

func (s *PostgresStore) ListCreditLedger(ctx context.Context, rawFilter CreditLedgerFilter) (CreditLedgerPage, error) {
	filter, err := normalizeCreditLedgerFilter(rawFilter)
	if err != nil {
		return CreditLedgerPage{}, err
	}
	// 与内存实现一致：credit_charge_capped 等失败请求也可能已实扣，不能用 status 排除已发生的点数变化。
	const query = `
WITH ledger AS (
	SELECT 'usage'::text COLLATE "C" AS source, u.id::text AS id, u.created_at,
		(-u.credits_micro::numeric) AS delta_micro, NULL::text AS quota_action,
		usage_actor.display_name AS actor_name, u.id AS usage_id
	FROM usage_records u
	LEFT JOIN users usage_actor ON usage_actor.id=u.user_id
	WHERE u.team_id=$1::uuid AND ($2::uuid IS NULL OR u.user_id=$2::uuid)
		AND u.credits_micro > 0
		AND ($3::timestamptz IS NULL OR u.created_at >= $3::timestamptz)
		AND ($4::timestamptz IS NULL OR u.created_at <= $4::timestamptz)
	UNION ALL
	SELECT 'quota'::text COLLATE "C", q.id::text, q.created_at,
		((q.new_quota_credits-q.old_quota_credits)*1000000)::numeric,
		q.action, actor.display_name, NULL::uuid
	FROM team_quota_adjustments q
	LEFT JOIN users actor ON actor.id=q.actor_user_id
	WHERE q.team_id=$1::uuid AND q.new_quota_credits<>q.old_quota_credits
		AND ($3::timestamptz IS NULL OR q.created_at >= $3::timestamptz)
		AND ($4::timestamptz IS NULL OR q.created_at <= $4::timestamptz)
), summary AS (
	SELECT trunc(COALESCE(sum(delta_micro) FILTER (WHERE delta_micro > 0),0))::text AS total_in,
		trunc(COALESCE(sum(-delta_micro) FILTER (WHERE delta_micro < 0),0))::text AS total_out,
		trunc(COALESCE(sum(delta_micro),0))::text AS net,
		count(*)::bigint AS entry_count
	FROM ledger
), page AS (
	SELECT * FROM ledger
	WHERE ($5::timestamptz IS NULL OR
		(created_at, source COLLATE "C", id COLLATE "C") < ($5::timestamptz, $6::text COLLATE "C", $7::text COLLATE "C"))
	ORDER BY created_at DESC, source COLLATE "C" DESC, id COLLATE "C" DESC
	LIMIT $8
)
SELECT summary.total_in, summary.total_out, summary.net, summary.entry_count,
	page.source, page.id, page.created_at, trunc(page.delta_micro)::text, page.quota_action, page.actor_name,
	CASE WHEN usage_row.id IS NULL THEN NULL ELSE jsonb_build_object(
		'id',usage_row.id::text,'requestId',usage_row.request_id,'userId',usage_row.user_id::text,
		'teamId',usage_row.team_id::text,'billingBindingId',COALESCE(usage_row.billing_binding_id::text,''),
		'deviceTokenId',COALESCE(usage_row.device_token_id::text,''),'model',usage_row.model,
		'upstreamModel',usage_row.upstream_model,'reasoningEffort',COALESCE(usage_row.reasoning_effort,''),
		'reasoningBudgetTokens',usage_row.reasoning_budget_tokens,'inputTokens',usage_row.input_tokens,
		'outputTokens',usage_row.output_tokens,'cacheReadTokens',usage_row.cache_read_tokens,
		'cacheCreationTokens',usage_row.cache_creation_tokens,'creditsMicro',usage_row.credits_micro,
		'priceSnapshot',usage_row.price_snapshot,'status',usage_row.status,'errorType',COALESCE(usage_row.error_type,''),
		'clientIp',COALESCE(usage_row.client_ip,''),'latencyMs',usage_row.latency_ms,
		'providerId',COALESCE(usage_row.provider_id::text,''),'createdAt',usage_row.created_at
	)::text END AS usage_json
FROM summary LEFT JOIN page ON true LEFT JOIN usage_records usage_row ON usage_row.id=page.usage_id
ORDER BY page.created_at DESC NULLS LAST, page.source COLLATE "C" DESC NULLS LAST, page.id COLLATE "C" DESC NULLS LAST`
	args := []any{filter.TeamID, nullIfEmpty(filter.UsageUserID), nullableTime(filter.From), nullableTime(filter.To), nullableTime(filter.CursorCreatedAt), nullIfEmpty(filter.CursorSource), nullIfEmpty(filter.CursorID), filter.Limit + 1}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return CreditLedgerPage{}, err
	}
	defer rows.Close()
	page := CreditLedgerPage{Items: []CreditLedgerItem{}}
	var summaryRead bool
	for rows.Next() {
		var (
			inMicro, outMicro, netMicro                   string
			entryCount                                    int64
			source, id, deltaText, quotaAction, actorName *string
			createdAt                                     *time.Time
			usageJSON                                     *string
		)
		if err := rows.Scan(&inMicro, &outMicro, &netMicro, &entryCount, &source, &id, &createdAt, &deltaText, &quotaAction, &actorName, &usageJSON); err != nil {
			return CreditLedgerPage{}, err
		}
		if !summaryRead {
			page.Summary = CreditLedgerSummary{TotalCreditsInMicro: inMicro, TotalCreditsOutMicro: outMicro, NetCreditsMicro: netMicro, EntryCount: entryCount}
			summaryRead = true
		}
		if id == nil {
			continue
		}
		delta, err := strconv.ParseInt(*deltaText, 10, 64)
		if err != nil {
			return CreditLedgerPage{}, fmt.Errorf("credit ledger delta out of range: %w", err)
		}
		item := CreditLedgerItem{Source: *source, ID: *id, CreatedAt: *createdAt, QuotaAction: derefString2(quotaAction), DeltaMicro: delta}
		if actorName != nil {
			name := *actorName
			item.ActorName = &name
		}
		if usageJSON != nil {
			var usage UsageRecord
			if err := json.Unmarshal([]byte(*usageJSON), &usage); err != nil {
				return CreditLedgerPage{}, err
			}
			item.Usage = &usage
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return CreditLedgerPage{}, err
	}
	if !summaryRead {
		return CreditLedgerPage{}, errors.New("credit ledger query returned no summary row")
	}
	if int64(len(page.Items)) > filter.Limit {
		page.HasMore = true
		page.Items = page.Items[:filter.Limit]
	}
	return page, nil
}
