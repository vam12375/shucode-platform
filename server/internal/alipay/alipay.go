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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ProductionGateway = "https://openapi.alipay.com/gateway.do"
	SandboxGateway    = "https://openapi-sandbox.dl.alipaydev.com/gateway.do"
	maxResponseBytes  = 1 << 20
)

var (
	ErrInvalidSignature = errors.New("alipay: invalid signature")
	ErrTradeNotExist    = errors.New("alipay: trade not exist")
	ErrBusinessFailure  = errors.New("alipay: business failure")
)

type Config struct {
	AppID      string
	SellerID   string
	PrivateKey string
	PublicKey  string
	NotifyURL  string
	ReturnURL  string
	GatewayURL string
}

type Client struct {
	config     Config
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	httpClient *http.Client
}

type Trade struct {
	OutTradeNo string
	TradeNo    string
	AppID      string
	SellerID   string
	Status     string
	AmountFen  int64
	PaidAt     time.Time
}

func New(config Config, client *http.Client) (*Client, error) {
	if config.AppID != strings.TrimSpace(config.AppID) || config.SellerID != strings.TrimSpace(config.SellerID) {
		return nil, errors.New("alipay: app id and seller id must not contain whitespace")
	}
	config.AppID = strings.TrimSpace(config.AppID)
	config.SellerID = strings.TrimSpace(config.SellerID)
	config.GatewayURL = strings.TrimSpace(config.GatewayURL)
	if config.GatewayURL == "" {
		config.GatewayURL = ProductionGateway
	}
	if config.AppID == "" || config.SellerID == "" {
		return nil, errors.New("alipay: app id and seller id are required")
	}
	if !alipayIdentifierPattern.MatchString(config.AppID) || !alipayIdentifierPattern.MatchString(config.SellerID) {
		return nil, errors.New("alipay: app id and seller id must be numeric identifiers")
	}
	if config.GatewayURL != ProductionGateway && config.GatewayURL != SandboxGateway {
		return nil, errors.New("alipay: gateway url is not an official endpoint")
	}
	for _, callback := range []string{config.NotifyURL, config.ReturnURL} {
		if callback == "" {
			continue
		}
		parsed, err := url.Parse(callback)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"))) {
			return nil, errors.New("alipay: callback must use https")
		}
	}
	privateKey, err := parsePrivateKey(config.PrivateKey)
	if err != nil {
		return nil, err
	}
	publicKey, err := parsePublicKey(config.PublicKey)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	} else {
		copy := *client
		if copy.Timeout == 0 {
			copy.Timeout = 15 * time.Second
		}
		client = &copy
	}
	// 网关响应不得重定向，防止带签名的交易查询被发送到非支付宝目标。
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("alipay: redirects are forbidden")
	}
	return &Client{config: config, privateKey: privateKey, publicKey: publicKey, httpClient: client}, nil
}

func (c *Client) PagePayURL(outTradeNo, subject string, amountFen int64, mobile bool, expiresAt time.Time) (string, error) {
	outTradeNo = strings.TrimSpace(outTradeNo)
	subject = strings.TrimSpace(subject)
	if !orderNumberPattern.MatchString(outTradeNo) || subject == "" || len(subject) > 256 {
		return "", errors.New("alipay: invalid order fields")
	}
	if amountFen <= 0 || amountFen > 99_999_999_999_999 {
		return "", errors.New("alipay: amount must be positive")
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(30 * time.Minute)
	}
	remaining := time.Until(expiresAt)
	if remaining <= 0 {
		return "", errors.New("alipay: order has expired")
	}
	if remaining > 30*time.Minute {
		expiresAt = time.Now().Add(30 * time.Minute)
		remaining = 30 * time.Minute
	}
	minutes := int((remaining + time.Minute - 1) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	method, product := "alipay.trade.page.pay", "FAST_INSTANT_TRADE_PAY"
	if mobile {
		method, product = "alipay.trade.wap.pay", "QUICK_WAP_WAY"
	}
	biz := map[string]string{
		"out_trade_no":    outTradeNo,
		"product_code":    product,
		"total_amount":    FormatAmountFen(amountFen),
		"subject":         subject,
		"timeout_express": strconv.Itoa(minutes) + "m",
	}
	if !expiresAt.IsZero() {
		biz["time_expire"] = expiresAt.In(shanghai).Format("2006-01-02 15:04:05")
	}
	bizJSON, _ := json.Marshal(biz)
	params := url.Values{
		"app_id": {c.config.AppID}, "method": {method}, "format": {"JSON"}, "charset": {"utf-8"},
		"sign_type": {"RSA2"}, "timestamp": {time.Now().In(shanghai).Format("2006-01-02 15:04:05")}, "version": {"1.0"},
		"biz_content": {string(bizJSON)},
	}
	if c.config.NotifyURL != "" {
		params.Set("notify_url", c.config.NotifyURL)
	}
	if c.config.ReturnURL != "" {
		params.Set("return_url", c.config.ReturnURL)
	}
	sign, err := c.sign(params)
	if err != nil {
		return "", err
	}
	params.Set("sign", sign)
	return c.config.GatewayURL + "?" + params.Encode(), nil
}

func (c *Client) VerifyNotification(values url.Values) (Trade, error) {
	if values == nil {
		return Trade{}, ErrInvalidSignature
	}
	for _, vals := range values {
		if len(vals) != 1 {
			return Trade{}, ErrInvalidSignature
		}
	}
	if values.Get("sign_type") != "RSA2" || !c.verify(values, values.Get("sign")) {
		return Trade{}, ErrInvalidSignature
	}
	trade, err := tradeFromValues(values)
	if err != nil {
		return Trade{}, err
	}
	if trade.AppID != c.config.AppID || (c.config.SellerID != "" && trade.SellerID != c.config.SellerID) {
		return Trade{}, ErrInvalidSignature
	}
	return trade, nil
}

func (c *Client) Query(ctx context.Context, outTradeNo string) (*Trade, error) {
	outTradeNo = strings.TrimSpace(outTradeNo)
	if !orderNumberPattern.MatchString(outTradeNo) {
		return nil, errors.New("alipay: invalid order number")
	}
	bizJSON, _ := json.Marshal(map[string]string{"out_trade_no": outTradeNo})
	params := url.Values{
		"app_id": {c.config.AppID}, "method": {"alipay.trade.query"}, "format": {"JSON"}, "charset": {"utf-8"},
		"sign_type": {"RSA2"}, "timestamp": {time.Now().In(shanghai).Format("2006-01-02 15:04:05")}, "version": {"1.0"},
		"biz_content": {string(bizJSON)},
	}
	sign, err := c.sign(params)
	if err != nil {
		return nil, err
	}
	params.Set("sign", sign)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.GatewayURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("alipay: gateway http status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("alipay: response too large")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("alipay: invalid gateway response")
	}
	responseRaw, ok := envelope["alipay_trade_query_response"]
	if !ok {
		return nil, errors.New("alipay: missing query response")
	}
	var responseSign string
	if raw, ok := envelope["sign"]; ok {
		_ = json.Unmarshal(raw, &responseSign)
	}
	if !c.verifyRaw(responseRaw, responseSign) {
		return nil, ErrInvalidSignature
	}
	var response struct {
		Code        string `json:"code"`
		SubCode     string `json:"sub_code"`
		OutTradeNo  string `json:"out_trade_no"`
		TradeNo     string `json:"trade_no"`
		AppID       string `json:"app_id"`
		SellerID    string `json:"seller_id"`
		TradeStatus string `json:"trade_status"`
		TotalAmount string `json:"total_amount"`
		SendPayDate string `json:"send_pay_date"`
	}
	if err := json.Unmarshal(responseRaw, &response); err != nil {
		return nil, errors.New("alipay: invalid query response")
	}
	if response.SubCode == "ACQ.TRADE_NOT_EXIST" {
		return nil, ErrTradeNotExist
	}
	if response.Code != "10000" {
		return nil, ErrBusinessFailure
	}
	amountFen, err := ParseAmountFen(response.TotalAmount)
	if err != nil || amountFen <= 0 || !validTradeStatus(response.TradeStatus) || !tradeNumberPattern.MatchString(response.TradeNo) || response.OutTradeNo != outTradeNo || (response.AppID != "" && response.AppID != c.config.AppID) || (response.SellerID != "" && response.SellerID != c.config.SellerID) {
		return nil, ErrInvalidSignature
	}
	return &Trade{OutTradeNo: response.OutTradeNo, TradeNo: response.TradeNo, AppID: c.config.AppID, SellerID: c.config.SellerID, Status: response.TradeStatus, AmountFen: amountFen, PaidAt: parseTime(response.SendPayDate)}, nil
}

// Close requests closure of an unpaid trade. The response is verified before
// interpreting its business code; an unsigned "not found" response is never
// accepted as a close result.
func (c *Client) Close(ctx context.Context, outTradeNo string) (*Trade, error) {
	outTradeNo = strings.TrimSpace(outTradeNo)
	if !orderNumberPattern.MatchString(outTradeNo) {
		return nil, errors.New("alipay: invalid order number")
	}
	bizJSON, _ := json.Marshal(map[string]string{"out_trade_no": outTradeNo})
	params := url.Values{
		"app_id": {c.config.AppID}, "method": {"alipay.trade.close"}, "format": {"JSON"}, "charset": {"utf-8"},
		"sign_type": {"RSA2"}, "timestamp": {time.Now().In(shanghai).Format("2006-01-02 15:04:05")}, "version": {"1.0"},
		"biz_content": {string(bizJSON)},
	}
	sign, err := c.sign(params)
	if err != nil {
		return nil, err
	}
	params.Set("sign", sign)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.GatewayURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("alipay: gateway http status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("alipay: response too large")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("alipay: invalid gateway response")
	}
	responseRaw, ok := envelope["alipay_trade_close_response"]
	if !ok {
		return nil, errors.New("alipay: missing close response")
	}
	var responseSign string
	if raw, ok := envelope["sign"]; ok {
		_ = json.Unmarshal(raw, &responseSign)
	}
	if !c.verifyRaw(responseRaw, responseSign) {
		return nil, ErrInvalidSignature
	}
	var response struct {
		Code       string `json:"code"`
		SubCode    string `json:"sub_code"`
		OutTradeNo string `json:"out_trade_no"`
		TradeNo    string `json:"trade_no"`
		AppID      string `json:"app_id"`
		SellerID   string `json:"seller_id"`
	}
	if err := json.Unmarshal(responseRaw, &response); err != nil {
		return nil, errors.New("alipay: invalid close response")
	}
	if response.SubCode == "ACQ.TRADE_NOT_EXIST" {
		return nil, ErrTradeNotExist
	}
	if response.Code != "10000" {
		return nil, ErrBusinessFailure
	}
	if !orderNumberPattern.MatchString(response.OutTradeNo) || response.OutTradeNo != outTradeNo || !tradeNumberPattern.MatchString(response.TradeNo) || (response.AppID != "" && response.AppID != c.config.AppID) || (response.SellerID != "" && response.SellerID != c.config.SellerID) {
		return nil, ErrInvalidSignature
	}
	return &Trade{OutTradeNo: response.OutTradeNo, TradeNo: response.TradeNo, AppID: c.config.AppID, SellerID: c.config.SellerID, Status: "TRADE_CLOSED"}, nil
}

func (c *Client) sign(values url.Values) (string, error) {
	digest := sha256.Sum256([]byte(canonical(values, false)))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

func (c *Client) verify(values url.Values, signature string) bool {
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || signature == "" {
		return false
	}
	digest := sha256.Sum256([]byte(canonical(values, true)))
	return rsa.VerifyPKCS1v15(c.publicKey, crypto.SHA256, digest[:], sig) == nil
}

func (c *Client) verifyRaw(raw json.RawMessage, signature string) bool {
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || signature == "" {
		return false
	}
	digest := sha256.Sum256(raw)
	return rsa.VerifyPKCS1v15(c.publicKey, crypto.SHA256, digest[:], sig) == nil
}

func canonical(values url.Values, notification bool) string {
	keys := make([]string, 0, len(values))
	for key, vals := range values {
		if key == "sign" || (notification && key == "sign_type") || len(vals) == 0 || vals[0] == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}
	return strings.Join(parts, "&")
}

func tradeFromValues(values url.Values) (Trade, error) {
	amountFen, err := ParseAmountFen(values.Get("total_amount"))
	if err != nil || amountFen <= 0 || !orderNumberPattern.MatchString(values.Get("out_trade_no")) || !tradeNumberPattern.MatchString(values.Get("trade_no")) || values.Get("app_id") == "" || values.Get("seller_id") == "" {
		return Trade{}, errors.New("alipay: invalid notification")
	}
	status := values.Get("trade_status")
	if !validTradeStatus(status) {
		return Trade{}, errors.New("alipay: invalid trade status")
	}
	return Trade{OutTradeNo: values.Get("out_trade_no"), TradeNo: values.Get("trade_no"), AppID: values.Get("app_id"), SellerID: values.Get("seller_id"), Status: status, AmountFen: amountFen, PaidAt: parseTime(values.Get("gmt_payment"))}, nil
}

func parseTime(value string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
		if parsed, err := time.ParseInLocation(layout, value, shanghai); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func ParseAmountFen(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") {
		return 0, errors.New("alipay: invalid amount")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || len(parts[0]) > 12 || (len(parts) == 2 && (len(parts[1]) == 0 || len(parts[1]) > 2)) {
		return 0, errors.New("alipay: invalid amount")
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	for len(frac) < 2 {
		frac += "0"
	}
	if parts[0] == "" || (len(parts[0]) > 1 && parts[0][0] == '0') {
		return 0, errors.New("alipay: invalid amount")
	}
	for _, ch := range parts[0] + frac {
		if ch < '0' || ch > '9' {
			return 0, errors.New("alipay: invalid amount")
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > (1<<62) {
		return 0, errors.New("alipay: invalid amount")
	}
	return whole*100 + int64(parseDigits(frac)), nil
}

func parseDigits(value string) int     { n, _ := strconv.Atoi(value); return n }
func FormatAmountFen(fen int64) string { return fmt.Sprintf("%d.%02d", fen/100, fen%100) }

func parsePrivateKey(value string) (*rsa.PrivateKey, error) {
	der, err := decodeKey(value)
	if err != nil {
		return nil, errors.New("alipay: invalid private key")
	}
	if block, _ := pem.Decode(der); block != nil {
		der = block.Bytes
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		if key.Validate() != nil {
			return nil, errors.New("alipay: invalid private key")
		}
		if key.N.BitLen() < 2048 {
			return nil, errors.New("alipay: rsa key is too small")
		}
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("alipay: invalid private key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, errors.New("alipay: invalid private key")
	}
	return key, nil
}

func parsePublicKey(value string) (*rsa.PublicKey, error) {
	der, err := decodeKey(value)
	if err != nil {
		return nil, errors.New("alipay: invalid public key")
	}
	if block, _ := pem.Decode(der); block != nil {
		der = block.Bytes
	}
	if key, err := x509.ParsePKIXPublicKey(der); err == nil {
		if rsaKey, ok := key.(*rsa.PublicKey); ok && rsaKey.N.BitLen() >= 2048 {
			return rsaKey, nil
		}
	}
	if key, err := x509.ParsePKCS1PublicKey(der); err == nil && key.N.BitLen() >= 2048 {
		return key, nil
	}
	return nil, errors.New("alipay: invalid public key")
}

func decodeKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "BEGIN") {
		return []byte(strings.ReplaceAll(value, `\n`, "\n")), nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if der, err := enc.DecodeString(value); err == nil {
			return der, nil
		}
	}
	return nil, errors.New("invalid key")
}

var shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)
var orderNumberPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var tradeNumberPattern = regexp.MustCompile(`^[0-9]{1,64}$`)
var alipayIdentifierPattern = regexp.MustCompile(`^[0-9]{8,32}$`)

func validTradeStatus(value string) bool {
	switch value {
	case "TRADE_SUCCESS", "TRADE_FINISHED", "WAIT_BUYER_PAY", "TRADE_CLOSED":
		return true
	default:
		return false
	}
}
