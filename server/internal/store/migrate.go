package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// runMigrations 的镜像:顺序执行 migrations/*.sql,以 platform_migrations 记账;
// 每个文件一个事务(先执行 SQL,再记 name),已存在的账目跳过。
// 迁移文件与 TS 侧共用同一目录(packages/db/migrations),M4 切换时才移动到 server/。
func RunMigrations(ctx context.Context, conn *pgx.Conn, migrationsDir string, log func(string)) error {
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS platform_migrations (
		name text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("建账表失败: %w", err)
	}
	names, err := listMigrationFiles(migrationsDir)
	if err != nil {
		return err
	}
	for _, name := range names {
		var exists bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM platform_migrations WHERE name = $1)`, name,
		).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		content, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(content)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("执行 %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO platform_migrations (name) VALUES ($1)`, name,
		); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		if log != nil {
			log("applied migration: " + name)
		}
	}
	return nil
}

func listMigrationFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取迁移目录 %s: %w", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	// TS localeCompare 对纯 ASCII 数字前缀文件名与字节序一致。
	sort.Strings(names)
	return names, nil
}
