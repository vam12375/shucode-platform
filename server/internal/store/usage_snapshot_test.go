package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

func runUsageSnapshotSuite(t *testing.T, ctx context.Context, s PlatformStore) {
	for _, mode := range []string{"insert", "settle"} {
		t.Run(mode, func(t *testing.T) {
			user := mustUser(t, ctx, s, fmt.Sprintf("snapshot-%s@test.local", mode))
			team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "snapshot-" + mode, QuotaCredits: "100"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.AddMember(ctx, team.ID, user.ID, MemberRoleOwner); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
			micro := int64(17)
			budget := int64(8192)
			snapshot := &UsagePricingSnapshot{
				SchemaVersion: 1, CapturedAt: at,
				Sale: UsagePriceSnapshot{Unit: "request", PerRequest: "0.005000", InputPerMtok: "3", OutputPerMtok: "15", CacheReadPerMtok: "0.3", CacheWritePerMtok: "3.75", Multiplier: "1", Currency: "CNY"},
				Cost: UsageCostSnapshot{
					Status: "estimated", PricingVersion: "prc_frozen", ModelID: "upstream-snapshot-model", EstimatedMicro: &micro,
					Rates: &UsagePriceSnapshot{InputPerMtok: "1", OutputPerMtok: "2", CacheReadPerMtok: "0.1", CacheWritePerMtok: "0.5", Multiplier: "0.5", Currency: "USD"},
				},
			}
			record := UsageRecord{
				ID: uuid.NewString(), RequestID: "snapshot-" + mode, UserID: user.ID, TeamID: team.ID,
				Model: "snapshot-model", UpstreamModel: "upstream-snapshot-model", InputTokens: 3, OutputTokens: 2,
				CreditsMicro: 31, Status: UsageStatusOk, CreatedAt: at, PriceSnapshot: snapshot,
				ReasoningEffort: "high", ReasoningBudgetTokens: &budget,
			}
			if mode == "settle" {
				reservationID := uuid.NewString()
				result, err := s.ReserveQuota(ctx, ReserveQuotaInput{ID: reservationID, TeamID: team.ID, UserID: user.ID, RequestID: record.RequestID, Micro: 100, CreatedAt: at})
				if err != nil || result != ReserveReserved {
					t.Fatalf("预留失败: %s %v", result, err)
				}
				if err := s.SettleReservation(ctx, reservationID, record); err != nil {
					t.Fatal(err)
				}
				changed := record
				changed.PriceSnapshot = nil
				if err := s.SettleReservation(ctx, reservationID, changed); err != nil {
					t.Fatal(err)
				}
				gotTeam, err := s.GetTeam(ctx, team.ID)
				if err != nil || gotTeam.UsedCredits != "0.000031" || gotTeam.ReservedCredits != "0.000000" {
					t.Fatalf("快照不得改变额度结算与幂等: %+v %v", gotTeam, err)
				}
			} else if err := s.InsertUsage(ctx, record); err != nil {
				t.Fatal(err)
			}
			// 根因：结构体值拷贝仍共享快照指针，调用方修改会篡改历史账本。
			snapshot.Sale.InputPerMtok = "999"
			snapshot.Sale.PerRequest = "999"
			snapshot.Cost.Rates.Currency = "CNY"
			*snapshot.Cost.EstimatedMicro = 999
			budget = 1
			assertStored := func(records []UsageRecord, err error) {
				t.Helper()
				if err != nil || len(records) != 1 || records[0].PriceSnapshot == nil {
					t.Fatalf("快照读取失败: %+v %v", records, err)
				}
				got := records[0].PriceSnapshot
				if records[0].ReasoningEffort != "high" || records[0].ReasoningBudgetTokens == nil || *records[0].ReasoningBudgetTokens != 8192 {
					t.Fatalf("request reasoning was lost or aliased: %+v", records[0])
				}
				if got.Sale.InputPerMtok != "3" || got.Sale.Unit != "request" || got.Sale.PerRequest != "0.005000" || got.Cost.Rates.Currency != "USD" || got.Cost.EstimatedMicro == nil || *got.Cost.EstimatedMicro != 17 || got.Cost.PricingVersion != "prc_frozen" || !got.CapturedAt.Equal(at) {
					t.Fatalf("历史价格快照被篡改: %+v", got)
				}
			}
			filter := UsageFilter{TeamID: team.ID}
			records, err := s.ListUsage(ctx, filter)
			assertStored(records, err)
			records[0].PriceSnapshot.Sale.InputPerMtok = "888"
			records[0].PriceSnapshot.Cost.Rates.Currency = "CNY"
			*records[0].PriceSnapshot.Cost.EstimatedMicro = 888
			*records[0].ReasoningBudgetTokens = 2
			assertStored(s.ListUsage(ctx, filter))
			exported, err := s.ListUsageForExport(ctx, filter, 10)
			assertStored(exported, err)
			exported[0].PriceSnapshot.Cost.Rates.Currency = "CNY"
			assertStored(s.ListUsageForExport(ctx, filter, 10))
			old := record
			old.ID, old.RequestID, old.PriceSnapshot = uuid.NewString(), record.RequestID+"-legacy", nil
			old.ReasoningEffort, old.ReasoningBudgetTokens = "", nil
			if err := s.InsertUsage(ctx, old); err != nil {
				t.Fatal(err)
			}
			legacy, err := s.ListUsage(ctx, UsageFilter{TeamID: team.ID, RequestID: old.RequestID})
			if err != nil || len(legacy) != 1 || legacy[0].PriceSnapshot != nil || legacy[0].ReasoningEffort != "" || legacy[0].ReasoningBudgetTokens != nil {
				t.Fatalf("旧记录必须保留未知成本，不能回填零: %+v %v", legacy, err)
			}
		})
	}
}
