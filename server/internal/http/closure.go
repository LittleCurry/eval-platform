package http

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/eval"
	"eval-platform/server/internal/store"
)

// closureHandler 提供 M6 的标注闭环报告: "标成 fixed 的题, 在新 run 里真的好了吗?"
//
// 为什么单独一个端点, 而不是让前端拿 /compare 的结果自己拼:
//   - 前端拼就要自己定义"什么是变好", 于是同一条题在对比页和闭环页可能得到相反结论;
//   - 标注是按 case_id 存的, 而 /compare 的单题结果只带 qid(它是题库视角) —— 前端拿不到键;
//   - 销单这件事有状态机约束(fixed → verified), 判断该不该销单必须与标注状态一起看。
//
// 路径放顶层 /closure: 主语是"两次 run", 与 /compare 同理, 也避开 /runs/:id 的路由树。
type closureHandler struct {
	runs        RunStore
	annotations AnnotationStore
}

func newClosureHandler(runs RunStore, annotations AnnotationStore) *closureHandler {
	return &closureHandler{runs: runs, annotations: annotations}
}

// Closure GET /api/v1/closure?baseline=112&candidate=100&status=fixed
//
// baseline = 打标注的那次 run(待办所在), candidate = 改完之后的新 run。
// 方向不能反: 反了会把"我修好了的题"读成"我弄坏了的题"。
func (h *closureHandler) Closure(c *gin.Context) {
	baselineID := parseIntQuery(c, "baseline")
	candidateID := parseIntQuery(c, "candidate")
	if baselineID <= 0 || candidateID <= 0 {
		writeErr(c, http.StatusBadRequest, "需要 baseline 与 candidate 两个 run id")
		return
	}
	if baselineID == candidateID {
		writeErr(c, http.StatusBadRequest, "baseline 与 candidate 不能是同一次 run(那没有可验证的变化)")
		return
	}
	statusFilter := c.Query("status")
	if statusFilter != "" && !allowedAnnotationStatus[statusFilter] {
		writeErr(c, http.StatusBadRequest, "status 必须是 open/fixed/verified/wontfix")
		return
	}

	ctx := c.Request.Context()
	baselineRun, ok := h.loadRun(c, baselineID, "baseline")
	if !ok {
		return
	}
	candidateRun, ok := h.loadRun(c, candidateID, "candidate")
	if !ok {
		return
	}

	if baselineRun.DatasetID != candidateRun.DatasetID {
		// 跨评测集: 题都不一样, 谈不上"这道题修好了没"
		c.JSON(http.StatusOK, eval.ClosureReport{
			Baseline:   baselineID,
			Candidate:  candidateID,
			Comparable: false,
			Reason: "两次 run 用的不是同一个评测集(dataset " +
				strconv.FormatInt(baselineRun.DatasetID, 10) + " vs " + strconv.FormatInt(candidateRun.DatasetID, 10) +
				"), 无法逐题核对修复效果",
			Summary: eval.ClosureSummary{ByStatus: map[string]int{}},
			Records: []eval.ClosureRecord{},
			Notes:   []string{},
		})
		return
	}

	// 标注挂在 baseline 上: 那是"当时发现的坏题", candidate 是"改完之后的样子"。
	annotations, err := h.annotations.ListAnnotations(ctx, store.AnnotationFilter{
		RunID: baselineID,
		Limit: maxCompareCases,
	})
	if err != nil {
		log.Printf("closure: list annotations %d: %v", baselineID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	baselineCases, err := h.runs.ListRunCaseResults(ctx, baselineID, maxCompareCases, false)
	if err != nil {
		log.Printf("closure: list baseline cases %d: %v", baselineID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	candidateCases, err := h.runs.ListRunCaseResults(ctx, candidateID, maxCompareCases, false)
	if err != nil {
		log.Printf("closure: list candidate cases %d: %v", candidateID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	items := make([]eval.ClosureAnnotation, 0, len(annotations))
	for _, annotation := range annotations {
		items = append(items, eval.ClosureAnnotation{
			AnnotationID: annotation.ID,
			CaseID:       annotation.CaseID,
			Status:       annotation.Status,
			Reason:       annotation.Reason,
			Comment:      annotation.Comment,
			Assignee:     annotation.Assignee,
		})
	}

	report := eval.ComputeClosure(items, toClosureCases(baselineCases), toClosureCases(candidateCases),
		eval.ClosureOptions{
			StatusFilter:         statusFilter,
			BaselineAttribution:  hasAttribution(baselineRun),
			CandidateAttribution: hasAttribution(candidateRun),
			BaselineHash:         baselineRun.ConfigHash,
			CandidateHash:        candidateRun.ConfigHash,
		})
	report.Baseline = baselineID
	report.Candidate = candidateID
	writeJSON(c, http.StatusOK, report)
}

// loadRun 取 run 并统一处理 404/500(两个 run 的取法一致, 免得各写一遍)。
func (h *closureHandler) loadRun(c *gin.Context, id int64, role string) (store.Run, bool) {
	run, err := h.runs.GetRun(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(c, http.StatusNotFound, role+" run 不存在")
		return store.Run{}, false
	}
	if err != nil {
		log.Printf("closure: get %s run %d: %v", role, id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return store.Run{}, false
	}
	return run, true
}

// toClosureCases 把落库的单题结果映射成闭环用的纯数据。
//
// 与 A/B 的 toABCases 的差别只有一个: **保留 case_id**(标注是按 case_id 存的),
// 因此不能直接复用; judge 摘要则复用同一套 judgeSummary, 免得两处对"有没有判定"判断不一致。
func toClosureCases(rows []store.RunCaseResult) []eval.ClosureCase {
	out := make([]eval.ClosureCase, 0, len(rows))
	for _, row := range rows {
		out = append(out, eval.ClosureCase{
			CaseID:   row.CaseID,
			QID:      row.QID,
			Question: row.Question,
			Metrics:  numericMetrics(row.Metrics),
			Flags:    row.Flags,
			Judge:    judgeSummary(row.Judge),
		})
	}
	return out
}

// numericMetrics 只搬数值字段(jsonb 里还可能混着字符串/布尔, 直接断言 float64 会 panic)。
func numericMetrics(source map[string]any) map[string]float64 {
	out := make(map[string]float64, len(source))
	for key, value := range source {
		switch typed := value.(type) {
		case float64:
			out[key] = typed
		case float32:
			out[key] = float64(typed)
		case int:
			out[key] = float64(typed)
		case int64:
			out[key] = float64(typed)
		}
	}
	return out
}
