// Package turnstile 校验 Cloudflare Turnstile 登录挑战。
// 只依赖 siteverify 接口，不在服务端保存 token 或挑战状态。
package turnstile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultEndpoint = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	DefaultAction   = "login"
	maxResponseSize = 64 << 10
)

var (
	ErrMissingToken       = errors.New("turnstile: missing token")
	ErrVerificationFailed = errors.New("turnstile: verification failed")
	ErrUnavailable        = errors.New("turnstile: verification unavailable")
)

type Options struct {
	SecretKey      string
	Endpoint       string
	ExpectedAction string
	HTTPClient     *http.Client
}

type Client struct {
	secretKey      string
	endpoint       string
	expectedAction string
	httpClient     *http.Client
}

// NewClient 创建 siteverify 客户端。Endpoint 为空时使用 Cloudflare 官方地址；
// 仅允许回环地址使用 http，避免把 secret 明文发到远端。
func NewClient(options Options) (*Client, error) {
	secretKey := strings.TrimSpace(options.SecretKey)
	if secretKey == "" {
		return nil, errors.New("turnstile: secret key is empty")
	}
	endpoint := strings.TrimSpace(options.Endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("turnstile: invalid verify endpoint")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("turnstile: verify endpoint must use https")
	}
	expectedAction := strings.TrimSpace(options.ExpectedAction)
	if expectedAction == "" {
		expectedAction = DefaultAction
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("turnstile: redirects are not allowed")
			},
		}
	}
	return &Client{
		secretKey:      secretKey,
		endpoint:       endpoint,
		expectedAction: expectedAction,
		httpClient:     httpClient,
	}, nil
}

type siteVerifyResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
	Action     string   `json:"action"`
}

// Verify 校验一次 Turnstile token。token 只用于请求体，不会进入错误信息或日志。
func (c *Client) Verify(ctx context.Context, token, remoteIP string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return ErrMissingToken
	}
	form := url.Values{}
	form.Set("secret", c.secretKey)
	form.Set("response", token)
	if remoteIP = strings.TrimSpace(remoteIP); remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "shucode-platform/turnstile")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: http %d", ErrUnavailable, resp.StatusCode)
	}
	var result siteVerifyResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("%w: invalid response", ErrUnavailable)
	}
	if !result.Success {
		codes := strings.Join(result.ErrorCodes, ",")
		if codes == "" {
			codes = "rejected"
		}
		return fmt.Errorf("%w: %s", ErrVerificationFailed, codes)
	}
	// widget 明确回传 action 时必须与登录动作一致；旧版 widget 未回传时保持兼容。
	if result.Action != "" && result.Action != c.expectedAction {
		return fmt.Errorf("%w: action mismatch", ErrVerificationFailed)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
