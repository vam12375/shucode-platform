// Package ratelimit:登录/表单防爆破的滑动窗口限流。
// 复合键语义(如登录 ip:email)由调用方拼装;共享窗口走 Redis(specs/go-rewrite.md §4),
// REDIS_URL 未设时回退进程内实现(单实例语义,等同旧 TS 行为)。
package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Limiter interface {
	// Allow:窗口内首次达到上限后拒绝。Redis 故障时返回 err,
	// 调用方按 fail-open 处理(与旧实现「该实例限流失效」等价)。
	Allow(ctx context.Context, key string) (bool, error)
}

type Window struct {
	Window time.Duration
	Max    int
}

// ---- 进程内滑动窗口(登录接口,参数 5min/10 次) ----

type InProc struct {
	cfg Window
	mu  sync.Mutex
	hit map[string][]time.Time
}

func NewInProc(cfg Window) *InProc {
	return &InProc{cfg: cfg, hit: map[string][]time.Time{}}
}

func (l *InProc) Allow(_ context.Context, key string) (bool, error) {
	return l.allowAt(key, time.Now()), nil
}

func (l *InProc) allowAt(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.cfg.Window)
	times := l.hit[key]
	kept := times[:0]
	for _, at := range times {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= l.cfg.Max {
		l.hit[key] = kept
		return false
	}
	l.hit[key] = append(kept, now)
	return true
}

// ---- Redis 共享窗口(ZSET,原子 Lua:清旧 + 计数 + 记录) ----

type RedisLimiter struct {
	cfg    Window
	client redis.UniversalClient
	script *redis.Script
}

func NewRedis(client redis.UniversalClient, cfg Window) *RedisLimiter {
	return &RedisLimiter{
		cfg:    cfg,
		client: client,
		script: redis.NewScript(`
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
return 0`),
	}
}

func (l *RedisLimiter) Allow(ctx context.Context, key string) (bool, error) {
	nowMs := time.Now().UnixMilli()
	member := key + ":" + time.Now().Format(time.RFC3339Nano)
	result, err := l.script.Run(ctx, l.client,
		[]string{"ratelimit:" + key},
		nowMs,
		l.cfg.Window.Milliseconds(),
		l.cfg.Max,
		member,
	).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}
