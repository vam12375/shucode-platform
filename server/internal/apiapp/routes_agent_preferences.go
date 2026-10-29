package apiapp

import (
	"bytes"
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
		if record == nil {
			if c.GetHeader("if-none-match") == `"agent-media-0"` {
				c.Header("etag", `"agent-media-0"`)
				c.Status(http.StatusNotModified)
				return
			}
			c.Header("cache-control", "private, no-cache")
			c.Header("etag", `"agent-media-0"`)
			c.JSON(http.StatusOK, okData(agentMediaPreferencesResponse{
				Version: 0, UpdatedAt: "", Preferences: json.RawMessage(`{}`),
			}))
			return
		}
		if c.GetHeader("if-none-match") == agentMediaPreferencesETag(record.Version) {
			c.Header("etag", agentMediaPreferencesETag(record.Version))
			c.Status(http.StatusNotModified)
			return
		}
		writeAgentMediaPreferences(c, record)
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
			c.Header("etag", agentMediaPreferencesETag(current.Version))
			c.JSON(http.StatusConflict, gin.H{"code": http.StatusConflict, "msg": "偏好版本冲突，请重新读取", "data": agentMediaPreferencesPayload(current)})
			return
		}
		if err != nil {
			internalError(c, deps, err)
			return
		}
		writeAgentMediaPreferences(c, record)
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
	return auth, true
}

func agentMediaPreferencesETag(version int64) string {
	return `"agent-media-` + strconv.FormatInt(version, 10) + `"`
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
	version, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || version < 0 {
		return nil, false
	}
	return &version, true
}

func agentMediaPreferencesPayload(record *store.AgentMediaPreferencesRecord) agentMediaPreferencesResponse {
	return agentMediaPreferencesResponse{Version: record.Version, UpdatedAt: record.UpdatedAt.UTC().Format(time.RFC3339Nano), Preferences: json.RawMessage(append([]byte(nil), record.Preferences...))}
}

func writeAgentMediaPreferences(c *gin.Context, record *store.AgentMediaPreferencesRecord) {
	c.Header("cache-control", "private, no-cache")
	c.Header("etag", agentMediaPreferencesETag(record.Version))
	c.JSON(http.StatusOK, okData(agentMediaPreferencesPayload(record)))
}
