package apiapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

const mediaAliasesKey = "_privacy.media-model-aliases.v1"
const mediaAliasesContextKey = "media.public-identities"

type mediaModelAliases struct {
	Version int               `json:"version"`
	Models  map[string]string `json:"models"`
}

// 显示名不会隐藏请求中的内部 ID。持久化随机别名与模型配置分离，
// 改名、换渠道或删除目录都不改变历史身份；CAS 保证多实例首次分配一致。
func ensureMediaAliases(ctx context.Context, db store.PlatformStore, ids []string) (map[string]string, error) {
	for range 20 {
		raw, err := db.GetConfigValue(ctx, mediaAliasesKey)
		if err != nil {
			return nil, err
		}
		value := mediaModelAliases{Version: 1, Models: map[string]string{}}
		if len(raw) > 0 && (json.Unmarshal(raw, &value) != nil || value.Version != 1 || value.Models == nil) {
			return nil, errors.New("media identities unavailable")
		}
		seen := map[string]bool{}
		for id, alias := range value.Models {
			if id == "" || strings.HasPrefix(id, "mm_") || !strings.HasPrefix(alias, "mm_") || len(alias) != 35 || seen[alias] {
				return nil, errors.New("media identities invalid")
			}
			if _, err := uuid.Parse(alias[3:]); err != nil {
				return nil, errors.New("media identities invalid")
			}
			seen[alias] = true
		}
		changed := false
		for _, id := range ids {
			if id == "" || value.Models[id] != "" {
				continue
			}
			if strings.HasPrefix(id, "mm_") {
				return nil, errors.New("media identity namespace reserved")
			}
			alias := "mm_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			if seen[alias] {
				return nil, errors.New("media identity collision")
			}
			value.Models[id], seen[alias], changed = alias, true, true
		}
		if !changed {
			return value.Models, nil
		}
		next, _ := json.Marshal(value)
		ok, err := db.CompareAndSwapConfigValue(ctx, mediaAliasesKey, raw, next)
		if err != nil {
			return nil, err
		}
		if ok {
			return value.Models, nil
		}
	}
	return nil, errors.New("media identities busy")
}

// 只映射协议中的模型引用字段，不能替换提示词、创作正文或显示名称。
func mapMediaModelRefs(value any, parent string, mapID func(string) string) {
	switch data := value.(type) {
	case map[string]any:
		for key, item := range data {
			if key == "modelId" || key == "imageModelId" || key == "videoModelId" || key == "videoModel" || key == "imageModel" || key == "previousVideoModel" || (key == "id" && parent == "models") {
				if id, ok := item.(string); ok && id != "" {
					data[key] = mapID(id)
				}
			} else if key == "verifiedModelIds" {
				if ids, ok := item.([]any); ok {
					for i, raw := range ids {
						if id, ok := raw.(string); ok {
							ids[i] = mapID(id)
						}
					}
				}
			} else {
				mapMediaModelRefs(item, key, mapID)
			}
		}
	case []any:
		for _, item := range data {
			mapMediaModelRefs(item, parent, mapID)
		}
	}
}

func mediaJSONValue(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	err = decoder.Decode(&result)
	return result, err
}

func resolveMediaBodyAliases(c *gin.Context, out any) bool {
	raw, exists := c.Get(mediaAliasesContextKey)
	if !exists {
		return true
	} // 管理接口继续编辑内部目录。
	aliases := raw.(map[string]string)
	if len(aliases) == 0 {
		return true
	}
	value, err := mediaJSONValue(out)
	if err != nil {
		c.JSON(400, fail(400, "视频参数不合法"))
		return false
	}
	reverse := make(map[string]string, len(aliases))
	for id, alias := range aliases {
		reverse[alias] = id
	}
	mapMediaModelRefs(value, "", func(id string) string {
		if internal, exists := reverse[id]; exists {
			return internal
		}
		return id // 旧 ID 继续进入原有模型授权及幂等校验。
	})
	encoded, _ := json.Marshal(value)
	if err := json.Unmarshal(encoded, out); err != nil {
		c.JSON(400, fail(400, "视频参数不合法"))
		return false
	}
	return true
}

func publicMediaData(c *gin.Context, deps *Deps, value any) {
	data, err := mediaJSONValue(value)
	if err != nil {
		internalError(c, deps, err)
		return
	}
	ids := []string{}
	mapMediaModelRefs(data, "", func(id string) string { ids = append(ids, id); return id })
	aliases, err := ensureMediaAliases(c.Request.Context(), deps.Store, ids)
	if err != nil {
		internalError(c, deps, err)
		return
	}
	mapMediaModelRefs(data, "", func(id string) string { return aliases[id] })
	projectMediaDiagnostics(data)
	c.Header("Cache-Control", "no-store")
	c.JSON(200, okData(data))
}

func projectMediaDiagnostics(value any) {
	switch data := value.(type) {
	case map[string]any:
		for key, item := range data {
			switch key {
			case "videoTaskId":
				if id, ok := item.(string); ok && id != "" {
					digest := sha256.Sum256([]byte("media-task:" + id))
					data[key] = "task_" + hex.EncodeToString(digest[:16])
				}
			case "providerError":
				if diagnostic, ok := item.(map[string]any); ok {
					code, _ := diagnostic["code"].(string)
					category := ""
					if classified := media.PublicProviderError(code); classified != nil {
						category = classified.Category
					}
					// 原实现把未知原因归为服务不可用；这里只保留精确已知分类，避免给用户错误建议。
					publicCode := "media_unknown"
					switch {
					case code == "ModelNotOpen":
						publicCode, category = "media_model_not_open", "authentication"
					case category == "content_policy" && strings.HasSuffix(code, ".PrivacyInformation"):
						publicCode, category = "media_privacy_information", "content_policy"
					case category == "content_policy" && strings.HasSuffix(code, ".PolicyViolation"):
						publicCode, category = "media_policy_violation", "content_policy"
					case slices.Contains([]string{"content_policy", "invalid_input", "authentication", "quota", "rate_limit", "service_unavailable"}, category):
						publicCode = "media_" + category
					default:
						category = ""
					}
					projected := map[string]any{"code": publicCode}
					if category != "" {
						projected["category"] = category
					}
					data[key] = projected
				}
			case "error", "archiveError":
				if message, ok := item.(string); ok {
					data[key] = publicMediaErrorCode(message)
				}
			default:
				projectMediaDiagnostics(item)
			}
		}
	case []any:
		for _, item := range data {
			projectMediaDiagnostics(item)
		}
	}
}
