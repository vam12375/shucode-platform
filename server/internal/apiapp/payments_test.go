package apiapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func paymentKeys(t *testing.T) (string, string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	priv := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pub := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
	return string(priv), string(pub), key
}
func signedNotify(key *rsa.PrivateKey, values url.Values) url.Values {
	out := url.Values{}
	for k, v := range values {
		out[k] = append([]string(nil), v...)
	}
	keys := make([]string, 0, len(out))
	for k, v := range out {
		if k == "sign" || k == "sign_type" || len(v) == 0 || v[0] == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+out.Get(k))
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "&")))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		panic(err)
	}
	out.Set("sign", base64.StdEncoding.EncodeToString(sig))
	out.Set("sign_type", "RSA2")
	return out
}
func configurePayments(t *testing.T, f *apiFixture, priv, pub string) string {
	t.Helper()
	cookie := f.loginAdmin()
	raw, _ := json.Marshal(map[string]any{"enabled": true, "appId": "2026000000000001", "sellerId": "2088000000000001", "gateway": "production", "privateKey": priv, "publicKey": pub})
	body := string(raw)
	rec := f.do("PUT", "/console/api/payments/config", cookie, body)
	if rec.Code != 200 {
		t.Fatalf("configure payment: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "PRIVATE KEY") || strings.Contains(rec.Body.String(), "PUBLIC KEY") {
		t.Fatal("payment keys echoed")
	}
	return cookie
}
func TestPaymentAPILinkAndReplay(t *testing.T) {
	f := newAPIFixture(t)
	priv, pub, key := paymentKeys(t)
	rootCookie := configurePayments(t, f, priv, pub)
	requestID := uuid.NewString()
	create := func(amount string, test bool) *httptest.ResponseRecorder {
		return f.do("POST", "/console/api/payments/orders", rootCookie, `{"requestId":"`+requestID+`","targetType":"personal","amountYuan":"`+amount+`","testPayment":`+fmt.Sprint(test)+`}`)
	}
	if rec := create("0.01", true); rec.Code != 200 {
		t.Fatalf("root 0.01 test: %d %s", rec.Code, rec.Body.String())
	}
	rec := create("0.01", true)
	if rec.Code != 200 {
		t.Fatalf("idempotent order: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			Order struct {
				OutTradeNo string `json:"outTradeNo"`
			} `json:"order"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	out := payload.Data.Order.OutTradeNo
	if out == "" {
		t.Fatal("missing out trade no")
	}
	values := url.Values{"out_trade_no": []string{out}, "trade_no": []string{"202611020001"}, "app_id": []string{"2026000000000001"}, "seller_id": []string{"2088000000000001"}, "total_amount": []string{"0.01"}, "trade_status": []string{"TRADE_SUCCESS"}, "gmt_payment": []string{"2026-11-02 12:00:00"}}
	signed := signedNotify(key, values)
	if rec = f.form("/api/v1/payments/alipay/notify", rootCookie, mapString(signed)); rec.Code != 200 || rec.Body.String() != "success" {
		t.Fatalf("notify: %d %s", rec.Code, rec.Body.String())
	}
	if rec = f.form("/api/v1/payments/alipay/notify", rootCookie, mapString(signed)); rec.Code != 200 || rec.Body.String() != "success" {
		t.Fatalf("notify replay: %d %s", rec.Code, rec.Body.String())
	}
	order, _ := f.store.GetPaymentOrder(context.Background(), out)
	if order.Status != store.PaymentPaid {
		t.Fatalf("order not paid: %+v", order)
	}
	team, _ := f.store.GetPersonalAccount(context.Background(), order.UserID)
	if team == nil || team.QuotaCredits != "0.010000" {
		t.Fatalf("personal credit: %+v", team)
	}
	bad := signedNotify(key, values)
	bad.Set("total_amount", "1.00")
	if rec = f.form("/api/v1/payments/alipay/notify", rootCookie, mapString(bad)); rec.Code == 200 && rec.Body.String() == "success" {
		t.Fatal("wrong amount accepted")
	}
	bad = values
	bad.Set("total_amount", "1.00")
	bad = signedNotify(key, bad)
	if rec = f.form("/api/v1/payments/alipay/notify", rootCookie, mapString(bad)); rec.Code == 200 && rec.Body.String() == "success" {
		t.Fatal("signed wrong amount accepted")
	}
	bad = signedNotify(key, values)
	bad.Set("app_id", "2026000000000002")
	if rec = f.form("/api/v1/payments/alipay/notify", rootCookie, mapString(bad)); rec.Code == 200 && rec.Body.String() == "success" {
		t.Fatal("wrong app accepted")
	}
	bad = values
	bad.Set("app_id", "2026000000000002")
	bad = signedNotify(key, bad)
	if rec = f.form("/api/v1/payments/alipay/notify", rootCookie, mapString(bad)); rec.Code == 200 && rec.Body.String() == "success" {
		t.Fatal("signed wrong app accepted")
	}
	bad = values
	bad.Set("seller_id", "2088000000000002")
	bad = signedNotify(key, bad)
	if rec = f.form("/api/v1/payments/alipay/notify", rootCookie, mapString(bad)); rec.Code == 200 && rec.Body.String() == "success" {
		t.Fatal("signed wrong seller accepted")
	}
	bad = signedNotify(key, values)
	bad.Set("sign", "invalid")
	if rec = f.form("/api/v1/payments/alipay/notify", rootCookie, mapString(bad)); rec.Code == 200 && rec.Body.String() == "success" {
		t.Fatal("wrong signature accepted")
	}
	if rec = f.do("GET", "/console/api/payments/orders/"+out, rootCookie, ""); rec.Code != 200 {
		t.Fatalf("read order: %d %s", rec.Code, rec.Body.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func signedQueryEnvelope(t *testing.T, key *rsa.PrivateKey, raw []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(raw)
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return []byte(`{"alipay_trade_query_response":` + string(raw) + `,"sign":"` + base64.StdEncoding.EncodeToString(signature) + `"}`)
}

func TestPaymentAPIQueryAndConfigurationRotation(t *testing.T) {
	f := newAPIFixture(t)
	priv, pub, oldKey := paymentKeys(t)
	rootCookie := configurePayments(t, f, priv, pub)
	requestID := uuid.NewString()
	body := `{"requestId":"` + requestID + `","targetType":"personal","amountYuan":"1.00"}`
	rec := f.do("POST", "/console/api/payments/orders", rootCookie, body)
	if rec.Code != 200 {
		t.Fatalf("create query order: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			Order struct {
				OutTradeNo string `json:"outTradeNo"`
			} `json:"order"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	out := payload.Data.Order.OutTradeNo
	newPriv, newPub, _ := paymentKeys(t)
	rotationBody, _ := json.Marshal(map[string]any{"enabled": true, "appId": "2026000000000001", "sellerId": "2088000000000001", "gateway": "production", "privateKey": newPriv, "publicKey": newPub})
	if rec = f.do("PUT", "/console/api/payments/config", rootCookie, string(rotationBody)); rec.Code != 200 {
		t.Fatalf("rotate config: %d %s", rec.Code, rec.Body.String())
	}
	oldValues := signedNotify(oldKey, url.Values{"out_trade_no": {out}, "trade_no": {"202611020002"}, "app_id": {"2026000000000001"}, "seller_id": {"2088000000000001"}, "total_amount": {"1.00"}, "trade_status": {"TRADE_SUCCESS"}, "gmt_payment": {"2026-11-02 12:00:00"}})
	if rec = f.form("/api/v1/payments/alipay/notify", rootCookie, mapString(oldValues)); rec.Code != 200 || rec.Body.String() != "success" {
		t.Fatalf("old config notify after rotation: %d %s", rec.Code, rec.Body.String())
	}
	order, _ := f.store.GetPaymentOrder(context.Background(), out)
	if order.Status != store.PaymentPaid {
		t.Fatalf("rotated old order not paid: %+v", order)
	}

	// 查询端点必须验签原始 query response；签名错误不得入账。
	f2 := newAPIFixture(t)
	priv2, pub2, key2 := paymentKeys(t)
	root2 := configurePayments(t, f2, priv2, pub2)
	rec = f2.do("POST", "/console/api/payments/orders", root2, `{"requestId":"`+uuid.NewString()+`","targetType":"personal","amountYuan":"1.00"}`)
	if rec.Code != 200 {
		t.Fatalf("create reconcile order: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	out = payload.Data.Order.OutTradeNo
	queryRaw := []byte(`{"code":"10000","out_trade_no":"` + out + `","trade_no":"202611020003","trade_status":"TRADE_SUCCESS","total_amount":"1.00","send_pay_date":"2026-11-02 12:00:00"}`)
	f2.deps.PaymentHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := signedQueryEnvelope(t, key2, queryRaw)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})}
	if rec = f2.do("POST", "/console/api/payments/orders/"+out+"/reconcile", root2, "{}"); rec.Code != 200 {
		t.Fatalf("query reconcile: %d %s", rec.Code, rec.Body.String())
	}
	order, _ = f2.store.GetPaymentOrder(context.Background(), out)
	if order.Status != store.PaymentPaid {
		t.Fatalf("query did not pay order: %+v", order)
	}
	f2.deps.PaymentHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"alipay_trade_query_response":` + string(queryRaw) + `,"sign":"bad"}`))), Header: make(http.Header), Request: req}, nil
	})}
	rec = f2.do("POST", "/console/api/payments/orders/"+out+"/reconcile", root2, "{}")
	if rec.Code != 200 {
		t.Fatalf("paid order should be idempotent after invalid later query: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPaymentAPIPermissionsAndReturn(t *testing.T) {
	f := newTenantFixture(t)
	priv, pub, _ := paymentKeys(t)
	raw, _ := json.Marshal(map[string]any{"enabled": true, "appId": "2026000000000001", "sellerId": "2088000000000001", "gateway": "production", "privateKey": priv, "publicKey": pub})
	if rec := f.do("PUT", "/console/api/payments/config", f.rootCookie, string(raw)); rec.Code != 200 {
		t.Fatalf("config: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", "/console/api/payments/orders", f.adminCookie, `{"requestId":"`+uuid.NewString()+`","targetType":"personal","amountYuan":"0.01","testPayment":true}`); rec.Code != 403 {
		t.Fatalf("user low test payment: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", "/console/api/payments/orders", f.memberCookie, `{"requestId":"`+uuid.NewString()+`","targetType":"team","teamId":"`+f.team.ID+`","amountYuan":"1.00"}`); rec.Code != 403 {
		t.Fatalf("team member recharge: %d %s", rec.Code, rec.Body.String())
	}
	rec := f.do("POST", "/console/api/payments/orders", f.adminCookie, `{"requestId":"`+uuid.NewString()+`","targetType":"team","teamId":"`+f.team.ID+`","amountYuan":"1.00"}`)
	if rec.Code != 200 {
		t.Fatalf("team admin recharge: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			Order struct {
				OutTradeNo string `json:"outTradeNo"`
			} `json:"order"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	out := payload.Data.Order.OutTradeNo
	if rec = f.do("GET", "/console/api/payments/orders/"+out, f.memberCookie, ""); rec.Code != 403 {
		t.Fatalf("team member order read: %d %s", rec.Code, rec.Body.String())
	}
	if rec = f.do("GET", "/api/v1/payments/alipay/return?out_trade_no="+url.QueryEscape(out), f.adminCookie, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "请返回应用核对") {
		t.Fatalf("return endpoint: %d %s", rec.Code, rec.Body.String())
	}
	order, _ := f.store.GetPaymentOrder(context.Background(), out)
	if order.Status != store.PaymentPending {
		t.Fatalf("return must not pay: %+v", order)
	}
	changed := f.do("POST", "/console/api/payments/orders", f.adminCookie, `{"requestId":"`+uuid.NewString()+`","targetType":"team","teamId":"`+f.team.ID+`","amountYuan":"1.00"}`)
	if changed.Code != 200 {
		t.Fatalf("second order: %d %s", changed.Code, changed.Body.String())
	}
}

func mapString(v url.Values) map[string]string {
	m := map[string]string{}
	for k := range v {
		m[k] = v.Get(k)
	}
	return m
}

func createPaymentForTest(t *testing.T, f *apiFixture, cookie, target, team, amount string, testPayment bool, requestID string) *store.PaymentOrderRecord {
	t.Helper()
	if requestID == "" {
		requestID = uuid.NewString()
	}
	body, _ := json.Marshal(map[string]any{"requestId": requestID, "targetType": target, "teamId": team, "amountYuan": amount, "testPayment": testPayment})
	rec := f.do("POST", "/console/api/payments/orders", cookie, string(body))
	if rec.Code != 200 {
		t.Fatalf("create %s payment: %d %s", target, rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			Order struct {
				OutTradeNo string `json:"outTradeNo"`
			} `json:"order"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	order, err := f.store.GetPaymentOrder(context.Background(), payload.Data.Order.OutTradeNo)
	if err != nil || order == nil {
		t.Fatalf("load created payment: %+v %v", order, err)
	}
	return order
}

func notificationForOrder(order *store.PaymentOrderRecord, tradeNo, status string) url.Values {
	return url.Values{"out_trade_no": {order.OutTradeNo}, "trade_no": {tradeNo}, "app_id": {order.AppID}, "seller_id": {order.SellerID}, "total_amount": {fmt.Sprintf("%d.%02d", order.AmountFen/100, order.AmountFen%100)}, "trade_status": {status}, "gmt_payment": {time.Now().UTC().Format(time.RFC3339)}}
}

func notifyPayment(f *apiFixture, values url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/v1/payments/alipay/notify", strings.NewReader(values.Encode()))
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func TestPaymentAPISignedNotificationValidation(t *testing.T) {
	f := newAPIFixture(t)
	priv, pub, key := paymentKeys(t)
	cookie := configurePayments(t, f, priv, pub)
	order := createPaymentForTest(t, f, cookie, "personal", "", "1.00", false, "")
	for name, mutate := range map[string]func(url.Values){"amount": func(v url.Values) { v.Set("total_amount", "1.01") }, "app": func(v url.Values) { v.Set("app_id", "2026000000000002") }, "seller": func(v url.Values) { v.Set("seller_id", "2088000000000002") }, "status": func(v url.Values) { v.Set("trade_status", "UNKNOWN") }} {
		values := notificationForOrder(order, "202611020010", "TRADE_SUCCESS")
		mutate(values)
		values = signedNotify(key, values)
		if rec := notifyPayment(f, values); rec.Code == 200 && rec.Body.String() == "success" {
			t.Errorf("signed wrong %s accepted", name)
		}
	}
	duplicate := signedNotify(key, notificationForOrder(order, "202611020010", "TRADE_SUCCESS"))
	duplicate.Add("total_amount", "1.00")
	if rec := notifyPayment(f, duplicate); rec.Code == 200 && rec.Body.String() == "success" {
		t.Fatal("duplicate notification field accepted")
	}
	saved, _ := f.store.GetPaymentOrder(context.Background(), order.OutTradeNo)
	account, _ := f.store.GetPersonalAccount(context.Background(), order.UserID)
	if saved.Status != store.PaymentPending || account.QuotaCredits != "0.000000" {
		t.Fatalf("invalid signed notification credited: %+v %+v", saved, account)
	}
}

func TestPaymentAPIConfigurationEncryptionAndDisabledOldOrders(t *testing.T) {
	f := newAPIFixture(t)
	priv, pub, key := paymentKeys(t)
	cookie := configurePayments(t, f, priv, pub)
	ctx := context.Background()
	before, err := loadPaymentConfig(ctx, f.deps, "")
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := f.deps.SecretBox.Decrypt(before.PrivateKeyCiphertext)
	if err != nil || decrypted != strings.TrimSpace(priv) {
		t.Fatal("application private key not encrypted correctly")
	}
	raw, _ := f.store.GetConfigValue(ctx, paymentConfigKey)
	if strings.Contains(string(raw), "PRIVATE KEY") || strings.Contains(string(raw), "PUBLIC KEY") {
		t.Fatal("plaintext key saved in config")
	}
	order := createPaymentForTest(t, f, cookie, "personal", "", "1.00", false, "")
	rec := f.do("PUT", "/console/api/payments/config", cookie, `{"enabled":false,"appId":"2026000000000001","sellerId":"2088000000000001","gateway":"production","privateKey":"","publicKey":""}`)
	if rec.Code != 200 {
		t.Fatalf("disable preserve keys: %d %s", rec.Code, rec.Body.String())
	}
	after, err := loadPaymentConfig(ctx, f.deps, "")
	if err != nil || after.PrivateKeyCiphertext != before.PrivateKeyCiphertext || after.PublicKeyCiphertext != before.PublicKeyCiphertext || after.ID == before.ID {
		t.Fatalf("blank keys did not preserve secret version: %+v %v", after, err)
	}
	rec = f.do("GET", "/console/api/payments/config", cookie, "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "Ciphertext") || strings.Contains(rec.Body.String(), "PRIVATE KEY") {
		t.Fatal("config response leaked a key")
	}
	rec = f.do("POST", "/console/api/payments/orders", cookie, `{"requestId":"`+uuid.NewString()+`","targetType":"personal","amountYuan":"1.00"}`)
	if rec.Code != 503 {
		t.Fatalf("disabled new order: %d %s", rec.Code, rec.Body.String())
	}
	if rec := notifyPayment(f, signedNotify(key, notificationForOrder(order, "202611020011", "TRADE_SUCCESS"))); rec.Code != 200 || rec.Body.String() != "success" {
		t.Fatalf("disabled old config notification: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPaymentAPIQueryInvalidSignatureAndConcurrentNotify(t *testing.T) {
	f := newAPIFixture(t)
	priv, pub, key := paymentKeys(t)
	cookie := configurePayments(t, f, priv, pub)
	order := createPaymentForTest(t, f, cookie, "personal", "", "1.00", false, "")
	raw, _ := json.Marshal(map[string]any{"code": "10000", "out_trade_no": order.OutTradeNo, "trade_no": "202611020012", "trade_status": "TRADE_SUCCESS", "total_amount": "1.00", "send_pay_date": time.Now().UTC().Format(time.RFC3339)})
	f.deps.PaymentHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"alipay_trade_query_response":` + string(raw) + `,"sign":"bad"}`)), Header: make(http.Header), Request: req}, nil
	})}
	path := "/console/api/payments/orders/" + order.OutTradeNo + "/reconcile"
	rec := f.do("POST", path, cookie, "{}")
	if rec.Code != 502 {
		t.Fatalf("unsigned query accepted: %d %s", rec.Code, rec.Body.String())
	}
	pending, _ := f.store.GetPaymentOrder(context.Background(), order.OutTradeNo)
	if pending.Status != store.PaymentPending {
		t.Fatal("unsigned query paid order")
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan *httptest.ResponseRecorder, 1)
	f.deps.PaymentHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		body := signedQueryEnvelope(t, key, raw)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})}
	go func() { done <- f.do("POST", path, cookie, "{}") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("query transport not called")
	}
	rec = notifyPayment(f, signedNotify(key, notificationForOrder(order, "202611020012", "TRADE_SUCCESS")))
	close(release)
	if rec.Code != 200 || rec.Body.String() != "success" {
		t.Fatalf("concurrent notification: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case rec = <-done:
		if rec.Code != 200 {
			t.Fatalf("concurrent query: %d %s", rec.Code, rec.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("query did not complete")
	}
	account, _ := f.store.GetPersonalAccount(context.Background(), order.UserID)
	flows, _ := f.store.ListQuotaAdjustments(context.Background(), order.TeamID, 100)
	if account.QuotaCredits != "1.000000" || len(flows) != 1 {
		t.Fatalf("notification/query double credited: %+v %+v", account, flows)
	}
}

func TestPaymentAPISameRequestAndPersonalOwnership(t *testing.T) {
	f := newTenantFixture(t)
	priv, pub, _ := paymentKeys(t)
	raw, _ := json.Marshal(map[string]any{"enabled": true, "appId": "2026000000000001", "sellerId": "2088000000000001", "gateway": "production", "privateKey": priv, "publicKey": pub})
	if rec := f.do("PUT", "/console/api/payments/config", f.rootCookie, string(raw)); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	requestID := uuid.NewString()
	order := createPaymentForTest(t, f.apiFixture, f.memberCookie, "personal", "", "1.00", false, requestID)
	replay := createPaymentForTest(t, f.apiFixture, f.memberCookie, "personal", "", "1.00", false, requestID)
	if replay.ID != order.ID {
		t.Fatal("replay created another order")
	}
	rec := f.do("POST", "/console/api/payments/orders", f.memberCookie, `{"requestId":"`+requestID+`","targetType":"personal","amountYuan":"2.00"}`)
	if rec.Code != 409 {
		t.Fatalf("changed amount: %d %s", rec.Code, rec.Body.String())
	}
	orders, _ := f.store.ListPaymentOrders(context.Background(), store.PaymentOrderFilter{UserID: f.member.ID})
	if len(orders) != 1 {
		t.Fatalf("duplicate stored orders: %d", len(orders))
	}
	for _, cookie := range []string{f.adminCookie, f.otherCookie} {
		if rec = f.do("GET", "/console/api/payments/orders/"+order.OutTradeNo, cookie, ""); rec.Code != 403 {
			t.Fatalf("other user personal order: %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec = f.do("POST", "/console/api/payments/orders", f.memberCookie, `{"requestId":"`+uuid.NewString()+`","targetType":"personal","amountYuan":"0.01"}`); rec.Code != 400 {
		t.Fatalf("ordinary low payment: %d %s", rec.Code, rec.Body.String())
	}
	if rec = f.do("PUT", "/console/api/payments/config", f.adminCookie, string(raw)); rec.Code != 403 {
		t.Fatalf("non-root config write: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPaymentAPIClosedLateSuccess(t *testing.T) {
	f := newAPIFixture(t)
	priv, pub, key := paymentKeys(t)
	cookie := configurePayments(t, f, priv, pub)
	order := createPaymentForTest(t, f, cookie, "personal", "", "1.00", false, "")
	rec := notifyPayment(f, signedNotify(key, notificationForOrder(order, "202611020013", "TRADE_CLOSED")))
	if rec.Code != 200 || rec.Body.String() != "success" {
		t.Fatalf("closed notify: %d %s", rec.Code, rec.Body.String())
	}
	closed, _ := f.store.GetPaymentOrder(context.Background(), order.OutTradeNo)
	if closed.Status != store.PaymentClosed {
		t.Fatalf("closed notification not persisted: %+v", closed)
	}
	rec = notifyPayment(f, signedNotify(key, notificationForOrder(order, "202611020013", "TRADE_SUCCESS")))
	if rec.Code != 200 || rec.Body.String() != "success" {
		t.Fatalf("late successful notify: %d %s", rec.Code, rec.Body.String())
	}
	paid, _ := f.store.GetPaymentOrder(context.Background(), order.OutTradeNo)
	if paid.Status != store.PaymentPaid || paid.ClosedAt != nil {
		t.Fatalf("closed state not repaired: %+v", paid)
	}
}
