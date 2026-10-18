// 迁移器骨架(M0)。M1 按计划接管 packages/db/migrate-cli 语义:
// platform_migrations 账本 + 同目录 SQL 逐文件单事务(specs/go-rewrite.md §2.7)。
package main

import (
	"errors"
	"log"
)

func main() {
	log.Fatalf("cmd/migrate 骨架:%v", errors.New("迁移器在 M1 实现"))
}
