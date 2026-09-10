package http

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/store"
)

type runHandler struct {
	store RunStore
}

func newRunHandler(s RunStore) *runHandler { return &runHandler{store: s} }

// queryInt 读取整型查询参数; 缺失或非法时用默认值。
func queryInt(c *gin.Context, key string, def int) int {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return value
}

// List GET /runs?dataset_id=&project_id=&limit=
func (h *runHandler) List(c *gin.Context) {
	datasetID, _ := strconv.ParseInt(c.Query("dataset_id"), 10, 64)
	projectID, _ := strconv.ParseInt(c.Query("project_id"), 10, 64)
	limit := queryInt(c, "limit", 50)

	runs, err := h.store.ListRuns(c.Request.Context(), datasetID, projectID, limit)
	if err != nil {
		log.Printf("list runs: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	if runs == nil {
		runs = []store.Run{} // 保证响应是 [] 而不是 null
	}
	writeJSON(c, http.StatusOK, runs)
}

// Get GET /runs/:id
func (h *runHandler) Get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	run, err := h.store.GetRun(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "运行记录不存在")
			return
		}
		log.Printf("get run %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, run)
}

// CaseResults GET /runs/:id/case-results?limit=&flagged=1
func (h *runHandler) CaseResults(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	limit := queryInt(c, "limit", 200)
	flagged := c.Query("flagged") == "1" || c.Query("flagged") == "true"
	ctx := c.Request.Context()

	items, err := h.store.ListRunCaseResults(ctx, id, limit, flagged)
	if err != nil {
		log.Printf("list run case results %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	if items == nil {
		items = []store.RunCaseResult{} // 保证响应是 [] 而不是 null
	}
	// 空结果要区分"run 不存在"与"run 没有结果": 前者 404
	if len(items) == 0 {
		if _, err := h.store.GetRun(ctx, id); errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "运行记录不存在")
			return
		} else if err != nil {
			log.Printf("get run %d: %v", id, err)
			writeErr(c, http.StatusInternalServerError, "查询失败")
			return
		}
	}
	writeJSON(c, http.StatusOK, items)
}

// Report GET /runs/:id/report?worst=10
// 服务端聚合: run 详情 + 指标 + 最差 N 题 + 归因标签统计(前端不必自己算)。
func (h *runHandler) Report(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	worst := queryInt(c, "worst", 10)
	ctx := c.Request.Context()

	run, err := h.store.GetRun(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "运行记录不存在")
			return
		}
		log.Printf("get run %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	// ListRunCaseResults 已按 recall 升序 -> 前 N 条即最差用例
	worstCases, err := h.store.ListRunCaseResults(ctx, id, worst, false)
	if err != nil {
		log.Printf("list worst cases %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	flagCounts, err := h.store.ListRunFlagCounts(ctx, id)
	if err != nil {
		log.Printf("flag counts %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	writeJSON(c, http.StatusOK, gin.H{
		"run":         run,
		"metrics":     run.Metrics,
		"worst_cases": worstCases,
		"flag_counts": flagCounts,
	})
}
