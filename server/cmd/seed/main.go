// 种子:载入种子内置配置(无活动 Release 时)、模型目录为空时写两条演示模型。
// root 账号不在此创建：由控制台首次初始化写入数据库（口令只以 scrypt 哈希落库，不读 .env）。
// 可重复执行。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/vam12375/shucode-platform/server/internal/platformenv"
	"github.com/vam12375/shucode-platform/server/internal/seed"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func main() {
	migrationsDir := flag.String("dir", "migrations", "迁移 SQL 目录")
	seedFile := flag.String("seed-file", "seeds/shucode-builtin.seed.json", "种子内置配置 JSON")
	flag.Parse()

	platformenv.LoadEnvFilesUpwards(".")
	if platformenv.Truthy(os.Getenv("MEMORY_DB")) {
		fmt.Println("MEMORY_DB=1 无需种子:本地体验模式已内置演示数据(admin@mendao.chat / mendao-admin, root)")
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

	root, err := st.FindRootUser(ctx)
	if err != nil {
		fail(err)
	}
	if root == nil {
		fmt.Println("尚未初始化超级管理员：首次打开控制台会进入 root 初始化页")
	} else {
		fmt.Printf("超级管理员已存在: %s\n", root.Email)
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
