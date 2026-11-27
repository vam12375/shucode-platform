package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// 后台任务的地址失去凭据授权时必须保存上游身份供核对，继续轮询或估算扣费都不安全。
const (
	BackgroundEndpointUnverified       = "background_endpoint_unverified"
	BackgroundSettledResultUnavailable = "background_settled_result_unavailable"
)

func (job BackgroundGenerationJob) AwaitingEndpointReview() bool {
	return job.LastError == BackgroundEndpointUnverified || job.LastError == BackgroundSettledResultUnavailable
}

func (job BackgroundGenerationJob) endpointReviewRecord() UsageRecord {
	record := job.Record
	record.BackgroundResponseID = job.ResponseID
	record.ErrorType = BackgroundEndpointUnverified
	record.Status = UsageStatusError
	record.CreditsMicro = 0
	if job.UsageObservedAt == nil {
		record.InputTokens, record.OutputTokens = 0, 0
		record.CacheReadTokens, record.CacheCreationTokens = 0, 0
	}
	return record
}

func (s *memoryStore) HoldBackgroundGenerationForReview(_ context.Context, reservationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.backgroundJobs[reservationID]
	if !ok || job.CompletedAt != nil || job.AwaitingEndpointReview() {
		return nil
	}
	// 旧版本可能已实扣却在留存结果前退出，此时只登记结果待恢复，不能产生新的账务核对。
	if s.reservations[reservationID].Status == ReservationSettled {
		job.LastError = BackgroundSettledResultUnavailable
		s.backgroundJobs[reservationID] = job
		return nil
	}
	record := job.endpointReviewRecord()
	if _, exists := s.providers[record.ProviderID]; !exists {
		record.ProviderID = ""
	}
	if err := s.enqueuePendingSettlementLocked(reservationID, record); err != nil {
		return err
	}
	// 人工核对从入队起就是 failed，滚动升级期间旧版补偿器也不会按零费用自动结算。
	item := s.pendingSettlements[reservationID]
	if item.Record.ErrorType == BackgroundEndpointUnverified && item.Status == PendingSettlementPending {
		item.Status, item.LastError = PendingSettlementFailed, BackgroundEndpointUnverified
		s.pendingSettlements[reservationID] = item
	}
	job.LastError = BackgroundEndpointUnverified
	s.backgroundJobs[reservationID] = job
	return nil
}

func (s *PostgresStore) HoldBackgroundGenerationForReview(ctx context.Context, reservationID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	job, err := scanBackgroundGeneration(tx.QueryRow(ctx, "SELECT "+backgroundGenerationCols+" FROM gateway_background_generations WHERE reservation_id=$1 FOR UPDATE", reservationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if job.CompletedAt != nil || job.AwaitingEndpointReview() {
		return nil
	}
	var settled bool
	if err := tx.QueryRow(ctx, "SELECT status='settled' FROM quota_reservations WHERE id=$1", reservationID).Scan(&settled); err != nil {
		return err
	}
	if settled {
		// 旧版本的实扣事务已成功；保留上游任务号与结果缺失状态供恢复。
		if _, err := tx.Exec(ctx, "UPDATE gateway_background_generations SET last_error=$2 WHERE reservation_id=$1", reservationID, BackgroundSettledResultUnavailable); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	record := job.endpointReviewRecord()
	if err := resolveBackgroundProviderID(ctx, tx, &record); err != nil {
		return err
	}
	if err := enqueuePendingSettlement(ctx, tx, reservationID, record); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE pending_settlements SET status='failed',last_error=$2,updated_at=now()
		WHERE reservation_id=$1 AND error_type=$2 AND status='pending'`, reservationID, BackgroundEndpointUnverified); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE gateway_background_generations SET last_error=$2 WHERE reservation_id=$1", reservationID, BackgroundEndpointUnverified); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func resolveBackgroundProviderID(ctx context.Context, tx pgx.Tx, record *UsageRecord) error {
	if record.ProviderID == "" {
		return nil
	}
	// 供应商可能在终态响应返回之前删除。保留任务和价格快照，账本及核对表外键只关联现存目录行。
	var providerID *string
	if err := tx.QueryRow(ctx, "SELECT (SELECT id::text FROM upstream_providers WHERE id=$1::uuid FOR KEY SHARE)", record.ProviderID).Scan(&providerID); err != nil {
		return err
	}
	record.ProviderID = derefString2(providerID)
	return nil
}
