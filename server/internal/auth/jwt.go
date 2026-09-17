package auth

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWT 签发与校验(M7-1)。
//
// 方案: **HS256 无状态 token**, 不用 refresh token、不落库、不做黑名单。
// 代价是"签出去的 token 到期前撤不回来"(只能换 JWT_SECRET 让全部失效), 这个代价
// 在内部工具 + 12 小时有效期下可以接受, 并且写进了 process.md 的决策(D22),
// 而不是含糊地"以后再补"。
//
// 安全性靠三件事, 缺一不可:
//  1. **只认 HS256**(WithValidMethods): 否则攻击者可以把 header 改成 alg=none
//     或 alg=RS256 并用公钥当 HMAC 密钥, 这是 JWT 最经典的翻车方式;
//  2. **校验 iss/aud/exp**: 只验签名等于"只要是本密钥签的就行", 同一个密钥的
//     另一个服务的 token 就能横向过来;
//  3. **密钥长度下限**: 短密钥对 HMAC 没有意义(可暴力), 启动时就拒绝。

const (
	// TokenIssuer / TokenAudience: 写死在这里而不是配置 —— 它们是"这个 token 属于谁"
	// 的标识, 允许配置只会让两个部署互相认错对方的 token。
	TokenIssuer   = "eval-platform"
	TokenAudience = "eval-platform-web"

	// MinSecretBytes HS256 的密钥至少 32 字节(等于 sha256 的输出长度)。
	MinSecretBytes = 32

	// ClockLeeway 允许的时钟偏差: 多实例部署时机器时间不可能完全一致。
	ClockLeeway = 30 * time.Second
)

var (
	ErrTokenInvalid   = errors.New("token 无效")
	ErrTokenExpired   = errors.New("token 已过期")
	ErrSecretTooShort = errors.New("JWT_SECRET 至少 32 字节")
)

// Claims 我们自己认的载荷。
//
// Role 放进 token 是为了"每次请求不查库"——代价是**改了角色要等 token 过期才生效**
// (<=12h)。这个取舍要写清楚: 内部工具接受; 真要立刻生效, 停用账号那条路是查库的
// (见 service.Authenticate), 因为"停用"必须立即生效。
type Claims struct {
	UserID int64  `json:"uid"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// Issuer 签发 token。
type Issuer struct {
	secret []byte
	ttl    time.Duration
	// now 可注入, 便于测试过期行为(不用 sleep 也不依赖真实时钟)。
	now func() time.Time
}

// NewIssuer 构造签发器。ttl <= 0 时用 12 小时。
func NewIssuer(secret string, ttl time.Duration) (*Issuer, error) {
	if len([]byte(secret)) < MinSecretBytes {
		return nil, ErrSecretTooShort
	}
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &Issuer{secret: []byte(secret), ttl: ttl, now: time.Now}, nil
}

// TTL 暴露有效期给上层(登录响应里要告诉前端什么时候过期)。
func (i *Issuer) TTL() time.Duration { return i.ttl }

// Sign 为某个用户签发 token, 返回 token 与过期时间。
func (i *Issuer) Sign(userID int64, email, role string) (string, time.Time, error) {
	issuedAt := i.now()
	expiresAt := issuedAt.Add(i.ttl)
	claims := Claims{
		UserID: userID,
		Email:  email,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    TokenIssuer,
			Audience:  jwt.ClaimStrings{TokenAudience},
			Subject:   strconv.FormatInt(userID, 10),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			NotBefore: jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        newTokenID(issuedAt),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(i.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("签发 token 失败: %w", err)
	}
	return signed, expiresAt, nil
}

// Verify 校验 token 并返回载荷。
//
// 失败一律返回 ErrTokenInvalid/ErrTokenExpired, 不把底层错误原样抛给调用方 ——
// 上层只需要"能不能用", 而把 jwt 库的错误文本透到响应里等于给攻击者送调试信息。
func (i *Issuer) Verify(raw string) (Claims, error) {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(TokenIssuer),
		jwt.WithAudience(TokenAudience),
		jwt.WithLeeway(ClockLeeway),
		jwt.WithExpirationRequired(),
	)
	var claims Claims
	_, err := parser.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) {
		return i.secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return Claims{}, ErrTokenExpired
		}
		return Claims{}, ErrTokenInvalid
	}
	if claims.UserID <= 0 || strings.TrimSpace(claims.Role) == "" {
		// 签名对但载荷缺关键字段(手工构造/旧版本 token): 同样视为无效
		return Claims{}, ErrTokenInvalid
	}
	return claims, nil
}

// newTokenID 给每个 token 一个 id(便于日志里追踪同一次登录的多条请求)。
// 不追求密码学随机: 它不作为安全凭据, 只是关联标识。
func newTokenID(at time.Time) string {
	return strconv.FormatInt(at.UnixNano(), 36)
}

// BearerToken 从 Authorization 头里取 Bearer token。
func BearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
