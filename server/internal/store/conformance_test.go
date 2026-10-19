package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// conformance 套件:内存实现必跑;Postgres 实现由 TEST_DATABASE_URL 启用
// (包内自建/自删 scratch 库并应用 packages/db/migrations,与 TS 完全同一份 schema)。

func newMemoryForTest(t *testing.T) (*memoryStore, func()) {
	t.Helper()
	ctx := context.Background()
	s, err := NewMemoryStore(ctx, MemoryOptions{})
	if err != nil {
		t.Fatalf("内存库构造: %v", err)
	}
	return s, func() { _ = s.Dispose(ctx) }
}

func TestMemoryStoreSuite(t *testing.T) {
	store, closer := newMemoryForTest(t)
	defer closer()
	runSuite(t, context.Background(), store)
}

func TestPostgresStoreSuite(t *testing.T) {
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("未设置 TEST_DATABASE_URL,跳过 Postgres conformance")
	}
	migrationsDir := os.Getenv("TEST_MIGRATIONS_DIR")
	if migrationsDir == "" {
		migrationsDir = "../../migrations"
	}
	store, closer := newPostgresForTest(t, baseURL, migrationsDir)
	defer closer()
	runSuite(t, context.Background(), store)
}

// TestCrossImplParity:同一份 usage 数据在 PG 与内存实现的
// listUsage/aggregate/series 输出必须完全一致(series 分桶逐一对比)。
func TestCrossImplParity(t *testing.T) {
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("未设置 TEST_DATABASE_URL,跳过跨实现对比")
	}
	mem, memClose := newMemoryForTest(t)
	defer memClose()
	pg, pgClose := newPostgresForTest(t, baseURL, os.Getenv("TEST_MIGRATIONS_DIR"))
	defer pgClose()

	ctx := context.Background()
	member, err := pg.CreateUser(ctx, UserInput{
		Email: "parity@test.local", PasswordHash: "x", DisplayName: "P", Role: RoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateUser(ctx, UserInput{
		Email: "parity@test.local", PasswordHash: "x", DisplayName: "P", Role: RoleUser,
	}); err != nil {
		t.Fatal(err)
	}
	team, err := pg.CreateTeam(ctx, CreateTeamInput{Name: "parity-team", QuotaCredits: "100.000000"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateTeam(ctx, CreateTeamInput{Name: "parity-team", QuotaCredits: "100.000000"}); err != nil {
		t.Fatal(err)
	}
	// 关键时间点:跨上海墙钟日界(15:59:59Z→17 日 / 16:00:00Z→18 日)与整点边界;
	// 每条记录再错开 1ms,保证 ORDER BY created_at 无并列(翻页断言确定性)。
	timestamps := []time.Time{
		time.Date(2026, 10, 17, 15, 59, 59, 0, time.UTC),
		time.Date(2026, 10, 17, 16, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 18, 1, 30, 15, 0, time.UTC),
	}
	models := []string{"claude-sonnet-4-5", "claude-haiku-4-5"}
	var microTotal int64
	for i, at := range timestamps {
		for j, model := range models {
			for _, s := range []PlatformStore{pg, mem} {
				if err := s.InsertUsage(ctx, UsageRecord{
					ID:        fmt.Sprintf("00000000-0000-4000-8000-%012d", i*100+j+len(timestamps)*1000),
					RequestID: fmt.Sprintf("req-%d-%d", i, j),
					UserID:    member.ID, TeamID: team.ID,
					Model: model, UpstreamModel: model + "-upstream",
					InputTokens: int64(100 + i*10), OutputTokens: int64(50 + j*5),
					CacheReadTokens: 7, CacheCreationTokens: 3,
					CreditsMicro: int64(1000*(i+1) + 10*j),
					Status:       UsageStatusOk,
					ClientIP:     "203.0.113.7",
					LatencyMs:    ptr(int64(120 + i)),
					CreatedAt:    at.Add(time.Duration(j) * time.Millisecond),
				}); err != nil {
					t.Fatal(err)
				}
			}
			microTotal += int64(1000*(i+1) + 10*j)
		}
	}
	filter := UsageFilter{TeamID: team.ID}

	// 三个维度逐一对齐(经 JSON 归一化后深比较)。
	memUsage, err := mem.ListUsage(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	pgUsage, err := pg.ListUsage(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEq(t, memUsage, pgUsage, "listUsage")

	memAgg, _ := mem.AggregateUsage(ctx, filter)
	pgAgg, err := pg.AggregateUsage(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEq(t, memAgg, pgAgg, "aggregateUsage")
	var total int64
	for _, row := range pgAgg {
		total += row.CreditsMicro
	}
	if total != microTotal {
		t.Fatalf("聚合 micro 总额不符: got %d want %d", total, microTotal)
	}

	for _, gran := range []UsageSeriesGranularity{SeriesGranularityDay, SeriesGranularityHour} {
		memSeries, _ := mem.AggregateUsageSeries(ctx, filter, gran)
		pgSeries, err := pg.AggregateUsageSeries(ctx, filter, gran)
		if err != nil {
			t.Fatal(err)
		}
		assertJSONEq(t, memSeries, pgSeries, "series "+string(gran))
	}
	// 日桶必须跨上海墙钟日界:15:59Z 属 17 日、16:00Z 属 18 日;小时桶 +8。
	pgDay, _ := pg.AggregateUsageSeries(ctx, filter, SeriesGranularityDay)
	buckets := map[string]int64{}
	for _, row := range pgDay {
		buckets[row.BucketStart] = row.CreditsMicro
	}
	if got, ok := buckets["2026-10-17T00:00:00"]; !ok || got != 2010 {
		t.Fatalf("17 日桶不符(期望 1000+1010): %v", buckets)
	}
	if got, ok := buckets["2026-10-18T00:00:00"]; !ok || got != microTotal-2010 {
		t.Fatalf("18 日桶不符: %v", buckets)
	}
	pgHour, _ := pg.AggregateUsageSeries(ctx, filter, SeriesGranularityHour)
	if len(pgHour) == 0 || pgHour[0].BucketStart != "2026-10-17T23:00:00" {
		t.Fatalf("小时桶 +8 偏移不符: %v", pgHour)
	}

	// 游标翻页:两实现取最新的 3 条后,从最后一条继续翻页,分页位置一致。
	pgPage1, _ := pg.ListUsage(ctx, UsageFilter{TeamID: team.ID, Limit: ptr(int64(3))})
	if len(pgPage1) != 3 {
		t.Fatalf("首页应为 3 条: %d", len(pgPage1))
	}
	last := pgPage1[len(pgPage1)-1]
	cursorFilter := UsageFilter{TeamID: team.ID, Limit: ptr(int64(3)), CursorCreatedAt: &last.CreatedAt, CursorID: last.ID}
	pgPage2, err := pg.ListUsage(ctx, cursorFilter)
	if err != nil {
		t.Fatal(err)
	}
	memPage2, _ := mem.ListUsage(ctx, cursorFilter)
	assertJSONEq(t, memPage2, pgPage2, "cursor page2")
	if len(pgPage2)+3 != 6 {
		t.Fatalf("翻页应有剩余 3 条: %d", len(pgPage2))
	}
}

func newPostgresForTest(t *testing.T, baseURL, migrationsDir string) (*PostgresStore, func()) {
	t.Helper()
	// 跨实现测试直接调用此 helper，缺省目录必须在共用入口补齐。
	if migrationsDir == "" {
		migrationsDir = "../../migrations"
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	dbName := "goconf_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, dbName)); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("建 scratch 库失败: %v", err)
	}
	_ = admin.Close(ctx)

	// 迁移必须作用在 scratch 库连接上(不是管理连接)。
	storeURL := replaceDBName(baseURL, dbName)
	scratchConn, err := pgx.Connect(ctx, storeURL)
	if err != nil {
		t.Fatalf("连接 scratch 库失败: %v", err)
	}
	if err := RunMigrations(ctx, scratchConn, migrationsDir, func(string) {}); err != nil {
		_ = scratchConn.Close(ctx)
		t.Fatalf("迁移失败: %v", err)
	}
	_ = scratchConn.Close(ctx)

	store, err := NewPostgres(ctx, storeURL)
	if err != nil {
		t.Fatalf("构造 PG store: %v", err)
	}
	return store, func() {
		_ = store.Dispose(ctx)
		cleanup, cerr := pgx.Connect(ctx, baseURL)
		if cerr == nil {
			_, _ = cleanup.Exec(ctx, fmt.Sprintf(`DROP DATABASE %s`, dbName))
			_ = cleanup.Close(ctx)
		}
	}
}

func replaceDBName(baseURL, dbName string) string {
	naive := len(baseURL) - 1
	for i := naive; i >= 0; i-- {
		if baseURL[i] == '/' {
			return baseURL[:i+1] + dbName
		}
	}
	return baseURL
}

func assertJSONEq(t *testing.T, got, want any, label string) {
	t.Helper()
	normalize := func(v any) any {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s 序列化: %v", label, err)
		}
		var out any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s 反序列化: %v", label, err)
		}
		return out
	}
	if !jsonDeepEqual(normalize(got), normalize(want)) {
		t.Fatalf("%s 不一致:\n got %s\nwant %s", label, mustJSON(t, normalize(got)), mustJSON(t, normalize(want)))
	}
}

func jsonDeepEqual(a, b any) bool {
	return jsonBytesEqual(mustMarshalNoErr(a), mustMarshalNoErr(b))
}

func mustMarshalNoErr(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

func jsonBytesEqual(a, b []byte) bool { return string(a) == string(b) }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
