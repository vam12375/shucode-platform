package media

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ProviderError 只保存可公开的供应商错误码；原 message 可能含素材地址和凭据，不能进入任务投影。
type ProviderError struct {
	Code     string `json:"code"`
	Category string `json:"category,omitempty"`
}

type providerErrorResponse struct {
	Code string
}

func (e *providerErrorResponse) UnmarshalJSON(raw []byte) error {
	// 错误对象缺失或畸形不应吞掉已确认的 failed 终态；只提取字符串 code，其余字段直接丢弃。
	e.Code = ""
	var response struct {
		Code json.RawMessage `json:"code"`
	}
	if json.Unmarshal(raw, &response) == nil {
		_ = json.Unmarshal(response.Code, &e.Code)
	}
	return nil
}

func sanitizeProviderError(code string) *ProviderError {
	if len(code) == 0 || len(code) > 128 {
		return nil
	}
	for _, ch := range code {
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '.' || ch == ':' || ch == '-' {
			continue
		}
		return nil
	}
	category := ""
	// 只按已知 code 精确分类；未知 code 保留以便核对，不能从供应商原文猜测失败原因。
	switch code {
	case "InputTextSensitiveContentDetected", "InputImageSensitiveContentDetected", "InputVideoSensitiveContentDetected", "OutputTextSensitiveContentDetected", "OutputImageSensitiveContentDetected", "OutputVideoSensitiveContentDetected":
		category = "content_policy"
	case "InvalidParameter", "MissingParameter", "InvalidParameter.Unsupported", "InvalidParameter.NotFound":
		category = "invalid_input"
	case "AuthenticationError", "InvalidAccessKey", "AccessDenied":
		category = "authentication"
	case "QuotaExceeded", "AccountOverdue":
		category = "quota"
	case "RateLimitExceeded":
		category = "rate_limit"
	case "InternalError", "ServiceUnavailable":
		category = "service_unavailable"
	}
	return &ProviderError{Code: code, Category: category}
}

type providerHTTPError struct {
	status int
	detail *ProviderError
}

func (e *providerHTTPError) Error() string {
	// admission 仍依据稳定 HTTP 业务码区分明确拒绝与未知受理，不能拼入供应商正文改变分类。
	return fmt.Sprintf("provider_http_%d", e.status)
}

func providerErrorFrom(err error) *ProviderError {
	var upstream *providerHTTPError
	if errors.As(err, &upstream) && upstream.detail != nil {
		return sanitizeProviderError(upstream.detail.Code)
	}
	return nil
}

func publicVideoTaskID(id string) string {
	if len(id) == 0 || len(id) > 256 {
		return ""
	}
	for _, ch := range id {
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' {
			continue
		}
		return ""
	}
	return id
}
