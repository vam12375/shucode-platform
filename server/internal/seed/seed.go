// Package seed:种子数据(管理员由 cmd/seed 创建;Release 与默认模型由 api 启动/seed 共用)。
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// DemoModels:模型目录为空时写入的演示模型(与 release-bootstrap.ts 数量与价格一致)。
var DemoModels = []store.UpsertModelInput{
	{
		ModelID: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5",
		InputPricePerMtok: "3", OutputPricePerMtok: "15",
		CacheReadPricePerMtok: ptr("0.3"), CacheWritePricePerMtok: ptr("3.75"),
		SortOrder: ptr(int64(10)),
	},
	{
		ModelID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5",
		InputPricePerMtok: "1", OutputPricePerMtok: "5",
		CacheReadPricePerMtok: ptr("0.1"), CacheWritePricePerMtok: ptr("1.25"),
		SortOrder: ptr(int64(20)),
	},
}

// EnsureSeedRelease:无活动 Release 时载入仓库内置种子配置(幂等)。
func EnsureSeedRelease(ctx context.Context, st store.PlatformStore, seedFile string) error {
	active, err := st.GetActiveRelease(ctx)
	if err != nil || active != nil {
		return err
	}
	raw, err := os.ReadFile(seedFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "未找到种子内置配置，跳过载入")
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		fmt.Fprintf(os.Stderr, "种子内置配置无效，跳过载入: %v\n", err)
		return nil
	}
	revision, content, err := contracts.ParseBuiltinReleaseLoose(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "种子内置配置无效，跳过载入: %v\n", err)
		return nil
	}
	existing, err := st.GetRelease(ctx, int64(revision))
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	encoded, err := jsonMarshal(content)
	if err != nil {
		return err
	}
	if _, err := st.InsertRelease(ctx, store.InsertReleaseInput{
		Revision: int64(revision), Content: encoded, PublishedBy: "",
	}); err != nil {
		return err
	}
	fmt.Printf("已载入种子内置配置（revision %d）\n", int64(revision))
	return nil
}

// EnsureDefaultModels:模型目录为空时写入演示模型(幂等)。
func EnsureDefaultModels(ctx context.Context, st store.PlatformStore) error {
	models, err := st.ListModels(ctx, true)
	if err != nil || len(models) > 0 {
		return err
	}
	for _, model := range DemoModels {
		if _, err := st.UpsertModel(ctx, model); err != nil {
			return err
		}
	}
	fmt.Printf("已写入默认模型目录（count %d）\n", len(DemoModels))
	return nil
}

func ptr[T any](v T) *T { return &v }

func jsonMarshal(v any) ([]byte, error) {
	encoded, err := contracts.EncodeJSONCompact(v)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
