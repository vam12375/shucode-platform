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
	// ConfigMode:recommended = 跟随官方内置规则；manual = 使用 ModelConfig 覆盖。
	ConfigMode string
	// ModelConfig:manual 模式下的客户端模型能力配置 JSON（properties/optionSpecs）。
	ModelConfig []byte
	Enabled     bool
	SortOrder   int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// UpstreamRouteRecord:模型的可调度上游路由（渠道池）。
type UpstreamRouteRecord struct {
	ID              string
	ModelID         string
	ProviderID      string
	ProviderName    string
	ProviderEnabled bool
	UpstreamModel   string
	Priority        int64
	Weight          int64
	Enabled         bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
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

// TeamCreditDebtLimitMicro is the finite team's maximum allowed credit overdraft.
const TeamCreditDebtLimitMicro int64 = 10_000

// ReserveResult:原子预留结果，调用方据此区分余额、预算、状态拒绝与重复请求。
type ReserveResult string

const (
	ReserveReserved       ReserveResult = "reserved"
	ReserveDenied         ReserveResult = "denied"
	ReserveDeniedBudget   ReserveResult = "denied_budget"
	ReserveDeniedInactive ReserveResult = "denied_inactive"
	ReserveDuplicate      ReserveResult = "duplicate"
)

type ReserveQuotaInput struct {
	Postpaid            bool
	ReserveAllAvailable bool
	RetainUntilSettled  bool
	HeldByTask          bool
	UserID              string
	ID                  string
	TeamID              string
	RequestID           string
	Micro               int64
	TTL                 time.Duration
	CreatedAt           time.Time
}

type QuotaReservationRecord struct {
	Postpaid           bool
	RetainUntilSettled bool
	HeldByTask         bool
	UserID             string
	ID                 string
	TeamID             string
	RequestID          string
	ReservedMicro      int64
	Status             QuotaReservationStatus
	CreatedAt          time.Time
	ExpiresAt          time.Time
	SettledAt          *time.Time
	ReleasedAt         *time.Time
	UsageID            string
}

type UsageChargeRates struct {
	InputPerMtok      float64 `json:"inputPerMtok"`
	OutputPerMtok     float64 `json:"outputPerMtok"`
	CacheReadPerMtok  float64 `json:"cacheReadPerMtok"`
	CacheWritePerMtok float64 `json:"cacheWritePerMtok"`
}

// BackgroundGenerationJob keeps the reservation owner and billing snapshot until an upstream Responses task is terminal.
type BackgroundGenerationJob struct {
	ReservationID   string           `json:"reservationId"`
	ResponseID      string           `json:"responseId"`
	ProviderID      string           `json:"providerId"`
	BaseURL         string           `json:"baseUrl,omitempty"`
	TeamID          string           `json:"teamId"`
	UserID          string           `json:"userId"`
	Record          UsageRecord      `json:"record"`
	UsageObservedAt *time.Time       `json:"usageObservedAt,omitempty"`
	Rates           UsageChargeRates `json:"rates"`
	Limited         bool             `json:"limited"`
	NextPollAt      time.Time        `json:"nextPollAt"`
	Attempts        int64            `json:"attempts"`
	LastError       string           `json:"lastError,omitempty"`
	CreatedAt       time.Time        `json:"createdAt"`
	FinalResponse   []byte           `json:"finalResponse,omitempty"`
	FinalStatus     int              `json:"finalStatus,omitempty"`
	FinalType       string           `json:"finalContentType,omitempty"`
	CompletedAt     *time.Time       `json:"completedAt,omitempty"`
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
	// PendingSettlements:结算失败后等待补偿的请求数（含已转人工的 failed）。
	PendingSettlements int64
	// PendingAuditLogs:审计 outbox 中尚未入库的事件数（由 api 层填充，非存储查询）。
	PendingAuditLogs int64
	ProviderStats    []OpsProviderStat
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
	ID                    string `json:"id"`
	RequestID             string `json:"requestId"`
	UserID                string `json:"userId"`
	TeamID                string `json:"teamId"`
	DeviceTokenID         string `json:"deviceTokenId,omitempty"`
	Model                 string `json:"model"`
	UpstreamModel         string `json:"upstreamModel"`
	ProviderID            string `json:"providerId,omitempty"`
	ReasoningEffort       string `json:"reasoningEffort,omitempty"`
	ReasoningBudgetTokens *int64 `json:"reasoningBudgetTokens,omitempty"`
	// 后台受理核对保留已取得的供应商结果 ID；只随内部补偿 JSON 保存。
	BackgroundResponseID string                `json:"backgroundResponseId,omitempty"`
	InputTokens          int64                 `json:"inputTokens"`
	OutputTokens         int64                 `json:"outputTokens"`
	CacheReadTokens      int64                 `json:"cacheReadTokens"`
	CacheCreationTokens  int64                 `json:"cacheCreationTokens"`
	CreditsMicro         int64                 `json:"creditsMicro"`
	PriceSnapshot        *UsagePricingSnapshot `json:"priceSnapshot,omitempty"`
	Status               UsageStatus           `json:"status"`
	ErrorType            string                `json:"errorType,omitempty"`
	ClientIP             string                `json:"clientIp,omitempty"`
	LatencyMs            *int64                `json:"latencyMs,omitempty"`
	CreatedAt            time.Time             `json:"createdAt"`
}

// PendingSettlementStatus:结算失败后的持久化补偿状态。
// pending 会按退避重试；超过上限后转 failed，等待人工对账。
type PendingSettlementStatus string

const (
	PendingSettlementPending PendingSettlementStatus = "pending"
	PendingSettlementApplied PendingSettlementStatus = "applied"
	PendingSettlementFailed  PendingSettlementStatus = "failed"
)

type PendingSettlementRecord struct {
	ReservationID string
	Record        UsageRecord
	Status        PendingSettlementStatus
	Attempts      int64
	LastError     string
	NextAttemptAt time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type PendingSettlementFilter struct {
	Status    PendingSettlementStatus // 空 = 全部状态
	DueBefore *time.Time              // 仅 next_attempt_at <= 该时刻
	Limit     int64
}

// GatewayAttemptRecord:一次上游尝试的轨迹（故障转移/对账用）。
type GatewayAttemptRecord struct {
	RequestID         string
	ReservationID     string
	Attempt           int64
	ProviderID        string
	UpstreamModel     string
	BaseHost          string
	StatusCode        *int
	ErrorType         string
	ProviderRequestID string
	StartedAt         time.Time
	DurationMs        int64
	Final             bool
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
	Name               *string
	QuotaCredits       *string
	QuotaIncreaseMicro *int64 // 仅 UpdateTeamWithAccounting 支持：锁内累加而非用客户端旧快照覆盖。
	Unlimited          *bool
	RPMLimit           *int64
	TPMLimit           *int64
	ConcurrencyLimit   *int64
	HasDisabled        bool
	DisabledAt         *time.Time
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
	Models     []string // nil 不筛选；非 nil 同时匹配所列实际账本模型。
	ProviderID string
	Status     UsageStatus
	RequestID  string
	Limit      *int64 // listUsage 专用，默认 50；公开页最多 500，内部含下一页探测最多 501。
	// 游标 (created_at, id) 元组:取严格更旧于该行的记录,两字段必须同时设置。
	CursorCreatedAt *time.Time
	CursorID        string
}

type AuditLogFilter struct {
	TeamID          string
	Limit           *int64 // 默认 50；公开页最多 500，内部含下一页探测最多 501。
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
	// EventID 用于可靠 outbox 重放去重；旧调用方可留空。
	EventID     string `json:"eventId"`
	TeamID      string `json:"teamId,omitempty"`
	ActorUserID string `json:"actorUserId,omitempty"`
	Action      string `json:"action"`
	TargetType  string `json:"targetType,omitempty"`
	TargetID    string `json:"targetId,omitempty"`
	Detail      []byte `json:"detail,omitempty"`
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

// UsageCostGroup:按模型/币种汇总的冻结成本快照。
type UsageCostGroup struct {
	Model                  string
	Currency               string
	Requests               int64
	CostMicro              int64
	ChargedMicro           int64
	EstimatedUsageRequests int64
}

type UsageCostTotal struct {
	Currency               string
	Requests               int64
	CostMicro              int64
	ChargedMicro           int64
	EstimatedUsageRequests int64
}

// UsageCostSummary:筛选范围内的完整成本汇总；MissingCostRequests 包含旧记录与未知成本。
type UsageCostSummary struct {
	Groups                 []UsageCostGroup
	Totals                 []UsageCostTotal
	EstimatedRequests      int64
	MissingCostRequests    int64
	EstimatedUsageRequests int64
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
