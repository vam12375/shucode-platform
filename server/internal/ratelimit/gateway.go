package ratelimit

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// GatewayLimiter:网关的 RPM/TPM/并发限流。
// Redis 配置时跨实例共享；未配置时退化为进程内（单实例语义）。
// 所有方法在 limit<=0 时直接放行（0 = 不限）。
type GatewayLimiter struct {
	client redis.UniversalClient
	mem    *memGatewayLimiter
}

func NewGatewayLimiter(client redis.UniversalClient) *GatewayLimiter {
	return &GatewayLimiter{client: client, mem: newMemGatewayLimiter()}
}

func (l *GatewayLimiter) AllowRPM(ctx context.Context, key string, limit int64) (bool, error) {
	if limit <= 0 {
		return true, nil
	}
	if l.client == nil {
		return l.mem.allowRPM(key, limit, time.Now()), nil
	}
	now := time.Now().UnixMilli()
	result, err := redisAllowRPM.Run(ctx, l.client,
		[]string{"gw:rpm:" + key}, now, int64(time.Minute/time.Millisecond), limit,
		fmt.Sprintf("%d:%d", now, time.Now().UnixNano())).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (l *GatewayLimiter) AllowTPM(ctx context.Context, key string, limit, cost int64) (bool, error) {
	if limit <= 0 {
		return true, nil
	}
	if cost < 1 {
		cost = 1
	}
	if l.client == nil {
		return l.mem.allowTPM(key, limit, cost, time.Now()), nil
	}
	minute := time.Now().Unix() / 60
	redisKey := "gw:tpm:" + key + ":" + strconv.FormatInt(minute, 10)
	result, err := redisAllowTPM.Run(ctx, l.client, []string{redisKey}, cost, 120_000, limit).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// AcquireConcurrency:返回 release（必须调用）与是否获得槽位。
func (l *GatewayLimiter) AcquireConcurrency(ctx context.Context, key string, limit int64) (func(), bool, error) {
	if limit <= 0 {
		return func() {}, true, nil
	}
	if l.client == nil {
		ok := l.mem.acquire(key, limit)
		if !ok {
			return func() {}, false, nil
		}
		var once sync.Once
		return func() { once.Do(func() { l.mem.release(key) }) }, true, nil
	}
	const leaseMs = int64(300_000)
	redisKey := "gw:conc:" + key
	result, err := redisAcquireConcurrency.Run(ctx, l.client,
		[]string{redisKey}, limit, leaseMs).Int64()
	if err != nil {
		return func() {}, false, err
	}
	if result != 1 {
		return func() {}, false, nil
	}
	// 长 SSE 请求可能持续数十分钟，固定 5 分钟过期会让并发槽位提前消失；
	// 持槽期间定期续租。续租失败（Redis 故障/键已过期）不影响请求本身。
	stopRenew := make(chan struct{})
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopRenew:
				return
			case <-ticker.C:
				renewCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = redisRenewConcurrency.Run(renewCtx, l.client, []string{redisKey}, leaseMs).Err()
				cancel()
			}
		}
	}()
	var once sync.Once
	release := func() {
		once.Do(func() {
			close(stopRenew)
			// 请求 ctx 可能已取消，释放必须用独立 ctx。
			releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = redisReleaseConcurrency.Run(releaseCtx, l.client,
				[]string{redisKey}, leaseMs).Err()
		})
	}
	return release, true, nil
}

var (
	redisAllowRPM = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local max = tonumber(ARGV[3])
redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)
local count = redis.call('ZCARD', key)
if count < max then
  redis.call('ZADD', key, now, ARGV[4])
  redis.call('PEXPIRE', key, window)
  return 1
end
return 0`)

	redisAllowTPM = redis.NewScript(`
local key = KEYS[1]
local cost = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local current = redis.call('INCRBY', key, cost)
if current == cost then
  redis.call('PEXPIRE', key, ttl)
end
local max = tonumber(ARGV[3])
if current > max then
  return 0
end
return 1`)

	redisAcquireConcurrency = redis.NewScript(`
local key = KEYS[1]
local max = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local current = redis.call('INCR', key)
if current > max then
  redis.call('DECR', key)
  return 0
end
redis.call('PEXPIRE', key, ttl)
return 1`)

	redisReleaseConcurrency = redis.NewScript(`
local key = KEYS[1]
local ttl = tonumber(ARGV[1])
local current = redis.call('DECR', key)
if current < 0 then
  redis.call('SET', key, 0, 'PX', ttl)
end
redis.call('PEXPIRE', key, ttl)
return 1`)

	// 仅当槽位键仍存在时续租；键已过期就不重建，避免复活旧的计数。
	redisRenewConcurrency = redis.NewScript(`
local key = KEYS[1]
local ttl = tonumber(ARGV[1])
if redis.call('EXISTS', key) == 1 then
  redis.call('PEXPIRE', key, ttl)
  return 1
end
return 0`)
)

// memGatewayLimiter:Redis 不可用时的单实例回退。
type memGatewayLimiter struct {
	mu       sync.Mutex
	rpm      map[string][]time.Time
	tpm      map[string]memTokenWindow
	inflight map[string]int64
}

type memTokenWindow struct {
	start time.Time
	used  int64
}

func newMemGatewayLimiter() *memGatewayLimiter {
	return &memGatewayLimiter{
		rpm:      map[string][]time.Time{},
		tpm:      map[string]memTokenWindow{},
		inflight: map[string]int64{},
	}
}

func (m *memGatewayLimiter) allowRPM(key string, limit int64, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := now.Add(-time.Minute)
	times := m.rpm[key]
	kept := times[:0]
	for _, at := range times {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if int64(len(kept)) >= limit {
		m.rpm[key] = kept
		return false
	}
	m.rpm[key] = append(kept, now)
	return true
}

func (m *memGatewayLimiter) allowTPM(key string, limit, cost int64, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	window := m.tpm[key]
	if now.Sub(window.start) >= time.Minute {
		window = memTokenWindow{start: now}
	}
	window.used += cost
	m.tpm[key] = window
	return window.used <= limit
}

func (m *memGatewayLimiter) acquire(key string, limit int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight[key] >= limit {
		return false
	}
	m.inflight[key]++
	return true
}

func (m *memGatewayLimiter) release(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight[key] > 0 {
		m.inflight[key]--
	}
}
