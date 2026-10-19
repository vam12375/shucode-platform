-- 团队管理员可设置更严格的流控；0 继承 teams 中由平台设置的上限。
-- 网关取双方非零上限的较小值，团队配置不能扩大平台授权。
ALTER TABLE team_policies
  ADD COLUMN rpm_limit integer NOT NULL DEFAULT 0 CHECK (rpm_limit BETWEEN 0 AND 1000000),
  ADD COLUMN tpm_limit integer NOT NULL DEFAULT 0 CHECK (tpm_limit BETWEEN 0 AND 1000000),
  ADD COLUMN concurrency_limit integer NOT NULL DEFAULT 0 CHECK (concurrency_limit BETWEEN 0 AND 1000000);
