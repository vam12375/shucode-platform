package apiapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

const mfaTestPassword = "Root-MFA-Password-2026!"

type rootMFAFixture struct {
	f      *apiFixture
	user   *store.UserRecord
	cookie string
	now    *time.Time
}

func newRootMFAFixture(t *testing.T) rootMFAFixture {
	t.Helper()
	f := newAPIEmptyFixture(t)
	ctx := context.Background()
	passwordHash, err := crypto.HashPassword(mfaTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	user, err := f.store.CreateUser(ctx, store.UserInput{Email: "mfa-root@test.local", PasswordHash: passwordHash, DisplayName: "MFA Root", Role: store.RoleRoot})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(30 * time.Second).Add(5 * time.Second)
	f.deps.Now = func() time.Time { return now }
	f.deps.ConsoleLoginLimiter = ratelimit.NewInProc(ratelimit.Window{Window: time.Minute, Max: 200})
	token := uuid.NewString()
	if err := f.store.CreateSession(ctx, store.SessionRecord{TokenHash: crypto.HashToken(token), UserID: user.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return rootMFAFixture{f: f, user: user, cookie: sessionCookieName + "=" + token, now: &now}
}
func (fixture *rootMFAFixture) setup(t *testing.T) string {
	t.Helper()
	response := fixture.f.do("POST", "/console/api/security/mfa/setup", fixture.cookie, fmt.Sprintf(`{"currentPassword":%q}`, mfaTestPassword))
	if response.Code != 200 {
		t.Fatalf("setup: %d %s", response.Code, response.Body.String())
	}
	data := dataOf(t, response)
	secret := data["secret"].(string)
	if !strings.HasPrefix(data["otpauthUrl"].(string), "otpauth://totp/") {
		t.Fatal("缺少可导入的TOTP URI")
	}
	return secret
}
func (fixture *rootMFAFixture) enable(t *testing.T, secret string) []string {
	t.Helper()
	code, err := crypto.TOTP(secret, fixture.now.Unix()/30)
	if err != nil {
		t.Fatal(err)
	}
	response := fixture.f.do("POST", "/console/api/security/mfa/enable", fixture.cookie, fmt.Sprintf(`{"code":%q}`, code))
	if response.Code != 200 {
		t.Fatalf("enable: %d %s", response.Code, response.Body.String())
	}
	fixture.cookie = sessionCookie(response)
	items := dataOf(t, response)["recoveryCodes"].([]any)
	codes := make([]string, len(items))
	for i, item := range items {
		codes[i] = item.(string)
	}
	if len(codes) != 10 {
		t.Fatal("恢复码应为10个")
	}
	return codes
}
func (fixture *rootMFAFixture) login(body string) *httptest.ResponseRecorder {
	return fixture.f.do("POST", "/console/api/auth/login", "", body)
}
func TestRootMFALifecycleLoginReplayAndRecovery(t *testing.T) {
	fixture := newRootMFAFixture(t)
	f := fixture.f
	ctx := context.Background()
	oldCookie := fixture.cookie
	secret := fixture.setup(t)
	raw, err := f.store.GetConfigValue(ctx, rootMFAKey(fixture.user.ID))
	if err != nil || strings.Contains(string(raw), secret) {
		t.Fatalf("TOTP明文不能入库: %v", err)
	}
	codes := fixture.enable(t, secret)
	if response := f.do("GET", "/console/api/auth/me", oldCookie, ""); response.Code != 401 {
		t.Fatalf("启用后旧会话必须撤销: %d", response.Code)
	}
	raw, _ = f.store.GetConfigValue(ctx, rootMFAKey(fixture.user.ID))
	for _, code := range codes {
		if strings.Contains(string(raw), code) || strings.Contains(string(raw), normalizeRecoveryCode(code)) {
			t.Fatal("恢复码不能明文保存")
		}
	}
	for _, action := range []string{"disable", "recovery-codes"} {
		response := f.do("POST", "/console/api/security/mfa/"+action, fixture.cookie, fmt.Sprintf(`{"currentPassword":"wrong","recoveryCode":%q}`, codes[9]))
		if response.Code != 403 {
			t.Fatalf("错误密码不得%s: %d %s", action, response.Code, response.Body.String())
		}
	}
	status := f.do("GET", "/console/api/security/mfa", fixture.cookie, "")
	if status.Code != 200 || dataOf(t, status)["enabled"] != true || dataOf(t, status)["recoveryCodesRemaining"] != float64(10) {
		t.Fatalf("MFA状态: %d %s", status.Code, status.Body.String())
	}
	prefix := fmt.Sprintf(`"email":%q,"password":%q`, fixture.user.Email, mfaTestPassword)
	for _, extra := range []string{"", `,"code":"000000"`} {
		response := fixture.login("{" + prefix + extra + "}")
		if response.Code != 403 || response.Header().Get("set-cookie") != "" {
			t.Fatalf("缺失/无效第二因素不得登录: %d %s", response.Code, response.Body.String())
		}
	}
	enabledCode, _ := crypto.TOTP(secret, fixture.now.Unix()/30)
	if response := fixture.login("{" + prefix + fmt.Sprintf(`,"code":%q}`, enabledCode)); response.Code != 403 {
		t.Fatalf("启用时已消费OTP不得再次登录: %d %s", response.Code, response.Body.String())
	}
	*fixture.now = fixture.now.Add(30 * time.Second)
	code, _ := crypto.TOTP(secret, fixture.now.Unix()/30)
	response := fixture.login("{" + prefix + fmt.Sprintf(`,"code":%q}`, code))
	if response.Code != 200 {
		t.Fatalf("有效OTP登录: %d %s", response.Code, response.Body.String())
	}
	if replay := fixture.login("{" + prefix + fmt.Sprintf(`,"code":%q}`, code)); replay.Code != 403 {
		t.Fatalf("OTP重放必须拒绝: %d %s", replay.Code, replay.Body.String())
	}
	var success, rejected atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan string, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := fixture.login("{" + prefix + fmt.Sprintf(`,"recoveryCode":%q}`, codes[0]))
			if rec.Code == 200 {
				success.Add(1)
			} else if rec.Code == 403 {
				rejected.Add(1)
			} else {
				errs <- fmt.Sprintf("%d %s", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if success.Load() != 1 || rejected.Load() != 11 {
		t.Fatalf("恢复码并发只能使用一次: success=%d rejected=%d", success.Load(), rejected.Load())
	}
	response = f.do("POST", "/console/api/security/mfa/setup", fixture.cookie, fmt.Sprintf(`{"currentPassword":%q}`, mfaTestPassword))
	if response.Code != 409 {
		t.Fatalf("已启用MFA不能用setup覆盖: %d", response.Code)
	}
	response = f.do("POST", "/console/api/security/mfa/recovery-codes", fixture.cookie, fmt.Sprintf(`{"currentPassword":%q,"recoveryCode":%q}`, mfaTestPassword, codes[1]))
	if response.Code != 200 {
		t.Fatalf("轮换恢复码: %d %s", response.Code, response.Body.String())
	}
	fixture.cookie = sessionCookie(response)
	newCodes := dataOf(t, response)["recoveryCodes"].([]any)
	if response := fixture.login("{" + prefix + fmt.Sprintf(`,"recoveryCode":%q}`, codes[2])); response.Code != 403 {
		t.Fatalf("轮换后旧恢复码必须失效: %d", response.Code)
	}
	response = f.do("POST", "/console/api/security/mfa/disable", fixture.cookie, fmt.Sprintf(`{"currentPassword":%q,"recoveryCode":%q}`, mfaTestPassword, newCodes[0]))
	if response.Code != 200 || dataOf(t, response)["enabled"] != false {
		t.Fatalf("停用MFA: %d %s", response.Code, response.Body.String())
	}
	fixture.cookie = sessionCookie(response)
	if response := fixture.login("{" + prefix + "}"); response.Code != 200 {
		t.Fatalf("停用后按密码登录: %d %s", response.Code, response.Body.String())
	}
	logs, _ := f.store.ListAuditLogs(ctx, store.AuditLogFilter{ActorUserID: fixture.user.ID})
	auditRaw, _ := json.Marshal(logs)
	if strings.Contains(string(auditRaw), secret) || strings.Contains(string(auditRaw), normalizeRecoveryCode(codes[0])) {
		t.Fatal("安全审计不能泄漏秘密")
	}
}
func TestRootMFARejectsWrongPasswordExpiredSetupAndOtherRoles(t *testing.T) {
	fixture := newRootMFAFixture(t)
	f := fixture.f
	if response := f.do("POST", "/console/api/security/mfa/setup", fixture.cookie, `{"currentPassword":"wrong"}`); response.Code != 403 {
		t.Fatalf("错误密码setup必须403: %d", response.Code)
	}
	if raw, _ := f.store.GetConfigValue(context.Background(), rootMFAKey(fixture.user.ID)); raw != nil {
		t.Fatal("错误密码不能创建pending")
	}
	secret := fixture.setup(t)
	*fixture.now = fixture.now.Add(11 * time.Minute)
	code, _ := crypto.TOTP(secret, fixture.now.Unix()/30)
	if response := f.do("POST", "/console/api/security/mfa/enable", fixture.cookie, fmt.Sprintf(`{"code":%q}`, code)); response.Code != 410 {
		t.Fatalf("过期setup不能启用: %d %s", response.Code, response.Body.String())
	}
	f.deps.SecretBox = nil
	if response := f.do("POST", "/console/api/security/mfa/setup", fixture.cookie, fmt.Sprintf(`{"currentPassword":%q}`, mfaTestPassword)); response.Code != 503 {
		t.Fatalf("未配置加密不得enroll: %d", response.Code)
	}
	quota := newQuotaRequestAPIFixture(t)
	for _, cookie := range []string{quota.adminCookie, quota.memberCookie} {
		for _, request := range []struct{ method, path, body string }{{"GET", "/console/api/security/mfa", ""}, {"POST", "/console/api/security/mfa/setup", `{"currentPassword":"anything"}`}, {"POST", "/console/api/security/mfa/enable", `{"code":"123456"}`}, {"POST", "/console/api/security/mfa/disable", `{"currentPassword":"anything","code":"123456"}`}, {"POST", "/console/api/security/mfa/recovery-codes", `{"currentPassword":"anything","code":"123456"}`}} {
			response := quota.f.do(request.method, request.path, cookie, request.body)
			if response.Code != 403 {
				t.Fatalf("非root安全接口必须403: %s got=%d", request.path, response.Code)
			}
		}
	}
}

type mfaBrokenLimiter struct{}

func (mfaBrokenLimiter) Allow(context.Context, string) (bool, error) {
	return false, errors.New("offline")
}
func TestRootMFAFailsClosedOnLimiterOrCorruptState(t *testing.T) {
	fixture := newRootMFAFixture(t)
	f := fixture.f
	secret := fixture.setup(t)
	fixture.enable(t, secret)
	f.deps.ConsoleLoginLimiter = mfaBrokenLimiter{}
	if response := f.do("POST", "/console/api/security/mfa/disable", fixture.cookie, fmt.Sprintf(`{"currentPassword":%q,"code":"123456"}`, mfaTestPassword)); response.Code != 503 {
		t.Fatalf("限流故障应failclosed: %d", response.Code)
	}
	f.deps.ConsoleLoginLimiter = ratelimit.NewInProc(ratelimit.Window{Window: time.Minute, Max: 200})
	if err := f.store.SetConfigValue(context.Background(), rootMFAKey(fixture.user.ID), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	response := fixture.login(fmt.Sprintf(`{"email":%q,"password":%q}`, fixture.user.Email, mfaTestPassword))
	if response.Code != 503 || response.Header().Get("set-cookie") != "" {
		t.Fatalf("MFA状态损坏不得降级为密码登录: %d %s", response.Code, response.Body.String())
	}
}

func TestRootMFAPlatformPasswordFormCannotBypassSecondFactor(t *testing.T) {
	fixture := newRootMFAFixture(t)
	secret := fixture.setup(t)
	fixture.enable(t, secret)
	f := fixture.f
	flowID := uuid.NewString()
	if err := f.store.CreateLoginFlow(context.Background(), store.LoginFlowRecord{ID: flowID, PollTokenHash: crypto.HashToken("poll"), Status: store.FlowStatusPending, DeviceName: "MFA设备", CreatedAt: *fixture.now, ExpiresAt: fixture.now.Add(5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	response := f.form("/app/platform/login", "", map[string]string{"flow_id": flowID, "email": fixture.user.Email, "password": mfaTestPassword})
	if response.Code != 403 || response.Header().Get("set-cookie") != "" || !strings.Contains(response.Body.String(), "/console/#/login") {
		t.Fatalf("设备密码表单不能绕过root MFA: %d %s", response.Code, response.Body.String())
	}
	f.deps.PlatformLoginLimiter = mfaBrokenLimiter{}
	blocked := f.form("/app/platform/login", "", map[string]string{"flow_id": flowID, "email": fixture.user.Email, "password": mfaTestPassword})
	if blocked.Code != 503 || blocked.Header().Get("set-cookie") != "" {
		t.Fatalf("设备密码表单限流故障必须拒绝登录: %d", blocked.Code)
	}
	response = f.do("GET", "/app/platform/login?flow_id="+flowID, fixture.cookie, "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), fixture.user.Email) {
		t.Fatalf("完成MFA的会话可以继续设备授权: %d %s", response.Code, response.Body.String())
	}
}

func TestRootMFAAttemptRateLimit(t *testing.T) {
	fixture := newRootMFAFixture(t)
	fixture.f.deps.ConsoleLoginLimiter = ratelimit.NewInProc(ratelimit.Window{Window: time.Minute, Max: 1})
	first := fixture.f.do("POST", "/console/api/security/mfa/setup", fixture.cookie, `{"currentPassword":"wrong"}`)
	second := fixture.f.do("POST", "/console/api/security/mfa/setup", fixture.cookie, fmt.Sprintf(`{"currentPassword":%q}`, mfaTestPassword))
	if first.Code != 403 || second.Code != 429 {
		t.Fatalf("密码验证必须受到账号限流: first=%d second=%d", first.Code, second.Code)
	}
}

type delayedRootSessionStore struct {
	store.PlatformStore
	blocked chan store.SessionRecord
	resume  chan struct{}
	once    sync.Once
}

func (s *delayedRootSessionStore) CreateSession(ctx context.Context, record store.SessionRecord) error {
	if record.SecurityVersion == "" {
		s.once.Do(func() { s.blocked <- record; <-s.resume })
	}
	return s.PlatformStore.CreateSession(ctx, record)
}
func TestRootMFAEnableInvalidatesLatePasswordSessions(t *testing.T) {
	for _, path := range []string{"console", "platform-form"} {
		t.Run(path, func(t *testing.T) {
			fixture := newRootMFAFixture(t)
			f := fixture.f
			secret := fixture.setup(t)
			blocked := make(chan store.SessionRecord, 1)
			resume := make(chan struct{})
			wrapper := &delayedRootSessionStore{PlatformStore: f.store, blocked: blocked, resume: resume}
			f.deps.Store = wrapper
			formID := uuid.NewString()
			if path == "platform-form" {
				if err := f.store.CreateLoginFlow(context.Background(), store.LoginFlowRecord{ID: formID, PollTokenHash: crypto.HashToken("race"), Status: store.FlowStatusPending, CreatedAt: *fixture.now, ExpiresAt: fixture.now.Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
			}
			responseChannel := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				if path == "console" {
					responseChannel <- fixture.login(fmt.Sprintf(`{"email":%q,"password":%q}`, fixture.user.Email, mfaTestPassword))
				} else {
					responseChannel <- f.form("/app/platform/login", "", map[string]string{"flow_id": formID, "email": fixture.user.Email, "password": mfaTestPassword})
				}
			}()
			select {
			case record := <-blocked:
				if record.SecurityVersion != "" {
					t.Fatal("密码验证尚未证明启用后的安全版本")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("未拦截到旧密码登录的CreateSession")
			}
			fixture.enable(t, secret)
			state, _, err := readRootMFAState(context.Background(), f.deps, fixture.user.ID)
			if err != nil || state.SecurityVersion == "" {
				t.Fatalf("启用必须轮换安全版本: %+v %v", state, err)
			}
			close(resume)
			response := <-responseChannel
			if response.Code != 200 && response.Code != 303 {
				t.Fatalf("竞态旧请求应完成以检验迟到会话: %d %s", response.Code, response.Body.String())
			}
			lateCookie := sessionCookie(response)
			if lateCookie == "" {
				t.Fatal("测试必须产生删除会话后才签发的旧版本cookie")
			}
			lateToken := strings.SplitN(lateCookie, "=", 2)[1]
			persisted, err := f.store.FindSession(context.Background(), crypto.HashToken(lateToken))
			if err != nil || persisted == nil || persisted.SecurityVersion != "" {
				t.Fatalf("迟到会话必须保留实际验证时的旧版本: %+v %v", persisted, err)
			}
			authenticated := f.do("GET", "/console/api/auth/me", lateCookie, "")
			if authenticated.Code != 401 {
				t.Fatalf("迟到密码会话不能绕过新启用MFA: %d %s", authenticated.Code, authenticated.Body.String())
			}
			current := f.do("GET", "/console/api/auth/me", fixture.cookie, "")
			if current.Code != 200 {
				t.Fatalf("完成MFA的当前会话应可用: %d %s", current.Code, current.Body.String())
			}
		})
	}
}

type failingSessionCleanupStore struct{ store.PlatformStore }

func (s *failingSessionCleanupStore) DeleteSessionsForUser(context.Context, string) error {
	return errors.New("cleanup unavailable")
}
func TestRootMFASecurityEpochRevokesSessionsWithoutDeletion(t *testing.T) {
	fixture := newRootMFAFixture(t)
	f := fixture.f
	secret := fixture.setup(t)
	oldCookie := fixture.cookie
	f.deps.Store = &failingSessionCleanupStore{PlatformStore: f.store}
	codes := fixture.enable(t, secret)
	assertRevoked := func(cookie string) {
		t.Helper()
		token := strings.SplitN(cookie, "=", 2)[1]
		persisted, err := f.store.FindSession(context.Background(), crypto.HashToken(token))
		if err != nil || persisted == nil {
			t.Fatalf("测试需保留尚未清理的旧会话: %+v %v", persisted, err)
		}
		response := f.do("GET", "/console/api/auth/me", cookie, "")
		if response.Code != 401 {
			t.Fatalf("旧安全版本不依赖删除成功也应失效: %d %s", response.Code, response.Body.String())
		}
	}
	assertRevoked(oldCookie)
	before, _, err := readRootMFAState(context.Background(), f.deps, fixture.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldCookie = fixture.cookie
	response := f.do("POST", "/console/api/security/mfa/recovery-codes", fixture.cookie, fmt.Sprintf(`{"currentPassword":%q,"recoveryCode":%q}`, mfaTestPassword, codes[0]))
	if response.Code != 200 {
		t.Fatalf("清理失败不影响安全轮换: %d %s", response.Code, response.Body.String())
	}
	fixture.cookie = sessionCookie(response)
	after, _, err := readRootMFAState(context.Background(), f.deps, fixture.user.ID)
	if err != nil || after.SecurityVersion == before.SecurityVersion {
		t.Fatalf("轮换恢复码必须换安全版本: %+v %v", after, err)
	}
	assertRevoked(oldCookie)
	nextCodes := dataOf(t, response)["recoveryCodes"].([]any)
	oldCookie = fixture.cookie
	response = f.do("POST", "/console/api/security/mfa/disable", fixture.cookie, fmt.Sprintf(`{"currentPassword":%q,"recoveryCode":%q}`, mfaTestPassword, nextCodes[0]))
	if response.Code != 200 {
		t.Fatalf("停用MFA: %d %s", response.Code, response.Body.String())
	}
	fixture.cookie = sessionCookie(response)
	assertRevoked(oldCookie)
	final, _, err := readRootMFAState(context.Background(), f.deps, fixture.user.ID)
	if err != nil || final.SecurityVersion == after.SecurityVersion || final.Enabled {
		t.Fatalf("停用也必须换版本: %+v %v", final, err)
	}
	response = f.do("GET", "/console/api/auth/me", fixture.cookie, "")
	if response.Code != 200 {
		t.Fatalf("安全动作新签发会话应可用: %d %s", response.Code, response.Body.String())
	}
}
func TestRootMFAProfileSessionRenewalRetainsVerifiedSecurityEpoch(t *testing.T) {
	fixture := newRootMFAFixture(t)
	secret := fixture.setup(t)
	fixture.enable(t, secret)
	f := fixture.f
	response := f.do("PATCH", "/console/api/profile", fixture.cookie, fmt.Sprintf(`{"currentPassword":%q,"password":"New-Root-Password-2026!"}`, mfaTestPassword))
	if response.Code != 200 {
		t.Fatalf("修改密码: %d %s", response.Code, response.Body.String())
	}
	cookie := sessionCookie(response)
	if cookie == "" {
		t.Fatal("密码变更必须续签会话")
	}
	response = f.do("GET", "/console/api/auth/me", cookie, "")
	if response.Code != 200 {
		t.Fatalf("续签必须复制已验证的安全版本: %d %s", response.Code, response.Body.String())
	}
}
