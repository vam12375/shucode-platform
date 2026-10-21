package store

import "time"

// 观察水位保存在既有模板 JSON，升级不丢失旧任务，也无需增加重复业务状态列。
type backgroundRecordTemplate struct {
	UsageRecord
	UsageObservedAt *time.Time `json:"usageObservedAt,omitempty"`
}

func updateBackgroundUsage(job *BackgroundGenerationJob, record UsageRecord) error {
	if record.TeamID != job.TeamID || record.UserID != job.UserID || record.RequestID != job.Record.RequestID {
		return ErrReservationMismatch
	}
	if record.CreatedAt.IsZero() {
		return ErrInvalidInput
	}
	for _, count := range []int64{record.InputTokens, record.OutputTokens, record.CacheReadTokens, record.CacheCreationTokens} {
		if count < 0 || count > 9_007_199_254_740_991 {
			return ErrInvalidInput
		}
	}
	if job.CompletedAt != nil || (job.UsageObservedAt != nil && !record.CreatedAt.After(*job.UsageObservedAt)) {
		return nil
	}
	// usage 是一份累计快照，缓存分类可能改变，逐字段 max 会将 fresh 与 cached 重复计费。
	job.Record.InputTokens = record.InputTokens
	job.Record.OutputTokens = record.OutputTokens
	job.Record.CacheReadTokens = record.CacheReadTokens
	job.Record.CacheCreationTokens = record.CacheCreationTokens
	job.Record.PriceSnapshot = cloneUsage(record).PriceSnapshot
	job.Record.CreditsMicro = 0
	job.UsageObservedAt = copyTime(&record.CreatedAt)
	return nil
}

func cloneBackgroundGeneration(job BackgroundGenerationJob) BackgroundGenerationJob {
	job.Record = cloneUsage(job.Record)
	job.UsageObservedAt = copyTime(job.UsageObservedAt)
	job.CompletedAt = copyTime(job.CompletedAt)
	job.FinalResponse = append([]byte(nil), job.FinalResponse...)
	return job
}
