package pricing

import (
	"testing"
	"time"
)

func shanghaiTime(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, shanghai)
}

func TestIsPeakTime(t *testing.T) {
	// 2026-10-19 是周一；2026-10-17 是周六。
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"周一 08:59 空闲", shanghaiTime(2026, 10, 19, 8, 59), false},
		{"周一 09:00 高峰", shanghaiTime(2026, 10, 19, 9, 0), true},
		{"周一 11:59 高峰", shanghaiTime(2026, 10, 19, 11, 59), true},
		{"周一 12:00 空闲", shanghaiTime(2026, 10, 19, 12, 0), false},
		{"周一 14:00 高峰", shanghaiTime(2026, 10, 19, 14, 0), true},
		{"周一 17:59 高峰", shanghaiTime(2026, 10, 19, 17, 59), true},
		{"周一 18:00 空闲", shanghaiTime(2026, 10, 19, 18, 0), false},
		{"周六 10:00 空闲", shanghaiTime(2026, 10, 17, 10, 0), false},
		{"周日 15:00 空闲", shanghaiTime(2026, 10, 18, 15, 0), false},
	}
	for _, tc := range cases {
		if got := IsPeakTime(tc.at, nil); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	// 法定节假日按日期排除（示例：周一设为假日）。
	holidays := map[string]bool{"2026-10-19": true}
	if IsPeakTime(shanghaiTime(2026, 10, 19, 10, 0), holidays) {
		t.Fatal("节假日应全天空闲")
	}
}

func TestOffPeakMultiplierAt(t *testing.T) {
	if got := OffPeakMultiplierAt(shanghaiTime(2026, 10, 19, 10, 0), 0.5, nil); got != 1 {
		t.Fatalf("高峰应使用原价: %v", got)
	}
	if got := OffPeakMultiplierAt(shanghaiTime(2026, 10, 19, 20, 0), 0.5, nil); got != 0.5 {
		t.Fatalf("空闲应使用倍率: %v", got)
	}
	if got := OffPeakMultiplierAt(shanghaiTime(2026, 10, 19, 20, 0), 1, nil); got != 1 {
		t.Fatalf("倍率为 1 应保持原价: %v", got)
	}
}
