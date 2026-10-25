package apiapp

// LoginGate 限制同一 API 实例内同时执行口令 KDF 的登录请求数。
//
// 限流桶限制的是"每分钟多少次"，挡不住攻击者在窗口内一次性并发 50 个请求：
// 每个请求仍会各跑一次 scrypt，CPU 会被瞬间打满。这里用非阻塞信号量把
// 同时进行的高成本校验限制在固定槽位内，槽位耗尽直接 429，而不是排队。
// 多实例部署时这是每实例上限，配合 Redis 限流仍能显著降低单机被打满的风险。
type LoginGate struct {
	slots chan struct{}
}

func NewLoginGate(maxConcurrent int) *LoginGate {
	if maxConcurrent <= 0 {
		return nil
	}
	return &LoginGate{slots: make(chan struct{}, maxConcurrent)}
}

func (g *LoginGate) TryAcquire() bool {
	if g == nil {
		return true
	}
	select {
	case g.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g *LoginGate) Release() {
	if g == nil {
		return
	}
	select {
	case <-g.slots:
	default:
	}
}
