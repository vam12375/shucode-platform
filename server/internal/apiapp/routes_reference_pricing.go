package apiapp

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func registerReferencePricingRoutes(router *gin.Engine, deps *Deps) {
	const path = "/console/api/media/reference-pricing"
	router.GET(path, func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		pricing, err := deps.Media.Repo.ReferencePricing(c.Request.Context())
		if err != nil {
			c.JSON(503, fail(503, "reference_pricing_unavailable"))
			return
		}
		c.JSON(200, okData(pricing))
	})
	router.PUT(path, func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(400, fail(400, "invalid_reference_pricing"))
			return
		}
		pricing, err := media.ParseReferencePricing(raw)
		if err != nil {
			c.JSON(400, fail(400, "invalid_reference_pricing"))
			return
		}
		stored, _ := json.Marshal(pricing)
		if err := deps.Store.SetConfigValue(c.Request.Context(), media.ReferencePricingKey, stored); err != nil {
			c.JSON(503, fail(503, "reference_pricing_unavailable"))
			return
		}
		writeAudit(c.Request.Context(), deps, actor, "media.reference-pricing.update", "reference_pricing", media.ReferencePricingKey, pricing)
		c.JSON(200, okData(pricing))
	})
}
