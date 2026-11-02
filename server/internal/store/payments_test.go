package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testPaymentOrders(t *testing.T, ctx context.Context, s PlatformStore) {
	user := mustUser(t, ctx, s, "payments@store.test")
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "payment-target", QuotaCredits: "10", Unlimited: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateTeam(ctx, CreateTeamInput{Name: "payment-other", QuotaCredits: "2"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	newOrder := func(fen int64) PaymentOrderRecord {
		return PaymentOrderRecord{ID: uuid.NewString(), OutTradeNo: "pay_" + uuid.NewString(), Provider: PaymentProviderAlipay, ConfigurationID: uuid.NewString(), TargetType: PaymentTargetTeam, TargetID: team.ID, TeamID: team.ID, UserID: user.ID, AppID: "2026000000000001", SellerID: "2088000000000001", AmountFen: fen, CreditMicro: fen * MicroCreditsPerFen, Subject: "余额充值", CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute)}
	}
	success := func(r PaymentOrderRecord) PaymentSuccessInput {
		return PaymentSuccessInput{OutTradeNo: r.OutTradeNo, TargetType: r.TargetType, TargetID: r.TargetID, TeamID: r.TeamID, AppID: r.AppID, SellerID: r.SellerID, AmountFen: r.AmountFen, TradeNo: "trade_" + r.ID, PaidAt: now.Add(time.Minute), NotifyPayload: []byte(`{"status":"TRADE_SUCCESS"}`)}
	}
	record := newOrder(1)
	record.TestPayment = true
	saved, err := s.CreatePaymentOrder(ctx, record)
	if err != nil || saved == nil || saved.Status != PaymentPending {
		t.Fatalf("create: %+v %v", saved, err)
	}
	replay := record
	replay.ID = uuid.NewString()
	replay.CreatedAt = now.Add(time.Second)
	replay.ExpiresAt = replay.CreatedAt.Add(30 * time.Minute)
	repeated, err := s.CreatePaymentOrder(ctx, replay)
	if err != nil || repeated.ID != record.ID {
		t.Fatalf("idempotent create: %+v %v", repeated, err)
	}
	conflict := record
	conflict.AmountFen = 2
	conflict.CreditMicro = 20_000
	if _, err = s.CreatePaymentOrder(ctx, conflict); !errors.Is(err, ErrPaymentConflict) {
		t.Fatalf("conflicting create: %v", err)
	}
	input := success(record)
	for label, bad := range map[string]PaymentSuccessInput{"amount": func() PaymentSuccessInput { v := input; v.AmountFen++; return v }(), "target": func() PaymentSuccessInput { v := input; v.TargetID = other.ID; return v }(), "team": func() PaymentSuccessInput { v := input; v.TeamID = other.ID; return v }(), "app": func() PaymentSuccessInput { v := input; v.AppID = "wrong"; return v }(), "seller": func() PaymentSuccessInput { v := input; v.SellerID = "wrong"; return v }()} {
		if _, _, err = s.ClaimPaymentSuccess(ctx, bad); !errors.Is(err, ErrPaymentConflict) {
			t.Errorf("%s mismatch: %v", label, err)
		}
	}
	var applied atomic.Int64
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order, duplicate, err := s.ClaimPaymentSuccess(ctx, input)
			if err != nil || order == nil || order.Status != PaymentPaid {
				t.Errorf("concurrent payment: %+v %v", order, err)
				return
			}
			if !duplicate {
				applied.Add(1)
			}
		}()
	}
	wg.Wait()
	if applied.Load() != 1 {
		t.Fatalf("first applications: %d", applied.Load())
	}
	updated, _ := s.GetTeam(ctx, team.ID)
	flows, _ := s.ListQuotaAdjustments(ctx, team.ID, 100)
	audits, _ := s.ListAuditLogs(ctx, AuditLogFilter{TeamID: team.ID})
	if updated.QuotaCredits != "10.010000" || updated.UsedCredits != "0.000000" || !updated.Unlimited || len(flows) != 1 || flows[0].Action != "alipay_recharge" || len(audits) != 1 || audits[0].Action != "payment.recharge" {
		t.Fatalf("atomic 0.01 recharge: %+v %+v %+v", updated, flows, audits)
	}
	paid, _ := s.GetPaymentOrder(ctx, record.OutTradeNo)
	paid.NotifyPayload[0] = 'x'
	*paid.PaidAt = time.Time{}
	isolated, _ := s.GetPaymentOrder(ctx, record.OutTradeNo)
	if isolated.NotifyPayload[0] != '{' || isolated.PaidAt.IsZero() {
		t.Fatal("order return values alias storage")
	}
	if closed, err := s.ClosePaymentOrder(ctx, record.OutTradeNo, now.Add(2*time.Minute)); err != nil || closed.Status != PaymentPaid {
		t.Fatalf("close cannot reverse paid: %+v %v", closed, err)
	}
	badTrade := input
	badTrade.TradeNo = "other-trade"
	if _, _, err = s.ClaimPaymentSuccess(ctx, badTrade); !errors.Is(err, ErrPaymentConflict) {
		t.Fatalf("paid order different transaction: %v", err)
	}
	otherOrder := newOrder(100)
	if _, err = s.CreatePaymentOrder(ctx, otherOrder); err != nil {
		t.Fatal(err)
	}
	reused := success(otherOrder)
	reused.TradeNo = input.TradeNo
	if _, _, err = s.ClaimPaymentSuccess(ctx, reused); !errors.Is(err, ErrPaymentConflict) {
		t.Fatalf("transaction reused: %v", err)
	}
	unmodified, _ := s.GetPaymentOrder(ctx, otherOrder.OutTradeNo)
	updated, _ = s.GetTeam(ctx, team.ID)
	if unmodified.Status != PaymentPending || updated.QuotaCredits != "10.010000" {
		t.Fatal("transaction conflict must roll back balance and order")
	}
	if _, err = s.ClosePaymentOrder(ctx, otherOrder.OutTradeNo, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if order, _, err := s.ClaimPaymentSuccess(ctx, success(otherOrder)); err != nil || order.Status != PaymentPaid {
		t.Fatalf("closed payment success should repair: %+v %v", order, err)
	}
	// 停用只阻止消费，不能丢弃已经支付的应收事实。
	disabled := now.Add(3 * time.Minute)
	if _, err = s.UpdateTeam(ctx, team.ID, UpdateTeamPatch{HasDisabled: true, DisabledAt: &disabled}); err != nil {
		t.Fatal(err)
	}
	personal := newOrder(200)
	personal.TargetType = PaymentTargetPersonal
	personal.TargetID = user.ID
	personalAccount, err := s.EnsurePersonalAccount(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	personal.TeamID = personalAccount.ID
	if _, err = s.CreatePaymentOrder(ctx, personal); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ClaimPaymentSuccess(ctx, success(personal)); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPaymentOrders(ctx, PaymentOrderFilter{TargetType: PaymentTargetPersonal, TargetID: user.ID})
	if err != nil || len(list) != 1 || list[0].ID != personal.ID {
		t.Fatalf("personal target isolation: %+v %v", list, err)
	}
	list, err = s.ListPaymentOrders(ctx, PaymentOrderFilter{TeamID: team.ID, Limit: 1})
	if err != nil || len(list) != 1 {
		t.Fatalf("page 1: %+v %v", list, err)
	}
	next, err := s.ListPaymentOrders(ctx, PaymentOrderFilter{TeamID: team.ID, Limit: 1, BeforeCreatedAt: &list[0].CreatedAt, BeforeID: list[0].ID})
	if err != nil || len(next) != 1 || next[0].ID == list[0].ID {
		t.Fatalf("same timestamp cursor: %+v %v", next, err)
	}
	overflow := newOrder(1)
	if _, err = s.CreatePaymentOrder(ctx, overflow); err != nil {
		t.Fatal(err)
	}
	ceiling := "999999999999.999999"
	if _, err = s.UpdateTeam(ctx, team.ID, UpdateTeamPatch{QuotaCredits: &ceiling}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ClaimPaymentSuccess(ctx, success(overflow)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("overflow must fail: %v", err)
	}
	pending, _ := s.GetPaymentOrder(ctx, overflow.OutTradeNo)
	flows, _ = s.ListQuotaAdjustments(ctx, team.ID, 100)
	if pending.Status != PaymentPending || len(flows) != 2 {
		t.Fatal("overflow partially committed")
	}
	for label, invalid := range map[string]PaymentOrderRecord{"zero amount": func() PaymentOrderRecord { v := newOrder(0); return v }(), "mismatched credits": func() PaymentOrderRecord { v := newOrder(1); v.CreditMicro = 1; return v }(), "wrong team target": func() PaymentOrderRecord { v := newOrder(1); v.TargetID = other.ID; return v }(), "other personal target": func() PaymentOrderRecord { v := personal; v.UserID = uuid.NewString(); return v }()} {
		if _, err = s.CreatePaymentOrder(ctx, invalid); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("invalid %s: %v", label, err)
		}
	}
	missing := fmt.Sprintf("missing_%s", uuid.NewString())
	if order, err := s.GetPaymentOrder(ctx, missing); err != nil || order != nil {
		t.Fatalf("missing lookup: %+v %v", order, err)
	}
}
