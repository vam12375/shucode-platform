package gwapp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func backgroundTestDeps(f *gatewayFixture) Deps {
	return Deps{
		Store: f.store, Env: f.env, Now: time.Now, HTTPClient: f.upstream.Client(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestBackgroundGenerationDoesNotHoldCreditsAndSettlesAfterRestart(t *testing.T) {
	f := newGatewayFixture(t, nil)
	var polls atomic.Int64
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if f.team().ReservedCredits != "0.000000" {
				t.Error("后台生成不能冻结余额")
			}
			fmt.Fprint(w, `{"id":"resp_background","status":"queued","usage":null}`)
			return
		}
		if r.Header.Get("authorization") != "Bearer upstream-key" {
			t.Error("后台轮询必须使用供应商凭据")
		}
		polls.Add(1)
		fmt.Fprint(w, `{"id":"resp_background","status":"completed","output":[],"usage":{"input_tokens":100,"output_tokens":20}}`)
	})
	rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true,"input":"hello"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"queued"`) {
		t.Fatalf("后台任务应正常创建: %d %s", rec.Code, rec.Body.String())
	}
	team := f.team()
	if team.ReservedCredits != "0.000000" || team.UsedCredits != "0.000000" || len(f.usageRecords()) != 0 {
		t.Fatalf("未完成任务不占余额且不实扣: %+v", team)
	}
	job, err := f.store.FindBackgroundGeneration(context.Background(), f.teamID, "resp_background")
	if err != nil || job == nil || job.Record.CreditsMicro != 0 {
		t.Fatalf("后台任务必须持久化: %+v %v", job, err)
	}
	if expired, err := f.store.ExpireStaleReservations(context.Background(), time.Now().Add(24*time.Hour)); err != nil || expired != 0 {
		t.Fatalf("后台预留不能被 TTL 回收: %d %v", expired, err)
	}
	// 使用新建 deps 模拟进程恢复：轮询只读取存储中的任务和锁定价格，不依赖请求闭包。
	deps := backgroundTestDeps(f)
	reconcileBackgroundGenerations(context.Background(), deps)
	reconcileBackgroundGenerations(context.Background(), deps)
	team = f.team()
	if team.ReservedCredits != "0.000000" || team.UsedCredits != "0.000600" || len(f.usageRecords()) != 1 || polls.Load() != 1 {
		t.Fatalf("终态扣实且幂等: team=%+v usage=%+v polls=%d", team, f.usageRecords(), polls.Load())
	}
	rec = f.do("GET", "/v1/responses/resp_background", f.token, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"completed"`) {
		t.Fatalf("完成后仍能读取留存结果: %d %s", rec.Code, rec.Body.String())
	}
	if polls.Load() != 1 {
		t.Fatal("已完成结果不应重复查询或计费")
	}
}

func TestBackgroundGenerationRetriesWithoutReleasingAndChecksOwnership(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/responses" {
			fmt.Fprint(w, `{"id":"resp_retry","status":"in_progress"}`)
			return
		}
		if r.URL.Path == "/v1/responses/resp_retry/cancel" {
			fmt.Fprint(w, `{"id":"resp_retry","status":"cancelled"}`)
			return
		}
		w.WriteHeader(503)
	})
	rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true}`)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	reconcileBackgroundGenerations(context.Background(), backgroundTestDeps(f))
	job, err := f.store.FindBackgroundGeneration(context.Background(), f.teamID, "resp_retry")
	if err != nil || job == nil || job.Attempts != 1 || job.LastError == "" || job.CompletedAt != nil {
		t.Fatalf("失败应保留可恢复任务: %+v %v", job, err)
	}
	if f.team().ReservedCredits != "0.000000" || len(f.usageRecords()) != 0 {
		t.Fatal("查询失败不占余额且不提前关闭结算身份")
	}
	ctx := context.Background()
	other, err := f.store.CreateUser(ctx, store.UserInput{Email: "other@test.local", DisplayName: "other", PasswordHash: "x", Role: store.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddMember(ctx, f.teamID, other.ID, store.MemberRoleMember); err != nil {
		t.Fatal(err)
	}
	otherToken := crypto.GenerateDeviceToken()
	if err := f.store.CreateDeviceToken(ctx, store.DeviceTokenRecord{ID: uuid.NewString(), UserID: other.ID, Name: "other", TokenHash: crypto.HashToken(otherToken), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/responses/resp_retry", "/v1/responses/resp_retry/cancel"} {
		method := http.MethodGet
		if strings.HasSuffix(path, "/cancel") {
			method = http.MethodPost
		}
		if denied := f.do(method, path, otherToken, ""); denied.Code != 404 {
			t.Fatalf("团队其他成员不能访问任务: %d %s", denied.Code, denied.Body.String())
		}
	}
	rec = f.do("POST", "/v1/responses/resp_retry/cancel", f.token, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"cancelled"`) {
		t.Fatalf("创建者应能取消: %d %s", rec.Code, rec.Body.String())
	}
}

func TestStreamedBackgroundGenerationPersistsResponseID(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"id":"resp_streamed","status":"completed","usage":{"input_tokens":10,"output_tokens":2}}`)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_streamed\",\"status\":\"in_progress\"}}\n\n")
	})
	rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true,"stream":true}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "resp_streamed") {
		t.Fatalf("stream create: %d %s", rec.Code, rec.Body.String())
	}
	job, err := f.store.FindBackgroundGeneration(context.Background(), f.teamID, "resp_streamed")
	if err != nil || job == nil {
		t.Fatalf("必须从创建事件持久化 ID: %+v %v", job, err)
	}
	if f.team().ReservedCredits != "0.000000" {
		t.Fatal("流结束等待后台终态，仍不占余额")
	}
	reconcileBackgroundGenerations(context.Background(), backgroundTestDeps(f))
	if f.team().ReservedCredits != "0.000000" || f.team().UsedCredits != "0.000060" {
		t.Fatalf("终态结算失败: %+v", f.team())
	}
}

func TestBackgroundGenerationContinuesUntilTerminalAndChargesFullUsage(t *testing.T) {
	f := newGatewayFixture(t, nil)
	if _, err := f.store.UpdateTeam(context.Background(), f.teamID, store.UpdateTeamPatch{QuotaCredits: ptr("0.001000")}); err != nil {
		t.Fatal(err)
	}
	var cancellations atomic.Int64
	var ready atomic.Bool
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/responses":
			fmt.Fprint(w, `{"id":"resp_cost_limit","status":"queued"}`)
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			cancellations.Add(1)
			fmt.Fprint(w, `{"id":"resp_cost_limit","status":"cancelled"}`)
		case !ready.Load():
			fmt.Fprint(w, `{"id":"resp_cost_limit","status":"in_progress","usage":{"input_tokens":500,"output_tokens":1}}`)
		default:
			fmt.Fprint(w, `{"id":"resp_cost_limit","status":"cancelled","usage":{"input_tokens":50000,"output_tokens":1}}`)
		}
	})
	rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true}`)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	deps := backgroundTestDeps(f)
	reconcileBackgroundGenerations(context.Background(), deps)
	if cancellations.Load() != 0 || f.team().ReservedCredits != "0.000000" || f.team().UsedCredits != "0.000000" {
		t.Fatalf("中间 usage 不应触发估算费用取消或扣款: cancel=%d team=%+v", cancellations.Load(), f.team())
	}
	job, err := f.store.FindBackgroundGeneration(context.Background(), f.teamID, "resp_cost_limit")
	if err != nil || job == nil {
		t.Fatalf("job: %+v %v", job, err)
	}
	if err := f.store.RetryBackgroundGeneration(context.Background(), job.ReservationID, time.Now().Add(-time.Second), ""); err != nil {
		t.Fatal(err)
	}
	ready.Store(true)
	reconcileBackgroundGenerations(context.Background(), deps)
	team := f.team()
	if team.ReservedCredits != "0.000000" || team.UsedCredits != "0.150015" {
		t.Fatalf("完整费用不能被尾差封顶截断: %+v", team)
	}
	records := f.usageRecords()
	if len(records) != 1 || records[0].CreditsMicro != 150015 || strings.Contains(records[0].ErrorType, "credit_charge_capped") {
		t.Fatalf("应完整入账实际用量: %+v", records)
	}
}

func TestBackgroundTerminalMissingUsageDoesNotEstimateCharge(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"id":"resp_no_usage","status":"queued"}`)
			return
		}
		fmt.Fprint(w, `{"id":"resp_no_usage","status":"completed"}`)
	})
	if rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true}`); rec.Code != 200 {
		t.Fatalf("create %d", rec.Code)
	}
	reconcileBackgroundGenerations(context.Background(), backgroundTestDeps(f))
	team := f.team()
	if team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("缺少最终用量不能按最大输出虚扣: %+v", team)
	}
	if records := f.usageRecords(); len(records) != 1 || records[0].PriceSnapshot.Cost.UsageEstimated || records[0].ErrorType != "background_usage_missing" {
		t.Fatalf("应标记计量不完整: %+v", records)
	}
}

func TestBackgroundRetainsObservedUsageBeforeMissingOrInvalidTerminalUsage(t *testing.T) {
	for _, terminalUsage := range []string{"", `,"usage":{"input_tokens":-1,"output_tokens":20}`} {
		t.Run(fmt.Sprintf("invalid=%t", terminalUsage != ""), func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			var polls atomic.Int64
			f.setHandler(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					fmt.Fprint(w, `{"id":"resp_observed","status":"queued"}`)
					return
				}
				if polls.Add(1) == 1 {
					fmt.Fprint(w, `{"id":"resp_observed","status":"in_progress","usage":{"input_tokens":100,"output_tokens":20}}`)
					return
				}
				fmt.Fprintf(w, `{"id":"resp_observed","status":"completed"%s}`, terminalUsage)
			})
			if rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true}`); rec.Code != 200 {
				t.Fatalf("create: %d", rec.Code)
			}
			deps := backgroundTestDeps(f)
			reconcileBackgroundGenerations(context.Background(), deps)
			job, err := f.store.FindBackgroundGeneration(context.Background(), f.teamID, "resp_observed")
			if err != nil || job == nil || job.UsageObservedAt == nil || job.Record.InputTokens != 100 || job.Record.OutputTokens != 20 || job.Record.CreditsMicro != 0 || f.team().UsedCredits != "0.000000" {
				t.Fatalf("合法观察必须持久化但不扣款: %+v %v", job, err)
			}
			if err := f.store.RetryBackgroundGeneration(context.Background(), job.ReservationID, time.Now().Add(-time.Second), ""); err != nil {
				t.Fatal(err)
			}
			// 新 deps 从存储恢复，终态没有合法 usage 时仍保留此前真实观察值。
			reconcileBackgroundGenerations(context.Background(), backgroundTestDeps(f))
			reconcileBackgroundGenerations(context.Background(), backgroundTestDeps(f))
			records := f.usageRecords()
			if f.team().UsedCredits != "0.000600" || len(records) != 1 || records[0].InputTokens != 100 || records[0].OutputTokens != 20 || records[0].PriceSnapshot.Cost.UsageEstimated || records[0].ErrorType != "background_usage_missing" {
				t.Fatalf("终态仅按持久化合法观察实扣一次: team=%+v records=%+v", f.team(), records)
			}
		})
	}
}

func TestBackgroundCreationObservationsSurviveMissingTerminalUsage(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streamed), func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			f.setHandler(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `{"id":"resp_created_usage","status":"completed"}`)
					return
				}
				if streamed {
					w.Header().Set("content-type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_created_usage\",\"status\":\"in_progress\"}}\n\n")
					w.(http.Flusher).Flush()
					fmt.Fprint(w, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_created_usage\",\"usage\":{\"input_tokens\":100,\"output_tokens\":20}}}\n\n")
					return
				}
				fmt.Fprint(w, `{"id":"resp_created_usage","status":"in_progress","usage":{"input_tokens":100,"output_tokens":20}}`)
			})
			body := fmt.Sprintf(`{"model":"claude-sonnet-4-5","background":true,"stream":%t}`, streamed)
			if rec := f.do("POST", "/v1/responses", f.token, body); rec.Code != 200 {
				t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
			}
			job, err := f.store.FindBackgroundGeneration(context.Background(), f.teamID, "resp_created_usage")
			if err != nil || job == nil || job.UsageObservedAt == nil || job.Record.OutputTokens != 20 || f.team().UsedCredits != "0.000000" {
				t.Fatalf("创建响应中的观察必须保存且不扣款: %+v %v", job, err)
			}
			reconcileBackgroundGenerations(context.Background(), backgroundTestDeps(f))
			if f.team().UsedCredits != "0.000600" || len(f.usageRecords()) != 1 {
				t.Fatalf("恢复终态应按创建观察实扣一次: %+v", f.usageRecords())
			}
		})
	}
}

func TestBackgroundFinishRecoversLatestObservationAfterSettlementFailure(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"resp_settle_retry","status":"queued"}`)
	})
	if rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true}`); rec.Code != 200 {
		t.Fatalf("create: %d", rec.Code)
	}
	stale, err := f.store.FindBackgroundGeneration(context.Background(), f.teamID, "resp_settle_retry")
	if err != nil || stale == nil {
		t.Fatalf("job: %+v %v", stale, err)
	}
	deps := backgroundTestDeps(f)
	deps.Store = &failingSettleStore{PlatformStore: f.store}
	terminal := []byte(`{"id":"resp_settle_retry","status":"completed","usage":{"input_tokens":100,"output_tokens":20}}`)
	if finishBackgroundGeneration(context.Background(), deps, *stale, terminal, "completed", "application/json") {
		t.Fatal("失败结算必须继续核对，不能假报已完成")
	}
	observed, _ := f.store.FindBackgroundGeneration(context.Background(), f.teamID, stale.ResponseID)
	if observed.UsageObservedAt == nil || observed.Record.InputTokens != 100 || observed.Record.OutputTokens != 20 || observed.CompletedAt != nil || len(f.usageRecords()) != 0 {
		t.Fatalf("最终合法 usage 必须先保存，失败恢复不能丢失: %+v", observed)
	}
	// 恢复处理仍持有过期快照，后续上游终态不再返回 usage，也要重读权威观察值。
	missing := []byte(`{"id":"resp_settle_retry","status":"completed"}`)
	if !finishBackgroundGeneration(context.Background(), backgroundTestDeps(f), *stale, missing, "completed", "application/json") {
		t.Fatal("恢复终态失败")
	}
	if !finishBackgroundGeneration(context.Background(), backgroundTestDeps(f), *stale, missing, "completed", "application/json") {
		t.Fatal("终态重放失败")
	}
	if f.team().UsedCredits != "0.000600" || len(f.usageRecords()) != 1 {
		t.Fatalf("恢复必须按合法已观察用量仅扣一次: %+v", f.usageRecords())
	}
}

func TestBackgroundQueryAndCancelShareObservedUsage(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cancel"):
			fmt.Fprint(w, `{"id":"resp_query_usage","status":"cancelled"}`)
		case r.Method == http.MethodPost:
			fmt.Fprint(w, `{"id":"resp_query_usage","status":"queued"}`)
		default:
			fmt.Fprint(w, `{"id":"resp_query_usage","status":"in_progress","usage":{"input_tokens":100,"output_tokens":20}}`)
		}
	})
	if rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true}`); rec.Code != 200 {
		t.Fatalf("create: %d", rec.Code)
	}
	if rec := f.do("GET", "/v1/responses/resp_query_usage", f.token, ""); rec.Code != 200 || f.team().UsedCredits != "0.000000" {
		t.Fatalf("查询保存观察但不能提前扣费: %d %+v", rec.Code, f.team())
	}
	if rec := f.do("POST", "/v1/responses/resp_query_usage/cancel", f.token, ""); rec.Code != 200 {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	if f.team().UsedCredits != "0.000600" || len(f.usageRecords()) != 1 {
		t.Fatalf("取消终态应按此前查询观察实扣: %+v", f.usageRecords())
	}
}

func TestLegacyBackgroundEstimateIsNotActualUsage(t *testing.T) {
	f := newGatewayFixture(t, nil)
	ctx := context.Background()
	id := uuid.NewString()
	if decision, err := f.store.AdmitUsage(ctx, store.ReserveQuotaInput{ID: id, TeamID: f.teamID, UserID: f.userID, RequestID: id}); err != nil || decision != store.ReserveReserved {
		t.Fatalf("admit: %s %v", decision, err)
	}
	at := time.Now()
	job := store.BackgroundGenerationJob{ReservationID: id, ResponseID: "resp_legacy_estimate", BaseURL: f.upstream.URL, TeamID: f.teamID, UserID: f.userID,
		Record: store.UsageRecord{ID: uuid.NewString(), TeamID: f.teamID, UserID: f.userID, RequestID: id, Model: "m", UpstreamModel: "m", CreditsMicro: 500_000, CreatedAt: at}, CreatedAt: at, NextPollAt: at}
	if err := f.store.CreateBackgroundGeneration(ctx, job); err != nil {
		t.Fatal(err)
	}
	if !finishBackgroundGeneration(ctx, backgroundTestDeps(f), job, []byte(`{"id":"resp_legacy_estimate","status":"completed"}`), "completed", "application/json") {
		t.Fatal("终态处理失败")
	}
	if f.team().UsedCredits != "0.000000" || len(f.usageRecords()) != 1 || f.usageRecords()[0].CreditsMicro != 0 {
		t.Fatalf("旧模板估算不得作为实际使用量: %+v", f.usageRecords())
	}
}

func TestBackgroundImmediateCompletionKeepsResultAndDeletionSettlesFirst(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		t.Run(fmt.Sprintf("immediate=%t", immediate), func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			var terminal atomic.Bool
			terminal.Store(immediate)
			var deletions atomic.Int64
			f.setHandler(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					if f.team().ReservedCredits != "0.000000" || len(f.usageRecords()) != 1 {
						t.Error("必须先结算最终用量再删除上游结果")
					}
					deletions.Add(1)
					fmt.Fprint(w, `{"id":"resp_delete","deleted":true}`)
					return
				}
				if terminal.Load() {
					fmt.Fprint(w, `{"id":"resp_delete","status":"completed","usage":{"input_tokens":100,"output_tokens":20}}`)
					return
				}
				fmt.Fprint(w, `{"id":"resp_delete","status":"in_progress"}`)
			})
			if rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true}`); rec.Code != 200 {
				t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
			}
			if !immediate {
				rec := f.do("DELETE", "/v1/responses/resp_delete", f.token, "")
				if rec.Code != 409 || deletions.Load() != 0 || f.team().ReservedCredits != "0.000000" {
					t.Fatalf("取消尚未完成不能删除或释放: %d %s", rec.Code, rec.Body.String())
				}
				terminal.Store(true)
			} else if rec := f.do("GET", "/v1/responses/resp_delete", f.token, ""); rec.Code != 200 {
				t.Fatalf("创建即完成仍能读取: %d %s", rec.Code, rec.Body.String())
			}
			rec := f.do("DELETE", "/v1/responses/resp_delete", f.token, "")
			if rec.Code != 200 || deletions.Load() != 1 || !strings.Contains(rec.Body.String(), `"deleted":true`) {
				t.Fatalf("终态后删除失败: %d %s deletes=%d", rec.Code, rec.Body.String(), deletions.Load())
			}
			if f.team().UsedCredits != "0.000600" || len(f.usageRecords()) != 1 {
				t.Fatalf("删除不能重复扣费或漏记: %+v", f.usageRecords())
			}
			if rec := f.do("GET", "/v1/responses/resp_delete", f.token, ""); rec.Code != 404 {
				t.Fatalf("删除后不能继续读取: %d", rec.Code)
			}
		})
	}
}

func TestUnlimitedBackgroundGenerationDoesNotCancelAtEstimate(t *testing.T) {
	f := newGatewayFixture(t, nil)
	if _, err := f.store.UpdateTeam(context.Background(), f.teamID, store.UpdateTeamPatch{Unlimited: ptr(true), QuotaCredits: ptr("0.000000")}); err != nil {
		t.Fatal(err)
	}
	var cancellations, polls atomic.Int64
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			cancellations.Add(1)
		}
		if r.URL.Path == "/v1/responses" {
			fmt.Fprint(w, `{"id":"resp_unlimited","status":"queued"}`)
			return
		}
		status := "in_progress"
		if polls.Add(1) > 1 {
			status = "completed"
		}
		fmt.Fprintf(w, `{"id":"resp_unlimited","status":%q,"usage":{"input_tokens":100000,"output_tokens":2}}`, status)
	})
	if rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true}`); rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	deps := backgroundTestDeps(f)
	reconcileBackgroundGenerations(context.Background(), deps)
	job, err := f.store.FindBackgroundGeneration(context.Background(), f.teamID, "resp_unlimited")
	if err != nil || job == nil {
		t.Fatalf("job: %+v %v", job, err)
	}
	if cancellations.Load() != 0 {
		t.Fatal("无限额度团队不能在估算预留额处取消")
	}
	if err := f.store.RetryBackgroundGeneration(context.Background(), job.ReservationID, time.Now().Add(-time.Second), ""); err != nil {
		t.Fatal(err)
	}
	reconcileBackgroundGenerations(context.Background(), deps)
	if f.team().ReservedCredits != "0.000000" || f.team().UsedCredits != "0.300030" || len(f.usageRecords()) != 1 {
		t.Fatalf("无限额度仍按实际用量计费: %+v", f.team())
	}
}

func TestBackgroundPollingRejectsRedirects(t *testing.T) {
	f := newGatewayFixture(t, nil)
	var redirected atomic.Int64
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			fmt.Fprint(w, `{"id":"resp_redirect","status":"queued"}`)
		case "/credential-target":
			redirected.Add(1)
			fmt.Fprint(w, `{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}`)
		default:
			w.Header().Set("location", f.upstream.URL+"/credential-target")
			w.WriteHeader(http.StatusTemporaryRedirect)
		}
	})
	if rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true}`); rec.Code != 200 {
		t.Fatalf("create: %d", rec.Code)
	}
	reconcileBackgroundGenerations(context.Background(), backgroundTestDeps(f))
	if redirected.Load() != 0 || f.team().ReservedCredits != "0.000000" || len(f.usageRecords()) != 0 {
		t.Fatal("轮询重定向不能携带供应商凭据离开原 endpoint，也不能解冻")
	}
}
