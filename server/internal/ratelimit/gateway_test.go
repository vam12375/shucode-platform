package ratelimit

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestGatewayLimiterRPM(t *testing.T) {
	limiter := NewGatewayLimiter(nil)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		allowed, err := limiter.AllowRPM(ctx, "team-rpm", 2)
		if err != nil || !allowed {
			t.Fatalf("第 %d 次应放行: %v", i+1, err)
		}
	}
	if allowed, err := limiter.AllowRPM(ctx, "team-rpm", 2); err != nil || allowed {
		t.Fatalf("超过 RPM 应拒绝: %v %v", allowed, err)
	}
	if allowed, _ := limiter.AllowRPM(ctx, "team-rpm", 0); !allowed {
		t.Fatal("limit=0 应不限制")
	}
}

func TestGatewayLimiterTPM(t *testing.T) {
	limiter := NewGatewayLimiter(nil)
	ctx := context.Background()
	if allowed, err := limiter.AllowTPM(ctx, "team-tpm", 100, 60); err != nil || !allowed {
		t.Fatalf("首笔 TPM 应放行: %v %v", allowed, err)
	}
	if allowed, err := limiter.AllowTPM(ctx, "team-tpm", 100, 60); err != nil || allowed {
		t.Fatalf("超过 TPM 应拒绝: %v %v", allowed, err)
	}
}

func TestGatewayLimiterRedisTMPScript(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("未设置 TEST_REDIS_URL，跳过 Redis TPM 测试")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	opts.DB = 15
	client := redis.NewClient(opts)
	defer client.Close()
	ctx := context.Background()
	keys, _ := client.Keys(ctx, "gw:tpm:redis-tpm:*").Result()
	if len(keys) > 0 {
		_ = client.Del(ctx, keys...).Err()
	}
	limiter := NewGatewayLimiter(client)
	if allowed, err := limiter.AllowTPM(ctx, "redis-tpm", 10, 6); err != nil || !allowed {
		t.Fatalf("首笔 Redis TPM 应放行: %v %v", allowed, err)
	}
	if allowed, err := limiter.AllowTPM(ctx, "redis-tpm", 10, 6); err != nil || allowed {
		t.Fatalf("超过 Redis TPM 应拒绝: %v %v", allowed, err)
	}
}

func TestGatewayLimiterRedisConcurrencyScript(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("未设置 TEST_REDIS_URL，跳过 Redis 并发槽测试")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	opts.DB = 15
	client := redis.NewClient(opts)
	defer client.Close()
	ctx := context.Background()
	key := "gw:conc:redis-conc"
	_ = client.Del(ctx, key).Err()
	limiter := NewGatewayLimiter(client)
	release, ok, err := limiter.AcquireConcurrency(ctx, "redis-conc", 1)
	if err != nil || !ok {
		t.Fatalf("首个 Redis 并发槽应获得: %v %v", ok, err)
	}
	if _, ok, _ := limiter.AcquireConcurrency(ctx, "redis-conc", 1); ok {
		t.Fatal("超过 Redis 并发上限应拒绝")
	}
	release()
	release() // 幂等释放不应把计数压到负数。
	release2, ok, err := limiter.AcquireConcurrency(ctx, "redis-conc", 1)
	if err != nil || !ok {
		t.Fatalf("释放后应可再次获得: %v %v", ok, err)
	}
	release2()
}

func TestGatewayLimiterConcurrency(t *testing.T) {
	limiter := NewGatewayLimiter(nil)
	ctx := context.Background()
	release, ok, err := limiter.AcquireConcurrency(ctx, "team-conc", 1)
	if err != nil || !ok {
		t.Fatalf("首个并发槽应获得: %v %v", ok, err)
	}
	if _, ok, _ := limiter.AcquireConcurrency(ctx, "team-conc", 1); ok {
		t.Fatal("超过并发上限应拒绝")
	}
	release()
	release() // 幂等释放不应把计数压到负数。
	_, ok, err = limiter.AcquireConcurrency(ctx, "team-conc", 1)
	if err != nil || !ok {
		t.Fatalf("释放后应可再次获得: %v %v", ok, err)
	}
}
