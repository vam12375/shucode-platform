// Package store:平台数据层端口与双实现(packages/db/src 的 Go 镜像)。
// Postgres 与内存实现必须同语义,由 conformance 测试共同夹住;
// 尤其是配额临界区:预留原子、结算同事务、失败释放,数值一律 int64 micro。
package store

import (
	"time"

	"github.com/vam12375/shucode-platform/server/internal/pricing"
)

type UserRole string

const (
	// RoleRoot:全库唯一的超级管理员，只能由首次初始化创建，不能通过用户管理接口授予。
	RoleRoot  UserRole = "root"
	RoleAdmin UserRole = "admin"
	RoleUser  UserRole = "user"
)

type TeamMemberRole string

const (
	MemberRoleOwner  TeamMemberRole = "owner"
	MemberRoleAdmin  TeamMemberRole = "admin"
	MemberRoleMember TeamMemberRole = "member"
)

type LoginFlowStatus string

const (
	FlowStatusPending    LoginFlowStatus = "pending"
	FlowStatusAuthorized LoginFlowStatus = "authorized"
	FlowStatusConsumed   LoginFlowStatus = "consumed"
	FlowStatusExpired    LoginFlowStatus = "expired"
	FlowStatusDenied     LoginFlowStatus = "denied"
)

type UsageStatus string

const (
	UsageStatusOk      UsageStatus = "ok"
	UsageStatusError   UsageStatus = "error"
	UsageStatusAborted UsageStatus = "aborted"
)

// UpstreamProtocol 上游供应商转发协议(阶段一同构透传)。
type UpstreamProtocol string

const (
	ProtocolOpenAIChatCompletions UpstreamProtocol = "openai-chat-completions"
	ProtocolOpenAIResponses       UpstreamProtocol = "openai-responses"
	ProtocolAnthropicMessages     UpstreamProtocol = "anthropic-messages"
)

type UpstreamProviderRecord struct {
	ID                  string
	Name                string
	BaseURL             string
	Protocol            UpstreamProtocol
	APIKeyCiphertext    string // AES-256-GCM 密文;控制台只回显 hint,明文不出库。
	APIKeyHint          string
	AnthropicVersion    string
	TimeoutMs           int64 // 0 表示回落网关全局超时(env TK 由路由层换算)。
	Enabled             bool
	DisabledMode        string // "manual"|"auto"|""(空 = 启用中)。
	AutoDisabledAt      *time.Time
	AutoDisabledReason  string
	ConsecutiveFailures int64
	LastTestAt          *time.Time
	LastTestOk          *bool
	LastTestKind        string
	LastTestStatus      *int64
	LastTestLatencyMs   *int64
	LastTestModel       string
	LastTestDetail      string
	CreatedAt           time.Time
}

type UserRecord struct {
	ID           string
	Email        string
	PasswordHash string
	DisplayName  string
	Role         UserRole
	DisabledAt   *time.Time
	CreatedAt    time.Time
}

type SessionRecord struct {
	SecurityVersion string
	TokenHash       string
	UserID          string
	CreatedAt       time.Time
	ExpiresAt       time.Time
	LastSeenAt      time.Time
}

type TeamRecord struct {
	ID              string
	Name            string
	QuotaCredits    string // numeric(18,6) 定点字符串。
	UsedCredits     string
	ReservedCredits string
	Unlimited       bool
	// 团队级流控：0 = 不限制。RPM/TPM 按分钟窗口，concurrency 按在途请求数。
	RPMLimit         int64
	TPMLimit         int64
	ConcurrencyLimit int64
	CreatedAt        time.Time
	DisabledAt       *time.Time
}

type TeamMemberView struct {
	MemberDisabledAt     *time.Time
	MonthlyBudgetCredits string
	User                 UserRecord
	Role                 TeamMemberRole
	CreatedAt            time.Time
}

type TeamWithUsage struct {
	Team        TeamRecord
	MemberCount int64
}

type DeviceTokenRecord struct {
	ID              string
	UserID          string
	Name            string
	Platform        string
	TokenHash       string
	TokenCiphertext string
	TokenHint       string
	CreatedAt       time.Time
	LastUsedAt      *time.Time
	RevokedAt       *time.Time
}

// Expired:ttl<=0 表示不按年龄过期（仍可由吊销/禁用即时失效）。
func (r DeviceTokenRecord) Expired(now time.Time, ttl time.Duration) bool {
	return ttl > 0 && !r.CreatedAt.Add(ttl).After(now)
}

type LoginFlowRecord struct {
	ID             string
	PollTokenHash  string
	Status         LoginFlowStatus
	UserID         string
	DeviceName     string
	DevicePlatform string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	AuthorizedAt   *time.Time
	ConsumedAt     *time.Time
	TokenID        string
	LastPolledAt   *time.Time
}

type ReleaseRecord struct {
	Revision    int64
	Content     []byte // jsonb 原始 JSON。
	PublishedAt time.Time
	PublishedBy string
	IsActive    bool
}

type PlatformModelRecord struct {
	ModelID                string
	DisplayName            string
	UpstreamModel          string
	UpstreamProviderID     string
	InputPricePerMtok      string
	OutputPricePerMtok     string
	CacheReadPricePerMtok  string
	CacheWritePricePerMtok string
	// OffPeakMultiplier:空闲时段价格倍率（1 = 无折扣；DeepSeek 0.5）。
	OffPeakMultiplier string
	Enabled           bool
	SortOrder         int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// UpstreamRouteRecord:模型的可调度上游路由（渠道池）。
type UpstreamRouteRecord struct {
	ID            string
	ModelID       string
	ProviderID    string
	ProviderName  string
	UpstreamModel string
	Priority      int64
	Weight        int64
	Enabled       bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type CreateUpstreamRouteInput struct {
	ModelID       string
	ProviderID    string
	UpstreamModel string
	Priority      int64
	Weight        int64
	Enabled       bool
}

type UpdateUpstreamRoutePatch struct {
	UpstreamModel *string
	Priority      *int64
	Weight        *int64
	Enabled       *bool
}

// QuotaReservationStatus:预留状态机。settled 后不可重复结算。
type QuotaReservationStatus string

const (
	ReservationReserved QuotaReservationStatus = "reserved"
	ReservationSettled  QuotaReservationStatus = "settled"
	ReservationReleased QuotaReservationStatus = "released"
	ReservationExpired  QuotaReservationStatus = "expired"
)

// ReserveResult:原子预留的三种结果，调用方据此区分 429 / 409。
type ReserveResult string

const (
	ReserveReserved  ReserveResult = "reserved"
	ReserveDenied    ReserveResult = "denied"
	ReserveDuplicate ReserveResult = "duplicate"
)

type ReserveQuotaInput struct {
	UserID    string
	ID        string
	TeamID    string
	RequestID string
	Micro     int64
	TTL       time.Duration
	CreatedAt time.Time
}

type QuotaReservationRecord struct {
	UserID        string
	ID            string
	TeamID        string
	RequestID     string
	ReservedMicro int64
	Status        QuotaReservationStatus
	CreatedAt     time.Time
	ExpiresAt     time.Time
	SettledAt     *time.Time
	ReleasedAt    *time.Time
	UsageID       string
}

// OpsProviderStat:运维大盘的供应商维度。
type OpsProviderStat struct {
	ProviderID   string
	ProviderName string
	Requests     int64
	Errors       int64
	ErrorRate    float64
	P95LatencyMs *float64
}

type OpsOverview struct {
	Requests            int64
	SuccessRequests     int64
	ErrorRequests       int64
	AbortedRequests     int64
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	CreditsMicro        int64
	P50LatencyMs        *float64
	P95LatencyMs        *float64
	P99LatencyMs        *float64
	ActiveDevices       int64
	OpenReservations    int64
	ProviderStats       []OpsProviderStat
}

type OpsSeriesPoint struct {
	BucketStart  string
	Requests     int64
	Errors       int64
	CreditsMicro int64
	P95LatencyMs *float64
}

type AlertRuleRecord struct {
	ID            string
	Name          string
	Metric        string
	Threshold     string // numeric(18,6)
	WindowMinutes int64
	Enabled       bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type UpsertAlertRuleInput struct {
	ID            string
	Name          string
	Metric        string
	Threshold     string
	WindowMinutes int64
	Enabled       bool
}

type AlertEventRecord struct {
	ID         string
	RuleID     string
	RuleName   string
	Metric     string
	Value      string
	Threshold  string
	Message    string
	Status     string
	CreatedAt  time.Time
	ResolvedAt *time.Time
}

type UsageRecord struct {
	ID                  string
	RequestID           string
	UserID              string
	TeamID              string
	DeviceTokenID       string
	Model               string
	UpstreamModel       string
	ProviderID          string
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	CreditsMicro        int64
	PriceSnapshot       *UsagePricingSnapshot
	Status              UsageStatus
	ErrorType           string
	ClientIP            string
	LatencyMs           *int64
	CreatedAt           time.Time
}

type ProviderTestOutcome struct {
	OK        bool
	Kind      string
	Status    *int64
	LatencyMs int64
	Model     string
	Detail    string
}

type UserInput struct {
	Email        string
	PasswordHash string
	DisplayName  string
	Role         UserRole
}

type CreateTeamInput struct {
	Name         string
	QuotaCredits string
	Unlimited    bool
}

type UpdateUserPatch struct {
	// nil=缺省保持;DisabledAtPtr 为区分「缺省/置 null」的指针语义传入 HasDisabled。
	HasDisabled  bool
	DisabledAt   *time.Time
	DisplayName  *string
	PasswordHash *string
	Role         *UserRole
}

type UpdateTeamPatch struct {
	Name             *string
	QuotaCredits     *string
	Unlimited        *bool
	RPMLimit         *int64
	TPMLimit         *int64
	ConcurrencyLimit *int64
	HasDisabled      bool
	DisabledAt       *time.Time
}

type UpdateLoginFlowPatch struct {
	Status       *LoginFlowStatus
	UserID       *string
	AuthorizedAt *time.Time
	ConsumedAt   *time.Time
	TokenID      *string
	LastPolledAt *time.Time
}

type InsertReleaseInput struct {
	Revision    int64
	Content     []byte
	PublishedBy string
}

type UpsertModelInput struct {
	ModelID     string
	DisplayName string
	// 绑定字段「缺省=保持、显式 null=清除」:ProvideXxx 参照 hasOwnProperty。
	ProvideUpstreamModel      bool
	UpstreamModel             string
	ProvideUpstreamProviderID bool
	UpstreamProviderID        string
	InputPricePerMtok         string
	OutputPricePerMtok        string
	CacheReadPricePerMtok     *string
	CacheWritePricePerMtok    *string
	OffPeakMultiplier         *string
	Enabled                   *bool
	SortOrder                 *int64
}

type CreateUpstreamProviderInput struct {
	Name             string
	BaseURL          string
	Protocol         UpstreamProtocol
	APIKeyCiphertext string
	APIKeyHint       string
	AnthropicVersion *string
	TimeoutMs        *int64
	Enabled          *bool
}

type UpdateUpstreamProviderPatch struct {
	Name     *string
	BaseURL  *string
	Protocol *UpstreamProtocol
	//apiKey 两字段「缺省=保持、显式 null=清除」(成对更新)。
	ProvideAPIKey           bool
	APIKeyCiphertext        string
	APIKeyHint              string
	ProvideAnthropicVersion bool
	AnthropicVersion        *string // 指针 nil=显式清除。
	ProvideTimeoutMs        bool
	TimeoutMs               *int64
	Enabled                 *bool
}

type UsageFilter struct {
	From       *time.Time
	To         *time.Time
	TeamID     string
	UserID     string
	Model      string
	ProviderID string
	Status     UsageStatus
	RequestID  string
	Limit      *int64 // listUsage 专用,默认 50 最大 500。
	// 游标 (created_at, id) 元组:取严格更旧于该行的记录,两字段必须同时设置。
	CursorCreatedAt *time.Time
	CursorID        string
}

type AuditLogFilter struct {
	TeamID          string
	Limit           *int64 // 默认 50 最大 500。
	CursorCreatedAt *time.Time
	CursorID        string
	ActorUserID     string
}

type AdminAuditLogView struct {
	TeamID      string
	ID          string
	ActorUserID string
	Action      string
	TargetType  string
	TargetID    string
	Detail      []byte
	CreatedAt   time.Time
	ActorEmail  string
}

type TeamQuotaAdjustmentView struct {
	ID              string
	Action          string
	OldQuotaCredits string
	NewQuotaCredits string
	OldUnlimited    bool
	NewUnlimited    bool
	CreatedAt       time.Time
	ActorEmail      string
}

type InsertAuditLogInput struct {
	TeamID      string
	ActorUserID string
	Action      string
	TargetType  string
	TargetID    string
	Detail      []byte
}

type InsertQuotaAdjustmentInput struct {
	TeamID          string
	ActorUserID     string
	Action          string
	OldQuotaCredits string
	NewQuotaCredits string
	OldUnlimited    bool
	NewUnlimited    bool
}

type UsageAggregateRow struct {
	Model               string
	Requests            int64
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	CreditsMicro        int64
	LatencyMsSum        int64
}

type UsageSeriesGranularity string

const (
	SeriesGranularityDay  UsageSeriesGranularity = "day"
	SeriesGranularityHour UsageSeriesGranularity = "hour"
)

// UsageSeriesRow:bucketStart 为上海墙钟 naive 串(天 "2006-01-02T00:00:00"、
// 小时 "…THH:00:00"),不带时区后缀。
type UsageSeriesRow struct {
	BucketStart         string
	Requests            int64
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	CreditsMicro        int64
	LatencyMsSum        int64
}

// PricingDocSnapshot 复用 internal/pricing 的快照定义。
type PricingDocSnapshot = pricing.Snapshot
type PricingEntry = pricing.PricingEntry

// TeamMemberPatch 只改变团队成员关系，不改变全局账号或其他团队的访问。
type TeamMemberPatch struct {
	Role                 *TeamMemberRole
	HasDisabled          bool
	DisabledAt           *time.Time
	MonthlyBudgetCredits *string
}

type TeamPolicyRecord struct {
	TeamID               string    `json:"teamId"`
	DailyBudgetCredits   string    `json:"dailyBudgetCredits"`
	MonthlyBudgetCredits string    `json:"monthlyBudgetCredits"`
	AllowedModelIDs      []string  `json:"allowedModelIds"`
	BudgetAlertPercent   int64     `json:"budgetAlertPercent"`
	RPMLimit             int64     `json:"rpmLimit"`
	TPMLimit             int64     `json:"tpmLimit"`
	ConcurrencyLimit     int64     `json:"concurrencyLimit"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

type TeamInvitationRecord struct {
	ID         string
	TeamID     string
	Email      string
	TokenHash  string
	CreatedBy  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	AcceptedAt *time.Time
	RevokedAt  *time.Time
}

func IsTeamAdmin(role TeamMemberRole) bool {
	return role == MemberRoleOwner || role == MemberRoleAdmin
}
