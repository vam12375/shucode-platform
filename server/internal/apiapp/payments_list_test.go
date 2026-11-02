package apiapp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestPaymentAPIOrderListPaginationAndScope(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	// 同一个毫秒内的订单需要保留完整时间精度和 ID，翻页才不会遗漏。
	created := time.Date(2026, 11, 2, 0, 0, 0, 123456000, time.UTC)
	insert := func(userID, teamID string, target store.PaymentTargetType, targetID string, n int) {
		t.Helper()
		_, err := f.store.CreatePaymentOrder(ctx, store.PaymentOrderRecord{ID: uuid.NewString(), ConfigurationID: uuid.NewString(), OutTradeNo: fmt.Sprintf("list-%d", n), Provider: store.PaymentProviderAlipay, TargetType: target, TargetID: targetID, TeamID: teamID, UserID: userID, AppID: "2026000000000001", SellerID: "2088000000000001", AmountFen: 100, CreditMicro: 1000000, Subject: "test recharge", CreatedAt: created, ExpiresAt: created.Add(30 * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 53; i++ {
		userID := f.admin.ID
		if i%2 == 0 {
			userID = f.root.ID
		}
		insert(userID, f.team.ID, store.PaymentTargetTeam, f.team.ID, i)
	}
	insert(f.other.ID, f.otherTeam.ID, store.PaymentTargetTeam, f.otherTeam.ID, 53)
	for i, user := range []*store.UserRecord{f.member, f.root} {
		account, err := f.store.EnsurePersonalAccount(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		insert(user.ID, account.ID, store.PaymentTargetPersonal, user.ID, 54+i)
	}
	path := "/console/api/payments/orders?targetType=team&teamId=" + f.team.ID
	first := dataOf(t, f.do("GET", path, f.adminCookie, ""))
	rows := first["orders"].([]any)
	cursor, ok := first["nextCursor"].(string)
	if len(rows) != 50 || !ok || cursor == "" {
		t.Fatalf("first page: %v", first)
	}
	second := dataOf(t, f.do("GET", path+"&cursor="+url.QueryEscape(cursor), f.adminCookie, ""))
	lastRows := second["orders"].([]any)
	if len(lastRows) != 3 || second["nextCursor"] != nil {
		t.Fatalf("last page: %v", second)
	}
	seen := map[string]bool{}
	rootOrders := 0
	for _, item := range append(rows, lastRows...) {
		row := item.(map[string]any)
		id := row["id"].(string)
		if seen[id] || row["teamId"] != f.team.ID {
			t.Fatal("pagination duplicated a row or leaked another account")
		}
		seen[id] = true
		if row["userId"] == f.root.ID {
			rootOrders++
		}
	}
	if rootOrders != 27 {
		t.Fatal("team manager cannot see another operator's team orders")
	}
	for _, item := range []struct {
		path, cookie string
		status       int
	}{
		{path, f.memberCookie, 403},
		{"/console/api/payments/orders?targetType=team&teamId=" + f.otherTeam.ID, f.adminCookie, 403},
		{"/console/api/payments/orders?targetType=team", f.adminCookie, 400},
		{"/console/api/payments/orders?targetType=invalid", f.rootCookie, 400},
		{"/console/api/payments/orders?targetType=personal&teamId=" + f.team.ID, f.rootCookie, 400},
		{path + "&cursor=bad", f.adminCookie, 400},
		{path + "&cursor=2026-99-02T00:00:00Z~" + uuid.NewString(), f.adminCookie, 400},
	} {
		if rec := f.do("GET", item.path, item.cookie, ""); rec.Code != item.status {
			t.Fatalf("%s: status %d: %s", item.path, rec.Code, rec.Body.String())
		}
	}
	for _, item := range []struct{ path, cookie, userID string }{
		{"/console/api/payments/orders", f.memberCookie, f.member.ID},
		{"/console/api/payments/orders?targetType=personal", f.rootCookie, f.root.ID},
	} {
		data := dataOf(t, f.do("GET", item.path, item.cookie, ""))
		list := data["orders"].([]any)
		if len(list) != 1 || list[0].(map[string]any)["userId"] != item.userID {
			t.Fatalf("personal scope: %v", data)
		}
	}
	all := dataOf(t, f.do("GET", "/console/api/payments/orders", f.rootCookie, ""))
	allCursor := all["nextCursor"].(string)
	allLast := dataOf(t, f.do("GET", "/console/api/payments/orders?cursor="+url.QueryEscape(allCursor), f.rootCookie, ""))
	if len(all["orders"].([]any)) != 50 || len(allLast["orders"].([]any)) != 6 {
		t.Fatal("root unfiltered view must include all accounts")
	}
	// 保留一位有效管理员后再撤销原管理员，避免最后管理员保护拦截权限失效场景。
	if err := f.store.AddMember(ctx, f.team.ID, f.member.ID, store.MemberRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RemoveMember(ctx, f.team.ID, f.admin.ID); err != nil {
		t.Fatal(err)
	}
	if rec := f.do("GET", path+"&cursor="+url.QueryEscape(cursor), f.adminCookie, ""); rec.Code != 403 {
		t.Fatalf("revoked manager may not reuse cursor: %d", rec.Code)
	}
}

type paymentListFailureStore struct {
	store.PlatformStore
	calls chan struct{}
}

func (s paymentListFailureStore) ListPaymentOrders(context.Context, store.PaymentOrderFilter) ([]store.PaymentOrderRecord, error) {
	s.calls <- struct{}{}
	return nil, errors.New("test storage unavailable")
}

func TestPaymentReconcilerRetriesWithoutLogger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make(chan struct{}, 8)
	StartPaymentReconciler(ctx, &Deps{Store: paymentListFailureStore{calls: calls}}, time.Millisecond)
	for i := 0; i < 2; i++ {
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatal("payment reconciler did not retry after storage failure")
		}
	}
}
