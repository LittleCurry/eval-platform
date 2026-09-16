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

// compareHandler 提供 A/B 对比(M5-1)。
//
// 路径刻意用顶层 /compare 而不是 /runs/compare: gin 的路由树里 /runs/:id 已经占了
// 通配段, 同级再挂静态段有冲突风险; 放在 /compare 上语义也更直白(它的主语是"两次 run")。
type compareHandler struct {
	runs RunStore
}

func newCompareHandler(runs RunStore) *compareHandler {
	return &compareHandler{runs: runs}
}

// maxCompareCases 单次对比最多取多少题。取 500 与 case-results 接口的上限一致:
// 评测集是题库, 规模可控; 真要上万题时该走离线作业, 而不是把浏览器拖死。
const maxCompareCases = 500

// Compare GET /api/v1/compare?left=112&right=100
//
// 返回逐题差值、Wilcoxon 与翻转题清单(见 eval.ComputeAB)。两条硬规则:
//   - 两次 run 的评测集不同 -> 200 + comparable=false + 原因(不崩、也不硬算);
//   - 共同题目不足一半 -> 同样标记不可比(拿半份题算平均分比不算更糟)。
func (h *compareHandler) Compare(c *gin.Context) {
	leftID, leftErr := strconv.ParseInt(c.Query("left"), 10, 64)
	rightID, rightErr := strconv.ParseInt(c.Query("right"), 10, 64)
	if leftErr != nil || rightErr != nil || leftID <= 0 || rightID <= 0 {
		writeErr(c, http.StatusBadRequest, "需要 left 与 right 两个 run id")
		return
	}
	if leftID == rightID {
		writeErr(c, http.StatusBadRequest, "left 与 right 不能是同一次 run")
		return
	}

	ctx := c.Request.Context()
	leftRun, err := h.runs.GetRun(ctx, leftID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(c, http.StatusNotFound, "left run 不存在")
		return
	}
	if err != nil {
		log.Printf("compare: get left run %d: %v", leftID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	rightRun, err := h.runs.GetRun(ctx, rightID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(c, http.StatusNotFound, "right run 不存在")
		return
	}
	if err != nil {
		log.Printf("compare: get right run %d: %v", rightID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	if leftRun.DatasetID != rightRun.DatasetID {
		// 跨评测集对比是常见误用: 明确说清, 而不是给出一个看起来很专业的平均值
		c.JSON(http.StatusOK, eval.ABReport{
			Left:       leftID,
			Right:      rightID,
			Comparable: false,
			Reason: "两次 run 用的不是同一个评测集（dataset " +
				strconv.FormatInt(leftRun.DatasetID, 10) + " vs " +
				strconv.FormatInt(rightRun.DatasetID, 10) + "），不能做配对比较",
			Summary:      map[string]eval.MetricDelta{},
			Fixed:        []eval.CaseDelta{},
			Broke:        []eval.CaseDelta{},
			Changed:      []eval.CaseDelta{},
			ByCategory:   []eval.Stratum{},
			ByDifficulty: []eval.Stratum{},
			ByFlag:       []eval.Stratum{},
		})
		return
	}

	leftCases, err := h.runs.ListRunCaseResults(ctx, leftID, maxCompareCases, false)
	if err != nil {
		log.Printf("compare: list left cases %d: %v", leftID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	rightCases, err := h.runs.ListRunCaseResults(ctx, rightID, maxCompareCases, false)
	if err != nil {
		log.Printf("compare: list right cases %d: %v", rightID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	report := eval.ComputeAB(toABCases(leftCases), toABCases(rightCases), eval.ABOptions{
		LeftAttribution:   hasAttribution(leftRun),
		RightAttribution:  hasAttribution(rightRun),
		IncludeGeneration: true,
	})
	report.Left = leftID
	report.Right = rightID
	c.JSON(http.StatusOK, report)
}

// hasAttribution 判断该 run 的归因标签是否已由规则写过:
// M4-3 的重算 CLI 会把 runs.metrics.attribution 落库, 它同时也是"这行标签可信"的凭据(D15)。
func hasAttribution(run store.Run) bool {
	if run.Metrics == nil {
		return false
	}
	_, ok := run.Metrics["attribution"]
	return ok
}

// toABCases 把落库的单题结果映射成对比用的纯数据(数值统一成 float64)。
func toABCases(rows []store.RunCaseResult) []eval.ABCase {
	out := make([]eval.ABCase, 0, len(rows))
	for _, row := range rows {
		metrics := make(map[string]float64, len(row.Metrics))
		for _, name := range eval.MetricsCompared {
			metrics[name] = floatField(row.Metrics, name)
		}
		out = append(out, eval.ABCase{
			QID:        row.QID,
			Category:   row.Category,
			Difficulty: row.Difficulty,
			Metrics:    metrics,
			Flags:      row.Flags,
			Judge:      judgeSummary(row.Judge),
		})
	}
	return out
}

// judgeSummary 从 case_results.judge 里抽出对比要用的计数与分数。
// 判定缺失(只跑检索的 run)返回 nil —— 生成侧指标会因此把这题排除在配对样本外。
func judgeSummary(judge map[string]any) *eval.ABJudge {
	if len(judge) == 0 {
		return nil
	}
	claims, ok := judge["claims"].([]any)
	if !ok && judge["rubric"] == nil {
		return nil
	}
	summary := &eval.ABJudge{}
	for _, raw := range claims {
		claim, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		summary.Claims++
		switch label, _ := claim["label"].(string); label {
		case "supported":
			summary.Supported++
		case "unsupported":
			summary.Unsupported++
		case "irrelevant":
			summary.Irrelevant++
		}
	}
	if rubric, ok := judge["rubric"].(map[string]any); ok && rubric != nil {
		summary.HasRubric = true
		summary.Relevance = floatField(rubric, "relevance")
		summary.Helpfulness = floatField(rubric, "helpfulness")
	}
	return summary
}

// floatField 读 jsonb 解出来的数值字段(jsonb 里都是 float64, 但历史数据可能是整数)。
func floatField(source map[string]any, key string) float64 {
	switch value := source[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	default:
		return 0
	}
}
