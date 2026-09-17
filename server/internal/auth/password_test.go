package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	if !LooksHashed(hash) {
		t.Fatalf("应该是一个 bcrypt 哈希: %q", hash)
	}
	if strings.Contains(hash, "correct-horse") {
		t.Fatal("哈希里绝不能出现明文")
	}
	if err := VerifyPassword(hash, "correct-horse-battery"); err != nil {
		t.Fatalf("正确口令应通过: %v", err)
	}
	if err := VerifyPassword(hash, "correct-horse-battery "); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("尾部空格应视为不同口令: %v", err)
	}
}

// TestHashIsSalted 同一个口令两次哈希必须不同 —— 否则彩虹表/相同口令一眼看穿。
func TestHashIsSalted(t *testing.T) {
	first, err := HashPassword("same-password-here")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	second, err := HashPassword("same-password-here")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	if first == second {
		t.Fatal("两次哈希结果相同: 说明没有加盐")
	}
	if err := VerifyPassword(first, "same-password-here"); err != nil {
		t.Fatalf("盐不同也要能互相校验: %v", err)
	}
}

func TestVerifyPasswordRejectsEmptyHash(t *testing.T) {
	// 历史行 / 手工建的占位账号: 没有口令就永远不能登录(显式规则, 不是靠库里恰好报错)
	if err := VerifyPassword("", "anything"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("空哈希必须拒绝: %v", err)
	}
	if err := VerifyPassword("not-a-bcrypt-hash", "anything"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("非法哈希必须拒绝: %v", err)
	}
}

// TestPasswordLengthBoundary 72 字节是 bcrypt 的硬上限: 超长口令会被静默截断,
// 那等于"100 字节口令"和"它的前 72 字节"是同一个口令 —— 必须直接拒绝。
func TestPasswordLengthBoundary(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("a", MinPasswordBytes-1)); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("%d 字节应太短: %v", MinPasswordBytes-1, err)
	}
	if _, err := HashPassword(strings.Repeat("a", MinPasswordBytes)); err != nil {
		t.Fatalf("%d 字节应通过: %v", MinPasswordBytes, err)
	}
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes)); err != nil {
		t.Fatalf("72 字节应通过: %v", err)
	}
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("73 字节应被拒绝(否则会被 bcrypt 悄悄截断): %v", err)
	}
	// 多字节字符按字节数算: 24 个汉字 = 72 字节, 正好在上限; 25 个就超了
	if _, err := HashPassword(strings.Repeat("密", 24)); err != nil {
		t.Fatalf("24 个汉字(72 字节)应通过: %v", err)
	}
	if _, err := HashPassword(strings.Repeat("密", 25)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("75 字节应被拒绝: %v", err)
	}
}

func TestPasswordMustNotBeOnlyWhitespace(t *testing.T) {
	if _, err := HashPassword("        "); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("纯空格口令是误操作: %v", err)
	}
}
