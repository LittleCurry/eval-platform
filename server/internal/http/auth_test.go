package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/auth"
	"eval-platform/server/internal/store"
)

// stubUsers 内存版用户存储(M7-1)。认证的 HTTP 测试不该依赖 PostgreSQL。
type stubUsers struct {
	nextID int64
	items  []store.User
}

func newStubUsers() *stubUsers {
	return &stubUsers{nextID: 1, items: []store.User{}}
}

func (s *stubUsers) CountUsers(context.Context) (int, error) { return len(s.items), nil }

func (s *stubUsers) CountActiveAdmins(context.Context) (int, error) {
	count := 0
	for _, item := range s.items {
		if item.Role == store.RoleAdmin && !item.Disabled {
			count++
		}
	}
	return count, nil
}

func (s *stubUsers) ListUsers(context.Context) ([]store.User, error) {
	return append([]store.User{}, s.items...), nil
}

func (s *stubUsers) GetUser(_ context.Context, id int64) (store.User, error) {
	for _, item := range s.items {
		if item.ID == id {
			return item, nil
		}
	}
	return store.User{}, store.ErrNotFound
}

func (s *stubUsers) GetUserByEmail(_ context.Context, email string) (store.User, error) {
	for _, item := range s.items {
		if item.Email == email {
			return item, nil
		}
	}
	return store.User{}, store.ErrNotFound
}

func (s *stubUsers) CreateUser(
	_ context.Context, email, name, passwordHash, role string,
) (store.User, error) {
	for _, item := range s.items {
		if item.Email == email {
			return store.User{}, store.ErrConflict
		}
	}
	user := store.User{ID: s.nextID, Email: email, Name: name, Role: role,
		PasswordHash: passwordHash, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	s.nextID++
	s.items = append(s.items, user)
	return user, nil
}

func (s *stubUsers) UpdateUser(
	_ context.Context, id int64, name, role *string, disabled *bool, passwordHash *string,
) (store.User, error) {
	for index, item := range s.items {
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
		s.items[index] = item
		return item, nil
	}
	return store.User{}, store.ErrNotFound
}

func (s *stubUsers) DeleteUser(_ context.Context, id int64) error {
	for index, item := range s.items {
		if item.ID == id {
			s.items = append(s.items[:index], s.items[index+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *stubUsers) TouchUserLogin(context.Context, int64) error { return nil }

const authTestSecret = "http-test-secret-with-enough-length-0123456789"

// authedRouter 构造一个**启用了鉴权**的 router: 空库 + 内存用户 + 真签发器。
func authedRouter(t *testing.T, runs RunStore, users *stubUsers) (http.Handler, *auth.Service) {
	t.Helper()
	issuer, err := auth.NewIssuer(authTestSecret, time.Hour)
	if err != nil {
		t.Fatalf("构造签发器失败: %v", err)
	}
	service := auth.NewService(users, issuer)
	gin.SetMode(gin.TestMode)
	// 各 store 都塞内存 stub: 权限测试要打到 handler 之后(403 必须在 handler 之前拦下,
	// 而"允许"的情况要继续走到业务逻辑, 所以不能留 nil 依赖)
	router := NewRouter(Deps{
		Postgres: fakePinger{}, Qdrant: fakePinger{},
		Projects: &stubProjectStore{}, Corpora: &stubCorpusStore{}, Documents: &stubDocumentStore{},
		Datasets: &stubDatasetStore{}, Cases: &stubCaseStore{},
		Runs: runs, Annotations: newStubAnnotations(), HumanGold: newStubHumanGold(),
		Auth: service,
	})
	return router, service
}

// seedUser 建一个指定角色的账号并返回可用 token(用与 service 相同的密钥签发)。
func seedUser(t *testing.T, users *stubUsers, service *auth.Service, email, role string) string {
	t.Helper()
	user, err := service.CreateUser(context.Background(), email, email, "correct-horse", role)
	if err != nil {
		t.Fatalf("建账号 %s 失败: %v", email, err)
	}
	issuer, err := auth.NewIssuer(authTestSecret, time.Hour)
	if err != nil {
		t.Fatalf("构造签发器失败: %v", err)
	}
	signed, _, err := issuer.Sign(user.ID, user.Email, user.Role)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	return signed
}

// doAuthed 带 Bearer token 发请求(与 doJSON 的区别只有这一个头)。
func doAuthed(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Buffer
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	var req *http.Request
	if reader != nil {
		req = httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// bodyOf / codeOf 让断言读起来短一点(httptest.Recorder 的字段名较长)。
func bodyOf(w *httptest.ResponseRecorder) string { return w.Body.String() }

// ---- 认证流程 ----

func TestAuthFlowThroughHTTP(t *testing.T) {
	users := newStubUsers()
	router, _ := authedRouter(t, &stubRunStore{}, users)

	// 空库: 前端据此显示"首次创建管理员"
	w := doAuthed(t, router, http.MethodGet, "/api/v1/auth/status", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status 应 200: %d (%s)", w.Code, bodyOf(w))
	}
	if !strings.Contains(bodyOf(w), `"bootstrap_needed":true`) {
		t.Fatalf("空库应报告需要初始化: %s", bodyOf(w))
	}

	// 首次注册 -> admin + token
	w = doAuthed(t, router, http.MethodPost, "/api/v1/auth/register", "",
		`{"email":"Admin@Example.com","name":"管理员","password":"correct-horse"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("首次注册应 201: %d (%s)", w.Code, bodyOf(w))
	}
	var registered struct {
		Token string     `json:"token"`
		User  store.User `json:"user"`
	}
	if err := json.Unmarshal([]byte(bodyOf(w)), &registered); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if registered.User.Role != store.RoleAdmin || registered.Token == "" {
		t.Fatalf("首个用户应为 admin 且直接拿到 token: %+v", registered)
	}
	if strings.Contains(bodyOf(w), "password_hash") || strings.Contains(bodyOf(w), "$2a$") {
		t.Fatal("响应里绝不能出现口令哈希")
	}

	// 再次注册 -> 关闭
	if w := doAuthed(t, router, http.MethodPost, "/api/v1/auth/register", "",
		`{"email":"x@y.com","name":"x","password":"correct-horse"}`); w.Code != http.StatusConflict {
		t.Fatalf("有账号后注册应 409: %d (%s)", w.Code, bodyOf(w))
	}

	// 带 token 访问 /auth/me
	w = doAuthed(t, router, http.MethodGet, "/api/v1/auth/me", registered.Token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("me 应 200: %d (%s)", w.Code, bodyOf(w))
	}
	if !strings.Contains(bodyOf(w), `"admin"`) || !strings.Contains(bodyOf(w), `"actions"`) {
		t.Fatalf("me 应返回角色与能力清单: %s", bodyOf(w))
	}

	// 不带 token -> 401
	if w := doAuthed(t, router, http.MethodGet, "/api/v1/auth/me", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401: %d", w.Code)
	}
	// 伪造 token -> 401
	if w := doAuthed(t, router, http.MethodGet, "/api/v1/auth/me", "not-a-token", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("伪造 token 应 401: %d", w.Code)
	}

	// 登录: 正确 / 错误口令
	if w := doAuthed(t, router, http.MethodPost, "/api/v1/auth/login", "",
		`{"email":"admin@example.com","password":"correct-horse"}`); w.Code != http.StatusOK {
		t.Fatalf("登录应 200: %d (%s)", w.Code, bodyOf(w))
	}
	if w := doAuthed(t, router, http.MethodPost, "/api/v1/auth/login", "",
		`{"email":"admin@example.com","password":"nope-nope-nope"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("错口令应 401: %d", w.Code)
	}
	// 不存在的账号与错口令回答一致(否则接口成了账号探测器)
	wrongUser := doAuthed(t, router, http.MethodPost, "/api/v1/auth/login", "",
		`{"email":"nobody@example.com","password":"nope-nope-nope"}`)
	wrongPass := doAuthed(t, router, http.MethodPost, "/api/v1/auth/login", "",
		`{"email":"admin@example.com","password":"nope-nope-nope"}`)
	if bodyOf(wrongUser) != bodyOf(wrongPass) {
		t.Fatalf("账号不存在与口令错误的响应必须一致: %s vs %s", bodyOf(wrongUser), bodyOf(wrongPass))
	}

	// 登出: 明确说明"客户端丢弃 token", 不假装撤销
	w = doAuthed(t, router, http.MethodPost, "/api/v1/auth/logout", registered.Token, "")
	if w.Code != http.StatusOK || !strings.Contains(bodyOf(w), "丢弃") {
		t.Fatalf("登出应说清语义: %d (%s)", w.Code, bodyOf(w))
	}
}

// TestEveryAPIRouteRequiresToken 新增端点若忘了挂鉴权, 这个测试会红。
//
// 做法: 把路由表里每个 /api/v1 端点(公开的三个除外)在"不带 token"时打一遍,
// 必须都是 401。没有中间件的端点会落到 handler 上, 于是给出 400/404/500 —— 一眼可辨。
func TestEveryAPIRouteRequiresToken(t *testing.T) {
	users := newStubUsers()
	router, _ := authedRouter(t, &stubRunStore{}, users)

	public := map[string]bool{
		"GET /api/v1/auth/status":    true,
		"POST /api/v1/auth/login":    true,
		"POST /api/v1/auth/register": true,
		"GET /healthz":               true,
		"GET /":                      true,
	}

	engine, ok := router.(*gin.Engine)
	if !ok {
		t.Fatal("router 不是 *gin.Engine")
	}
	checked := 0
	for _, route := range engine.Routes() {
		key := route.Method + " " + route.Path
		if public[key] {
			continue
		}
		if !strings.HasPrefix(route.Path, "/api/v1/") {
			t.Fatalf("出现未分类的非 /api/v1 路由: %s", key)
		}
		// 路径参数换成具体值, 否则 gin 匹配不上(:id / :case_id 都换成 1)
		path := route.Path
		for _, segment := range strings.Split(path, "/") {
			if strings.HasPrefix(segment, ":") {
				path = strings.Replace(path, "/"+segment, "/1", 1)
			}
		}
		w := doAuthed(t, router, route.Method, path, "", "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s 未受保护: 无 token 时返回 %d(%s)", key, w.Code, bodyOf(w))
		}
		checked++
	}
	if checked < 30 {
		t.Fatalf("受保护路由数量异常(%d): 路由表可能被改坏了", checked)
	}
}

// TestRoleMatrixThroughHTTP 三角色在真实路由上的表现: viewer 只能读, editor 能写能跑,
// admin 才能删数据与管人。
func TestRoleMatrixThroughHTTP(t *testing.T) {
	users := newStubUsers()
	router, service := authedRouter(t, &stubRunStore{}, users)

	viewerToken := seedUser(t, users, service, "viewer@example.com", store.RoleViewer)
	editorToken := seedUser(t, users, service, "editor@example.com", store.RoleEditor)
	adminToken := seedUser(t, users, service, "admin@example.com", store.RoleAdmin)

	cases := []struct {
		name      string
		method    string
		path      string
		body      string
		token     string
		wantCode  int
		wantOther bool // true = 只要求"不是 403"(handler 可能因其它原因返回 4xx)
	}{
		{"viewer 能看报告", http.MethodGet, "/api/v1/runs", "", viewerToken, http.StatusOK, false},
		{"viewer 能看闭环", http.MethodGet, "/api/v1/closure?baseline=1&candidate=2", "", viewerToken, http.StatusOK, false},
		{"viewer 不能提交实验", http.MethodPost, "/api/v1/runs", `{"dataset_id":4,"corpus_id":4}`, viewerToken, http.StatusForbidden, false},
		{"viewer 不能打标", http.MethodPost, "/api/v1/annotations", `{"run_id":1,"case_id":1}`, viewerToken, http.StatusForbidden, false},
		{"viewer 不能打分", http.MethodPost, "/api/v1/human-gold", `{"run_id":1,"case_id":1,"annotator":"me","verdict":"faithful"}`, viewerToken, http.StatusForbidden, false},
		{"viewer 不能删数据集", http.MethodDelete, "/api/v1/datasets/1", "", viewerToken, http.StatusForbidden, false},
		{"viewer 不能加账号", http.MethodPost, "/api/v1/users", `{"email":"a@b.com","password":"correct-horse"}`, viewerToken, http.StatusForbidden, false},
		{"viewer 不能看账号列表", http.MethodGet, "/api/v1/users", "", viewerToken, http.StatusForbidden, false},

		{"editor 能打标", http.MethodPost, "/api/v1/annotations", `{"run_id":1,"case_id":1,"reason":"retrieval"}`, editorToken, http.StatusCreated, true},
		{"editor 能打分", http.MethodPost, "/api/v1/human-gold", `{"run_id":1,"case_id":1,"annotator":"me","verdict":"faithful"}`, editorToken, http.StatusOK, true},
		{"editor 能改配置模板", http.MethodPost, "/api/v1/pipeline-preview", `{}`, editorToken, http.StatusOK, true},
		{"editor 不能删数据集", http.MethodDelete, "/api/v1/datasets/1", "", editorToken, http.StatusForbidden, false},
		{"editor 不能加账号", http.MethodPost, "/api/v1/users", `{"email":"a@b.com","password":"correct-horse"}`, editorToken, http.StatusForbidden, false},

		{"admin 能看账号列表", http.MethodGet, "/api/v1/users", "", adminToken, http.StatusOK, false},
		{"admin 能加账号", http.MethodPost, "/api/v1/users", `{"email":"new@example.com","name":"新人","password":"correct-horse","role":"viewer"}`, adminToken, http.StatusCreated, false},
		{"admin 能删数据集", http.MethodDelete, "/api/v1/datasets/1", "", adminToken, http.StatusNoContent, false},
	}

	for _, item := range cases {
		w := doAuthed(t, router, item.method, item.path, item.token, item.body)
		if item.wantOther {
			if w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
				t.Fatalf("%s: 不该被权限拦住, 实际 %d (%s)", item.name, w.Code, bodyOf(w))
			}
			continue
		}
		if w.Code != item.wantCode {
			t.Fatalf("%s: code = %d, want %d (%s)", item.name, w.Code, item.wantCode, bodyOf(w))
		}
	}
}

// TestAdminCannotLockHimselfOut 通过 HTTP 验证"别把自己锁死"的保护(C6 的规则在真实路由上生效)。
func TestAdminCannotLockHimselfOut(t *testing.T) {
	users := newStubUsers()
	router, service := authedRouter(t, &stubRunStore{}, users)
	adminToken := seedUser(t, users, service, "admin@example.com", store.RoleAdmin)

	// 只有一个 admin 时: 让另一个 admin(此处不存在)去降级它 -> 409
	viewerToken := seedUser(t, users, service, "viewer@example.com", store.RoleViewer)
	w := doAuthed(t, router, http.MethodPatch, "/api/v1/users/1", viewerToken, `{"role":"editor"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer 改角色应 403(先被权限矩阵拦住): %d", w.Code)
	}

	// 自己改自己 -> 409
	w = doAuthed(t, router, http.MethodPatch, "/api/v1/users/1", adminToken, `{"role":"viewer"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("不能给自己降级: %d (%s)", w.Code, bodyOf(w))
	}
	// 删自己 -> 409
	w = doAuthed(t, router, http.MethodDelete, "/api/v1/users/1", adminToken, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("不能删自己: %d (%s)", w.Code, bodyOf(w))
	}
	// 口令太短 -> 400
	w = doAuthed(t, router, http.MethodPost, "/api/v1/users", adminToken,
		`{"email":"weak@example.com","password":"short"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("弱口令应 400: %d (%s)", w.Code, bodyOf(w))
	}
	// 默认角色是最小权限(加人时忘了选角色不该变成给管理员)
	w = doAuthed(t, router, http.MethodPost, "/api/v1/users", adminToken,
		`{"email":"default-role@example.com","password":"correct-horse"}`)
	if w.Code != http.StatusCreated || !strings.Contains(bodyOf(w), `"role":"viewer"`) {
		t.Fatalf("未指定角色时应默认 viewer: %d (%s)", w.Code, bodyOf(w))
	}
}
