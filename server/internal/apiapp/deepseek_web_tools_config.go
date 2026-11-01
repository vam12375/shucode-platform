package apiapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/webtools"
)

type webToolsConfigurationState struct {
	webtools.Config
	Source          string `json:"source"`
	Configured      bool   `json:"configured"`
	ProviderName    string `json:"providerName"`
	ProviderEnabled bool   `json:"providerEnabled"`
	HasKey          bool   `json:"hasKey"`
	BaseURL         string `json:"baseUrl"`
	CanConfigure    bool   `json:"canConfigure"`
}

func webToolsConfigState(ctx context.Context, deps *Deps) (webToolsConfigurationState, error) {
	c, found, err := webtools.Load(ctx, deps.Store)
	if err != nil {
		return webToolsConfigurationState{}, err
	}
	state := webToolsConfigurationState{Config: c, Source: "none", CanConfigure: deps.SecretBox != nil}
	if found {
		state.Source = "console"
	}
	if c.ProviderID != "" {
		p, err := deps.Store.GetUpstreamProvider(ctx, c.ProviderID)
		if err != nil {
			return webToolsConfigurationState{}, err
		}
		if p != nil {
			state.ProviderName, state.ProviderEnabled, state.HasKey, state.BaseURL = p.Name, p.Enabled, p.APIKeyCiphertext != "", p.BaseURL
		}
	}
	if c.Enabled {
		_, err := webtools.Resolve(ctx, deps.Store, deps.SecretBox, c)
		state.Configured = err == nil
	}
	return state, nil
}

func registerDeepSeekWebToolsConfigRoutes(router *gin.Engine, deps *Deps) {
	const path = "/console/api/deepseek-web-tools"
	respond := func(c *gin.Context) {
		state, err := webToolsConfigState(c.Request.Context(), deps)
		if err != nil {
			c.JSON(503, fail(503, "web_tools_configuration_unavailable"))
			return
		}
		c.JSON(200, okData(state))
	}
	writeError := func(c *gin.Context, err error) {
		switch {
		case errors.Is(err, webtools.ErrConflict):
			c.JSON(409, fail(409, "配置已被更新，请刷新后重试"))
		case errors.Is(err, webtools.ErrInvalid):
			c.JSON(400, fail(400, "网页工具配置无效"))
		default:
			c.JSON(503, fail(503, "web_tools_configuration_unavailable"))
		}
	}
	router.GET(path, func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		respond(c)
	})
	router.PUT(path, func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		body, ok := readObjectBodyLimited(c, 16<<10)
		if !ok || len(body) != 9 || !rejectUnknownFields(c, body, "expectedVersion", "enabled", "providerId", "searchEnabled", "fetchEnabled", "searchSaleMicro", "searchCostMicro", "fetchSaleMicro", "fetchCostMicro") {
			if !c.Writer.Written() {
				c.JSON(400, fail(400, "网页工具配置无效"))
			}
			return
		}
		expected, ok := body["expectedVersion"].(string)
		if !ok || expected == "" {
			c.JSON(400, fail(400, "缺少配置版本"))
			return
		}
		delete(body, "expectedVersion")
		body["version"] = expected
		raw, _ := json.Marshal(body)
		config, err := webtools.Parse(raw)
		if err != nil {
			writeError(c, err)
			return
		}
		// 只绑定显式选择的密钥 owner；即使草稿停用也验证非空供应商，不能静默选取其它模型商。
		if config.ProviderID != "" {
			probe := config
			probe.Enabled = true
			if _, err := webtools.Resolve(c.Request.Context(), deps.Store, deps.SecretBox, probe); err != nil {
				c.JSON(400, fail(400, "请选择启用、已配置密钥且使用 HTTPS 的工具供应商"))
				return
			}
		}
		saved, err := webtools.Save(c.Request.Context(), deps.Store, expected, config)
		if err != nil {
			writeError(c, err)
			return
		}
		writeAudit(c.Request.Context(), deps, actor, "web-tools.update", "web_tools", webtools.ConfigKey, gin.H{"providerId": saved.ProviderID, "version": saved.Version, "enabled": saved.Enabled, "searchEnabled": saved.SearchEnabled, "fetchEnabled": saved.FetchEnabled})
		deps.ConfigEvents.Emit(events.DomainPlatformConfig)
		respond(c)
	})
	router.DELETE(path, func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		body, ok := readObjectBodyLimited(c, 4096)
		if !ok || len(body) != 1 {
			c.JSON(400, fail(400, "缺少配置版本"))
			return
		}
		expected, ok := body["expectedVersion"].(string)
		if !ok {
			c.JSON(400, fail(400, "缺少配置版本"))
			return
		}
		// 删除保存停用墓碑；删掉 KV 会重新激活旧环境密钥，违背管理员的停用操作。
		saved, err := webtools.Save(c.Request.Context(), deps.Store, expected, webtools.Defaults())
		if err != nil {
			writeError(c, err)
			return
		}
		writeAudit(c.Request.Context(), deps, actor, "web-tools.clear", "web_tools", webtools.ConfigKey, gin.H{"version": saved.Version})
		deps.ConfigEvents.Emit(events.DomainPlatformConfig)
		respond(c)
	})
	router.POST(path+"/test", func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		body, ok := readObjectBodyLimited(c, 4096)
		if !ok || len(body) != 1 {
			c.JSON(400, fail(400, "请选择工具供应商"))
			return
		}
		providerID, ok := body["providerId"].(string)
		if !ok {
			c.JSON(400, fail(400, "请选择工具供应商"))
			return
		}
		config := webtools.Defaults()
		config.ProviderID, config.Enabled = providerID, true
		snapshot, err := webtools.Resolve(c.Request.Context(), deps.Store, deps.SecretBox, config)
		if err != nil {
			c.JSON(400, fail(400, "工具供应商不可用"))
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		// 连通测试只读模型列表，不提交付费 Search/Fetch；成功不等同于工具账号权限已开通。
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, snapshot.BaseURL+"/v1/models", nil)
		if err != nil {
			c.JSON(400, fail(400, "工具供应商地址无效"))
			return
		}
		req.Header.Set("Authorization", "Bearer "+snapshot.APIKey)
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if deps.HTTPClient != nil {
			*client = *deps.HTTPClient
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		}
		started := time.Now()
		res, callErr := client.Do(req)
		status := 0
		if res != nil {
			status = res.StatusCode
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
			_ = res.Body.Close()
		}
		ok = callErr == nil && status == 200
		writeAudit(c.Request.Context(), deps, actor, "web-tools.test", "web_tools", webtools.ConfigKey, gin.H{"providerId": providerID, "ok": ok, "status": status})
		c.JSON(200, okData(gin.H{"ok": ok, "status": status, "latencyMs": time.Since(started).Milliseconds(), "scope": "connectivity_auth_only"}))
	})
}
