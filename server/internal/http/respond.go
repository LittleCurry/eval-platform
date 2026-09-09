package http

import "github.com/gin-gonic/gin"

// writeErr 输出统一错误结构 {"error": msg}。
func writeErr(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"error": msg})
}

// writeJSON 输出 JSON 响应。
func writeJSON(c *gin.Context, status int, v any) {
	c.JSON(status, v)
}
