// 种子:镜像 apps/api/src/seed-cli.ts。
// 创建管理员(SEED_ADMIN_EMAIL/SEED_ADMIN_PASSWORD,口令空则跳过)、
// 载入种子内置配置(无活动 Release 时)、模型目录为空时写两条演示模型。可重复执行。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/platformenv"
	"github.com/vam12375/shucode-platform/server/internal/seed"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func main() {
	migrationsDir := flag.String("dir", "packages/db/migrations", "迁移 SQL 目录")
	seedFile := flag.String("seed-file", "apps/api/seeds/shucode-builtin.seed.json", "种子内置配置 JSON")
	flag.Parse()

	platformenv.LoadEnvFilesUpwards(".")
	if platformenv.Truthy(os.Getenv("MEMORY_DB")) {
		fmt.Println("MEMORY_DB=1 无需种子:本地体验模式已内置演示数据(admin@mendao.chat / mendao-admin)")
		return
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
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
		fail(err)
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

	if err := seed.EnsureSeedRelease(ctx, st, *seedFile); err != nil {
		fail(err)
	}
	if err := seed.EnsureDefaultModels(ctx, st); err != nil {
		fail(err)
	}
	fmt.Println("seed done")
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "[seed] 失败: %v\n", err)
	os.Exit(1)
}
