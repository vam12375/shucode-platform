package apiapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/alipay"
	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func packageAmountYuan(fen int64) string     { return alipay.FormatAmountFen(fen) }
func packageCreditAmount(micro int64) string { return credits.FormatMicroCredits(micro) }

func publicPackage(p store.PaymentPackage, revision string) gin.H {
	return gin.H{"sku": p.SKU, "version": revision, "name": p.Name, "description": p.Description, "targetType": p.TargetType,
		"amountYuan": packageAmountYuan(p.AmountFen), "creditAmount": packageCreditAmount(p.AmountFen * store.MicroCreditsPerFen),
		"recommended": p.Recommended, "sortOrder": p.SortOrder}
}

func publicAdminPackage(p store.PaymentPackage) gin.H {
	row := publicPackage(p, "")
	delete(row, "version")
	row["status"] = p.Status
	return row
}

func publicPackageSnapshot(snapshot *store.PaymentPackageSnapshot) gin.H {
	if snapshot == nil {
		return nil
	}
	return gin.H{"sku": snapshot.SKU, "version": snapshot.CatalogRevision, "name": snapshot.Name, "description": snapshot.Description,
		"targetType": snapshot.TargetType, "amountYuan": packageAmountYuan(snapshot.AmountFen), "creditAmount": packageCreditAmount(snapshot.CreditMicro)}
}

func clientPaymentOrderPublic(order *store.PaymentOrderRecord) gin.H {
	return paymentOrderPublic(order)
}

func clientAccountPublic(team *store.TeamRecord, id, name string) gin.H {
	if team == nil {
		return nil
	}
	return gin.H{"id": id, "name": name, "quotaCredits": team.QuotaCredits, "usedCredits": team.UsedCredits,
		"reservedCredits": team.ReservedCredits, "balanceCredits": balanceCredits(team), "unlimited": team.Unlimited}
}

func clientPaymentAccount(ctx context.Context, deps *Deps, user *store.UserRecord) (gin.H, error) {
	personal, err := deps.Store.GetPersonalAccount(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	personalView := clientAccountPublic(personal, user.ID, "个人余额")
	if personalView == nil {
		personalView = gin.H{"id": user.ID, "name": "个人余额", "quotaCredits": "0.000000", "usedCredits": "0.000000", "reservedCredits": "0.000000", "balanceCredits": "0.000000", "unlimited": false}
	}
	memberships, err := deps.Store.ListTeamsForUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	eligible := make([]gin.H, 0, len(memberships))
	active := make([]store.TeamMembership, 0, len(memberships))
	for _, membership := range memberships {
		if membership.Team.DisabledAt != nil || membership.MemberDisabledAt != nil {
			continue
		}
		canRecharge := store.IsTeamAdmin(membership.Role)
		eligible = append(eligible, gin.H{"teamId": membership.Team.ID, "name": membership.Team.Name, "role": membership.Role,
			"canRecharge": canRecharge, "quotaCredits": membership.Team.QuotaCredits, "usedCredits": membership.Team.UsedCredits,
			"reservedCredits": membership.Team.ReservedCredits, "balanceCredits": balanceCredits(&membership.Team), "unlimited": membership.Team.Unlimited})
		active = append(active, membership)
	}
	resolution := "missing"
	var resolved *store.TeamMembership
	if len(active) == 1 {
		resolution, resolved = "unique", &active[0]
	} else if len(active) > 1 {
		resolution = "ambiguous"
	}
	mode, err := deps.Store.GetBillingMode(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	return gin.H{"enabled": paymentEnabled(ctx, deps), "mode": mode, "personal": personalView,
		"activeTeamResolution": resolution, "team": func() gin.H {
			if resolved == nil {
				return nil
			}
			return clientAccountPublic(&resolved.Team, resolved.Team.ID, resolved.Team.Name)
		}(),
		"eligibleTeams": eligible}, nil
}

func clientPaymentAuth(ctx context.Context, deps *Deps, c *gin.Context) (*AuthenticatedDevice, bool) {
	// 支付路由限定 Bearer，避免公共设备鉴权的 x-api-key fallback 接受不符合支付契约的凭据。
	authorizations := c.Request.Header.Values("Authorization")
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	if len(authorizations) != 1 || len(authorization) < 7 || !strings.EqualFold(authorization[:7], "bearer ") || strings.TrimSpace(authorization[7:]) == "" || strings.TrimSpace(c.GetHeader("x-api-key")) != "" {
		c.JSON(http.StatusUnauthorized, fail(http.StatusUnauthorized, "请使用 Bearer 设备凭据登录"))
		return nil, false
	}
	auth, err := authenticateDevice(ctx, deps, c)
	if err != nil {
		internalError(c, deps, err)
		return nil, false
	}
	if auth == nil {
		c.JSON(http.StatusUnauthorized, fail(http.StatusUnauthorized, "请先登录"))
		return nil, false
	}
	return auth, true
}

func clientPaymentTarget(ctx context.Context, deps *Deps, actor *store.UserRecord, targetType store.PaymentTargetType, teamID string) (string, string, error) {
	if targetType == store.PaymentTargetPersonal {
		personal, err := deps.Store.EnsurePersonalAccount(ctx, actor.ID)
		if err != nil {
			return "", "", err
		}
		return actor.ID, personal.ID, nil
	}
	if targetType != store.PaymentTargetTeam || !uuidShape(teamID) {
		return "", "", store.ErrInvalidInput
	}
	if _, ok := requireClientTeamRechargeAccess(ctx, deps, actor.ID, teamID); !ok {
		return "", "", errors.New("forbidden")
	}
	return teamID, teamID, nil
}

// 设备 token 即使属于 root 也不获得 console root 的跨团队管理能力。
func requireClientTeamRechargeAccess(ctx context.Context, deps *Deps, userID, teamID string) (*store.TeamMembership, bool) {
	memberships, err := deps.Store.ListTeamsForUser(ctx, userID)
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

func clientPaymentCatalogResponse(catalog *store.PaymentPackageCatalog, publishedOnly bool) gin.H {
	rows := make([]gin.H, 0, len(catalog.Packages))
	for _, p := range catalog.Packages {
		if publishedOnly && p.Status != store.PaymentPackagePublished {
			continue
		}
		if publishedOnly {
			rows = append(rows, publicPackage(p, catalog.Revision))
		} else {
			rows = append(rows, publicAdminPackage(p))
		}
	}
	result := gin.H{"revision": catalog.Revision, "packages": rows}
	if !publishedOnly {
		result["previousRevision"], result["publishedAt"], result["publishedBy"] = catalog.PreviousRevision, catalog.PublishedAt, catalog.PublishedBy
	}
	return result
}

func registerClientPaymentRoutes(router *gin.Engine, deps *Deps) {
	client := func(path string, handler gin.HandlerFunc) {
		router.Any(path, func(c *gin.Context) {
			if c.Request.Method == http.MethodOptions {
				c.Status(http.StatusNoContent)
				return
			}
			handler(c)
		})
	}
	client("/api/v1/client/payments/session-bindings", func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Status(http.StatusMethodNotAllowed)
			return
		}
		auth, ok := clientPaymentAuth(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		var body struct {
			SessionID         string  `json:"sessionId"`
			WorkspaceIdentity string  `json:"workspaceIdentity"`
			RemoteSessionID   *string `json:"remoteSessionId"`
			ParentSessionID   *string `json:"parentSessionId"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.SessionID) == "" || strings.TrimSpace(body.WorkspaceIdentity) == "" {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "请求参数无效"))
			return
		}
		input := store.BillingSessionBindingInput{SessionID: body.SessionID, WorkspaceIdentity: body.WorkspaceIdentity, ActorUserID: auth.User.ID, DeviceTokenID: auth.Device.ID}
		if body.RemoteSessionID != nil {
			input.RemoteSessionID = *body.RemoteSessionID
		}
		if body.ParentSessionID != nil {
			input.ParentSessionID = *body.ParentSessionID
		}
		binding, err := deps.Store.CreateBillingSessionBinding(c.Request.Context(), input)
		if err != nil {
			if respondBillingBindingError(c, err) {
				return
			}
			if errors.Is(err, store.ErrInvalidInput) {
				c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "请求参数无效"))
				return
			}
			internalError(c, deps, err)
			return
		}
		if _, err := ResolveClientBillingBindingAccount(c.Request.Context(), deps.Store, binding); err != nil {
			if respondBillingBindingError(c, err) {
				return
			}
			internalError(c, deps, err)
			return
		}
		c.JSON(http.StatusOK, okData(gin.H{"bindingId": binding.ID, "sessionId": binding.SessionID, "workspaceIdentity": binding.WorkspaceIdentity, "remoteSessionId": func() any {
			if binding.RemoteSessionID == "" {
				return nil
			}
			return binding.RemoteSessionID
		}(), "parentSessionId": func() any {
			if binding.ParentSessionID == "" {
				return nil
			}
			return binding.ParentSessionID
		}(), "billingAccount": gin.H{"id": binding.BillingAccountID, "type": binding.BillingAccountType, "displayName": binding.BillingAccountName}, "actorUserId": binding.ActorUserID, "createdAt": binding.CreatedAt.UTC().Format(time.RFC3339Nano)}))
	})
	client("/api/v1/client/payments/packages", func(c *gin.Context) {
		if c.Request.Method != http.MethodGet {
			c.Status(http.StatusMethodNotAllowed)
			return
		}
		if _, ok := clientPaymentAuth(c.Request.Context(), deps, c); !ok {
			return
		}
		catalog, err := deps.Store.GetPaymentPackageCatalog(c.Request.Context())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(http.StatusOK, okData(clientPaymentCatalogResponse(catalog, true)))
	})
	client("/api/v1/client/payments/account", func(c *gin.Context) {
		if c.Request.Method != http.MethodGet {
			c.Status(http.StatusMethodNotAllowed)
			return
		}
		auth, ok := clientPaymentAuth(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		account, err := clientPaymentAccount(c.Request.Context(), deps, auth.User)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(http.StatusOK, okData(account))
	})
	client("/api/v1/client/payments/account/mode", func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Status(http.StatusMethodNotAllowed)
			return
		}
		auth, ok := clientPaymentAuth(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		body, valid := readObjectBodyLimited(c, 8<<10)
		if !valid || !rejectUnknownFields(c, body, "mode") {
			return
		}
		mode, valid := body["mode"].(string)
		if !valid || (mode != "personal" && mode != "team") {
			c.JSON(400, fail(400, "消费归属必须为 personal 或 team"))
			return
		}
		if mode == "team" {
			selected, err := store.ResolveUserTeam(c.Request.Context(), deps.Store, auth.User.ID)
			if errors.Is(err, store.ErrAmbiguousTeam) {
				c.JSON(409, gin.H{"code": 409, "msg": "当前存在多个有效团队，无法设为默认消费账户", "details": gin.H{"reason": "team_resolution", "activeTeamResolution": "ambiguous"}})
				return
			}
			if err != nil {
				internalError(c, deps, err)
				return
			}
			if selected == nil {
				c.JSON(409, gin.H{"code": 409, "msg": "当前没有有效团队，无法设为默认消费账户", "details": gin.H{"reason": "team_resolution", "activeTeamResolution": "missing"}})
				return
			}
		}
		if err := deps.Store.SetBillingMode(c.Request.Context(), auth.User.ID, mode); err != nil {
			internalError(c, deps, err)
			return
		}
		writeAudit(c.Request.Context(), deps, auth.User, "payment.billing-mode.update", "user", auth.User.ID, gin.H{"mode": mode, "source": "device"})
		if deps.ConfigEvents != nil {
			deps.ConfigEvents.Emit(events.DomainTeams)
		}
		account, err := clientPaymentAccount(c.Request.Context(), deps, auth.User)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(http.StatusOK, okData(account))
	})

	client("/api/v1/client/payments/orders", func(c *gin.Context) {
		if c.Request.Method == http.MethodGet {
			clientPaymentList(c, deps)
			return
		}
		if c.Request.Method != http.MethodPost {
			c.Status(http.StatusMethodNotAllowed)
			return
		}
		clientPaymentCreate(c, deps)
	})
	client("/api/v1/client/payments/orders/:outTradeNo", func(c *gin.Context) {
		if c.Request.Method != http.MethodGet {
			c.Status(http.StatusMethodNotAllowed)
			return
		}
		auth, ok := clientPaymentAuth(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		order, found := clientVisiblePaymentOrder(c, deps, auth.User)
		if !found {
			return
		}
		c.JSON(200, okData(gin.H{"order": clientPaymentOrderPublic(order)}))
	})
	client("/api/v1/client/payments/orders/:outTradeNo/reconcile", func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Status(http.StatusMethodNotAllowed)
			return
		}
		auth, ok := clientPaymentAuth(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		order, found := clientVisiblePaymentOrder(c, deps, auth.User)
		if !found {
			return
		}
		saved, err := reconcilePaymentOrder(c.Request.Context(), deps, order)
		if err != nil {
			c.JSON(502, fail(502, "支付宝核对未完成，请稍后重试"))
			return
		}
		c.JSON(200, okData(gin.H{"order": clientPaymentOrderPublic(saved)}))
	})
}

func clientVisiblePaymentOrder(c *gin.Context, deps *Deps, actor *store.UserRecord) (*store.PaymentOrderRecord, bool) {
	order, err := deps.Store.GetPaymentOrder(c.Request.Context(), c.Param("outTradeNo"))
	if err != nil || order == nil {
		c.JSON(404, fail(404, "订单不存在"))
		return nil, false
	}
	if order.UserID == actor.ID && order.TargetType == store.PaymentTargetPersonal {
		return order, true
	}
	if order.TargetType == store.PaymentTargetTeam {
		if _, allowed := requireClientTeamRechargeAccess(c.Request.Context(), deps, actor.ID, order.TeamID); allowed {
			return order, true
		}
	}
	c.JSON(403, fail(403, "没有该订单权限"))
	return nil, false
}

func clientPaymentList(c *gin.Context, deps *Deps) {
	auth, ok := clientPaymentAuth(c.Request.Context(), deps, c)
	if !ok {
		return
	}
	targetType := c.Query("targetType")
	if targetType == "" {
		targetType = string(store.PaymentTargetPersonal)
	}
	if targetType != string(store.PaymentTargetPersonal) && targetType != string(store.PaymentTargetTeam) {
		c.JSON(400, fail(400, "targetType 不合法"))
		return
	}
	teamID := c.Query("teamId")
	if targetType == string(store.PaymentTargetTeam) && !uuidShape(teamID) {
		c.JSON(400, fail(400, "团队订单必须指定 teamId"))
		return
	}
	if targetType == string(store.PaymentTargetTeam) {
		if _, allowed := requireClientTeamRechargeAccess(c.Request.Context(), deps, auth.User.ID, teamID); !allowed {
			c.JSON(403, fail(403, "没有该团队权限"))
			return
		}
	}
	filter := store.PaymentOrderFilter{TargetType: store.PaymentTargetType(targetType), TeamID: teamID, Limit: 51}
	if targetType == string(store.PaymentTargetPersonal) {
		filter.TargetID, filter.UserID = auth.User.ID, auth.User.ID
	}
	if raw := c.Query("cursor"); raw != "" {
		created, id, valid := decodeUsageCursor(raw)
		if !valid {
			c.JSON(400, fail(400, "分页游标不合法"))
			return
		}
		parsed, valid := parseQueryTime(created)
		if !valid {
			c.JSON(400, fail(400, "分页游标不合法"))
			return
		}
		filter.BeforeCreatedAt, filter.BeforeID = &parsed, id
	}
	orders, err := deps.Store.ListPaymentOrders(c.Request.Context(), filter)
	if err != nil {
		internalError(c, deps, err)
		return
	}
	hasMore := len(orders) > 50
	page := orders
	if hasMore {
		page = orders[:50]
	}
	rows := make([]gin.H, 0, len(page))
	for i := range page {
		rows = append(rows, clientPaymentOrderPublic(&page[i]))
	}
	var next *string
	if hasMore {
		cursor := encodeUsageCursor(page[len(page)-1].CreatedAt, page[len(page)-1].ID)
		next = &cursor
	}
	c.JSON(200, okData(gin.H{"orders": rows, "nextCursor": next}))
}

func clientPaymentCreate(c *gin.Context, deps *Deps) {
	auth, ok := clientPaymentAuth(c.Request.Context(), deps, c)
	if !ok {
		return
	}
	body, valid := readObjectBodyLimited(c, 16<<10)
	if !valid {
		c.JSON(400, fail(400, "请求参数不合法"))
		return
	}
	if !rejectUnknownFields(c, body, "requestId", "targetType", "teamId", "purchaseKind", "sku", "version", "amountYuan", "mobile") {
		return
	}
	requestID, valid := body["requestId"].(string)
	requestUUID, err := uuid.Parse(requestID)
	if !valid || err != nil {
		c.JSON(400, fail(400, "requestId 必须为 UUID"))
		return
	}
	targetType, valid := body["targetType"].(string)
	if !valid || (targetType != string(store.PaymentTargetPersonal) && targetType != string(store.PaymentTargetTeam)) {
		c.JSON(400, fail(400, "targetType 不合法"))
		return
	}
	targetID, teamID, err := clientPaymentTarget(c.Request.Context(), deps, auth.User, store.PaymentTargetType(targetType), stringValue(body["teamId"]))
	if err != nil {
		if errors.Is(err, store.ErrInvalidInput) {
			c.JSON(400, fail(400, "充值目标不合法"))
			return
		}
		c.JSON(403, fail(403, "没有目标账户的充值权限"))
		return
	}
	kind, valid := body["purchaseKind"].(string)
	if !valid || (kind != store.PaymentPurchasePlan && kind != store.PaymentPurchaseCustom) {
		c.JSON(400, fail(400, "purchaseKind 不合法"))
		return
	}
	requestOut := createPaymentReference(auth.User.ID, requestUUID.String())
	existing, err := deps.Store.GetPaymentOrder(c.Request.Context(), requestOut)
	if err != nil {
		internalError(c, deps, err)
		return
	}
	if existing != nil {
		if existing.UserID != auth.User.ID || existing.TargetType != store.PaymentTargetType(targetType) || existing.TargetID != targetID || existing.TeamID != teamID || existing.PurchaseKind != kind {
			c.JSON(409, gin.H{"code": 409, "msg": "同一 requestId 的充值参数不能改变", "details": gin.H{"reason": "request_conflict"}})
			return
		}
		if kind == store.PaymentPurchasePlan {
			sku, _ := body["sku"].(string)
			version, _ := body["version"].(string)
			if existing.PackageSnapshot == nil || existing.PackageSnapshot.SKU != sku || existing.PackageSnapshot.CatalogRevision != version {
				c.JSON(409, gin.H{"code": 409, "msg": "套餐报价已变化，请重新选择", "details": gin.H{"reason": "request_conflict"}})
				return
			}
		}
		if kind == store.PaymentPurchaseCustom {
			amountText, _ := body["amountYuan"].(string)
			amount, parseErr := alipay.ParseAmountFen(amountText)
			if parseErr != nil || amount != existing.AmountFen {
				c.JSON(409, gin.H{"code": 409, "msg": "同一 requestId 的充值参数不能改变", "details": gin.H{"reason": "request_conflict"}})
				return
			}
		}
		if v, present := body["mobile"]; present {
			if _, ok := v.(bool); !ok {
				c.JSON(400, fail(400, "mobile 必须为布尔值"))
				return
			}
		}
		writePaymentCheckout(c, deps, existing, clientBoolValue(body["mobile"]))
		return
	}
	config, err := loadPaymentConfig(c.Request.Context(), deps, "")
	if err != nil || !config.Enabled {
		c.JSON(503, fail(503, "支付宝充值暂未启用"))
		return
	}
	if _, err = paymentClient(deps, config); err != nil {
		c.JSON(503, fail(503, "支付宝配置不完整"))
		return
	}
	now := deps.Now()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	order := store.PaymentOrderRecord{ID: uuid.NewString(), OutTradeNo: requestOut, Provider: store.PaymentProviderAlipay, ConfigurationID: config.ID, TargetType: store.PaymentTargetType(targetType), TargetID: targetID, TeamID: teamID, UserID: auth.User.ID, AppID: config.AppID, SellerID: config.SellerID, Subject: "门道 credit 充值", CreatedAt: now.UTC(), ExpiresAt: now.UTC().Add(paymentOrderTTL)}
	if kind == store.PaymentPurchasePlan {
		if _, hasAmount := body["amountYuan"]; hasAmount {
			c.JSON(400, fail(400, "套餐订单不能自定义金额"))
			return
		}
		if v, present := body["mobile"]; present {
			if _, ok := v.(bool); !ok {
				c.JSON(400, fail(400, "mobile 必须为布尔值"))
				return
			}
		}
		sku, skuOK := body["sku"].(string)
		version, versionOK := body["version"].(string)
		if !skuOK || !versionOK {
			c.JSON(400, fail(400, "套餐 sku 和 version 必填"))
			return
		}
		saved, err := deps.Store.CreatePlanPaymentOrder(c.Request.Context(), order, version, sku)
		if errors.Is(err, store.ErrPaymentPackageCatalogConflict) || errors.Is(err, store.ErrPaymentPackageUnavailable) {
			catalog, _ := deps.Store.GetPaymentPackageCatalog(c.Request.Context())
			var current *store.PaymentPackage
			if catalog != nil {
				for i := range catalog.Packages {
					if catalog.Packages[i].SKU == sku && catalog.Packages[i].Status == store.PaymentPackagePublished {
						current = &catalog.Packages[i]
						break
					}
				}
			}
			c.JSON(409, gin.H{"code": 409, "msg": "套餐报价已变化，请重新选择", "details": gin.H{"reason": "package_changed", "currentRevision": func() string {
				if catalog == nil {
					return ""
				}
				return catalog.Revision
			}(), "package": func() gin.H {
				if current == nil {
					return nil
				}
				return publicPackage(*current, catalog.Revision)
			}()}})
			return
		}
		if err != nil {
			internalError(c, deps, err)
			return
		}
		writePaymentCheckout(c, deps, saved, clientBoolValue(body["mobile"]))
		return
	}
	if v, present := body["mobile"]; present {
		if _, ok := v.(bool); !ok {
			c.JSON(400, fail(400, "mobile 必须为布尔值"))
			return
		}
	}
	if _, hasSKU := body["sku"]; hasSKU {
		c.JSON(400, fail(400, "自定义充值不能指定套餐"))
		return
	}
	if _, hasVersion := body["version"]; hasVersion {
		c.JSON(400, fail(400, "自定义充值不能指定套餐版本"))
		return
	}
	amount, amountOK := body["amountYuan"].(string)
	amountFen, parseErr := alipay.ParseAmountFen(amount)
	if !amountOK || parseErr != nil || amountFen < 100 || amountFen > 10_000_000 {
		c.JSON(400, fail(400, "自定义充值金额需为 1–100000 元，最多两位小数"))
		return
	}
	order.AmountFen, order.CreditMicro = amountFen, amountFen*store.MicroCreditsPerFen
	order.PurchaseKind = store.PaymentPurchaseCustom
	saved, err := deps.Store.CreatePaymentOrder(c.Request.Context(), order)
	if err != nil {
		internalError(c, deps, err)
		return
	}
	writePaymentCheckout(c, deps, saved, clientBoolValue(body["mobile"]))
}

func clientBoolValue(v any) bool { b, _ := v.(bool); return b }

func ResolveClientBillingBindingAccount(ctx context.Context, st store.PlatformStore, binding *store.BillingSessionBinding) (*store.TeamMembership, error) {
	return store.ResolveBillingBindingAccount(ctx, st, binding)
}
