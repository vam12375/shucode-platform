package store

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
)

func (s *memoryStore) CreatePaymentOrder(_ context.Context, record PaymentOrderRecord) (*PaymentOrderRecord, error) {
	record, err := normalizePaymentOrder(record)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if saved, exists := s.paymentOrders[record.OutTradeNo]; exists {
		if !samePaymentOrder(saved, record) {
			return nil, ErrPaymentConflict
		}
		return clonePaymentOrder(saved), nil
	}
	if _, exists := s.teams[record.TeamID]; !exists {
		return nil, ErrInvalidInput
	}
	if _, exists := s.users[record.UserID]; !exists {
		return nil, ErrInvalidInput
	}
	if record.TargetType == PaymentTargetPersonal && s.personalAccounts[record.TargetID] != record.TeamID || record.TargetType == PaymentTargetTeam && s.personalOwnerLocked(record.TeamID) != "" {
		return nil, ErrPaymentConflict
	}
	for _, saved := range s.paymentOrders {
		if saved.ID == record.ID {
			return nil, ErrPaymentConflict
		}
	}
	s.paymentOrders[record.OutTradeNo] = *clonePaymentOrder(record)
	return clonePaymentOrder(record), nil
}

func (s *memoryStore) GetPaymentOrder(_ context.Context, outTradeNo string) (*PaymentOrderRecord, error) {
	if !paymentReferencePattern.MatchString(outTradeNo) {
		return nil, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, exists := s.paymentOrders[outTradeNo]; exists {
		return clonePaymentOrder(record), nil
	}
	return nil, nil
}

func (s *memoryStore) ListPaymentOrders(_ context.Context, filter PaymentOrderFilter) ([]PaymentOrderRecord, error) {
	filter, err := normalizePaymentFilter(filter)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []PaymentOrderRecord{}
	for _, record := range s.paymentOrders {
		if filter.TargetType != "" && record.TargetType != filter.TargetType || filter.TargetID != "" && record.TargetID != filter.TargetID || filter.TeamID != "" && record.TeamID != filter.TeamID || filter.UserID != "" && record.UserID != filter.UserID || filter.Status != "" && record.Status != filter.Status {
			continue
		}
		if filter.BeforeCreatedAt != nil && (record.CreatedAt.After(*filter.BeforeCreatedAt) || record.CreatedAt.Equal(*filter.BeforeCreatedAt) && record.ID >= filter.BeforeID) {
			continue
		}
		out = append(out, *clonePaymentOrder(record))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > int(filter.Limit) {
		out = out[:int(filter.Limit)]
	}
	return out, nil
}

func (s *memoryStore) ClaimPaymentSuccess(_ context.Context, input PaymentSuccessInput) (*PaymentOrderRecord, bool, error) {
	if err := validatePaymentSuccess(input); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.paymentOrders[input.OutTradeNo]
	if !exists {
		return nil, false, ErrPaymentNotFound
	}
	if err := matchPaymentSuccess(record, input); err != nil {
		return nil, false, err
	}
	if record.Status == PaymentPaid {
		return clonePaymentOrder(record), true, nil
	}
	for _, saved := range s.paymentOrders {
		if saved.Provider == record.Provider && saved.TradeNo == input.TradeNo && saved.OutTradeNo != record.OutTradeNo {
			return nil, false, ErrPaymentConflict
		}
	}
	team, exists := s.teams[record.TeamID]
	if !exists {
		return nil, false, ErrInvalidInput
	}
	audit := paymentAudit(record, input)
	if _, exists := s.auditEventIDs[audit.EventID]; exists {
		return nil, false, ErrPaymentConflict
	}
	patch, err := prepareTeamCreditIncrease(team.QuotaCredits, UpdateTeamPatch{QuotaIncreaseMicro: &record.CreditMicro}, &audit)
	if err != nil {
		return nil, false, err
	}
	oldQuota := team.QuotaCredits
	applyTeamPatch(&team, patch)
	at := input.PaidAt.UTC()
	// 订单状态与余额、流水在同一个锁内提交，通知重试不能把充值拆成两次写入。
	record.Status, record.TradeNo, record.PaidAt = PaymentPaid, input.TradeNo, &at
	record.ClosedAt = nil
	record.NotifyPayload = append([]byte(nil), input.NotifyPayload...)
	s.teams[team.ID] = team
	s.quotaAdjusts = append(s.quotaAdjusts, quotaAdjustmentRow{TeamQuotaAdjustmentView: TeamQuotaAdjustmentView{ID: uuid.NewString(), Action: "alipay_recharge", OldQuotaCredits: oldQuota, NewQuotaCredits: team.QuotaCredits, OldUnlimited: team.Unlimited, NewUnlimited: team.Unlimited, CreatedAt: at}, TeamID: team.ID, ActorUserID: record.UserID})
	s.auditEventIDs[audit.EventID] = struct{}{}
	s.auditLogs = append(s.auditLogs, AdminAuditLogView{ID: uuid.NewString(), ActorUserID: record.UserID, TeamID: team.ID, Action: audit.Action, TargetType: audit.TargetType, TargetID: audit.TargetID, Detail: append([]byte(nil), audit.Detail...), CreatedAt: at})
	s.paymentOrders[record.OutTradeNo] = record
	return clonePaymentOrder(record), false, nil
}

func (s *memoryStore) ClosePaymentOrder(_ context.Context, outTradeNo string, at time.Time) (*PaymentOrderRecord, error) {
	if !paymentReferencePattern.MatchString(outTradeNo) || at.IsZero() {
		return nil, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.paymentOrders[outTradeNo]
	if !exists {
		return nil, ErrPaymentNotFound
	}
	if record.Status == PaymentPending {
		closed := at.UTC()
		record.Status, record.ClosedAt = PaymentClosed, &closed
		s.paymentOrders[outTradeNo] = record
	}
	return clonePaymentOrder(record), nil
}
