package apiapp

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestClientPaymentOrderResponses(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewMemoryStore(ctx, store.MemoryOptions{DemoSeed: true})
	if err != nil {
		t.Fatal(err)
	}
	box, err := crypto.NewSecretBox(testSecretKey())
	if err != nil {
		t.Fatal(err)
	}
	deps := &Deps{Store: st, Env: &Env{MemoryDB: true}, SecretBox: box, Now: time.Now, Logger: slog.Default()}
	f := &apiFixture{t: t, deps: deps, store: st, engine: New(deps)}
	user, err := st.FindRootUser(ctx)
	if err != nil || user == nil {
		t.Fatalf("加载用户失败: %v", err)
	}
	account, err := f.store.EnsurePersonalAccount(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	token := createDeviceTokenForTest(t, f, user.ID)
	start := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	order, err := f.store.CreatePaymentOrder(ctx, store.PaymentOrderRecord{
		ID: "00000000-0000-4000-8000-000000000001", OutTradeNo: "contractOrder001",
		Provider: store.PaymentProviderAlipay, ConfigurationID: uuid.NewString(),
		TargetType: store.PaymentTargetPersonal, TargetID: user.ID, UserID: user.ID, TeamID: account.ID,
		AppID: "fixtureApp", SellerID: "fixtureSeller", AmountFen: 100, CreditMicro: 1_000_000,
		Subject: "充值契约样本", CreatedAt: start, ExpiresAt: start.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../contracts/testdata/payment-order-responses.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]map[string]any
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	check := func(method, suffix, status string) {
		t.Helper()
		want := fixtures[status]
		data := want["data"].(map[string]any)
		data["userId"], data["targetId"], data["teamId"] = user.ID, user.ID, account.ID
		rec := clientPaymentRequest(f, method, "/api/v1/client/payments/orders/"+order.OutTradeNo+suffix, token, "")
		if rec.Code != 200 || !reflect.DeepEqual(jsonRecorderBody(t, rec), want) {
			t.Fatalf("%s %s (%s): %d %s", method, suffix, status, rec.Code, rec.Body.String())
		}
	}
	check("GET", "", "pending")
	_, _, err = f.store.ClaimPaymentSuccess(ctx, store.PaymentSuccessInput{
		OutTradeNo: order.OutTradeNo, TargetType: order.TargetType, TargetID: order.TargetID, TeamID: order.TeamID,
		AppID: order.AppID, SellerID: order.SellerID, AmountFen: 100, TradeNo: "fixtureTrade001", PaidAt: start.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	check("GET", "", "paid")
	check("POST", "/reconcile", "paid")
	check("POST", "/reconcile", "paid")
	account, err = f.store.GetPersonalAccount(ctx, user.ID)
	if err != nil || account == nil || account.QuotaCredits != "1.000000" {
		t.Fatalf("重复核对改变了余额: %+v %v", account, err)
	}
}
