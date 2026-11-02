package apiapp

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// 支付管理端点只使用同源 console 会话；把 Bearer/API-key 混入 Cookie 请求会让 csrfOriginGuard
// 误判为无环境凭据，从而绕过来源校验。客户端设备支付使用独立 /api/v1/client 路由。
func paymentConsoleCookieOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/console/api/payments/") && (strings.TrimSpace(c.GetHeader("Authorization")) != "" || strings.TrimSpace(c.GetHeader("X-API-Key")) != "") {
			c.AbortWithStatusJSON(http.StatusForbidden, fail(http.StatusForbidden, "支付管理接口仅接受控制台会话"))
			return
		}
		c.Next()
	}
}
