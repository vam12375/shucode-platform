package events

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func receive(t *testing.T, ch <-chan []string, within time.Duration) []string {
	t.Helper()
	select {
	case domains := <-ch:
		return domains
	case <-time.After(within):
		t.Fatal("等待事件超时")
		return nil
	}
}

func assertDomains(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("domains 不符: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("domains 不符: got %v want %v", got, want)
		}
	}
}

func TestInProcCoalescesDomains(t *testing.T) {
	bus := NewInProc()
	defer bus.Close()
	ch, cancel := bus.Subscribe(context.Background())
	defer cancel()
	bus.Emit(DomainTeams)
	bus.Emit(DomainModels, DomainTeams)
	bus.Emit(DomainProviders)
	// 300ms 合并窗口:一次推送、并集、有序。
	assertDomains(t, receive(t, ch, time.Second), DomainModels, DomainProviders, DomainTeams)
	select {
	case extra := <-ch:
		t.Fatalf("合并窗口只应推送一次: %v", extra)
	case <-time.After(400 * time.Millisecond):
	}
}

func TestInProcUnsubscribe(t *testing.T) {
	bus := NewInProc()
	ch, cancel := bus.Subscribe(context.Background())
	cancel()
	bus.Emit(DomainTeams)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("取消订阅后不应再收到事件")
		}
	case <-time.After(500 * time.Millisecond):
		// 通道关闭或事件被丢弃都可以接受;关键是没向已关闭通道发送导致 panic。
	}
	bus.Close()
}

func TestRedisBusFanout(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("未设置 TEST_REDIS_URL,跳过 Redis 总线测试")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	opts.DB = 15
	clientA := redis.NewClient(opts)
	clientB := redis.NewClient(opts)
	defer clientA.Close()
	defer clientB.Close()
	ctx := context.Background()
	busA, err := NewRedis(ctx, clientA)
	if err != nil {
		t.Fatal(err)
	}
	busB, err := NewRedis(ctx, clientB)
	if err != nil {
		t.Fatal(err)
	}
	defer busA.Close()
	defer busB.Close()
	chA, cancelA := busA.Subscribe(ctx)
	defer cancelA()
	chB, cancelB := busB.Subscribe(ctx)
	defer cancelB()

	busA.Emit(DomainReleases, DomainDevices)
	assertDomains(t, receive(t, chA, 2*time.Second), DomainDevices, DomainReleases)
	assertDomains(t, receive(t, chB, 2*time.Second), DomainDevices, DomainReleases)
}
