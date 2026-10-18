package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// 内存实现:设备凭据 / 登录流 / Release / 模型目录 / 上游供应商。
// UpdateUpstreamProvider 与 UpsertModel 采用 Postgres 行为为唯一口径(见 conformance):
// enabled 翻转不做现场清理;模型可选字段缺省时按 EXCLUDED 默认值写入。

// ---- device tokens ----

func (s *memoryStore) CreateDeviceToken(_ context.Context, record DeviceTokenRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deviceTokens[record.ID] = record
	return nil
}

func (s *memoryStore) FindDeviceTokenByHash(_ context.Context, tokenHash string) (*DeviceTokenRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, token := range s.deviceTokens {
		if token.TokenHash == tokenHash {
			return cloneDeviceToken(&token), nil
		}
	}
	return nil, nil
}

func (s *memoryStore) FindDeviceTokenByID(_ context.Context, id string) (*DeviceTokenRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token, ok := s.deviceTokens[id]; ok {
		return cloneDeviceToken(&token), nil
	}
	return nil, nil
}

func (s *memoryStore) ListDeviceTokens(_ context.Context, userID string) ([]DeviceTokenRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DeviceTokenRecord, 0, len(s.deviceTokens))
	for _, token := range s.deviceTokens {
		if userID != "" && token.UserID != userID {
			continue
		}
		out = append(out, token)
	}
	// Postgres:id 按用户过滤则全量返回,否则 LIMIT 500;均按创建时间倒序。
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if userID == "" && len(out) > 500 {
		out = out[:500]
	}
	return out, nil
}

func (s *memoryStore) TouchDeviceToken(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token, ok := s.deviceTokens[id]; ok {
		token.LastUsedAt = &at
		s.deviceTokens[id] = token
	}
	return nil
}

func (s *memoryStore) RevokeDeviceToken(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token, ok := s.deviceTokens[id]; ok {
		token.RevokedAt = &at
		s.deviceTokens[id] = token
	}
	return nil
}

func (s *memoryStore) RevokeDeviceTokensForUser(_ context.Context, userID string, at time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for id, token := range s.deviceTokens {
		if token.UserID == userID && token.RevokedAt == nil {
			token.RevokedAt = &at
			s.deviceTokens[id] = token
			count++
		}
	}
	return count, nil
}

// ---- login flows ----

func (s *memoryStore) CreateLoginFlow(_ context.Context, record LoginFlowRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flows[record.ID] = record
	return nil
}

func (s *memoryStore) GetLoginFlow(_ context.Context, id string) (*LoginFlowRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if flow, ok := s.flows[id]; ok {
		return cloneLoginFlow(&flow), nil
	}
	return nil, nil
}

func (s *memoryStore) UpdateLoginFlow(_ context.Context, id string, patch UpdateLoginFlowPatch) (*LoginFlowRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow, ok := s.flows[id]
	if !ok {
		return nil, nil
	}
	if patch.Status != nil {
		flow.Status = *patch.Status
	}
	if patch.UserID != nil {
		flow.UserID = *patch.UserID
	}
	if patch.AuthorizedAt != nil {
		v := *patch.AuthorizedAt
		flow.AuthorizedAt = &v
	}
	if patch.ConsumedAt != nil {
		v := *patch.ConsumedAt
		flow.ConsumedAt = &v
	}
	if patch.TokenID != nil {
		flow.TokenID = *patch.TokenID
	}
	if patch.LastPolledAt != nil {
		v := *patch.LastPolledAt
		flow.LastPolledAt = &v
	}
	s.flows[id] = flow
	return cloneLoginFlow(&flow), nil
}

func (s *memoryStore) ConsumeLoginFlow(_ context.Context, id string, consumedAt time.Time) (*LoginFlowRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow, ok := s.flows[id]
	if !ok || flow.Status != FlowStatusAuthorized {
		return nil, nil
	}
	flow.Status = FlowStatusConsumed
	flow.ConsumedAt = &consumedAt
	s.flows[id] = flow
	return cloneLoginFlow(&flow), nil
}

// ---- releases ----

func (s *memoryStore) InsertRelease(_ context.Context, input InsertReleaseInput) (*ReleaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.releases[input.Revision]; exists {
		return nil, fmt.Errorf("%w: %d", ErrReleaseTaken, input.Revision)
	}
	record := ReleaseRecord{
		Revision:    input.Revision,
		Content:     append([]byte(nil), input.Content...),
		PublishedAt: time.Now(),
		PublishedBy: input.PublishedBy,
		IsActive:    false,
	}
	// Postgres 语义:NOT EXISTS(active) 时首个插入自动激活。
	active := false
	for _, item := range s.releases {
		if item.IsActive {
			active = true
			break
		}
	}
	if !active {
		record.IsActive = true
	}
	s.releases[record.Revision] = record
	out := record
	return &out, nil
}

func (s *memoryStore) ListReleases(_ context.Context) ([]ReleaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ReleaseRecord, 0, len(s.releases))
	for _, item := range s.releases {
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Revision > out[j].Revision })
	return out, nil
}

func (s *memoryStore) GetRelease(_ context.Context, revision int64) (*ReleaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item, ok := s.releases[revision]; ok {
		return cloneRelease(&item), nil
	}
	return nil, nil
}

func (s *memoryStore) GetActiveRelease(_ context.Context) (*ReleaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.releases {
		if item.IsActive {
			return cloneRelease(&item), nil
		}
	}
	return nil, nil
}

func (s *memoryStore) ActivateRelease(_ context.Context, revision int64) (*ReleaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target, ok := s.releases[revision]
	if !ok {
		return nil, nil
	}
	for id, item := range s.releases {
		if item.IsActive {
			item.IsActive = false
			s.releases[id] = item
		}
	}
	target.IsActive = true
	s.releases[revision] = target
	return cloneRelease(&target), nil
}

func (s *memoryStore) GetMaxReleaseRevision(_ context.Context) (*int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.releases) == 0 {
		return nil, nil
	}
	var max int64
	for revision := range s.releases {
		if revision > max {
			max = revision
		}
	}
	return &max, nil
}

// ---- models ----

func (s *memoryStore) ListModels(_ context.Context, includeDisabled bool) ([]PlatformModelRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PlatformModelRecord, 0, len(s.models))
	for _, model := range s.models {
		if !includeDisabled && !model.Enabled {
			continue
		}
		out = append(out, model)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].ModelID < out[j].ModelID
	})
	return out, nil
}

func (s *memoryStore) GetModel(_ context.Context, modelID string) (*PlatformModelRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if model, ok := s.models[modelID]; ok {
		return cloneModel(&model), nil
	}
	return nil, nil
}

func (s *memoryStore) UpsertModel(_ context.Context, input UpsertModelInput) (*PlatformModelRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, hasExisting := s.models[input.ModelID]
	// 绑定字段语义(与 Postgres CASE 一致):缺省=保持原值,显式=以传入值(可为空串清除)覆盖。
	upstreamModel := existing.UpstreamModel
	upstreamProviderID := existing.UpstreamProviderID
	if input.ProvideUpstreamModel {
		upstreamModel = input.UpstreamModel
	}
	if !hasExisting && !input.ProvideUpstreamModel {
		upstreamModel = ""
	}
	if input.ProvideUpstreamProviderID {
		upstreamProviderID = input.UpstreamProviderID
	}
	if !hasExisting && !input.ProvideUpstreamProviderID {
		upstreamProviderID = ""
	}
	// 可选值缺省时按 Postgres EXCLUDED 口径落默认值,而非保留旧值。
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
	record := PlatformModelRecord{
		ModelID:                input.ModelID,
		DisplayName:            input.DisplayName,
		UpstreamModel:          upstreamModel,
		UpstreamProviderID:     upstreamProviderID,
		InputPricePerMtok:      numeric6(input.InputPricePerMtok),
		OutputPricePerMtok:     numeric6(input.OutputPricePerMtok),
		CacheReadPricePerMtok:  numeric6(cacheRead),
		CacheWritePricePerMtok: numeric6(cacheWrite),
		Enabled:                enabled,
		SortOrder:              sortOrder,
		CreatedAt:              existing.CreatedAt,
		UpdatedAt:              time.Now(),
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = record.UpdatedAt
	}
	s.models[record.ModelID] = record
	return cloneModel(&record), nil
}

func (s *memoryStore) DeleteModel(_ context.Context, modelID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.models[modelID]; !ok {
		return false, nil
	}
	delete(s.models, modelID)
	return true, nil
}

// ---- upstream providers ----

func (s *memoryStore) ListUpstreamProviders(_ context.Context, includeDisabled bool) ([]UpstreamProviderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]UpstreamProviderRecord, 0, len(s.providers))
	for _, provider := range s.providers {
		if !includeDisabled && !provider.Enabled {
			continue
		}
		out = append(out, provider)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *memoryStore) GetUpstreamProvider(_ context.Context, id string) (*UpstreamProviderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if provider, ok := s.providers[id]; ok {
		return cloneProvider(&provider), nil
	}
	return nil, nil
}

func (s *memoryStore) CreateUpstreamProvider(_ context.Context, input CreateUpstreamProviderInput) (*UpstreamProviderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := input.Name
	for _, provider := range s.providers {
		if provider.Name == name {
			return nil, fmt.Errorf("%w: %s", ErrProviderNameTaken, name)
		}
	}
	record := UpstreamProviderRecord{
		ID:               uuid.NewString(),
		Name:             name,
		BaseURL:          input.BaseURL,
		Protocol:         input.Protocol,
		APIKeyCiphertext: input.APIKeyCiphertext,
		APIKeyHint:       input.APIKeyHint,
		AnthropicVersion: derefString(input.AnthropicVersion),
		TimeoutMs:        derefInt64(input.TimeoutMs),
		Enabled:          derefBool(input.Enabled, true),
		CreatedAt:        time.Now(),
	}
	s.providers[record.ID] = record
	return cloneProvider(&record), nil
}

func (s *memoryStore) UpdateUpstreamProvider(_ context.Context, id string, patch UpdateUpstreamProviderPatch) (*UpstreamProviderRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider, ok := s.providers[id]
	if !ok {
		return nil, nil
	}
	if patch.Name != nil {
		provider.Name = *patch.Name
	}
	if patch.BaseURL != nil {
		provider.BaseURL = *patch.BaseURL
	}
	if patch.Protocol != nil {
		provider.Protocol = *patch.Protocol
	}
	if patch.ProvideAPIKey {
		provider.APIKeyCiphertext = patch.APIKeyCiphertext
		provider.APIKeyHint = patch.APIKeyHint
	}
	if patch.ProvideAnthropicVersion {
		if patch.AnthropicVersion != nil {
			provider.AnthropicVersion = *patch.AnthropicVersion
		} else {
			provider.AnthropicVersion = ""
		}
	}
	if patch.ProvideTimeoutMs {
		if patch.TimeoutMs != nil {
			provider.TimeoutMs = *patch.TimeoutMs
		} else {
			provider.TimeoutMs = 0
		}
	}
	// enabled 翻转只改这一列(Postgres 口径);自动停用现场的清理由
	// reportUpstreamSuccess/熔断复位承担。TS 内存版曾在此清除现场,属双实现漂移。
	if patch.Enabled != nil {
		provider.Enabled = *patch.Enabled
	}
	s.providers[id] = provider
	return cloneProvider(&provider), nil
}

func (s *memoryStore) DeleteUpstreamProvider(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.providers[id]; !ok {
		return false, nil
	}
	delete(s.providers, id)
	return true, nil
}

func (s *memoryStore) RecordProviderTestResult(_ context.Context, id string, result ProviderTestOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider, ok := s.providers[id]
	if !ok {
		return nil
	}
	now := time.Now()
	provider.LastTestAt = &now
	provider.LastTestOk = &result.OK
	provider.LastTestKind = result.Kind
	provider.LastTestStatus = result.Status
	provider.LastTestLatencyMs = &result.LatencyMs
	provider.LastTestModel = result.Model
	provider.LastTestDetail = result.Detail
	s.providers[id] = provider
	return nil
}

func (s *memoryStore) ReportUpstreamFailure(_ context.Context, id, reason string, threshold int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider, ok := s.providers[id]
	if !ok || !provider.Enabled {
		return false, nil
	}
	provider.ConsecutiveFailures++
	// threshold=0 语义为「关闭熔断」(文档口径);旧 Postgres SQL 在 0 时首败即熔断,
	// 这里按文档语义修正,两实现一致。
	if threshold > 0 && provider.ConsecutiveFailures >= threshold {
		provider.Enabled = false
		provider.DisabledMode = "auto"
		now := time.Now()
		provider.AutoDisabledAt = &now
		provider.AutoDisabledReason = reason
		s.providers[id] = provider
		return true, nil
	}
	s.providers[id] = provider
	return false, nil
}

func (s *memoryStore) ReportUpstreamSuccess(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider, ok := s.providers[id]
	if ok {
		provider.ConsecutiveFailures = 0
		s.providers[id] = provider
	}
	return nil
}

func cloneDeviceToken(t *DeviceTokenRecord) *DeviceTokenRecord { c := *t; return &c }
func cloneLoginFlow(f *LoginFlowRecord) *LoginFlowRecord       { c := *f; return &c }
func cloneRelease(r *ReleaseRecord) *ReleaseRecord             { c := *r; return &c }
func cloneModel(m *PlatformModelRecord) *PlatformModelRecord   { c := *m; return &c }
func cloneProvider(p *UpstreamProviderRecord) *UpstreamProviderRecord {
	c := *p
	return &c
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func derefBool(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}
