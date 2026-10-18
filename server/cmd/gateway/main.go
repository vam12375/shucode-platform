// 模型网关骨架(M0):起 Gin 验证工具链与端口约定;三协议在 M2 接管
// apps/gateway 的全部行为(specs/go-rewrite.md §2.6)。
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

func main() {
	port := os.Getenv("GATEWAY_PORT")
	if port == "" {
		port = "8788"
	}
	gin.SetMode(gin.ReleaseMode)
	app := gin.New()
	app.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	log.Printf("gateway 骨架监听 :%s", port)
	if err := app.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}
