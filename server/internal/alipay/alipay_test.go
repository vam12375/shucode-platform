package alipay

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixtureOnce sync.Once
var appKey, alipayKey *rsa.PrivateKey

func keys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	fixtureOnce.Do(func() {
		appKey, _ = rsa.GenerateKey(rand.Reader, 2048)
		alipayKey, _ = rsa.GenerateKey(rand.Reader, 2048)
	})
	return appKey, alipayKey
}
func config(t *testing.T) Config {
	_, ali := keys(t)
	pub, _ := x509.MarshalPKIXPublicKey(&ali.PublicKey)
	return Config{AppID: "2026000000000001", SellerID: "208800000000001", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(appKey)})), PublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})), NotifyURL: "https://platform.example/api/payments/alipay/notify", ReturnURL: "https://platform.example/console/#/payments"}
}
func signFor(t *testing.T, value string) string {
	_, key := keys(t)
	d := sha256.Sum256([]byte(value))
	raw, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}
func TestAmountAndPagePay(t *testing.T) {
	for _, tc := range []struct {
		in  string
		out int64
	}{{"0.01", 1}, {"1", 100}, {"1.1", 110}} {
		got, err := ParseAmountFen(tc.in)
		if err != nil || got != tc.out {
			t.Fatal(tc, got, err)
		}
		if got2, _ := ParseAmountFen(FormatAmountFen(got)); got2 != got {
			t.Fatal("roundtrip")
		}
	}
	for _, v := range []string{"", "1.", ".01", "1.001", "-1", "01", "1e2"} {
		if _, err := ParseAmountFen(v); err == nil {
			t.Fatal(v)
		}
	}
	c, err := New(config(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.PagePayURL("order-1", "充值", 1, false, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	var biz map[string]string
	if json.Unmarshal([]byte(q.Get("biz_content")), &biz) != nil || biz["total_amount"] != "0.01" || biz["product_code"] != "FAST_INSTANT_TRADE_PAY" {
		t.Fatal(biz)
	}
	d := sha256.Sum256([]byte(canonical(q, false)))
	sig, _ := base64.StdEncoding.DecodeString(q.Get("sign"))
	if rsa.VerifyPKCS1v15(&appKey.PublicKey, crypto.SHA256, d[:], sig) != nil {
		t.Fatal("signature")
	}
	if _, err := c.PagePayURL("order-1", "充值", 1, false, time.Now().Add(-time.Minute)); err == nil {
		t.Fatal("expired")
	}
}
func notification(t *testing.T) url.Values {
	v := url.Values{"app_id": {config(t).AppID}, "seller_id": {config(t).SellerID}, "out_trade_no": {"order-1"}, "trade_no": {"202611020001"}, "total_amount": {"0.01"}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"RSA2"}}
	v.Set("sign", signFor(t, canonical(v, true)))
	return v
}
func TestNotification(t *testing.T) {
	c, _ := New(config(t), nil)
	tr, err := c.VerifyNotification(notification(t))
	if err != nil || tr.AmountFen != 1 {
		t.Fatal(tr, err)
	}
	v := notification(t)
	v.Add("total_amount", "0.01")
	if _, err := c.VerifyNotification(v); err == nil {
		t.Fatal("duplicate")
	}
	for _, s := range []string{"TRADE_FINISHED", "WAIT_BUYER_PAY", "TRADE_CLOSED"} {
		v = notification(t)
		v.Set("trade_status", s)
		v.Set("sign", signFor(t, canonical(v, true)))
		if _, err := c.VerifyNotification(v); err != nil {
			t.Fatal(err)
		}
	}
}

type rt func(*http.Request) (*http.Response, error)

func (f rt) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestQuery(t *testing.T) {
	response := `{"code":"10000","out_trade_no":"order-1","trade_no":"202611020001","trade_status":"TRADE_SUCCESS","total_amount":"0.01"}`
	envelope := `{"alipay_trade_query_response":` + response + `,"sign":"` + signFor(t, response) + `"}`
	c, err := New(config(t), &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(envelope)), Request: r}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := c.Query(context.Background(), "order-1")
	if err != nil || tr.AmountFen != 1 {
		t.Fatal(tr, err)
	}
	missing := `{"code":"40004","sub_code":"ACQ.TRADE_NOT_EXIST"}`
	c, _ = New(config(t), &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"alipay_trade_query_response":` + missing + `,"sign":"` + signFor(t, missing) + `"}`)), Request: r}, nil
	})})
	if _, err = c.Query(context.Background(), "order-1"); !errors.Is(err, ErrTradeNotExist) {
		t.Fatal(err)
	}
}

func TestStrictNotificationAndQuerySecurity(t *testing.T) {
	client, err := New(config(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Empty trade_no must be rejected even when a correctly signed payload is supplied.
	v := notification(t)
	v.Set("trade_no", "")
	v.Set("sign", signFor(t, canonical(v, true)))
	if _, err := client.VerifyNotification(v); err == nil {
		t.Fatal("empty trade_no accepted")
	}

	// A signed response with the wrong order number, app ID, or seller ID is not a valid match.
	base := `{"code":"10000","out_trade_no":"order-1","trade_no":"202611020001","trade_status":"TRADE_SUCCESS","total_amount":"0.01","app_id":"2026000000000001","seller_id":"208800000000001"}`
	for _, replacement := range []struct{ old, next string }{{`"out_trade_no":"order-1"`, `"out_trade_no":"other-order"`}, {`"app_id":"2026000000000001"`, `"app_id":"2026000000000002"`}, {`"seller_id":"208800000000001"`, `"seller_id":"208800000000002"`}} {
		body := strings.Replace(base, replacement.old, replacement.next, 1)
		envelope := `{"alipay_trade_query_response":` + body + `,"sign":"` + signFor(t, body) + `"}`
		c, err := New(config(t), &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(envelope)), Request: r}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Query(context.Background(), "order-1"); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("wrong binding accepted: %v", err)
		}
	}

	// Unsigned "not found" cannot become ErrTradeNotExist.
	notFound := `{"code":"40004","sub_code":"ACQ.TRADE_NOT_EXIST"}`
	unsignedEnvelope := `{"alipay_trade_query_response":` + notFound + `,"sign":"invalid"}`
	c, err := New(config(t), &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(unsignedEnvelope)), Request: r}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(context.Background(), "order-1"); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("unsigned not-found accepted: %v", err)
	}

	// A tampered raw response must fail verification before business parsing.
	valid := `{"code":"10000","out_trade_no":"order-1","trade_no":"202611020001","trade_status":"TRADE_SUCCESS","total_amount":"0.01"}`
	tampered := `{"alipay_trade_query_response":` + valid + `,"sign":"` + signFor(t, valid) + `"}`
	tampered = strings.Replace(tampered, `"0.01"`, `"1.00"`, 1)
	c, err = New(config(t), &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tampered)), Request: r}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(context.Background(), "order-1"); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("tampered response accepted: %v", err)
	}
}

func TestClose(t *testing.T) {
	response := `{"code":"10000","out_trade_no":"order-1","trade_no":"202611020002"}`
	envelope := `{"alipay_trade_close_response":` + response + `,"sign":"` + signFor(t, response) + `"}`
	c, err := New(config(t), &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatal("close must use POST")
		}
		values, _ := url.ParseQuery(mustReadBody(t, r))
		if values.Get("method") != "alipay.trade.close" {
			t.Fatal("wrong close method")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(envelope)), Request: r}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	trade, err := c.Close(context.Background(), "order-1")
	if err != nil || trade.Status != "TRADE_CLOSED" || trade.AmountFen != 0 || trade.TradeNo != "202611020002" {
		t.Fatalf("%+v %v", trade, err)
	}

	// Invalid response signature is not a business not-found result.
	notFound := `{"code":"40004","sub_code":"ACQ.TRADE_NOT_EXIST"}`
	badEnvelope := `{"alipay_trade_close_response":` + notFound + `,"sign":"invalid"}`
	c, err = New(config(t), &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(badEnvelope)), Request: r}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Close(context.Background(), "order-1"); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("bad close signature accepted: %v", err)
	}
}

func mustReadBody(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestRequestCanonicalAndWAP(t *testing.T) {
	c, err := New(config(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.PagePayURL("wap-1", "充值", 1, true, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	values := u.Query()
	if values.Get("method") != "alipay.trade.wap.pay" {
		t.Fatal("wrong WAP method")
	}
	var biz map[string]string
	if json.Unmarshal([]byte(values.Get("biz_content")), &biz) != nil || biz["product_code"] != "QUICK_WAP_WAY" {
		t.Fatal("wrong WAP product")
	}
	if !strings.Contains(canonical(values, false), "sign_type=RSA2") || strings.Contains(canonical(values, true), "sign_type=") {
		t.Fatal("request and notification signature parameter rules differ")
	}
	digest := sha256.Sum256([]byte(canonical(values, false)))
	signature, _ := base64.StdEncoding.DecodeString(values.Get("sign"))
	if rsa.VerifyPKCS1v15(&appKey.PublicKey, crypto.SHA256, digest[:], signature) != nil {
		t.Fatal("invalid request signature")
	}
}

func TestKeyAndIdentifierRejection(t *testing.T) {
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config(t)
	cfg.PrivateKey = base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(small))
	if _, err := New(cfg, nil); err == nil {
		t.Fatal("weak private key accepted")
	}
	cfg = config(t)
	cfg.PublicKey = base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PublicKey(&small.PublicKey))
	if _, err := New(cfg, nil); err == nil {
		t.Fatal("weak public key accepted")
	}
	for _, value := range []string{"abc", "12345678&extra=1", "12345678\n", "12345678\u0000"} {
		cfg = config(t)
		cfg.AppID = value
		if _, err := New(cfg, nil); err == nil {
			t.Fatalf("invalid app identifier accepted: %q", value)
		}
	}
}

func TestNotificationTamperAndBindings(t *testing.T) {
	c, _ := New(config(t), nil)
	for _, key := range []string{"app_id", "seller_id", "trade_status", "total_amount", "trade_no"} {
		values := notification(t)
		values.Set(key, "bad")
		values.Set("sign", signFor(t, canonical(values, true)))
		if _, err := c.VerifyNotification(values); err == nil {
			t.Fatalf("invalid signed %s accepted", key)
		}
	}
	values := notification(t)
	values.Set("total_amount", "1.00")
	if _, err := c.VerifyNotification(values); !errors.Is(err, ErrInvalidSignature) {
		t.Fatal("tampered notification accepted")
	}
}

func TestQueryRedirectAndBodyLimit(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusOK} {
		calls := 0
		c, err := New(config(t), &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
			calls++
			body := ""
			headers := http.Header{}
			if status == http.StatusFound {
				headers.Set("Location", "https://evil.example")
			} else {
				body = strings.Repeat("x", maxResponseBytes+1)
			}
			return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Query(context.Background(), "order-1"); err == nil {
			t.Fatal("redirect/oversize accepted")
		}
		if calls != 1 {
			t.Fatal("redirect followed")
		}
	}
}
