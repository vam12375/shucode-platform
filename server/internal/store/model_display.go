package store

import (
	"context"
	"strings"
	"time"
)

func (s *memoryStore) CompareAndSwapModelDisplayName(_ context.Context, modelID, expectedName, displayName string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	model, exists := s.models[modelID]
	if !exists || model.DisplayName != expectedName {
		return false, nil
	}
	model.DisplayName, model.UpdatedAt = displayName, time.Now()
	s.models[modelID] = model
	return true, nil
}

func (s *PostgresStore) CompareAndSwapModelDisplayName(ctx context.Context, modelID, expectedName, displayName string) (bool, error) {
	// 展示页只写名称，不能用全量 Upsert 把并行编辑的价格和路由恢复为旧值。
	tag, err := s.pool.Exec(ctx, `UPDATE platform_models SET display_name=$3,updated_at=now() WHERE model_id=$1 AND display_name=$2`, modelID, expectedName, displayName)
	return tag.RowsAffected() > 0, err
}

// ModelDisplayName 只修正旧目录的缺省原始 ID；root 显式设置的展示名不能被默认映射覆盖。
func ModelDisplayName(modelID, configured string) string {
	name := strings.TrimSpace(configured)
	if name != "" && name != modelID {
		return name
	}
	switch modelID {
	case "deepseek-flash":
		return "DeepSeek Flash"
	case "deepseek-v4-pro":
		return "DeepSeek V4 Pro"
	case "glm-5-3-flash-260828":
		return "GLM 5.3 Flash"
	}
	return name
}
