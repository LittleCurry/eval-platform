package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Pinger 定义依赖服务的连通性检查能力, 由 handler 消费、测试注入 fake。
type Pinger interface {
	Ping(ctx context.Context) error
}

var errNotInitialized = errors.New("dependency not initialized")

// healthHandler 实现 GET /healthz。
type healthHandler struct {
	postgres Pinger
	qdrant   Pinger
}

func newHealthHandler(postgres, qdrant Pinger) *healthHandler {
	return &healthHandler{postgres: postgres, qdrant: qdrant}
}

// pingDep 容忍 nil 依赖(启动时连不上也继续跑, 探活结果交给 /healthz)。
func pingDep(ctx context.Context, p Pinger) error {
	if p == nil {
		return errNotInitialized
	}
	return p.Ping(ctx)
}

// Healthz 逐个探测依赖, 全通则 200, 否则 503。
func (h *healthHandler) Healthz(c *gin.Context) {
	ctx := c.Request.Context()
	checks := map[string]string{}
	allUp := true

	if err := pingDep(ctx, h.postgres); err != nil {
		checks["postgres"] = "down"
		allUp = false
	} else {
		checks["postgres"] = "up"
	}

	if err := pingDep(ctx, h.qdrant); err != nil {
		checks["qdrant"] = "down"
		allUp = false
	} else {
		checks["qdrant"] = "up"
	}

	status, code := "ok", http.StatusOK
	if !allUp {
		status, code = "degraded", http.StatusServiceUnavailable
	}
	c.JSON(code, gin.H{"status": status, "checks": checks})
}
