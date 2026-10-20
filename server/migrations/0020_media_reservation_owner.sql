-- 媒体生成由持久化任务负责最终结算／释放，不能让 HTTP 预留清理器提前恢复余额。
ALTER TABLE quota_reservations ADD COLUMN IF NOT EXISTS held_by_task boolean NOT NULL DEFAULT false;
