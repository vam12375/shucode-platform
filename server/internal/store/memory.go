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

	users          map[string]UserRecord
	sessions       map[string]SessionRecord
	teams          map[string]TeamRecord
	members        map[string]map[string]struct{} // teamID -> userIDs
	memberRoles    map[string]TeamMemberRole      // "teamId:userId"
	memberJoinedAt map[string]time.Time
	deviceTokens   map[string]DeviceTokenRecord
	flows          map[string]LoginFlowRecord
	releases       map[int64]ReleaseRecord
	models         map[string]PlatformModelRecord
	providers      map[string]UpstreamProviderRecord
	config         map[string][]byte
	usage          []UsageRecord
	auditLogs      []AdminAuditLogView
	quotaAdjusts   []quotaAdjustmentRow
	upstreamRoutes map[string]UpstreamRouteRecord
	reservations   map[string]QuotaReservationRecord
	alertRules     map[string]AlertRuleRecord
	alertEvents    []AlertEventRecord
}

type quotaAdjustmentRow struct {
	TeamQuotaAdjustmentView
	TeamID      string
	ActorUserID string
}

func NewMemoryStore(ctx context.Context, opts MemoryOptions) (*memoryStore, error) {
	s := &memoryStore{
		users:          map[string]UserRecord{},
		sessions:       map[string]SessionRecord{},
		teams:          map[string]TeamRecord{},
		members:        map[string]map[string]struct{}{},
		memberRoles:    map[string]TeamMemberRole{},
		memberJoinedAt: map[string]time.Time{},
		deviceTokens:   map[string]DeviceTokenRecord{},
		flows:          map[string]LoginFlowRecord{},
		releases:       map[int64]ReleaseRecord{},
		models:         map[string]PlatformModelRecord{},
		providers:      map[string]UpstreamProviderRecord{},
		config:         map[string][]byte{},
		upstreamRoutes: map[string]UpstreamRouteRecord{},
		reservations:   map[string]QuotaReservationRecord{},
		alertRules:     map[string]AlertRuleRecord{},
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
			v := user
			return &v, nil
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

func (s *memoryStore) CreateUser(_ context.Context, input UserInput) (*UserRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		out = append(out, user)
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
	if patch.HasDisabled {
		user.DisabledAt = patch.DisabledAt
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
		out = append(out, TeamWithUsage{Team: team, MemberCount: count})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Team.CreatedAt.Before(out[j].Team.CreatedAt) })
	return out, nil
}

func (s *memoryStore) UpdateTeam(_ context.Context, id string, patch UpdateTeamPatch) (*TeamRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	team, ok := s.teams[id]
	if !ok {
		return nil, nil
	}
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
		team.DisabledAt = patch.DisabledAt
	}
	s.teams[id] = team
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
		out = append(out, TeamMemberView{User: user, Role: role, CreatedAt: joinedAt})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *memoryStore) AddMember(_ context.Context, teamID, userID string, role TeamMemberRole) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.teams[teamID]; !ok {
		return fmt.Errorf("团队不存在: %s", teamID)
	}
	if _, ok := s.users[userID]; !ok {
		return fmt.Errorf("用户不存在: %s", userID)
	}
	set, ok := s.members[teamID]
	if !ok {
		set = map[string]struct{}{}
		s.members[teamID] = set
	}
	key := memberKey(teamID, userID)
	if _, exists := set[userID]; !exists {
		s.memberJoinedAt[key] = time.Now()
	}
	set[userID] = struct{}{}
	s.memberRoles[key] = role
	return nil
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
	delete(set, userID)
	key := memberKey(teamID, userID)
	delete(s.memberRoles, key)
	delete(s.memberJoinedAt, key)
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
		out = append(out, row{m: TeamMembership{Team: team, Role: role}, at: joined})
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

func cloneUser(u *UserRecord) *UserRecord          { c := *u; return &c }
func cloneTeam(t *TeamRecord) *TeamRecord          { c := *t; return &c }
func cloneSession(s *SessionRecord) *SessionRecord { c := *s; return &c }
