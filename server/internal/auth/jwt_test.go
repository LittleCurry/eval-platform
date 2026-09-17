package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newTestIssuer(t *testing.T, ttl time.Duration) *Issuer {
	t.Helper()
	issuer, err := NewIssuer(testSecret, ttl)
	if err != nil {
		t.Fatalf("构造签发器失败: %v", err)
	}
	return issuer
}

func TestSignAndVerify(t *testing.T) {
	issuer := newTestIssuer(t, time.Hour)
	token, expiresAt, err := issuer.Sign(42, "a@b.com", "editor")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if time.Until(expiresAt) > time.Hour+time.Minute {
		t.Fatalf("过期时间应约等于 ttl: %v", expiresAt)
	}
	claims, err := issuer.Verify(token)
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if claims.UserID != 42 || claims.Role != "editor" || claims.Email != "a@b.com" {
		t.Fatalf("载荷不符: %+v", claims)
	}
	if claims.Issuer != TokenIssuer || len(claims.Audience) != 1 || claims.Audience[0] != TokenAudience {
		t.Fatalf("iss/aud 应写死: %+v", claims.RegisteredClaims)
	}
	if claims.ID == "" {
		t.Fatal("应带 jti(便于日志关联)")
	}
}

// TestVerifyRejectsTamperedPayload 改一个字节的载荷(比如把 role 改成 admin)必须失败。
func TestVerifyRejectsTamperedPayload(t *testing.T) {
	issuer := newTestIssuer(t, time.Hour)
	token, _, err := issuer.Sign(7, "v@b.com", "viewer")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT 应是三段: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("解码载荷失败: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("解析载荷失败: %v", err)
	}
	claims["role"] = "admin" // 提权尝试
	modified, _ := json.Marshal(claims)
	forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString(modified) + "." + parts[2]

	if _, err := issuer.Verify(forged); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("提权后的 token 必须无效: %v", err)
	}
}

// TestVerifyRejectsAlgNone 经典攻击: 把 header 的 alg 改成 none。
func TestVerifyRejectsAlgNone(t *testing.T) {
	issuer := newTestIssuer(t, time.Hour)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(
		`{"uid":1,"role":"admin","iss":"eval-platform","aud":["eval-platform-web"],"exp":9999999999}`))
	forged := header + "." + payload + "."

	if _, err := issuer.Verify(forged); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("alg=none 必须被拒绝: %v", err)
	}
}

// TestVerifyRejectsWrongSecret 换密钥签的 token 不能被接受。
func TestVerifyRejectsWrongSecret(t *testing.T) {
	issuer := newTestIssuer(t, time.Hour)
	other, err := NewIssuer("another-secret-with-enough-length-0123456789", time.Hour)
	if err != nil {
		t.Fatalf("构造第二个签发器失败: %v", err)
	}
	token, _, err := other.Sign(1, "a@b.com", "admin")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if _, err := issuer.Verify(token); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("别的密钥签的 token 必须无效: %v", err)
	}
}

// TestVerifyRejectsWrongIssuerOrAudience 只验签名是不够的: 同一个密钥的另一个服务的
// token 不能横向过来。
func TestVerifyRejectsWrongIssuerOrAudience(t *testing.T) {
	issuer := newTestIssuer(t, time.Hour)
	base := Claims{
		UserID: 1, Email: "a@b.com", Role: "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "1",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}

	wrongIssuer := base
	wrongIssuer.Issuer = "another-service"
	wrongIssuer.Audience = jwt.ClaimStrings{TokenAudience}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, wrongIssuer).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("构造 token 失败: %v", err)
	}
	if _, err := issuer.Verify(signed); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("iss 不符必须拒绝: %v", err)
	}

	wrongAudience := base
	wrongAudience.Issuer = TokenIssuer
	wrongAudience.Audience = jwt.ClaimStrings{"other-app"}
	signed, err = jwt.NewWithClaims(jwt.SigningMethodHS256, wrongAudience).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("构造 token 失败: %v", err)
	}
	if _, err := issuer.Verify(signed); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("aud 不符必须拒绝: %v", err)
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	issuer := newTestIssuer(t, time.Hour)
	// 把一个"两小时前签发、一小时有效期"的 token 造出来(用可注入的时钟, 不 sleep)
	past := time.Now().Add(-2 * time.Hour)
	issuer.now = func() time.Time { return past }
	token, _, err := issuer.Sign(1, "a@b.com", "viewer")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	issuer.now = time.Now

	if _, err := issuer.Verify(token); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("过期 token 应报 ErrTokenExpired: %v", err)
	}
}

// TestVerifyRejectsMissingClaims 签名正确但载荷缺 uid/role 的 token(手工构造/旧版本)一律无效。
func TestVerifyRejectsMissingClaims(t *testing.T) {
	issuer := newTestIssuer(t, time.Hour)
	claims := Claims{
		UserID: 0, Role: "",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: TokenIssuer, Audience: jwt.ClaimStrings{TokenAudience}, Subject: "0",
			IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("构造 token 失败: %v", err)
	}
	if _, err := issuer.Verify(token); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("缺 uid/role 必须无效: %v", err)
	}
}

func TestNewIssuerRejectsShortSecret(t *testing.T) {
	if _, err := NewIssuer("too-short", time.Hour); !errors.Is(err, ErrSecretTooShort) {
		t.Fatalf("短密钥必须拒绝: %v", err)
	}
	if _, err := NewIssuer(strings.Repeat("k", MinSecretBytes), time.Hour); err != nil {
		t.Fatalf("%d 字节密钥应通过: %v", MinSecretBytes, err)
	}
}

func TestIssuerDefaultTTL(t *testing.T) {
	issuer, err := NewIssuer(testSecret, 0)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if issuer.TTL() != 12*time.Hour {
		t.Fatalf("未指定 ttl 时应默认 12 小时: %v", issuer.TTL())
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc.def.ghi", "abc.def.ghi", true},
		{"bearer abc", "abc", true}, // 大小写不敏感(HTTP 头本就如此)
		{"Bearer  abc  ", "abc", true},
		{"Token abc", "", false},
		{"Bearer ", "", false},
		{"", "", false},
		{"abc", "", false},
	}
	for _, item := range cases {
		got, ok := BearerToken(item.header)
		if ok != item.ok || got != item.want {
			t.Fatalf("BearerToken(%q) = (%q, %v), want (%q, %v)", item.header, got, ok, item.want, item.ok)
		}
	}
}
