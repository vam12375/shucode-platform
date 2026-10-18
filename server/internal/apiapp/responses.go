package apiapp

import (
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 响应映射:store 记录 → 控制台/客户端线上字段名(camelCase,时间为 toISOString 口径)。

// isoString 复刻 Date#toISOString:UTC、毫秒固定三位、以 Z 结尾。
func isoString(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func isoStringPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := isoString(*t)
	return &v
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	v := value
	return &v
}

type userPublic struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	DisplayName string  `json:"displayName"`
	Role        string  `json:"role"`
	DisabledAt  *string `json:"disabledAt"`
	CreatedAt   string  `json:"createdAt"`
}

func toPublicUser(user *store.UserRecord) userPublic {
	return userPublic{
		ID: user.ID, Email: user.Email, DisplayName: user.DisplayName,
		Role: string(user.Role), DisabledAt: isoStringPtr(user.DisabledAt),
		CreatedAt: isoString(user.CreatedAt),
	}
}

type teamPublic struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	QuotaCredits    string  `json:"quotaCredits"`
	UsedCredits     string  `json:"usedCredits"`
	ReservedCredits string  `json:"reservedCredits"`
	Unlimited       bool    `json:"unlimited"`
	CreatedAt       string  `json:"createdAt"`
	DisabledAt      *string `json:"disabledAt"`
}

func toPublicTeam(team *store.TeamRecord) teamPublic {
	return teamPublic{
		ID: team.ID, Name: team.Name, QuotaCredits: team.QuotaCredits,
		UsedCredits: team.UsedCredits, ReservedCredits: team.ReservedCredits,
		Unlimited: team.Unlimited, CreatedAt: isoString(team.CreatedAt),
		DisabledAt: isoStringPtr(team.DisabledAt),
	}
}

type modelPublic struct {
	ModelID                string  `json:"modelId"`
	DisplayName            string  `json:"displayName"`
	UpstreamModel          *string `json:"upstreamModel"`
	UpstreamProviderID     *string `json:"upstreamProviderId"`
	InputPricePerMtok      string  `json:"inputPricePerMtok"`
	OutputPricePerMtok     string  `json:"outputPricePerMtok"`
	CacheReadPricePerMtok  string  `json:"cacheReadPricePerMtok"`
	CacheWritePricePerMtok string  `json:"cacheWritePricePerMtok"`
	Enabled                bool    `json:"enabled"`
	SortOrder              int64   `json:"sortOrder"`
	CreatedAt              string  `json:"createdAt"`
	UpdatedAt              string  `json:"updatedAt"`
}

func toPublicModel(model *store.PlatformModelRecord) modelPublic {
	return modelPublic{
		ModelID: model.ModelID, DisplayName: model.DisplayName,
		UpstreamModel:      stringPtrOrNil(model.UpstreamModel),
		UpstreamProviderID: stringPtrOrNil(model.UpstreamProviderID),
		InputPricePerMtok:  model.InputPricePerMtok, OutputPricePerMtok: model.OutputPricePerMtok,
		CacheReadPricePerMtok: model.CacheReadPricePerMtok, CacheWritePricePerMtok: model.CacheWritePricePerMtok,
		Enabled: model.Enabled, SortOrder: model.SortOrder,
		CreatedAt: isoString(model.CreatedAt), UpdatedAt: isoString(model.UpdatedAt),
	}
}

type usagePublic struct {
	ID                  string  `json:"id"`
	RequestID           string  `json:"requestId"`
	UserID              string  `json:"userId"`
	TeamID              string  `json:"teamId"`
	DeviceTokenID       *string `json:"deviceTokenId"`
	Model               string  `json:"model"`
	UpstreamModel       string  `json:"upstreamModel"`
	ProviderID          *string `json:"providerId"`
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheReadTokens     int64   `json:"cacheReadTokens"`
	CacheCreationTokens int64   `json:"cacheCreationTokens"`
	CreditsMicro        int64   `json:"creditsMicro"`
	Status              string  `json:"status"`
	ErrorType           *string `json:"errorType"`
	ClientIP            *string `json:"clientIp"`
	LatencyMs           *int64  `json:"latencyMs"`
	CreatedAt           string  `json:"createdAt"`
}

func toPublicUsage(record *store.UsageRecord) usagePublic {
	return usagePublic{
		ID: record.ID, RequestID: record.RequestID, UserID: record.UserID, TeamID: record.TeamID,
		DeviceTokenID: stringPtrOrNil(record.DeviceTokenID),
		Model:         record.Model, UpstreamModel: record.UpstreamModel,
		ProviderID:  stringPtrOrNil(record.ProviderID),
		InputTokens: record.InputTokens, OutputTokens: record.OutputTokens,
		CacheReadTokens: record.CacheReadTokens, CacheCreationTokens: record.CacheCreationTokens,
		CreditsMicro: record.CreditsMicro, Status: string(record.Status),
		ErrorType: stringPtrOrNil(record.ErrorType),
		ClientIP:  stringPtrOrNil(record.ClientIP),
		LatencyMs: record.LatencyMs, CreatedAt: isoString(record.CreatedAt),
	}
}

type aggregatePublic struct {
	Model               string `json:"model"`
	Requests            int64  `json:"requests"`
	InputTokens         int64  `json:"inputTokens"`
	OutputTokens        int64  `json:"outputTokens"`
	CacheReadTokens     int64  `json:"cacheReadTokens"`
	CacheCreationTokens int64  `json:"cacheCreationTokens"`
	CreditsMicro        int64  `json:"creditsMicro"`
	LatencyMsSum        int64  `json:"latencyMsSum"`
}

func toPublicAggregate(row store.UsageAggregateRow) aggregatePublic {
	return aggregatePublic{
		Model: row.Model, Requests: row.Requests, InputTokens: row.InputTokens,
		OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens,
		CacheCreationTokens: row.CacheCreationTokens, CreditsMicro: row.CreditsMicro,
		LatencyMsSum: row.LatencyMsSum,
	}
}

type seriesPublic struct {
	BucketStart         string `json:"bucketStart"`
	Requests            int64  `json:"requests"`
	InputTokens         int64  `json:"inputTokens"`
	OutputTokens        int64  `json:"outputTokens"`
	CacheReadTokens     int64  `json:"cacheReadTokens"`
	CacheCreationTokens int64  `json:"cacheCreationTokens"`
	CreditsMicro        int64  `json:"creditsMicro"`
	LatencyMsSum        int64  `json:"latencyMsSum"`
}

func toPublicSeries(row store.UsageSeriesRow) seriesPublic {
	return seriesPublic{
		BucketStart: row.BucketStart, Requests: row.Requests, InputTokens: row.InputTokens,
		OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens,
		CacheCreationTokens: row.CacheCreationTokens, CreditsMicro: row.CreditsMicro,
		LatencyMsSum: row.LatencyMsSum,
	}
}

type auditPublic struct {
	ID          string  `json:"id"`
	ActorUserID *string `json:"actorUserId"`
	Action      string  `json:"action"`
	TargetType  *string `json:"targetType"`
	TargetID    *string `json:"targetId"`
	Detail      any     `json:"detail"`
	CreatedAt   string  `json:"createdAt"`
	ActorEmail  *string `json:"actorEmail"`
}

func toPublicAudit(row store.AdminAuditLogView) auditPublic {
	var detail any
	if len(row.Detail) > 0 {
		detail, _ = jsonDecode(row.Detail)
	}
	return auditPublic{
		ID: row.ID, ActorUserID: stringPtrOrNil(row.ActorUserID), Action: row.Action,
		TargetType: stringPtrOrNil(row.TargetType), TargetID: stringPtrOrNil(row.TargetID),
		Detail: detail, CreatedAt: isoString(row.CreatedAt),
		ActorEmail: stringPtrOrNil(row.ActorEmail),
	}
}

type quotaAdjustmentPublic struct {
	ID              string  `json:"id"`
	Action          string  `json:"action"`
	OldQuotaCredits string  `json:"oldQuotaCredits"`
	NewQuotaCredits string  `json:"newQuotaCredits"`
	OldUnlimited    bool    `json:"oldUnlimited"`
	NewUnlimited    bool    `json:"newUnlimited"`
	CreatedAt       string  `json:"createdAt"`
	ActorEmail      *string `json:"actorEmail"`
}

func toPublicQuotaAdjustment(row store.TeamQuotaAdjustmentView) quotaAdjustmentPublic {
	return quotaAdjustmentPublic{
		ID: row.ID, Action: row.Action, OldQuotaCredits: row.OldQuotaCredits,
		NewQuotaCredits: row.NewQuotaCredits, OldUnlimited: row.OldUnlimited,
		NewUnlimited: row.NewUnlimited, CreatedAt: isoString(row.CreatedAt),
		ActorEmail: stringPtrOrNil(row.ActorEmail),
	}
}

type providerPublic struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	BaseURL             string  `json:"baseUrl"`
	Protocol            string  `json:"protocol"`
	Enabled             bool    `json:"enabled"`
	DisabledMode        *string `json:"disabledMode"`
	AutoDisabledAt      *string `json:"autoDisabledAt"`
	AutoDisabledReason  *string `json:"autoDisabledReason"`
	ConsecutiveFailures int64   `json:"consecutiveFailures"`
	LastTestAt          *string `json:"lastTestAt"`
	LastTestOk          *bool   `json:"lastTestOk"`
	LastTestKind        *string `json:"lastTestKind"`
	LastTestStatus      *int64  `json:"lastTestStatus"`
	LastTestLatencyMs   *int64  `json:"lastTestLatencyMs"`
	LastTestModel       *string `json:"lastTestModel"`
	LastTestDetail      *string `json:"lastTestDetail"`
	APIKeyHint          *string `json:"apiKeyHint"`
	HasKey              bool    `json:"hasKey"`
	AnthropicVersion    *string `json:"anthropicVersion"`
	TimeoutMs           *int64  `json:"timeoutMs"`
	CreatedAt           string  `json:"createdAt"`
}

func toPublicProvider(record *store.UpstreamProviderRecord) providerPublic {
	out := providerPublic{
		ID: record.ID, Name: record.Name, BaseURL: record.BaseURL, Protocol: string(record.Protocol),
		Enabled: record.Enabled, ConsecutiveFailures: record.ConsecutiveFailures,
		LastTestOk:        record.LastTestOk,
		LastTestStatus:    record.LastTestStatus,
		LastTestLatencyMs: record.LastTestLatencyMs,
		APIKeyHint:        stringPtrOrNil(record.APIKeyHint),
		AnthropicVersion:  stringPtrOrNil(record.AnthropicVersion),
		TimeoutMs:         nil,
		CreatedAt:         isoString(record.CreatedAt),
	}
	if record.DisabledMode != "" {
		mode := record.DisabledMode
		out.DisabledMode = &mode
	}
	out.AutoDisabledAt = isoStringPtr(record.AutoDisabledAt)
	out.AutoDisabledReason = stringPtrOrNil(record.AutoDisabledReason)
	out.LastTestAt = isoStringPtr(record.LastTestAt)
	out.LastTestKind = stringPtrOrNil(record.LastTestKind)
	out.LastTestModel = stringPtrOrNil(record.LastTestModel)
	out.LastTestDetail = stringPtrOrNil(record.LastTestDetail)
	if record.TimeoutMs > 0 {
		timeout := record.TimeoutMs
		out.TimeoutMs = &timeout
	}
	out.HasKey = record.APIKeyCiphertext != ""
	return out
}

type devicePublic struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Platform   *string `json:"platform"`
	Hint       string  `json:"hint"`
	UserEmail  string  `json:"userEmail"`
	CreatedAt  string  `json:"createdAt"`
	LastUsedAt *string `json:"lastUsedAt"`
	RevokedAt  *string `json:"revokedAt"`
}

type releaseSummary struct {
	Revision    int64  `json:"revision"`
	PublishedAt string `json:"publishedAt"`
	IsActive    bool   `json:"isActive"`
}
