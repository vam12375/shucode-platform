package apiapp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

const agentMediaPreferencesPath = "/api/v1/client/preferences/agent-media"

type agentMediaPreferencesResponse struct {
	Version     int64           `json:"version"`
	UpdatedAt   string          `json:"updatedAt"`
	Preferences json.RawMessage `json:"preferences"`
}

type agentMediaPreferencesRequest struct {
	Preferences json.RawMessage `json:"preferences"`
}

var agentMediaPreferenceKeys = map[string]int{"imageModelId": 200, "videoModelId": 200, "imageResolution": 100, "videoResolution": 100, "imageAspectRatio": 20, "videoAspectRatio": 20}

func validateAgentMediaPreferences(raw json.RawMessage) ([]byte, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	for key, value := range object {
		maxLength, ok := agentMediaPreferenceKeys[key]
		if !ok {
			return nil, false
		}
		var text string
		if json.Unmarshal(value, &text) != nil || strings.TrimSpace(text) == "" || len([]rune(text)) > maxLength {
			return nil, false
		}
		object[key] = json.RawMessage(strconv.Quote(strings.TrimSpace(text)))
	}
	encoded, err := json.Marshal(object)
	return encoded, err == nil
}

func registerAgentMediaPreferencesRoutes(router *gin.Engine, deps *Deps) {
	router.GET(agentMediaPreferencesPath, func(c *gin.Context) {
		auth, ok := authenticateAgentMediaPreferences(c, deps)
		if !ok {
			return
		}
		record, err := deps.Store.GetAgentMediaPreferences(c.Request.Context(), auth.User.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		writeAgentMediaPreferences(c, deps, auth, record, http.StatusOK)
	})

	router.PUT(agentMediaPreferencesPath, func(c *gin.Context) {
		auth, ok := authenticateAgentMediaPreferences(c, deps)
		if !ok {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var input agentMediaPreferencesRequest
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || len(bytes.TrimSpace(input.Preferences)) == 0 {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "preferences 必须是 JSON 对象"))
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "请求体只能包含一个 JSON 对象"))
			return
		}
		if !resolveMediaBodyAliases(c, &input) {
			return
		}
		preferences, valid := validateAgentMediaPreferences(input.Preferences)
		if !valid {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "preferences 必须是 JSON 对象"))
			return
		}
		expected, hasExpected := parseAgentMediaIfMatch(c.GetHeader("if-match"))
		if !hasExpected && strings.TrimSpace(c.GetHeader("if-match")) != "" {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "If-Match 无效"))
			return
		}
		record, err := deps.Store.SaveAgentMediaPreferences(c.Request.Context(), auth.User.ID, expected, preferences, deps.Now().UTC())
		if errors.Is(err, store.ErrPreferenceVersionConflict) {
			current, getErr := deps.Store.GetAgentMediaPreferences(c.Request.Context(), auth.User.ID)
			if getErr != nil {
				internalError(c, deps, getErr)
				return
			}
			if current == nil {
				c.JSON(http.StatusConflict, fail(http.StatusConflict, "偏好版本冲突，请重新读取"))
				return
			}
			writeAgentMediaPreferences(c, deps, auth, current, http.StatusConflict)
			return
		}
		if err != nil {
			internalError(c, deps, err)
			return
		}
		writeAgentMediaPreferences(c, deps, auth, record, http.StatusOK)
		deps.ConfigEvents.Emit(events.DomainPreferences)
	})
}

func authenticateAgentMediaPreferences(c *gin.Context, deps *Deps) (*AuthenticatedDevice, bool) {
	auth, err := authenticateDevice(c.Request.Context(), deps, c)
	if err != nil {
		internalError(c, deps, err)
		return nil, false
	}
	if auth == nil {
		c.JSON(http.StatusUnauthorized, fail(http.StatusUnauthorized, "凭据无效或已吊销"))
		return nil, false
	}
	aliases, err := ensureMediaAliases(c.Request.Context(), deps.Store, nil)
	if err != nil {
		internalError(c, deps, err)
		return nil, false
	}
	c.Set(mediaAliasesContextKey, aliases)
	return auth, true
}

func parseAgentMediaIfMatch(value string) (*int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, true
	}
	if !strings.HasPrefix(value, `"agent-media-`) || !strings.HasSuffix(value, `"`) {
		return nil, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(value, `"agent-media-`), `"`)
	if strings.HasPrefix(raw, "public-") {
		parts := strings.Split(strings.TrimPrefix(raw, "public-"), "-")
		if len(parts) != 2 || len(parts[1]) != 16 {
			return nil, false
		}
		if _, err := hex.DecodeString(parts[1]); err != nil {
			return nil, false
		}
		raw = parts[0]
	}
	version, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || version < 0 {
		return nil, false
	}
	return &version, true
}

func agentMediaPreferencesPayload(record *store.AgentMediaPreferencesRecord) agentMediaPreferencesResponse {
	return agentMediaPreferencesResponse{Version: record.Version, UpdatedAt: record.UpdatedAt.UTC().Format(time.RFC3339Nano), Preferences: json.RawMessage(append([]byte(nil), record.Preferences...))}
}

// 偏好也是模型身份出口。只映射当前可用目录，过期/未授权的模型选择不回显；
// 未提交草稿不因此创建无限量全局别名，且不修改持久化偏好或其版本。
func writeAgentMediaPreferences(c *gin.Context, deps *Deps, auth *AuthenticatedDevice, record *store.AgentMediaPreferencesRecord, status int) {
	payload := agentMediaPreferencesResponse{Preferences: json.RawMessage(`{}`)}
	if record != nil {
		payload = agentMediaPreferencesPayload(record)
	}
	var preferences map[string]string
	if err := json.Unmarshal(payload.Preferences, &preferences); err != nil {
		internalError(c, deps, err)
		return
	}
	allowed := map[string]bool{}
	account, err := store.ResolveBillingAccount(c.Request.Context(), deps.Store, auth.User.ID)
	if err != nil {
		internalError(c, deps, err)
		return
	}
	if account != nil && account.Team.DisabledAt == nil && account.MemberDisabledAt == nil {
		policy, err := deps.Store.GetTeamPolicy(c.Request.Context(), account.Team.ID)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		models, err := deps.Media.Repo.Models(c.Request.Context())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		for _, model := range models {
			if store.TeamModelAllowed(policy, model.ID) {
				allowed[model.ID] = true
			}
		}
	}
	ids := []string{}
	for id := range allowed {
		ids = append(ids, id)
	}
	aliases, err := ensureMediaAliases(c.Request.Context(), deps.Store, ids)
	if err != nil {
		internalError(c, deps, err)
		return
	}
	reverse := map[string]string{}
	for id, alias := range aliases {
		reverse[alias] = id
	}
	for _, key := range []string{"imageModelId", "videoModelId"} {
		id, exists := preferences[key]
		if !exists {
			continue
		}
		if internal, exists := reverse[id]; exists {
			id = internal
		}
		if allowed[id] {
			preferences[key] = aliases[id]
		} else {
			delete(preferences, key)
		}
	}
	payload.Preferences, _ = json.Marshal(preferences)
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	// 旧 ETag 对应含内部 ID 的表示，不能用 304 让客户端继续沿用旧响应。
	etag := `"agent-media-public-` + strconv.FormatInt(payload.Version, 10) + "-" + hex.EncodeToString(digest[:8]) + `"`
	c.Header("cache-control", "private, no-cache")
	c.Header("etag", etag)
	if status == http.StatusOK && c.Request.Method == http.MethodGet && c.GetHeader("if-none-match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	if status == http.StatusConflict {
		c.JSON(status, gin.H{"code": status, "msg": "偏好版本冲突，请重新读取", "data": payload})
		return
	}
	c.JSON(status, okData(payload))
}
