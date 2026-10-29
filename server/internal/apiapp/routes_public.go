package apiapp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 公共路由:公开配置 / Release / user-config / 额度投影 / SSE / 设备登录流(镜像 api 的六个 route 文件)。

func registerPublicRoutes(router *gin.Engine, deps *Deps) {
	registerClientConfigRoutes(router, deps)
	registerReleaseRoutes(router, deps)
	registerPlatformAuthRoutes(router, deps)
	registerUserConfigRoutes(router, deps)
	registerAgentMediaPreferencesRoutes(router, deps)
	registerClientCreditRoutes(router, deps)
	registerClientEventRoutes(router, deps)
}

// ---- client-configs ----

func registerClientConfigRoutes(router *gin.Engine, deps *Deps) {
	handler := func(c *gin.Context) {
		ctx := c.Request.Context()
		helpRaw, err := deps.Store.GetConfigValue(ctx, contracts.PlatformConfigKeyHelp)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		forceRaw, err := deps.Store.GetConfigValue(ctx, contracts.PlatformConfigKeyForceUpdate)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		pluginRaw, err := deps.Store.GetConfigValue(ctx, contracts.PlatformConfigKeyPluginStoreOrder)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		active, err := deps.Store.GetActiveRelease(ctx)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		input := contracts.BuildClientConfigsEnvelopeInput{}
		if help, err := jsonDecode(helpRaw); err == nil {
			input.Help = help
		}
		if plugin, err := jsonDecode(pluginRaw); err == nil && plugin != nil {
			input.PluginStoreOrder = plugin
		}
		// 更新清单仍是 stub 时，即使数据库里残留旧配置也不下发给客户端：
		// 否则客户端会被要求升级却拉不到安装包。
		if deps.Env.ReleaseManifestEnabled {
			if force, err := jsonDecode(forceRaw); err == nil {
				input.ForceUpdateMinimalVersion = readForceUpdateMinimalVersion(force)
			}
		}
		// 客户端 download 边界只接受 https 绝对地址;非 https(本地开发)不下发。
		if active != nil && strings.HasPrefix(deps.Env.ReleaseBaseURL, "https://") {
			input.BuiltinReleaseURL = contracts.BuildBuiltinReleaseDownloadURL(deps.Env.ReleaseBaseURL, float64(active.Revision))
		}
		c.Header("Cache-Control", "no-cache")
		c.JSON(200, contracts.BuildClientConfigsEnvelope(input))
	}
	router.GET("/api/v1/client/configs", handler)
	router.GET("/n/configs", handler)
}

func readForceUpdateMinimalVersion(value any) string {
	record, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if minimal, ok := record["minimalVersion"].(string); ok {
		if trimmed := strings.TrimSpace(minimal); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// ---- releases ----

func registerReleaseRoutes(router *gin.Engine, deps *Deps) {
	// latest.json 与 :revision 合并在同一路由(gin 的静态/参数同级会冲突),按 param 分派。
	router.GET("/api/v1/releases/builtin/:revision", func(c *gin.Context) {
		ctx := c.Request.Context()
		// latest.json 必须先于 parseInt 判定(TS 静态路由优先于参数路由)。
		if c.Param("revision") == "latest.json" {
			active, err := deps.Store.GetActiveRelease(ctx)
			if err != nil {
				internalError(c, deps, err)
				return
			}
			if active == nil {
				c.JSON(404, gin.H{"error": "no active release"})
				return
			}
			c.Header("Cache-Control", "public, max-age=60")
			c.Data(200, "application/json; charset=utf-8", active.Content)
			return
		}
		raw := strings.TrimSuffix(c.Param("revision"), ".json")
		revision, ok := parseLeadingInt(raw)
		if !ok {
			c.JSON(400, gin.H{"error": "invalid revision"})
			return
		}
		release, err := deps.Store.GetRelease(ctx, revision)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if release == nil {
			c.JSON(404, gin.H{"error": "release not found"})
			return
		}
		c.Header("Cache-Control", "public, max-age=300")
		c.Data(200, "application/json; charset=utf-8", release.Content)
	})

	router.GET("/api/v1/releases/electron/manifest", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Data(200, "text/yaml; charset=utf-8", []byte("version: 0.0.0\n"))
	})
}

// ---- user-config ----

func registerUserConfigRoutes(router *gin.Engine, deps *Deps) {
	router.GET(contracts.UserConfigAPIPath, func(c *gin.Context) {
		ctx := c.Request.Context()
		auth, err := authenticateDevice(ctx, deps, c)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if auth == nil {
			c.JSON(401, fail(401, "凭据无效或已吊销"))
			return
		}
		membership, err := store.ResolveUserTeam(ctx, deps.Store, auth.User.ID)
		if errors.Is(err, store.ErrAmbiguousTeam) {
			c.JSON(409, fail(409, "账号存在多个有效团队，请联系管理员明确团队归属"))
			return
		}
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if membership == nil {
			c.JSON(403, fail(403, "账号没有有效团队或团队访问已停用，请联系团队管理员"))
			return
		}
		policy, err := deps.Store.GetTeamPolicy(ctx, membership.Team.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		models, err := deps.Store.ListModels(ctx, false)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		input := BuildUserConfigInput{
			UserID:         auth.User.ID,
			TeamID:         membership.Team.ID,
			DeviceTokenID:  auth.Device.ID,
			GatewayBaseURL: deps.Env.GatewayBaseURL,
			GatewayAPI: gatewayAPI{
				Type:    deps.Env.GatewayAPIType,
				BaseURL: deps.Env.GatewayAPIBaseURL,
			},
		}
		for _, model := range models {
			if !store.TeamModelAllowed(policy, model.ModelID) {
				continue
			}
			input.Models = append(input.Models, userConfigModel{
				ModelID:     model.ModelID,
				DisplayName: model.DisplayName,
				ConfigMode:  model.ConfigMode,
				Config:      canonicalModelConfig(model.ModelConfig),
			})
		}
		if len(input.Models) > 0 {
			input.DefaultModelID = input.Models[0].ModelID
		}
		envelope := buildUserConfigEnvelope(input)
		if auth.PlaintextToken != "" {
			envelope = withPlatformAccessToken(envelope, auth.PlaintextToken)
		}
		body, err := jsonMarshalNoHTML(map[string]any{"code": 0, "data": map[string]any{"envelope": envelope}})
		if err != nil {
			internalError(c, deps, err)
			return
		}
		sum := sha256Bytes(body)
		etag := `"` + base64urlNoPad(sum[:])[:27] + `"`
		if match := c.GetHeader("if-none-match"); match != "" {
			for _, candidate := range splitHeaderList(match) {
				if candidate == etag {
					c.Header("etag", etag)
					c.Header("cache-control", "private, no-cache")
					c.Status(304)
					return
				}
			}
		}
		c.Header("content-type", "application/json; charset=utf-8")
		c.Header("etag", etag)
		c.Header("cache-control", "private, no-cache")
		c.Data(200, "application/json; charset=utf-8", body)
	})
}

// ---- client-credit ----

// registerClientCreditRoutes:客户端模型菜单展示用的只读团队额度投影。
// 额度执行点仍在网关；这里只把余额、预留和已用比例回给设备，不参与本地扣减。
func registerClientCreditRoutes(router *gin.Engine, deps *Deps) {
	router.GET("/api/v1/client/credit", func(c *gin.Context) {
		ctx := c.Request.Context()
		auth, err := authenticateDevice(ctx, deps, c)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if auth == nil {
			c.JSON(401, fail(401, "凭据无效或已吊销"))
			return
		}
		membership, err := store.ResolveUserTeam(ctx, deps.Store, auth.User.ID)
		if errors.Is(err, store.ErrAmbiguousTeam) {
			c.JSON(409, fail(409, "账号存在多个有效团队，请联系管理员明确团队归属"))
			return
		}
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if membership == nil {
			c.JSON(403, fail(403, "账号没有有效团队或团队访问已停用，请联系团队管理员"))
			return
		}
		team := membership.Team
		quotaMicro, err := credits.ParseCreditsToMicro(team.QuotaCredits)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		usedMicro, err := credits.ParseCreditsToMicro(team.UsedCredits)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		// 余额只扣除已结算用量；历史兼容字段不能再把进行中请求显示为余额归零。
		remainingMicro := quotaMicro - usedMicro
		usedRatio := 0.0
		if !team.Unlimited && quotaMicro > 0 {
			usedRatio = float64(usedMicro) / float64(quotaMicro)
			if usedRatio < 0 {
				usedRatio = 0
			}
			if usedRatio > 1 {
				usedRatio = 1
			}
		}
		c.Header("cache-control", "private, no-cache")
		c.JSON(200, okData(gin.H{
			"teamId":           team.ID,
			"teamName":         team.Name,
			"unlimited":        team.Unlimited,
			"quotaCredits":     credits.FormatMicroCredits(quotaMicro),
			"usedCredits":      credits.FormatMicroCredits(usedMicro),
			"reservedCredits":  credits.FormatMicroCredits(0),
			"remainingCredits": credits.FormatMicroCredits(remainingMicro),
			"usedRatio":        usedRatio,
		}))
	})
}

// ---- client-events(SSE) ----

func registerClientEventRoutes(router *gin.Engine, deps *Deps) {
	router.GET("/api/v1/client/events", func(c *gin.Context) {
		ctx := c.Request.Context()
		auth, err := authenticateDevice(ctx, deps, c)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if auth == nil {
			c.JSON(401, fail(401, "凭据无效或已吊销"))
			return
		}
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		ch, unsubscribe := deps.ConfigEvents.Subscribe(streamCtx)
		defer unsubscribe()

		c.Header("content-type", "text/event-stream")
		c.Header("cache-control", "no-cache")
		c.Header("connection", "keep-alive")
		c.Writer.WriteHeader(200)
		c.Writer.Flush()
		// 初始帧:hello + retry 建议。
		if _, err := c.Writer.WriteString("retry: 5000\ndata: " + `{"type":"hello"}` + "\n\n"); err != nil {
			return
		}
		c.Writer.Flush()

		pending := []string{}
		lastPing := time.Now()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-streamCtx.Done():
				return
			case domains, ok := <-ch:
				if !ok {
					return
				}
				pending = mergeDomains(pending, domains)
			case <-ticker.C:
			}
			if len(pending) > 0 {
				configDomains := make([]string, 0, len(pending))
				for _, domain := range pending {
					if domain != events.DomainPreferences {
						configDomains = append(configDomains, domain)
					}
				}
				if len(configDomains) > 0 {
					payload, err := jsonMarshalNoHTML(map[string]any{"type": "config.changed", "domains": configDomains})
					if err != nil {
						return
					}
					if _, err := c.Writer.WriteString("event: config.changed\ndata: " + string(payload) + "\n\n"); err != nil {
						return
					}
				}
				for _, domain := range pending {
					if domain != events.DomainPreferences {
						continue
					}
					payload, err := jsonMarshalNoHTML(map[string]any{"type": "preferences.changed", "domain": domain})
					if err != nil {
						return
					}
					if _, err := c.Writer.WriteString("event: preferences.changed\ndata: " + string(payload) + "\n\n"); err != nil {
						return
					}
				}
				c.Writer.Flush()
				pending = []string{}
			} else if time.Since(lastPing) >= 25*time.Second {
				if _, err := c.Writer.WriteString("event: ping\ndata: \n\n"); err != nil {
					return
				}
				c.Writer.Flush()
				lastPing = time.Now()
			}
		}
	})
}

func mergeDomains(pending, incoming []string) []string {
	seen := make(map[string]struct{}, len(pending)+len(incoming))
	out := make([]string, 0, len(pending)+len(incoming))
	for _, domain := range append(append([]string{}, pending...), incoming...) {
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		out = append(out, domain)
	}
	return out
}

// ---- platform-auth(客户端 API + 浏览器页面) ----

type loginFlowTTL = time.Duration

const (
	flowTTL              loginFlowTTL = 10 * time.Minute
	pollIntervalSec                   = 3
	maxAuthInitBodyBytes              = 64 << 10
	maxLoginFormBytes                 = 64 << 10
)

func registerPlatformAuthRoutes(router *gin.Engine, deps *Deps) {
	router.GET("/api/v1/platform/account", func(c *gin.Context) {
		// 应用登录与视频复用同一设备会话；账号校验不依赖团队或媒体配置。
		auth, err := authenticateDevice(c.Request.Context(), deps, c)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if auth == nil {
			c.JSON(401, fail(401, "请先登录"))
			return
		}
		c.JSON(200, okData(gin.H{"id": auth.User.ID, "email": auth.User.Email, "display_name": auth.User.DisplayName}))
	})

	router.POST("/api/v1/platform/auth/init", func(c *gin.Context) {
		ctx := c.Request.Context()
		// 匿名入口：先限流再读 body，避免被用来无限写 login_flows 或耗尽内存。
		if deps.PlatformLoginLimiter != nil {
			allowed, limitErr := deps.PlatformLoginLimiter.Allow(ctx, resolveLimiterClientIP(c, deps)+":auth-init")
			if limitErr != nil {
				deps.Logger.Warn("登录流初始化限流不可用，按 fail-open 放行", "error", limitErr)
			} else if !allowed {
				c.JSON(429, fail(429, "请求过于频繁，请稍后再试"))
				return
			}
		}
		pollToken, ok := readPollToken(c)
		if !ok || len(pollToken) < 32 {
			c.JSON(400, fail(400, "缺少轮询凭据"))
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAuthInitBodyBytes)
		raw, readErr := io.ReadAll(c.Request.Body)
		if readErr != nil {
			c.JSON(413, fail(413, "请求体过大"))
			return
		}
		body := map[string]any{}
		if len(raw) > 0 {
			var parsed any
			if json.Unmarshal(raw, &parsed) == nil {
				if obj, ok := parsed.(map[string]any); ok {
					body = obj
				} else {
					// 与 zod 一致:非对象 body 直接拒绝(只有 JSON 解析失败才回落空对象)。
					c.JSON(400, fail(400, "请求参数不合法"))
					return
				}
			}
		}
		request, err := contracts.ParsePlatformAuthInitRequest(body)
		if err != nil {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		now := deps.Now()
		flowID, err := contracts.RandomUUIDv4()
		if err != nil {
			internalError(c, deps, err)
			return
		}
		deviceName := "未知设备"
		if request.DeviceName != nil {
			deviceName = *request.DeviceName
		}
		platform := ""
		if request.Platform != nil {
			platform = *request.Platform
		}
		if err := deps.Store.CreateLoginFlow(ctx, store.LoginFlowRecord{
			ID:             flowID,
			PollTokenHash:  crypto.HashToken(pollToken),
			Status:         store.FlowStatusPending,
			DeviceName:     deviceName,
			DevicePlatform: platform,
			CreatedAt:      now,
			ExpiresAt:      now.Add(flowTTL),
		}); err != nil {
			internalError(c, deps, err)
			return
		}
		authorizeURL := deps.Env.PublicAppOrigin + "/app/platform/login?flow_id=" + flowID
		c.JSON(200, okData(gin.H{
			"flow_id":           flowID,
			"authorize_url":     authorizeURL,
			"expires_at":        now.Add(flowTTL).Unix(),
			"poll_interval_sec": pollIntervalSec,
		}))
	})

	router.GET("/api/v1/platform/auth/poll/:flowId", func(c *gin.Context) {
		ctx := c.Request.Context()
		pollToken, ok := readPollToken(c)
		if !ok {
			c.JSON(400, fail(400, "缺少轮询凭据"))
			return
		}
		flowID := c.Param("flowId")
		flow, err := deps.Store.GetLoginFlow(ctx, flowID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if flow == nil {
			c.JSON(404, fail(404, "登录流程不存在"))
			return
		}
		if crypto.HashToken(pollToken) != flow.PollTokenHash {
			c.JSON(403, fail(403, "轮询凭据不匹配"))
			return
		}
		now := deps.Now()
		if (flow.Status == store.FlowStatusPending || flow.Status == store.FlowStatusAuthorized) &&
			!flow.ExpiresAt.After(now) {
			if _, err := deps.Store.UpdateLoginFlowIfStatus(ctx, flowID, flow.Status,
				store.UpdateLoginFlowPatch{Status: ptr(store.FlowStatusExpired)}); err != nil {
				internalError(c, deps, err)
				return
			}
			c.JSON(200, okData(gin.H{"status": "expired"}))
			return
		}
		if _, err := deps.Store.UpdateLoginFlow(ctx, flowID, store.UpdateLoginFlowPatch{LastPolledAt: &now}); err != nil {
			internalError(c, deps, err)
			return
		}
		switch flow.Status {
		case store.FlowStatusPending:
			c.JSON(200, okData(gin.H{"status": "pending"}))
			return
		case store.FlowStatusDenied:
			c.JSON(200, okData(gin.H{"status": "denied"}))
			return
		case store.FlowStatusExpired:
			c.JSON(200, okData(gin.H{"status": "expired"}))
			return
		}
		if flow.Status != store.FlowStatusAuthorized || flow.UserID == "" {
			c.JSON(200, okData(gin.H{"status": "consumed"}))
			return
		}
		if deps.SecretBox == nil {
			c.JSON(503, fail(503, "平台未配置 PLATFORM_SECRET_KEY，无法签发凭据"))
			return
		}
		user, err := deps.Store.FindUserByID(ctx, flow.UserID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if user == nil || user.DisabledAt != nil {
			c.JSON(403, fail(403, "账号不可用"))
			return
		}
		claimed, err := deps.Store.ConsumeLoginFlow(ctx, flowID, now)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if claimed == nil {
			c.JSON(200, okData(gin.H{"status": "consumed"}))
			return
		}
		token := crypto.GenerateDeviceToken()
		tokenID, err := contracts.RandomUUIDv4()
		if err != nil {
			internalError(c, deps, err)
			return
		}
		ciphertext, err := deps.SecretBox.Encrypt(token)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if err := deps.Store.CreateDeviceToken(ctx, store.DeviceTokenRecord{
			ID: tokenID, UserID: user.ID, Name: flow.DeviceName, Platform: flow.DevicePlatform,
			TokenHash: crypto.HashToken(token), TokenCiphertext: ciphertext,
			TokenHint: crypto.DeviceTokenHint(token), CreatedAt: now,
		}); err != nil {
			internalError(c, deps, err)
			return
		}
		if _, err := deps.Store.UpdateLoginFlow(ctx, flowID, store.UpdateLoginFlowPatch{TokenID: &tokenID}); err != nil {
			internalError(c, deps, err)
			return
		}
		teams, err := deps.Store.ListTeamsForUser(ctx, user.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		teamRows := make([]any, 0, len(teams))
		for _, entry := range teams {
			teamRows = append(teamRows, gin.H{
				"id": entry.Team.ID, "name": entry.Team.Name, "role": string(entry.Role),
			})
		}
		c.JSON(200, okData(gin.H{
			"status": "authorized",
			"credentials": gin.H{
				"token": token, "token_id": tokenID, "token_hint": crypto.DeviceTokenHint(token),
			},
			"user": gin.H{
				"id": user.ID, "email": user.Email, "display_name": user.DisplayName,
			},
			"teams": teamRows,
		}))
	})

	// ---- 浏览器页面 ----

	router.GET("/app/platform/login", func(c *gin.Context) {
		ctx := c.Request.Context()
		flowID := strings.TrimSpace(c.Query("flow_id"))
		var flow *store.LoginFlowRecord
		var err error
		if flowID != "" {
			flow, err = deps.Store.GetLoginFlow(ctx, flowID)
			if err != nil {
				internalError(c, deps, err)
				return
			}
		}
		if flow == nil {
			c.Data(400, "text/html; charset=utf-8", []byte(renderMessagePage(messagePageOptions{
				Title: "链接无效", Message: "登录链接无效，请回到应用重新发起登录。", Tone: "error",
			})))
			return
		}
		if flow.Status == store.FlowStatusDenied {
			c.Data(200, "text/html; charset=utf-8", []byte(renderMessagePage(messagePageOptions{
				Title: "已取消", Message: "本次登录已取消，可关闭本页面。",
			})))
			return
		}
		if flow.Status == store.FlowStatusConsumed || flow.Status == store.FlowStatusAuthorized {
			c.Redirect(303, "/app/platform/authorized?flow_id="+flowID)
			return
		}
		if !flow.ExpiresAt.After(deps.Now()) {
			if flow.Status == store.FlowStatusPending {
				if _, err := deps.Store.UpdateLoginFlowIfStatus(ctx, flowID, store.FlowStatusPending,
					store.UpdateLoginFlowPatch{Status: ptr(store.FlowStatusExpired)}); err != nil {
					internalError(c, deps, err)
					return
				}
			}
			c.Data(410, "text/html; charset=utf-8", []byte(renderMessagePage(messagePageOptions{
				Title: "链接已过期", Message: "登录链接已过期，请回到应用重新发起登录。", Tone: "error",
			})))
			return
		}
		user, err := readSessionUser(ctx, deps, c)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if user == nil {
			c.Data(200, "text/html; charset=utf-8", []byte(renderLoginPage(loginPageOptions{
				FlowID: flowID, DeviceName: flow.DeviceName,
				TurnstileSiteKey: deps.Env.TurnstileSiteKey,
			})))
			return
		}
		c.Data(200, "text/html; charset=utf-8", []byte(renderAuthorizePage(authorizePageOptions{
			FlowID: flowID, DeviceName: flow.DeviceName, DevicePlatform: flow.DevicePlatform, Email: user.Email,
		})))
	})

	router.POST("/app/platform/login", func(c *gin.Context) {
		ctx := c.Request.Context()
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxLoginFormBytes)
		if err := c.Request.ParseForm(); err != nil {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		flowID := c.Request.FormValue("flow_id")
		email := strings.TrimSpace(c.Request.FormValue("email"))
		password := c.Request.FormValue("password")
		var flow *store.LoginFlowRecord
		var err error
		if flowID != "" {
			flow, err = deps.Store.GetLoginFlow(ctx, flowID)
			if err != nil {
				internalError(c, deps, err)
				return
			}
		}
		if flow == nil || flow.Status != store.FlowStatusPending || !flow.ExpiresAt.After(deps.Now()) {
			c.Data(400, "text/html; charset=utf-8", []byte(renderMessagePage(messagePageOptions{
				Title: "链接无效", Message: "登录链接无效或已过期，请回到应用重新发起登录。", Tone: "error",
			})))
			return
		}
		if len(email) > 254 {
			c.Data(400, "text/html; charset=utf-8", []byte(renderLoginPage(loginPageOptions{
				FlowID: flowID, DeviceName: flow.DeviceName, Error: "邮箱格式不正确。",
				TurnstileSiteKey: deps.Env.TurnstileSiteKey,
			})))
			return
		}
		if deps.PlatformLoginLimiter == nil {
			rootMFAUnavailable(c)
			return
		}
		ip := authRequestIP(c, deps)
		allowed, retryAfter, err := checkLoginRateLimits(ctx, deps, c, deps.PlatformLoginLimiter, email)
		if err != nil {
			// 此表单也签发控制台会话；限流故障不能退化为无限次密码验证。
			c.Data(503, "text/html; charset=utf-8", []byte(renderMessagePage(messagePageOptions{Title: "登录暂不可用", Message: "登录验证暂不可用，请稍后重试。", Tone: "error"})))
			return
		}
		if !allowed {
			setRetryAfter(c, retryAfter)
			c.Data(429, "text/html; charset=utf-8", []byte(renderLoginPage(loginPageOptions{
				FlowID: flowID, DeviceName: flow.DeviceName, Error: "尝试次数过多，请稍后再试。",
				TurnstileSiteKey: deps.Env.TurnstileSiteKey,
			})))
			return
		}
		if err := requireTurnstile(ctx, deps, c, c.Request.FormValue("cf-turnstile-response")); err != nil {
			deps.Logger.Warn("设备登录人机验证未通过", "error", err)
			c.Data(turnstileHTMLStatus(err), "text/html; charset=utf-8", []byte(renderLoginPage(loginPageOptions{
				FlowID: flowID, DeviceName: flow.DeviceName, Error: turnstileHTMLMessage(err), Email: email,
				TurnstileSiteKey: deps.Env.TurnstileSiteKey,
			})))
			return
		}
		var user *store.UserRecord
		if email != "" {
			user, err = deps.Store.FindUserByEmail(ctx, email)
			if err != nil {
				internalError(c, deps, err)
				return
			}
		}
		allowed, retryAfter, err = checkRootLoginLimit(ctx, deps, user)
		if err != nil {
			deps.Logger.Error("设备登录 root 限流不可用，拒绝登录", "error", err)
			c.Data(503, "text/html; charset=utf-8", []byte(renderMessagePage(messagePageOptions{
				Title: "登录暂不可用", Message: "登录验证暂不可用，请稍后重试。", Tone: "error",
			})))
			return
		}
		if !allowed {
			setRetryAfter(c, retryAfter)
			c.Data(429, "text/html; charset=utf-8", []byte(renderLoginPage(loginPageOptions{
				FlowID: flowID, DeviceName: flow.DeviceName, Error: "尝试次数过多，请稍后再试。", Email: email,
				TurnstileSiteKey: deps.Env.TurnstileSiteKey,
			})))
			return
		}
		valid, gateBusy := verifyLoginPasswordWithGate(deps, user, password)
		if gateBusy {
			setRetryAfter(c, time.Second)
			c.Data(429, "text/html; charset=utf-8", []byte(renderLoginPage(loginPageOptions{
				FlowID: flowID, DeviceName: flow.DeviceName, Error: "登录请求过多，请稍后再试。", Email: email,
				TurnstileSiteKey: deps.Env.TurnstileSiteKey,
			})))
			return
		}
		if !valid {
			userID := ""
			if user != nil {
				userID = user.ID
			}
			writeAuthAudit(ctx, deps, userID, "auth.device.login", "failure", ip,
				map[string]any{"reason": loginFailureReason(user)})
			c.Data(401, "text/html; charset=utf-8", []byte(renderLoginPage(loginPageOptions{
				FlowID: flowID, DeviceName: flow.DeviceName, Error: "邮箱或密码不正确。", Email: email,
				TurnstileSiteKey: deps.Env.TurnstileSiteKey,
			})))
			return
		}
		if !allowRootMFAPlatformPasswordLogin(ctx, deps, c, user) {
			return
		}
		rehashPasswordIfNeeded(ctx, deps, user, password)
		if err := loginWithSession(ctx, deps, c, user); err != nil {
			internalError(c, deps, err)
			return
		}
		writeAuthAudit(ctx, deps, user.ID, "auth.device.login", "success", ip, nil)
		if user.Role == store.RoleRoot {
			notifySecurityEvent(deps, "root.device_login", "超级管理员通过设备授权页登录 user="+user.ID+" ip="+ip)
		}
		c.Redirect(303, "/app/platform/login?flow_id="+flowID)
	})

	router.POST("/app/platform/authorize", func(c *gin.Context) {
		ctx := c.Request.Context()
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxLoginFormBytes)
		if err := c.Request.ParseForm(); err != nil {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		flowID := c.Request.FormValue("flow_id")
		decision := c.Request.FormValue("decision")
		user, err := readSessionUser(ctx, deps, c)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if user == nil {
			c.Redirect(303, "/app/platform/login?flow_id="+flowID)
			return
		}
		var flow *store.LoginFlowRecord
		if flowID != "" {
			flow, err = deps.Store.GetLoginFlow(ctx, flowID)
			if err != nil {
				internalError(c, deps, err)
				return
			}
		}
		if flow == nil {
			c.Data(400, "text/html; charset=utf-8", []byte(renderMessagePage(messagePageOptions{
				Title: "链接无效", Message: "登录链接无效，请回到应用重新发起登录。", Tone: "error",
			})))
			return
		}
		if flow.Status == store.FlowStatusAuthorized || flow.Status == store.FlowStatusConsumed {
			c.Redirect(303, "/app/platform/authorized?flow_id="+flowID)
			return
		}
		if flow.Status != store.FlowStatusPending || !flow.ExpiresAt.After(deps.Now()) {
			c.Data(410, "text/html; charset=utf-8", []byte(renderMessagePage(messagePageOptions{
				Title: "链接已过期", Message: "登录链接已过期，请回到应用重新发起登录。", Tone: "error",
			})))
			return
		}
		if decision == "approve" {
			now := deps.Now()
			updated, err := deps.Store.UpdateLoginFlowIfStatus(ctx, flowID, store.FlowStatusPending, store.UpdateLoginFlowPatch{
				Status: ptr(store.FlowStatusAuthorized), UserID: &user.ID, AuthorizedAt: &now,
			})
			if err != nil {
				internalError(c, deps, err)
				return
			}
			if updated == nil {
				// 并发的另一授权/拒绝/过期请求已先完成状态转换。
				current, getErr := deps.Store.GetLoginFlow(ctx, flowID)
				if getErr != nil {
					internalError(c, deps, getErr)
					return
				}
				if current != nil && (current.Status == store.FlowStatusAuthorized || current.Status == store.FlowStatusConsumed) {
					c.Redirect(303, "/app/platform/authorized?flow_id="+flowID)
					return
				}
				c.Data(409, "text/html; charset=utf-8", []byte(renderMessagePage(messagePageOptions{
					Title: "授权状态已变化", Message: "本次登录授权已被处理，请回到应用重新发起登录。", Tone: "error",
				})))
				return
			}
			c.Redirect(303, "/app/platform/authorized?flow_id="+flowID)
			return
		}
		if _, err := deps.Store.UpdateLoginFlowIfStatus(ctx, flowID, store.FlowStatusPending,
			store.UpdateLoginFlowPatch{Status: ptr(store.FlowStatusDenied)}); err != nil {
			internalError(c, deps, err)
			return
		}
		c.Data(200, "text/html; charset=utf-8", []byte(renderMessagePage(messagePageOptions{
			Title: "已取消", Message: "已取消本次授权，可关闭本页面。",
		})))
	})

	router.GET("/app/platform/authorized", func(c *gin.Context) {
		ctx := c.Request.Context()
		flowID := strings.TrimSpace(c.Query("flow_id"))
		var flow *store.LoginFlowRecord
		var err error
		if flowID != "" {
			flow, err = deps.Store.GetLoginFlow(ctx, flowID)
			if err != nil {
				internalError(c, deps, err)
				return
			}
		}
		if flow == nil || (flow.Status != store.FlowStatusAuthorized && flow.Status != store.FlowStatusConsumed) {
			c.Data(200, "text/html; charset=utf-8", []byte(renderMessagePage(messagePageOptions{
				Title: "状态未知", Message: "授权状态未知，请回到应用查看登录结果。",
			})))
			return
		}
		c.Data(200, "text/html; charset=utf-8", []byte(renderAuthorizedPage(flowID, contracts.BuildPlatformDeepLinkCallbackURL(flowID))))
	})
}

func readPollToken(c *gin.Context) (string, bool) {
	header := strings.TrimSpace(c.GetHeader("authorization"))
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return "", false
	}
	token := strings.TrimSpace(header[7:])
	if token == "" {
		return "", false
	}
	return token, true
}

func ptr[T any](v T) *T { return &v }

func splitHeaderList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

// sha256Bytes/base64urlNoPad:ETag 计算用。
func sha256Bytes(data []byte) [32]byte { return sha256.Sum256(data) }

func base64urlNoPad(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

func internalError(c *gin.Context, deps *Deps, err error) {
	deps.Logger.Error("请求处理失败", "path", c.Request.URL.Path, "error", err)
	c.JSON(500, fail(500, "服务器内部错误"))
}
