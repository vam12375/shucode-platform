-- 已部署的 0023 不可修改；补充准入时额度模式，保留无限额度后台任务语义。
ALTER TABLE gateway_background_generations ADD COLUMN limited boolean NOT NULL DEFAULT true;
UPDATE gateway_background_generations AS job
SET limited = NOT team.unlimited
FROM teams AS team WHERE job.team_id = team.id;
