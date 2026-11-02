package apiapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/alipay"
	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

const paymentOrderTTL = 30 * time.Minute
const paymentQueryTimeout = 15 * time.Second

func paymentOrderPublic(order *store.PaymentOrderRecord) gin.H {
	if order == nil {
		return nil
	}
	return gin.H{"id": order.ID, "outTradeNo": order.OutTradeNo, "targetType": order.TargetType,
		"targetId": order.TargetID, "teamId": order.TeamID, "userId": order.UserID,
		"amountYuan": alipay.FormatAmountFen(order.AmountFen), "creditAmount": packageCreditAmount(order.CreditMicro),
		"status": order.Status, "testPayment": order.TestPayment, "purchaseKind": order.PurchaseKind, "package": publicPackageSnapshot(order.PackageSnapshot), "tradeNo": order.TradeNo,
		"subject": order.Subject, "createdAt": order.CreatedAt, "expiresAt": order.ExpiresAt,
		"paidAt": order.PaidAt, "closedAt": order.ClosedAt}
}

func paymentNotifyPayload(values url.Values) []byte {
	// 只保存足以对账的支付宝元数据，永不把签名、买家账号或原始凭据写入账本。
	allowed := []string{"out_trade_no", "trade_no", "app_id", "seller_id", "total_amount", "trade_status", "gmt_payment", "notify_time"}
	row := make(map[string]string, len(allowed))
	for _, key := range allowed {
		if value := values.Get(key); value != "" {
			row[key] = value
		}
	}
	raw, _ := json.Marshal(row)
	return raw
}

func paymentConfigForOrder(ctx context.Context, deps *Deps, order *store.PaymentOrderRecord) (paymentConfiguration, error) {
	if order == nil || order.ConfigurationID == "" {
		return paymentConfiguration{}, errors.New("payment configuration unavailable")
	}
	return loadPaymentConfig(ctx, deps, order.ConfigurationID)
}

func paymentClientForOrder(ctx context.Context, deps *Deps, order *store.PaymentOrderRecord) (*alipay.Client, error) {
	config, err := paymentConfigForOrder(ctx, deps, order)
	if err != nil {
		return nil, err
	}
	return paymentClient(deps, config)
}

func writePaymentCheckout(c *gin.Context, deps *Deps, order *store.PaymentOrderRecord, mobile bool) {
	if order.Status != store.PaymentPending {
		c.JSON(200, okData(gin.H{"order": paymentOrderPublic(order), "payUrl": ""}))
		return
	}
	client, err := paymentClientForOrder(c.Request.Context(), deps, order)
	if err != nil {
		c.JSON(503, fail(503, "支付配置版本不可用，暂时无法生成收银台链接"))
		return
	}
	payURL, err := client.PagePayURL(order.OutTradeNo, order.Subject, order.AmountFen, mobile, order.ExpiresAt)
	if err != nil {
		c.JSON(409, fail(409, "订单已过期，请重新创建充值订单"))
		return
	}
	c.JSON(200, okData(gin.H{"order": paymentOrderPublic(order), "payUrl": payURL}))
}

func createPaymentReference(userID, requestID string) string {
	// 商户订单号绑定操作者和幂等键；网络重试与并发点击复用同一支付事实。
	return "md" + paymentHash(userID + ":" + requestID)[:48]
}

func paymentTarget(ctx context.Context, deps *Deps, actor *store.UserRecord, targetType store.PaymentTargetType, teamID string) (targetID, billingTeamID string, err error) {
	if targetType == store.PaymentTargetPersonal {
		team, ensureErr := deps.Store.EnsurePersonalAccount(ctx, actor.ID)
		if ensureErr != nil {
			return "", "", ensureErr
		}
		return actor.ID, team.ID, nil
	}
	if targetType != store.PaymentTargetTeam || !uuidShape(teamID) {
		return "", "", store.ErrInvalidInput
	}
	if _, ok := requireTeamAccessForPayment(ctx, deps, actor, teamID); !ok {
		return "", "", errors.New("forbidden")
	}
	return teamID, teamID, nil
}

func requireTeamAccessForPayment(ctx context.Context, deps *Deps, actor *store.UserRecord, teamID string) (*store.TeamMembership, bool) {
	if private, err := deps.Store.IsPersonalAccountTeam(ctx, teamID); err != nil || private {
		return nil, false
	}
	if actor.Role == store.RoleRoot {
		team, err := deps.Store.GetTeam(ctx, teamID)
		return func() (*store.TeamMembership, bool) {
			if err != nil || team == nil || team.DisabledAt != nil {
				return nil, false
			}
			return &store.TeamMembership{Team: *team, Role: store.MemberRoleOwner}, true
		}()
	}
	memberships, err := deps.Store.ListTeamsForUser(ctx, actor.ID)
	if err != nil {
		return nil, false
	}
	for i := range memberships {
		m := &memberships[i]
		if m.Team.ID == teamID && m.Team.DisabledAt == nil && m.MemberDisabledAt == nil && store.IsTeamAdmin(m.Role) {
			return m, true
		}
	}
	return nil, false
}

func registerPaymentRoutes(router *gin.Engine, deps *Deps) {
	router.GET("/console/api/payments/account", func(c *gin.Context) {
		actor, ok := requireConsoleUser(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		mode, err := deps.Store.GetBillingMode(ctx, actor.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		personal, err := deps.Store.GetPersonalAccount(ctx, actor.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		result := gin.H{"mode": mode, "enabled": paymentEnabled(ctx, deps), "personal": nil, "team": nil}
		if personal != nil {
			result["personal"] = billingAccountPublic(personal, actor.ID, "个人余额")
		} else {
			result["personal"] = gin.H{"id": actor.ID, "name": "个人余额", "quotaCredits": "0.000000", "usedCredits": "0.000000", "balanceCredits": "0.000000"}
		}
		if membership, resolveErr := store.ResolveUserTeam(ctx, deps.Store, actor.ID); resolveErr == nil && membership != nil {
			result["team"] = billingAccountPublic(&membership.Team, membership.Team.ID, membership.Team.Name)
		}
		c.JSON(http.StatusOK, okData(result))
	})

	router.PUT("/console/api/payments/account", func(c *gin.Context) {
		actor, ok := requireConsoleUser(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		body, valid := readObjectBodyLimited(c, 8<<10)
		if !valid {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		if !rejectUnknownFields(c, body, "mode") {
			return
		}
		mode, valid := body["mode"].(string)
		if !valid || (mode != "personal" && mode != "team") {
			c.JSON(400, fail(400, "消费归属必须为 personal 或 team"))
			return
		}
		if mode == "team" {
			if membership, err := store.ResolveUserTeam(c.Request.Context(), deps.Store, actor.ID); err != nil || membership == nil {
				c.JSON(409, fail(409, "当前没有唯一可用团队"))
				return
			}
		}
		if err := deps.Store.SetBillingMode(c.Request.Context(), actor.ID, mode); err != nil {
			internalError(c, deps, err)
			return
		}
		writeAudit(c.Request.Context(), deps, actor, "payment.billing-mode.update", "user", actor.ID, gin.H{"mode": mode})
		if deps.ConfigEvents != nil {
			deps.ConfigEvents.Emit(events.DomainTeams)
		}
		c.JSON(200, okData(gin.H{"mode": mode}))
	})

	router.POST("/console/api/payments/orders", func(c *gin.Context) {
		actor, ok := requireConsoleUser(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		body, valid := readObjectBodyLimited(c, 16<<10)
		if !valid {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		if !rejectUnknownFields(c, body, "requestId", "targetType", "teamId", "amountYuan", "testPayment", "mobile") {
			return
		}
		requestID, valid := body["requestId"].(string)
		if !valid {
			c.JSON(400, fail(400, "requestId 必须为 UUID"))
			return
		}
		requestKey, err := uuid.Parse(requestID)
		if err != nil {
			c.JSON(400, fail(400, "requestId 必须为 UUID"))
			return
		}
		targetType, valid := body["targetType"].(string)
		if !valid {
			c.JSON(400, fail(400, "targetType 不合法"))
			return
		}
		amount, valid := body["amountYuan"].(string)
		if !valid {
			c.JSON(400, fail(400, "充值金额不合法"))
			return
		}
		amountFen, err := alipay.ParseAmountFen(amount)
		if err != nil || amountFen <= 0 || amountFen > 10_000_000 {
			c.JSON(400, fail(400, "金额需为正数，最多两位小数且不超过 100000 元"))
			return
		}
		testPayment, _ := body["testPayment"].(bool)
		mobile, _ := body["mobile"].(bool)
		for _, field := range []string{"testPayment", "mobile"} {
			if value, present := body[field]; present {
				if _, valid := value.(bool); !valid {
					c.JSON(400, fail(400, "布尔参数不合法"))
					return
				}
			}
		}
		if testPayment && actor.Role != store.RoleRoot {
			c.JSON(403, fail(403, "0.01 元测试支付仅限 root"))
			return
		}
		if testPayment && amountFen != 1 {
			c.JSON(400, fail(400, "测试支付金额必须为 0.01 元"))
			return
		}
		if !testPayment && amountFen < 100 {
			c.JSON(400, fail(400, "普通充值最低 1 元"))
			return
		}
		targetID, teamID, err := paymentTarget(ctx, deps, actor, store.PaymentTargetType(targetType), stringValue(body["teamId"]))
		if err != nil {
			c.JSON(403, fail(403, "没有目标账户的充值权限"))
			return
		}
		outTradeNo := createPaymentReference(actor.ID, requestKey.String())
		existing, err := deps.Store.GetPaymentOrder(ctx, outTradeNo)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if existing != nil {
			if existing.UserID != actor.ID || existing.TargetType != store.PaymentTargetType(targetType) || existing.TargetID != targetID || existing.TeamID != teamID || existing.AmountFen != amountFen || existing.TestPayment != testPayment {
				c.JSON(409, fail(409, "同一 requestId 的充值金额或目标不能改变"))
				return
			}
			writePaymentCheckout(c, deps, existing, mobile)
			return
		}
		config, err := loadPaymentConfig(ctx, deps, "")
		if err != nil || !config.Enabled {
			c.JSON(503, fail(503, "支付宝充值暂未启用"))
			return
		}
		_, err = paymentClient(deps, config)
		if err != nil {
			c.JSON(503, fail(503, "支付宝配置不完整"))
			return
		}
		now := deps.Now()
		if now.IsZero() {
			now = time.Now().UTC()
		}
		order := store.PaymentOrderRecord{ID: uuid.NewString(), OutTradeNo: outTradeNo, Provider: store.PaymentProviderAlipay, ConfigurationID: config.ID, TargetType: store.PaymentTargetType(targetType), TargetID: targetID, TeamID: teamID, UserID: actor.ID, AppID: config.AppID, SellerID: config.SellerID, AmountFen: amountFen, CreditMicro: amountFen * store.MicroCreditsPerFen, Subject: "门道 credit 充值", TestPayment: testPayment, CreatedAt: now.UTC(), ExpiresAt: now.UTC().Add(paymentOrderTTL)}
		saved, err := deps.Store.CreatePaymentOrder(ctx, order)
		if errors.Is(err, store.ErrPaymentConflict) {
			c.JSON(409, fail(409, "同一 requestId 的充值参数不能改变，请刷新订单"))
			return
		}
		if err != nil {
			internalError(c, deps, err)
			return
		}
		writePaymentCheckout(c, deps, saved, mobile)
	})

	registerPaymentReadRoutes(router, deps)
}

func registerPaymentReadRoutes(router *gin.Engine, deps *Deps) {
	router.GET("/console/api/payments/orders", func(c *gin.Context) {
		actor, ok := requireConsoleUser(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		targetTypeRaw, teamID := c.Query("targetType"), c.Query("teamId")
		if targetTypeRaw != "" && targetTypeRaw != string(store.PaymentTargetPersonal) && targetTypeRaw != string(store.PaymentTargetTeam) {
			c.JSON(400, fail(400, "targetType 不合法"))
			return
		}
		if teamID != "" && (!uuidShape(teamID) || targetTypeRaw != string(store.PaymentTargetTeam)) {
			c.JSON(400, fail(400, "teamId 只能用于团队订单且必须为 UUID"))
			return
		}
		filter := store.PaymentOrderFilter{Limit: 51, TargetType: store.PaymentTargetType(targetTypeRaw), TeamID: teamID}
		switch {
		case actor.Role == store.RoleRoot && targetTypeRaw == string(store.PaymentTargetPersonal):
			// root 显式查看个人时只展示自己的个人订单；不传 targetType 才是全量视图。
			filter.UserID = actor.ID
		case actor.Role == store.RoleRoot:
			filter.UserID = ""
		case targetTypeRaw == string(store.PaymentTargetTeam):
			if teamID == "" {
				c.JSON(400, fail(400, "团队订单必须指定 teamId"))
				return
			}
			if _, ok := requireTeamAccessForPayment(c.Request.Context(), deps, actor, teamID); !ok {
				c.JSON(403, fail(403, "没有该团队权限"))
				return
			}
			// 团队管理员可查看整个团队的充值记录，而不是只看自己的下单记录。
			filter.UserID = ""
		case targetTypeRaw == "" || targetTypeRaw == string(store.PaymentTargetPersonal):
			filter.TargetType = store.PaymentTargetPersonal
			filter.UserID = actor.ID
		}
		cursorRaw := c.Query("cursor")
		cursorCreatedAt, cursorID, cursorOK := decodeUsageCursor(cursorRaw)
		if cursorRaw != "" && !cursorOK {
			c.JSON(400, fail(400, "分页游标不合法"))
			return
		}
		if cursorOK {
			parsed, valid := parseQueryTime(cursorCreatedAt)
			if !valid {
				c.JSON(400, fail(400, "分页游标不合法"))
				return
			}
			filter.BeforeCreatedAt, filter.BeforeID = &parsed, cursorID
		}
		orders, err := deps.Store.ListPaymentOrders(c.Request.Context(), filter)
		if err != nil {
			if errors.Is(err, store.ErrInvalidInput) {
				c.JSON(400, fail(400, "订单筛选条件不合法"))
			} else {
				internalError(c, deps, err)
			}
			return
		}
		hasMore := len(orders) > 50
		page := orders
		if hasMore {
			page = orders[:50]
		}
		rows := make([]gin.H, 0, len(page))
		for i := range page {
			rows = append(rows, paymentOrderPublic(&page[i]))
		}
		var nextCursor *string
		if hasMore && len(page) > 0 {
			last := page[len(page)-1]
			cursor := encodeUsageCursor(last.CreatedAt, last.ID)
			nextCursor = &cursor
		}
		c.JSON(200, okData(gin.H{"orders": rows, "nextCursor": nextCursor}))
	})
	router.GET("/console/api/payments/orders/:outTradeNo", func(c *gin.Context) {
		order, ok := paymentVisibleOrder(c, deps)
		if !ok {
			return
		}
		c.JSON(200, okData(gin.H{"order": paymentOrderPublic(order)}))
	})
	router.POST("/console/api/payments/orders/:outTradeNo/reconcile", func(c *gin.Context) {
		order, ok := paymentVisibleOrder(c, deps)
		if !ok {
			return
		}
		saved, err := reconcilePaymentOrder(c.Request.Context(), deps, order)
		if err != nil {
			c.JSON(502, fail(502, "支付宝核对未完成，请稍后重试"))
			return
		}
		c.JSON(200, okData(gin.H{"order": paymentOrderPublic(saved)}))
	})
}

func paymentVisibleOrder(c *gin.Context, deps *Deps) (*store.PaymentOrderRecord, bool) {
	actor, ok := requireConsoleUser(c.Request.Context(), deps, c)
	if !ok {
		return nil, false
	}
	order, err := deps.Store.GetPaymentOrder(c.Request.Context(), c.Param("outTradeNo"))
	if err != nil || order == nil {
		c.JSON(404, fail(404, "订单不存在"))
		return nil, false
	}
	if order.UserID == actor.ID && order.TargetType == store.PaymentTargetPersonal {
		return order, true
	}
	if actor.Role == store.RoleRoot {
		return order, true
	}
	if order.TargetType == store.PaymentTargetTeam {
		if _, ok := requireTeamAccessForPayment(c.Request.Context(), deps, actor, order.TeamID); ok {
			return order, true
		}
	}
	c.JSON(403, fail(403, "没有该订单权限"))
	return nil, false
}

func registerPaymentPublicRoutes(router *gin.Engine, deps *Deps) {
	router.POST("/api/v1/payments/alipay/notify", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if !strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "application/x-www-form-urlencoded") || c.Request.URL.RawQuery != "" {
			c.String(400, "failure")
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		if err := c.Request.ParseForm(); err != nil || len(c.Request.PostForm) > 64 {
			c.String(400, "failure")
			return
		}
		values := c.Request.PostForm
		for _, items := range values {
			if len(items) != 1 {
				c.String(400, "failure")
				return
			}
		}
		order, err := deps.Store.GetPaymentOrder(c.Request.Context(), values.Get("out_trade_no"))
		if err != nil {
			c.String(503, "failure")
			return
		}
		if order == nil {
			c.String(400, "failure")
			return
		}
		client, err := paymentClientForOrder(c.Request.Context(), deps, order)
		if err != nil {
			c.String(503, "failure")
			return
		}
		trade, err := client.VerifyNotification(values)
		if err != nil {
			c.String(400, "failure")
			return
		}
		saved, err := applyPaymentTrade(c.Request.Context(), deps, order, trade, paymentNotifyPayload(values))
		if err != nil || saved == nil {
			c.String(503, "failure")
			return
		}
		c.String(200, "success")
	})
	router.GET("/api/v1/payments/alipay/return", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>支付已返回</title><body><main><h1>支付已返回</h1><p>请返回应用核对订单状态与账户余额。</p></main></body></html>`))
	})
}

func billingAccountPublic(team *store.TeamRecord, id, name string) gin.H {
	return gin.H{"id": id, "name": name, "quotaCredits": team.QuotaCredits, "usedCredits": team.UsedCredits, "reservedCredits": team.ReservedCredits, "balanceCredits": balanceCredits(team), "unlimited": team.Unlimited}
}
func balanceCredits(team *store.TeamRecord) string {
	quota, _ := credits.ParseCreditsToMicro(team.QuotaCredits)
	used, _ := credits.ParseCreditsToMicro(team.UsedCredits)
	return credits.FormatMicroCredits(quota - used)
}
func stringValue(value any) string { s, _ := value.(string); return s }
func paymentEnabled(ctx context.Context, deps *Deps) bool {
	config, err := loadPaymentConfig(ctx, deps, "")
	return err == nil && config.Enabled
}
func paymentHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
