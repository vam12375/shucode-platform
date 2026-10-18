// 迁移器:接管 packages/db/src/migrate-cli.ts 语义——DATABASE_URL 必填、
// 从 cwd 向上找 .env、顺序应用 packages/db/migrations(M4 后改 server/migrations)。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/vam12375/shucode-platform/server/internal/platformenv"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func main() {
	migrationsDir := flag.String("dir", "migrations", "迁移 SQL 目录")
	flag.Parse()

	platformenv.LoadEnvFilesUpwards(".")
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "缺少 DATABASE_URL(参考 .env.example)")
		os.Exit(1)
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[migrate] 连接失败: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	if err := store.RunMigrations(ctx, conn, *migrationsDir, func(message string) {
		fmt.Println(message)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "[migrate] 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("migrations done")
}
