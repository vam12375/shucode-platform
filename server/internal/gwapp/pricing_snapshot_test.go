package gwapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/pricing"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

type unavailablePricingStore struct{ store.PlatformStore }

func (s *unavailablePricingStore) GetModelPricing(context.Context) (*store.PricingDocSnapshot, error) {
	return nil, errors.New("pricing temporarily unavailable")
}

func snapshotCostEntry(modelID, currency string) store.PricingEntry {
	return store.PricingEntry{
		ModelID: modelID, DisplayName: modelID, InputCostPerMillion: "1", OutputCostPerMillion: "2",
		CacheReadCostPerMillion: "0.1", CacheCreationCostPerMillion: "0.4", Currency: currency,
	}
}

func TestGatewayFreezesSaleAndReferenceCostBeforeUpstream(t *testing.T) {
	f := newGatewayFixture(t, nil)
	ctx := context.Background()
	entry := snapshotCostEntry("claude-sonnet-4-5", "CNY")
	doc, err := f.store.SaveModelPricing(ctx, []store.PricingEntry{entry}, pricing.EmptyVersion)
	if err != nil || doc == nil {
		t.Fatalf("保存参考成本: %+v %v", doc, err)
	}
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		entry.InputCostPerMillion = "99"
		if _, err := f.store.SaveModelPricing(ctx, []store.PricingEntry{entry}, doc.Version); err != nil {
			t.Error(err)
		}
		if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{
			ModelID: "claude-sonnet-4-5", DisplayName: "Changed", InputPricePerMtok: "100", OutputPricePerMtok: "200",
		}); err != nil {
			t.Error(err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":40}}}`))
	})
	rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`)
	if rec.Code != 200 {
		t.Fatalf("网关请求失败: %d %s", rec.Code, rec.Body.String())
	}
	records := f.usageRecords()
	if len(records) != 1 || records[0].PriceSnapshot == nil {
		t.Fatalf("用量必须保留价格快照: %+v", records)
	}
	snapshot := records[0].PriceSnapshot
	if records[0].CreditsMicro != 942 || snapshot.Sale.InputPerMtok != "3.000000" || snapshot.Sale.OutputPerMtok != "15.000000" || snapshot.Sale.Currency != "CNY" {
		t.Fatalf("成交价及实际扣费不能随上游期间改价而漂移: %+v %+v", records[0], snapshot)
	}
	if snapshot.Cost.Status != "estimated" || snapshot.Cost.PricingVersion != doc.Version || snapshot.Cost.Rates.InputPerMtok != "1" || snapshot.Cost.EstimatedMicro == nil || *snapshot.Cost.EstimatedMicro != 164 {
		t.Fatalf("参考成本必须使用请求前的冻结版本: %+v", snapshot.Cost)
	}
}

func TestGatewayMissingCostIsUnknownAndDoesNotChangeBilling(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "not configured", true: "read unavailable"}[unavailable], func(t *testing.T) {
			f := newGatewayFixture(t, func(_ *Env, f *gatewayFixture) {
				if unavailable {
					f.store = &unavailablePricingStore{PlatformStore: f.store}
				}
			})
			f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "application/json")
				_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
			})
			rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`)
			if rec.Code != 200 {
				t.Fatalf("缺少参考成本不得阻断正常计费: %d %s", rec.Code, rec.Body.String())
			}
			record := f.usageRecords()[0]
			if record.CreditsMicro != 18 || record.PriceSnapshot == nil || record.PriceSnapshot.Cost.Status != "unknown" || record.PriceSnapshot.Cost.EstimatedMicro != nil {
				t.Fatalf("未知采购成本不能记零或改变扣费: %+v", record)
			}
		})
	}
}

func TestUsagePricingSnapshotUsesAppliedMultipliersAndCurrency(t *testing.T) {
	f := newGatewayFixture(t, nil)
	ctx := context.Background()
	entry := snapshotCostEntry("vendor-model", "USD")
	entry.OffPeakMultiplier = "0.25"
	if _, err := f.store.SaveModelPricing(ctx, []store.PricingEntry{entry}, pricing.EmptyVersion); err != nil {
		t.Fatal(err)
	}
	model, err := f.store.GetModel(ctx, "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	model.OffPeakMultiplier = "0.5"
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) // 周日空闲时段。
	capture := captureUsagePricing(ctx, Deps{Store: f.store, Env: f.env, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, model, at)
	snapshot := capture.snapshot(upstreamTarget{UpstreamModel: "vendor-model"}, UsageCounts{InputTokens: 100, OutputTokens: 50}, store.UsageStatusOk, "")
	if snapshot.Sale.Multiplier != "0.5" || snapshot.Cost.Rates.Multiplier != "0.25" || snapshot.Cost.Rates.Currency != "USD" || snapshot.Cost.ModelID != "vendor-model" || *snapshot.Cost.EstimatedMicro != 50 {
		t.Fatalf("必须冻结实际倍率、采购币种与真实上游模型: %+v", snapshot)
	}
	unknown := capture.snapshot(upstreamTarget{UpstreamModel: "vendor-model"}, UsageCounts{}, store.UsageStatusAborted, "client_aborted")
	if unknown.Cost.Status != "unknown" || unknown.Cost.EstimatedMicro != nil || unknown.Cost.Reason != "usage_unavailable" {
		t.Fatalf("断连无计量不能伪造零成本: %+v", unknown.Cost)
	}
}
