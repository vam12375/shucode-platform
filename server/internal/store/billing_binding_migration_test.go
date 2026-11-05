package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBillingBindingDeviceMigrationBackfillsExistingSessions(t *testing.T) {
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("需要 TEST_DATABASE_URL 验证已有 binding 迁移")
	}
	s, closeStore := newPostgresForTest(t, baseURL, os.Getenv("TEST_MIGRATIONS_DIR"))
	defer closeStore()
	ctx := context.Background()
	user := mustUser(t, ctx, s, "binding-migration@test.local")
	if _, err := s.EnsurePersonalAccount(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBillingMode(ctx, user.ID, "personal"); err != nil {
		t.Fatal(err)
	}
	deviceID := uuid.NewString()
	if err := s.CreateDeviceToken(ctx, DeviceTokenRecord{ID: deviceID, UserID: user.ID, TokenHash: uuid.NewString(), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	input := BillingSessionBindingInput{SessionID: uuid.NewString(), WorkspaceIdentity: "local:migration", ActorUserID: user.ID, DeviceTokenID: deviceID}
	before, err := s.CreateBillingSessionBinding(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	// 仅在本测试自建 scratch 库还原升级前结构，检查已有会话设备授权的回填。
	if _, err := s.pool.Exec(ctx, `DROP TABLE billing_session_binding_devices`); err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("TEST_MIGRATIONS_DIR")
	if dir == "" {
		dir = "../../migrations"
	}
	migration, err := os.ReadFile(filepath.Join(dir, "0039_billing_session_binding_devices.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	after, err := s.ResolveBillingSessionBinding(ctx, before.ID, user.ID, deviceID)
	if err != nil || after.ID != before.ID || after.BillingAccountID != before.BillingAccountID {
		t.Fatalf("migration lost existing binding: %+v %v", after, err)
	}
	if err := s.RevokeDeviceToken(ctx, deviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	input.DeviceTokenID = uuid.NewString()
	if err := s.CreateDeviceToken(ctx, DeviceTokenRecord{ID: input.DeviceTokenID, UserID: user.ID, TokenHash: uuid.NewString(), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if rebound, err := s.CreateBillingSessionBinding(ctx, input); err != nil || rebound.ID != before.ID {
		t.Fatalf("migrated session reauthentication: %+v %v", rebound, err)
	}
}
