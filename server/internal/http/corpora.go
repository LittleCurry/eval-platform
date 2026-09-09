package http

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/store"
)

// allowedSourceTypes 与 DB CHECK 约束保持一致(双保险)。
var allowedSourceTypes = map[string]bool{
	"manual":   true,
	"upload":   true,
	"external": true,
}

type corpusHandler struct {
	store CorpusStore
}

func newCorpusHandler(s CorpusStore) *corpusHandler { return &corpusHandler{store: s} }

// parseID 解析路径参数中的正整数 id; 失败时已写好 400 响应并返回 false。
func parseID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(c, http.StatusBadRequest, "id 必须是正整数")
		return 0, false
	}
	return id, true
}

func (h *corpusHandler) List(c *gin.Context) {
	projectID, err := strconv.ParseInt(c.Query("project_id"), 10, 64)
	if err != nil || projectID <= 0 {
		writeErr(c, http.StatusBadRequest, "project_id 必填且为正整数")
		return
	}
	items, err := h.store.ListCorpora(c.Request.Context(), projectID)
	if err != nil {
		log.Printf("list corpora: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, items)
}

type createCorpusReq struct {
	ProjectID  int64  `json:"project_id"`
	Name       string `json:"name"`
	SourceType string `json:"source_type"`
}

func (h *corpusHandler) Create(c *gin.Context) {
	var req createCorpusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.ProjectID <= 0 {
		writeErr(c, http.StatusBadRequest, "project_id 必填且为正整数")
		return
	}
	if req.Name == "" {
		writeErr(c, http.StatusBadRequest, "name 必填")
		return
	}
	if req.SourceType == "" {
		req.SourceType = "manual"
	}
	if !allowedSourceTypes[req.SourceType] {
		writeErr(c, http.StatusBadRequest, "source_type 必须是 manual/upload/external")
		return
	}

	co, err := h.store.CreateCorpus(c.Request.Context(), req.ProjectID, req.Name, req.SourceType)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeErr(c, http.StatusNotFound, "项目不存在")
		case errors.Is(err, store.ErrConflict):
			writeErr(c, http.StatusConflict, "该项目下已存在同名语料库")
		default:
			log.Printf("create corpus: %v", err)
			writeErr(c, http.StatusInternalServerError, "创建失败")
		}
		return
	}
	writeJSON(c, http.StatusCreated, co)
}

func (h *corpusHandler) Get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	co, err := h.store.GetCorpus(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "语料库不存在")
			return
		}
		log.Printf("get corpus: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, co)
}

type updateCorpusReq struct {
	Name       *string `json:"name"`
	SourceType *string `json:"source_type"`
}

func (h *corpusHandler) Update(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req updateCorpusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			writeErr(c, http.StatusBadRequest, "name 不能为空")
			return
		}
		req.Name = &trimmed
	}
	if req.SourceType != nil && !allowedSourceTypes[*req.SourceType] {
		writeErr(c, http.StatusBadRequest, "source_type 必须是 manual/upload/external")
		return
	}

	co, err := h.store.UpdateCorpus(c.Request.Context(), id, req.Name, req.SourceType)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "语料库不存在")
			return
		}
		log.Printf("update corpus %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "更新失败")
		return
	}
	writeJSON(c, http.StatusOK, co)
}

func (h *corpusHandler) Delete(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.store.DeleteCorpus(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "语料库不存在")
			return
		}
		log.Printf("delete corpus %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "删除失败")
		return
	}
	c.Status(http.StatusNoContent)
}
