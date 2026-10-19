package ratelimit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestInProcWindow(t *testing.T) {
	l := NewInProc(Window{Window: 5 * time.Minute, Max: 10})
	now := time.Now()
	for i := 0; i < 10; i++ {
		if !l.allowAt("ip:email", now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("窗口内第 %d 次应放行", i+1)
		}
	}
	if l.allowAt("ip:email", now.Add(10*time.Second)) {
		t.Fatal("超过上限应拒绝")
	}
	if !l.allowAt("other:email", now.Add(10*time.Second)) {
		t.Fatal("不同 key 互不影响")
	}
	// 窗口滑过首个时间戳后,可再放行一次。
	if !l.allowAt("ip:email", now.Add(5*time.Minute+2*time.Second)) {
		t.Fatal("窗口滑过后应放行")
	}
}

func TestCompositeAllowStopsAtFirstRejection(t *testing.T) {
	l := NewInProc(Window{Window: time.Minute, Max: 1})
	ctx := context.Background()
	allowed, err := CompositeAllow(ctx,
		Rule{Limiter: nil, Key: "ignored"},
		Rule{Limiter: l, Key: "ip"},
		Rule{Limiter: l, Key: "email"},
	)
	if err != nil || !allowed {
		t.Fatalf("首次应放行: allowed=%v err=%v", allowed, err)
	}
	allowed, err = CompositeAllow(ctx,
		Rule{Limiter: l, Key: "ip"},
		Rule{Limiter: l, Key: "email"},
	)
	if err != nil || allowed {
		t.Fatalf("任一桶拒绝应整体拒绝: allowed=%v err=%v", allowed, err)
	}
}

func TestHashKeyIsStableAndBounded(t *testing.T) {
	long := make([]byte, 1000)
	for i := range long {
		long[i] = 'a'
	}
	key := string(long)
	hashed := hashKey(key)
	if hashed != hashKey(key) {
		t.Fatal("同一 key 的哈希应稳定")
	}
	if len(hashed) != 64 {
		t.Fatalf("SHA-256 hex 长度应为 64，得到 %d", len(hashed))
	}
	if hashed == hashKey("other") {
		t.Fatal("不同 key 不应碰撞")
	}
}

func TestRedisWindowSharedAcrossInstances(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("未设置 TEST_REDIS_URL,跳过 Redis 限流测试")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	opts.DB = 15
	client := redis.NewClient(opts)
	defer client.Close()
	ctx := context.Background()
	_ = client.Del(ctx, "ratelimit:"+hashKey("shared-key")).Err()

	cfg := Window{Window: 5 * time.Minute, Max: 5}
	a := NewRedis(client, cfg)
	b := NewRedis(client, cfg)
	allowed := 0
	for i := 0; i < 10; i++ {
		limiter := a
		if i%2 == 1 {
			limiter = b
		}
		ok, err := limiter.Allow(ctx, "shared-key")
		if err != nil {
			t.Fatalf("Redis 限流错误: %v", err)
		}
		if ok {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("两个实例共享同一窗口,应恰好放行 5 次,实际 %d", allowed)
	}
}
