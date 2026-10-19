package store

import (
	"fmt"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/credits"
)

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func normalizeBudget(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	micro, err := credits.ParseCreditsToMicro(value)
	if err != nil || micro < 0 {
		return "", fmt.Errorf("预算必须是非负金额")
	}
	return credits.FormatMicroCredits(micro), nil
}

func normalizeTeamPolicy(record TeamPolicyRecord) (TeamPolicyRecord, error) {
	var err error
	record.DailyBudgetCredits, err = normalizeBudget(record.DailyBudgetCredits)
	if err != nil {
		return record, err
	}
	record.MonthlyBudgetCredits, err = normalizeBudget(record.MonthlyBudgetCredits)
	if err != nil {
		return record, err
	}
	if record.BudgetAlertPercent == 0 {
		record.BudgetAlertPercent = 80
	}
	if record.BudgetAlertPercent < 1 || record.BudgetAlertPercent > 100 {
		return record, fmt.Errorf("预算提醒比例必须在 1 到 100 之间")
	}
	for _, limit := range []int64{record.RPMLimit, record.TPMLimit, record.ConcurrencyLimit} {
		if limit < 0 || limit > 1_000_000 {
			return record, fmt.Errorf("团队流控必须在 0 到 1000000 之间，0 表示继承平台上限")
		}
	}
	record.AllowedModelIDs = append([]string{}, record.AllowedModelIDs...)
	record.UpdatedAt = time.Now()
	return record, nil
}

func budgetAllows(limit string, spent, requested int64) (bool, error) {
	if requested < 0 {
		return false, nil
	}
	if limit == "" {
		return true, nil
	}
	micro, err := credits.ParseCreditsToMicro(limit)
	if err != nil {
		return false, err
	}
	// 显式零预算表示暂停使用，即使零价模型的预留金额为零也不放行。
	return micro > 0 && requested <= micro && spent <= micro-requested, nil
}

// 与趋势图统一按上海自然日/月，不能沿用 UTC 日界来执行团队预算。
func budgetWindow(at time.Time) (time.Time, time.Time, time.Time, time.Time) {
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	local := at.In(zone)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone)
	month := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, zone)
	return day, day.AddDate(0, 0, 1), month, month.AddDate(0, 1, 0)
}

func TeamModelAllowed(policy *TeamPolicyRecord, modelID string) bool {
	if policy == nil || len(policy.AllowedModelIDs) == 0 {
		return true
	}
	for _, allowed := range policy.AllowedModelIDs {
		if allowed == modelID {
			return true
		}
	}
	return false
}

func addBudgetSpent(total, value int64) int64 {
	const maxMicro = int64(^uint64(0) >> 1)
	if value > 0 && total > maxMicro-value {
		return maxMicro
	}
	return total + value
}
