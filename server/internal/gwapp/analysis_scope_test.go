package gwapp

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/pricing"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func gatewayAnalysisFixture(t *testing.T) (*gatewayFixture, media.Repository, *media.Project, *crypto.SecretBox, string, *atomic.Int64) {
	t.Helper()
	f := newGatewayFixture(t, nil)
	ctx := context.Background()
	device, err := f.store.FindDeviceTokenByHash(ctx, crypto.HashToken(f.token))
	if err != nil || device == nil {
		t.Fatal("missing fixture device")
	}
	fee := &media.FixedFee{ID: uuid.NewString(), RequestID: "test-analysis:" + uuid.NewString(), Model: "media:source-analysis", Micro: media.ReferenceAnalysisFeeMicro, CreatedAt: time.Now().UTC()}
	if result, err := f.store.AdmitUsage(ctx, store.ReserveQuotaInput{ID: fee.ID, TeamID: f.teamID, UserID: f.userID, RequestID: fee.RequestID, Micro: fee.Micro}); err != nil || result != store.ReserveReserved {
		t.Fatalf("analysis admission: %s %v", result, err)
	}
	repo := media.Repository{Store: f.store}
	p := &media.Project{ID: uuid.NewString(), Version: 1, TeamID: f.teamID, UserID: f.userID, DeviceID: device.ID, Status: "analyzing", Brief: media.Brief{ReferenceAssetID: "reference-video"}, Analysis: &media.Analysis{InputID: uuid.NewString()}, AnalysisFee: fee}
	if err = repo.Save(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	box, err := crypto.NewSecretBox(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("g", 32))))
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := media.IssueAnalysisScope(ctx, repo, box, p, "claude-sonnet-4-5", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	deps := backgroundTestDeps(f)
	deps.SecretBox = box
	f.engine = New(deps)
	calls := &atomic.Int64{}
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get(media.AnalysisScopeHeader) != "" {
			t.Error("analysis scope leaked upstream")
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":100}}`))
	})
	return f, repo, p, box, ticket, calls
}

func requestAnalysisScope(f *gatewayFixture, method, path, token, ticket, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(media.AnalysisScopeHeader, ticket)
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, req)
	return w
}

func TestAnalysisScopeGatewayZeroSaleRetainsUsageCostAndNormalChatPrice(t *testing.T) {
	f, _, _, _, ticket, calls := gatewayAnalysisFixture(t)
	if _, err := f.store.SaveModelPricing(context.Background(), []store.PricingEntry{snapshotCostEntry("claude-sonnet-4-5", "CNY")}, pricing.EmptyVersion); err != nil {
		t.Fatal(err)
	}
	const body = `{"model":"claude-sonnet-4-5","max_tokens":100}`
	response := requestAnalysisScope(f, "POST", "/v1/chat/completions", f.token, ticket, body)
	if response.Code != 200 {
		t.Fatalf("scoped request: %d %s", response.Code, response.Body.String())
	}
	rows := f.usageRecords()
	if len(rows) != 1 || rows[0].CreditsMicro != 0 || rows[0].InputTokens != 20 || rows[0].OutputTokens != 100 || rows[0].PriceSnapshot == nil || rows[0].PriceSnapshot.Sale.InputPerMtok != "0" || rows[0].PriceSnapshot.Sale.OutputPerMtok != "0" || rows[0].PriceSnapshot.Cost.Status != "estimated" || rows[0].PriceSnapshot.Cost.EstimatedMicro == nil || *rows[0].PriceSnapshot.Cost.EstimatedMicro <= 0 {
		t.Fatalf("analysis must retain tokens/cost while zeroing sale: %+v", rows)
	}
	if team := f.team(); team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("analysis token call changed user balance: %+v", team)
	}
	response = f.do("POST", "/v1/chat/completions", f.token, body)
	if response.Code != 200 || calls.Load() != 2 || f.team().UsedCredits != "0.001560" {
		t.Fatalf("normal chat price changed: %d team=%+v", response.Code, f.team())
	}
}

func TestAnalysisScopeGatewayRejectsInvalidAndOverBudgetBeforeUpstream(t *testing.T) {
	for _, kind := range []string{"forged", "empty", "expired", "ended", "stale-input", "released-fee", "wrong-model", "wrong-device", "wrong-user", "missing-auth", "team-budget", "scope-tokens"} {
		t.Run(kind, func(t *testing.T) {
			f, repo, p, box, ticket, calls := gatewayAnalysisFixture(t)
			ctx := context.Background()
			body, token, expected := `{"model":"claude-sonnet-4-5","max_tokens":100}`, f.token, 403
			var err error
			switch kind {
			case "forged":
				ticket += "x"
			case "empty":
				ticket = ""
			case "expired":
				ticket, err = media.IssueAnalysisScope(ctx, repo, box, p, "claude-sonnet-4-5", time.Now().Add(-2*time.Hour))
			case "ended", "stale-input":
				next := *p
				if kind == "ended" {
					next.Status = "review"
				} else {
					a := *p.Analysis
					a.InputID = uuid.NewString()
					next.Analysis = &a
				}
				err = repo.Save(ctx, &next, p)
			case "released-fee":
				err = f.store.ReleaseReservationByID(ctx, p.AnalysisFee.ID, time.Now())
			case "wrong-model":
				_, err = f.store.UpsertModel(ctx, store.UpsertModelInput{ModelID: "other", DisplayName: "other", InputPricePerMtok: "1", OutputPricePerMtok: "1"})
				body = `{"model":"other"}`
			case "wrong-device", "wrong-user":
				userID := f.userID
				if kind == "wrong-user" {
					user, createErr := f.store.CreateUser(ctx, store.UserInput{Email: "other-analysis@test.local", DisplayName: "other", PasswordHash: "x", Role: store.RoleUser})
					if createErr != nil {
						t.Fatal(createErr)
					}
					userID = user.ID
					if err = f.store.AddMember(ctx, f.teamID, userID, store.MemberRoleMember); err != nil {
						t.Fatal(err)
					}
				}
				token = crypto.GenerateDeviceToken()
				err = f.store.CreateDeviceToken(ctx, store.DeviceTokenRecord{ID: uuid.NewString(), UserID: userID, Name: "other", TokenHash: crypto.HashToken(token), TokenCiphertext: "v1.a.b.c", CreatedAt: time.Now()})
			case "missing-auth":
				token, expected = "", 401
			case "team-budget":
				_, err = f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: f.teamID, DailyBudgetCredits: "0.000000"})
				expected = 429
			case "scope-tokens":
				body, expected = `{"model":"claude-sonnet-4-5","max_tokens":256000,"n":16}`, 429
			}
			if err != nil {
				t.Fatal(err)
			}
			response := requestAnalysisScope(f, "POST", "/v1/chat/completions", token, ticket, body)
			if response.Code != expected || calls.Load() != 0 || len(f.usageRecords()) != 0 {
				t.Fatalf("invalid scope reached upstream/fell back: code=%d body=%s calls=%d", response.Code, response.Body.String(), calls.Load())
			}
		})
	}
}

func TestAnalysisScopeGatewayRejectsBackgroundAndEnforcesRequestBudget(t *testing.T) {
	f, repo, p, _, ticket, calls := gatewayAnalysisFixture(t)
	for _, test := range []struct{ method, path, body string }{
		{"POST", "/v1/responses", `{"model":"claude-sonnet-4-5","background":true}`},
		{"GET", "/v1/responses/resp_test", ""},
		{"POST", "/v1/responses/resp_test/cancel", ""},
		{"DELETE", "/v1/responses/resp_test", ""},
	} {
		w := requestAnalysisScope(f, test.method, test.path, f.token, ticket, test.body)
		if w.Code != 403 || calls.Load() != 0 {
			t.Fatalf("scope allowed background operation: %d %s", w.Code, w.Body.String())
		}
	}
	if err := repo.Store.SetConfigValue(context.Background(), "media:analysis-scope-budget:"+p.TeamID+":"+p.AnalysisFee.ID, []byte(`{"requests":128,"tokens":100}`)); err != nil {
		t.Fatal(err)
	}
	w := requestAnalysisScope(f, "POST", "/v1/chat/completions", f.token, ticket, `{"model":"claude-sonnet-4-5"}`)
	if w.Code != 429 || calls.Load() != 0 {
		t.Fatalf("request budget bypassed: %d %s", w.Code, w.Body.String())
	}
}
