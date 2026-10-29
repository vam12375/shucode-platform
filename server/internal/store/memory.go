package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/credits"
)

// shanghaiOffset:内存实现的 series 分桶按上海墙钟固定 UTC+8(无夏令时),
// 与 Postgres 实现的 AT TIME ZONE 'Asia/Shanghai' 对齐。
const shanghaiOffset = 8 * time.Hour

// MemoryOptions 对应 TS MemoryStoreOptions。
type MemoryOptions struct {
	// DemoSeed = 无 Postgres 的本地体验模式:预置管理员/成员/演示团队与两个模型。
	DemoSeed bool
}

// 内存实现:语义与 Postgres 实现一致,用于本地体验与测试。
// 与 TS 版的关键差异:所有读写经互斥锁,返回值一律深拷贝(PG 快照语义),
// 以规避 TS 内存实现「get* 返回原地引用」的别名陷阱(详见 AGENTS.md 已踩坑记录)。
type memoryStore struct {
	mu sync.Mutex

	users                 map[string]UserRecord
	sessions              map[string]SessionRecord
	teams                 map[string]TeamRecord
	teamAssets            map[string]map[string]TeamAssetRecord
	teamAssetBlobIDs      map[string]map[string]string
	teamAssetUsedBytes    map[string]int64
	teamAssetDeletions    map[string]map[string]TeamAssetDeletionRecord
	members               map[string]map[string]struct{} // teamID -> userIDs
	memberRoles           map[string]TeamMemberRole      // "teamId:userId"
	memberDisabled        map[string]*time.Time
	memberBudgets         map[string]string
	teamPolicies          map[string]TeamPolicyRecord
	invitations           map[string]TeamInvitationRecord
	quotaRequests         map[string]QuotaRequestRecord
	memberJoinedAt        map[string]time.Time
	deviceTokens          map[string]DeviceTokenRecord
	flows                 map[string]LoginFlowRecord
	releases              map[int64]ReleaseRecord
	models                map[string]PlatformModelRecord
	providers             map[string]UpstreamProviderRecord
	config                map[string][]byte
	agentMediaPreferences map[string]AgentMediaPreferencesRecord
	usage                 []UsageRecord
	auditLogs             []AdminAuditLogView
	auditEventIDs         map[string]struct{}
	quotaAdjusts          []quotaAdjustmentRow
	upstreamRoutes        map[string]UpstreamRouteRecord
	reservations          map[string]QuotaReservationRecord
	backgroundJobs        map[string]BackgroundGenerationJob
	pendingSettlements    map[string]PendingSettlementRecord
	gatewayAttempts       []GatewayAttemptRecord
	alertRules            map[string]AlertRuleRecord
	alertEvents           []AlertEventRecord
}

type quotaAdjustmentRow struct {
	TeamQuotaAdjustmentView
	TeamID      string
	ActorUserID string
}

func NewMemoryStore(ctx context.Context, opts MemoryOptions) (*memoryStore, error) {
	s := &memoryStore{
		users:                 map[string]UserRecord{},
		sessions:              map[string]SessionRecord{},
		teams:                 map[string]TeamRecord{},
		teamAssets:            map[string]map[string]TeamAssetRecord{},
		teamAssetBlobIDs:      map[string]map[string]string{},
		teamAssetUsedBytes:    map[string]int64{},
		teamAssetDeletions:    map[string]map[string]TeamAssetDeletionRecord{},
		members:               map[string]map[string]struct{}{},
		memberRoles:           map[string]TeamMemberRole{},
		memberJoinedAt:        map[string]time.Time{},
		memberDisabled:        map[string]*time.Time{},
		memberBudgets:         map[string]string{},
		teamPolicies:          map[string]TeamPolicyRecord{},
		invitations:           map[string]TeamInvitationRecord{},
		quotaRequests:         map[string]QuotaRequestRecord{},
		deviceTokens:          map[string]DeviceTokenRecord{},
		flows:                 map[string]LoginFlowRecord{},
		releases:              map[int64]ReleaseRecord{},
		models:                map[string]PlatformModelRecord{},
		providers:             map[string]UpstreamProviderRecord{},
		config:                map[string][]byte{},
		agentMediaPreferences: map[string]AgentMediaPreferencesRecord{},
		auditEventIDs:         map[string]struct{}{},
		upstreamRoutes:        map[string]UpstreamRouteRecord{},
		reservations:          map[string]QuotaReservationRecord{},
		backgroundJobs:        map[string]BackgroundGenerationJob{},
		pendingSettlements:    map[string]PendingSettlementRecord{},
		alertRules:            map[string]AlertRuleRecord{},
	}
	if opts.DemoSeed {
		if err := s.seedDemo(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// ---- users ----

func (s *memoryStore) FindUserByEmail(_ context.Context, email string) (*UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 与 Postgres SQL 一致:比较时 lower 两侧但不 trim(trim/降写只发生在建档时)。
	normalized := strings.ToLower(email)
	for _, user := range s.users {
		if strings.ToLower(user.Email) == normalized {
			return cloneUser(&user), nil
		}
	}
	return nil, nil
}

func (s *memoryStore) FindUserByID(_ context.Context, id string) (*UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if user, ok := s.users[id]; ok {
		return cloneUser(&user), nil
	}
	return nil, nil
}

func (s *memoryStore) FindRootUser(_ context.Context) (*UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, user := range s.users {
		if user.Role == RoleRoot {
			return cloneUser(&user), nil
		}
	}
	return nil, nil
}

func (s *memoryStore) CreateUser(_ context.Context, input UserInput) (*UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.Role == RoleRoot {
		for _, user := range s.users {
			if user.Role == RoleRoot {
				return nil, ErrRootUserExists
			}
		}
	}
	email := strings.ToLower(strings.TrimSpace(input.Email))
	for _, user := range s.users {
		if strings.ToLower(user.Email) == email {
			return nil, fmt.Errorf("%w: %s", ErrEmailTaken, email)
		}
	}
	record := UserRecord{
		ID:           uuid.NewString(),
		Email:        email,
		PasswordHash: input.PasswordHash,
		DisplayName:  input.DisplayName,
		Role:         input.Role,
		CreatedAt:    time.Now(),
	}
	s.users[record.ID] = record
	return cloneUser(&record), nil
}

func (s *memoryStore) ListUsers(_ context.Context) ([]UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]UserRecord, 0, len(s.users))
	for _, user := range s.users {
		out = append(out, *cloneUser(&user))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *memoryStore) UpdateUser(_ context.Context, id string, patch UpdateUserPatch) (*UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[id]
	if !ok {
		return nil, nil
	}
	effectiveRole := user.Role
	if patch.Role != nil {
		effectiveRole = *patch.Role
	}
	disabling := patch.HasDisabled && patch.DisabledAt != nil
	if user.Role == RoleRoot &&
		((patch.Role != nil && *patch.Role != RoleRoot) || disabling) {
		return nil, ErrRootUserProtected
	}
	if effectiveRole == RoleRoot && user.Role != RoleRoot {
		for otherID, other := range s.users {
			if otherID != id && other.Role == RoleRoot {
				return nil, ErrRootUserExists
			}
		}
	}
	if effectiveRole == RoleRoot && disabling {
		return nil, ErrRootUserProtected
	}
	// 全局停用同样会撤销团队管理能力，必须和成员停用共用最后管理员保护。
	if disabling {
		for teamID, members := range s.members {
			if _, exists := members[id]; exists {
				if err := s.protectAdminLocked(teamID, id); err != nil {
					return nil, err
				}
			}
		}
	}
	if patch.HasDisabled {
		user.DisabledAt = copyTime(patch.DisabledAt)
	}
	if patch.DisplayName != nil {
		user.DisplayName = *patch.DisplayName
	}
	if patch.PasswordHash != nil {
		user.PasswordHash = *patch.PasswordHash
	}
	if patch.Role != nil {
		user.Role = *patch.Role
	}
	s.users[id] = user
	return cloneUser(&user), nil
}

// ---- sessions ----

func (s *memoryStore) CreateSession(_ context.Context, record SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[record.TokenHash] = record
	return nil
}

func (s *memoryStore) FindSession(_ context.Context, tokenHash string) (*SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[tokenHash]; ok {
		return cloneSession(&session), nil
	}
	return nil, nil
}

func (s *memoryStore) TouchSession(_ context.Context, tokenHash string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[tokenHash]; ok {
		session.LastSeenAt = at
		s.sessions[tokenHash] = session
	}
	return nil
}

func (s *memoryStore) DeleteSession(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tokenHash)
	return nil
}

func (s *memoryStore) DeleteSessionsForUser(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tokenHash, session := range s.sessions {
		if session.UserID == userID {
			delete(s.sessions, tokenHash)
		}
	}
	return nil
}

// ---- teams ----

func (s *memoryStore) CreateTeam(_ context.Context, input CreateTeamInput) (*TeamRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := strings.TrimSpace(input.Name)
	for _, team := range s.teams {
		if team.Name == name {
			return nil, fmt.Errorf("%w: %s", ErrTeamNameTaken, name)
		}
	}
	record := TeamRecord{
		ID:              uuid.NewString(),
		Name:            name,
		QuotaCredits:    numeric6(input.QuotaCredits),
		UsedCredits:     "0.000000",
		ReservedCredits: "0.000000",
		Unlimited:       input.Unlimited,
		CreatedAt:       time.Now(),
	}
	s.teams[record.ID] = record
	s.members[record.ID] = map[string]struct{}{}
	return cloneTeam(&record), nil
}

func (s *memoryStore) GetTeam(_ context.Context, id string) (*TeamRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if team, ok := s.teams[id]; ok {
		return cloneTeam(&team), nil
	}
	return nil, nil
}

func (s *memoryStore) ListTeams(_ context.Context) ([]TeamWithUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TeamWithUsage, 0, len(s.teams))
	for _, team := range s.teams {
		count := int64(len(s.members[team.ID]))
		out = append(out, TeamWithUsage{Team: *cloneTeam(&team), MemberCount: count})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Team.CreatedAt.Before(out[j].Team.CreatedAt) })
	return out, nil
}

func applyTeamPatch(team *TeamRecord, patch UpdateTeamPatch) {
	if patch.Name != nil {
		team.Name = *patch.Name
	}
	if patch.QuotaCredits != nil {
		team.QuotaCredits = numeric6(*patch.QuotaCredits)
	}
	if patch.Unlimited != nil {
		team.Unlimited = *patch.Unlimited
	}
	if patch.RPMLimit != nil {
		team.RPMLimit = *patch.RPMLimit
	}
	if patch.TPMLimit != nil {
		team.TPMLimit = *patch.TPMLimit
	}
	if patch.ConcurrencyLimit != nil {
		team.ConcurrencyLimit = *patch.ConcurrencyLimit
	}
	if patch.HasDisabled {
		team.DisabledAt = copyTime(patch.DisabledAt)
	}
}

func (s *memoryStore) UpdateTeam(_ context.Context, id string, patch UpdateTeamPatch) (*TeamRecord, error) {
	if patch.QuotaIncreaseMicro != nil {
		return nil, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	team, ok := s.teams[id]
	if !ok {
		return nil, nil
	}
	applyTeamPatch(&team, patch)
	s.teams[id] = team
	return cloneTeam(&team), nil
}

func (s *memoryStore) UpdateTeamWithAccounting(_ context.Context, id string, patch UpdateTeamPatch, adjustmentAction string, audit *InsertAuditLogInput) (*TeamRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	team, ok := s.teams[id]
	if !ok {
		return nil, nil
	}
	oldQuota, oldUnlimited := team.QuotaCredits, team.Unlimited
	if patch.QuotaIncreaseMicro != nil && audit != nil && audit.EventID != "" {
		if _, exists := s.auditEventIDs[audit.EventID]; exists {
			return cloneTeam(&team), nil
		}
	}
	patch, err := prepareTeamCreditIncrease(oldQuota, patch, audit)
	if err != nil {
		return nil, err
	}
	applyTeamPatch(&team, patch)
	s.teams[id] = team
	if adjustmentAction != "" {
		actorID := ""
		if audit != nil {
			actorID = audit.ActorUserID
		}
		s.quotaAdjusts = append(s.quotaAdjusts, quotaAdjustmentRow{
			TeamQuotaAdjustmentView: TeamQuotaAdjustmentView{
				ID:              uuid.NewString(),
				Action:          adjustmentAction,
				OldQuotaCredits: oldQuota,
				NewQuotaCredits: team.QuotaCredits,
				OldUnlimited:    oldUnlimited,
				NewUnlimited:    team.Unlimited,
				CreatedAt:       time.Now(),
			},
			TeamID:      id,
			ActorUserID: actorID,
		})
	}
	auditDuplicate := false
	if audit != nil && audit.EventID != "" {
		_, auditDuplicate = s.auditEventIDs[audit.EventID]
	}
	if audit != nil && !auditDuplicate {
		if audit.EventID != "" {
			s.auditEventIDs[audit.EventID] = struct{}{}
		}
		s.auditLogs = append(s.auditLogs, AdminAuditLogView{
			ID:          uuid.NewString(),
			ActorUserID: audit.ActorUserID,
			TeamID:      audit.TeamID,
			Action:      audit.Action,
			TargetType:  audit.TargetType,
			TargetID:    audit.TargetID,
			Detail:      append([]byte(nil), audit.Detail...),
			CreatedAt:   time.Now(),
		})
	}
	return cloneTeam(&team), nil
}

func (s *memoryStore) ListMembers(_ context.Context, teamID string) ([]TeamMemberView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.members[teamID]
	out := make([]TeamMemberView, 0, len(ids))
	for userID := range ids {
		user, ok := s.users[userID]
		if !ok {
			continue
		}
		key := memberKey(teamID, userID)
		joinedAt, ok := s.memberJoinedAt[key]
		if !ok {
			joinedAt = time.Now()
		}
		role := s.memberRoles[key]
		if role == "" {
			role = MemberRoleMember
		}
		out = append(out, TeamMemberView{User: *cloneUser(&user), Role: role, CreatedAt: joinedAt, MemberDisabledAt: copyTime(s.memberDisabled[key]), MonthlyBudgetCredits: s.memberBudgets[key]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *memoryStore) AddMember(_ context.Context, teamID, userID string, role TeamMemberRole) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addMemberLocked(teamID, userID, role)
}

func (s *memoryStore) RemoveMember(_ context.Context, teamID, userID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.members[teamID]
	if !ok {
		return false, nil
	}
	if _, exists := set[userID]; !exists {
		return false, nil
	}
	if err := s.protectAdminLocked(teamID, userID); err != nil {
		return false, err
	}
	delete(set, userID)
	key := memberKey(teamID, userID)
	delete(s.memberRoles, key)
	delete(s.memberJoinedAt, key)
	delete(s.memberDisabled, key)
	delete(s.memberBudgets, key)
	return true, nil
}

func (s *memoryStore) ListTeamsForUser(_ context.Context, userID string) ([]TeamMembership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type row struct {
		m  TeamMembership
		at time.Time
	}
	out := make([]row, 0, 2)
	for teamID, userIDs := range s.members {
		if _, ok := userIDs[userID]; !ok {
			continue
		}
		team, ok := s.teams[teamID]
		if !ok {
			continue
		}
		role := s.memberRoles[memberKey(teamID, userID)]
		if role == "" {
			role = MemberRoleMember
		}
		joined, ok := s.memberJoinedAt[memberKey(teamID, userID)]
		if !ok {
			joined = time.Now()
		}
		out = append(out, row{m: TeamMembership{Team: *cloneTeam(&team), Role: role, MemberDisabledAt: copyTime(s.memberDisabled[memberKey(teamID, userID)]), MonthlyBudgetCredits: s.memberBudgets[memberKey(teamID, userID)]}, at: joined})
	}
	// 与 Postgres 实现同语义:按成员加入时间(team_members.created_at)排序。
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].at.Before(out[j].at)
	})
	memberships := make([]TeamMembership, 0, len(out))
	for _, r := range out {
		memberships = append(memberships, r.m)
	}
	return memberships, nil
}

func (s *memoryStore) ListAllMemberships(_ context.Context) ([]AllMembershipRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type row struct {
		r  AllMembershipRow
		at time.Time
	}
	rows := make([]row, 0, len(s.members)*2)
	for teamID, userIDs := range s.members {
		team, ok := s.teams[teamID]
		if !ok {
			continue
		}
		for userID := range userIDs {
			key := memberKey(teamID, userID)
			role := s.memberRoles[key]
			if role == "" {
				role = MemberRoleMember
			}
			joined, ok := s.memberJoinedAt[key]
			if !ok {
				joined = time.Now()
			}
			rows = append(rows, row{
				at: joined,
				r: AllMembershipRow{
					TeamID: teamID, TeamName: team.Name, UserID: userID, Role: role,
					MonthlyBudgetCredits: s.memberBudgets[key],
					MemberDisabledAt:     copyTime(s.memberDisabled[key]),
					JoinedAt:             joined,
				},
			})
		}
	}
	// 与 Postgres 实现同语义:加入时间升序,同时间按(团队ID, 用户ID)稳定排序。
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].at.Equal(rows[j].at) {
			return rows[i].at.Before(rows[j].at)
		}
		if rows[i].r.TeamID != rows[j].r.TeamID {
			return rows[i].r.TeamID < rows[j].r.TeamID
		}
		return rows[i].r.UserID < rows[j].r.UserID
	})
	out := make([]AllMembershipRow, 0, len(rows))
	for _, item := range rows {
		out = append(out, item.r)
	}
	return out, nil
}

func memberKey(teamID, userID string) string {
	return teamID + ":" + userID
}

// numeric6:把 numeric(18,6) 列语义带到内存实现——写入即按 6 位小数定点,
// 读取时与 Postgres ::text 输出一致(否则跨实现对比会因 "0.3" vs "0.300000" 漂移)。
func numeric6(value string) string {
	micro, err := credits.ParseCreditsToMicro(value)
	if err != nil {
		return value
	}
	return credits.FormatMicroCredits(micro)
}

func cloneUser(u *UserRecord) *UserRecord          { c := *u; c.DisabledAt = copyTime(u.DisabledAt); return &c }
func cloneTeam(t *TeamRecord) *TeamRecord          { c := *t; c.DisabledAt = copyTime(t.DisabledAt); return &c }
func cloneSession(s *SessionRecord) *SessionRecord { c := *s; return &c }
