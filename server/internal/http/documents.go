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

type documentHandler struct {
	store DocumentStore
}

func newDocumentHandler(s DocumentStore) *documentHandler { return &documentHandler{store: s} }

type documentItemReq struct {
	DocID   string         `json:"doc_id"`
	Title   string         `json:"title"`
	RawText string         `json:"raw_text"`
	Meta    map[string]any `json:"meta"`
}

type createDocumentsReq struct {
	Documents []documentItemReq `json:"documents"`
}

func (h *documentHandler) Create(c *gin.Context) {
	corpusID, ok := parseID(c)
	if !ok {
		return
	}
	var req createDocumentsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if len(req.Documents) == 0 {
		writeErr(c, http.StatusBadRequest, "documents 不能为空")
		return
	}

	// 单条校验 + 请求内 doc_id 去重校验
	seen := make(map[string]struct{}, len(req.Documents))
	docs := make([]store.Document, 0, len(req.Documents))
	for i, it := range req.Documents {
		it.DocID = strings.TrimSpace(it.DocID)
		it.Title = strings.TrimSpace(it.Title)
		idx := i + 1
		switch {
		case it.DocID == "":
			writeErr(c, http.StatusBadRequest, "第"+strconv.Itoa(idx)+"条 doc_id 必填")
			return
		case it.Title == "":
			writeErr(c, http.StatusBadRequest, "第"+strconv.Itoa(idx)+"条 title 必填")
			return
		case strings.TrimSpace(it.RawText) == "":
			writeErr(c, http.StatusBadRequest, "第"+strconv.Itoa(idx)+"条 raw_text 必填")
			return
		}
		if _, dup := seen[it.DocID]; dup {
			writeErr(c, http.StatusBadRequest, "同一请求中 doc_id 重复: "+it.DocID)
			return
		}
		seen[it.DocID] = struct{}{}
		docs = append(docs, store.Document{
			DocID:   it.DocID,
			Title:   it.Title,
			RawText: it.RawText,
			Meta:    it.Meta,
		})
	}

	inserted, err := h.store.CreateDocuments(c.Request.Context(), corpusID, docs)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeErr(c, http.StatusNotFound, "语料库不存在")
		case errors.Is(err, store.ErrConflict):
			writeErr(c, http.StatusConflict, "存在重复 doc_id")
		default:
			log.Printf("create documents: %v", err)
			writeErr(c, http.StatusInternalServerError, "导入失败")
		}
		return
	}
	writeJSON(c, http.StatusCreated, gin.H{"inserted": inserted})
}

func (h *documentHandler) List(c *gin.Context) {
	corpusID, ok := parseID(c)
	if !ok {
		return
	}
	items, err := h.store.ListDocuments(c.Request.Context(), corpusID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "语料库不存在")
			return
		}
		log.Printf("list documents: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, items)
}

func (h *documentHandler) Get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	doc, err := h.store.GetDocument(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "文档不存在")
			return
		}
		log.Printf("get document: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, doc)
}

func (h *documentHandler) Delete(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.store.DeleteDocument(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "文档不存在")
			return
		}
		log.Printf("delete document: %v", err)
		writeErr(c, http.StatusInternalServerError, "删除失败")
		return
	}
	c.Status(http.StatusNoContent)
}
