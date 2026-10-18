// Package platformenv:TS 端 loadEnvFilesUpwards 的镜像——pnpm 脚本 cwd 在包目录,
// 仓库根 .env 需要向上查找;只填充未设置的环境变量,不覆盖调用方显式传入的值。
package platformenv

import (
	"os"
	"path/filepath"
	"strings"
)

func LoadEnvFilesUpwards(startDir string) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return
	}
	for level := 0; level < 4; level++ {
		for _, name := range []string{".env.local", ".env"} {
			path := filepath.Join(dir, name)
			if info, err := os.Stat(path); err != nil || info.IsDir() {
				continue
			}
			applyEnvFile(path)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

// applyEnvFile 逐行解析 KEY=VALUE:trim、支持成对单/双引号剥离、不覆盖已有变量;
// 解析失败/无分隔符的行跳过(与 TS 相同的宽松行为)。
func applyEnvFile(path string) {
	content, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sep := strings.Index(line, "=")
		if sep <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:sep])
		value := strings.TrimSpace(line[sep+1:])
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if key != "" {
			if _, exists := os.LookupEnv(key); !exists {
				os.Setenv(key, value)
			}
		}
	}
}
