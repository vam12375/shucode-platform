package store

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/credits"
)

// 内存实现：运维域与 Postgres 同语义（返回深拷贝，临界区在 s.mu 内完成）。

// ---- upstream routes ----

func (s *memoryStore) routeView(route UpstreamRouteRecord) UpstreamRouteRecord {
	if provider, ok := s.providers[route.ProviderID]; ok {
		route.ProviderName = provider.Name
		route.ProviderEnabled = provider.Enabled
	}
	return route
}

func (s *memoryStore) ListUpstreamRoutes(_ context.Context, modelID string, includeDisabled bool) ([]UpstreamRouteRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []UpstreamRouteRecord{}
	for _, route := range s.upstreamRoutes {
		if modelID != "" && route.ModelID != modelID {
			continue
		}
		if !includeDisabled {
			if !route.Enabled {
				continue
			}
			if provider, ok := s.providers[route.ProviderID]; !ok || !provider.Enabled {
				continue
			}
		}
		out = append(out, s.routeView(route))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *memoryStore) CreateUpstreamRoute(_ context.Context, input CreateUpstreamRouteInput) (*UpstreamRouteRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.providers[input.ProviderID]; !ok {
		return nil, nil
	}
	for _, route := range s.upstreamRoutes {
		if route.ModelID == input.ModelID && route.ProviderID == input.ProviderID {
			return nil, errDuplicateRoute
		}
	}
	now := time.Now()
	record := UpstreamRouteRecord{
		ID: uuid.NewString(), ModelID: input.ModelID, ProviderID: input.ProviderID,
		UpstreamModel: input.UpstreamModel, Priority: input.Priority,
		Weight: input.Weight, Enabled: input.Enabled, CreatedAt: now, UpdatedAt: now,
	}
	if record.Weight < 1 {
		record.Weight = 1
	}
	s.upstreamRoutes[record.ID] = record
	view := s.routeView(record)
	return &view, nil
}

func (s *memoryStore) UpdateUpstreamRoute(_ context.Context, id string, patch UpdateUpstreamRoutePatch) (*UpstreamRouteRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	route, ok := s.upstreamRoutes[id]
	if !ok {
		return nil, nil
	}
	if patch.UpstreamModel != nil {
		route.UpstreamModel = *patch.UpstreamModel
	}
	if patch.Priority != nil {
		route.Priority = *patch.Priority
	}
	if patch.Weight != nil {
		if *patch.Weight < 1 {
			route.Weight = 1
		} else {
			route.Weight = *patch.Weight
		}
	}
	if patch.Enabled != nil {
		route.Enabled = *patch.Enabled
	}
	route.UpdatedAt = time.Now()
	s.upstreamRoutes[id] = route
	view := s.routeView(route)
	return &view, nil
}

func (s *memoryStore) DeleteUpstreamRoute(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.upstreamRoutes[id]; !ok {
		return false, nil
	}
	delete(s.upstreamRoutes, id)
	return true, nil
}

func (s *memoryStore) RecoverUpstreamProvider(_ context.Context, id string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider, ok := s.providers[id]
	if !ok || provider.Enabled || provider.DisabledMode != "auto" {
		return false, nil
	}
	provider.Enabled = true
	provider.DisabledMode = ""
	provider.AutoDisabledAt = nil
	provider.AutoDisabledReason = ""
	provider.ConsecutiveFailures = 0
	s.providers[id] = provider
	return true, nil
}

// ---- quota reservations ----

func (s *memoryStore) AdmitUsage(ctx context.Context, input ReserveQuotaInput) (ReserveResult, error) {
	input.Postpaid = true
	input.ReserveAllAvailable = false
	input.RetainUntilSettled = true
	return s.reserveQuota(ctx, input)
}

func (s *memoryStore) ReserveQuota(ctx context.Context, input ReserveQuotaInput) (ReserveResult, error) {
	input.Postpaid = false
	return s.reserveQuota(ctx, input)
}

func (s *memoryStore) reserveQuota(_ context.Context, input ReserveQuotaInput) (ReserveResult, error) {
	if input.Micro < 0 || input.Micro > maxCreditFieldMicro {
		return ReserveDenied, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.reservations[input.ID]; input.ID != "" && exists {
		return ReserveDuplicate, nil
	}
	for _, reservation := range s.reservations {
		if reservation.TeamID == input.TeamID && reservation.RequestID == input.RequestID {
			return ReserveDuplicate, nil
		}
	}
	team, ok := s.teams[input.TeamID]
	if !ok || team.DisabledAt != nil {
		return ReserveDeniedInactive, nil
	}
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	if input.ReserveAllAvailable && !team.Unlimited {
		quota, e1 := credits.ParseCreditsToMicro(team.QuotaCredits)
		used, e2 := credits.ParseCreditsToMicro(team.UsedCredits)
		reserved, e3 := credits.ParseCreditsToMicro(team.ReservedCredits)
		if e1 != nil || e2 != nil || e3 != nil {
			return ReserveDenied, ErrInvalidCreditCharge
		}
		input.Micro = maxInt64(0, quota-used-reserved)
	}
	decision, err := s.reservationBudgetAllowedLocked(input, createdAt)
	if err != nil {
		return "", err
	}
	if decision != ReserveReserved {
		return decision, nil
	}
	var withinLimit bool
	if input.Postpaid {
		withinLimit, err = teamCreditAdmissionAllowed(team.UsedCredits, team.QuotaCredits, team.Unlimited, input.Micro)
	} else {
		withinLimit, err = teamCreditWithinLimit(team.UsedCredits, team.ReservedCredits, team.QuotaCredits, team.Unlimited, input.Micro)
	}
	if err != nil {
		return "", err
	}
	if !withinLimit {
		return ReserveDenied, nil
	}
	ttl := input.TTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	id := input.ID
	if id == "" {
		id = uuid.NewString()
	}
	reservedMicro := input.Micro
	if input.Postpaid {
		reservedMicro = 0
	}
	reservation := QuotaReservationRecord{
		ID: id, TeamID: input.TeamID, UserID: input.UserID, RequestID: input.RequestID,
		Postpaid: input.Postpaid, ReservedMicro: reservedMicro, Status: ReservationReserved, HeldByTask: input.HeldByTask, RetainUntilSettled: input.RetainUntilSettled,
		CreatedAt: createdAt, ExpiresAt: createdAt.Add(ttl),
	}
	s.reservations[id] = reservation
	if !input.Postpaid {
		reserved, _ := credits.ParseCreditsToMicro(team.ReservedCredits)
		team.ReservedCredits = credits.FormatMicroCredits(reserved + reservedMicro)
		s.teams[team.ID] = team
	}
	return ReserveReserved, nil
}

func (s *memoryStore) GetQuotaReservation(_ context.Context, id string) (*QuotaReservationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reservation, ok := s.reservations[id]
	if !ok {
		return nil, nil
	}
	if reservation.SettledAt != nil {
		at := *reservation.SettledAt
		reservation.SettledAt = &at
	}
	if reservation.ReleasedAt != nil {
		at := *reservation.ReleasedAt
		reservation.ReleasedAt = &at
	}
	return &reservation, nil
}

func (s *memoryStore) SettleReservation(_ context.Context, reservationID string, record UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.CreditsMicro < 0 || record.CreditsMicro > maxCreditFieldMicro {
		return ErrInvalidCreditCharge
	}
	reservation, ok := s.reservations[reservationID]
	if !ok {
		return ErrReservationNotFound
	}
	if record.TeamID != reservation.TeamID || (reservation.UserID != "" && record.UserID != reservation.UserID) || (reservation.Postpaid && record.RequestID != reservation.RequestID) {
		return ErrReservationMismatch
	}
	if reservation.Status == ReservationSettled || reservation.Status == ReservationReleased {
		return nil
	}
	team, ok := s.teams[reservation.TeamID]
	capped := false
	if ok {
		used, err := credits.ParseCreditsToMicro(team.UsedCredits)
		if err != nil {
			return err
		}
		reservedBefore := team.ReservedCredits
		releasedMicro := int64(0)
		if !reservation.Postpaid && reservation.Status == ReservationReserved {
			reserved, err := credits.ParseCreditsToMicro(reservedBefore)
			if err != nil {
				return err
			}
			team.ReservedCredits = credits.FormatMicroCredits(maxInt64(0, reserved-reservation.ReservedMicro))
			releasedMicro = reservation.ReservedMicro
		}
		var charge int64
		var wasCapped bool
		if reservation.Postpaid {
			charge, err = fullTeamCreditCharge(team.UsedCredits, record.CreditsMicro)
		} else {
			charge, wasCapped, err = capTeamCreditCharge(team.UsedCredits, reservedBefore, team.QuotaCredits, team.Unlimited, releasedMicro, record.CreditsMicro)
		}
		if err != nil {
			return err
		}
		capped = wasCapped
		record.CreditsMicro = charge
		if wasCapped {
			markCreditChargeCapped(&record)
		}
		team.UsedCredits = credits.FormatMicroCredits(used + charge)
		s.teams[team.ID] = team
	}
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	reservation.Status = ReservationSettled
	settledAt := record.CreatedAt
	if settledAt.IsZero() {
		settledAt = time.Now()
	}
	reservation.SettledAt = &settledAt
	reservation.UsageID = record.ID
	s.reservations[reservation.ID] = reservation
	s.usage = append(s.usage, cloneUsage(record))
	if capped {
		return ErrCreditChargeCapped
	}
	return nil
}

func (s *memoryStore) ReleaseReservationByID(_ context.Context, reservationID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reservation, ok := s.reservations[reservationID]
	if !ok || reservation.Status != ReservationReserved {
		return nil
	}
	if team, ok := s.teams[reservation.TeamID]; ok && !reservation.Postpaid {
		reserved, err := credits.ParseCreditsToMicro(team.ReservedCredits)
		if err != nil {
			return err
		}
		team.ReservedCredits = credits.FormatMicroCredits(maxInt64(0, reserved-reservation.ReservedMicro))
		s.teams[team.ID] = team
	}
	reservation.Status = ReservationReleased
	releasedAt := at
	reservation.ReleasedAt = &releasedAt
	s.reservations[reservation.ID] = reservation
	return nil
}

func (s *memoryStore) ExpireStaleReservations(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	perTeam := map[string]int64{}
	var count int64
	for id, reservation := range s.reservations {
		if reservation.Postpaid || reservation.RetainUntilSettled || reservation.HeldByTask || reservation.Status != ReservationReserved || !reservation.ExpiresAt.Before(before) {
			continue
		}
		perTeam[reservation.TeamID] += reservation.ReservedMicro
		reservation.Status = ReservationExpired
		releasedAt := before
		reservation.ReleasedAt = &releasedAt
		s.reservations[id] = reservation
		count++
	}
	for teamID, micro := range perTeam {
		if team, ok := s.teams[teamID]; ok {
			reserved, err := credits.ParseCreditsToMicro(team.ReservedCredits)
			if err != nil {
				return 0, err
			}
			team.ReservedCredits = credits.FormatMicroCredits(maxInt64(0, reserved-micro))
			s.teams[teamID] = team
		}
	}
	return count, nil
}

func (s *memoryStore) ListOpenReservations(_ context.Context, limit int64) ([]QuotaReservationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	out := []QuotaReservationRecord{}
	for _, reservation := range s.reservations {
		if reservation.Status == ReservationReserved {
			out = append(out, reservation)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if int64(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memoryStore) CreateBackgroundGeneration(_ context.Context, job BackgroundGenerationJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.ReservationID == "" || job.ResponseID == "" || (job.ProviderID == "" && job.BaseURL == "") {
		return ErrInvalidInput
	}
	reservation, ok := s.reservations[job.ReservationID]
	if !ok || reservation.Status != ReservationReserved || reservation.TeamID != job.TeamID || reservation.UserID != job.UserID {
		return ErrReservationMismatch
	}
	if _, exists := s.backgroundJobs[job.ReservationID]; exists {
		return nil
	}
	for id, existing := range s.backgroundJobs {
		if id != job.ReservationID && existing.TeamID == job.TeamID && existing.ResponseID == job.ResponseID {
			return ErrDuplicate
		}
	}
	s.backgroundJobs[job.ReservationID] = cloneBackgroundGeneration(job)
	return nil
}

func (s *memoryStore) UpdateBackgroundGenerationUsage(_ context.Context, reservationID string, record UsageRecord) (*BackgroundGenerationJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.backgroundJobs[reservationID]
	if !ok {
		return nil, ErrReservationNotFound
	}
	if err := updateBackgroundUsage(&job, record); err != nil {
		return nil, err
	}
	s.backgroundJobs[reservationID] = cloneBackgroundGeneration(job)
	copy := cloneBackgroundGeneration(job)
	return &copy, nil
}

func (s *memoryStore) FindBackgroundGeneration(_ context.Context, teamID, responseID string) (*BackgroundGenerationJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, job := range s.backgroundJobs {
		if job.TeamID == teamID && job.ResponseID == responseID {
			copy := cloneBackgroundGeneration(job)
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *memoryStore) ClaimBackgroundGenerations(_ context.Context, now, leaseUntil time.Time, limit int64) ([]BackgroundGenerationJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	out := make([]BackgroundGenerationJob, 0, limit)
	for _, job := range s.backgroundJobs {
		if job.CompletedAt == nil && !job.NextPollAt.After(now) {
			out = append(out, job)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextPollAt.Before(out[j].NextPollAt) })
	if int64(len(out)) > limit {
		out = out[:limit]
	}
	for i := range out {
		job := out[i]
		job.Attempts++
		job.NextPollAt = leaseUntil
		s.backgroundJobs[job.ReservationID] = cloneBackgroundGeneration(job)
		out[i] = cloneBackgroundGeneration(job)
	}
	return out, nil
}

func (s *memoryStore) RetryBackgroundGeneration(_ context.Context, reservationID string, nextPollAt time.Time, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.backgroundJobs[reservationID]
	if ok {
		job.NextPollAt = nextPollAt
		job.LastError = lastError
		s.backgroundJobs[reservationID] = job
	}
	return nil
}

func (s *memoryStore) DeleteBackgroundGeneration(_ context.Context, reservationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.backgroundJobs, reservationID)
	return nil
}

func (s *memoryStore) FinishBackgroundGeneration(_ context.Context, reservationID string, response []byte, status int, contentType string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.backgroundJobs[reservationID]
	if !ok {
		return nil
	}
	job.FinalResponse = append([]byte(nil), response...)
	job.FinalStatus = status
	job.FinalType = contentType
	completedAt := at
	job.CompletedAt = &completedAt
	job.LastError = ""
	s.backgroundJobs[reservationID] = job
	return nil
}

// ---- 结算失败补偿 ----

func (s *memoryStore) EnqueuePendingSettlement(_ context.Context, reservationID string, record UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.pendingSettlements[reservationID]; exists {
		return nil
	}
	now := time.Now()
	s.pendingSettlements[reservationID] = PendingSettlementRecord{
		ReservationID: reservationID,
		Record:        cloneUsage(record),
		Status:        PendingSettlementPending,
		NextAttemptAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	return nil
}

func (s *memoryStore) ListPendingSettlements(_ context.Context, filter PendingSettlementFilter) ([]PendingSettlementRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	out := []PendingSettlementRecord{}
	for _, item := range s.pendingSettlements {
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		if filter.DueBefore != nil && item.NextAttemptAt.After(*filter.DueBefore) {
			continue
		}
		item.Record = cloneUsage(item.Record)
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].NextAttemptAt.Equal(out[j].NextAttemptAt) {
			return out[i].NextAttemptAt.Before(out[j].NextAttemptAt)
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	if int64(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memoryStore) MarkPendingSettlementApplied(_ context.Context, reservationID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.pendingSettlements[reservationID]
	if !ok {
		return nil
	}
	item.Status = PendingSettlementApplied
	item.LastError = ""
	item.UpdatedAt = at
	s.pendingSettlements[reservationID] = item
	return nil
}

func (s *memoryStore) MarkPendingSettlementRetry(_ context.Context, reservationID string, attempts int64, lastError string, nextAttemptAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.pendingSettlements[reservationID]
	if !ok {
		return nil
	}
	item.Status = PendingSettlementPending
	item.Attempts = attempts
	item.LastError = lastError
	item.NextAttemptAt = nextAttemptAt
	item.UpdatedAt = time.Now()
	s.pendingSettlements[reservationID] = item
	return nil
}

func (s *memoryStore) MarkPendingSettlementFailed(_ context.Context, reservationID string, attempts int64, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.pendingSettlements[reservationID]
	if !ok {
		return nil
	}
	item.Status = PendingSettlementFailed
	item.Attempts = attempts
	item.LastError = lastError
	item.UpdatedAt = time.Now()
	s.pendingSettlements[reservationID] = item
	return nil
}

func (s *memoryStore) InsertGatewayAttempt(_ context.Context, record GatewayAttemptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.StatusCode != nil {
		status := *record.StatusCode
		record.StatusCode = &status
	}
	s.gatewayAttempts = append(s.gatewayAttempts, record)
	return nil
}

func (s *memoryStore) CountPendingSettlements(_ context.Context, status PendingSettlementStatus) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, item := range s.pendingSettlements {
		if status == "" || item.Status == status {
			count++
		}
	}
	return count, nil
}

func (s *memoryStore) ListUsageForExport(_ context.Context, filter UsageFilter, max int64) ([]UsageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if max <= 0 {
		max = 10_000
	}
	if max > 100_000 {
		max = 100_000
	}
	out := []UsageRecord{}
	for _, record := range s.usage {
		if !usageMatches(record, filter) {
			continue
		}
		out = append(out, cloneUsage(record))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return cursorOlder(out[j].CreatedAt, out[j].ID, out[i].CreatedAt, out[i].ID)
	})
	if int64(len(out)) > max {
		out = out[:max]
	}
	return out, nil
}

// ---- ops ----

func percentileCont(sorted []float64, p float64) *float64 {
	if len(sorted) == 0 {
		return nil
	}
	rank := p * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower < 0 {
		lower = 0
	}
	if upper >= len(sorted) {
		upper = len(sorted) - 1
	}
	value := sorted[lower]
	if upper != lower {
		value += (sorted[upper] - sorted[lower]) * (rank - float64(lower))
	}
	return &value
}

func (s *memoryStore) OpsOverview(_ context.Context, since time.Time) (*OpsOverview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &OpsOverview{ProviderStats: []OpsProviderStat{}}
	latencies := []float64{}
	devices := map[string]struct{}{}
	providerStats := map[string]*OpsProviderStat{}
	for _, record := range s.usage {
		if record.CreatedAt.Before(since) {
			continue
		}
		out.Requests++
		switch record.Status {
		case UsageStatusOk:
			out.SuccessRequests++
		case UsageStatusError:
			out.ErrorRequests++
		case UsageStatusAborted:
			out.AbortedRequests++
		}
		out.InputTokens += record.InputTokens
		out.OutputTokens += record.OutputTokens
		out.CacheReadTokens += record.CacheReadTokens
		out.CacheCreationTokens += record.CacheCreationTokens
		out.CreditsMicro += record.CreditsMicro
		if record.LatencyMs != nil {
			latencies = append(latencies, float64(*record.LatencyMs))
		}
		if record.DeviceTokenID != "" {
			devices[record.DeviceTokenID] = struct{}{}
		}
		key := record.ProviderID
		stat, ok := providerStats[key]
		if !ok {
			name := "网关默认上游"
			if provider, exists := s.providers[key]; exists {
				name = provider.Name
			}
			stat = &OpsProviderStat{ProviderID: key, ProviderName: name}
			providerStats[key] = stat
		}
		stat.Requests++
		if record.Status == UsageStatusError {
			stat.Errors++
		}
	}
	sort.Float64s(latencies)
	out.P50LatencyMs = percentileCont(latencies, 0.5)
	out.P95LatencyMs = percentileCont(latencies, 0.95)
	out.P99LatencyMs = percentileCont(latencies, 0.99)
	out.ActiveDevices = int64(len(devices))
	for _, reservation := range s.reservations {
		if reservation.Status == ReservationReserved {
			out.OpenReservations++
		}
	}
	for _, pending := range s.pendingSettlements {
		if pending.Status == PendingSettlementPending || pending.Status == PendingSettlementFailed {
			out.PendingSettlements++
		}
	}
	// 供应商 P95 需要单独收集，按 provider 分组重扫。
	providerLatencies := map[string][]float64{}
	for _, record := range s.usage {
		if record.CreatedAt.Before(since) || record.LatencyMs == nil {
			continue
		}
		providerLatencies[record.ProviderID] = append(providerLatencies[record.ProviderID], float64(*record.LatencyMs))
	}
	for key, stat := range providerStats {
		values := providerLatencies[key]
		sort.Float64s(values)
		stat.P95LatencyMs = percentileCont(values, 0.95)
		if stat.Requests > 0 {
			stat.ErrorRate = float64(stat.Errors) / float64(stat.Requests)
		}
		out.ProviderStats = append(out.ProviderStats, *stat)
	}
	sort.SliceStable(out.ProviderStats, func(i, j int) bool {
		return out.ProviderStats[i].Requests > out.ProviderStats[j].Requests
	})
	return out, nil
}

func (s *memoryStore) OpsSeries(_ context.Context, since time.Time, bucketMinutes int64) ([]OpsSeriesPoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bucketMinutes < 1 {
		bucketMinutes = 5
	}
	if bucketMinutes > 1440 {
		bucketMinutes = 1440
	}
	bucket := time.Duration(bucketMinutes) * time.Minute
	grouped := map[string]*OpsSeriesPoint{}
	latencies := map[string][]float64{}
	for _, record := range s.usage {
		if record.CreatedAt.Before(since) {
			continue
		}
		start := record.CreatedAt.UTC().Truncate(bucket)
		key := start.Add(shanghaiOffset).Format("2006-01-02T15:04:05")
		row, ok := grouped[key]
		if !ok {
			row = &OpsSeriesPoint{BucketStart: key}
			grouped[key] = row
		}
		row.Requests++
		if record.Status == UsageStatusError {
			row.Errors++
		}
		row.CreditsMicro += record.CreditsMicro
		if record.LatencyMs != nil {
			latencies[key] = append(latencies[key], float64(*record.LatencyMs))
		}
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]OpsSeriesPoint, 0, len(keys))
	for _, key := range keys {
		values := latencies[key]
		sort.Float64s(values)
		row := grouped[key]
		row.P95LatencyMs = percentileCont(values, 0.95)
		out = append(out, *row)
	}
	return out, nil
}

// ---- alerts ----

func (s *memoryStore) ListAlertRules(_ context.Context) ([]AlertRuleRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []AlertRuleRecord{}
	for _, rule := range s.alertRules {
		out = append(out, rule)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *memoryStore) UpsertAlertRule(_ context.Context, input UpsertAlertRuleInput) (*AlertRuleRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	window := input.WindowMinutes
	if window <= 0 {
		window = 15
	}
	if input.ID == "" {
		record := AlertRuleRecord{
			ID: uuid.NewString(), Name: input.Name, Metric: input.Metric,
			Threshold: input.Threshold, WindowMinutes: window, Enabled: input.Enabled,
			CreatedAt: now, UpdatedAt: now,
		}
		s.alertRules[record.ID] = record
		return &record, nil
	}
	record, ok := s.alertRules[input.ID]
	if !ok {
		return nil, nil
	}
	record.Name, record.Metric, record.Threshold = input.Name, input.Metric, input.Threshold
	record.WindowMinutes, record.Enabled, record.UpdatedAt = window, input.Enabled, now
	s.alertRules[record.ID] = record
	return &record, nil
}

func (s *memoryStore) DeleteAlertRule(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.alertRules[id]; !ok {
		return false, nil
	}
	delete(s.alertRules, id)
	return true, nil
}

func (s *memoryStore) OpenAlertEventForRule(_ context.Context, ruleID string) (*AlertEventRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.alertEvents) - 1; i >= 0; i-- {
		event := s.alertEvents[i]
		if event.RuleID == ruleID && event.Status == "open" {
			copy := event
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *memoryStore) InsertAlertEvent(_ context.Context, rule AlertRuleRecord, value, message string, at time.Time) (*AlertEventRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event := AlertEventRecord{
		ID: uuid.NewString(), RuleID: rule.ID, RuleName: rule.Name, Metric: rule.Metric,
		Value: value, Threshold: rule.Threshold, Message: message, Status: "open", CreatedAt: at,
	}
	s.alertEvents = append(s.alertEvents, event)
	copy := event
	return &copy, nil
}

func (s *memoryStore) ResolveAlertEvent(_ context.Context, id string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.alertEvents {
		if s.alertEvents[i].ID == id && s.alertEvents[i].Status == "open" {
			s.alertEvents[i].Status = "resolved"
			s.alertEvents[i].ResolvedAt = &at
			return true, nil
		}
	}
	return false, nil
}

func (s *memoryStore) ResolveOpenAlertEventsForRule(_ context.Context, ruleID string, at time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for i := range s.alertEvents {
		if s.alertEvents[i].RuleID == ruleID && s.alertEvents[i].Status == "open" {
			s.alertEvents[i].Status = "resolved"
			s.alertEvents[i].ResolvedAt = &at
			count++
		}
	}
	return count, nil
}

func (s *memoryStore) ListAlertEvents(_ context.Context, status string, limit int64) ([]AlertEventRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	out := []AlertEventRecord{}
	for i := len(s.alertEvents) - 1; i >= 0; i-- {
		event := s.alertEvents[i]
		if status != "" && event.Status != status {
			continue
		}
		out = append(out, event)
		if int64(len(out)) >= limit {
			break
		}
	}
	return out, nil
}

var errDuplicateRoute = &routeError{message: "同一模型不能重复绑定同一供应商"}

type routeError struct{ message string }

func (e *routeError) Error() string { return e.message }

func normalizeRouteUpstreamModel(model, upstream string) string {
	if strings.TrimSpace(upstream) != "" {
		return strings.TrimSpace(upstream)
	}
	return model
}
