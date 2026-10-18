// Package events:控制台写路径的配置变更事件总线(镜像 apps/api/src/services/config-events.ts)。
// 语义:emit 后 300ms 窗口内合并 domains(并集),再向本实例全部订阅者推送;
// Redis 引入后由 pub/sub 跨实例分发,未设 REDIS_URL 时退回进程内实现。
package events

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Domain 常量与 TS config-events 的六个域一致(排序用于稳定输出)。
const (
	DomainModels         = "models"
	DomainProviders      = "providers"
	DomainTeams          = "teams"
	DomainDevices        = "devices"
	DomainPlatformConfig = "platform-config"
	DomainReleases       = "releases"
)

const coalesceWindow = 300 * time.Millisecond

type Bus interface {
	// Emit 合并窗口内多次变更,去抖后广播域并集。
	Emit(domains ...string)
	// Subscribe 返回收事件通道与取消函数;通道在取消/关闭后关闭。
	Subscribe(ctx context.Context) (<-chan []string, func())
	Close()
}

// ---- 进程内实现(旧 TS 行为) ----

type InProc struct {
	mu        sync.Mutex
	listeners map[int]chan []string
	pending   map[string]struct{}
	timer     *time.Timer
	nextID    int
}

func NewInProc() *InProc {
	return &InProc{listeners: map[int]chan []string{}, pending: map[string]struct{}{}}
}

func (b *InProc) Emit(domains ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, domain := range domains {
		if domain != "" {
			b.pending[domain] = struct{}{}
		}
	}
	if b.timer != nil {
		return
	}
	b.timer = time.AfterFunc(coalesceWindow, b.flush)
}

func (b *InProc) flush() {
	// 发送与关闭都在同一把锁内(均为非阻塞/无等待操作),避免 send on closed channel。
	b.mu.Lock()
	defer b.mu.Unlock()
	domains := drainPending(b.pending)
	b.timer = nil
	if len(domains) == 0 {
		return
	}
	for _, ch := range b.listeners {
		select {
		case ch <- domains:
		default: // 慢消费者丢弃本次合并事件(SSE 有 1s 兜底轮询,与 TS 非阻塞广播语义一致)
		}
	}
}

func drainPending(pending map[string]struct{}) []string {
	if len(pending) == 0 {
		return nil
	}
	domains := make([]string, 0, len(pending))
	for domain := range pending {
		domains = append(domains, domain)
	}
	for domain := range pending {
		delete(pending, domain)
	}
	sort.Strings(domains)
	return domains
}

func (b *InProc) Subscribe(_ context.Context) (<-chan []string, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	ch := make(chan []string, 8)
	b.listeners[id] = ch
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.listeners, id)
			close(ch)
			b.mu.Unlock()
		})
	}
	return ch, cancel
}

func (b *InProc) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	for id, ch := range b.listeners {
		close(ch)
		delete(b.listeners, id)
	}
}

// ---- Redis pub/sub 实现(跨实例分发;本实例发布即刻经订阅回流,不丢自身事件) ----

const redisChannel = "platform:config-events"

type RedisBus struct {
	client redis.UniversalClient
	inner  *InProc // 本地合并与订阅者管理复用进程内实现
	pubsub *redis.PubSub
}

func NewRedis(ctx context.Context, client redis.UniversalClient) (*RedisBus, error) {
	bus := &RedisBus{client: client, inner: NewInProc()}
	pubsub := client.Subscribe(ctx, redisChannel)
	// 先建立订阅再返回,保证 emit 不丢本实例事件。
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, err
	}
	bus.pubsub = pubsub
	go bus.consume(pubsub.Channel())
	return bus, nil
}

func (b *RedisBus) consume(ch <-chan *redis.Message) {
	for message := range ch {
		var payload struct {
			Domains []string `json:"domains"`
		}
		if err := json.Unmarshal([]byte(message.Payload), &payload); err != nil {
			continue
		}
		b.inner.Emit(payload.Domains...)
	}
}

func (b *RedisBus) Emit(domains ...string) {
	if len(domains) == 0 {
		return
	}
	payload, err := json.Marshal(map[string]any{"domains": domains})
	if err != nil {
		return
	}
	// 发布失败:仍本地合并一次,避免单实例场景完全静默(错误由调用方日志观测)。
	if err := b.client.Publish(context.Background(), redisChannel, payload).Err(); err != nil {
		b.inner.Emit(domains...)
	}
}

func (b *RedisBus) Subscribe(ctx context.Context) (<-chan []string, func()) {
	return b.inner.Subscribe(ctx)
}

func (b *RedisBus) Close() {
	_ = b.pubsub.Close()
	b.inner.Close()
}
