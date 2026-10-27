package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

type referenceBillingStore struct {
	store.PlatformStore
	failSettlement atomic.Bool
	lostSettlement atomic.Bool
	failPublish    atomic.Bool
	lostIntent     atomic.Bool
	lostSuccess    atomic.Bool
}

func (s *referenceBillingStore) SettleReservation(ctx context.Context, id string, usage store.UsageRecord) error {
	if s.failSettlement.Swap(false) {
		return errors.New("injected unavailable ledger")
	}
	if err := s.PlatformStore.SettleReservation(ctx, id, usage); err != nil {
		return err
	}
	if s.lostSettlement.Swap(false) {
		return errors.New("injected lost settlement response")
	}
	return nil
}

func (s *referenceBillingStore) CompareAndSwapConfigValue(ctx context.Context, key string, previous, next []byte) (bool, error) {
	if strings.HasPrefix(key, "media:assets:") && s.failPublish.Swap(false) {
		return false, errors.New("injected unavailable asset registry")
	}
	ok, err := s.PlatformStore.CompareAndSwapConfigValue(ctx, key, previous, next)
	if err != nil || !ok || !strings.HasPrefix(key, "media:reference-import:") {
		return ok, err
	}
	var operation referenceOperation
	_ = json.Unmarshal(next, &operation)
	if (operation.Status == "settlement_pending" && s.lostIntent.Swap(false)) || (operation.Status == "succeeded" && s.lostSuccess.Swap(false)) {
		return false, errors.New("injected lost operation response")
	}
	return ok, err
}

func referenceBillingFixture(t *testing.T) (*fixture, *atomic.Int64, []byte) {
	t.Helper()
	f := setup(t)
	calls := &atomic.Int64{}
	video := append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, make([]byte, 512)...)
	f.s.SourceParser = &SourceParser{Key: "fixture-secret", Preflight: referenceFixturePreflight, Client: &http.Client{Transport: referenceTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == SnapAnyExtractURL {
			calls.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"medias":[{"media_type":"video","resource_url":"https://cdn.example.test/video.mp4"}]}`)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(video)), Header: make(http.Header)}, nil
	})}}
	f.s.ReferenceProbe = func(context.Context, string) (ReferenceMetadata, error) {
		return ReferenceMetadata{DurationSeconds: 7.3, Width: 1080, Height: 1920}, nil
	}
	return f, calls, video
}

func referenceBillingState(t *testing.T, f *fixture, expected string, count int) {
	t.Helper()
	team, err := f.s.Repo.Store.GetTeam(context.Background(), f.team)
	if err != nil || team.UsedCredits != expected || team.ReservedCredits != "0.000000" {
		t.Fatalf("unexpected ledger balance: %+v %v", team, err)
	}
	usage, err := f.s.Repo.Store.ListUsage(context.Background(), store.UsageFilter{TeamID: f.team})
	if err != nil || len(usage) != count {
		t.Fatalf("unexpected usage count: %d %v", len(usage), err)
	}
	for _, entry := range usage {
		if entry.Model != "media:source-import" || entry.InputTokens != 0 || entry.OutputTokens != 0 {
			t.Fatalf("fixed fee must not fabricate model tokens: %+v", entry)
		}
	}
}

func TestReferenceImportFixedFeeAndFreeLocalUpload(t *testing.T) {
	f, calls, video := referenceBillingFixture(t)
	ctx := context.Background()
	local, err := f.s.UploadReference(ctx, f.team, "local.mp4", bytes.NewReader(video), "")
	if err != nil || local.SourceURL != "" {
		t.Fatalf("local upload: %+v %v", local, err)
	}
	referenceBillingState(t, f, "0.000000", 0)
	// 读取/选择团队原件只访问资产投影，不触发收费或解析。
	if _, err = f.s.Repo.Assets(ctx, f.team); err != nil || calls.Load() != 0 {
		t.Fatal("team asset selection unexpectedly parsed")
	}
	id := uuid.NewString()
	asset, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video")
	if err != nil {
		t.Fatal(err)
	}
	referenceBillingState(t, f, "0.500000", 1)
	again, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video")
	if err != nil || again.ID != asset.ID || calls.Load() != 1 {
		t.Fatalf("import replay: %+v %v calls=%d", again, err, calls.Load())
	}
	referenceBillingState(t, f, "0.500000", 1)
}

func TestReferenceImportAdmissionRejectsBeforeParserAndDoesNotFreeze(t *testing.T) {
	for _, kind := range []string{"balance", "daily-budget", "monthly-budget", "member-budget", "inactive"} {
		t.Run(kind, func(t *testing.T) {
			f, calls, _ := referenceBillingFixture(t)
			ctx := context.Background()
			var err error
			expected := "budget_exhausted"
			switch kind {
			case "balance":
				quota := "0.499999"
				_, err = f.s.Repo.Store.UpdateTeam(ctx, f.team, store.UpdateTeamPatch{QuotaCredits: &quota})
				expected = "insufficient_credits"
			case "daily-budget":
				_, err = f.s.Repo.Store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: f.team, DailyBudgetCredits: "0.499999"})
			case "monthly-budget":
				_, err = f.s.Repo.Store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: f.team, MonthlyBudgetCredits: "0.499999"})
			case "member-budget":
				limit := "0.499999"
				_, err = f.s.Repo.Store.UpdateTeamMember(ctx, f.team, f.user, store.TeamMemberPatch{MonthlyBudgetCredits: &limit})
			case "inactive":
				disabled := time.Now().UTC()
				_, err = f.s.Repo.Store.UpdateTeam(ctx, f.team, store.UpdateTeamPatch{HasDisabled: true, DisabledAt: &disabled})
				expected = "team_inactive"
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.ImportReference(ctx, f.team, f.user, uuid.NewString(), "https://example.test/video"); err == nil || err.Error() != expected || calls.Load() != 0 {
				t.Fatalf("admission must precede paid call: %v calls=%d", err, calls.Load())
			}
			referenceBillingState(t, f, "0.000000", 0)
		})
	}
}

func TestReferenceImportFailureClosesIdentityWithoutCharge(t *testing.T) {
	for _, stage := range []string{"parser", "download", "validation"} {
		t.Run(stage, func(t *testing.T) {
			f, calls, _ := referenceBillingFixture(t)
			ctx := context.Background()
			base := f.s.SourceParser.Client.Transport
			f.s.SourceParser.Client.Transport = referenceTransport(func(r *http.Request) (*http.Response, error) {
				if (stage == "parser" && r.URL.String() == SnapAnyExtractURL) || (stage == "download" && r.URL.String() != SnapAnyExtractURL) {
					return nil, errors.New("injected connection failure")
				}
				return base.RoundTrip(r)
			})
			if stage == "validation" {
				f.s.ReferenceProbe = func(context.Context, string) (ReferenceMetadata, error) {
					return ReferenceMetadata{}, errors.New("source_video_probe_failed")
				}
			}
			id := uuid.NewString()
			if _, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video"); err == nil {
				t.Fatal("failure unexpectedly succeeded")
			}
			referenceBillingState(t, f, "0.000000", 0)
			fee := newFixedFee(f.team, f.user, "media:source-import:"+id, "media:source-import", ReferenceImportFeeMicro)
			identity, err := f.s.Repo.Store.GetQuotaReservation(ctx, fee.ID)
			if err != nil || identity == nil || identity.Status != store.ReservationReleased {
				t.Fatalf("failed import retained an open identity: %+v %v", identity, err)
			}
			before := calls.Load()
			if _, err = f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video"); err == nil || calls.Load() != before {
				t.Fatal("failed operation retried provider")
			}
			assets, _ := f.s.Repo.Assets(ctx, f.team)
			if len(assets) != 1 {
				t.Fatal("failed operation published an asset")
			}
		})
	}
}

func TestReferenceImportRecoversPendingWithoutReparseOrReprice(t *testing.T) {
	for _, fault := range []string{"settlement-failed", "settlement-response-lost", "publish-failed", "intent-response-lost", "success-response-lost"} {
		t.Run(fault, func(t *testing.T) {
			f, calls, _ := referenceBillingFixture(t)
			ctx := context.Background()
			faults := &referenceBillingStore{PlatformStore: f.s.Repo.Store}
			switch fault {
			case "settlement-failed":
				faults.failSettlement.Store(true)
			case "settlement-response-lost":
				faults.lostSettlement.Store(true)
			case "publish-failed":
				faults.failPublish.Store(true)
			case "intent-response-lost":
				faults.lostIntent.Store(true)
			case "success-response-lost":
				faults.lostSuccess.Store(true)
			}
			f.s.Repo.Store = faults
			id := uuid.NewString()
			if _, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video"); err == nil || err.Error() != "source_import_settlement_pending" {
				t.Fatalf("injected failure not preserved: %v", err)
			}
			raw, err := faults.GetConfigValue(ctx, "media:reference-import:"+f.team+":"+f.user+":"+id)
			if err != nil {
				t.Fatal(err)
			}
			var operation referenceOperation
			if json.Unmarshal(raw, &operation) != nil || operation.AssetID == "" || operation.Fee == nil {
				t.Fatalf("missing recovery intent: %s", raw)
			}
			if _, err = os.Stat(f.s.Blobs.Path(f.team, operation.AssetID)); err != nil {
				t.Fatalf("pending file was deleted: %v", err)
			}
			assets, _ := f.s.Repo.Assets(ctx, f.team)
			if fault != "success-response-lost" && len(assets) != 1 {
				t.Fatal("unsettled asset was published")
			}
			// 模拟另一 API 实例：无解析器配置，价格也已改变，只能沿用持久产物/锁价恢复。
			nextPrice, _ := json.Marshal(ReferencePricing{SnapAnyImportMicro: 900_000, AnalysisMicro: ReferenceAnalysisFeeMicro})
			if err = faults.SetConfigValue(ctx, ReferencePricingKey, nextPrice); err != nil {
				t.Fatal(err)
			}
			resumed := *f.s
			resumed.SourceParser = nil
			asset, err := resumed.ImportReference(ctx, f.team, f.user, id, "https://example.test/video")
			if err != nil || asset.ID != operation.AssetID || asset.Checksum == "" || asset.DurationSeconds != 7.3 || calls.Load() != 1 {
				t.Fatalf("pending replay: %+v %v calls=%d", asset, err, calls.Load())
			}
			referenceBillingState(t, f, "0.500000", 1)
		})
	}
}

func TestReferenceImportConcurrentReplaysSettleAndPublishOnce(t *testing.T) {
	f, calls, _ := referenceBillingFixture(t)
	ctx := context.Background()
	storeFaults := &referenceBillingStore{PlatformStore: f.s.Repo.Store}
	storeFaults.failSettlement.Store(true)
	f.s.Repo.Store = storeFaults
	id := uuid.NewString()
	if _, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video"); err == nil {
		t.Fatal("expected pending settlement")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			asset, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video")
			if err != nil || asset.ID == "" {
				t.Errorf("concurrent replay: %+v %v", asset, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("provider called %d times", calls.Load())
	}
	referenceBillingState(t, f, "0.500000", 1)
	assets, _ := f.s.Repo.Assets(ctx, f.team)
	if len(assets) != 2 {
		t.Fatalf("published duplicate assets: %d", len(assets))
	}
}

func TestReferenceImportConcurrentClaimsCallParserOnce(t *testing.T) {
	f, calls, _ := referenceBillingFixture(t)
	ctx := context.Background()
	entered, finish := make(chan struct{}), make(chan struct{})
	base := f.s.SourceParser.Client.Transport
	f.s.SourceParser.Client.Transport = referenceTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == SnapAnyExtractURL {
			referenceBillingState(t, f, "0.000000", 0)
			close(entered)
			<-finish
		}
		return base.RoundTrip(r)
	})
	id := uuid.NewString()
	const contenders = 12
	start, results := make(chan struct{}), make(chan error, contenders)
	for range contenders {
		go func() {
			<-start
			_, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video")
			results <- err
		}()
	}
	close(start)
	<-entered
	for range contenders - 1 {
		err := <-results
		if err == nil || err.Error() != "source_import_in_progress" {
			t.Errorf("non-owner was allowed to parse: %v", err)
		}
	}
	close(finish)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider called %d times", calls.Load())
	}
	referenceBillingState(t, f, "0.500000", 1)
}

func TestReferenceImportMissingPendingFileDoesNotCharge(t *testing.T) {
	f, calls, _ := referenceBillingFixture(t)
	ctx := context.Background()
	faults := &referenceBillingStore{PlatformStore: f.s.Repo.Store}
	faults.failSettlement.Store(true)
	f.s.Repo.Store = faults
	id := uuid.NewString()
	if _, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video"); err == nil {
		t.Fatal("expected pending settlement")
	}
	raw, _ := faults.GetConfigValue(ctx, "media:reference-import:"+f.team+":"+f.user+":"+id)
	var operation referenceOperation
	_ = json.Unmarshal(raw, &operation)
	if err := os.Remove(f.s.Blobs.Path(f.team, operation.AssetID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video"); err == nil || err.Error() != "source_import_unavailable" || calls.Load() != 1 {
		t.Fatalf("missing file was delivered or reparsed: %v", err)
	}
	referenceBillingState(t, f, "0.000000", 0)
}

func TestFixedFeeRejectsIdentityCollisionAndOtherTerminalUsage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	fee := newFixedFee(f.team, f.user, "media:source-import:"+uuid.NewString(), "media:source-import", ReferenceImportFeeMicro)
	if err := f.s.admitFixedFee(ctx, f.team, f.user, fee); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{f.user, uuid.NewString()} {
		wrong := fee
		wrong.RequestID += "-other"
		if err := f.s.admitFixedFee(ctx, f.team, user, wrong); err == nil {
			t.Fatal("duplicate identity was accepted for another request/owner")
		}
		if user != f.user {
			if err := f.s.settleFixedFee(ctx, f.team, user, "", fee); err == nil {
				t.Fatal("different owner settled the fixed fee")
			}
		}
	}
	if err := f.s.releaseFixedFee(ctx, f.team, f.user, fee); err != nil {
		t.Fatal(err)
	}
	if err := f.s.settleFixedFee(ctx, f.team, f.user, "", fee); err == nil {
		t.Fatal("released identity was treated as charged")
	}
	other := newFixedFee(f.team, f.user, "media:source-import:"+uuid.NewString(), "media:source-import", ReferenceImportFeeMicro)
	if err := f.s.admitFixedFee(ctx, f.team, f.user, other); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Repo.Store.SettleReservation(ctx, other.ID, store.UsageRecord{ID: uuid.NewString(), TeamID: f.team, UserID: f.user, RequestID: other.RequestID, Status: store.UsageStatusAborted, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.settleFixedFee(ctx, f.team, f.user, "", other); err == nil {
		t.Fatal("another zero-usage terminal was treated as this fee receipt")
	}
}

func TestReferenceImportLegacySuccessIsNotCharged(t *testing.T) {
	f, calls, _ := referenceBillingFixture(t)
	ctx := context.Background()
	id := uuid.NewString()
	digest := sha256.Sum256([]byte("https://example.test/video"))
	asset := Asset{ID: uuid.NewString(), Kind: "video", DurationSeconds: 7.3}
	if err := f.s.Repo.AddAsset(ctx, f.team, asset); err != nil {
		t.Fatal(err)
	}
	old, _ := json.Marshal(referenceOperation{Digest: hex.EncodeToString(digest[:]), Status: "succeeded", AssetID: asset.ID})
	if err := f.s.Repo.Store.SetConfigValue(ctx, "media:reference-import:"+f.team+":"+f.user+":"+id, old); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video")
	if err != nil || got.ID != asset.ID || calls.Load() != 0 {
		t.Fatalf("legacy replay: %+v %v", got, err)
	}
	referenceBillingState(t, f, "0.000000", 0)
}

func TestReferenceImportUsesConfiguredAndZeroPrice(t *testing.T) {
	for _, price := range []int64{250_000, 0} {
		f, calls, _ := referenceBillingFixture(t)
		ctx := context.Background()
		raw, _ := json.Marshal(ReferencePricing{SnapAnyImportMicro: price, AnalysisMicro: ReferenceAnalysisFeeMicro})
		if err := f.s.Repo.Store.SetConfigValue(ctx, ReferencePricingKey, raw); err != nil {
			t.Fatal(err)
		}
		id := uuid.NewString()
		if _, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video"); err != nil || calls.Load() != 1 {
			t.Fatalf("configured price replay: %v", err)
		}
		usage, err := f.s.Repo.Store.ListUsage(ctx, store.UsageFilter{TeamID: f.team})
		if err != nil || len(usage) != 1 || usage[0].CreditsMicro != price {
			t.Fatalf("wrong configured fee: %+v %v", usage, err)
		}
	}
}
