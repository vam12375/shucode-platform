package pricing

import "time"

// DeepSeek/北京时间高峰时段：周一至周五 09:00-12:00、14:00-18:00，
// 不含中国法定节假日。假日列表由部署方按年维护（PEAK_HOURS_HOLIDAYS）。
var shanghai = time.FixedZone("Asia/Shanghai", 8*3600)

// IsPeakTime 判断给定时刻是否处于高峰时段。
// holidays 使用 YYYY-MM-DD（北京时间日期）集合；周末与节假日全天为空闲。
func IsPeakTime(now time.Time, holidays map[string]bool) bool {
	local := now.In(shanghai)
	weekday := local.Weekday()
	if weekday == time.Saturday || weekday == time.Sunday {
		return false
	}
	if holidays != nil && holidays[local.Format("2006-01-02")] {
		return false
	}
	hour := local.Hour()
	return (hour >= 9 && hour < 12) || (hour >= 14 && hour < 18)
}

// OffPeakMultiplierAt 返回当前时刻应使用的空闲倍率。
// multiplier <= 0 或 current 为高峰时返回 1；否则返回 multiplier。
func OffPeakMultiplierAt(now time.Time, multiplier float64, holidays map[string]bool) float64 {
	if multiplier <= 0 || multiplier == 1 {
		return 1
	}
	if IsPeakTime(now, holidays) {
		return 1
	}
	return multiplier
}
