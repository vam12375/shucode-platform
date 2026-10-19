package gwapp

import "github.com/vam12375/shucode-platform/server/internal/store"

// 团队策略只能收紧平台上限，0 继承平台值；平台不限时仍可由团队主动限制。
func effectiveTeamLimit(platformLimit, policyLimit int64) int64 {
	if policyLimit <= 0 {
		return platformLimit
	}
	if platformLimit <= 0 || policyLimit < platformLimit {
		return policyLimit
	}
	return platformLimit
}

func effectiveTeamLimits(team store.TeamRecord, policy *store.TeamPolicyRecord) store.TeamRecord {
	if policy != nil {
		team.RPMLimit = effectiveTeamLimit(team.RPMLimit, policy.RPMLimit)
		team.TPMLimit = effectiveTeamLimit(team.TPMLimit, policy.TPMLimit)
		team.ConcurrencyLimit = effectiveTeamLimit(team.ConcurrencyLimit, policy.ConcurrencyLimit)
	}
	return team
}
