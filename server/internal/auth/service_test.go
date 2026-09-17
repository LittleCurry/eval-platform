package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"eval-platform/server/internal/store"
)

// memUsers 内存版用户存储: 认证逻辑的测试不该依赖 PostgreSQL。
type memUsers struct {
	mu     sync.Mutex
	nextID int64
	items  []store.User
}

func newMemUsers() *memUsers {
	return &memUsers{nextID: 1, items: []store.User{}}
}

func (m *memUsers) CountUsers(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items), nil
}

func (m *memUsers) CountActiveAdmins(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, item := range m.items {
		if item.Role == store.RoleAdmin && !item.Disabled {
			count++
		}
	}
	return count, nil
}

func (m *memUsers) ListUsers(_ context.Context) ([]store.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.User{}, m.items...), nil
}

func (m *memUsers) GetUser(_ context.Context, id int64) (store.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range m.items {
		if item.ID == id {
			return item, nil
		}
	}
	return store.User{}, store.ErrNotFound
}

func (m *memUsers) GetUserByEmail(_ context.Context, email string) (store.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range m.items {
		if item.Email == email {
			return item, nil
		}
	}
	return store.User{}, store.ErrNotFound
}

func (m *memUsers) CreateUser(
	_ context.Context, email, name, passwordHash, role string,
) (store.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range m.items {
		if item.Email == email {
			return store.User{}, store.ErrConflict
		}
	}
	user := store.User{
		ID: m.nextID, Email: email, Name: name, Role: role,
		PasswordHash: passwordHash, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	m.nextID++
	m.items = append(m.items, user)
	return user, nil
}

func (m *memUsers) UpdateUser(
	_ context.Context, id int64, name, role *string, disabled *bool, passwordHash *string,
) (store.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index, item := range m.items {
		if item.ID != id {
			continue
		}
		if name != nil {
			item.Name = *name
		}
		if role != nil {
			item.Role = *role
		}
		if disabled != nil {
			item.Disabled = *disabled
		}
		if passwordHash != nil {
			item.PasswordHash = *passwordHash
		}
		item.UpdatedAt = time.Now()
		m.items[index] = item
		return item, nil
	}
	return store.User{}, store.ErrNotFound
}

func (m *memUsers) DeleteUser(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index, item := range m.items {
		if item.ID == id {
			m.items = append(m.items[:index], m.items[index+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *memUsers) TouchUserLogin(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for index, item := range m.items {
		if item.ID == id {
			item.LastLoginAt = &now
			m.items[index] = item
			return nil
		}
	}
	return store.ErrNotFound
}

// 测试用密钥: 长度足够(>=32 字节), 但显然不是生产密钥。
const testSecret = "test-secret-for-unit-tests-0123456789abcdef"

func newTestService(t *testing.T, users *memUsers) *Service {
	t.Helper()
	issuer, err := NewIssuer(testSecret, time.Hour)
	if err != nil {
		t.Fatalf("构造签发器失败: %v", err)
	}
	return NewService(users, issuer)
}

// ---- 首用户 bootstrap ----

func TestFirstUserBecomesAdminThenRegistrationCloses(t *testing.T) {
	users := newMemUsers()
	service := newTestService(t, users)
	ctx := context.Background()

	needed, err := service.IsBootstrapNeeded(ctx)
	if err != nil || !needed {
		t.Fatalf("空库应需要 bootstrap: needed=%v err=%v", needed, err)
	}

	result, err := service.Register(ctx, "  Admin@Example.COM ", "管理员", "correct-horse")
	if err != nil {
		t.Fatalf("首个注册应成功: %v", err)
	}
	if result.User.Role != store.RoleAdmin {
		t.Fatalf("首个用户必须是 admin(否则没人能进去): %s", result.User.Role)
	}
	if result.User.Email != "admin@example.com" {
		t.Fatalf("email 应归一化为小写去空格: %q", result.User.Email)
	}
	if result.Token == "" || result.ExpiresAt.Before(time.Now()) {
		t.Fatalf("注册应直接签发可用 token: %+v", result)
	}
	if LooksHashed(result.User.PasswordHash) == false {
		t.Fatalf("口令必须以哈希形式落库: %q", result.User.PasswordHash)
	}

	if _, err := service.Register(ctx, "second@example.com", "第二个人", "correct-horse"); !errors.Is(err, ErrRegistrationClosed) {
		t.Fatalf("有账号后注册必须关闭(加人走 /users): %v", err)
	}
}

func TestRegisterRejectsWeakInput(t *testing.T) {
	users := newMemUsers()
	service := newTestService(t, users)
	ctx := context.Background()

	cases := []struct {
		name     string
		email    string
		password string
	}{
		{"口令太短", "a@b.com", "short"},
		{"口令超 72 字节", "a@b.com", string(make([]byte, 73))},
		{"email 不合法", "not-an-email", "correct-horse"},
		{"email 为空", "", "correct-horse"},
	}
	for _, item := range cases {
		if _, err := service.Register(ctx, item.email, "x", item.password); err == nil {
			t.Fatalf("%s: 应被拒绝", item.name)
		}
	}
	if count, _ := users.CountUsers(ctx); count != 0 {
		t.Fatalf("被拒绝的注册不该留下账号: %d", count)
	}
}

// ---- 登录 ----

func TestLoginSuccessAndFailure(t *testing.T) {
	users := newMemUsers()
	service := newTestService(t, users)
	ctx := context.Background()
	if _, err := service.Register(ctx, "admin@example.com", "管理员", "correct-horse"); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	result, err := service.Login(ctx, "ADMIN@example.com", "correct-horse")
	if err != nil {
		t.Fatalf("大小写不同的 email 也应能登录: %v", err)
	}
	if result.User.ID != 1 {
		t.Fatalf("应返回正确账号: %+v", result.User)
	}

	if _, err := service.Login(ctx, "admin@example.com", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("错口令应报凭证错误: %v", err)
	}
	// 账号不存在与口令错误必须是同一个错误, 否则接口变成"账号是否存在"的探测器
	_, notFoundErr := service.Login(ctx, "nobody@example.com", "correct-horse")
	if !errors.Is(notFoundErr, ErrInvalidCredentials) {
		t.Fatalf("账号不存在也应报同一个凭证错误: %v", err)
	}
}

func TestLoginRejectsDisabledAccount(t *testing.T) {
	users := newMemUsers()
	service := newTestService(t, users)
	ctx := context.Background()
	created, err := service.Register(ctx, "admin@example.com", "管理员", "correct-horse")
	if err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	disabled := true
	if _, err := users.UpdateUser(ctx, created.User.ID, nil, nil, &disabled, nil); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if _, err := service.Login(ctx, "admin@example.com", "correct-horse"); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("停用账号应被拒绝并说明原因: %v", err)
	}
}

// ---- 每次请求都回库确认(停用要立即生效) ----

func TestAuthenticateReflectsCurrentState(t *testing.T) {
	users := newMemUsers()
	service := newTestService(t, users)
	ctx := context.Background()
	created, err := service.Register(ctx, "admin@example.com", "管理员", "correct-horse")
	if err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	if _, err := service.Authenticate(ctx, created.Token); err != nil {
		t.Fatalf("有效 token 应通过: %v", err)
	}

	// 停用后同一个 token 必须立刻失效 —— 这正是"每次请求回库"存在的理由
	disabled := true
	if _, err := users.UpdateUser(ctx, created.User.ID, nil, nil, &disabled, nil); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if _, err := service.Authenticate(ctx, created.Token); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("停用后旧 token 必须立即失效: %v", err)
	}

	// 账号被删同理
	disabled = false
	_, _ = users.UpdateUser(ctx, created.User.ID, nil, nil, &disabled, nil)
	if err := users.DeleteUser(ctx, created.User.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := service.Authenticate(ctx, created.Token); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("账号被删后 token 应无效: %v", err)
	}
}

// ---- "别把自己锁死" 的三条保护 ----

func TestUpdateUserProtections(t *testing.T) {
	users := newMemUsers()
	service := newTestService(t, users)
	ctx := context.Background()

	// 首个注册者 = admin1; 再由 admin1 建 admin2(注意 CreateUser 返回的是 store.User)
	first, err := service.Register(ctx, "admin1@example.com", "管理员一", "correct-horse")
	if err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	second, err := service.CreateUser(ctx, "admin2@example.com", "管理员二", "correct-horse", store.RoleAdmin)
	if err != nil {
		t.Fatalf("建第二个 admin 失败: %v", err)
	}

	// 1) 不能改自己的角色
	editorRole := store.RoleEditor
	if _, err := service.UpdateUser(ctx, first.User.ID, first.User.ID, nil, &editorRole, nil, nil); !errors.Is(err, ErrSelfDemote) {
		t.Fatalf("不能给自己降级: %v", err)
	}
	// 2) 不能停用自己
	self := true
	if _, err := service.UpdateUser(ctx, first.User.ID, first.User.ID, nil, nil, &self, nil); !errors.Is(err, ErrSelfDemote) {
		t.Fatalf("不能停用自己: %v", err)
	}
	// 3) 不能删自己
	if err := service.DeleteUser(ctx, first.User.ID, first.User.ID); !errors.Is(err, ErrSelfDemote) {
		t.Fatalf("不能删自己: %v", err)
	}

	// 4) 两个可用 admin 时: 把对方降级是允许的(这正是"换人"的正常路径)
	if _, err := service.UpdateUser(ctx, first.User.ID, second.ID, nil, &editorRole, nil, nil); err != nil {
		t.Fatalf("两个 admin 时降级其中一个应允许: %v", err)
	}
	// 5) 恢复成 admin, 再删掉 —— 此刻系统里只剩 admin1 一个可用 admin
	adminRole := store.RoleAdmin
	if _, err := service.UpdateUser(ctx, first.User.ID, second.ID, nil, &adminRole, nil, nil); err != nil {
		t.Fatalf("恢复 admin 失败: %v", err)
	}
	if err := service.DeleteUser(ctx, first.User.ID, second.ID); err != nil {
		t.Fatalf("两个 admin 时删一个应允许: %v", err)
	}

	// 6) 最后一个可用 admin 不能被降级/删除(换一个非本人的 actor, 否则会被自我保护先挡住)
	viewer, err := service.CreateUser(ctx, "viewer@example.com", "只读", "correct-horse", store.RoleViewer)
	if err != nil {
		t.Fatalf("建 viewer 失败: %v", err)
	}
	if _, err := service.UpdateUser(ctx, viewer.ID, first.User.ID, nil, &editorRole, nil, nil); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("不能降级最后一个可用 admin: %v", err)
	}
	disabled := true
	if _, err := service.UpdateUser(ctx, viewer.ID, first.User.ID, nil, nil, &disabled, nil); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("不能停用最后一个可用 admin: %v", err)
	}
	if err := service.DeleteUser(ctx, viewer.ID, first.User.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("不能删除最后一个可用 admin: %v", err)
	}

	// 7) 但"两个 admin, 其中一个已停用"时仍然只算一个可用 —— 停用的那个可以随便处理
	if _, err := service.UpdateUser(ctx, first.User.ID, viewer.ID, nil, &adminRole, nil, nil); err != nil {
		t.Fatalf("把 viewer 提成 admin 失败: %v", err)
	}
	if _, err := service.UpdateUser(ctx, viewer.ID, first.User.ID, nil, &editorRole, nil, nil); err != nil {
		t.Fatalf("有第二个可用 admin 后应允许降级前者: %v", err)
	}
}

func TestCreateUserValidatesRole(t *testing.T) {
	users := newMemUsers()
	service := newTestService(t, users)
	if _, err := service.CreateUser(context.Background(), "x@y.com", "x", "correct-horse", "superuser"); err == nil {
		t.Fatal("非法角色应被拒绝")
	}
}

func TestUpdateUserResetsPassword(t *testing.T) {
	users := newMemUsers()
	service := newTestService(t, users)
	ctx := context.Background()
	if _, err := service.Register(ctx, "admin@example.com", "管理员", "correct-horse"); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	other, err := service.CreateUser(ctx, "ops@example.com", "运维", "correct-horse", store.RoleEditor)
	if err != nil {
		t.Fatalf("建账号失败: %v", err)
	}

	newPassword := "brand-new-password"
	admin, _ := service.GetUser(ctx, 1)
	if _, err := service.UpdateUser(ctx, admin.ID, other.ID, nil, nil, nil, &newPassword); err != nil {
		t.Fatalf("重置口令失败: %v", err)
	}
	if _, err := service.Login(ctx, "ops@example.com", "brand-new-password"); err != nil {
		t.Fatalf("新口令应能登录: %v", err)
	}
	if _, err := service.Login(ctx, "ops@example.com", "correct-horse"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("旧口令应失效: %v", err)
	}
}
