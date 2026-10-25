package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/pricing"
)

// 内存实现:定价文档 / 审计与额度流水 / 配置 KV / 用量与配额临界区 / 演示种子。

// ---- model pricing ----

func (s *memoryStore) GetModelPricing(_ context.Context) (*PricingDocSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.config[pricing.ModelPricingKVKey]
	if !ok {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return pricing.ParseDoc(value), nil
}

func (s *memoryStore) SaveModelPricing(_ context.Context, entries []PricingEntry, expectedVersion string) (*PricingDocSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var current *PricingDocSnapshot
	if raw, ok := s.config[pricing.ModelPricingKVKey]; ok {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		current = pricing.ParseDoc(value)
	}
	currentVersion := pricing.EmptyVersion
	if current != nil {
		currentVersion = current.Version
	}
	if currentVersion != expectedVersion {
		return nil, nil
	}
	// TS 写入 {entries}:entries 为完整对象数组(未知字段随对象保存)。
	encodedEntries := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		raw, err := entry.MarshalJSON()
		if err != nil {
			return nil, err
		}
		encodedEntries = append(encodedEntries, raw)
	}
	doc, err := json.Marshal(map[string]json.RawMessage{"entries": mustJoinArray(encodedEntries)})
	if err != nil {
		return nil, err
	}
	s.config[pricing.ModelPricingKVKey] = doc
	version := pricing.ComputeDocVersion(entries)
	return &PricingDocSnapshot{Entries: entries, Version: version}, nil
}

func mustJoinArray(items []json.RawMessage) json.RawMessage {
	out, _ := json.Marshal(items)
	return out
}

// ---- admin audit + 额度流水(操作者邮箱在读取时 join,与 Postgres left join 一致) ----

func (s *memoryStore) InsertAuditLog(_ context.Context, entry InsertAuditLogInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.EventID != "" {
		if _, exists := s.auditEventIDs[entry.EventID]; exists {
			return nil
		}
		s.auditEventIDs[entry.EventID] = struct{}{}
	}
	s.auditLogs = append(s.auditLogs, AdminAuditLogView{
		ID:          uuid.NewString(),
		ActorUserID: entry.ActorUserID,
		TeamID:      entry.TeamID,
		Action:      entry.Action,
		TargetType:  entry.TargetType,
		TargetID:    entry.TargetID,
		Detail:      append([]byte(nil), entry.Detail...),
		CreatedAt:   time.Now(),
	})
	return nil
}

func (s *memoryStore) ListAuditLogs(_ context.Context, filter AuditLogFilter) ([]AdminAuditLogView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := clampLimit(filter.Limit)
	out := make([]AdminAuditLogView, 0, len(s.auditLogs))
	for _, entry := range s.auditLogs {
		if filter.TeamID != "" && entry.TeamID != filter.TeamID {
			continue
		}
		if filter.ActorUserID != "" && entry.ActorUserID != filter.ActorUserID {
			continue
		}
		if filter.CursorCreatedAt != nil && filter.CursorID != "" {
			if !cursorOlder(entry.CreatedAt, entry.ID, *filter.CursorCreatedAt, filter.CursorID) {
				continue
			}
		}
		entry.ActorEmail = s.actorEmailLocked(entry.ActorUserID)
		entry.Detail = append([]byte(nil), entry.Detail...)
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memoryStore) InsertQuotaAdjustment(_ context.Context, entry InsertQuotaAdjustmentInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotaAdjusts = append(s.quotaAdjusts, quotaAdjustmentRow{
		TeamQuotaAdjustmentView: TeamQuotaAdjustmentView{
			ID:              uuid.NewString(),
			Action:          entry.Action,
			OldQuotaCredits: entry.OldQuotaCredits,
			NewQuotaCredits: entry.NewQuotaCredits,
			OldUnlimited:    entry.OldUnlimited,
			NewUnlimited:    entry.NewUnlimited,
			CreatedAt:       time.Now(),
		},
		TeamID:      entry.TeamID,
		ActorUserID: entry.ActorUserID,
	})
	return nil
}

func (s *memoryStore) ListQuotaAdjustments(_ context.Context, teamID string, limit int64) ([]TeamQuotaAdjustmentView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	out := make([]TeamQuotaAdjustmentView, 0, len(s.quotaAdjusts))
	for _, row := range s.quotaAdjusts {
		if row.TeamID != teamID {
			continue
		}
		view := cloneQuotaView(&row.TeamQuotaAdjustmentView)
		view.ActorEmail = s.actorEmailLocked(row.ActorUserID)
		out = append(out, *view)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > int(limit) {
		out = out[:limit]
	}
	return out, nil
}

func (s *memoryStore) actorEmailLocked(actorUserID string) string {
	if actorUserID == "" {
		return ""
	}
	if user, ok := s.users[actorUserID]; ok {
		return user.Email
	}
	return ""
}

// ---- config kv ----

func (s *memoryStore) GetConfigValue(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if raw, ok := s.config[key]; ok {
		return append([]byte(nil), raw...), nil
	}
	return nil, nil
}

func (s *memoryStore) SetConfigValue(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config[key] = append([]byte(nil), value...)
	return nil
}

// ---- usage ----

func (s *memoryStore) InsertUsage(_ context.Context, record UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	s.usage = append(s.usage, cloneUsage(record))
	return nil
}

func (s *memoryStore) ListUsage(_ context.Context, filter UsageFilter) ([]UsageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := clampLimit(filter.Limit)
	out := make([]UsageRecord, 0, len(s.usage))
	for _, record := range s.usage {
		if usageMatches(record, filter) {
			out = append(out, cloneUsage(record))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memoryStore) AggregateUsage(_ context.Context, filter UsageFilter) ([]UsageAggregateRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	grouped := aggregateLocked(s.usage, filter)
	sort.SliceStable(grouped, func(i, j int) bool { return grouped[i].CreditsMicro > grouped[j].CreditsMicro })
	return grouped, nil
}

func (s *memoryStore) AggregateUsageCosts(_ context.Context, filter UsageFilter) (*UsageCostSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	summary := &UsageCostSummary{Groups: []UsageCostGroup{}, Totals: []UsageCostTotal{}}
	groups := map[string]*UsageCostGroup{}
	totals := map[string]*UsageCostTotal{}
	totalRequests := int64(0)
	for _, record := range s.usage {
		if !usageMatches(record, filter) {
			continue
		}
		totalRequests++
		snapshot := record.PriceSnapshot
		if snapshot == nil || snapshot.Cost.Status != "estimated" ||
			snapshot.Cost.Rates == nil || snapshot.Cost.Rates.Currency == "" ||
			snapshot.Cost.EstimatedMicro == nil || *snapshot.Cost.EstimatedMicro < 0 {
			continue
		}
		currency := snapshot.Cost.Rates.Currency
		key := record.Model + "\x00" + currency
		group := groups[key]
		if group == nil {
			group = &UsageCostGroup{Model: record.Model, Currency: currency}
			groups[key] = group
		}
		group.Requests++
		group.CostMicro += *snapshot.Cost.EstimatedMicro
		group.ChargedMicro += record.CreditsMicro
		if snapshot.Cost.UsageEstimated {
			group.EstimatedUsageRequests++
		}
		total := totals[currency]
		if total == nil {
			total = &UsageCostTotal{Currency: currency}
			totals[currency] = total
		}
		total.Requests++
		total.CostMicro += *snapshot.Cost.EstimatedMicro
		total.ChargedMicro += record.CreditsMicro
		if snapshot.Cost.UsageEstimated {
			total.EstimatedUsageRequests++
		}
		summary.EstimatedRequests++
		if snapshot.Cost.UsageEstimated {
			summary.EstimatedUsageRequests++
		}
	}
	for _, group := range groups {
		summary.Groups = append(summary.Groups, *group)
	}
	sort.SliceStable(summary.Groups, func(i, j int) bool {
		if summary.Groups[i].Model != summary.Groups[j].Model {
			return summary.Groups[i].Model < summary.Groups[j].Model
		}
		return summary.Groups[i].Currency < summary.Groups[j].Currency
	})
	for _, total := range totals {
		summary.Totals = append(summary.Totals, *total)
	}
	sort.SliceStable(summary.Totals, func(i, j int) bool {
		return summary.Totals[i].Currency < summary.Totals[j].Currency
	})
	summary.MissingCostRequests = totalRequests - summary.EstimatedRequests
	if summary.MissingCostRequests < 0 {
		summary.MissingCostRequests = 0
	}
	return summary, nil
}

func aggregateLocked(usage []UsageRecord, filter UsageFilter) []UsageAggregateRow {
	grouped := map[string]*UsageAggregateRow{}
	for _, record := range usage {
		if !usageMatches(record, filter) {
			continue
		}
		row, ok := grouped[record.Model]
		if !ok {
			row = &UsageAggregateRow{}
			grouped[record.Model] = row
			recordCopy := record
			row.Model = recordCopy.Model
		}
		row.Requests++
		row.InputTokens += record.InputTokens
		row.OutputTokens += record.OutputTokens
		row.CacheReadTokens += record.CacheReadTokens
		row.CacheCreationTokens += record.CacheCreationTokens
		row.CreditsMicro += record.CreditsMicro
		if record.LatencyMs != nil {
			row.LatencyMsSum += *record.LatencyMs
		}
	}
	out := make([]UsageAggregateRow, 0, len(grouped))
	for _, row := range grouped {
		out = append(out, *row)
	}
	return out
}

func (s *memoryStore) AggregateUsageSeries(_ context.Context, filter UsageFilter, granularity UsageSeriesGranularity) ([]UsageSeriesRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	grouped := map[string]*UsageSeriesRow{}
	keys := []string{}
	for _, record := range s.usage {
		if !usageMatches(record, filter) {
			continue
		}
		key := seriesBucket(record.CreatedAt, granularity)
		row, ok := grouped[key]
		if !ok {
			keys = append(keys, key)
			row = &UsageSeriesRow{BucketStart: key}
			grouped[key] = row
		}
		row.Requests++
		row.InputTokens += record.InputTokens
		row.OutputTokens += record.OutputTokens
		row.CacheReadTokens += record.CacheReadTokens
		row.CacheCreationTokens += record.CacheCreationTokens
		row.CreditsMicro += record.CreditsMicro
		if record.LatencyMs != nil {
			row.LatencyMsSum += *record.LatencyMs
		}
	}
	sort.Strings(keys)
	out := make([]UsageSeriesRow, 0, len(keys))
	for _, key := range keys {
		out = append(out, *grouped[key])
	}
	return out, nil
}

// seriesBucket 与 Postgres date_trunc(AT TIME ZONE 'Asia/Shanghai')同语义:
// 天 = "YYYY-MM-DDT00:00:00",小时 = "YYYY-MM-DDTHH:00:00"(naive 无时区)。
func seriesBucket(at time.Time, granularity UsageSeriesGranularity) string {
	shifted := at.UTC().Add(shanghaiOffset).Format("2006-01-02T15:04:05")
	if granularity == SeriesGranularityHour {
		return shifted[:13] + ":00:00"
	}
	return shifted[:10] + "T00:00:00"
}

func usageMatches(record UsageRecord, filter UsageFilter) bool {
	if filter.TeamID != "" && record.TeamID != filter.TeamID {
		return false
	}
	if filter.UserID != "" && record.UserID != filter.UserID {
		return false
	}
	if filter.Models != nil {
		match := false
		for _, model := range filter.Models {
			if record.Model == model {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	if filter.Model != "" && record.Model != filter.Model {
		return false
	}
	if filter.ProviderID != "" && record.ProviderID != filter.ProviderID {
		return false
	}
	if filter.Status != "" && record.Status != filter.Status {
		return false
	}
	if filter.RequestID != "" && record.RequestID != filter.RequestID {
		return false
	}
	if filter.From != nil && record.CreatedAt.Before(*filter.From) {
		return false
	}
	if filter.To != nil && record.CreatedAt.After(*filter.To) {
		return false
	}
	if filter.CursorCreatedAt != nil && filter.CursorID != "" {
		if !cursorOlder(record.CreatedAt, record.ID, *filter.CursorCreatedAt, filter.CursorID) {
			return false
		}
	}
	return true
}

// cursorOlder:(created_at, id) 元组比较。id 按 uuid 二进制序(Postgres uuid 比较),
// 而非字符串序——时间撞在同一秒时翻页位置才稳定。
func cursorOlder(createdAt time.Time, id string, cursorAt time.Time, cursorID string) bool {
	if createdAt.After(cursorAt) {
		return false
	}
	if createdAt.Before(cursorAt) {
		return true
	}
	return uuidLess(id, cursorID)
}

func uuidLess(a, b string) bool {
	ab, aerr := uuid.Parse(strings.ToLower(a))
	bb, berr := uuid.Parse(strings.ToLower(b))
	if aerr != nil || berr != nil {
		return a < b
	}
	return ab.String() < bb.String()
}

func clampLimit(limit *int64) int {
	defaulted := int64(50)
	if limit != nil {
		defaulted = *limit
	}
	if defaulted < 1 {
		defaulted = 1
	}
	if defaulted > 500 {
		defaulted = 500
	}
	return int(defaulted)
}

func cloneQuotaView(v *TeamQuotaAdjustmentView) *TeamQuotaAdjustmentView { c := *v; return &c }

// ---- quota(Gateway 临界区;整数 micro 运算,拒绝 float) ----

func (s *memoryStore) TryReserveQuota(_ context.Context, teamID string, micro int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	team, ok := s.teams[teamID]
	if !ok || team.DisabledAt != nil {
		return false, nil
	}
	withinLimit, err := teamCreditWithinLimit(team.UsedCredits, team.ReservedCredits, team.QuotaCredits, team.Unlimited, micro)
	if err != nil {
		return false, err
	}
	if !withinLimit {
		return false, nil
	}
	reserved, _ := credits.ParseCreditsToMicro(team.ReservedCredits)
	team.ReservedCredits = credits.FormatMicroCredits(reserved + micro)
	s.teams[teamID] = team
	return true, nil
}

func (s *memoryStore) SettleUsage(_ context.Context, record UsageRecord, reservedMicro int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.CreditsMicro < 0 || reservedMicro < 0 || reservedMicro > maxCreditFieldMicro {
		return ErrInvalidCreditCharge
	}
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	capped := false
	if team, ok := s.teams[record.TeamID]; ok {
		reservedBefore := team.ReservedCredits
		reserved, err := credits.ParseCreditsToMicro(reservedBefore)
		if err != nil {
			return err
		}
		team.ReservedCredits = credits.FormatMicroCredits(maxInt64(0, reserved-reservedMicro))
		used, err := credits.ParseCreditsToMicro(team.UsedCredits)
		if err != nil {
			return err
		}
		charge, wasCapped, err := capTeamCreditCharge(team.UsedCredits, reservedBefore, team.QuotaCredits, team.Unlimited, reservedMicro, record.CreditsMicro)
		if err != nil {
			return err
		}
		capped = wasCapped
		record.CreditsMicro = charge
		if capped {
			markCreditChargeCapped(&record)
		}
		team.UsedCredits = credits.FormatMicroCredits(used + charge)
		s.teams[record.TeamID] = team
	}
	s.usage = append(s.usage, cloneUsage(record))
	if capped {
		return ErrCreditChargeCapped
	}
	return nil
}

func (s *memoryStore) ReleaseReservation(_ context.Context, teamID string, micro int64) error {
	if micro < 0 || micro > maxCreditFieldMicro {
		return ErrInvalidCreditCharge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	team, ok := s.teams[teamID]
	if !ok {
		return nil
	}
	reserved, err := credits.ParseCreditsToMicro(team.ReservedCredits)
	if err != nil {
		return err
	}
	team.ReservedCredits = credits.FormatMicroCredits(maxInt64(0, reserved-micro))
	s.teams[teamID] = team
	return nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (s *memoryStore) Dispose(_ context.Context) error { return nil }

// ---- 演示种子(与 pnpm seed 内容一致,便于无数据库体验完整链路) ----

func (s *memoryStore) seedDemo(ctx context.Context) error {
	adminHash, err := crypto.HashPassword("mendao-admin")
	if err != nil {
		return err
	}
	memberHash, err := crypto.HashPassword("mendao-member")
	if err != nil {
		return err
	}
	admin, err := s.CreateUser(ctx, UserInput{
		Email: "admin@mendao.chat", PasswordHash: adminHash,
		DisplayName: "平台超级管理员", Role: RoleRoot,
	})
	if err != nil {
		return err
	}
	member, err := s.CreateUser(ctx, UserInput{
		Email: "member@mendao.chat", PasswordHash: memberHash,
		DisplayName: "演示成员", Role: RoleUser,
	})
	if err != nil {
		return err
	}
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "演示团队", QuotaCredits: "1000.000000", Unlimited: false})
	if err != nil {
		return err
	}
	if err := s.AddMember(ctx, team.ID, admin.ID, MemberRoleOwner); err != nil {
		return err
	}
	if err := s.AddMember(ctx, team.ID, member.ID, MemberRoleMember); err != nil {
		return err
	}
	sortOrder10, sortOrder20 := int64(10), int64(20)
	if _, err := s.UpsertModel(ctx, UpsertModelInput{
		ModelID: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5",
		InputPricePerMtok: "3", OutputPricePerMtok: "15",
		CacheReadPricePerMtok: ptr("0.3"), CacheWritePricePerMtok: ptr("3.75"),
		SortOrder: &sortOrder10,
	}); err != nil {
		return err
	}
	if _, err := s.UpsertModel(ctx, UpsertModelInput{
		ModelID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5",
		InputPricePerMtok: "1", OutputPricePerMtok: "5",
		CacheReadPricePerMtok: ptr("0.1"), CacheWritePricePerMtok: ptr("1.25"),
		SortOrder: &sortOrder20,
	}); err != nil {
		return err
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
