package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testPersonalAccounts(t *testing.T, ctx context.Context, s PlatformStore) {
	owner := mustUser(t, ctx, s, "personal-owner@store.test")
	other := mustUser(t, ctx, s, "personal-other@store.test")
	// 普通团队占用旧的按用户 ID 命名方式时，个人账户仍须独立创建且名称唯一。
	collision, err := s.CreateTeam(ctx, CreateTeamInput{Name: personalTeamName(owner.ID), QuotaCredits: "0"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetPersonalAccount(ctx, owner.ID)
	if err != nil || before != nil {
		t.Fatalf("get must not create: %+v %v", before, err)
	}
	mode, err := s.GetBillingMode(ctx, owner.ID)
	if err != nil || mode != "team" {
		t.Fatalf("default mode: %s %v", mode, err)
	}
	ids := make(chan string, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			team, err := s.EnsurePersonalAccount(ctx, owner.ID)
			if err != nil {
				t.Errorf("ensure personal: %v", err)
				return
			}
			ids <- team.ID
		}()
	}
	wg.Wait()
	close(ids)
	var accountID string
	for id := range ids {
		if accountID == "" {
			accountID = id
		}
		if accountID != id {
			t.Fatal("concurrent personal accounts duplicated")
		}
	}
	account, err := s.GetPersonalAccount(ctx, owner.ID)
	if err != nil || account == nil || account.ID != accountID || account.QuotaCredits != "0.000000" || account.Unlimited {
		t.Fatalf("personal account: %+v %v", account, err)
	}
	if account.Name == collision.Name {
		t.Fatal("personal account reused an existing organization name")
	}
	account.QuotaCredits = "999"
	account, _ = s.GetPersonalAccount(ctx, owner.ID)
	if account.QuotaCredits != "0.000000" {
		t.Fatal("account snapshots alias storage")
	}
	teams, err := s.ListTeams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range teams {
		if team.Team.ID == accountID {
			t.Fatal("private account leaked into organization list")
		}
	}
	for _, id := range []string{owner.ID, other.ID} {
		if err = s.AddMember(ctx, accountID, id, MemberRoleOwner); !errors.Is(err, ErrPersonalAccountForbidden) {
			t.Fatalf("private account add member: %v", err)
		}
	}
	if err = s.CreateTeamInvitation(ctx, TeamInvitationRecord{ID: uuid.NewString(), TeamID: accountID, CreatedBy: owner.ID, Email: other.Email, TokenHash: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour)}); !errors.Is(err, ErrPersonalAccountForbidden) {
		t.Fatalf("private account invitation: %v", err)
	}
	if private, err := s.IsPersonalAccountTeam(ctx, accountID); err != nil || !private {
		t.Fatalf("is personal: %v %v", private, err)
	}
	organization, err := s.CreateTeam(ctx, CreateTeamInput{Name: "personal-owner-org", QuotaCredits: "5"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddMember(ctx, organization.ID, owner.ID, MemberRoleMember); err != nil {
		t.Fatalf("personal account must not consume organization slot: %v", err)
	}
	selected, err := ResolveBillingAccount(ctx, s, owner.ID)
	if err != nil || selected == nil || selected.Team.ID != organization.ID {
		t.Fatalf("default organization: %+v %v", selected, err)
	}
	if err = s.SetBillingMode(ctx, owner.ID, "personal"); err != nil {
		t.Fatal(err)
	}
	selected, err = ResolveBillingAccount(ctx, s, owner.ID)
	if err != nil || selected == nil || selected.Team.ID != accountID {
		t.Fatalf("select personal: %+v %v", selected, err)
	}
	orgSelected, err := ResolveUserTeam(ctx, s, owner.ID)
	if err != nil || orgSelected == nil || orgSelected.Team.ID != organization.ID {
		t.Fatalf("organization semantics changed: %+v %v", orgSelected, err)
	}
	if err = s.SetBillingMode(ctx, other.ID, "personal"); err != nil {
		t.Fatal(err)
	}
	otherAccount, err := s.GetPersonalAccount(ctx, other.ID)
	if err != nil || otherAccount == nil || otherAccount.ID == accountID {
		t.Fatalf("independent account: %+v %v", otherAccount, err)
	}
	if err = s.SetBillingMode(ctx, owner.ID, "invalid"); !errors.Is(err, ErrBillingModeInvalid) {
		t.Fatalf("invalid mode: %v", err)
	}
	now := time.Now().UTC()
	order := PaymentOrderRecord{ID: uuid.NewString(), OutTradeNo: "personal_" + uuid.NewString(), Provider: PaymentProviderAlipay, ConfigurationID: uuid.NewString(), TargetType: PaymentTargetPersonal, TargetID: owner.ID, TeamID: accountID, UserID: owner.ID, AppID: "2026000000000001", SellerID: "2088000000000001", AmountFen: 100, CreditMicro: 1_000_000, Subject: "个人充值", CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute)}
	if _, err = s.CreatePaymentOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ClaimPaymentSuccess(ctx, PaymentSuccessInput{OutTradeNo: order.OutTradeNo, TargetType: order.TargetType, TargetID: order.TargetID, TeamID: accountID, AppID: order.AppID, SellerID: order.SellerID, AmountFen: 100, TradeNo: "personal_trade_" + uuid.NewString(), PaidAt: now}); err != nil {
		t.Fatal(err)
	}
	reservationID, requestID := uuid.NewString(), uuid.NewString()
	result, err := s.AdmitUsage(ctx, ReserveQuotaInput{ID: reservationID, RequestID: requestID, TeamID: accountID, UserID: owner.ID, Micro: 0, CreatedAt: now})
	if err != nil || result != ReserveReserved {
		t.Fatalf("personal admission: %s %v", result, err)
	}
	result, err = s.AdmitUsage(ctx, ReserveQuotaInput{ID: uuid.NewString(), RequestID: uuid.NewString(), TeamID: accountID, UserID: other.ID, Micro: 0})
	if err != nil || result != ReserveDeniedInactive {
		t.Fatalf("other user private admission: %s %v", result, err)
	}
	result, err = s.AdmitUsage(ctx, ReserveQuotaInput{ID: uuid.NewString(), RequestID: uuid.NewString(), TeamID: accountID, Micro: 0})
	if err != nil || result != ReserveDeniedInactive {
		t.Fatalf("anonymous private admission: %s %v", result, err)
	}
	if err = s.SetBillingMode(ctx, owner.ID, "team"); err != nil {
		t.Fatal(err)
	}
	if err = s.SettleReservation(ctx, reservationID, UsageRecord{ID: uuid.NewString(), RequestID: requestID, TeamID: accountID, UserID: owner.ID, Model: "personal-test-model", UpstreamModel: "personal-test-model", CreditsMicro: 250_000, Status: UsageStatusOk, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	account, _ = s.GetPersonalAccount(ctx, owner.ID)
	organization, _ = s.GetTeam(ctx, organization.ID)
	if account.QuotaCredits != "1.000000" || account.UsedCredits != "0.250000" || organization.QuotaCredits != "5.000000" || organization.UsedCredits != "0.000000" {
		t.Fatalf("settlement must stay with admitted account: %+v %+v", account, organization)
	}
	disabled := now
	if _, err = s.UpdateUser(ctx, owner.ID, UpdateUserPatch{HasDisabled: true, DisabledAt: &disabled}); err != nil {
		t.Fatal(err)
	}
	result, err = s.AdmitUsage(ctx, ReserveQuotaInput{ID: uuid.NewString(), RequestID: uuid.NewString(), TeamID: accountID, UserID: owner.ID, Micro: 0})
	if err != nil || result != ReserveDeniedInactive {
		t.Fatalf("disabled owner private admission: %s %v", result, err)
	}
}
