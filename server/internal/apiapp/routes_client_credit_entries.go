package apiapp

import (
	"context"
	"encoding/base64"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 客户端点数明细只读投影已提交的消费和额度变化；统计及权限过滤由同一 Store 快照完成。

const (
	clientCreditEntriesDefaultLimit = 50
	clientCreditEntriesMaxLimit     = 100
)

// 范围按上海墙钟自然日切分，与 console 趋势图分桶口径一致；存储与导出仍按 UTC。
var creditLedgerZone = time.FixedZone("Asia/Shanghai", 8*3600)

func creditLedgerRangeFrom(rangeKey string, now time.Time) (*time.Time, bool) {
	switch rangeKey {
	case "", "all":
		return nil, true
	case "7d", "30d":
		days := 7
		if rangeKey == "30d" {
			days = 30
		}
		local := now.In(creditLedgerZone)
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, creditLedgerZone).
			AddDate(0, 0, -(days - 1)).UTC()
		return &start, true
	}
	return nil, false
}

// creditLedgerKind:把账本模型身份映射为客户端契约的记录类型。
// 固定费用（网页工具、原片导入/分析）按模型身份识别；媒体目录按展示投影的 kind 区分图片/视频/音频。
func creditLedgerKind(model string, display usageModelDisplay) string {
	switch model {
	case "deepseek:web-search":
		return "search"
	case "deepseek:web-fetch":
		return "fetch"
	case "media:reference-analysis":
		return "analysis"
	case "media:source-import":
		return "import"
	}
	switch display.Kind {
	case "text":
		return "chat"
	case "image", "video", "audio":
		return display.Kind
	}
	if strings.HasPrefix(model, "media:") {
		return "media"
	}
	return "chat"
}

func creditLedgerUsageEntry(c *gin.Context, record *store.UsageRecord) gin.H {
	display := usageDisplay(c, record.Model)
	kind := creditLedgerKind(record.Model, display)
	// 只有文字模型请求有 token 口径；固定费用与媒体记录的 token 列保持空值，不用 0 伪造拆分。
	var usage any
	if kind == "chat" {
		usage = gin.H{
			"inputTokens": record.InputTokens, "outputTokens": record.OutputTokens,
			"cachedInputTokens": record.CacheReadTokens, "cacheCreationTokens": record.CacheCreationTokens, "reasoningTokens": nil,
		}
	}
	var reasoningEffort, requestID any
	if record.ReasoningEffort != "" {
		reasoningEffort = record.ReasoningEffort
	}
	if record.RequestID != "" {
		requestID = record.RequestID
	}
	var quantity any
	if record.PriceSnapshot != nil && record.PriceSnapshot.Sale.Quantity > 0 {
		quantity = record.PriceSnapshot.Sale.Quantity
	}
	return gin.H{
		"kind": kind, "changeReason": "usage",
		// usage_records 只保存已结算事实；待核对任务在 pending settlement 中，不在本列表出现。
		"status":    "settled",
		"modelName": display.Name, "sessionId": nil, "requestId": requestID, "quantity": quantity,
		"reasoningEffort": reasoningEffort, "reasoningBudgetTokens": record.ReasoningBudgetTokens, "usage": usage,
	}
}

func creditLedgerQuotaReason(action string) (kind, reason string) {
	switch action {
	case "initial_grant":
		return "adjustment", "initial_grant"
	case "alipay_recharge":
		return "topup", "alipay_recharge"
	case "add_credit":
		return "adjustment", "manual_add"
	case "approve_quota_request":
		return "adjustment", "quota_request_approved"
	case "set_quota":
		return "adjustment", "quota_adjustment"
	default:
		return "adjustment", "other_adjustment"
	}
}

// 累计 micro 可超出 int64；用整数定点格式化，避免浮点和绝对值取反溢出。
func formatCreditLedgerMicro(raw string) (string, error) {
	micro, valid := new(big.Int).SetString(raw, 10)
	if !valid {
		return "", fmt.Errorf("invalid ledger micro total")
	}
	sign := ""
	if micro.Sign() < 0 {
		sign = "-"
	}
	micro.Abs(micro)
	whole, fraction := new(big.Int), new(big.Int)
	whole.QuoRem(micro, big.NewInt(1_000_000), fraction)
	return fmt.Sprintf("%s%s.%06d", sign, whole.String(), fraction.Int64()), nil
}

func creditLedgerEntry(c *gin.Context, item store.CreditLedgerItem) (gin.H, error) {
	var row gin.H
	switch item.Source {
	case "usage":
		if item.Usage == nil {
			return nil, fmt.Errorf("ledger usage record missing")
		}
		row = creditLedgerUsageEntry(c, item.Usage)
	case "quota":
		kind, reason := creditLedgerQuotaReason(item.QuotaAction)
		row = gin.H{"kind": kind, "changeReason": reason, "status": "settled",
			"modelName": nil, "sessionId": nil, "requestId": nil, "quantity": nil,
			"reasoningEffort": nil, "reasoningBudgetTokens": nil, "usage": nil}
	default:
		return nil, fmt.Errorf("unknown ledger source")
	}
	// 操作者只暴露团队可见的 displayName；邮箱和审计身份不进入客户端流水。
	var actorName any
	if item.ActorName != nil && strings.TrimSpace(*item.ActorName) != "" {
		actorName = *item.ActorName
	}
	row["actorName"] = actorName
	amount := big.NewInt(item.DeltaMicro)
	row["direction"] = "credit"
	if amount.Sign() < 0 {
		row["direction"] = "debit"
	}
	amount.Abs(amount)
	formatted, err := formatCreditLedgerMicro(amount.String())
	if err != nil {
		return nil, err
	}
	row["id"], row["createdAt"], row["credits"] = item.Source+":"+item.ID, item.CreatedAt.UTC().Format(time.RFC3339Nano), formatted
	return row, nil
}

func encodeCreditLedgerCursor(created time.Time, source, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("v1~" + created.UTC().Format(time.RFC3339Nano) + "~" + source + "~" + id))
}

func decodeCreditLedgerCursor(raw string) (created time.Time, source, id string, valid bool) {
	if len(raw) > 512 {
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return
	}
	parts := strings.Split(string(decoded), "~")
	// Store 游标只接受规范小写 UUID；API 先拒绝格式变体，避免无效输入变成内部错误。
	if len(parts) != 4 || parts[0] != "v1" || (parts[2] != "usage" && parts[2] != "quota") || !uuidShape(parts[3]) || parts[3] != strings.ToLower(parts[3]) {
		return
	}
	created, err = time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return
	}
	return created, parts[2], parts[3], true
}

func singleCreditLedgerQuery(query map[string][]string, key string) (string, bool, bool) {
	values, ok := query[key]
	if !ok {
		return "", false, true
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", true, false
	}
	return values[0], true, true
}

// clientCreditLedgerScope:把查看目标收窄为当前设备用户有权读取的账本范围。
// personal 只读本人隐藏账本；team 要求当前有效成员关系，owner/admin 可读全团队，普通成员只读本人记录。
// 设备 token 即使属于 root 也不获得跨团队视角。
func clientCreditLedgerScope(ctx context.Context, deps *Deps, user *store.UserRecord, targetType, teamID string) (filter store.CreditLedgerFilter, empty bool, code int, message string, err error) {
	switch targetType {
	case string(store.PaymentTargetPersonal):
		if teamID != "" {
			return filter, false, 400, "个人明细不能指定 teamId", nil
		}
		personal, err := deps.Store.GetPersonalAccount(ctx, user.ID)
		if err != nil {
			return filter, false, 0, "", err
		}
		if personal == nil {
			// 还没有个人账本就没有任何记录；不能让空 TeamID 退化成跨账本查询。
			return filter, true, 0, "", nil
		}
		return store.CreditLedgerFilter{TeamID: personal.ID, UsageUserID: user.ID}, false, 0, "", nil
	case string(store.PaymentTargetTeam):
		if !uuidShape(teamID) {
			return filter, false, 400, "团队明细必须指定 teamId", nil
		}
		memberships, err := deps.Store.ListTeamsForUser(ctx, user.ID)
		if err != nil {
			return filter, false, 0, "", err
		}
		for i := range memberships {
			m := &memberships[i]
			if m.Team.ID != teamID || m.Team.DisabledAt != nil || m.MemberDisabledAt != nil {
				continue
			}
			if store.IsTeamAdmin(m.Role) {
				return store.CreditLedgerFilter{TeamID: teamID}, false, 0, "", nil
			}
			return store.CreditLedgerFilter{TeamID: teamID, UsageUserID: user.ID}, false, 0, "", nil
		}
		return filter, false, 403, "没有该团队权限", nil
	}
	return filter, false, 400, "targetType 不合法", nil
}

func registerClientCreditEntryRoutes(router *gin.Engine, deps *Deps) {
	router.GET("/api/v1/client/credit/entries", func(c *gin.Context) {
		ctx := c.Request.Context()
		auth, ok := clientPaymentAuth(ctx, deps, c)
		if !ok {
			return
		}
		query := c.Request.URL.Query()
		for key := range query {
			if key != "targetType" && key != "teamId" && key != "range" && key != "cursor" && key != "limit" {
				c.JSON(400, fail(400, "查询参数不合法"))
				return
			}
		}
		targetType, hasTargetType, valid := singleCreditLedgerQuery(query, "targetType")
		if !valid {
			c.JSON(400, fail(400, "targetType 不合法"))
			return
		}
		if !hasTargetType {
			targetType = string(store.PaymentTargetPersonal)
		}
		rangeKey, hasRange, valid := singleCreditLedgerQuery(query, "range")
		if !valid {
			c.JSON(400, fail(400, "range 不合法"))
			return
		}
		if !hasRange {
			rangeKey = "all"
		}
		now := deps.Now().UTC()
		from, valid := creditLedgerRangeFrom(rangeKey, now)
		if !valid {
			c.JSON(400, fail(400, "range 不合法"))
			return
		}
		limit := int64(clientCreditEntriesDefaultLimit)
		if raw, hasLimit, valid := singleCreditLedgerQuery(query, "limit"); !valid {
			c.JSON(400, fail(400, "limit 不合法"))
			return
		} else if hasLimit {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed < 1 || parsed > clientCreditEntriesMaxLimit {
				c.JSON(400, fail(400, "limit 不合法"))
				return
			}
			limit = parsed
		}
		teamID, _, valid := singleCreditLedgerQuery(query, "teamId")
		if !valid {
			c.JSON(400, fail(400, "teamId 不合法"))
			return
		}
		filter, empty, code, message, err := clientCreditLedgerScope(ctx, deps, auth.User, targetType, teamID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if code != 0 {
			c.JSON(code, fail(code, message))
			return
		}
		cursor, hasCursor, valid := singleCreditLedgerQuery(query, "cursor")
		if !valid {
			c.JSON(400, fail(400, "分页游标不合法"))
			return
		}
		if hasCursor {
			created, source, id, valid := decodeCreditLedgerCursor(cursor)
			if !valid {
				c.JSON(400, fail(400, "分页游标不合法"))
				return
			}
			filter.CursorCreatedAt, filter.CursorSource, filter.CursorID = &created, source, id
		}
		c.Header("cache-control", "private, no-store")
		if empty {
			c.JSON(200, okData(gin.H{"range": rangeKey, "entries": []gin.H{}, "nextCursor": nil,
				"summary": gin.H{"totalCreditsIn": "0.000000", "totalCreditsOut": "0.000000", "netCredits": "0.000000", "entryCount": 0}}))
			return
		}
		filter.From = from
		if from != nil {
			filter.To = &now
		}
		if !prepareModelDisplay(c, deps) {
			return
		}
		filter.Limit = limit
		page, err := deps.Store.ListCreditLedger(ctx, filter)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		rows := make([]gin.H, 0, len(page.Items))
		for _, item := range page.Items {
			row, err := creditLedgerEntry(c, item)
			if err != nil {
				internalError(c, deps, err)
				return
			}
			rows = append(rows, row)
		}
		var next any
		if page.HasMore && len(page.Items) > 0 {
			last := page.Items[len(page.Items)-1]
			next = encodeCreditLedgerCursor(last.CreatedAt, last.Source, last.ID)
		}
		in, err := formatCreditLedgerMicro(page.Summary.TotalCreditsInMicro)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		out, err := formatCreditLedgerMicro(page.Summary.TotalCreditsOutMicro)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		net, err := formatCreditLedgerMicro(page.Summary.NetCreditsMicro)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(200, okData(gin.H{
			"range": rangeKey, "entries": rows, "nextCursor": next,
			"summary": gin.H{"totalCreditsIn": in, "totalCreditsOut": out, "netCredits": net, "entryCount": page.Summary.EntryCount},
		}))
	})
}
