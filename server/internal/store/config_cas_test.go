package store

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMemoryConfigCASConformance(t *testing.T) {
	s, closeStore := newMemoryForTest(t)
	defer closeStore()
	runConfigCASSuite(t, s)
}
func TestPostgresConfigCASConformance(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 TEST_DATABASE_URL,跳过 Postgres CAS")
	}
	s, closeStore := newPostgresForTest(t, url, os.Getenv("TEST_MIGRATIONS_DIR"))
	defer closeStore()
	runConfigCASSuite(t, s)
}
func runConfigCASSuite(t *testing.T, s PlatformStore) {
	ctx := context.Background()
	key := "security-test-cas"
	swapped, err := s.CompareAndSwapConfigValue(ctx, key, nil, []byte(`{"b":2,"a":1}`))
	if err != nil || !swapped {
		t.Fatalf("首次插入CAS: %v %v", swapped, err)
	}
	if swapped, err := s.CompareAndSwapConfigValue(ctx, key, nil, []byte(`{}`)); err != nil || swapped {
		t.Fatalf("nil expected不能覆盖已存在值: %v %v", swapped, err)
	}
	if swapped, err := s.CompareAndSwapConfigValue(ctx, key, []byte(`{"a":0,"b":2}`), []byte(`{}`)); err != nil || swapped {
		t.Fatalf("错误expected不能覆盖: %v %v", swapped, err)
	}
	var success atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.CompareAndSwapConfigValue(ctx, key, []byte(`{ "a":1.0,"b":2 }`), []byte(`{"consumed":true}`))
			if err != nil {
				errs <- err
			} else if ok {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if success.Load() != 1 {
		t.Fatalf("JSON语义CAS仅一次成功: %d", success.Load())
	}
	if _, err := s.CompareAndSwapConfigValue(ctx, key, nil, []byte(`invalid`)); err == nil {
		t.Fatal("无效JSON不能写入")
	}
}
