package contracts

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// release.ts 镜像:builtinReleaseLooseSchema 的结构粗检。
// 校验失败错误形如「内置配置结构不正确：<path> (<code>)」,path 用点号连接、
// 空路径显示 root,zod issue code 一致(invalid_type/invalid_literal/too_big/too_small)。

const builtinReleaseMaxSafeRevision = float64(1<<53 - 1) // Number.MAX_SAFE_INTEGER

// ParseBuiltinReleaseLoose 输入为 KV/DB 里反序列化后的任意 JSON 值。
func ParseBuiltinReleaseLoose(input any) (float64, map[string]any, error) {
	if err := checkLooseRelease(input); err != nil {
		// 与 TS 同为全角冒号,控制台发布 Release 的报错文案逐字一致。
		return 0, nil, fmt.Errorf("内置配置结构不正确：%s", err)
	}
	obj := input.(map[string]any)
	revision, _ := obj["revision"].(float64)
	return revision, obj, nil
}

func checkLooseRelease(input any) error {
	obj, ok := input.(map[string]any)
	if !ok {
		return issue("", "invalid_type")
	}
	// 该仓库 zod 对 literal 字段缺省与不匹配统一报 invalid_value。
	if v, ok := obj["schemaVersion"]; !ok {
		return issue("schemaVersion", "invalid_value")
	} else if f, ok := v.(float64); !ok || f != 1 {
		return issue("schemaVersion", "invalid_value")
	}
	if v, ok := obj["revision"]; !ok || v == nil {
		return issue("revision", "invalid_type")
	} else if f, ok := v.(float64); !ok {
		return issue("revision", "invalid_type")
	} else if math.Trunc(f) != f {
		return issue("revision", "invalid_type")
	} else if f < 0 {
		return issue("revision", "too_small")
	} else if f > builtinReleaseMaxSafeRevision {
		return issue("revision", "too_big")
	}
	config, ok := obj["config"].(map[string]any)
	if !ok {
		return issue("config", "invalid_type")
	}
	providerRulesMap, ok := config["providerConfigRules"].(map[string]any)
	if !ok {
		return issue("config.providerConfigRules", "invalid_type")
	}
	for _, key := range []string{"templateRules", "providerRules"} {
		if _, ok := providerRulesMap[key]; !ok {
			return issue("config.providerConfigRules."+key, "invalid_type")
		} else if _, isArr := providerRulesMap[key].([]any); !isArr {
			return issue("config.providerConfigRules."+key, "invalid_type")
		}
	}
	return nil
}

func issue(path, code string) error {
	if path == "" {
		path = "root"
	}
	return fmt.Errorf("%s (%s)", path, code)
}

// BuildBuiltinReleaseDownloadURL:去掉 baseUrl 尾部斜杠后拼 JSON 路径。
func BuildBuiltinReleaseDownloadURL(baseURL string, revision float64) string {
	return strings.TrimRight(baseURL, "/") + BuiltinReleaseJSONPathPrefix + "/" + jsonNumber(revision) + ".json"
}

// jsonNumber 输出与 TS Number→字符串一致的整数形式(revision 恒为非负整数)。
func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
