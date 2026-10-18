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
	_ = client.Del(ctx, "ratelimit:shared-key").Err()

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
