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

// annotationHandler 提供 Bad Case 标注的 CRUD 与统计(M6)。
//
// 三件事值得说明:
//  1. **状态机在服务端**: DB 的 CHECK 只管枚举, "verified 不能直接跳 fixed"这类业务规则
//     由 canTransition 把关 —— 否则改规则就要写迁移, 而且前端绕过去就失效。
//  2. **归因建议只做"建议"**: suggestionRationale 由机器标签推导(与 M4-3 的标签族对齐),
//     人工可以改成别的值; 机器标签与人工归因的差异正是 M6 校准报告要量化的东西。
//  3. **统计与建议用独立顶层路径**: /annotation-stats 而不是 /annotations/stats ——
//     gin(httprouter) 同层不允许静态段与 :id 通配段共存(会 panic), 与 M5 的 /compare 同理。
type annotationHandler struct {
	store AnnotationStore
	// runs 只用于"归因建议": 要知道这道题的机器标签, 就得读它的单题结果。
	// 拆成两个依赖而不是并入 AnnotationStore, 是为了让标注存储与运行结果各管各的。
	runs RunStore
}

func newAnnotationHandler(annotations AnnotationStore, runs RunStore) *annotationHandler {
	return &annotationHandler{store: annotations, runs: runs}
}

// 状态枚举与 DB 的 CHECK 约束保持一致(双保险, 也便于出错时给出人话提示)。
var allowedAnnotationStatus = map[string]bool{
	"open": true, "fixed": true, "verified": true, "wontfix": true,
}

// 归因枚举与 M4-3 的标签族对齐; 空串 = 还没归类。
var allowedAnnotationReason = map[string]bool{
	"": true, "retrieval": true, "hallucination": true,
	"generation": true, "dataset": true, "unknown": true,
}

// canTransition 标注状态机。
//
// 主链路 open → fixed → verified; wontfix 表示"确认不修"(漏召回是数据集问题、或者这题本身
// 出得不合理)。允许回退到 open(复核时发现标错了), 但不允许 verified → fixed ——
// 那等于把"已验证修好"悄悄降级, 会让闭环报告失真。
func canTransition(from, to string) bool {
	if from == to {
		return true // 幂等: 重复提交同一个状态不算非法(前端重试/双开都会遇到)
	}
	switch from {
	case "open":
		return to == "fixed" || to == "wontfix"
	case "fixed":
		return to == "verified" || to == "open"
	case "verified":
		return to == "open"
	case "wontfix":
		return to == "open"
	default:
		return false
	}
}

// reasonFamily 机器标签 -> 人工归因族的建议映射(与 worker 的 FLAG_PRIORITY 顺序一致)。
//
// 为什么取**第一个**标签: flags[0] 就是主因(D16), 建议值应与主因一致, 否则工作台上
// "建议"和"主因"两个字段会互相打架。
func reasonFamily(flags []string) string {
	if len(flags) == 0 {
		return ""
	}
	switch flags[0] {
	case "no_gold", "anchor_incomplete":
		return "dataset"
	case "retrieval_miss", "retrieval_partial", "retrieval_low_rank":
		return "retrieval"
	case "hallucination":
		return "hallucination"
	case "off_topic", "generation_quality", "no_claims":
		return "generation"
	default:
		return "unknown"
	}
}

// suggestionRationale 给建议配一句人话, 免得工作台只显示一个枚举值。
func suggestionRationale(flags []string) string {
	if len(flags) == 0 {
		return "本次 run 没有给这道题打任何归因标签"
	}
	primary := flags[0]
	switch primary {
	case "no_gold":
		return "数据集缺 gold, 检索侧不可评测 —— 先补锚点"
	case "anchor_incomplete":
		return "没召回 gold 但答案全有据, 说明锚点漏标(该修数据集, 不是检索)"
	case "retrieval_miss":
		return "gold 全未召回: 证据根本没送到模型面前"
	case "retrieval_partial":
		return "部分 gold 未召回: 证据不全"
	case "retrieval_low_rank":
		return "命中但排位靠后: 证据在, 只是排序问题"
	case "hallucination":
		return "存在无据断言: 答案在编"
	case "off_topic":
		return "断言与问题无关: 有据但答非所问"
	case "generation_quality":
		return "检索到位且无幻觉, 但质量不达标"
	case "no_claims":
		return "答案没有可核查断言(例如只说\"资料中未提及\")"
	default:
		return "未知标签: " + primary
	}
}

// List GET /annotations?run_id=&case_id=&status=&limit=
func (h *annotationHandler) List(c *gin.Context) {
	filter := store.AnnotationFilter{
		RunID:  parseIntQuery(c, "run_id"),
		CaseID: parseIntQuery(c, "case_id"),
		Status: strings.TrimSpace(c.Query("status")),
		Limit:  queryInt(c, "limit", 200),
	}
	if filter.Status != "" && !allowedAnnotationStatus[filter.Status] {
		writeErr(c, http.StatusBadRequest, "status 取值非法")
		return
	}
	items, err := h.store.ListAnnotations(c.Request.Context(), filter)
	if err != nil {
		log.Printf("list annotations: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, items)
}

// createAnnotationReq 的字段用**指针**: 这个端点是"按 (run, case) 打标"的 upsert,
// 工作台常常只带一个 status(点一下"标记已修")。若用零值判断, 那次请求会把 reason/comment
// 一起清空 —— 实测就踩过: 只改状态后统计里冒出 unclassified。
// 约定与 PATCH 一致: 没传 = 不改, 显式传空串 = 清空。
type createAnnotationReq struct {
	ProjectID int64   `json:"project_id"`
	RunID     int64   `json:"run_id"`
	CaseID    int64   `json:"case_id"`
	Status    *string `json:"status"`
	Reason    *string `json:"reason"`
	Comment   *string `json:"comment"`
	Assignee  *string `json:"assignee"`
	CreatedBy string  `json:"created_by"`
}

// Upsert POST /annotations
//
// 同一题在同一次 run 里只有一条标注: 已有记录则按状态机更新, 没有则新建。
// 这样工作台就是"点一下打标", 不必先查再决定 POST 还是 PATCH。
func (h *annotationHandler) Upsert(c *gin.Context) {
	var req createAnnotationReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.RunID <= 0 || req.CaseID <= 0 {
		writeErr(c, http.StatusBadRequest, "run_id 与 case_id 必填且为正整数")
		return
	}
	status := "open"
	if req.Status != nil {
		status = *req.Status
	}
	if !allowedAnnotationStatus[status] {
		writeErr(c, http.StatusBadRequest, "status 必须是 open/fixed/verified/wontfix")
		return
	}
	if req.Reason != nil && !allowedAnnotationReason[*req.Reason] {
		writeErr(c, http.StatusBadRequest, "reason 必须是 retrieval/hallucination/generation/dataset/unknown 或留空")
		return
	}
	comment := trimPtr(req.Comment)
	assignee := trimPtr(req.Assignee)

	ctx := c.Request.Context()
	existing, err := h.store.GetAnnotationByRunCase(ctx, req.RunID, req.CaseID)
	switch {
	case err == nil:
		if !canTransition(existing.Status, status) {
			writeErr(c, http.StatusConflict,
				"状态流转非法: "+existing.Status+" → "+status)
			return
		}
		updated, err := h.store.UpdateAnnotation(ctx, existing.ID,
			&status, req.Reason, comment, assignee)
		if err != nil {
			log.Printf("update annotation %d: %v", existing.ID, err)
			writeErr(c, http.StatusInternalServerError, "更新失败")
			return
		}
		writeJSON(c, http.StatusOK, updated)
	case errors.Is(err, store.ErrNotFound):
		projectID := req.ProjectID
		if projectID <= 0 {
			projectID = 1 // M1 阶段无登录体系: 与前端 useProject 的默认值一致
		}
		reason := ""
		if req.Reason != nil {
			reason = *req.Reason
		}
		created, err := h.store.CreateAnnotation(ctx, projectID, req.RunID, req.CaseID,
			status, reason, derefOr(comment, ""), derefOr(assignee, ""), strings.TrimSpace(req.CreatedBy))
		if err != nil {
			switch {
			case errors.Is(err, store.ErrConflict):
				writeErr(c, http.StatusConflict, "该题在本次 run 里已有标注")
			case errors.Is(err, store.ErrNotFound):
				writeErr(c, http.StatusNotFound, "run 或 case 不存在")
			default:
				log.Printf("create annotation: %v", err)
				writeErr(c, http.StatusInternalServerError, "创建失败")
			}
			return
		}
		writeJSON(c, http.StatusCreated, created)
	default:
		log.Printf("get annotation %d/%d: %v", req.RunID, req.CaseID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
	}
}

type updateAnnotationReq struct {
	Status   *string `json:"status"`
	Reason   *string `json:"reason"`
	Comment  *string `json:"comment"`
	Assignee *string `json:"assignee"`
}

// Update PATCH /annotations/:id
func (h *annotationHandler) Update(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req updateAnnotationReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.Status != nil {
		if !allowedAnnotationStatus[*req.Status] {
			writeErr(c, http.StatusBadRequest, "status 必须是 open/fixed/verified/wontfix")
			return
		}
	}
	if req.Reason != nil && !allowedAnnotationReason[*req.Reason] {
		writeErr(c, http.StatusBadRequest, "reason 取值非法")
		return
	}
	if req.Comment != nil {
		trimmed := strings.TrimSpace(*req.Comment)
		req.Comment = &trimmed
	}
	if req.Assignee != nil {
		trimmed := strings.TrimSpace(*req.Assignee)
		req.Assignee = &trimmed
	}

	ctx := c.Request.Context()
	items, err := h.store.ListAnnotations(ctx, store.AnnotationFilter{CaseID: 0, Limit: 500})
	if err != nil {
		log.Printf("list annotations for state check: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	var current *store.Annotation
	for index := range items {
		if items[index].ID == id {
			current = &items[index]
			break
		}
	}
	if current == nil {
		writeErr(c, http.StatusNotFound, "标注不存在")
		return
	}
	if req.Status != nil && !canTransition(current.Status, *req.Status) {
		writeErr(c, http.StatusConflict, "状态流转非法: "+current.Status+" → "+*req.Status)
		return
	}

	updated, err := h.store.UpdateAnnotation(ctx, id, req.Status, req.Reason, req.Comment, req.Assignee)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "标注不存在")
			return
		}
		log.Printf("update annotation %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "更新失败")
		return
	}
	writeJSON(c, http.StatusOK, updated)
}

// Delete DELETE /annotations/:id
func (h *annotationHandler) Delete(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.store.DeleteAnnotation(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "标注不存在")
			return
		}
		log.Printf("delete annotation %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "删除失败")
		return
	}
	c.Status(http.StatusNoContent)
}

// Stats GET /annotation-stats?run_id=
func (h *annotationHandler) Stats(c *gin.Context) {
	runID, err := strconv.ParseInt(c.Query("run_id"), 10, 64)
	if err != nil || runID <= 0 {
		writeErr(c, http.StatusBadRequest, "run_id 必填且为正整数")
		return
	}
	stats, err := h.store.AggregateAnnotationStats(c.Request.Context(), runID)
	if err != nil {
		log.Printf("annotation stats %d: %v", runID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, stats)
}

// Suggestion GET /annotation-suggestion?run_id=&case_id=
//
// 工作台点开一道题时先给出"机器认为的归因", 人工确认或纠正 —— 纠正本身就是要被
// 校准报告量化的数据(机器标签 vs 人工归因)。
func (h *annotationHandler) Suggestion(c *gin.Context) {
	runID := parseIntQuery(c, "run_id")
	caseID := parseIntQuery(c, "case_id")
	if runID <= 0 || caseID <= 0 {
		writeErr(c, http.StatusBadRequest, "run_id 与 case_id 必填且为正整数")
		return
	}
	row, err := h.runs.GetRunCaseResult(c.Request.Context(), runID, caseID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(c, http.StatusNotFound, "该题在本次 run 里没有结果")
		return
	}
	if err != nil {
		log.Printf("annotation suggestion %d/%d: %v", runID, caseID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	flags := row.Flags
	switch {
	case row.Judge == nil && len(flags) > 0 && reasonFamily(flags) == "hallucination":
		// 没判定就不可能有"幻觉"结论: 标签是历史遗留(例如 run 改造过), 说清而不是照抄
		writeJSON(c, http.StatusOK, gin.H{
			"reason":    "unknown",
			"rationale": "该 run 没有判定结果, 无法确认幻觉 —— 请人工判断",
			"flags":     flags,
		})
		return
	}
	writeJSON(c, http.StatusOK, gin.H{
		"reason":    reasonFamily(flags),
		"rationale": suggestionRationale(flags),
		"flags":     flags,
	})
}

// trimPtr 去掉首尾空格; nil 保持 nil(表示"不改该字段")。
func trimPtr(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

func derefOr(value *string, def string) string {
	if value == nil {
		return def
	}
	return *value
}

// parseIntQuery 读整型查询参数, 缺失或非法返回 0(调用方自行判断是否为必填)。
func parseIntQuery(c *gin.Context, key string) int64 {
	value, err := strconv.ParseInt(c.Query(key), 10, 64)
	if err != nil {
		return 0
	}
	return value
}
