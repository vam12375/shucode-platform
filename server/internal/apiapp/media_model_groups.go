package apiapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/vam12375/shucode-platform/server/internal/media"
)

func publicMediaModelGroupID(m media.Model) string {
	// 分组不能使用展示名猜测，也不能将供应商与上游内部标识直接公开给客户端。
	identity, _ := json.Marshal([]string{m.Kind, m.ProviderID, m.Adapter, strings.TrimSpace(m.UpstreamModel)})
	digest := sha256.Sum256(identity)
	return "media-group-" + hex.EncodeToString(digest[:])
}

func publicMediaModelGroupName(m media.Model) string {
	name := strings.TrimSpace(m.Name)
	if m.Kind == "video" {
		for _, resolution := range m.Resolutions {
			suffix := " " + strings.ToLower(resolution)
			if strings.HasSuffix(strings.ToLower(name), suffix) {
				name = strings.TrimSpace(name[:len(name)-len(suffix)])
				break
			}
		}
	}
	if name == "" {
		name = strings.TrimSpace(m.ID)
	}
	if name == "" {
		name = m.Kind
	}
	return string([]rune(name)[:min(len([]rune(name)), 200)])
}
