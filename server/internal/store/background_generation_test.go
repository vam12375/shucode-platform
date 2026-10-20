package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testFullPrechargeAndBackgroundJobs(t *testing.T, ctx context.Context, s PlatformStore) {
	u := mustUser(t, ctx, s, "background@store.test")
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "background-precharge", QuotaCredits: "1.000000"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, team.ID, u.ID, MemberRoleOwner); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int64
	ids := make(chan string, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := uuid.NewString()
			result, err := s.ReserveQuota(ctx, ReserveQuotaInput{ID: id, TeamID: team.ID, UserID: u.ID, RequestID: id, Micro: 1, ReserveAllAvailable: true, RetainUntilSettled: true})
			if err != nil {
				t.Errorf("reserve: %v", err)
				return
			}
			if result == ReserveReserved {
				accepted.Add(1)
				ids <- id
			} else if result != ReserveDenied {
				t.Errorf("unexpected result %s", result)
			}
		}()
	}
	wg.Wait()
	close(ids)
	if accepted.Load() != 1 {
		t.Fatalf("全额冻结必须只有一个并发请求成功: %d", accepted.Load())
	}
	id := <-ids
	r, err := s.GetQuotaReservation(ctx, id)
	if err != nil || r == nil || r.ReservedMicro != 1_000_000 {
		t.Fatalf("锁内冻结全部余额: %+v %v", r, err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	job := BackgroundGenerationJob{ReservationID: id, ResponseID: "resp_" + id, BaseURL: "https://example.invalid", TeamID: team.ID, UserID: u.ID,
		Record:     UsageRecord{ID: uuid.NewString(), RequestID: id, TeamID: team.ID, UserID: u.ID, Model: "test", UpstreamModel: "test", CreditsMicro: 1_000_000, CreatedAt: now},
		NextPollAt: now, CreatedAt: now}
	bad := job
	bad.UserID = uuid.NewString()
	if err := s.CreateBackgroundGeneration(ctx, bad); !errors.Is(err, ErrReservationMismatch) {
		t.Fatalf("任务不能修改预留所有者: %v", err)
	}
	if err := s.CreateBackgroundGeneration(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBackgroundGeneration(ctx, job); err != nil {
		t.Fatalf("创建应幂等: %v", err)
	}
	jobs, err := s.ClaimBackgroundGenerations(ctx, now, now.Add(time.Minute), 10)
	if err != nil || len(jobs) != 1 || jobs[0].Attempts != 1 {
		t.Fatalf("领取: %+v %v", jobs, err)
	}
	jobs, err = s.ClaimBackgroundGenerations(ctx, now, now.Add(time.Minute), 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("租约内不能重复领取: %+v %v", jobs, err)
	}
	if err := s.RetryBackgroundGeneration(ctx, id, now.Add(-time.Second), "temporary"); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.ClaimBackgroundGenerations(ctx, now, now.Add(time.Minute), 10)
	if err != nil || len(jobs) != 1 || jobs[0].Attempts != 2 {
		t.Fatalf("重试领取: %+v %v", jobs, err)
	}
	record := job.Record
	record.Status = UsageStatusOk
	record.CreditsMicro = 200_000
	if err := s.SettleReservation(ctx, id, record); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishBackgroundGeneration(ctx, id, []byte(`{"status":"completed"}`), 200, "application/json", now); err != nil {
		t.Fatal(err)
	}
	got, err := s.FindBackgroundGeneration(ctx, team.ID, job.ResponseID)
	if err != nil || got == nil || got.CompletedAt == nil || string(got.FinalResponse) != `{"status":"completed"}` {
		t.Fatalf("完成结果: %+v %v", got, err)
	}
	got.FinalResponse[0] = 'x'
	got.Record.Model = "mutated"
	again, _ := s.FindBackgroundGeneration(ctx, team.ID, job.ResponseID)
	if string(again.FinalResponse) != `{"status":"completed"}` || again.Record.Model != "test" {
		t.Fatal("读取必须返回副本")
	}
	jobs, err = s.ClaimBackgroundGenerations(ctx, now.Add(2*time.Minute), now.Add(3*time.Minute), 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("完成任务不能再次领取: %+v %v", jobs, err)
	}
	if other, _ := s.FindBackgroundGeneration(ctx, uuid.NewString(), job.ResponseID); other != nil {
		t.Fatal("不能跨团队读取")
	}
	teamAfter, _ := s.GetTeam(ctx, team.ID)
	if teamAfter.ReservedCredits != "0.000000" || teamAfter.UsedCredits != "0.200000" {
		t.Fatalf("扣实解冻: %+v", teamAfter)
	}
}
