package apiapp

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/alipay"
	"github.com/vam12375/shucode-platform/server/internal/credits"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func registerPaymentPackageRoutes(router *gin.Engine, deps *Deps) {
	router.GET("/console/api/payments/packages", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		catalog, err := deps.Store.GetPaymentPackageCatalog(c.Request.Context())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		c.JSON(http.StatusOK, okData(clientPaymentCatalogResponse(catalog, false)))
	})
	router.PUT("/console/api/payments/packages", func(c *gin.Context) {
		actor, ok := requireRoot(c.Request.Context(), deps, c)
		if !ok {
			return
		}
		body, valid := readObjectBodyLimited(c, 128<<10)
		if !valid || !rejectUnknownFields(c, body, "expectedRevision", "packages") {
			return
		}
		expected, valid := body["expectedRevision"].(string)
		if !valid || expected == "" {
			c.JSON(400, fail(400, "expectedRevision 必须为 UUID"))
			return
		}
		if !uuidShape(expected) {
			c.JSON(400, fail(400, "expectedRevision 必须为 UUID"))
			return
		}
		raw, err := json.Marshal(body["packages"])
		if err != nil {
			if errors.Is(err, store.ErrInvalidInput) {
				c.JSON(400, fail(400, "套餐列表不合法"))
				return
			}
			internalError(c, deps, err)
			return
		}
		var inputs []struct {
			SKU          string                     `json:"sku"`
			Name         string                     `json:"name"`
			Description  string                     `json:"description"`
			TargetType   store.PaymentTargetType    `json:"targetType"`
			AmountYuan   string                     `json:"amountYuan"`
			CreditAmount string                     `json:"creditAmount"`
			Status       store.PaymentPackageStatus `json:"status"`
			Recommended  bool                       `json:"recommended"`
			SortOrder    int                        `json:"sortOrder"`
		}
		if json.Unmarshal(raw, &inputs) != nil {
			c.JSON(400, fail(400, "套餐列表不合法"))
			return
		}
		packages := make([]store.PaymentPackage, 0, len(inputs))
		for _, input := range inputs {
			amount, parseErr := alipay.ParseAmountFen(input.AmountYuan)
			if parseErr != nil {
				c.JSON(400, fail(400, "套餐金额不合法"))
				return
			}
			if input.CreditAmount != "" {
				credit, creditErr := credits.ParseCreditsToMicro(input.CreditAmount)
				if creditErr != nil || credit != amount*store.MicroCreditsPerFen {
					c.JSON(400, fail(400, "creditAmount 必须与金额 1:1"))
					return
				}
			}
			packages = append(packages, store.PaymentPackage{SKU: input.SKU, Name: input.Name, Description: input.Description, TargetType: input.TargetType, AmountFen: amount, Status: input.Status, Recommended: input.Recommended, SortOrder: input.SortOrder})
		}
		candidate := store.PaymentPackageCatalog{Revision: uuid.NewString(), Packages: packages, PublishedBy: actor.ID}
		saved, err := deps.Store.SavePaymentPackageCatalog(c.Request.Context(), expected, candidate)
		if errors.Is(err, store.ErrPaymentPackageCatalogConflict) {
			current, _ := deps.Store.GetPaymentPackageCatalog(c.Request.Context())
			details := gin.H{"reason": "catalog_conflict"}
			if current != nil {
				details["currentRevision"] = current.Revision
			}
			c.JSON(409, gin.H{"code": 409, "msg": "套餐目录已被其他操作更新，请刷新后重试", "details": details})
			return
		}
		if err != nil {
			c.JSON(400, fail(400, "套餐列表不合法"))
			return
		}
		writeAudit(c.Request.Context(), deps, actor, "payment.package-catalog.update", "payment_packages", saved.Revision, gin.H{"packageCount": len(saved.Packages)})
		c.JSON(200, okData(clientPaymentCatalogResponse(saved, false)))
	})
}
