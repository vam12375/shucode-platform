package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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
		if err := ValidateAnalysisScope(ctx, f.s.Repo, box, test.ticket, test.team, test.user, test.device, test.model, 10, test.at); !errors.Is(err, ErrAnalysisScopeInvalid) {
			t.Errorf("%s accepted: %v", test.name, err)
		}
	}
	if err := ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", 100, now); err != nil {
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
			if err = ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", 100, now); !errors.Is(err, ErrAnalysisScopeInvalid) {
				t.Fatalf("%s remained usable: %v", kind, err)
			}
		})
	}
}

func TestAnalysisScopeReissueSharesRequestAndTokenBudget(t *testing.T) {
	for _, kind := range []string{"requests", "tokens"} {
		t.Run(kind, func(t *testing.T) {
			f, p, box, now := analysisScopeFixture(t)
			ctx := context.Background()
			ticket, err := IssueAnalysisScope(ctx, f.s.Repo, box, p, "model-a", now)
			if err != nil {
				t.Fatal(err)
			}
			count, tokens := AnalysisScopeMaxRequests, int64(1)
			if kind == "tokens" {
				count, tokens = 1, AnalysisScopeMaxTokens
			}
			for range count {
				if err = ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", tokens, now); err != nil {
					t.Fatal(err)
				}
			}
			ticket, err = IssueAnalysisScope(ctx, f.s.Repo, box, p, "model-a", now)
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", 1, now); !errors.Is(err, ErrAnalysisScopeBudget) {
				t.Fatalf("reissue reset %s budget: %v", kind, err)
			}
		})
	}
}

func TestAnalysisScopeConcurrentBudgetIsAtomic(t *testing.T) {
	f, p, box, now := analysisScopeFixture(t)
	ctx := context.Background()
	ticket, err := IssueAnalysisScope(ctx, f.s.Repo, box, p, "model-a", now)
	if err != nil {
		t.Fatal(err)
	}
	key := "media:analysis-scope-budget:" + p.TeamID + ":" + p.AnalysisFee.ID
	raw, _ := json.Marshal(analysisScopeBudget{Requests: AnalysisScopeMaxRequests - 2, Tokens: AnalysisScopeMaxTokens - 2})
	if err = f.s.Repo.Store.SetConfigValue(ctx, key, raw); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "model-a", 1, now)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrAnalysisScopeBudget) {
				t.Errorf("unexpected budget failure: %v", err)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 2 {
		t.Fatalf("budget admitted %d requests, want 2", accepted.Load())
	}
}
