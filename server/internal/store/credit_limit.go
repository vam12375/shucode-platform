package store

import "github.com/vam12375/shucode-platform/server/internal/credits"

const maxCreditFieldMicro int64 = 999_999_999_999_999_999

func teamCreditAdmissionAllowed(used, quota string, unlimited bool, requested int64) (bool, error) {
	if requested < 0 || requested > maxCreditFieldMicro {
		return false, nil
	}
	usedMicro, usedErr := credits.ParseCreditsToMicro(used)
	quotaMicro, quotaErr := credits.ParseCreditsToMicro(quota)
	if usedErr != nil || quotaErr != nil || usedMicro < 0 || usedMicro > maxCreditFieldMicro || quotaMicro < 0 || quotaMicro > maxCreditFieldMicro {
		return false, ErrInvalidCreditCharge
	}
	if unlimited {
		return true, nil
	}
	// 余额仅按已结算账本计算；并发已受理请求不占额度，实际费用可以使余额为负。
	return usedMicro < quotaMicro && requested <= quotaMicro-usedMicro, nil
}

func fullTeamCreditCharge(used string, chargeMicro int64) (int64, error) {
	usedMicro, err := credits.ParseCreditsToMicro(used)
	if err != nil || usedMicro < 0 || usedMicro > maxCreditFieldMicro || chargeMicro < 0 || chargeMicro > maxCreditFieldMicro-usedMicro {
		return 0, ErrInvalidCreditCharge
	}
	// 已受理请求必须完整实扣，只检查账本存储范围，不能以余额或旧尾差上限截断真实费用。
	return chargeMicro, nil
}

func prepareTeamCreditIncrease(oldQuota string, patch UpdateTeamPatch, audit *InsertAuditLogInput) (UpdateTeamPatch, error) {
	if patch.QuotaIncreaseMicro == nil {
		return patch, nil
	}
	amount := *patch.QuotaIncreaseMicro
	old, err := credits.ParseCreditsToMicro(oldQuota)
	if audit == nil || audit.EventID == "" || patch.QuotaCredits != nil || amount <= 0 || amount > maxCreditFieldMicro || err != nil || old < 0 || old > maxCreditFieldMicro-amount {
		return patch, ErrInvalidInput
	}
	quota := credits.FormatMicroCredits(old + amount)
	patch.QuotaCredits = &quota
	return patch, nil
}

func teamCreditCeiling(quotaMicro int64) int64 {
	if quotaMicro > maxCreditFieldMicro-TeamCreditDebtLimitMicro {
		return maxCreditFieldMicro
	}
	return quotaMicro + TeamCreditDebtLimitMicro
}

// teamCreditWithinLimit tests a proposed reservation without overflowing int64.
func teamCreditWithinLimit(used, reserved, quota string, unlimited bool, additional int64) (bool, error) {
	if additional < 0 || additional > maxCreditFieldMicro {
		return false, nil
	}
	if unlimited {
		return true, nil
	}
	usedMicro, err := credits.ParseCreditsToMicro(used)
	if err != nil {
		return false, err
	}
	reservedMicro, err := credits.ParseCreditsToMicro(reserved)
	if err != nil {
		return false, err
	}
	quotaMicro, err := credits.ParseCreditsToMicro(quota)
	if err != nil {
		return false, err
	}
	// 新准入不能使用透支额度；-0.01 仅留给已放行请求的结算尾差。
	ceiling := quotaMicro
	if usedMicro >= ceiling || reservedMicro >= ceiling-usedMicro || additional > ceiling || usedMicro > ceiling-additional {
		return false, nil
	}
	return reservedMicro <= ceiling-additional-usedMicro, nil
}

// capTeamCreditCharge keeps settled team usage within the hard debt floor.
// releasedMicro is removed from the team's outstanding reservation first.
func capTeamCreditCharge(used, reserved, quota string, unlimited bool, releasedMicro, chargeMicro int64) (int64, bool, error) {
	if chargeMicro < 0 || chargeMicro > maxCreditFieldMicro {
		return 0, false, ErrInvalidCreditCharge
	}
	if unlimited {
		return chargeMicro, false, nil
	}
	usedMicro, err := credits.ParseCreditsToMicro(used)
	if err != nil {
		return 0, false, err
	}
	reservedMicro, err := credits.ParseCreditsToMicro(reserved)
	if err != nil {
		return 0, false, err
	}
	quotaMicro, err := credits.ParseCreditsToMicro(quota)
	if err != nil {
		return 0, false, err
	}
	if releasedMicro > 0 {
		reservedMicro -= releasedMicro
		if reservedMicro < 0 {
			reservedMicro = 0
		}
	}
	room := teamCreditCeiling(quotaMicro) - usedMicro - reservedMicro
	if room < 0 {
		room = 0
	}
	if chargeMicro > room {
		return room, true, nil
	}
	return chargeMicro, false, nil
}

func markCreditChargeCapped(record *UsageRecord) {
	// 实扣封顶后仍保留原始 token 和单价快照，账本显式标记异常，不能静默当成功。
	record.Status = UsageStatusError
	record.ErrorType = "credit_charge_capped"
}
