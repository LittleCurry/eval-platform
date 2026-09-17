package auth

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// 口令哈希(M7-1)。
//
// 用 bcrypt 而不是自己拼 sha256+salt: 口令哈希的要害不是"能不能抗碰撞", 而是
// **能不能抗离线爆破** —— bcrypt 的 cost 就是为这个设计的(每次尝试都要真花时间)。
//
// 为什么不用 argon2: x/crypto 里也有, 但参数(内存/并行度)调错反而更弱,
// 而 bcrypt 的默认 cost 是"拧一个数字就不会错"的那种; 内部工具不值得在这里冒险。

const (
	// MinPasswordBytes / MaxPasswordBytes: bcrypt 只吃前 72 字节, 超长口令会被
	// **静默截断** —— 那意味着"100 字节的口令"和"它的前 72 字节"是同一个口令,
	// 这是真实的安全问题, 所以宁可直接拒绝, 也不接受一个被悄悄剪短的口令。
	MinPasswordBytes = 8
	MaxPasswordBytes = 72
)

// 口令校验的失败原因(handler 只把它们翻成 400, 不泄露细节)。
var (
	ErrPasswordTooShort = errors.New("口令太短")
	ErrPasswordTooLong  = errors.New("口令太长")
	ErrPasswordMismatch = errors.New("账号或口令不正确")
)

// ValidatePassword 只做长度校验; 复杂度规则(大小写/符号)刻意不加 ——
// 内部工具的 8 位随机口令比"必须含符号"的 6 位口令强, 而后者只会让人写在便签上。
func ValidatePassword(password string) error {
	length := len([]byte(password))
	if length < MinPasswordBytes {
		return ErrPasswordTooShort
	}
	if length > MaxPasswordBytes {
		return ErrPasswordTooLong
	}
	// 纯空格口令能过长度检查但显然是误操作
	if strings.TrimSpace(password) == "" {
		return ErrPasswordTooShort
	}
	return nil
}

// HashPassword 生成 bcrypt 哈希。
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("生成口令哈希失败: %w", err)
	}
	return string(hashed), nil
}

// VerifyPassword 校验口令。
//
// 空哈希(历史行 / 手工建的占位账号)**永远不通过**: bcrypt 对空哈希会直接报错,
// 但依赖"它恰好报错"太隐晦, 这里显式拒绝 —— "没有口令的账号不能登录"是一条规则,
// 不是一个副作用。所有失败都返回同一个错误, 调用方不必区分(也不该区分)。
func VerifyPassword(hash, password string) error {
	if hash == "" {
		return ErrPasswordMismatch
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return ErrPasswordMismatch
	}
	return nil
}

// LooksHashed 判断一个字符串是不是 bcrypt 哈希(前缀 $2a/$2b/$2y + 长度)。
// 迁移历史明文口令时用得上; 现在只用于测试与自检。
func LooksHashed(value string) bool {
	if len(value) != 60 {
		return false
	}
	if !strings.HasPrefix(value, "$2a$") && !strings.HasPrefix(value, "$2b$") &&
		!strings.HasPrefix(value, "$2y$") {
		return false
	}
	return utf8.ValidString(value)
}
