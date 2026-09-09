package http

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/store"
)

type projectHandler struct {
	store ProjectStore
}

func newProjectHandler(s ProjectStore) *projectHandler { return &projectHandler{store: s} }

func (h *projectHandler) List(c *gin.Context) {
	items, err := h.store.ListProjects(c.Request.Context())
	if err != nil {
		log.Printf("list projects: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, items)
}

type createProjectReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *projectHandler) Create(c *gin.Context) {
	var req createProjectReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(c, http.StatusBadRequest, "name 必填")
		return
	}

	pr, err := h.store.CreateProject(c.Request.Context(), req.Name, strings.TrimSpace(req.Description))
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(c, http.StatusConflict, "项目名已存在")
			return
		}
		log.Printf("create project: %v", err)
		writeErr(c, http.StatusInternalServerError, "创建失败")
		return
	}
	writeJSON(c, http.StatusCreated, pr)
}
