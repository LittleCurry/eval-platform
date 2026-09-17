package http

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/auth"
	"eval-platform/server/internal/store"
)

// authFixture 自带内存用户表与签发器: 隔离测试既要"带真 token", 又不想依赖 PostgreSQL。
type authFixture struct {
	users   *stubUsers
	service *auth.Service
	issuer  *auth.Issuer
	nextID  int64
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	issuer, err := auth.NewIssuer(authTestSecret, time.Hour)
	if err != nil {
		t.Fatalf("构造签发器失败: %v", err)
	}
	users := newStubUsers()
	return &authFixture{users: users, service: auth.NewService(users, issuer), issuer: issuer}
}

// seed 建一个指定角色的账号并返回可用 token。
func (f *authFixture) seed(t *testing.T, email, role string) string {
	t.Helper()
	f.nextID++
	id := f.nextID
	f.users.items = append(f.users.items, store.User{
		ID: id, Email: email, Name: email, Role: role,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	token, _, err := f.issuer.Sign(id, email, role)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	return token
}

var _ = context.Background

// M7-2 的隔离规则测试。
//
// 决策背景(D24): **不建成员表, 全员可见** —— 所以这里校验的不是"能不能看见",
// 而是两条别的东西:
//   1. **归属留痕**: 谁建的项目/数据集/语料要落库, 出事找得到人;
//   2. **项目内一致性**: 一次 run 的数据集与语料必须同属一个项目 ——
//      跨项目混用是"静默错误": 指标照样算得出来, 但那是两个项目的数拼出来的。

// projectRouter 建一个"启用了鉴权 + 可断言 stub"的 router(M7-2 用)。
func projectRouter(t *testing.T, projects ProjectStore, runs RunStore) (http.Handler, *authFixture) {
	t.Helper()
	fixture := newAuthFixture(t)
	gin.SetMode(gin.TestMode)
	router := NewRouter(Deps{
		Postgres: fakePinger{}, Qdrant: fakePinger{},
		Projects: projects, Corpora: &stubCorpusStore{}, Documents: &stubDocumentStore{},
		Datasets: &stubDatasetStore{}, Cases: &stubCaseStore{},
		Runs: runs, Annotations: newStubAnnotations(), HumanGold: newStubHumanGold(),
		Auth: fixture.service,
	})
	return router, fixture
}

// ---- 归属: 创建时记下当前登录用户 ----

func TestCreateProjectRecordsOwner(t *testing.T) {
	projects := &stubProjectStore{}
	router, fixture := projectRouter(t, projects, &stubRunStore{})
	token := fixture.seed(t, "editor@example.com", store.RoleEditor)

	// 注: /projects 是 admin 动作, 用 admin token 更贴实际; 这里换 admin
	adminToken := fixture.seed(t, "admin@example.com", store.RoleAdmin)
	w := doAuthed(t, router, http.MethodPost, "/api/v1/projects", adminToken,
		`{"name":"客服知识库","description":"第一版"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("建项目应 201: %d (%s)", w.Code, bodyOf(w))
	}
	if projects.gotCreatedBy == 0 {
		t.Fatal("创建者必须落库(全员可见的项目更要能追到是谁建的)")
	}
	_ = token

	// viewer 建项目 -> 403(权限矩阵在 M7-1 已定)
	viewerToken := fixture.seed(t, "viewer@example.com", store.RoleViewer)
	if w := doAuthed(t, router, http.MethodPost, "/api/v1/projects", viewerToken,
		`{"name":"x"}`); w.Code != http.StatusForbidden {
		t.Fatalf("viewer 建项目应 403: %d", w.Code)
	}
}

// ---- 跨项目: 一次 run 的数据集与语料必须同项目 ----

func TestSubmitRunRejectsCrossProject(t *testing.T) {
	stub := &stubRunStore{createErr: store.ErrProjectMismatch}
	router, fixture := projectRouter(t, &stubProjectStore{}, stub)
	token := fixture.seed(t, "editor@example.com", store.RoleEditor)

	w := doAuthed(t, router, http.MethodPost, "/api/v1/runs", token,
		`{"dataset_id":4,"corpus_id":9,"top_k":5}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("跨项目混用应 400: %d (%s)", w.Code, bodyOf(w))
	}
	// 错误信息要说清"错在哪、为什么严重", 而不是只丢一句"参数错误"
	message := bodyOf(w)
	for _, want := range []string{"不属于同一个项目", "一个项目内"} {
		if !strings.Contains(message, want) {
			t.Fatalf("错误信息应包含 %q: %s", want, message)
		}
	}
}

// TestSubmitRunProjectMismatchWrapped 包了一层上下文之后仍要认出是同一条规则
// (errors.Is 穿透 wrap): 否则"存储层加了点上下文"就会让 400 变成 500。
func TestSubmitRunProjectMismatchWrapped(t *testing.T) {
	wrapped := fmt.Errorf("create run: %w", store.ErrProjectMismatch)
	stub := &stubRunStore{createErr: wrapped}
	router, fixture := projectRouter(t, &stubProjectStore{}, stub)
	token := fixture.seed(t, "editor@example.com", store.RoleEditor)

	w := doAuthed(t, router, http.MethodPost, "/api/v1/runs", token,
		`{"dataset_id":4,"corpus_id":9}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("包了一层的领域错误仍应映射成 400: %d (%s)", w.Code, bodyOf(w))
	}
}

// ---- 项目详情: 前端切换器需要明确的 404, 而不是悄悄回退 ----

func TestGetProjectNotFound(t *testing.T) {
	router, fixture := projectRouter(t, &stubProjectStore{}, &stubRunStore{})
	token := fixture.seed(t, "viewer@example.com", store.RoleViewer)

	w := doAuthed(t, router, http.MethodGet, "/api/v1/projects/999", token, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("项目不存在应 404: %d (%s)", w.Code, bodyOf(w))
	}
	// 参数非法是 400
	if w := doAuthed(t, router, http.MethodGet, "/api/v1/projects/abc", token, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("非法 id 应 400: %d", w.Code)
	}
}

func TestListProjectsIsReadableByViewer(t *testing.T) {
	// 全员可见(D24): 只读账号也要能拿到项目列表(否则前端连选项目都做不到)
	router, fixture := projectRouter(t, &stubProjectStore{}, &stubRunStore{})
	token := fixture.seed(t, "viewer@example.com", store.RoleViewer)

	w := doAuthed(t, router, http.MethodGet, "/api/v1/projects", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("viewer 应能读项目列表: %d (%s)", w.Code, bodyOf(w))
	}
}
