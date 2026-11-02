package store

import "context"

// ResolveBillingBindingAccount 校验绑定账本仍归属于 actor 且处于可消费状态；不重新读取用户当前模式。
func ResolveBillingBindingAccount(ctx context.Context, st PlatformStore, binding *BillingSessionBinding) (*TeamMembership, error) {
	if binding == nil || !canonicalUUID(binding.BillingAccountID) || !canonicalUUID(binding.ActorUserID) {
		return nil, ErrBillingBindingForbidden
	}
	if binding.BillingAccountType == "personal" {
		account, err := st.GetPersonalAccount(ctx, binding.ActorUserID)
		if err != nil {
			return nil, err
		}
		if account == nil || account.ID != binding.BillingAccountID || account.DisabledAt != nil {
			return nil, ErrBillingBindingForbidden
		}
		return &TeamMembership{Team: *account, Role: MemberRoleOwner}, nil
	}
	member, err := st.GetTeamMember(ctx, binding.BillingAccountID, binding.ActorUserID)
	if err != nil {
		return nil, err
	}
	if member == nil || member.MemberDisabledAt != nil || member.User.DisabledAt != nil {
		return nil, ErrBillingBindingForbidden
	}
	team, err := st.GetTeam(ctx, binding.BillingAccountID)
	if err != nil || team == nil || team.DisabledAt != nil {
		if err != nil {
			return nil, err
		}
		return nil, ErrBillingBindingForbidden
	}
	return &TeamMembership{Team: *team, Role: member.Role, MemberDisabledAt: member.MemberDisabledAt, MonthlyBudgetCredits: member.MonthlyBudgetCredits}, nil
}
