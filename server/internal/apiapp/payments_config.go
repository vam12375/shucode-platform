package apiapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/alipay"
)

const paymentConfigKey = "payments:alipay:current"
const paymentConfigVersionPrefix = "payments:alipay:version:"

type paymentConfiguration struct {
	ID                   string `json:"id"`
	Enabled              bool   `json:"enabled"`
	AppID                string `json:"appId"`
	SellerID             string `json:"sellerId"`
	Gateway              string `json:"gateway"`
	PrivateKeyCiphertext string `json:"privateKeyCiphertext"`
	PublicKeyCiphertext  string `json:"publicKeyCiphertext"`
}

func paymentURLs(deps *Deps) (string, string) {
	origin := strings.TrimRight(deps.Env.PublicAPIOrigin, "/")
	return origin + "/api/v1/payments/alipay/notify", origin + "/api/v1/payments/alipay/return"
}

// InitializePaymentConfiguration imports environment credentials once. Console
// updates own subsequent configuration; immutable versions keep old orders valid.
func InitializePaymentConfiguration(ctx context.Context, deps *Deps) error {
	env := deps.Env
	if env.AlipayAppID == "" && env.AlipaySellerID == "" && env.AlipayPrivateKey == "" && env.AlipayPublicKey == "" {
		return nil
	}
	current, err := deps.Store.GetConfigValue(ctx, paymentConfigKey)
	if err != nil {
		return errors.New("payment configuration unavailable")
	}
	if len(current) > 0 {
		return nil
	}
	if deps.SecretBox == nil {
		return errors.New("payment encryption unavailable")
	}
	gateway := env.AlipayGateway
	if gateway == "" {
		gateway = "production"
	}
	if gateway != "production" && gateway != "sandbox" {
		return errors.New("ALIPAY_GATEWAY must be production or sandbox")
	}
	private, err := deps.SecretBox.Encrypt(env.AlipayPrivateKey)
	if err != nil {
		return errors.New("payment encryption unavailable")
	}
	public, err := deps.SecretBox.Encrypt(env.AlipayPublicKey)
	if err != nil {
		return errors.New("payment encryption unavailable")
	}
	config := paymentConfiguration{ID: uuid.NewString(), Enabled: true, AppID: env.AlipayAppID, SellerID: env.AlipaySellerID, Gateway: gateway, PrivateKeyCiphertext: private, PublicKeyCiphertext: public}
	if _, err := paymentClient(deps, config); err != nil {
		return errors.New("ALIPAY configuration is incomplete or invalid")
	}
	raw, _ := json.Marshal(config)
	if err := deps.Store.SetConfigValue(ctx, paymentConfigVersionPrefix+config.ID, raw); err != nil {
		return errors.New("payment configuration unavailable")
	}
	_, err = deps.Store.CompareAndSwapConfigValue(ctx, paymentConfigKey, nil, raw)
	return err
}

func loadPaymentConfig(ctx context.Context, deps *Deps, id string) (paymentConfiguration, error) {
	key := paymentConfigKey
	if id != "" {
		key = paymentConfigVersionPrefix + id
	}
	raw, err := deps.Store.GetConfigValue(ctx, key)
	if err != nil {
		return paymentConfiguration{}, err
	}
	if len(raw) == 0 {
		return paymentConfiguration{Gateway: "production"}, nil
	}
	var config paymentConfiguration
	if json.Unmarshal(raw, &config) != nil || config.ID == "" {
		return config, errors.New("payment configuration unavailable")
	}
	return config, nil
}

func paymentClient(deps *Deps, config paymentConfiguration) (*alipay.Client, error) {
	if deps.SecretBox == nil {
		return nil, errors.New("payment encryption unavailable")
	}
	private, err := deps.SecretBox.Decrypt(config.PrivateKeyCiphertext)
	if err != nil {
		return nil, errors.New("payment key unavailable")
	}
	public, err := deps.SecretBox.Decrypt(config.PublicKeyCiphertext)
	if err != nil {
		return nil, errors.New("payment key unavailable")
	}
	gateway := "https://openapi.alipay.com/gateway.do"
	if config.Gateway == "sandbox" {
		gateway = "https://openapi-sandbox.dl.alipaydev.com/gateway.do"
	}
	notify, ret := paymentURLs(deps)
	client := deps.PaymentHTTPClient
	if client == nil {
		client = &http.Client{Timeout: paymentQueryTimeout}
	}
	return alipay.New(alipay.Config{AppID: config.AppID, SellerID: config.SellerID, PrivateKey: private, PublicKey: public, NotifyURL: notify, ReturnURL: ret, GatewayURL: gateway}, client)
}

func publicPaymentConfig(deps *Deps, config paymentConfiguration) gin.H {
	notify, ret := paymentURLs(deps)
	return gin.H{"enabled": config.Enabled, "appId": config.AppID, "sellerId": config.SellerID, "gateway": config.Gateway, "privateKeyConfigured": config.PrivateKeyCiphertext != "", "publicKeyConfigured": config.PublicKeyCiphertext != "", "notifyUrl": notify, "returnUrl": ret}
}

func registerPaymentConfigRoutes(router *gin.Engine, deps *Deps) {
	router.GET("/console/api/payments/config", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		config, err := loadPaymentConfig(c.Request.Context(), deps, "")
		if err != nil {
			c.JSON(503, fail(503, "支付配置不可用"))
			return
		}
		c.JSON(200, okData(publicPaymentConfig(deps, config)))
	})
	router.PUT("/console/api/payments/config", func(c *gin.Context) {
		ctx := c.Request.Context()
		actor, ok := requireRoot(ctx, deps, c)
		if !ok {
			return
		}
		if deps.SecretBox == nil {
			c.JSON(503, fail(503, "请先配置 PLATFORM_SECRET_KEY 以加密支付密钥"))
			return
		}
		body, ok := readObjectBodyLimited(c, 32<<10)
		if !ok {
			c.JSON(400, fail(400, "支付配置不合法"))
			return
		}
		if !rejectUnknownFields(c, body, "enabled", "appId", "sellerId", "gateway", "privateKey", "publicKey") {
			return
		}
		oldRaw, err := deps.Store.GetConfigValue(ctx, paymentConfigKey)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		config, err := loadPaymentConfig(ctx, deps, "")
		if err != nil {
			c.JSON(503, fail(503, "支付配置不可用"))
			return
		}
		enabled, eOK := body["enabled"].(bool)
		app, aOK := body["appId"].(string)
		seller, sOK := body["sellerId"].(string)
		gateway, gOK := body["gateway"].(string)
		if !eOK || !aOK || !sOK || !gOK || (gateway != "production" && gateway != "sandbox") {
			c.JSON(400, fail(400, "支付配置不合法"))
			return
		}
		config.ID = uuid.NewString()
		config.Enabled = enabled
		config.AppID = strings.TrimSpace(app)
		config.SellerID = strings.TrimSpace(seller)
		config.Gateway = gateway
		for name, dest := range map[string]*string{"privateKey": &config.PrivateKeyCiphertext, "publicKey": &config.PublicKeyCiphertext} {
			if value, present := body[name]; present {
				key, valid := value.(string)
				if !valid {
					c.JSON(400, fail(400, "密钥必须是文本"))
					return
				}
				if strings.TrimSpace(key) != "" {
					encrypted, err := deps.SecretBox.Encrypt(strings.TrimSpace(key))
					if err != nil {
						internalError(c, deps, err)
						return
					}
					*dest = encrypted
				}
			}
		}
		if config.Enabled || config.PrivateKeyCiphertext != "" || config.PublicKeyCiphertext != "" {
			if _, err := paymentClient(deps, config); err != nil {
				c.JSON(400, fail(400, "请检查应用ID、商户ID、RSA2应用私钥和支付宝公钥，以及公开回调地址"))
				return
			}
		}
		raw, _ := json.Marshal(config)
		// 先保存不可变配置版本，再CAS切换当前指针；旧订单始终使用下单时的密钥版本。
		if err := deps.Store.SetConfigValue(ctx, paymentConfigVersionPrefix+config.ID, raw); err != nil {
			internalError(c, deps, err)
			return
		}
		swapped, err := deps.Store.CompareAndSwapConfigValue(ctx, paymentConfigKey, oldRaw, raw)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if !swapped {
			c.JSON(409, fail(409, "支付配置已被其他操作更新，请刷新后重试"))
			return
		}
		writeAudit(ctx, deps, actor, "payment.config.update", "payment_config", config.ID, gin.H{"enabled": config.Enabled, "gateway": config.Gateway})
		c.JSON(200, okData(publicPaymentConfig(deps, config)))
	})
}
