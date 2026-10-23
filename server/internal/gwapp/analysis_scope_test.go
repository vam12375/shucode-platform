package gwapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	if _, err := f.store.UpsertModel(ctx, store.UpsertModelInput{ModelID: "claude-sonnet-4-5", DisplayName: "fixture-alias", ProvideUpstreamModel: true, UpstreamModel: "deepseek-fixture", InputPricePerMtok: "3", OutputPricePerMtok: "15"}); err != nil {
		t.Fatal(err)
	}
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

func multimodalAnalysisBody(imageCount, imageBytes, maxTokens int) string {
	content := make([]any, 0, imageCount+1)
	content = append(content, map[string]any{"type": "text", "text": "请分析这些抽帧的真实画面、镜头和声音证据。"})
	payload := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", imageBytes)))
	for range imageCount {
		content = append(content, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": "data:image/jpeg;base64," + payload},
		})
	}
	body, _ := json.Marshal(map[string]any{
		"model":            "claude-sonnet-4-5",
		"max_tokens":       maxTokens,
		"reasoning_effort": "high",
		"messages":         []any{map[string]any{"role": "user", "content": content}},
	})
	return string(body)
}

func remoteMultimodalAnalysisBody(imageCount, maxTokens int) string {
	content := make([]any, 0, imageCount+1)
	content = append(content, map[string]any{"type": "text", "text": "请分析这些远程抽帧。"})
	for i := 0; i < imageCount; i++ {
		content = append(content, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": "https://cdn.example.test/frame-" + uuid.NewString() + ".jpg"},
		})
	}
	body, _ := json.Marshal(map[string]any{
		"model":            "claude-sonnet-4-5",
		"max_tokens":       maxTokens,
		"reasoning_effort": "high",
		"messages":         []any{map[string]any{"role": "user", "content": content}},
	})
	return string(body)
}

func TestAnalysisScopeAllowsFullFrameBatchesAcrossRoundsWithoutBudgetState(t *testing.T) {
	f, repo, p, _, ticket, calls := gatewayAnalysisFixture(t)
	// 复刻 scope 仅校验授权；全量抽帧不能因旧版 1M 总预算被拒绝。
	body := multimodalAnalysisBody(40, 75*1024, 256_000)
	response := requestAnalysisScope(f, "POST", "/v1/chat/completions", f.token, ticket, body)
	if response.Code != 200 || calls.Load() != 1 {
		t.Fatalf("full frame batch should pass scope authorization: %d calls=%d %s", response.Code, calls.Load(), response.Body.String())
	}
	response = requestAnalysisScope(f, "POST", "/v1/chat/completions", f.token, ticket, `{"model":"claude-sonnet-4-5","max_tokens":256000,"reasoning_effort":"high","messages":[{"role":"user","content":"请汇总原片分析结果。"}]}`)
	if response.Code != 200 || calls.Load() != 2 {
		t.Fatalf("follow-up summary should pass scope authorization: %d calls=%d %s", response.Code, calls.Load(), response.Body.String())
	}
	raw, err := repo.Store.GetConfigValue(context.Background(), "media:analysis-scope-budget:"+p.TeamID+":"+p.AnalysisFee.ID)
	if err != nil || len(raw) != 0 {
		t.Fatalf("scope authorization wrote budget state: %s %v", raw, err)
	}
	response = requestAnalysisScope(f, "POST", "/v1/chat/completions", f.token, ticket, body)
	if response.Code != 200 || calls.Load() != 3 {
		t.Fatalf("repeating the full image batch must remain authorized: %d calls=%d %s", response.Code, calls.Load(), response.Body.String())
	}
}

func TestAnalysisScopeAllowsLargeInputsWithoutDedicatedTokenLimit(t *testing.T) {
	f, _, _, _, ticket, calls := gatewayAnalysisFixture(t)
	// 图片加输出预估超过旧 1M 限制也只能接受普通团队限流校验。
	body := remoteMultimodalAnalysisBody(64, 256_000)
	response := requestAnalysisScope(f, "POST", "/v1/chat/completions", f.token, ticket, body)
	if response.Code != 200 || calls.Load() != 1 {
		t.Fatalf("large input was incorrectly blocked by scope: %d calls=%d %s", response.Code, calls.Load(), response.Body.String())
	}
	// 大文本和多候选输出同样沿用普通对话规则，不再经过scope总量门槛。
	body = `{"model":"claude-sonnet-4-5","max_tokens":256000,"n":16,"reasoning_effort":"high","messages":[{"role":"user","content":"` + strings.Repeat("x", 4_000_000) + `"}]}`
	response = requestAnalysisScope(f, "POST", "/v1/chat/completions", f.token, ticket, body)
	if response.Code != 200 || calls.Load() != 2 {
		t.Fatalf("large token request was incorrectly blocked by scope: %d calls=%d %s", response.Code, calls.Load(), response.Body.String())
	}
}

func TestAnalysisScopeUsesSameOutputLimitAsNormalChat(t *testing.T) {
	for _, test := range []struct {
		name, path, body, field string
	}{
		{"chat", "/v1/chat/completions", `{"model":"claude-sonnet-4-5","max_completion_tokens":256000,"reasoning_effort":"high","messages":[]}`, "max_completion_tokens"},
		{"responses", "/v1/responses", `{"model":"claude-sonnet-4-5","max_output_tokens":256000,"reasoning":{"effort":"high"},"input":[]}`, "max_output_tokens"},
		{"anthropic", "/v1/messages", `{"model":"claude-sonnet-4-5","max_tokens":256000,"output_config":{"effort":"high"},"messages":[]}`, "max_tokens"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, _, _, _, ticket, calls := gatewayAnalysisFixture(t)
			f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("content-type", "application/json")
				_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1,"input_tokens":1,"output_tokens":1}}`))
			})
			response := requestAnalysisScope(f, "POST", test.path, f.token, ticket, test.body)
			_, raw := f.upstreamRequest()
			var actual map[string]any
			if err := json.Unmarshal(raw, &actual); err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || calls.Load() != 1 || actual[test.field] != float64(maxMaxTokens) {
				t.Fatalf("scoped output differs from normal chat limit: %d actual=%v %s", response.Code, actual[test.field], response.Body.String())
			}
			response = f.do("POST", test.path, f.token, test.body)
			_, raw = f.upstreamRequest()
			if err := json.Unmarshal(raw, &actual); err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || calls.Load() != 2 || actual[test.field] != float64(maxMaxTokens) {
				t.Fatalf("normal chat output limit changed: %d actual=%v %s", response.Code, actual[test.field], response.Body.String())
			}
		})
	}
}

func TestAnalysisScopeDoesNotCreateBudgetStateAfterUpstreamFailure(t *testing.T) {
	f, repo, p, _, ticket, calls := gatewayAnalysisFixture(t)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"fixture unavailable"}}`))
	})
	response := requestAnalysisScope(f, "POST", "/v1/chat/completions", f.token, ticket, `{"model":"claude-sonnet-4-5","max_tokens":100,"reasoning_effort":"high"}`)
	if response.Code != 503 || calls.Load() != 1 {
		t.Fatalf("unexpected fixture failure response: %d calls=%d %s", response.Code, calls.Load(), response.Body.String())
	}
	raw, err := repo.Store.GetConfigValue(context.Background(), "media:analysis-scope-budget:"+p.TeamID+":"+p.AnalysisFee.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Fatalf("upstream failure wrote scope budget state: %s", raw)
	}
}

func TestAnalysisScopeGatewayZeroSaleRetainsUsageCostAndNormalChatPrice(t *testing.T) {
	f, _, _, _, ticket, calls := gatewayAnalysisFixture(t)
	if _, err := f.store.SaveModelPricing(context.Background(), []store.PricingEntry{snapshotCostEntry("claude-sonnet-4-5", "CNY")}, pricing.EmptyVersion); err != nil {
		t.Fatal(err)
	}
	const body = `{"model":"claude-sonnet-4-5","max_tokens":100,"reasoning_effort":"high"}`
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

func TestAnalysisScopeStreamingZeroSaleRetainsFrozenCost(t *testing.T) {
	f, _, _, _, ticket, calls := gatewayAnalysisFixture(t)
	entry := snapshotCostEntry("deepseek-fixture", "CNY")
	doc, err := f.store.SaveModelPricing(context.Background(), []store.PricingEntry{entry}, pricing.EmptyVersion)
	if err != nil {
		t.Fatal(err)
	}
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get(media.AnalysisScopeHeader) != "" {
			t.Error("analysis scope leaked upstream")
		}
		// SSE 在完成后才写用量，仍须沿用请求发出前冻结的采购价，而不是最新目录价。
		entry.OutputCostPerMillion = "999"
		if _, err := f.store.SaveModelPricing(context.Background(), []store.PricingEntry{entry}, doc.Version); err != nil {
			t.Error(err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":100,\"prompt_tokens_details\":{\"cached_tokens\":5}}}\n\ndata: [DONE]\n\n"))
	})
	response := requestAnalysisScope(f, "POST", "/v1/chat/completions", f.token, ticket,
		`{"model":"claude-sonnet-4-5","max_tokens":100,"stream":true,"reasoning_effort":"high"}`)
	if response.Code != 200 || calls.Load() != 1 {
		t.Fatalf("scoped stream: %d calls=%d %s", response.Code, calls.Load(), response.Body.String())
	}
	rows := f.usageRecords()
	if len(rows) != 1 {
		t.Fatalf("stream should settle exactly one usage: %+v", rows)
	}
	row := rows[0]
	if row.CreditsMicro != 0 || row.InputTokens != 15 || row.OutputTokens != 100 || row.CacheReadTokens != 5 ||
		row.PriceSnapshot == nil || row.PriceSnapshot.Sale.OutputPerMtok != "0" || row.PriceSnapshot.Cost.Status != "estimated" ||
		row.PriceSnapshot.Cost.ModelID != "deepseek-fixture" || row.PriceSnapshot.Cost.PricingVersion != doc.Version ||
		row.PriceSnapshot.Cost.Rates == nil || row.PriceSnapshot.Cost.Rates.OutputPerMtok != "2" ||
		row.PriceSnapshot.Cost.EstimatedMicro == nil || *row.PriceSnapshot.Cost.EstimatedMicro != 216 {
		t.Fatalf("scoped stream must preserve frozen actual-token cost independently from zero sale: %+v", row)
	}
	if team := f.team(); team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("scoped stream changed user balance: %+v", team)
	}
}

func TestAnalysisScopeGatewayRejectsInvalidAuthorizationAndTeamBudgetBeforeUpstream(t *testing.T) {
	for _, kind := range []string{"forged", "empty", "expired", "ended", "stale-input", "released-fee", "wrong-model", "wrong-device", "wrong-user", "missing-auth", "team-budget"} {
		t.Run(kind, func(t *testing.T) {
			f, repo, p, box, ticket, calls := gatewayAnalysisFixture(t)
			ctx := context.Background()
			body, token, expected := `{"model":"claude-sonnet-4-5","max_tokens":100,"reasoning_effort":"high"}`, f.token, 403
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

func TestSourceAnalysisScopeRequiresActualHighBeforeUpstream(t *testing.T) {
	for _, test := range []struct {
		name, route, fragment string
		allowed               bool
	}{
		{"missing", "/v1/chat/completions", `"messages":[]`, false},
		{"max", "/v1/chat/completions", `"reasoning_effort":"max"`, false},
		{"budget-only", "/v1/chat/completions", `"thinking":{"type":"enabled","budget_tokens":32768}`, false},
		{"disabled", "/v1/chat/completions", `"reasoning_effort":"high","thinking":{"type":"disabled"}`, false},
		{"high", "/v1/chat/completions", `"reasoning_effort":"high","thinking":{"type":"enabled"}`, true},
		{"responses-high", "/v1/responses", `"reasoning":{"effort":"high"},"thinking":{"type":"disabled"}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, _, _, _, ticket, calls := gatewayAnalysisFixture(t)
			body := `{"model":"claude-sonnet-4-5","max_tokens":100,` + test.fragment + `}`
			response := requestAnalysisScope(f, "POST", test.route, f.token, ticket, body)
			if test.allowed {
				if response.Code != 200 || calls.Load() != 1 || f.usageRecords()[0].ReasoningEffort != "high" {
					t.Fatalf("explicit high should be retained: %d %s", response.Code, response.Body.String())
				}
			} else if response.Code != 403 || calls.Load() != 0 || len(f.usageRecords()) != 0 {
				t.Fatalf("source high requirement was bypassed: %d calls=%d %s", response.Code, calls.Load(), response.Body.String())
			}
		})
	}
}

func TestAnalysisScopeGatewayRejectsBackgroundAndIgnoresLegacyBudget(t *testing.T) {
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
	if err := repo.Store.SetConfigValue(context.Background(), "media:analysis-scope-budget:"+p.TeamID+":"+p.AnalysisFee.ID, []byte(`{"requests":128,"tokens":1000000}`)); err != nil {
		t.Fatal(err)
	}
	w := requestAnalysisScope(f, "POST", "/v1/chat/completions", f.token, ticket, `{"model":"claude-sonnet-4-5","reasoning_effort":"high"}`)
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("legacy budget blocked valid authorization: %d %s", w.Code, w.Body.String())
	}
}

func TestAnalysisScopeRejectsUpstreamModelChangedAfterIssue(t *testing.T) {
	f, _, _, _, ticket, calls := gatewayAnalysisFixture(t)
	if _, err := f.store.UpsertModel(context.Background(), store.UpsertModelInput{ModelID: "claude-sonnet-4-5", DisplayName: "fixture-alias", ProvideUpstreamModel: true, UpstreamModel: "other-fixture", InputPricePerMtok: "3", OutputPricePerMtok: "15"}); err != nil {
		t.Fatal(err)
	}
	response := requestAnalysisScope(f, "POST", "/v1/chat/completions", f.token, ticket, `{"model":"claude-sonnet-4-5","max_tokens":100,"reasoning_effort":"high"}`)
	if response.Code != 403 || calls.Load() != 0 || len(f.usageRecords()) != 0 {
		t.Fatalf("scope changed upstream model: %d %s calls=%d", response.Code, response.Body.String(), calls.Load())
	}
}
