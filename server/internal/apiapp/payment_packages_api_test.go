package apiapp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

type packageRow struct {
	SKU          string                     `json:"sku"`
	Name         string                     `json:"name"`
	Description  string                     `json:"description"`
	TargetType   store.PaymentTargetType    `json:"targetType"`
	AmountYuan   string                     `json:"amountYuan"`
	CreditAmount string                     `json:"creditAmount"`
	Status       store.PaymentPackageStatus `json:"status"`
	Recommended  bool                       `json:"recommended"`
	SortOrder    int                        `json:"sortOrder"`
}

func packageRows(t *testing.T, data map[string]any) []packageRow {
	t.Helper()
	raw, err := json.Marshal(data["packages"])
	if err != nil {
		t.Fatal(err)
	}
	var packages []packageRow
	if err = json.Unmarshal(raw, &packages); err != nil {
		t.Fatal(err)
	}
	return packages
}

func packageRevision(t *testing.T, data map[string]any) string {
	t.Helper()
	revision, ok := data["revision"].(string)
	if !ok || revision == "" {
		t.Fatalf("missing package revision: %v", data)
	}
	return revision
}

func savePackages(t *testing.T, f *apiFixture, cookie, expected string, packages []packageRow) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"expectedRevision": expected, "packages": packages})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.do("PUT", "/console/api/payments/packages", cookie, string(body))
	if rec.Code != 200 {
		t.Fatalf("save packages: %d %s", rec.Code, rec.Body.String())
	}
	return dataOf(t, rec)
}

func publishTestPackages(t *testing.T, f *apiFixture, cookie string) map[string]any {
	t.Helper()
	current := dataOf(t, f.do("GET", "/console/api/payments/packages", cookie, ""))
	packages := packageRows(t, current)
	for i := range packages {
		if packages[i].SKU == "personal-standard" {
			packages[i].Status = store.PaymentPackagePublished
		}
	}
	return savePackages(t, f, cookie, packageRevision(t, current), packages)
}

func clientPaymentRequest(f *apiFixture, method, path, token, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func TestPaymentPackagesCatalogCASAndClientFrozenOrders(t *testing.T) {
	f := newAPIFixture(t)
	priv, pub, _ := paymentKeys(t)
	rootCookie := configurePayments(t, f, priv, pub)
	admin := dataOf(t, f.do("GET", "/console/api/payments/packages", rootCookie, ""))
	if len(packageRows(t, admin)) != 5 {
		t.Fatalf("initial draft packages: %v", admin)
	}
	for _, plan := range packageRows(t, admin) {
		if plan.Status != store.PaymentPackageDraft {
			t.Fatalf("initial package must be draft: %+v", plan)
		}
	}

	published := publishTestPackages(t, f, rootCookie)
	revision := packageRevision(t, published)
	rootUser, err := f.store.FindUserByEmail(context.Background(), "admin@mendao.chat")
	if err != nil {
		t.Fatal(err)
	}
	rootToken := createDeviceTokenForTest(t, f, rootUser.ID)
	clientPackages := clientPaymentRequest(f, "GET", "/api/v1/client/payments/packages", rootToken, "")
	if clientPackages.Code != 200 {
		t.Fatalf("client packages: %d %s", clientPackages.Code, clientPackages.Body.String())
	}
	clientList := dataOf(t, clientPackages)
	if len(packageRows(t, clientList)) != 1 || packageRows(t, clientList)[0].SKU != "personal-standard" {
		t.Fatalf("client must see only published: %v", clientList)
	}
	if unauthorized := clientPaymentRequest(f, "GET", "/api/v1/client/payments/packages", "", ""); unauthorized.Code != 401 {
		t.Fatalf("device credential required: %d", unauthorized.Code)
	}
	if rootBearerConsole := clientPaymentRequest(f, "GET", "/console/api/payments/packages", rootToken, ""); rootBearerConsole.Code != 403 {
		t.Fatalf("device root must not get console privileges: %d", rootBearerConsole.Code)
	}

	account := dataOf(t, clientPaymentRequest(f, "GET", "/api/v1/client/payments/account", rootToken, ""))
	if account["mode"] != "team" || account["activeTeamResolution"] != "unique" {
		t.Fatalf("account mode and team resolution: %v", account)
	}
	personal := account["personal"].(map[string]any)
	if personal["balanceCredits"] != "0.000000" {
		t.Fatalf("personal account projection: %v", personal)
	}
	team := account["team"].(map[string]any)
	_ = team["id"].(string)

	// Root's device credential receives no platform-wide team bypass.
	otherTeam, err := f.store.CreateTeam(context.Background(), store.CreateTeamInput{Name: "not-a-root-device-team", QuotaCredits: "0"})
	if err != nil {
		t.Fatal(err)
	}
	teamBody, _ := json.Marshal(map[string]any{"requestId": uuid.NewString(), "targetType": "team", "teamId": otherTeam.ID, "purchaseKind": "custom", "amountYuan": "1.00"})
	if rec := clientPaymentRequest(f, "POST", "/api/v1/client/payments/orders", rootToken, string(teamBody)); rec.Code != 403 {
		t.Fatalf("root device cross-team: %d %s", rec.Code, rec.Body.String())
	}

	requestID := uuid.NewString()
	planBody := func(id, version, name string) string {
		raw, _ := json.Marshal(map[string]any{"requestId": id, "targetType": "personal", "purchaseKind": "plan", "sku": "personal-standard", "version": version, "mobile": false})
		return string(raw)
	}
	created := clientPaymentRequest(f, "POST", "/api/v1/client/payments/orders", rootToken, planBody(requestID, revision, ""))
	if created.Code != 200 {
		t.Fatalf("plan order: %d %s", created.Code, created.Body.String())
	}
	createdOrder := dataOf(t, created)["order"].(map[string]any)
	if createdOrder["purchaseKind"] != "plan" || createdOrder["amountYuan"] != "99.00" || createdOrder["creditAmount"] != "99.000000" {
		t.Fatalf("plan order amounts: %v", createdOrder)
	}
	snapshot := createdOrder["package"].(map[string]any)
	if snapshot["name"] != "常用包" || snapshot["version"] != revision {
		t.Fatalf("plan order snapshot: %v", snapshot)
	}
	outTradeNo := createdOrder["outTradeNo"].(string)

	// Repricing publishes a new catalog, but an accepted requestId retains its frozen order.
	current := dataOf(t, f.do("GET", "/console/api/payments/packages", rootCookie, ""))
	plans := packageRows(t, current)
	for i := range plans {
		if plans[i].SKU == "personal-standard" {
			plans[i].Name, plans[i].AmountYuan, plans[i].CreditAmount = "常用包升级", "109.00", "109.000000"
		}
	}
	newCatalog := savePackages(t, f, rootCookie, packageRevision(t, current), plans)
	if packageRevision(t, newCatalog) == revision {
		t.Fatal("published revision did not advance")
	}
	replay := clientPaymentRequest(f, "POST", "/api/v1/client/payments/orders", rootToken, planBody(requestID, revision, ""))
	if replay.Code != 200 {
		t.Fatalf("idempotent old order: %d %s", replay.Code, replay.Body.String())
	}
	replayOrder := dataOf(t, replay)["order"].(map[string]any)
	if replayOrder["outTradeNo"] != outTradeNo || replayOrder["amountYuan"] != "99.00" || replayOrder["package"].(map[string]any)["name"] != "常用包" {
		t.Fatalf("old order lost snapshot: %v", replayOrder)
	}
	stale := clientPaymentRequest(f, "POST", "/api/v1/client/payments/orders", rootToken, planBody(uuid.NewString(), revision, ""))
	if stale.Code != 409 {
		t.Fatalf("stale catalog: %d %s", stale.Code, stale.Body.String())
	}
	staleDetails := jsonRecorderBody(t, stale)["details"].(map[string]any)
	if staleDetails["currentRevision"] != packageRevision(t, newCatalog) || staleDetails["package"].(map[string]any)["amountYuan"] != "109.00" {
		t.Fatalf("stale quote details: %v", staleDetails)
	}

	customBody := map[string]any{"targetType": "personal", "purchaseKind": "custom", "amountYuan": "1.00"}
	customID := uuid.NewString()
	customBody["requestId"] = customID
	customRaw, _ := json.Marshal(customBody)
	custom := clientPaymentRequest(f, "POST", "/api/v1/client/payments/orders", rootToken, string(customRaw))
	if custom.Code != 200 {
		t.Fatalf("custom order: %d %s", custom.Code, custom.Body.String())
	}
	customOrder := dataOf(t, custom)["order"].(map[string]any)
	if customOrder["purchaseKind"] != "custom" || customOrder["creditAmount"] != "1.000000" {
		t.Fatalf("custom credit amount: %v", customOrder)
	}
	customBody["amountYuan"] = "2.00"
	customRaw, _ = json.Marshal(customBody)
	if changed := clientPaymentRequest(f, "POST", "/api/v1/client/payments/orders", rootToken, string(customRaw)); changed.Code != 409 {
		t.Fatalf("custom request id amount mutation: %d", changed.Code)
	}

	// Normal same-origin cookie requests remain valid; CAS rejects an outdated revision.
	if normal := f.do("GET", "/console/api/payments/packages", rootCookie, ""); normal.Code != 200 {
		t.Fatalf("same-origin console cookie: %d", normal.Code)
	}
	conflict := f.do("PUT", "/console/api/payments/packages", rootCookie, `{"expectedRevision":"`+revision+`","packages":[]}`)
	if conflict.Code != 409 {
		t.Fatalf("package CAS conflict: %d %s", conflict.Code, conflict.Body.String())
	}
}

func TestPaymentConsoleRejectsMixedBearerCookieAndPublicReturn(t *testing.T) {
	f := newAPIFixture(t)
	priv, pub, _ := paymentKeys(t)
	rootCookie := configurePayments(t, f, priv, pub)
	orderRec := f.do("POST", "/console/api/payments/orders", rootCookie, `{"requestId":"`+uuid.NewString()+`","targetType":"personal","amountYuan":"1.00"}`)
	if orderRec.Code != 200 {
		t.Fatalf("create order: %d %s", orderRec.Code, orderRec.Body.String())
	}
	order := dataOf(t, orderRec)["order"].(map[string]any)
	paths := []struct{ method, path, body string }{
		{"PUT", "/console/api/payments/config", `{"enabled":true,"appId":"2026000000000001","sellerId":"2088000000000001","gateway":"production"}`},
		{"PUT", "/console/api/payments/packages", `{"expectedRevision":"00000000-0000-4000-8000-000000003035","packages":[]}`},
		{"POST", "/console/api/payments/orders", `{"requestId":"` + uuid.NewString() + `","targetType":"personal","amountYuan":"1.00"}`},
		{"POST", "/console/api/payments/orders/" + order["outTradeNo"].(string) + "/reconcile", "{}"},
	}
	for _, item := range paths {
		rec := f.doForge(item.method, item.path, rootCookie, item.body, map[string]string{"Authorization": "Bearer forged-device-token"})
		if rec.Code != 403 {
			t.Errorf("mixed cookie/bearer %s %s: %d %s", item.method, item.path, rec.Code, rec.Body.String())
		}
	}
	account := f.doForge("GET", "/console/api/payments/account", rootCookie, "", map[string]string{"X-API-Key": "forged"})
	if account.Code != 403 {
		t.Fatalf("account mixed credentials without Origin: %d", account.Code)
	}
	ret := f.do("GET", "/api/v1/payments/alipay/return", "", "")
	if ret.Code != 200 || !strings.Contains(ret.Body.String(), "请返回应用核对") {
		t.Fatalf("anonymous return page: %d %s", ret.Code, ret.Body.String())
	}
}
