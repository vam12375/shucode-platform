package apiapp

import (
	"context"
	"errors"
	"github.com/vam12375/shucode-platform/server/internal/alipay"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"time"
)

func applyPaymentTrade(ctx context.Context, deps *Deps, order *store.PaymentOrderRecord, trade alipay.Trade, payload []byte) (*store.PaymentOrderRecord, error) {
	if trade.OutTradeNo != order.OutTradeNo || trade.AppID != order.AppID || trade.SellerID != order.SellerID || trade.AmountFen != order.AmountFen {
		return nil, store.ErrPaymentConflict
	}
	switch trade.Status {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		paidAt := trade.PaidAt
		// 支付状态和金额已经验签；部分历史订单查单不带付款时间，记录确认时间而不丢弃支付事实。
		if paidAt.IsZero() {
			paidAt = deps.Now()
		}
		saved, duplicate, err := deps.Store.ClaimPaymentSuccess(ctx, store.PaymentSuccessInput{OutTradeNo: order.OutTradeNo, TargetType: order.TargetType, TargetID: order.TargetID, TeamID: order.TeamID, AppID: trade.AppID, SellerID: trade.SellerID, AmountFen: trade.AmountFen, TradeNo: trade.TradeNo, PaidAt: paidAt, NotifyPayload: payload})
		if err == nil && !duplicate && deps.ConfigEvents != nil {
			deps.ConfigEvents.Emit(events.DomainTeams)
		}
		return saved, err
	case "TRADE_CLOSED":
		return deps.Store.ClosePaymentOrder(ctx, order.OutTradeNo, deps.Now())
	case "WAIT_BUYER_PAY":
		return order, nil
	default:
		return nil, store.ErrPaymentConflict
	}
}

func reconcilePaymentOrder(ctx context.Context, deps *Deps, order *store.PaymentOrderRecord) (*store.PaymentOrderRecord, error) {
	if order.Status == store.PaymentPaid {
		return order, nil
	}
	client, err := paymentClientForOrder(ctx, deps, order)
	if err != nil {
		return nil, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, paymentQueryTimeout)
	defer cancel()
	trade, err := client.Query(queryCtx, order.OutTradeNo)
	if errors.Is(err, alipay.ErrTradeNotExist) {
		// 本地到期本身不能证明未付款；支付宝验签确认无此交易且超过绝对截止后才关闭。
		if deps.Now().After(order.ExpiresAt.Add(5 * time.Minute)) {
			return deps.Store.ClosePaymentOrder(ctx, order.OutTradeNo, deps.Now())
		}
		return order, nil
	}
	if err != nil {
		return nil, err
	}
	saved, err := applyPaymentTrade(ctx, deps, order, *trade, []byte(`{"source":"query"}`))
	if err != nil {
		return nil, err
	}
	if trade.Status == "WAIT_BUYER_PAY" && deps.Now().After(order.ExpiresAt) {
		closeCtx, cancelClose := context.WithTimeout(ctx, paymentQueryTimeout)
		defer cancelClose()
		closed, err := client.Close(closeCtx, order.OutTradeNo)
		if err != nil {
			return nil, err
		}
		if closed.OutTradeNo != order.OutTradeNo || closed.AppID != order.AppID || closed.SellerID != order.SellerID || closed.Status != "TRADE_CLOSED" || closed.TradeNo != trade.TradeNo {
			return nil, store.ErrPaymentConflict
		}
		return deps.Store.ClosePaymentOrder(ctx, order.OutTradeNo, deps.Now())
	}
	return saved, nil
}

// StartPaymentReconciler walks all pending orders with a stable cursor. A failed
// provider call cannot starve older pages; retries and multiple API instances are
// safe because only the store transaction can credit an order.
func StartPaymentReconciler(ctx context.Context, deps *Deps, interval time.Duration) {
	if deps == nil || deps.Store == nil {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	logWarn := func(message string, args ...any) {
		if deps.Logger != nil {
			deps.Logger.Warn(message, args...)
		}
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		filter := store.PaymentOrderFilter{Status: store.PaymentPending, Limit: 20}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			orders, err := deps.Store.ListPaymentOrders(ctx, filter)
			if err != nil {
				logWarn("支付订单核对列表不可用")
				continue
			}
			for i := range orders {
				if ctx.Err() != nil {
					return
				}
				if _, err := reconcilePaymentOrder(ctx, deps, &orders[i]); err != nil {
					logWarn("支付订单等待再次核对", "outTradeNo", orders[i].OutTradeNo)
				}
			}
			if len(orders) < int(filter.Limit) {
				filter.BeforeCreatedAt = nil
				filter.BeforeID = ""
			} else {
				last := orders[len(orders)-1]
				filter.BeforeCreatedAt = &last.CreatedAt
				filter.BeforeID = last.ID
			}
		}
	}()
}
