package media

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

const ReferenceImportFeeMicro int64 = 500_000
const ReferenceAnalysisFeeMicro int64 = 1_000_000

// FixedFee 随操作意图持久化；重放必须沿用同一身份和锁定金额。
type FixedFee struct {
	ID        string    `json:"id"`
	RequestID string    `json:"requestId"`
	Model     string    `json:"model"`
	Micro     int64     `json:"micro"`
	CreatedAt time.Time `json:"createdAt"`
}

func newFixedFee(team, user, requestID, model string, micro int64) FixedFee {
	return FixedFee{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("media:fixed-fee:"+team+":"+user+":"+requestID)).String(), RequestID: requestID, Model: model, Micro: micro, CreatedAt: time.Now().UTC()}
}

func (s *Service) fixedFeeIdentity(ctx context.Context, team, user string, fee FixedFee) (*store.QuotaReservationRecord, error) {
	if fee.Micro < 0 || fee.Model == "" || fee.RequestID == "" || fee.CreatedAt.IsZero() {
		return nil, ErrInvalid
	}
	identity, err := s.Repo.Store.GetQuotaReservation(ctx, fee.ID)
	if err != nil {
		return nil, err
	}
	if identity != nil && (!identity.Postpaid || identity.TeamID != team || identity.UserID != user || identity.RequestID != fee.RequestID) {
		return nil, store.ErrReservationMismatch
	}
	return identity, nil
}

func (s *Service) admitFixedFee(ctx context.Context, team, user string, fee FixedFee) error {
	if fee.Micro < 0 || fee.Model == "" || fee.RequestID == "" || fee.CreatedAt.IsZero() {
		return ErrInvalid
	}
	result, err := s.Repo.Store.AdmitUsage(ctx, store.ReserveQuotaInput{ID: fee.ID, TeamID: team, UserID: user, RequestID: fee.RequestID, Micro: fee.Micro, CreatedAt: fee.CreatedAt})
	if err != nil {
		return err
	}
	switch result {
	case store.ReserveReserved:
		return nil
	case store.ReserveDenied:
		return fmt.Errorf("insufficient_credits")
	case store.ReserveDeniedBudget:
		return fmt.Errorf("budget_exhausted")
	case store.ReserveDeniedInactive:
		return fmt.Errorf("team_inactive")
	case store.ReserveDuplicate:
		// duplicate 只说明唯一键已存在，不能证明本次操作拥有该收费身份。
		identity, err := s.fixedFeeIdentity(ctx, team, user, fee)
		if err != nil {
			return err
		}
		if identity != nil && (identity.Status == store.ReservationReserved || identity.Status == store.ReservationSettled) {
			return nil
		}
		return ErrConflict
	default:
		return fmt.Errorf("unknown media usage admission result: %s", result)
	}
}

func (s *Service) settleFixedFee(ctx context.Context, team, user, device string, fee FixedFee) error {
	identity, err := s.fixedFeeIdentity(ctx, team, user, fee)
	if err != nil {
		return err
	}
	// released 是零扣终态；不能把存储层的幂等成功误当作已经收取费用。
	if identity == nil || (identity.Status != store.ReservationReserved && identity.Status != store.ReservationSettled) {
		return ErrConflict
	}
	usageID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("media:fixed-fee:usage:"+fee.ID)).String()
	if identity.Status == store.ReservationSettled && identity.UsageID != usageID {
		return ErrConflict
	}
	if err = s.Repo.Store.SettleReservation(ctx, fee.ID, store.UsageRecord{
		ID: usageID, RequestID: fee.RequestID,
		TeamID: team, UserID: user, DeviceTokenID: device, Model: fee.Model, UpstreamModel: fee.Model,
		CreditsMicro: fee.Micro, Status: store.UsageStatusOk, CreatedAt: fee.CreatedAt,
	}); err != nil {
		return err
	}
	// 关闭身份与结算并发时存储会幂等返回；交付前必须确认费用已形成这笔用量。
	identity, err = s.fixedFeeIdentity(ctx, team, user, fee)
	if err != nil {
		return err
	}
	if identity == nil || identity.Status != store.ReservationSettled || identity.UsageID != usageID {
		return ErrConflict
	}
	return nil
}

func (s *Service) releaseFixedFee(ctx context.Context, team, user string, fee FixedFee) error {
	identity, err := s.fixedFeeIdentity(ctx, team, user, fee)
	if err != nil || identity == nil {
		return err
	}
	if identity.Status == store.ReservationSettled {
		return ErrConflict
	}
	return s.Repo.Store.ReleaseReservationByID(ctx, fee.ID, time.Now().UTC())
}
