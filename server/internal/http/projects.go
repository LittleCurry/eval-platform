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

// Get GET /projects/:id
//
// 前端项目切换器会带着本地记住的 project id 进来: 项目被删时它要能明确拿到 404,
// 而不是悄悄回退到"第一个项目"(那会让人以为自己在看 A, 实际在看 B)。
func (h *projectHandler) Get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	project, err := h.store.GetProject(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(c, http.StatusNotFound, "项目不存在")
		return
	}
	if err != nil {
		log.Printf("get project %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, project)
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

	// 项目是隔离单位: 记下创建者(M7-2)。全员可见, 但"谁建的"必须留痕 —— 出事找得到人。
	actor, _ := auth.CurrentUser(c)
	pr, err := h.store.CreateProject(c.Request.Context(), req.Name,
		strings.TrimSpace(req.Description), actor.ID)
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
