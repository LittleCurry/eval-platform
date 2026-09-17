package http

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/auth"
	"eval-platform/server/internal/store"
)

type datasetHandler struct {
	store DatasetStore
}

func newDatasetHandler(s DatasetStore) *datasetHandler { return &datasetHandler{store: s} }

func (h *datasetHandler) List(c *gin.Context) {
	projectID, err := strconv.ParseInt(c.Query("project_id"), 10, 64)
	if err != nil || projectID <= 0 {
		writeErr(c, http.StatusBadRequest, "project_id 必填且为正整数")
		return
	}
	items, err := h.store.ListDatasets(c.Request.Context(), projectID)
	if err != nil {
		log.Printf("list datasets: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, items)
}

type createDatasetReq struct {
	ProjectID   int64  `json:"project_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *datasetHandler) Create(c *gin.Context) {
	var req createDatasetReq
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

	actor, _ := auth.CurrentUser(c)
	ds, err := h.store.CreateDataset(c.Request.Context(),
		req.ProjectID, req.Name, strings.TrimSpace(req.Description), actor.ID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeErr(c, http.StatusNotFound, "项目不存在")
		case errors.Is(err, store.ErrConflict):
			writeErr(c, http.StatusConflict, "该项目下已存在同名数据集")
		default:
			log.Printf("create dataset: %v", err)
			writeErr(c, http.StatusInternalServerError, "创建失败")
		}
		return
	}
	writeJSON(c, http.StatusCreated, ds)
}

func (h *datasetHandler) Get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	ds, err := h.store.GetDataset(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "数据集不存在")
			return
		}
		log.Printf("get dataset: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, ds)
}

func (h *datasetHandler) Delete(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.store.DeleteDataset(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "数据集不存在")
			return
		}
		log.Printf("delete dataset: %v", err)
		writeErr(c, http.StatusInternalServerError, "删除失败")
		return
	}
	c.Status(http.StatusNoContent)
}
