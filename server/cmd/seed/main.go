// 种子:镜像 apps/api/src/seed-cli.ts。
// 创建管理员(SEED_ADMIN_EMAIL/SEED_ADMIN_PASSWORD,口令空则跳过)、
// 载入种子内置配置(无活动 Release 时)、模型目录为空时写两条演示模型。可重复执行。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/platformenv"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

var demoModels = []store.UpsertModelInput{
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

func main() {
	migrationsDir := flag.String("dir", "packages/db/migrations", "迁移 SQL 目录")
	seedFile := flag.String("seed-file", "apps/api/seeds/shucode-builtin.seed.json", "种子内置配置 JSON")
	flag.Parse()

	platformenv.LoadEnvFilesUpwards(".")
	if truthy(os.Getenv("MEMORY_DB")) {
		fmt.Println("MEMORY_DB=1 无需种子:本地体验模式已内置演示数据(admin@mendao.chat / mendao-admin)")
		return
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "缺少 DATABASE_URL(参考 .env.example)")
		os.Exit(1)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[seed] 连接失败: %v\n", err)
		os.Exit(1)
	}
	if err := store.RunMigrations(ctx, conn, *migrationsDir, func(message string) {
		fmt.Println(message)
	}); err != nil {
		_ = conn.Close(ctx)
		fmt.Fprintf(os.Stderr, "[seed] 迁移失败: %v\n", err)
		os.Exit(1)
	}
	_ = conn.Close(ctx)

	st, err := store.NewPostgres(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[seed] 构造存储失败: %v\n", err)
		os.Exit(1)
	}
	defer st.Dispose(ctx)

	adminEmail := strings.TrimSpace(os.Getenv("SEED_ADMIN_EMAIL"))
	if adminEmail == "" {
		adminEmail = "admin@mendao.chat"
	}
	adminPassword := strings.TrimSpace(os.Getenv("SEED_ADMIN_PASSWORD"))
	existing, err := st.FindUserByEmail(ctx, adminEmail)
	if err != nil {
		fail(err)
	}
	switch {
	case existing != nil:
		fmt.Printf("管理员已存在: %s\n", adminEmail)
	case adminPassword == "":
		fmt.Printf("未创建管理员:请设置 SEED_ADMIN_PASSWORD 后重试(目标邮箱 %s)\n", adminEmail)
	default:
		hash, err := crypto.HashPassword(adminPassword)
		if err != nil {
			fail(err)
		}
		if _, err := st.CreateUser(ctx, store.UserInput{
			Email: adminEmail, PasswordHash: hash, DisplayName: "平台管理员", Role: store.RoleAdmin,
		}); err != nil {
			fail(err)
		}
		fmt.Printf("管理员已创建: %s\n", adminEmail)
	}

	if err := ensureSeedRelease(ctx, st, *seedFile); err != nil {
		fail(err)
	}
	if err := ensureDefaultModels(ctx, st); err != nil {
		fail(err)
	}
	fmt.Println("seed done")
}

func ensureSeedRelease(ctx context.Context, st store.PlatformStore, seedFile string) error {
	active, err := st.GetActiveRelease(ctx)
	if err != nil || active != nil {
		return err
	}
	raw, err := os.ReadFile(seedFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "未找到种子内置配置,跳过载入")
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		fmt.Fprintf(os.Stderr, "种子内置配置无效,跳过载入: %v\n", err)
		return nil
	}
	revision, content, err := contracts.ParseBuiltinReleaseLoose(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "种子内置配置无效,跳过载入: %v\n", err)
		return nil
	}
	existing, err := st.GetRelease(ctx, int64(revision))
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return err
	}
	if _, err := st.InsertRelease(ctx, store.InsertReleaseInput{
		Revision: int64(revision), Content: encoded, PublishedBy: "",
	}); err != nil {
		return err
	}
	fmt.Printf("已载入种子内置配置(revision %d)\n", int64(revision))
	return nil
}

func ensureDefaultModels(ctx context.Context, st store.PlatformStore) error {
	models, err := st.ListModels(ctx, true)
	if err != nil || len(models) > 0 {
		return err
	}
	for _, model := range demoModels {
		if _, err := st.UpsertModel(ctx, model); err != nil {
			return err
		}
	}
	fmt.Printf("已写入默认模型目录(count %d)\n", len(demoModels))
	return nil
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func ptr[T any](v T) *T { return &v }

func fail(err error) {
	fmt.Fprintf(os.Stderr, "[seed] 失败: %v\n", err)
	os.Exit(1)
}
