package http

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/auth"
	"eval-platform/server/internal/store"
)

// userHandler 用户管理(M7-1, 仅 admin)。
//
// 三条"别把自己锁死"的保护放在 service 层(有测试): 不能改自己的角色/停用自己、
// 不能删自己、不能把最后一个可用 admin 降级或停用。这里只负责把错误翻成人话。
type userHandler struct {
	service *auth.Service
}

func newUserHandler(service *auth.Service) *userHandler {
	return &userHandler{service: service}
}

// List GET /users
func (h *userHandler) List(c *gin.Context) {
	items, err := h.service.ListUsers(c.Request.Context())
	if err != nil {
		log.Printf("list users: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, items)
}

type createUserReq struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// Create POST /users
func (h *userHandler) Create(c *gin.Context) {
	var req createUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = store.RoleViewer // 默认给最小权限: 加人时"忘了选角色"不该变成"给了管理员"
	}
	user, err := h.service.CreateUser(c.Request.Context(), req.Email, req.Name, req.Password, role)
	if err != nil {
		writeUserError(c, err, "创建失败")
		return
	}
	writeJSON(c, http.StatusCreated, user)
}

type updateUserReq struct {
	Name     *string `json:"name"`
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
	Password *string `json:"password"`
}

// Update PATCH /users/:id
//
// 指针语义(与 M6 一致): 没传 = 不改。密码是重置入口(传了才改)。
func (h *userHandler) Update(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req updateUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.Role != nil && !auth.ValidRole(*req.Role) {
		writeErr(c, http.StatusBadRequest, "role 必须是 admin/editor/viewer")
		return
	}

	actor, _ := auth.CurrentUser(c)
	user, err := h.service.UpdateUser(c.Request.Context(), actor.ID, id,
		req.Name, req.Role, req.Disabled, req.Password)
	if err != nil {
		writeUserError(c, err, "更新失败")
		return
	}
	writeJSON(c, http.StatusOK, user)
}

// Delete DELETE /users/:id
func (h *userHandler) Delete(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	actor, _ := auth.CurrentUser(c)
	if err := h.service.DeleteUser(c.Request.Context(), actor.ID, id); err != nil {
		writeUserError(c, err, "删除失败")
		return
	}
	c.Status(http.StatusNoContent)
}

// writeUserError 把 service 的错误翻成状态码 + 人话。
//
// 400 vs 409 的分界: 请求本身不合法(口令太短/角色非法)是 400;
// 请求合法但与当前系统状态冲突(最后一个管理员、自己改自己)是 409 ——
// 前端对这两类该说的话不一样。
func writeUserError(c *gin.Context, err error, fallback string) {
	switch {
	case errors.Is(err, auth.ErrLastAdmin):
		writeErr(c, http.StatusConflict, "这是最后一个可用的管理员: 先加一个管理员再操作, 否则系统没人管了")
	case errors.Is(err, auth.ErrSelfDemote):
		writeErr(c, http.StatusConflict, "不能修改自己的角色/停用或删除自己: 请让另一个管理员操作")
	case errors.Is(err, store.ErrNotFound):
		writeErr(c, http.StatusNotFound, "账号不存在")
	case errors.Is(err, store.ErrConflict):
		writeErr(c, http.StatusConflict, "该 email 已被使用")
	case errors.Is(err, auth.ErrPasswordTooShort):
		writeErr(c, http.StatusBadRequest, "口令至少 8 位")
	case errors.Is(err, auth.ErrPasswordTooLong):
		writeErr(c, http.StatusBadRequest, "口令不能超过 72 字节(bcrypt 上限, 更长会被静默截断)")
	default:
		if strings.Contains(err.Error(), "email") || strings.Contains(err.Error(), "角色") {
			writeErr(c, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("user op: %v", err)
		writeErr(c, http.StatusInternalServerError, fallback)
	}
}
