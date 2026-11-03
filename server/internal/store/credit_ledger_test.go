package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestCreditLedgerMemory(t *testing.T) {
	s, close := newMemoryForTest(t)
	defer close()
	testCreditLedger(t, s)
}
func TestCreditLedgerPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	s, close := newPostgresForTest(t, os.Getenv("TEST_DATABASE_URL"), "")
	defer close()
	testCreditLedger(t, s)
}

func TestCreditLedgerSettledErrorsMemory(t *testing.T) {
	s, close := newMemoryForTest(t)
	defer close()
	testCreditLedgerSettledErrors(t, s)
}

func TestCreditLedgerSettledErrorsPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	s, close := newPostgresForTest(t, os.Getenv("TEST_DATABASE_URL"), "")
	defer close()
	testCreditLedgerSettledErrors(t, s)
}

func testCreditLedgerSettledErrors(t *testing.T, s PlatformStore) {
	ctx := context.Background()
	user := mustUser(t, ctx, s, "ledger-settled-error@test.local")
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "ledger-settled-error", QuotaCredits: "1"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
	for i, charge := range []int64{250_000, 2_000_000, 1} {
		record := UsageRecord{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), RequestID: fmt.Sprintf("ledger-settled-error-%d", i), UserID: user.ID, TeamID: team.ID, Model: "public-model", UpstreamModel: "private-model", CreditsMicro: charge, Status: UsageStatusOk, CreatedAt: at.Add(time.Duration(i) * time.Second)}
		err := s.SettleUsage(ctx, record, 0)
		if i == 0 && err != nil || i > 0 && !errors.Is(err, ErrCreditChargeCapped) {
			t.Fatalf("settlement %d: %v", i, err)
		}
	}
	stored, err := s.ListUsage(ctx, UsageFilter{TeamID: team.ID})
	if err != nil || len(stored) != 3 {
		t.Fatalf("settled usage facts: %+v %v", stored, err)
	}
	for i, want := range []struct {
		status UsageStatus
		charge int64
	}{{UsageStatusOk, 250_000}, {UsageStatusError, 760_000}, {UsageStatusError, 0}} {
		for _, record := range stored {
			if record.RequestID != fmt.Sprintf("ledger-settled-error-%d", i) {
				continue
			}
			if record.Status != want.status || record.CreditsMicro != want.charge || i > 0 && record.ErrorType != "credit_charge_capped" {
				t.Fatalf("unexpected settlement fact: %+v", record)
			}
		}
	}
	account, err := s.GetTeam(ctx, team.ID)
	if err != nil || account.UsedCredits != "1.010000" {
		t.Fatalf("actual settled debit: %+v %v", account, err)
	}
	filter := CreditLedgerFilter{TeamID: team.ID, UsageUserID: user.ID, Limit: 1}
	var usageRequests []string
	for range 4 {
		page, err := s.ListCreditLedger(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if page.Summary.TotalCreditsInMicro != "1000000" || page.Summary.TotalCreditsOutMicro != "1010000" || page.Summary.NetCreditsMicro != "-10000" || page.Summary.EntryCount != 3 {
			t.Fatalf("settled error excluded from cursor-independent summary: %+v", page.Summary)
		}
		for _, item := range page.Items {
			if item.Usage == nil {
				continue
			}
			usageRequests = append(usageRequests, item.Usage.RequestID)
			if item.DeltaMicro != -item.Usage.CreditsMicro || item.Usage.CreditsMicro <= 0 {
				t.Fatalf("ledger must reflect actual nonzero debit: %+v", item)
			}
		}
		if !page.HasMore {
			break
		}
		last := page.Items[len(page.Items)-1]
		filter.CursorCreatedAt, filter.CursorSource, filter.CursorID = &last.CreatedAt, last.Source, last.ID
	}
	if want := []string{"ledger-settled-error-1", "ledger-settled-error-0"}; !reflect.DeepEqual(usageRequests, want) {
		t.Fatalf("nonzero settled errors must be listed and zero errors excluded: got %v want %v", usageRequests, want)
	}
}

func testCreditLedger(t *testing.T, s PlatformStore) {
	ctx := context.Background()
	user := mustUser(t, ctx, s, "ledger-user@test.local")
	other := mustUser(t, ctx, s, "ledger-other@test.local")
	team, err := s.CreateTeam(ctx, CreateTeamInput{Name: "ledger", QuotaCredits: "10"})
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := s.CreateTeam(ctx, CreateTeamInput{Name: "ledger-other", QuotaCredits: "0"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 2, 1, 2, 3, 123456000, time.UTC)
	for i, input := range []struct {
		user, team string
		amount     int64
	}{{user.ID, team.ID, 1_000_001}, {other.ID, team.ID, 2_000_002}, {user.ID, team.ID, 0}, {user.ID, elsewhere.ID, 7_000_000}} {
		if err := s.InsertUsage(ctx, UsageRecord{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), RequestID: fmt.Sprint("ledger-", i), UserID: input.user, TeamID: input.team, Model: "public-model", UpstreamModel: "private-model", CreditsMicro: input.amount, Status: UsageStatusOk, CreatedAt: at, ReasoningBudgetTokens: ptr(int64(99)), PriceSnapshot: &UsagePricingSnapshot{Sale: UsagePriceSnapshot{Quantity: 2}}}); err != nil {
			t.Fatal(err)
		}
	}
	// Source and UUID are both necessary: a quota row may share the same UUID and timestamp with usage.
	for i, input := range []struct{ action, old, new string }{{"alipay_recharge", "10", "15"}, {"set_quota", "15", "12"}, {"set_quota", "12", "12"}} {
		if err := s.InsertQuotaAdjustment(ctx, InsertQuotaAdjustmentInput{TeamID: team.ID, ActorUserID: user.ID, Action: input.action, OldQuotaCredits: input.old, NewQuotaCredits: input.new}); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		switch actual := s.(type) {
		case *memoryStore:
			actual.mu.Lock()
			for j := range actual.quotaAdjusts {
				if actual.quotaAdjusts[j].TeamID == team.ID && actual.quotaAdjusts[j].Action == input.action && actual.quotaAdjusts[j].OldQuotaCredits == input.old {
					actual.quotaAdjusts[j].ID = id
					actual.quotaAdjusts[j].CreatedAt = at
				}
			}
			actual.mu.Unlock()
		case *PostgresStore:
			if _, err := actual.pool.Exec(ctx, `UPDATE team_quota_adjustments SET id=$1::uuid,created_at=$2 WHERE team_id=$3::uuid AND action=$4 AND old_quota_credits=$5::numeric`, id, at, team.ID, input.action, input.old); err != nil {
				t.Fatal(err)
			}
		}
	}
	filter := CreditLedgerFilter{TeamID: team.ID, UsageUserID: user.ID, From: &at, To: &at, Limit: 1}
	var identities []string
	for range 5 {
		page, err := s.ListCreditLedger(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if page.Summary.TotalCreditsInMicro != "5000000" || page.Summary.TotalCreditsOutMicro != "4000001" || page.Summary.NetCreditsMicro != "999999" || page.Summary.EntryCount != 3 {
			t.Fatalf("cursor-independent member summary: %+v", page.Summary)
		}
		for _, item := range page.Items {
			identities = append(identities, item.Source+":"+item.ID)
			if item.Source == "quota" && (item.ActorName == nil || *item.ActorName != user.DisplayName) {
				t.Fatalf("quota actor display identity: %+v", item)
			}
		}
		if !page.HasMore {
			break
		}
		last := page.Items[len(page.Items)-1]
		filter.CursorCreatedAt = &last.CreatedAt
		filter.CursorSource = last.Source
		filter.CursorID = last.ID
	}
	want := []string{"usage:00000000-0000-4000-8000-000000000001", "quota:00000000-0000-4000-8000-000000000002", "quota:00000000-0000-4000-8000-000000000001"}
	if !reflect.DeepEqual(identities, want) {
		t.Fatalf("compound pagination got %v want %v", identities, want)
	}
	full, err := s.ListCreditLedger(ctx, CreditLedgerFilter{TeamID: team.ID, From: &at, To: &at})
	if err != nil || len(full.Items) != 4 || full.Summary.TotalCreditsOutMicro != "6000003" || full.Summary.NetCreditsMicro != "-1000003" {
		t.Fatalf("team scope: %+v %v", full, err)
	}
	full.Items[0].Usage.PriceSnapshot.Sale.Quantity = 999
	*full.Items[0].Usage.ReasoningBudgetTokens = 999
	again, err := s.ListCreditLedger(ctx, CreditLedgerFilter{TeamID: team.ID, From: &at, To: &at})
	if err != nil || again.Items[0].Usage.PriceSnapshot.Sale.Quantity != 2 || *again.Items[0].Usage.ReasoningBudgetTokens != 99 {
		t.Fatalf("return values alias storage: %+v %v", again, err)
	}
	for _, f := range []CreditLedgerFilter{{}, {TeamID: team.ID, CursorID: want[0]}, {TeamID: team.ID, CursorCreatedAt: &at, CursorSource: "invalid", CursorID: user.ID}} {
		if _, err := s.ListCreditLedger(ctx, f); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid filter accepted: %+v %v", f, err)
		}
	}

	// Both team write paths must preserve numeric changes, including direct writes without audit metadata.
	initial, err := s.ListCreditLedger(ctx, CreditLedgerFilter{TeamID: team.ID})
	if err != nil || initial.Summary.TotalCreditsInMicro != "15000000" {
		t.Fatalf("initial grant missing: %+v %v", initial, err)
	}
	q := "9"
	if _, err := s.UpdateTeam(ctx, team.ID, UpdateTeamPatch{QuotaCredits: &q}); err != nil {
		t.Fatal(err)
	}
	q = "8"
	if _, err := s.UpdateTeamWithAccounting(ctx, team.ID, UpdateTeamPatch{QuotaCredits: &q}, "", nil); err != nil {
		t.Fatal(err)
	}
	changed, err := s.ListCreditLedger(ctx, CreditLedgerFilter{TeamID: team.ID})
	if err != nil || changed.Summary.TotalCreditsOutMicro != "8000003" || changed.Summary.EntryCount != 7 {
		t.Fatalf("direct update facts missing: %+v %v", changed, err)
	}
	if _, err := s.UpdateTeam(ctx, team.ID, UpdateTeamPatch{QuotaCredits: &q, Unlimited: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.ListCreditLedger(ctx, CreditLedgerFilter{TeamID: team.ID})
	if err != nil || unchanged.Summary.EntryCount != 7 {
		t.Fatalf("zero/unlimited-only should not be a money event: %+v %v", unchanged, err)
	}

	// Aggregate beyond int64 must stay exact, rather than wrapping or failing a page scan.
	huge, err := s.CreateTeam(ctx, CreateTeamInput{Name: "huge-ledger", QuotaCredits: "0"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := s.InsertUsage(ctx, UsageRecord{RequestID: fmt.Sprint("huge-", i), UserID: user.ID, TeamID: huge.ID, Model: "public-model", UpstreamModel: "private", CreditsMicro: math.MaxInt64, Status: UsageStatusOk, CreatedAt: at.Add(24*time.Hour + time.Duration(i)*time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	bigPage, err := s.ListCreditLedger(ctx, CreditLedgerFilter{TeamID: huge.ID, Limit: 1})
	if err != nil || bigPage.Summary.TotalCreditsOutMicro != "18446744073709551614" || bigPage.Summary.NetCreditsMicro != "-18446744073709551614" {
		t.Fatalf("aggregate overflow: %+v %v", bigPage, err)
	}
}
