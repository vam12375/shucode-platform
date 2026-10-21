-- 请求身份继续承担后台任务与补偿的幂等关联，执行期间不再占用余额。
ALTER TABLE quota_reservations ADD COLUMN postpaid boolean NOT NULL DEFAULT false;

-- 迁移保留原 UUID、状态及所有外键；旧过期但尚未结算的身份也按完整实际费用结算。
UPDATE quota_reservations
SET postpaid = true, reserved_credits = 0, retain_until_settled = true
WHERE status IN ('reserved', 'expired');

ALTER TABLE quota_reservations
ADD CONSTRAINT quota_reservations_postpaid_no_reservation CHECK (NOT postpaid OR reserved_credits = 0);

-- migration runner 在同一事务内执行，团队汇总与请求占款一起归零。
UPDATE teams SET reserved_credits = 0;
