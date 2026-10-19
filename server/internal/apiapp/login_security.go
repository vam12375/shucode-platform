package apiapp

import (
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// dummyPasswordHash 让未知邮箱也执行一次同参数的 scrypt，避免“账号不存在时
// 跳过口令哈希”形成代码级时序侧信道。哈希在进程启动时生成，盐随机且不入库。
var dummyPasswordHash = func() string {
	hash, err := crypto.HashPassword("shucode-dummy-password-never-matches")
	if err != nil {
		panic("生成 dummy scrypt 哈希失败: " + err.Error())
	}
	return hash
}()

// verifyLoginPassword 对未知用户执行 dummy 校验，对存在用户执行真实校验；
// 禁用账号仍执行真实哈希，但结果一律为 false。
func verifyLoginPassword(user *store.UserRecord, password string) bool {
	if user == nil {
		_ = crypto.VerifyPassword(password, dummyPasswordHash)
		return false
	}
	if !crypto.VerifyPassword(password, user.PasswordHash) {
		return false
	}
	return user.DisabledAt == nil
}
