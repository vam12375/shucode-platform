package apiapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/netguard"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

func NewMediaService(deps *Deps) *media.Service {
	root := os.Getenv("MEDIA_STORAGE_DIR")
	if root == "" {
		root = "data/media"
	}
	key := deps.Env.PlatformSecretKey
	if key == "" {
		key = uuid.NewString()
	}
	client := deps.HTTPClient
	if client == nil {
		client = &http.Client{Transport: netguard.NewTransport(false), Timeout: 60_000_000_000, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("redirect_forbidden") }}
	}
	s := &media.Service{Repo: media.Repository{Store: deps.Store}, Blobs: media.Blobs{Root: root, Origin: strings.TrimRight(deps.Env.PublicAPIOrigin, "/"), SigningKey: key, Client: client}, WorkerID: uuid.NewString()}
	// 配音使用独立管理员配置，不能误把视频供应商密钥当成 speech 授权。
	ttsClient := &http.Client{Transport: netguard.NewTransport(deps.Env.AllowPrivateUpstreamNetworks), Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("redirect_forbidden") }}
	if deps.HTTPClient != nil {
		copyClient := *deps.HTTPClient
		copyClient.CheckRedirect = ttsClient.CheckRedirect
		copyClient.Timeout = 2 * time.Minute
		ttsClient = &copyClient
	}
	s.Narration = &media.NarrationConfig{BaseURL: strings.TrimSpace(os.Getenv("MEDIA_TTS_BASE_URL")), Model: strings.TrimSpace(os.Getenv("MEDIA_TTS_MODEL")), Voice: strings.TrimSpace(os.Getenv("MEDIA_TTS_VOICE")), APIKey: strings.TrimSpace(os.Getenv("MEDIA_TTS_API_KEY")), Client: ttsClient, AllowInsecure: deps.Env.AllowInsecureUpstreamURLs}
	s.BackgroundMusicPath = strings.TrimSpace(os.Getenv("MEDIA_BGM_PATH"))
	s.ArchiveCanvasOutput = newCanvasArchivePublisher(deps, s.Blobs)
	parserClient := media.NewReferenceHTTPClient(90 * time.Second)
	s.SourceParser = &media.SourceParser{Client: parserClient, ResolveKey: func(ctx context.Context) (string, error) {
		return resolveSourceParserKey(ctx, deps)
	}}
	s.Provider = func(ctx context.Context, m media.Model) (media.Provider, error) {
		p, e := deps.Store.GetUpstreamProvider(ctx, m.ProviderID)
		if e != nil {
			return media.Provider{}, e
		}
		if p == nil || !p.Enabled || deps.SecretBox == nil {
			return media.Provider{}, fmt.Errorf("media_provider_unavailable")
		}
		key, e := deps.SecretBox.Decrypt(p.APIKeyCiphertext)
		if e != nil {
			return media.Provider{}, e
		}
		return media.Provider{BaseURL: p.BaseURL, Key: key, Adapter: m.Adapter, Client: client}, nil
	}
	return s
}
func mediaIdentity(c *gin.Context, deps *Deps) (*AuthenticatedDevice, *store.TeamMembership, bool) {
	a, e := authenticateDevice(c.Request.Context(), deps, c)
	if e != nil {
		internalError(c, deps, e)
		return nil, nil, false
	}
	if a == nil {
		c.JSON(401, fail(401, "请登录平台"))
		return nil, nil, false
	}
	m, e := store.ResolveUserTeam(c.Request.Context(), deps.Store, a.User.ID)
	if e != nil || m == nil {
		c.JSON(403, fail(403, "账号没有唯一有效团队"))
		return nil, nil, false
	}
	return a, m, true
}
func mediaError(c *gin.Context, deps *Deps, e error) {
	if errors.Is(e, media.ErrConflict) {
		c.JSON(409, fail(409, "制作单已更新，请刷新"))
		return
	}
	if errors.Is(e, media.ErrInvalid) {
		c.JSON(400, fail(400, "视频参数不合法"))
		return
	}
	deps.Logger.Warn("媒体操作失败", "error", e)
	c.JSON(422, fail(422, e.Error()))
}
func registerMediaRoutes(router *gin.Engine, deps *Deps) {
	if deps.Media == nil {
		deps.Media = NewMediaService(deps)
	}
	s := deps.Media
	registerMediaReferenceRoutes(router, deps)
	registerReferencePricingRoutes(router, deps)
	registerMediaAnalysisConfigRoutes(router, deps)
	registerMediaSourceRoutes(router, deps)
	registerCanvasGenerationRoutes(router, deps)
	router.GET("/api/v1/media/blobs/:team/:id", func(c *gin.Context) {
		team, id := c.Param("team"), c.Param("id")
		if !s.Blobs.Verify(team, id, c.Query("expires"), c.Query("signature")) {
			c.Status(403)
			return
		}
		assets, e := s.Repo.Assets(c.Request.Context(), team)
		if e != nil {
			internalError(c, deps, e)
			return
		}
		for _, a := range assets {
			if a.ID == id {
				// 签名 URL 允许 Desktop/Web 以 no-cors 跨源加载预览；只放宽此媒体响应，脚本 CORS 仍关闭。
				c.Header("Cross-Origin-Resource-Policy", "cross-origin")
				c.Header("Content-Type", a.Mime)
				c.Header("Content-Disposition", "inline")
				c.Header("Cache-Control", "private, max-age=300")
				c.File(s.Blobs.Path(team, id))
				return
			}
		}
		c.Status(404)
	})
	router.GET("/api/v1/media/overview", func(c *gin.Context) {
		a, m, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		models, e := s.Repo.Models(ctx)
		if e != nil {
			internalError(c, deps, e)
			return
		}
		policy, e := deps.Store.GetTeamPolicy(ctx, m.Team.ID)
		if e != nil {
			internalError(c, deps, e)
			return
		}
		allowed := []media.Model{}
		for _, model := range models {
			if store.TeamModelAllowed(policy, model.ID) {
				allowed = append(allowed, model)
			}
		}
		models = allowed
		assets, e := s.Repo.Assets(ctx, m.Team.ID)
		if e != nil {
			internalError(c, deps, e)
			return
		}
		visibleAssets := assets[:0]
		for _, asset := range assets {
			if asset.Internal {
				continue
			}
			asset.URL = s.Blobs.URL(m.Team.ID, asset.ID)
			visibleAssets = append(visibleAssets, asset)
		}
		assets = visibleAssets
		all, e := s.Repo.All(ctx)
		if e != nil {
			internalError(c, deps, e)
			return
		}
		projects := []media.ProjectView{}
		generations := []media.ProjectView{}
		for _, p := range all {
			if p.TeamID == m.Team.ID {
				if p.Canvas != nil {
					generations = append(generations, p.View())
				} else {
					projects = append(projects, p.View())
				}
			}
		}
		quota, _ := credits.ParseCreditsToMicro(m.Team.QuotaCredits)
		used, _ := credits.ParseCreditsToMicro(m.Team.UsedCredits)
		// 余额只由已结算账本决定；进行中的聊天和媒体不占用点数，负余额如实返回。
		pricing, e := s.Repo.ReferencePricing(c.Request.Context())
		if e != nil {
			c.JSON(503, fail(503, "reference_pricing_unavailable"))
			return
		}
		c.JSON(200, okData(gin.H{"user": gin.H{"id": a.User.ID, "name": a.User.DisplayName}, "team": gin.H{"id": m.Team.ID, "name": m.Team.Name, "availableMicro": quota - used, "reservedMicro": int64(0), "unlimited": m.Team.Unlimited}, "models": publicMediaModels(models), "assets": assets, "projects": projects, "generations": generations, "compositionAvailable": media.CompositionAvailable(), "referencePricing": pricing, "capabilities": s.FinishingCapabilities(ctx)}))
	})
	router.POST("/api/v1/media/assets", func(c *gin.Context) {
		_, m, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, media.MaxCanvasVideoBytes+4096)
		if e := c.Request.ParseMultipartForm(1 << 20); e != nil {
			c.JSON(413, fail(413, "素材最大50MB；图片20MB，音频15MB"))
			return
		}
		if c.Request.MultipartForm != nil {
			defer c.Request.MultipartForm.RemoveAll()
		}
		f, h, e := c.Request.FormFile("file")
		if e != nil {
			c.JSON(400, fail(400, "缺少素材"))
			return
		}
		defer f.Close()
		asset, e := s.UploadCanvasMedia(c.Request.Context(), m.Team.ID, h.Filename, c.Request.FormValue("product"), f)
		if e != nil {
			mediaError(c, deps, e)
			return
		}
		c.JSON(200, okData(asset))
	})
	router.POST("/api/v1/media/projects", func(c *gin.Context) {
		a, m, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		var body struct {
			ID    string      `json:"id"`
			Brief media.Brief `json:"brief"`
		}
		if !mediaBody(c, &body) {
			return
		}
		if !body.Brief.SourceOnly && !media.CompositionAvailable() {
			c.JSON(503, fail(503, "composition_dependencies_missing"))
			return
		}
		p, e := s.Create(c.Request.Context(), m.Team.ID, a.User.ID, a.Device.ID, body.ID, body.Brief)
		if e != nil {
			mediaError(c, deps, e)
			return
		}
		c.JSON(200, okData(p.View()))
	})
	router.GET("/api/v1/media/projects/:id", func(c *gin.Context) {
		_, m, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		p, e := s.Repo.Get(c.Request.Context(), m.Team.ID, c.Param("id"))
		if e != nil {
			internalError(c, deps, e)
			return
		}
		if p == nil {
			c.Status(404)
			return
		}
		c.JSON(200, okData(p.View()))
	})
	router.POST("/api/v1/media/projects/:id/:action", func(c *gin.Context) {
		auth, m, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		var body struct {
			Version        int64                `json:"version"`
			Analysis       media.Analysis       `json:"analysis"`
			InputID        string               `json:"inputId"`
			Error          string               `json:"error"`
			Plan           media.Plan           `json:"plan"`
			Brief          media.Brief          `json:"brief"`
			SourceAnalysis media.SourceAnalysis `json:"sourceAnalysis"`
		}
		if !mediaBody(c, &body) {
			return
		}
		if body.Version <= 0 {
			c.JSON(400, fail(400, "缺少制作单版本"))
			return
		}
		current, err := s.Repo.Get(c.Request.Context(), m.Team.ID, c.Param("id"))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if current != nil && current.SourceChecksum != "" && (current.UserID != auth.User.ID || current.DeviceID != auth.Device.ID) {
			c.JSON(403, fail(403, "video_analysis_scope_invalid"))
			return
		}
		var p *media.Project
		var e error
		switch c.Param("action") {
		case "analysis":
			p, e = s.Bind(c.Request.Context(), m.Team.ID, c.Param("id"), body.Version, body.Analysis)
		case "plan":
			p, e = s.SetPlan(c.Request.Context(), m.Team.ID, c.Param("id"), body.Version, body.InputID, body.Plan)
		case "source-analysis":
			p, e = s.SetSourceAnalysis(c.Request.Context(), m.Team.ID, c.Param("id"), body.Version, body.InputID, body.SourceAnalysis)
		case "configure":
			p, e = s.ConfigureReference(c.Request.Context(), m.Team.ID, c.Param("id"), body.Version, body.Brief)
		case "revise":
			p, e = s.Revise(c.Request.Context(), m.Team.ID, c.Param("id"), body.Version, body.Plan)
		case "analysis-failed":
			if body.InputID == "" && body.Error == "" {
				p, e = s.Action(c.Request.Context(), m.Team.ID, c.Param("id"), body.Version, "analysis-failed")
			} else {
				p, e = s.FailAnalysis(c.Request.Context(), m.Team.ID, c.Param("id"), body.Version, body.InputID, body.Error)
			}
		default:
			if c.Param("action") == "generate" && !media.CompositionAvailable() {
				c.JSON(503, fail(503, "composition_dependencies_missing"))
				return
			}
			p, e = s.Action(c.Request.Context(), m.Team.ID, c.Param("id"), body.Version, c.Param("action"))
		}
		if e != nil {
			mediaError(c, deps, e)
			return
		}
		c.JSON(200, okData(p.View()))
	})
	router.GET("/console/api/media/models", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		models, e := s.Repo.Models(c.Request.Context())
		if e != nil {
			internalError(c, deps, e)
			return
		}
		c.JSON(200, okData(consoleMediaModels(models)))
	})
	router.PUT("/console/api/media/models", func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		var models []media.Model
		if !mediaBody(c, &models) {
			return
		}
		if e := media.ValidateModels(models); e != nil {
			mediaError(c, deps, e)
			return
		}
		// 目录配置允许绑定尚未启用/尚未填密钥的供应商，便于先建模型再补密钥；
		// 真正提交生成时 s.Provider 仍会拒绝不可用供应商。
		for _, m := range models {
			providerIDs := []string{m.ProviderID}
			for _, route := range m.Routes {
				providerIDs = append(providerIDs, route.ProviderID)
			}
			for _, providerID := range providerIDs {
				p, e := deps.Store.GetUpstreamProvider(c.Request.Context(), providerID)
				if e != nil {
					internalError(c, deps, e)
					return
				}
				if p == nil {
					c.JSON(400, fail(400, "媒体模型绑定的上游供应商不存在"))
					return
				}
			}
		}
		data, _ := json.Marshal(models)
		if e := deps.Store.SetConfigValue(c.Request.Context(), media.ConfigKey, data); e != nil {
			internalError(c, deps, e)
			return
		}
		writeAudit(c.Request.Context(), deps, actor, "media.models.update", "media_catalog", media.ConfigKey, gin.H{"modelCount": len(models)})
		c.JSON(200, okData(consoleMediaModels(models)))
	})
	router.GET("/console/api/media/projects", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		all, e := s.Repo.All(c.Request.Context())
		if e != nil {
			internalError(c, deps, e)
			return
		}
		pending := []media.Project{}
		for _, p := range all {
			if p.Status == "reconciliation_required" || media.NarrationNeedsReconciliation(p) {
				pending = append(pending, p)
			}
		}
		c.JSON(200, okData(pending))
	})
	router.POST("/console/api/media/projects/:team/:id/reconcile", func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		var body media.Reconciliation
		if !mediaBody(c, &body) {
			return
		}
		p, e := s.Reconcile(c.Request.Context(), c.Param("team"), c.Param("id"), body)
		if e != nil {
			mediaError(c, deps, e)
			return
		}
		writeAudit(c.Request.Context(), deps, actor, "media.project.reconcile", "media_project", p.ID, gin.H{"teamId": p.TeamID, "shot": body.Shot, "outcome": body.Outcome, "reason": body.Reason})
		c.JSON(200, okData(p))
	})
}
func mediaBody(c *gin.Context, out any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(out); e != nil {
		c.JSON(400, fail(400, "视频参数不合法"))
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		c.JSON(400, fail(400, "视频参数不合法"))
		return false
	}
	return true
}

func publicMediaModels(models []media.Model) []gin.H {
	out := make([]gin.H, 0, len(models))
	groupNames := map[string]string{}
	for _, m := range models {
		id, name := publicMediaModelGroupID(m), publicMediaModelGroupName(m)
		// 名称不是身份；历史条目名称不一致时按稳定顺序选取一个公开组名。
		if previous, exists := groupNames[id]; !exists || name < previous {
			groupNames[id] = name
		}
	}
	for _, m := range models {
		resolutions := append([]string{}, m.Resolutions...)
		if m.Kind == "image" {
			resolutions = media.ImageResolutions(m)
		}
		durations := append([]int{}, m.Durations...)
		// 区间目录向旧客户端展开为枚举，避免它们拿到空 durations 而无法提交；
		// 新客户端优先读取 minDuration/maxDuration。
		if m.MinDuration > 0 && m.MaxDuration >= m.MinDuration {
			durations = m.AllowedDurations()
		}
		canvasCapability := media.CanvasCapabilitiesFor(m)
		if canvasCapability != nil {
			// 旧2—30配置不代表2.0全能参考支持；公开管理员配置与已适配时长的交集。
			durations = slices.DeleteFunc(durations, func(d int) bool { return d < 4 || d > 15 })
		}
		// 用户消耗用于客户端展示；供应商成本与内部价格版本只用于服务端计费及 root 控制台。
		name := groupNames[publicMediaModelGroupID(m)]
		model := gin.H{"id": m.ID, "name": name, "kind": m.Kind, "resolutions": resolutions, "maxReferenceImages": media.MaxReferenceImages(m), "durations": durations, "generateAudio": m.GenerateAudio, "priceMicro": m.PriceMicro}
		groupID := publicMediaModelGroupID(m)
		model["groupId"] = groupID
		model["groupName"] = groupNames[groupID]
		model["aspectRatios"] = media.ModelAspectRatios(m)
		if canvasCapability != nil {
			model["canvasCapabilities"] = canvasCapability
		}
		if m.MinDuration > 0 && m.MaxDuration >= m.MinDuration {
			minimum, maximum := m.MinDuration, m.MaxDuration
			if canvasCapability != nil {
				minimum, maximum = max(4, minimum), min(15, maximum)
			}
			if minimum <= maximum {
				model["minDuration"] = minimum
				model["maxDuration"] = maximum
			}
		}
		if m.VideoReference != nil {
			reference := gin.H{
				"maxDurationSeconds": m.VideoReference.MaxDurationSeconds,
				"maxImages":          m.VideoReference.MaxImages,
				"priceMicro":         m.VideoReference.PriceMicro,
			}
			// 可选能力仅在配置后下发，保持旧目录缺字段的wire语义。
			if m.VideoReference.MaxAudioSeconds > 0 {
				reference["maxAudioSeconds"] = m.VideoReference.MaxAudioSeconds
			}
			// 旧目录没有 token 费率时不下发零值，保持 wire 兼容。
			if m.VideoReference.TokenPriceMicroPerMillion > 0 {
				reference["tokenPriceMicroPerMillion"] = m.VideoReference.TokenPriceMicroPerMillion
			}
			model["videoReference"] = reference
		}
		out = append(out, model)
	}
	return out
}
