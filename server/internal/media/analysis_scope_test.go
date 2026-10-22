package media

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
)

func analysisScopeFixture(t *testing.T) (*fixture, *Project, *crypto.SecretBox, time.Time) {
	t.Helper()
	f := setup(t)
	now := time.Now().UTC()
	fee := newFixedFee(f.team, f.user, "analysis:"+uuid.NewString(), "media:source-analysis", ReferenceAnalysisFeeMicro)
	if err := f.s.admitFixedFee(context.Background(), f.team, f.user, fee); err != nil {
		t.Fatal(err)
	}
	p := &Project{ID: uuid.NewString(), TeamID: f.team, UserID: f.user, DeviceID: uuid.NewString(), Version: 1, Status: "analyzing", Brief: Brief{ReferenceAssetID: "reference-video"}, Analysis: &Analysis{InputID: uuid.NewString()}, AnalysisFee: &fee}
	if err := f.s.Repo.Save(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
	box, err := crypto.NewSecretBox(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("t", 32))))
	if err != nil {
		t.Fatal(err)
	}
	return f, p, box, now
}

func TestAnalysisScopeBindsIdentityModelAndExpiry(t *testing.T) {
	f, p, box, now := analysisScopeFixture(t)
	ctx := context.Background()
	ticket, err := IssueAnalysisScope(ctx, f.s.Repo, box, p, "model-a", now)
	if err != nil || strings.Contains(ticket, p.ID) || strings.Contains(ticket, p.AnalysisFee.ID) {
		t.Fatalf("scope issuance/encryption failed: %v", err)
	}
	for _, test := range []struct {
		name, ticket, team, user, device, model string
		at                                      time.Time
	}{
		{"forged", ticket + "x", p.TeamID, p.UserID, p.DeviceID, "model-a", now},
		{"team", ticket, uuid.NewString(), p.UserID, p.DeviceID, "model-a", now},
		{"user", ticket, p.TeamID, uuid.NewString(), p.DeviceID, "model-a", now},
		{"device", ticket, p.TeamID, p.UserID, uuid.NewString(), "model-a", now},
		{"model", ticket, p.TeamID, p.UserID, p.DeviceID, "model-b", now},
		{"expired", ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", now.Add(time.Hour)},
		{"future", ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", now.Add(-time.Second)},
	} {
		if err := ValidateAnalysisScope(ctx, f.s.Repo, box, test.ticket, test.team, test.user, test.device, test.model, test.at, "high"); !errors.Is(err, ErrAnalysisScopeInvalid) {
			t.Errorf("%s accepted: %v", test.name, err)
		}
	}
	if err := ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", now, "high"); err != nil {
		t.Fatal(err)
	}
}

func TestAnalysisScopeRejectsEndedAndStaleBindings(t *testing.T) {
	for _, kind := range []string{"review", "cancelled", "analysis_settling", "new-input", "new-fee", "missing-source", "released", "settled"} {
		t.Run(kind, func(t *testing.T) {
			f, p, box, now := analysisScopeFixture(t)
			ctx := context.Background()
			ticket, err := IssueAnalysisScope(ctx, f.s.Repo, box, p, "model-a", now)
			if err != nil {
				t.Fatal(err)
			}
			next := *p
			analysis := *p.Analysis
			next.Analysis = &analysis
			fee := *p.AnalysisFee
			next.AnalysisFee = &fee
			switch kind {
			case "new-input":
				next.Analysis.InputID = uuid.NewString()
			case "new-fee":
				next.AnalysisFee.ID = uuid.NewString()
			case "missing-source":
				next.Brief.ReferenceAssetID = ""
			case "released":
				err = f.s.releaseFixedFee(ctx, p.TeamID, p.UserID, *p.AnalysisFee)
			case "settled":
				err = f.s.settleFixedFee(ctx, p.TeamID, p.UserID, p.DeviceID, *p.AnalysisFee)
			default:
				next.Status = kind
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.Repo.Save(ctx, &next, p); err != nil {
				t.Fatal(err)
			}
			if err = ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", now, "high"); !errors.Is(err, ErrAnalysisScopeInvalid) {
				t.Fatalf("%s remained usable: %v", kind, err)
			}
		})
	}
}

func TestAnalysisScopeAllowsRepeatedAndConcurrentAuthorizationWithoutBudgetState(t *testing.T) {
	f, p, box, now := analysisScopeFixture(t)
	ctx := context.Background()
	ticket, err := IssueAnalysisScope(ctx, f.s.Repo, box, p, "model-a", now)
	if err != nil {
		t.Fatal(err)
	}
	key := "media:analysis-scope-budget:" + p.TeamID + ":" + p.AnalysisFee.ID
	// 旧服务留下的损坏/耗尽记录不应成为新授权的输入，也不能继续写它。
	const legacy = `{"requests":128,"tokens":1000000}`
	if err = f.s.Repo.Store.SetConfigValue(ctx, key, []byte(legacy)); err != nil {
		t.Fatal(err)
	}
	for range 256 {
		if err = ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", now, "high"); err != nil {
			t.Fatalf("valid repeated authorization rejected: %v", err)
		}
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", now, "high"); err != nil {
				t.Errorf("valid concurrent authorization rejected: %v", err)
			}
		}()
	}
	wg.Wait()
	ticket, err = IssueAnalysisScope(ctx, f.s.Repo, box, p, "model-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", now, "high"); err != nil {
		t.Fatal(err)
	}
	raw, err := f.s.Repo.Store.GetConfigValue(ctx, key)
	if err != nil || string(raw) != legacy {
		t.Fatalf("authorization changed legacy budget state: %q %v", raw, err)
	}
}
