package store

import (
	"context"
	"errors"
	"time"
)

// 与 ports.ts 的 PlatformStore 一一对应的存储端口。
// api/gateway 只依赖本接口;PG 与内存实现由 conformance 测试夹紧同语义。
type PlatformStore interface {
	// ---- users ----
	FindUserByEmail(ctx context.Context, email string) (*UserRecord, error)
	FindUserByID(ctx context.Context, id string) (*UserRecord, error)
	// FindRootUser:返回唯一超级管理员；未初始化时为 nil。
	FindRootUser(ctx context.Context) (*UserRecord, error)
	CreateUser(ctx context.Context, input UserInput) (*UserRecord, error)
	ListUsers(ctx context.Context) ([]UserRecord, error)
	// UpdateUser:patch 语义与 TS 完全一致——未提供的字段保持原值;
	// HasDisabled=true 且 DisabledAt=nil 表示显式启用。
	UpdateUser(ctx context.Context, id string, patch UpdateUserPatch) (*UserRecord, error)

	// ---- sessions ----
	CreateSession(ctx context.Context, record SessionRecord) error
	FindSession(ctx context.Context, tokenHash string) (*SessionRecord, error)
	TouchSession(ctx context.Context, tokenHash string, at time.Time) error
	DeleteSession(ctx context.Context, tokenHash string) error
	DeleteSessionsForUser(ctx context.Context, userID string) error

	// ---- teams ----
	CreateTeam(ctx context.Context, input CreateTeamInput) (*TeamRecord, error)
	GetTeam(ctx context.Context, id string) (*TeamRecord, error)
	ListTeams(ctx context.Context) ([]TeamWithUsage, error)
	UpdateTeam(ctx context.Context, id string, patch UpdateTeamPatch) (*TeamRecord, error)
	ListMembers(ctx context.Context, teamID string) ([]TeamMemberView, error)
	GetTeamMember(ctx context.Context, teamID, userID string) (*TeamMemberView, error)
	UpdateTeamMember(ctx context.Context, teamID, userID string, patch TeamMemberPatch) (*TeamMemberView, error)
	GetTeamPolicy(ctx context.Context, teamID string) (*TeamPolicyRecord, error)
	SaveTeamPolicy(ctx context.Context, record TeamPolicyRecord) (*TeamPolicyRecord, error)
	CreateTeamInvitation(ctx context.Context, record TeamInvitationRecord) error
	FindTeamInvitationByHash(ctx context.Context, tokenHash string) (*TeamInvitationRecord, error)
	ListTeamInvitations(ctx context.Context, teamID string) ([]TeamInvitationRecord, error)
	RevokeTeamInvitation(ctx context.Context, teamID, id string, at time.Time) (bool, error)
	AcceptTeamInvitation(ctx context.Context, tokenHash, userID string, at time.Time) (*TeamInvitationRecord, error)
	AddMember(ctx context.Context, teamID, userID string, role TeamMemberRole) error
	RemoveMember(ctx context.Context, teamID, userID string) (bool, error)
	ListTeamsForUser(ctx context.Context, userID string) ([]TeamMembership, error)
	// ListAllMemberships:跨团队列出全部成员关系(展平含团队名)，按加入时间升序。
	// 供控制台账号列表一次性注入归属，避免按用户逐一查询。
	ListAllMemberships(ctx context.Context) ([]AllMembershipRow, error)

	// ---- manual quota requests ----
	CreateQuotaRequest(ctx context.Context, record QuotaRequestRecord) error
	ListQuotaRequests(ctx context.Context, teamID string) ([]QuotaRequestRecord, error)
	ReviewQuotaRequest(ctx context.Context, id, reviewerID, decision, note string, at time.Time) (*QuotaRequestRecord, error)

	// ---- device tokens ----
	CreateDeviceToken(ctx context.Context, record DeviceTokenRecord) error
	FindDeviceTokenByHash(ctx context.Context, tokenHash string) (*DeviceTokenRecord, error)
	FindDeviceTokenByID(ctx context.Context, id string) (*DeviceTokenRecord, error)
	ListDeviceTokens(ctx context.Context, userID string) ([]DeviceTokenRecord, error)
	TouchDeviceToken(ctx context.Context, id string, at time.Time) error
	RevokeDeviceToken(ctx context.Context, id string, at time.Time) error
	// 吊销用户全部设备凭据,返回本次新吊销条数;已吊销保持原 revoked_at。
	RevokeDeviceTokensForUser(ctx context.Context, userID string, at time.Time) (int64, error)

	// ---- login flows ----
	CreateLoginFlow(ctx context.Context, record LoginFlowRecord) error
	GetLoginFlow(ctx context.Context, id string) (*LoginFlowRecord, error)
	UpdateLoginFlow(ctx context.Context, id string, patch UpdateLoginFlowPatch) (*LoginFlowRecord, error)
	// UpdateLoginFlowIfStatus:仅当前状态等于 expected 时应用 patch。
	// 授权/拒绝/过期都走条件更新，避免旧请求把 consumed 回写成 authorized。
	UpdateLoginFlowIfStatus(ctx context.Context, id string, expected LoginFlowStatus, patch UpdateLoginFlowPatch) (*LoginFlowRecord, error)
	// 条件消费:仅 status=authorized 时置 consumed 并返回——并发轮询只签发一次。
	ConsumeLoginFlow(ctx context.Context, id string, consumedAt time.Time) (*LoginFlowRecord, error)
	// DeleteExpiredLoginFlows:清理已过期的登录流，返回删除条数。
	DeleteExpiredLoginFlows(ctx context.Context, before time.Time) (int64, error)

	// ---- releases ----
	InsertRelease(ctx context.Context, input InsertReleaseInput) (*ReleaseRecord, error)
	ListReleases(ctx context.Context) ([]ReleaseRecord, error)
	GetRelease(ctx context.Context, revision int64) (*ReleaseRecord, error)
	GetActiveRelease(ctx context.Context) (*ReleaseRecord, error)
	ActivateRelease(ctx context.Context, revision int64) (*ReleaseRecord, error)
	GetMaxReleaseRevision(ctx context.Context) (*int64, error)

	// ---- models ----
	ListModels(ctx context.Context, includeDisabled bool) ([]PlatformModelRecord, error)
	GetModel(ctx context.Context, modelID string) (*PlatformModelRecord, error)
	UpsertModel(ctx context.Context, input UpsertModelInput) (*PlatformModelRecord, error)
	DeleteModel(ctx context.Context, modelID string) (bool, error)

	// ---- upstream providers ----
	ListUpstreamProviders(ctx context.Context, includeDisabled bool) ([]UpstreamProviderRecord, error)
	GetUpstreamProvider(ctx context.Context, id string) (*UpstreamProviderRecord, error)
	CreateUpstreamProvider(ctx context.Context, input CreateUpstreamProviderInput) (*UpstreamProviderRecord, error)
	UpdateUpstreamProvider(ctx context.Context, id string, patch UpdateUpstreamProviderPatch) (*UpstreamProviderRecord, error)
	DeleteUpstreamProvider(ctx context.Context, id string) (bool, error)
	// 控制台测试结果原样落库(密钥不落)。
	RecordProviderTestResult(ctx context.Context, id string, result ProviderTestOutcome) error
	// 网关失败熔断:并发安全地 +1 计数,达到阈值(>0)原子转 auto 停用;
	// 只作用于 enabled 行,返回本次是否触发停用。
	ReportUpstreamFailure(ctx context.Context, id, reason string, threshold int64) (bool, error)
	ReportUpstreamSuccess(ctx context.Context, id string) error
	// RecoverUpstreamProvider:仅自动熔断的供应商可自动恢复；手动停用保持停用。
	RecoverUpstreamProvider(ctx context.Context, id string, at time.Time) (bool, error)

	// ---- upstream routes(渠道池) ----
	ListUpstreamRoutes(ctx context.Context, modelID string, includeDisabled bool) ([]UpstreamRouteRecord, error)
	CreateUpstreamRoute(ctx context.Context, input CreateUpstreamRouteInput) (*UpstreamRouteRecord, error)
	UpdateUpstreamRoute(ctx context.Context, id string, patch UpdateUpstreamRoutePatch) (*UpstreamRouteRecord, error)
	DeleteUpstreamRoute(ctx context.Context, id string) (bool, error)

	// ---- model pricing(内容版本乐观锁,KV 承载) ----
	GetModelPricing(ctx context.Context) (*PricingDocSnapshot, error)
	SaveModelPricing(ctx context.Context, entries []PricingEntry, expectedVersion string) (*PricingDocSnapshot, error)

	// ---- admin audit ----
	InsertAuditLog(ctx context.Context, entry InsertAuditLogInput) error
	ListAuditLogs(ctx context.Context, filter AuditLogFilter) ([]AdminAuditLogView, error)
	InsertQuotaAdjustment(ctx context.Context, entry InsertQuotaAdjustmentInput) error
	ListQuotaAdjustments(ctx context.Context, teamID string, limit int64) ([]TeamQuotaAdjustmentView, error)

	// ---- config kv ----
	GetConfigValue(ctx context.Context, key string) ([]byte, error)
	SetConfigValue(ctx context.Context, key string, value []byte) error
	CompareAndSwapConfigValue(ctx context.Context, key string, expected, value []byte) (bool, error)

	// ---- usage ----
	InsertUsage(ctx context.Context, record UsageRecord) error
	ListUsage(ctx context.Context, filter UsageFilter) ([]UsageRecord, error)
	// ListUsageForExport:导出专用，允许超过列表默认 500 条上限。
	ListUsageForExport(ctx context.Context, filter UsageFilter, max int64) ([]UsageRecord, error)
	AggregateUsage(ctx context.Context, filter UsageFilter) ([]UsageAggregateRow, error)
	// 时间桶按上海墙钟(UTC+8)切分;空桶不返回,按桶起点升序。
	// 无维度过滤时优先读小时 rollup，带维度过滤时回退明细。
	AggregateUsageSeries(ctx context.Context, filter UsageFilter, granularity UsageSeriesGranularity) ([]UsageSeriesRow, error)

	// ---- quota(Gateway 临界区) ----
	// 原子「检查 + 预留 + 幂等占位」:request_id 重复返回 duplicate。
	ReserveQuota(ctx context.Context, input ReserveQuotaInput) (ReserveResult, error)
	// SettleReservation:同事务完成预留状态、团队额度与 usage 落库；重复结算幂等。
	SettleReservation(ctx context.Context, reservationID string, record UsageRecord) error
	// ReleaseReservationByID:仅 reserved 状态可释放，释放团队预留。
	ReleaseReservationByID(ctx context.Context, reservationID string, at time.Time) error
	// ExpireStaleReservations:回收过期预留，返回本次回收条数。
	ExpireStaleReservations(ctx context.Context, before time.Time) (int64, error)
	ListOpenReservations(ctx context.Context, limit int64) ([]QuotaReservationRecord, error)

	// ---- ops / alerts ----
	OpsOverview(ctx context.Context, since time.Time) (*OpsOverview, error)
	OpsSeries(ctx context.Context, since time.Time, bucketMinutes int64) ([]OpsSeriesPoint, error)
	ListAlertRules(ctx context.Context) ([]AlertRuleRecord, error)
	UpsertAlertRule(ctx context.Context, input UpsertAlertRuleInput) (*AlertRuleRecord, error)
	DeleteAlertRule(ctx context.Context, id string) (bool, error)
	OpenAlertEventForRule(ctx context.Context, ruleID string) (*AlertEventRecord, error)
	InsertAlertEvent(ctx context.Context, rule AlertRuleRecord, value, message string, at time.Time) (*AlertEventRecord, error)
	ResolveAlertEvent(ctx context.Context, id string, at time.Time) (bool, error)
	ResolveOpenAlertEventsForRule(ctx context.Context, ruleID string, at time.Time) (int64, error)
	ListAlertEvents(ctx context.Context, status string, limit int64) ([]AlertEventRecord, error)

	// ---- legacy quota helpers(旧调用方/测试保留) ----
	// 原子「检查 + 预留」:disabled_at 为空且(unlimited 或配额足够)时加入预留。
	TryReserveQuota(ctx context.Context, teamID string, micro int64) (bool, error)
	// 结算与账本写入必须同事务:预留转实扣 + usage 落库。
	SettleUsage(ctx context.Context, record UsageRecord, reservedMicro int64) error
	ReleaseReservation(ctx context.Context, teamID string, micro int64) error

	Dispose(ctx context.Context) error
}

// TeamMembership:listTeamsForUser 的返回行。
type TeamMembership struct {
	MemberDisabledAt     *time.Time
	MonthlyBudgetCredits string
	Team                 TeamRecord
	Role                 TeamMemberRole
}

// AllMembershipRow:ListAllMemberships 的展平行(成员关系 + 团队名，不含用户与团队全字段)。
type AllMembershipRow struct {
	TeamID               string
	TeamName             string
	UserID               string
	Role                 TeamMemberRole
	MonthlyBudgetCredits string
	MemberDisabledAt     *time.Time
	JoinedAt             time.Time
}

// ---- 语义错误(两实现都必须等价返回;路由层翻译为响应) ----

var (
	ErrUserAlreadyInTeam       = errors.New("用户已属于其他团队")
	ErrAmbiguousTeam           = errors.New("用户存在多个有效团队，请先修正团队归属")
	ErrLastTeamAdmin           = errors.New("不能移除、停用或降级最后一位团队管理员")
	ErrInvitationInvalid       = errors.New("邀请已失效、过期或被使用")
	ErrInvitationEmailMismatch = errors.New("邀请邮箱与账号邮箱不匹配")
	ErrEmailTaken              = errors.New("邮箱已存在")
	ErrTeamNameTaken           = errors.New("团队名已存在")
	ErrReleaseTaken            = errors.New("Release revision 已存在")
	ErrProviderNameTaken       = errors.New("供应商名已存在")
	ErrRootUserExists          = errors.New("超级管理员已存在")
	ErrRootUserProtected       = errors.New("超级管理员不能被禁用或降级")
)

// ResolveUserTeam 显式拒绝既存多团队歧义，避免按列表顺序错误扣费。
func ResolveUserTeam(ctx context.Context, st PlatformStore, userID string) (*TeamMembership, error) {
	memberships, err := st.ListTeamsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	var selected *TeamMembership
	for _, membership := range memberships {
		if membership.Team.DisabledAt != nil || membership.MemberDisabledAt != nil {
			continue
		}
		if selected != nil {
			return nil, ErrAmbiguousTeam
		}
		copy := membership
		selected = &copy
	}
	return selected, nil
}
