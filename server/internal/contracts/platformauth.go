package contracts

import (
	"fmt"
	"strings"
)

// ---- platform-auth 请求/响应形状(zod 镜像,只含服务端实际解析/构造的部分) ----

// PlatformAuthInitRequest:strict 校验(未知字段整体拒绝),device_name 最大
// 120 字符、platform 最大 60,trim 后生效(与 platformAuthInitRequestSchema 一致)。
type PlatformAuthInitRequest struct {
	DeviceName *string
	Platform   *string
}

func ParsePlatformAuthInitRequest(body map[string]any) (*PlatformAuthInitRequest, error) {
	for key := range body {
		if key != "device_name" && key != "platform" {
			return nil, fmt.Errorf("未知字段: %s", key)
		}
	}
	req := &PlatformAuthInitRequest{}
	// zod 语义:字段不存在=合法(optional);显式 null=类型错误。两者必须区分。
	if v, exists := body["device_name"]; exists {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("device_name必须是字符串")
		}
		trimmed := strings.TrimSpace(s)
		if n := len([]rune(trimmed)); n > 120 {
			return nil, fmt.Errorf("device_name过长,最多 120 字符")
		}
		req.DeviceName = &trimmed
	}
	if v, exists := body["platform"]; exists {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("platform必须是字符串")
		}
		trimmed := strings.TrimSpace(s)
		if n := len([]rune(trimmed)); n > 60 {
			return nil, fmt.Errorf("platform过长,最多 60 字符")
		}
		req.Platform = &trimmed
	}
	return req, nil
}

// ---- 登录流轮询(platformAuthPoll*) ----

type PollStatus string

const (
	PollStatusPending    PollStatus = "pending"
	PollStatusAuthorized PollStatus = "authorized"
	PollStatusConsumed   PollStatus = "consumed"
	PollStatusExpired    PollStatus = "expired"
	PollStatusDenied     PollStatus = "denied"
)

type PlatformWireUser struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

type PlatformWireTeam struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type PlatformWireCredential struct {
	Token     string `json:"token"`
	TokenID   string `json:"token_id"`
	TokenHint string `json:"token_hint"`
}

// PlatformAuthPollData:authorized 分支带三段,其余只含 status。
type PlatformAuthPollData struct {
	Status      PollStatus
	Credentials *PlatformWireCredential
	User        *PlatformWireUser
	Teams       []PlatformWireTeam
}
