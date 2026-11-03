package apiapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func requestCreditEntries(t *testing.T, f *apiFixture, token string, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/client/credit/entries?"+query.Encode(), nil)
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func settleUsageForTest(t *testing.T, f *apiFixture, teamID, userID, model string, micro int64, at time.Time) {
	t.Helper()
	ctx := context.Background()
	identity := uuid.NewString()
	result, err := f.store.AdmitUsage(ctx, store.ReserveQuotaInput{
		ID: identity, TeamID: teamID, UserID: userID, RequestID: "req-" + identity, CreatedAt: at, TTL: time.Minute,
	})
	if err != nil || result != store.ReserveReserved {
		t.Fatalf("准入失败: %s %v", result, err)
	}
	budget := int64(8192)
	if err := f.store.SettleReservation(ctx, identity, store.UsageRecord{
		UserID: userID, TeamID: teamID, Model: model, UpstreamModel: model, RequestID: "req-" + identity,
		InputTokens: 120, OutputTokens: 30, CacheReadTokens: 100, CacheCreationTokens: 20, ReasoningEffort: "high", ReasoningBudgetTokens: &budget,
		CreditsMicro: micro, Status: store.UsageStatusOk, CreatedAt: at,
	}); err != nil {
		t.Fatalf("结算失败: %v", err)
	}
}

func entriesOf(t *testing.T, rec *httptest.ResponseRecorder) (map[string]any, []map[string]any) {
	t.Helper()
	data := dataOf(t, rec)
	rawData, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contracts.ParseCreditLedgerPage(rawData); err != nil {
		t.Fatalf("实际 API 响应必须符合客户端契约: %v: %s", err, rawData)
	}
	raw, ok := data["entries"].([]any)
	if !ok {
		t.Fatalf("缺 entries: %s", rec.Body.String())
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		rows = append(rows, item.(map[string]any))
	}
	return data, rows
}

func TestClientCreditEntriesPersonalLedger(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	user, err := f.store.CreateUser(ctx, store.UserInput{Email: "ledger-personal@example.com", PasswordHash: "x", DisplayName: "Ledger", Role: store.RoleUser})
	if err != nil {
		t.Fatalf("创建用户: %v", err)
	}
	personal, err := f.store.EnsurePersonalAccount(ctx, user.ID)
	if err != nil {
		t.Fatalf("个人账本: %v", err)
	}
	unlimited := true
	if _, err := f.store.UpdateTeam(ctx, personal.ID, store.UpdateTeamPatch{Unlimited: &unlimited}); err != nil {
		t.Fatalf("个人额度: %v", err)
	}
	now := time.Now().UTC()
	// 演示目录内置 claude-sonnet-4-5；对外名必须来自目录展示名投影，而不是回显内部模型 ID。
	settleUsageForTest(t, f, personal.ID, user.ID, "claude-sonnet-4-5", 12_345, now.Add(-time.Minute))
	settleUsageForTest(t, f, personal.ID, user.ID, "media:source-import", 500_000, now.Add(-2*time.Minute))
	settleUsageForTest(t, f, personal.ID, user.ID, "deepseek:web-search", 5_000, now.AddDate(0, 0, -40))
	token := createDeviceTokenForTest(t, f, user.ID)

	rec := requestCreditEntries(t, f, token, url.Values{"targetType": {"personal"}, "range": {"7d"}})
	if rec.Code != 200 {
		t.Fatalf("明细接口: %d %s", rec.Code, rec.Body.String())
	}
	data, rows := entriesOf(t, rec)
	if data["range"] != "7d" || len(rows) != 2 {
		t.Fatalf("近 7 日应有两条消费: %s", rec.Body.String())
	}
	first := rows[0]
	if first["kind"] != "chat" || first["direction"] != "debit" || first["status"] != "settled" || first["credits"] != "0.012345" {
		t.Fatalf("文字记录投影不符: %v", first)
	}
	if first["modelName"] != "Claude Sonnet 4.5" {
		t.Fatalf("应使用对外展示名: %v", first["modelName"])
	}
	usage, ok := first["usage"].(map[string]any)
	if !ok || usage["inputTokens"] != float64(120) || usage["cachedInputTokens"] != float64(100) || usage["cacheCreationTokens"] != float64(20) || usage["reasoningTokens"] != nil {
		t.Fatalf("token 拆分不符: %v", first["usage"])
	}
	if first["changeReason"] != "usage" || first["reasoningEffort"] != "high" || first["requestId"] == nil || first["reasoningBudgetTokens"] != float64(8192) {
		t.Fatalf("思考档位与请求身份应保留: %v", first)
	}
	if first["actorName"] != "Ledger" {
		t.Fatalf("团队明细只展示安全操作者昵称: %v", first)
	}
	for _, forbidden := range []string{"upstreamModel", "priceSnapshot", "providerId", "costMicro"} {
		if _, leaked := first[forbidden]; leaked {
			t.Fatalf("不得泄漏经营字段 %s: %v", forbidden, first)
		}
	}
	second := rows[1]
	if second["kind"] != "import" || second["usage"] != nil || second["credits"] != "0.500000" {
		t.Fatalf("固定费用记录应无 token 拆分: %v", second)
	}
	summary := data["summary"].(map[string]any)
	if summary["totalCreditsIn"] != "0.000000" || summary["totalCreditsOut"] != "0.512345" || summary["netCredits"] != "-0.512345" || summary["entryCount"] != float64(2) {
		t.Fatalf("汇总应由服务端按范围计算: %v", summary)
	}

	all := requestCreditEntries(t, f, token, url.Values{"range": {"all"}, "limit": {"2"}})
	allData, allRows := entriesOf(t, all)
	if len(allRows) != 2 || allData["nextCursor"] == nil {
		t.Fatalf("全部范围应分页: %s", all.Body.String())
	}
	next := requestCreditEntries(t, f, token, url.Values{"range": {"all"}, "limit": {"2"}, "cursor": {allData["nextCursor"].(string)}})
	nextData, nextRows := entriesOf(t, next)
	if len(nextRows) != 1 || nextRows[0]["kind"] != "search" || nextData["nextCursor"] != nil {
		t.Fatalf("第二页应只剩历史搜索记录: %s", next.Body.String())
	}
	if nextData["summary"].(map[string]any)["entryCount"] != float64(3) || nextData["summary"].(map[string]any)["totalCreditsOut"] != "0.517345" {
		t.Fatalf("汇总不随游标变化: %s", next.Body.String())
	}

	if bad := requestCreditEntries(t, f, token, url.Values{"range": {"90d"}}); bad.Code != 400 {
		t.Fatalf("非法范围应 400: %d", bad.Code)
	}
	if unauthorized := requestCreditEntries(t, f, "", nil); unauthorized.Code != 401 {
		t.Fatalf("缺少凭据应 401: %d", unauthorized.Code)
	}
}

func TestClientCreditEntriesTeamScope(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	owner, err := f.store.CreateUser(ctx, store.UserInput{Email: "ledger-owner@example.com", PasswordHash: "x", DisplayName: "Owner", Role: store.RoleUser})
	if err != nil {
		t.Fatalf("创建 owner: %v", err)
	}
	member, err := f.store.CreateUser(ctx, store.UserInput{Email: "ledger-member@example.com", PasswordHash: "x", DisplayName: "Member", Role: store.RoleUser})
	if err != nil {
		t.Fatalf("创建成员: %v", err)
	}
	outsider, err := f.store.CreateUser(ctx, store.UserInput{Email: "ledger-outsider@example.com", PasswordHash: "x", DisplayName: "Outsider", Role: store.RoleUser})
	if err != nil {
		t.Fatalf("创建外部用户: %v", err)
	}
	team, err := f.store.CreateTeam(ctx, store.CreateTeamInput{Name: "明细团队", QuotaCredits: "100", Unlimited: false})
	if err != nil {
		t.Fatalf("创建团队: %v", err)
	}
	if err := f.store.AddMember(ctx, team.ID, owner.ID, store.MemberRoleOwner); err != nil {
		t.Fatalf("加入 owner: %v", err)
	}
	if err := f.store.AddMember(ctx, team.ID, member.ID, store.MemberRoleMember); err != nil {
		t.Fatalf("加入成员: %v", err)
	}
	now := time.Now().UTC()
	settleUsageForTest(t, f, team.ID, owner.ID, "deepseek-flash", 1_000_000, now.Add(-time.Minute))
	settleUsageForTest(t, f, team.ID, member.ID, "deepseek-flash", 2_000_000, now.Add(-2*time.Minute))
	query := url.Values{"targetType": {"team"}, "teamId": {team.ID}, "range": {"30d"}}

	ownerRec := requestCreditEntries(t, f, createDeviceTokenForTest(t, f, owner.ID), query)
	ownerData, ownerRows := entriesOf(t, ownerRec)
	if len(ownerRows) != 3 || ownerData["summary"].(map[string]any)["totalCreditsOut"] != "3.000000" || ownerData["summary"].(map[string]any)["netCredits"] != "97.000000" {
		t.Fatalf("owner 应看到全团队: %s", ownerRec.Body.String())
	}
	memberRec := requestCreditEntries(t, f, createDeviceTokenForTest(t, f, member.ID), query)
	memberData, memberRows := entriesOf(t, memberRec)
	if len(memberRows) != 2 || memberData["summary"].(map[string]any)["totalCreditsOut"] != "2.000000" || memberData["summary"].(map[string]any)["totalCreditsIn"] != "100.000000" {
		t.Fatalf("普通成员可见团队入账与本人消费: %s", memberRec.Body.String())
	}
	if outsiderRec := requestCreditEntries(t, f, createDeviceTokenForTest(t, f, outsider.ID), query); outsiderRec.Code != 403 {
		t.Fatalf("非成员应 403: %d %s", outsiderRec.Code, outsiderRec.Body.String())
	}
	if missing := requestCreditEntries(t, f, createDeviceTokenForTest(t, f, owner.ID), url.Values{"targetType": {"team"}}); missing.Code != 400 {
		t.Fatalf("团队明细缺 teamId 应 400: %d", missing.Code)
	}
	memberToken := createDeviceTokenForTest(t, f, member.ID)
	if removed, err := f.store.RemoveMember(ctx, team.ID, member.ID); err != nil || !removed {
		t.Fatalf("移除成员: %v", err)
	}
	if revoked := requestCreditEntries(t, f, memberToken, query); revoked.Code != 403 {
		t.Fatalf("撤权后必须立即拒绝: %d %s", revoked.Code, revoked.Body.String())
	}
}

func TestCreditLedgerCursorPreservesPrecisionAndRejectsMalformed(t *testing.T) {
	at := time.Date(2026, 11, 2, 12, 0, 0, 123456000, time.UTC)
	id := uuid.NewString()
	raw := encodeCreditLedgerCursor(at, "quota", id)
	created, source, decodedID, valid := decodeCreditLedgerCursor(raw)
	if !valid || !created.Equal(at) || source != "quota" || decodedID != id {
		t.Fatalf("游标必须无损保留微秒和来源: %v %s %s %v", created, source, decodedID, valid)
	}
	for _, malformed := range []string{"bad", base64.RawURLEncoding.EncodeToString([]byte("v1~2026-11-02T12:00:00Z~unknown~" + id)), base64.RawURLEncoding.EncodeToString([]byte("v1~bad~usage~" + id)), base64.RawURLEncoding.EncodeToString([]byte("v1~2026-11-02T12:00:00Z~usage~bad"))} {
		if _, _, _, valid := decodeCreditLedgerCursor(malformed); valid {
			t.Fatalf("不能接受非法游标 %s", malformed)
		}
	}
	upperID := "ABCDEF00-1234-4000-8000-000000000001"
	if _, _, _, valid := decodeCreditLedgerCursor(encodeCreditLedgerCursor(at, "usage", upperID)); valid {
		t.Fatal("非规范 UUID 游标应在 API 拒绝，不能触发 Store 500")
	}
}

func TestClientCreditEntriesValidatesEmptyLedgerQueries(t *testing.T) {
	f := newAPIEmptyFixture(t)
	user, err := f.store.CreateUser(context.Background(), store.UserInput{Email: "ledger-empty@example.com", PasswordHash: "x", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	token := createDeviceTokenForTest(t, f, user.ID)
	for _, query := range []url.Values{{"targetType": {"personal"}, "teamId": {uuid.NewString()}}, {"targetType": {"personal"}, "teamId": {""}}, {"cursor": {"bad"}}, {"cursor": {""}}, {"limit": {"101"}}, {"limit": {"0"}}, {"targetType": {"all"}}} {
		if rec := requestCreditEntries(t, f, token, query); rec.Code != 400 {
			t.Fatalf("空账本也必须验证查询 %v: %d %s", query, rec.Code, rec.Body.String())
		}
	}
	if rec := requestCreditEntries(t, f, token, url.Values{"limit": {"1", "2"}}); rec.Code != 400 {
		t.Fatalf("重复参数必须被拒绝: %d %s", rec.Code, rec.Body.String())
	}
	if rec := requestCreditEntries(t, f, token, url.Values{"unexpected": {"x"}}); rec.Code != 400 {
		t.Fatalf("未知参数必须被拒绝: %d %s", rec.Code, rec.Body.String())
	}
	rec := requestCreditEntries(t, f, token, nil)
	data, rows := entriesOf(t, rec)
	summary := data["summary"].(map[string]any)
	if len(rows) != 0 || data["nextCursor"] != nil || summary["totalCreditsIn"] != "0.000000" || summary["totalCreditsOut"] != "0.000000" || summary["netCredits"] != "0.000000" {
		t.Fatalf("无记录返回真实空汇总: %s", rec.Body.String())
	}
}

func TestClientCreditEntriesAllCommittedChanges(t *testing.T) {
	f := newAPIEmptyFixture(t)
	ctx := context.Background()
	owner, err := f.store.CreateUser(ctx, store.UserInput{Email: "ledger-all@example.com", PasswordHash: "x", DisplayName: "Ledger Owner", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	root, err := f.store.CreateUser(ctx, store.UserInput{Email: "ledger-reviewer@example.com", PasswordHash: "x", DisplayName: "Ledger Reviewer", Role: store.RoleRoot})
	if err != nil {
		t.Fatal(err)
	}
	team, err := f.store.CreateTeam(ctx, store.CreateTeamInput{Name: "全部点数变动", QuotaCredits: "10"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddMember(ctx, team.ID, owner.ID, store.MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	amount := int64(2_500_000)
	audit := &store.InsertAuditLogInput{EventID: uuid.NewString(), TeamID: team.ID, ActorUserID: owner.ID, Action: "team.credit.add", TargetType: "team", TargetID: team.ID}
	for range 2 {
		if _, err := f.store.UpdateTeamWithAccounting(ctx, team.ID, store.UpdateTeamPatch{QuotaIncreaseMicro: &amount}, "add_credit", audit); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	requestID := uuid.NewString()
	if err := f.store.CreateQuotaRequest(ctx, store.QuotaRequestRecord{ID: requestID, TeamID: team.ID, RequestedBy: owner.ID, AmountCredits: "3", Reason: "扩大项目额度", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReviewQuotaRequest(ctx, requestID, root.ID, "approve", "", now); err != nil {
		t.Fatal(err)
	}
	order := store.PaymentOrderRecord{ID: uuid.NewString(), OutTradeNo: "ledger_" + uuid.NewString(), Provider: store.PaymentProviderAlipay, ConfigurationID: uuid.NewString(), TargetType: store.PaymentTargetTeam, TargetID: team.ID, TeamID: team.ID, UserID: owner.ID, AppID: "test-ledger-app", SellerID: "test-ledger-seller", AmountFen: 100, CreditMicro: 1_000_000, Subject: "余额充值", CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute)}
	if _, err := f.store.CreatePaymentOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	paid := store.PaymentSuccessInput{OutTradeNo: order.OutTradeNo, TargetType: order.TargetType, TargetID: order.TargetID, TeamID: team.ID, AppID: order.AppID, SellerID: order.SellerID, AmountFen: order.AmountFen, TradeNo: "ledger-trade", PaidAt: now}
	for range 2 {
		if _, _, err := f.store.ClaimPaymentSuccess(ctx, paid); err != nil {
			t.Fatal(err)
		}
	}
	quota := "16"
	if _, err := f.store.UpdateTeamWithAccounting(ctx, team.ID, store.UpdateTeamPatch{QuotaCredits: &quota}, "set_quota", nil); err != nil {
		t.Fatal(err)
	}
	unlimited := true
	if _, err := f.store.UpdateTeamWithAccounting(ctx, team.ID, store.UpdateTeamPatch{Unlimited: &unlimited}, "set_quota", nil); err != nil {
		t.Fatal(err)
	}
	settleUsageForTest(t, f, team.ID, owner.ID, "deepseek-flash", 0, now)
	settleUsageForTest(t, f, team.ID, owner.ID, "deepseek-flash", 2_000_001, now)
	token := createDeviceTokenForTest(t, f, owner.ID)
	query := url.Values{"targetType": {"team"}, "teamId": {team.ID}, "limit": {"2"}}
	seen := map[string]bool{}
	reasons := map[string]map[string]any{}
	for page := 0; ; page++ {
		if page > 5 {
			t.Fatal("分页没有终止")
		}
		rec := requestCreditEntries(t, f, token, query)
		if rec.Code != 200 {
			t.Fatalf("全部变动接口 %d %s", rec.Code, rec.Body.String())
		}
		data, rows := entriesOf(t, rec)
		summary := data["summary"].(map[string]any)
		if summary["totalCreditsIn"] != "16.500000" || summary["totalCreditsOut"] != "2.500001" || summary["netCredits"] != "13.999999" || summary["entryCount"] != float64(6) {
			t.Fatalf("收支应对上真实余额且不随分页改变: %s", rec.Body.String())
		}
		for _, row := range rows {
			id := row["id"].(string)
			if seen[id] {
				t.Fatalf("分页重复 %s", id)
			}
			seen[id] = true
			reasons[row["changeReason"].(string)] = row
			for _, forbidden := range []string{"actorEmail", "outTradeNo", "tradeNo", "providerId", "upstreamModel", "priceSnapshot", "detail"} {
				if _, leaked := row[forbidden]; leaked {
					t.Fatalf("不得泄露 %s", forbidden)
				}
			}
			if row["actorEmail"] != nil {
				t.Fatalf("不得包含邮箱: %v", row)
			}
			if row["changeReason"] == "usage" && row["actorName"] != "Ledger Owner" {
				t.Fatalf("消费核对身份应只呈现昵称: %v", row)
			}
		}
		if data["nextCursor"] == nil {
			break
		}
		query.Set("cursor", data["nextCursor"].(string))
	}
	for _, reason := range []string{"initial_grant", "manual_add", "quota_request_approved", "alipay_recharge", "quota_adjustment", "usage"} {
		if reasons[reason] == nil {
			t.Fatalf("漏记原因 %s: %v", reason, reasons)
		}
	}
	if reasons["quota_adjustment"]["direction"] != "debit" || reasons["quota_adjustment"]["credits"] != "0.500000" || reasons["alipay_recharge"]["kind"] != "topup" {
		t.Fatalf("减额和充值的类型/方向不符: %v", reasons)
	}
	updated, err := f.store.GetTeam(ctx, team.ID)
	if err != nil || updated.QuotaCredits != "16.000000" || updated.UsedCredits != "2.000001" {
		t.Fatalf("真实账户应与流水一致: %+v %v", updated, err)
	}
}

func TestFormatCreditLedgerMicroDoesNotOverflow(t *testing.T) {
	for raw, expected := range map[string]string{"0": "0.000000", "-1": "-0.000001", "9223372036854775808": "9223372036854.775808", "-9223372036854775808": "-9223372036854.775808"} {
		got, err := formatCreditLedgerMicro(raw)
		if err != nil || got != expected {
			t.Fatalf("整数定点格式化 %s: %s %v", raw, got, err)
		}
	}
	if _, err := formatCreditLedgerMicro("1.5"); err == nil {
		t.Fatal("不能把非整数 micro 舍入为金额")
	}
}

func TestClientCreditEntriesIncludesCommittedFailedCharges(t *testing.T) {
	f := newAPIEmptyFixture(t)
	ctx := context.Background()
	user, err := f.store.CreateUser(ctx, store.UserInput{Email: "ledger-failed@example.com", PasswordHash: "x", DisplayName: "Ledger", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	team, err := f.store.CreateTeam(ctx, store.CreateTeamInput{Name: "已扣费的失败请求", QuotaCredits: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddMember(ctx, team.ID, user.ID, store.MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	// 旧结算路径封顶后仍会实际扣正数并标记 error，流水必须依实际金额而非请求状态判断。
	for i := range 2 {
		err := f.store.SettleUsage(ctx, store.UsageRecord{ID: uuid.NewString(), TeamID: team.ID, UserID: user.ID, Model: "deepseek-flash", RequestID: uuid.NewString(), CreditsMicro: 2_000_000, Status: store.UsageStatusOk, CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second)}, 0)
		if !errors.Is(err, store.ErrCreditChargeCapped) {
			t.Fatalf("预期真实封顶结算: %v", err)
		}
	}
	rec := requestCreditEntries(t, f, createDeviceTokenForTest(t, f, user.ID), url.Values{"targetType": {"team"}, "teamId": {team.ID}})
	if rec.Code != 200 {
		t.Fatalf("明细接口: %d %s", rec.Code, rec.Body.String())
	}
	data, rows := entriesOf(t, rec)
	summary := data["summary"].(map[string]any)
	if len(rows) != 2 || summary["entryCount"] != float64(2) || summary["totalCreditsIn"] != "1.000000" || summary["totalCreditsOut"] != "1.010000" || summary["netCredits"] != "-0.010000" {
		t.Fatalf("应包含已扣费失败、排除零费用失败且汇总对齐余额: %s", rec.Body.String())
	}
	if rows[0]["changeReason"] != "usage" || rows[0]["direction"] != "debit" || rows[0]["credits"] != "1.010000" || rows[0]["status"] != "settled" {
		t.Fatalf("请求失败仍须投影为已结算的实际扣费: %v", rows[0])
	}
}

func TestClientCreditEntriesDateRangeUsesShanghaiBoundaryAndNowUpperBound(t *testing.T) {
	f := newAPIEmptyFixture(t)
	ctx := context.Background()
	user, err := f.store.CreateUser(ctx, store.UserInput{Email: "ledger-date@example.com", PasswordHash: "x", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	personal, err := f.store.EnsurePersonalAccount(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	quota := "10"
	if _, err := f.store.UpdateTeam(ctx, personal.ID, store.UpdateTeamPatch{QuotaCredits: &quota}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	f.deps.Now = func() time.Time { return now }
	local := now.In(creditLedgerZone)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, creditLedgerZone).AddDate(0, 0, -6).UTC()
	settleUsageForTest(t, f, personal.ID, user.ID, "deepseek-flash", 1_000_000, start)
	settleUsageForTest(t, f, personal.ID, user.ID, "deepseek-flash", 2_000_000, now.Add(-time.Hour))
	settleUsageForTest(t, f, personal.ID, user.ID, "deepseek-flash", 3_000_000, now.Add(time.Hour))
	rec := requestCreditEntries(t, f, createDeviceTokenForTest(t, f, user.ID), url.Values{"targetType": {"personal"}, "range": {"7d"}})
	data, rows := entriesOf(t, rec)
	if rec.Code != 200 || len(rows) != 3 || data["summary"].(map[string]any)["totalCreditsOut"] != "3.000000" || data["summary"].(map[string]any)["totalCreditsIn"] != "10.000000" {
		t.Fatalf("范围应含上海起点及 now 前记录，不含未来记录: %d %s", rec.Code, rec.Body.String())
	}
}
