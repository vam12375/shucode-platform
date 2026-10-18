// 种子骨架(M0)。M1 按计划接管 apps/api/seed-cli 语义:
// 管理员 + ensureSeedRelease + ensureDefaultModels,幂等(specs/go-rewrite.md §2.7)。
package main

import (
	"errors"
	"log"
)

func main() {
	log.Fatalf("cmd/seed 骨架:%v", errors.New("种子在 M1 实现"))
}
