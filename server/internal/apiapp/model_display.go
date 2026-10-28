package apiapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

type modelDisplayEntry struct {
	ID              string   `json:"id"`
	Kind            string   `json:"kind"`
	DisplayName     string   `json:"displayName"`
	ModelIDs        []string `json:"modelIds"`
	Resolutions     []string `json:"resolutions"`
	ExpectedVersion string   `json:"expectedVersion"`
}

func displayVersion(value any) string {
	// Postgres JSONB 会重排对象键；版本按规范化内容计算，不能让成功写入返回的版本立即失效。
	if raw, ok := value.(json.RawMessage); ok {
		var normalized any
		if json.Unmarshal(raw, &normalized) == nil {
			value = normalized
		}
	}
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func modelDisplayEntries(models []store.PlatformModelRecord, mediaModels []media.Model, mediaVersion string) []modelDisplayEntry {
	entries := make([]modelDisplayEntry, 0, len(models)+len(mediaModels))
	for _, model := range models {
		name := store.ModelDisplayName(model.ModelID, model.DisplayName)
		if name == "" {
			name = "文字模型"
		}
		entries = append(entries, modelDisplayEntry{ID: "text:" + model.ModelID, Kind: "text", DisplayName: name, ModelIDs: []string{model.ModelID}, Resolutions: []string{}, ExpectedVersion: displayVersion(model.DisplayName)})
	}
	groups := map[string]int{}
	for _, model := range mediaModels {
		id := publicMediaModelGroupID(model)
		index, exists := groups[id]
		if !exists {
			index = len(entries)
			groups[id] = index
			entries = append(entries, modelDisplayEntry{ID: id, Kind: model.Kind, DisplayName: publicMediaModelGroupName(model), ModelIDs: []string{}, Resolutions: []string{}, ExpectedVersion: mediaVersion})
		}
		entry := &entries[index]
		if name := publicMediaModelGroupName(model); name < entry.DisplayName {
			entry.DisplayName = name
		}
		entry.ModelIDs = append(entry.ModelIDs, model.ID)
		resolutions := model.Resolutions
		if model.Kind == "image" {
			resolutions = media.ImageResolutions(model)
		}
		for _, resolution := range resolutions {
			found := false
			for _, previous := range entry.Resolutions {
				if previous == resolution {
					found = true
					break
				}
			}
			if !found {
				entry.Resolutions = append(entry.Resolutions, resolution)
			}
		}
	}
	return entries
}

func loadDisplayCatalog(ctx context.Context, deps *Deps) ([]store.PlatformModelRecord, []media.Model, []byte, error) {
	models, err := deps.Store.ListModels(ctx, true)
	if err != nil {
		return nil, nil, nil, err
	}
	raw, err := deps.Store.GetConfigValue(ctx, media.ConfigKey)
	if err != nil {
		return nil, nil, nil, err
	}
	mediaModels := []media.Model{}
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &mediaModels)
	}
	return models, mediaModels, raw, err
}

func registerModelDisplayRoutes(group *gin.RouterGroup, deps *Deps) {
	group.GET("/model-display", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		models, mediaModels, raw, err := loadDisplayCatalog(c.Request.Context(), deps)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.Header("cache-control", "no-store")
		c.JSON(200, okData(gin.H{"entries": modelDisplayEntries(models, mediaModels, displayVersion(json.RawMessage(raw)))}))
	})
	group.PUT("/model-display", func(c *gin.Context) {
		ctx := c.Request.Context()
		actor, ok := requireRoot(ctx, deps, c)
		if !ok {
			return
		}
		body, ok := readObjectBody(c)
		if !ok {
			c.JSON(400, fail(400, "请求参数不合法"))
			return
		}
		if !rejectUnknownFields(c, body, "id", "displayName", "expectedVersion") {
			return
		}
		id, idOK := body["id"].(string)
		name, nameOK := body["displayName"].(string)
		name = strings.TrimSpace(name)
		version, versionOK := body["expectedVersion"].(string)
		if !idOK || id == "" || !nameOK || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 120 || !versionOK || version == "" {
			c.JSON(400, fail(400, "展示名称需为 1–120 字符，并提供当前版本"))
			return
		}
		models, mediaModels, raw, err := loadDisplayCatalog(ctx, deps)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		entries := modelDisplayEntries(models, mediaModels, displayVersion(json.RawMessage(raw)))
		var target *modelDisplayEntry
		for i := range entries {
			if entries[i].ID == id {
				target = &entries[i]
				break
			}
		}
		if target == nil {
			c.JSON(404, fail(404, "模型不存在"))
			return
		}
		if target.ExpectedVersion != version {
			c.JSON(409, fail(409, "模型目录已更新，请刷新后重试"))
			return
		}
		var changed bool
		if target.Kind == "text" {
			for _, model := range models {
				if model.ModelID == target.ModelIDs[0] {
					changed, err = deps.Store.CompareAndSwapModelDisplayName(ctx, model.ModelID, model.DisplayName, name)
					target.ExpectedVersion = displayVersion(name)
					break
				}
			}
		} else {
			// 媒体各档共享同一逻辑组，只改 DisplayName；整份目录 CAS 避免覆盖同时发生的价格或能力编辑。
			for i := range mediaModels {
				if publicMediaModelGroupID(mediaModels[i]) == id {
					mediaModels[i].DisplayName = name
				}
			}
			var updated []byte
			updated, err = json.Marshal(mediaModels)
			if err == nil {
				changed, err = deps.Store.CompareAndSwapConfigValue(ctx, media.ConfigKey, raw, updated)
			}
			target.ExpectedVersion = displayVersion(json.RawMessage(updated))
		}
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if !changed {
			c.JSON(409, fail(409, "模型目录已更新，请刷新后重试"))
			return
		}
		target.DisplayName = name
		writeAudit(ctx, deps, actor, "model.display.update", "model_display", id, gin.H{"displayName": name, "kind": target.Kind})
		if target.Kind == "text" {
			deps.ConfigEvents.Emit(events.DomainModels)
		}
		c.Header("cache-control", "no-store")
		c.JSON(200, okData(gin.H{"entry": target}))
	})
}

type usageModelDisplay struct{ Key, Name, Kind string }
type modelDisplayProjection struct{ models map[string]usageModelDisplay }

const modelDisplayProjectionKey = "console.modelDisplayProjection"

func prepareModelDisplay(c *gin.Context, deps *Deps) bool {
	if _, exists := c.Get(modelDisplayProjectionKey); exists {
		return true
	}
	models, mediaModels, _, err := loadDisplayCatalog(c.Request.Context(), deps)
	if err != nil {
		internalError(c, deps, err)
		return false
	}
	projection := modelDisplayProjection{models: map[string]usageModelDisplay{}}
	for _, entry := range modelDisplayEntries(models, mediaModels, "") {
		kind, prefix := entry.Kind, "text:"
		if kind != "text" {
			prefix = "media:"
		}
		display := usageModelDisplay{Key: prefix + displayVersion(entry.ID), Name: entry.DisplayName, Kind: kind}
		for _, id := range entry.ModelIDs {
			if kind != "text" {
				id = "media:" + id
			}
			projection.models[id] = display
		}
	}
	c.Set(modelDisplayProjectionKey, projection)
	return true
}

func usageDisplay(c *gin.Context, model string) usageModelDisplay {
	// 网页工具独立按次结算，不能落入未知文字模型而混淆账单与筛选。
	if model == "deepseek:web-search" || model == "deepseek:web-fetch" {
		name := "网页搜索"
		if model == "deepseek:web-fetch" {
			name = "网页抓取"
		}
		return usageModelDisplay{Key: "tool:" + displayVersion(model), Name: name, Kind: "tool"}
	}
	if model == "media:reference-analysis" {
		return usageModelDisplay{Key: model, Name: "参考视频分析", Kind: "media"}
	}
	if value, exists := c.Get(modelDisplayProjectionKey); exists {
		if display, ok := value.(modelDisplayProjection).models[model]; ok {
			return display
		}
	}
	// 删除目录的历史账本仍保留原始身份，但对外不能回退到带供应商信息的内部 ID。
	prefix, name, kind := "text:", "文字模型", "text"
	if strings.HasPrefix(model, "media:") {
		prefix, name, kind = "media:", "媒体模型", "media"
	}
	return usageModelDisplay{Key: prefix + displayVersion(model), Name: name, Kind: kind}
}

func publicUsageModel(c *gin.Context, model string) string {
	user, _ := c.Get(consoleUserKey)
	if user != nil && user.(*store.UserRecord).Role == store.RoleRoot {
		return model
	}
	return usageDisplay(c, model).Key
}

func aggregateUsageForConsole(c *gin.Context, rows []store.UsageAggregateRow) []aggregatePublic {
	result := []aggregatePublic{}
	positions := map[string]int{}
	for _, row := range rows {
		display := usageDisplay(c, row.Model)
		key := publicUsageModel(c, row.Model)
		if index, exists := positions[key]; exists {
			target := &result[index]
			target.Requests += row.Requests
			target.InputTokens += row.InputTokens
			target.OutputTokens += row.OutputTokens
			target.CacheReadTokens += row.CacheReadTokens
			target.CacheCreationTokens += row.CacheCreationTokens
			target.CreditsMicro += row.CreditsMicro
			target.LatencyMsSum += row.LatencyMsSum
		} else {
			value := toPublicAggregate(row)
			value.Model = key
			value.ModelDisplayName = display.Name
			value.ModelKind = display.Kind
			positions[key] = len(result)
			result = append(result, value)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CreditsMicro != result[j].CreditsMicro {
			return result[i].CreditsMicro > result[j].CreditsMicro
		}
		return result[i].Model < result[j].Model
	})
	return result
}

func resolveUsageModelFilter(c *gin.Context, deps *Deps, filter *store.UsageFilter) bool {
	if !prepareModelDisplay(c, deps) {
		return false
	}
	user, _ := c.Get(consoleUserKey)
	if user == nil || user.(*store.UserRecord).Role == store.RoleRoot || filter.Model == "" {
		return true
	}
	key := filter.Model
	// 只反查当前授权范围，不让对外筛选键突破原团队、成员或时间范围。
	filter.Model = ""
	rows, err := deps.Store.AggregateUsage(c.Request.Context(), *filter)
	if err != nil {
		internalError(c, deps, err)
		return false
	}
	filter.Models = []string{}
	for _, row := range rows {
		if usageDisplay(c, row.Model).Key == key {
			filter.Models = append(filter.Models, row.Model)
		}
	}
	return true
}

func exportUsageModel(c *gin.Context, model string) string {
	user, _ := c.Get(consoleUserKey)
	if user != nil && user.(*store.UserRecord).Role == store.RoleRoot {
		return model
	}
	return usageDisplay(c, model).Name
}
