-- 角色层级：root(唯一超级管理员) > admin(管理员) > user(普通用户)。
-- root 账号只能由控制台首次初始化创建，且全库只允许一个；密码只以 scrypt 哈希落库，不进 .env。

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
UPDATE users SET role = 'user' WHERE role = 'member';

-- 存量部署升级：把最早创建的可用管理员提升为 root，避免升级后无人拥有超级权限。
-- 新库没有管理员时不创建账号，由控制台首次初始化创建。
UPDATE users
SET role = 'root'
WHERE id = (
  SELECT id
  FROM users
  WHERE role = 'admin' AND disabled_at IS NULL
  ORDER BY created_at ASC, id ASC
  LIMIT 1
)
AND NOT EXISTS (SELECT 1 FROM users WHERE role = 'root');

ALTER TABLE users
  ADD CONSTRAINT users_role_check CHECK (role IN ('root', 'admin', 'user'));

-- root 唯一；root 不可禁用（状态约束与唯一性同层兜底）。
CREATE UNIQUE INDEX users_single_root ON users (role) WHERE role = 'root';
ALTER TABLE users
  ADD CONSTRAINT users_root_enabled CHECK (role <> 'root' OR disabled_at IS NULL);
