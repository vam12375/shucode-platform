package apiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

const sourceParserConfigKey = "media:source-parser:v1"

type storedSourceParserConfig struct {
	KeyCiphertext string `json:"keyCiphertext"`
}

func loadSourceParserConfiguration(ctx context.Context, deps *Deps) (string, string, error) {
	raw, err := deps.Store.GetConfigValue(ctx, sourceParserConfigKey)
	if err != nil {
		return "", "", fmt.Errorf("source_parser_configuration_unavailable")
	}
	if len(raw) > 0 {
		var config struct {
			KeyCiphertext *string `json:"keyCiphertext"`
		}
		if json.Unmarshal(raw, &config) != nil || config.KeyCiphertext == nil {
			return "", "", fmt.Errorf("source_parser_configuration_unavailable")
		}
		if *config.KeyCiphertext != "" {
			if deps.SecretBox == nil {
				return "", "", fmt.Errorf("source_parser_configuration_unavailable")
			}
			key, err := deps.SecretBox.Decrypt(*config.KeyCiphertext)
			if err != nil {
				return "", "", fmt.Errorf("source_parser_configuration_unavailable")
			}
			key, err = media.NormalizeSourceParserKey(key)
			if err != nil {
				return "", "", fmt.Errorf("source_parser_configuration_unavailable")
			}
			return key, "console", nil
		}
	}
	if key := strings.TrimSpace(deps.Env.SnapAnyAPIKey); key != "" {
		return key, "environment", nil
	}
	return "", "none", nil
}

func resolveSourceParserKey(ctx context.Context, deps *Deps) (string, error) {
	key, _, err := loadSourceParserConfiguration(ctx, deps)
	return key, err
}

func sourceParserStatus(deps *Deps, source string) gin.H {
	return gin.H{
		"configured": source != "none", "provider": "snapany", "source": source,
		"environmentConfigured": strings.TrimSpace(deps.Env.SnapAnyAPIKey) != "",
		"canConfigure":          deps.SecretBox != nil,
		"maxBytes":              media.MaxReferenceBytes,
		"maxSeconds":            media.MaxReferenceSeconds,
	}
}

func registerSourceParserConfigRoutes(router *gin.Engine, deps *Deps) {
	const path = "/console/api/media/source-parser"
	router.GET(path, func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		_, source, err := loadSourceParserConfiguration(c.Request.Context(), deps)
		if err != nil {
			c.JSON(503, fail(503, "source_parser_configuration_unavailable"))
			return
		}
		c.JSON(200, okData(sourceParserStatus(deps, source)))
	})
	router.PUT(path, func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		if deps.SecretBox == nil {
			c.JSON(503, fail(503, "source_parser_encryption_unavailable"))
			return
		}
		body, ok := readObjectBodyLimited(c, 16<<10)
		if !ok || len(body) != 1 {
			c.JSON(400, fail(400, "invalid_source_parser_key"))
			return
		}
		rawKey, ok := body["apiKey"].(string)
		key, err := media.NormalizeSourceParserKey(rawKey)
		if !ok || err != nil {
			c.JSON(400, fail(400, "invalid_source_parser_key"))
			return
		}
		ciphertext, err := deps.SecretBox.Encrypt(key)
		if err != nil {
			c.JSON(503, fail(503, "source_parser_encryption_unavailable"))
			return
		}
		raw, _ := json.Marshal(storedSourceParserConfig{KeyCiphertext: ciphertext})
		if err := deps.Store.SetConfigValue(c.Request.Context(), sourceParserConfigKey, raw); err != nil {
			c.JSON(503, fail(503, "source_parser_configuration_unavailable"))
			return
		}
		writeAudit(c.Request.Context(), deps, actor, "media.source-parser.update", "source_parser", sourceParserConfigKey, gin.H{"provider": "snapany"})
		c.JSON(200, okData(sourceParserStatus(deps, "console")))
	})
	router.DELETE(path, func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		// config 值是 JSONB；用空配置清除，避免把 nil 写成无效 JSON。
		raw, _ := json.Marshal(storedSourceParserConfig{})
		if err := deps.Store.SetConfigValue(c.Request.Context(), sourceParserConfigKey, raw); err != nil {
			c.JSON(503, fail(503, "source_parser_configuration_unavailable"))
			return
		}
		source := "none"
		if strings.TrimSpace(deps.Env.SnapAnyAPIKey) != "" {
			source = "environment"
		}
		writeAudit(c.Request.Context(), deps, actor, "media.source-parser.clear", "source_parser", sourceParserConfigKey, gin.H{"provider": "snapany"})
		c.JSON(200, okData(sourceParserStatus(deps, source)))
	})
}
