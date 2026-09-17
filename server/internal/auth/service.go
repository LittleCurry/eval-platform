package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"eval-platform/server/internal/store"
)

// Service 把"账号存储 + 口令 + token"拼成认证用例(M7-1)。
//
// 它只依赖一个小接口(不是 *store.Postgres): 测试里可以塞内存实现,
// 也让"认证用哪些存储能力"这件事在接口上就看得见。
type Service struct {
	users  UserStore
	issuer *Issuer
	now    func() time.Time
}

// UserStore 认证需要的存储能力(比 store 的完整用户接口更窄)。
type UserStore interface {
	CountUsers(ctx context.Context) (int, error)
	CountActiveAdmins(ctx context.Context) (int, error)
	ListUsers(ctx context.Context) ([]store.User, error)
	GetUser(ctx context.Context, id int64) (store.User, error)
	GetUserByEmail(ctx context.Context, email string) (store.User, error)
	CreateUser(ctx context.Context, email, name, passwordHash, role string) (store.User, error)
	UpdateUser(
		ctx context.Context, id int64, name, role *string, disabled *bool, passwordHash *string,
	) (store.User, error)
	DeleteUser(ctx context.Context, id int64) error
	TouchUserLogin(ctx context.Context, id int64) error
}

var (
	ErrInvalidCredentials = errors.New("账号或口令不正确")
	ErrUserDisabled       = errors.New("账号已停用")
	ErrRegistrationClosed = errors.New("注册已关闭: 请让管理员在用户管理里添加账号")
	ErrLastAdmin          = errors.New("不能操作最后一个可用的管理员")
	ErrSelfDemote         = errors.New("不能修改自己的角色或停用自己")
)

// NewService 构造认证服务。
func NewService(users UserStore, issuer *Issuer) *Service {
	return &Service{users: users, issuer: issuer, now: time.Now}
}

// TTL 暴露 token 有效期(登录响应与 /auth/status 都要告诉前端)。
func (s *Service) TTL() time.Duration { return s.issuer.TTL() }

// LoginResult 登录成功的结果(handler 直接返回它)。
type LoginResult struct {
	Token     string     `json:"token"`
	ExpiresAt time.Time  `json:"expires_at"`
	User      store.User `json:"user"`
}

// NormalizeEmail 归一化 email: 小写 + 去空格。
//
// 必须在**所有**入口(normalize 再查/再写)统一走它: 登录是拿 email 查唯一行的,
// 大小写不一致会变成"注册成功但登录不上"这种最难查的问题。DB 那边有 CHECK 兜底,
// 但 CHECK 只会报错, 不会帮你修。
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Register 建账号。
//
// 首次启动(库里一个账号都没有)时, 第一个注册者自动成为 admin —— 否则部署完
// 谁也进不去, 只能手动插库; 之后注册关闭, 由 admin 在用户管理里加人。
func (s *Service) Register(ctx context.Context, email, name, password string) (LoginResult, error) {
	count, err := s.users.CountUsers(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	if count > 0 {
		return LoginResult{}, ErrRegistrationClosed
	}
	return s.createUser(ctx, email, name, password, store.RoleAdmin)
}

// CreateUser 由 admin 建账号(角色任意)。
func (s *Service) CreateUser(
	ctx context.Context, email, name, password, role string,
) (store.User, error) {
	if !ValidRole(role) {
		return store.User{}, errors.New("角色必须是 admin/editor/viewer")
	}
	result, err := s.createUser(ctx, email, name, password, role)
	if err != nil {
		return store.User{}, err
	}
	return result.User, nil
}

func (s *Service) createUser(
	ctx context.Context, email, name, password, role string,
) (LoginResult, error) {
	normalized := NormalizeEmail(email)
	if normalized == "" || !strings.Contains(normalized, "@") {
		return LoginResult{}, errors.New("email 不合法")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return LoginResult{}, err
	}
	user, err := s.users.CreateUser(ctx, normalized, strings.TrimSpace(name), hash, role)
	if err != nil {
		return LoginResult{}, err
	}
	return s.issue(user)
}

// Login 校验口令并签发 token。
func (s *Service) Login(ctx context.Context, email, password string) (LoginResult, error) {
	user, err := s.users.GetUserByEmail(ctx, NormalizeEmail(email))
	if errors.Is(err, store.ErrNotFound) {
		// 账号不存在与口令错误返回同一个错误: 否则接口会变成"账号是否存在"的探测器
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}
	if err := VerifyPassword(user.PasswordHash, password); err != nil {
		return LoginResult{}, ErrInvalidCredentials
	}
	if user.Disabled {
		// 口令对了但已停用: 这时告诉对方"已停用"是合适的 —— 能证明身份的人
		// 有权知道原因, 而口令错的人拿不到这个信息。
		return LoginResult{}, ErrUserDisabled
	}
	if err := s.users.TouchUserLogin(ctx, user.ID); err != nil {
		// 记录登录时间失败不该拦住登录本身
		_ = err
	}
	return s.issue(user)
}

func (s *Service) issue(user store.User) (LoginResult, error) {
	token, expiresAt, err := s.issuer.Sign(user.ID, user.Email, user.Role)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, ExpiresAt: expiresAt, User: user}, nil
}

// Authenticate 校验 token 并**回库确认账号仍然有效**。
//
// 为什么不只信 token 里的 role: 角色改动可以等 token 过期(12h), 但"停用"不行 ——
// 同事离职当天必须立刻断掉。所以每次请求都查一次库(单实例 + 内部工具, 这点开销可忽略);
// 用户被删也在这里变成 401。
func (s *Service) Authenticate(ctx context.Context, token string) (store.User, error) {
	claims, err := s.issuer.Verify(token)
	if err != nil {
		return store.User{}, err
	}
	user, err := s.users.GetUser(ctx, claims.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, ErrTokenInvalid
	}
	if err != nil {
		return store.User{}, err
	}
	if user.Disabled {
		return store.User{}, ErrUserDisabled
	}
	return user, nil
}

// UpdateUser 管人动作(仅 admin 调用), 带三条"别把自己锁死"的保护。
func (s *Service) UpdateUser(
	ctx context.Context, actorID, targetID int64, name, role *string, disabled *bool, password *string,
) (store.User, error) {
	target, err := s.users.GetUser(ctx, targetID)
	if err != nil {
		return store.User{}, err
	}

	self := actorID == targetID
	if self && (role != nil || (disabled != nil && *disabled)) {
		// 管理员把自己降级/停用之后, 若他是最后一个 admin, 系统就没人能管了。
		// 与其做复杂的"事后检测", 不如直接禁止 —— 需要换人时由另一个 admin 操作。
		return store.User{}, ErrSelfDemote
	}

	// 目标当前是"可用 admin", 且这次要把它变差(降级或停用)时, 必须留一个。
	if target.Role == store.RoleAdmin && !target.Disabled {
		demoting := role != nil && *role != store.RoleAdmin
		disabling := disabled != nil && *disabled
		if demoting || disabling {
			active, err := s.users.CountActiveAdmins(ctx)
			if err != nil {
				return store.User{}, err
			}
			if active <= 1 {
				return store.User{}, ErrLastAdmin
			}
		}
	}

	var hash *string
	if password != nil {
		hashed, err := HashPassword(*password)
		if err != nil {
			return store.User{}, err
		}
		hash = &hashed
	}
	return s.users.UpdateUser(ctx, targetID, name, role, disabled, hash)
}

// DeleteUser 删账号(仅 admin), 同样不能删自己、不能删掉最后一个可用 admin。
func (s *Service) DeleteUser(ctx context.Context, actorID, targetID int64) error {
	if actorID == targetID {
		return ErrSelfDemote
	}
	target, err := s.users.GetUser(ctx, targetID)
	if err != nil {
		return err
	}
	if target.Role == store.RoleAdmin && !target.Disabled {
		active, err := s.users.CountActiveAdmins(ctx)
		if err != nil {
			return err
		}
		if active <= 1 {
			return ErrLastAdmin
		}
	}
	return s.users.DeleteUser(ctx, targetID)
}

// ListUsers 列出账号(仅 admin)。
func (s *Service) ListUsers(ctx context.Context) ([]store.User, error) {
	return s.users.ListUsers(ctx)
}

// GetUser 取当前登录用户(用于 /auth/me)。
func (s *Service) GetUser(ctx context.Context, id int64) (store.User, error) {
	return s.users.GetUser(ctx, id)
}

// IsBootstrapNeeded 报告"系统里还没有任何账号"(前端据此显示"首次创建管理员")。
func (s *Service) IsBootstrapNeeded(ctx context.Context) (bool, error) {
	count, err := s.users.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}
