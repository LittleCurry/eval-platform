package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/store"
)

// Gin 中间件(M7-1)。
//
// 两个失败码刻意分开, 前端据此做不同的事:
//   401 = 没登录/登录失效 -> 跳登录页;
//   403 = 登录了但没这个权限 -> 留在原页并提示"需要 XX 权限"。
// 混用会让"权限不足"变成反复跳登录页, 用户永远不知道自己只是没权限。

// ContextUserKey 是 gin.Context 里存当前用户的键。
const ContextUserKey = "auth.user"

// Middleware 组装鉴权中间件。
type Middleware struct {
	service *Service
	// enabled=false 时中间件直接放行(仅用于测试与显式关闭鉴权的本地开发)。
	enabled bool
}

// NewMiddleware 构造中间件。
func NewMiddleware(service *Service, enabled bool) *Middleware {
	return &Middleware{service: service, enabled: enabled}
}

// Enabled 报告鉴权是否真的在起作用(router 用它决定要不要挂)。
func (m *Middleware) Enabled() bool {
	return m.enabled && m.service != nil
}

// Required 校验 Bearer token 并把用户放进 context。
func (m *Middleware) Required() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !m.Enabled() {
			// 未启用鉴权: 塞一个匿名 admin, 让 handler 里的"当前用户"逻辑不必到处判空。
			// 这个分支只在测试或显式关闭鉴权时走到, main.go 会打警告。
			c.Set(ContextUserKey, store.User{ID: 0, Email: "anonymous", Role: store.RoleAdmin})
			c.Next()
			return
		}
		token, ok := BearerToken(c.GetHeader("Authorization"))
		if !ok {
			abortUnauthorized(c, "需要登录(缺少 Bearer token)")
			return
		}
		user, err := m.service.Authenticate(c.Request.Context(), token)
		if err != nil {
			switch {
			case errors.Is(err, ErrUserDisabled):
				abortUnauthorized(c, "账号已停用, 请联系管理员")
			case errors.Is(err, ErrTokenExpired):
				abortUnauthorized(c, "登录已过期, 请重新登录")
			default:
				abortUnauthorized(c, "登录状态无效, 请重新登录")
			}
			return
		}
		c.Set(ContextUserKey, user)
		c.Next()
	}
}

// Require 要求至少某个角色档位。
func (m *Middleware) Require(minRole string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !m.Enabled() {
			c.Next()
			return
		}
		user, ok := CurrentUser(c)
		if !ok {
			abortUnauthorized(c, "需要登录")
			return
		}
		if !AtLeast(user.Role, minRole) {
			abortForbidden(c, "需要 "+RoleLabel(minRole)+" 及以上权限, 当前角色: "+RoleLabel(user.Role))
			return
		}
		c.Next()
	}
}

// RequireAction 要求某个具体动作(权限矩阵在 rbac.go)。
func (m *Middleware) RequireAction(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !m.Enabled() {
			c.Next()
			return
		}
		user, ok := CurrentUser(c)
		if !ok {
			abortUnauthorized(c, "需要登录")
			return
		}
		if !Can(user.Role, action) {
			abortForbidden(c, "当前角色("+RoleLabel(user.Role)+")不能执行该操作")
			return
		}
		c.Next()
	}
}

// CurrentUser 从 context 取当前用户。
func CurrentUser(c *gin.Context) (store.User, bool) {
	value, ok := c.Get(ContextUserKey)
	if !ok {
		return store.User{}, false
	}
	user, ok := value.(store.User)
	return user, ok
}

// RoleLabel 角色的中文名(错误信息给人看)。
func RoleLabel(role string) string {
	switch role {
	case store.RoleAdmin:
		return "管理员"
	case store.RoleEditor:
		return "编辑者"
	case store.RoleViewer:
		return "只读"
	default:
		if strings.TrimSpace(role) == "" {
			return "未知角色"
		}
		return role
	}
}

func abortUnauthorized(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": message})
}

func abortForbidden(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": message})
}
