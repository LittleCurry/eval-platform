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

// authHandler 提供登录/注册/当前用户(M7-1)。
//
// 端点划分:
//   - POST /auth/register —— **只在系统还没有任何账号时可用**(首个用户自动成为 admin),
//     之后返回 409; 加人走 /users(admin)。
//     为什么不做自助注册: 这是内部工具, 谁能进来应该由管理员决定, 而不是"谁先看到地址谁进"。
//   - POST /auth/login    —— 公开。
//   - GET  /auth/me       —— 需要 token(前端刷新页面时用它确认会话还有效)。
//   - POST /auth/logout   —— 无状态 JWT 没有服务端会话可销, 所以它只回 204 并说明
//     客户端该丢弃 token; 真正的"立刻断开"用停用账号(每次请求都回库校验)。
type authHandler struct {
	service *auth.Service
}

func newAuthHandler(service *auth.Service) *authHandler {
	return &authHandler{service: service}
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login POST /auth/login
func (h *authHandler) Login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(req.Email) == "" || req.Password == "" {
		writeErr(c, http.StatusBadRequest, "email 与 password 必填")
		return
	}
	result, err := h.service.Login(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			// 401 而不是 404/400: 账号不存在与口令错误必须是同一个回答
			writeErr(c, http.StatusUnauthorized, "账号或口令不正确")
		case errors.Is(err, auth.ErrUserDisabled):
			writeErr(c, http.StatusForbidden, "账号已停用, 请联系管理员")
		default:
			log.Printf("login: %v", err)
			writeErr(c, http.StatusInternalServerError, "登录失败")
		}
		return
	}
	writeJSON(c, http.StatusOK, result)
}

type registerReq struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// Register POST /auth/register —— 仅用于"首次创建管理员"。
func (h *authHandler) Register(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	result, err := h.service.Register(c.Request.Context(), req.Email, req.Name, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrRegistrationClosed):
			writeErr(c, http.StatusConflict,
				"系统已有账号: 注册已关闭, 请让管理员在「用户管理」里添加")
		case errors.Is(err, auth.ErrPasswordTooShort):
			writeErr(c, http.StatusBadRequest, "口令至少 8 位")
		case errors.Is(err, auth.ErrPasswordTooLong):
			writeErr(c, http.StatusBadRequest, "口令不能超过 72 字节(bcrypt 上限, 更长会被静默截断)")
		case errors.Is(err, store.ErrConflict):
			writeErr(c, http.StatusConflict, "该 email 已被使用")
		default:
			if strings.Contains(err.Error(), "email") {
				writeErr(c, http.StatusBadRequest, err.Error())
				return
			}
			log.Printf("register: %v", err)
			writeErr(c, http.StatusInternalServerError, "注册失败")
		}
		return
	}
	writeJSON(c, http.StatusCreated, result)
}

// Me GET /auth/me
//
// 返回 token 对应账号的**当前**角色(回库取, 不是 token 里的旧角色)——
// 管理员刚把某人降级后, 前端刷新一次就该看到正确的能力。
func (h *authHandler) Me(c *gin.Context) {
	user, ok := auth.CurrentUser(c)
	if !ok {
		writeErr(c, http.StatusUnauthorized, "需要登录")
		return
	}
	writeJSON(c, http.StatusOK, gin.H{
		"user":    user,
		"actions": auth.ActionsOf(user.Role),
	})
}

// Status GET /auth/status —— 公开: 前端据此决定显示"登录"还是"首次创建管理员"。
func (h *authHandler) Status(c *gin.Context) {
	needed, err := h.service.IsBootstrapNeeded(c.Request.Context())
	if err != nil {
		log.Printf("auth status: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, gin.H{
		"bootstrap_needed": needed,
		"token_ttl_hours":  int(h.service.TTL().Hours()),
	})
}

// Logout POST /auth/logout
//
// 无状态 token 撤不回来, 所以这里不假装撤销: 明确告诉客户端丢弃 token,
// 并说明"要立刻断掉某人的访问, 用停用账号"。
func (h *authHandler) Logout(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"message": "已退出: 请在前端丢弃 token(无状态 JWT 无法在服务端撤销; 要立刻断开某人请停用其账号)",
	})
}
