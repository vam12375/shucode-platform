// 控制面 API 骨架(M0):起 Gin 验证工具链与端口约定;真实路由在 M3 接管
// apps/api 的全部端点(specs/go-rewrite.md §2.1)。
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8787"
	}
	gin.SetMode(gin.ReleaseMode)
	app := gin.New()
	app.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	log.Printf("api 骨架监听 :%s", port)
	if err := app.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}
