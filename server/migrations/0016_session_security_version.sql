-- MFA 设置变化后按版本撤销会话，覆盖「删除旧会话后，旧密码登录才完成签发」的竞态。
ALTER TABLE sessions ADD COLUMN security_version text NOT NULL DEFAULT '';
-- 兼容已有 MFA 状态：赋予首次安全版本，迁移前没有双因素版本的会话立即失效。
UPDATE platform_config
SET value=jsonb_set(value,'{securityVersion}',to_jsonb(gen_random_uuid()::text)),updated_at=now()
WHERE left(key,length('_security.root-mfa.'))='_security.root-mfa.'
 AND value->>'enabled'='true' AND COALESCE(value->>'securityVersion','')='';
