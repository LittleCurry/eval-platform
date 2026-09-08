package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// NewRouter 组装全部路由。依赖以 Pinger 接口注入, 便于测试替换。
func NewRouter(postgres, qdrant Pinger) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	health := newHealthHandler(postgres, qdrant)
	r.GET("/healthz", health.Healthz)

	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"service": "eval-platform", "docs": "/healthz"})
	})
	return r
}
