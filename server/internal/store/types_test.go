package store

import (
	"testing"
	"time"
)

func TestDeviceTokenExpired(t *testing.T) {
	now := time.Now().UTC()
	record := DeviceTokenRecord{CreatedAt: now.Add(-100 * 24 * time.Hour)}
	if record.Expired(now, 0) {
		t.Fatal("ttl=0 不应按年龄过期")
	}
	if !record.Expired(now, 90*24*time.Hour) {
		t.Fatal("超过 TTL 应判定过期")
	}
	record.CreatedAt = now.Add(-10 * 24 * time.Hour)
	if record.Expired(now, 90*24*time.Hour) {
		t.Fatal("未超过 TTL 不应过期")
	}
}
