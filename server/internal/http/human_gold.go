package http

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/eval"
	"eval-platform/server/internal/store"
)

// humanGoldHandler 提供人工金标打分(M6)与 judge 校准报告。
//
// 与 Bad Case 标注的区别, 这是 M6 里最容易混的一处:
//   - **标注(annotations)** 回答"这题坏在哪、修没修", 主语是"待办";
//   - **金标(human_gold_scores)** 回答"这次 run 的这道题答案, 人认为是忠实/幻觉、几分",
//     主语是"评判依据" —— 它的唯一用途是**校准 judge 本身**(κ/MAE/偏差)。
//
// 为什么金标挂在 run 上而不是 dataset 上: judge 判的是**某一次 run 生成的那段答案**,
// 换一次 run(temperature、模型、top_k 都变了)答案就变了, 拿旧金标去校准新 run 是错的。
//
// 为什么 "看不清" 是合法取值: 强迫标注员在 faithful/hallucinated 二选一, 猜出来的标签
// 会把 κ 算漂亮 —— 那等于用人工的噪声给 judge 背书。
type humanGoldHandler struct {
	store HumanGoldStore
	runs  RunStore
}

func newHumanGoldHandler(gold HumanGoldStore, runs RunStore) *humanGoldHandler {
	return &humanGoldHandler{store: gold, runs: runs}
}

// verdict 枚举与 DB 的 CHECK 约束一致。
var allowedVerdict = map[string]bool{
	eval.VerdictFaithful: true, eval.VerdictHallucinated: true, eval.VerdictUnclear: true,
}

// List GET /human-gold?run_id=&annotator=
func (h *humanGoldHandler) List(c *gin.Context) {
	runID := parseIntQuery(c, "run_id")
	if runID <= 0 {
		writeErr(c, http.StatusBadRequest, "run_id 必填且为正整数")
		return
	}
	annotator := strings.TrimSpace(c.Query("annotator"))
	items, err := h.store.ListHumanGoldScores(c.Request.Context(), runID, annotator)
	if err != nil {
		log.Printf("list human gold %d: %v", runID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, items)
}

// createHumanGoldReq 的分数/备注用**指针**: 只判幻觉不打分是合法用法,
// 用零值判断会把"没打分"变成 0 分, 而 0 分会被算进 MAE 把校准结果毁掉;
// 备注同理 —— 第二次只提交 verdict 时不能把上次写的备注冲成空串。
type createHumanGoldReq struct {
	ProjectID   int64   `json:"project_id"`
	RunID       int64   `json:"run_id"`
	CaseID      int64   `json:"case_id"`
	Annotator   string  `json:"annotator"`
	Verdict     string  `json:"verdict"`
	Relevance   *int    `json:"relevance"`
	Helpfulness *int    `json:"helpfulness"`
	Note        *string `json:"note"`
	Reviewed    *bool   `json:"reviewed"`
}

// Upsert POST /human-gold
//
// 按 (run_id, case_id, annotator) upsert: 打分页是一题一次连续录入, 标注员改主意是常态,
// "同一人同一题重复打分"本身没有语义, 覆盖即可(不必让前端先查再决定 POST/PATCH)。
// 没传的字段保持原值(合并语义), 见 store.UpsertHumanGoldScore。
func (h *humanGoldHandler) Upsert(c *gin.Context) {
	var req createHumanGoldReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	annotator := strings.TrimSpace(req.Annotator)
	if req.RunID <= 0 || req.CaseID <= 0 {
		writeErr(c, http.StatusBadRequest, "run_id 与 case_id 必填且为正整数")
		return
	}
	if annotator == "" {
		// 不留默认值: 金标的主键含义就是"谁判的", 系统替人编一个标注员名字,
		// 会让两个人各自打的分数被合成一条记录 —— 双人一致性就永远算不出来了。
		writeErr(c, http.StatusBadRequest, "annotator 必填(用于区分不同标注员, 也是双人一致性的依据)")
		return
	}
	if !allowedVerdict[req.Verdict] {
		writeErr(c, http.StatusBadRequest, "verdict 必须是 faithful/hallucinated/unclear")
		return
	}
	if !validScore(req.Relevance) {
		writeErr(c, http.StatusBadRequest, "relevance 必须是 1–5 的整数或不传")
		return
	}
	if !validScore(req.Helpfulness) {
		writeErr(c, http.StatusBadRequest, "helpfulness 必须是 1–5 的整数或不传")
		return
	}

	projectID := req.ProjectID
	if projectID <= 0 {
		projectID = 1 // 与前端 useProject 的默认值一致(M1 阶段无登录体系)
	}
	item, err := h.store.UpsertHumanGoldScore(c.Request.Context(), store.HumanGoldInput{
		ProjectID:   projectID,
		RunID:       req.RunID,
		CaseID:      req.CaseID,
		Annotator:   annotator,
		Verdict:     req.Verdict,
		Relevance:   req.Relevance,
		Helpfulness: req.Helpfulness,
		Note:        trimPtr(req.Note),
		Reviewed:    req.Reviewed,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeErr(c, http.StatusNotFound, "run 或 case 不存在")
		case errors.Is(err, store.ErrConflict):
			writeErr(c, http.StatusConflict, "该题已有同一标注员的记录")
		default:
			log.Printf("upsert human gold: %v", err)
			writeErr(c, http.StatusInternalServerError, "保存失败")
		}
		return
	}
	writeJSON(c, http.StatusOK, item)
}

type updateHumanGoldReq struct {
	Verdict     *string `json:"verdict"`
	Relevance   *int    `json:"relevance"`
	Helpfulness *int    `json:"helpfulness"`
	Note        *string `json:"note"`
	Reviewed    *bool   `json:"reviewed"`
}

// Update PATCH /human-gold/:id
//
// 语义与标注一致: **没传 = 不改, 显式传才改**(指针)。
// 但打分字段多一个约定: **传 0 = 撤回这个分数(置 NULL)**。前端要能改掉打错的分,
// 否则只能删掉整条记录重来, 而删掉会把 verdict 一起丢掉(见 store.UpdateHumanGoldScore)。
func (h *humanGoldHandler) Update(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req updateHumanGoldReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.Verdict != nil && !allowedVerdict[*req.Verdict] {
		writeErr(c, http.StatusBadRequest, "verdict 必须是 faithful/hallucinated/unclear")
		return
	}
	if !validScoreOrClear(req.Relevance) {
		writeErr(c, http.StatusBadRequest, "relevance 必须是 1–5 的整数、0(撤回)或不传")
		return
	}
	if !validScoreOrClear(req.Helpfulness) {
		writeErr(c, http.StatusBadRequest, "helpfulness 必须是 1–5 的整数、0(撤回)或不传")
		return
	}
	if req.Note != nil {
		trimmed := strings.TrimSpace(*req.Note)
		req.Note = &trimmed
	}

	item, err := h.store.UpdateHumanGoldScore(c.Request.Context(), id,
		req.Verdict, req.Relevance, req.Helpfulness, req.Note, req.Reviewed)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "金标记录不存在")
			return
		}
		log.Printf("update human gold %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "更新失败")
		return
	}
	writeJSON(c, http.StatusOK, item)
}

// Delete DELETE /human-gold/:id
func (h *humanGoldHandler) Delete(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.store.DeleteHumanGoldScore(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "金标记录不存在")
			return
		}
		log.Printf("delete human gold %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "删除失败")
		return
	}
	c.Status(http.StatusNoContent)
}

// Calibration GET /judge-calibration?run_id=&annotator=
//
// 报告回答"judge 值不值得信", 而不是"judge 准确率 87%"这种没有上下文的数字:
//  1. 二分类(有/无幻觉)的混淆矩阵 + 一致率 + κ —— κ 才是扣掉"蒙对"后的信息量;
//  2. 分数(helpfulness/relevance)的完全一致率 / ±1 / MAE / 均值偏差 —— 偏差带符号,
//     能看出 judge 是偏宽松还是偏严格;
//  3. 只有一位标注员、样本不足 20 题、覆盖率低时, 报告自己先说"别信我"。
func (h *humanGoldHandler) Calibration(c *gin.Context) {
	runID := parseIntQuery(c, "run_id")
	if runID <= 0 {
		writeErr(c, http.StatusBadRequest, "run_id 必填且为正整数")
		return
	}
	ctx := c.Request.Context()
	run, err := h.runs.GetRun(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(c, http.StatusNotFound, "run 不存在")
		return
	}
	if err != nil {
		log.Printf("calibration get run %d: %v", runID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	annotator := strings.TrimSpace(c.Query("annotator"))
	gold, err := h.store.ListHumanGoldScores(ctx, runID, annotator)
	if err != nil {
		log.Printf("calibration list gold %d: %v", runID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	// 一次取该 run 的单题结果, 按 case_id 建索引: 有金标的题最多几十道,
	// 但逐题查询会是 N+1 次往返, 而这份结果本来就要整体读出来算 cases_total 兜底。
	// 上限与 /compare 一致(500), 超过时宁可少数几题不参与校准, 也不静默截断成错误的样本量。
	rows, err := h.runs.ListRunCaseResults(ctx, runID, maxCompareCases, false)
	if err != nil {
		log.Printf("calibration list case results %d: %v", runID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	byCase := make(map[int64]store.RunCaseResult, len(rows))
	for _, row := range rows {
		byCase[row.CaseID] = row
	}

	items := make([]eval.CalibrationItem, 0, len(gold))
	for _, score := range gold {
		row, ok := byCase[score.CaseID]
		item := eval.CalibrationItem{
			CaseID:           score.CaseID,
			Annotator:        score.Annotator,
			HumanVerdict:     score.Verdict,
			HumanRelevance:   score.Relevance,
			HumanHelpfulness: score.Helpfulness,
		}
		if ok {
			item.QID = row.QID
			// judgeSummary 复用 A/B 对比里的同一套抽取逻辑: 两处对"有没有判定结论"
			// 的判断必须一致, 否则对比说"这题判过"、校准说"没判过"。
			if summary := judgeSummary(row.Judge); summary != nil {
				item.JudgeHasOpinion = summary.Claims > 0
				item.JudgeUnsupported = summary.Unsupported
				item.JudgeRelevance = rubricScore(summary.HasRubric, summary.Relevance)
				item.JudgeHelpfulness = rubricScore(summary.HasRubric, summary.Helpfulness)
			}
		}
		items = append(items, item)
	}

	// cases_total 是"这次 run 一共多少题", 用来算金标覆盖率。
	// 老数据可能没有这个键 -> 退化成"本次取回的单题结果数", 宁可分母偏小也要给出覆盖率。
	totalCases := intField(run.Metrics, "cases_total", len(rows))
	report := eval.ComputeCalibration(runID, items, eval.CalibrationOptions{TotalCases: totalCases})
	if annotator != "" {
		// 过滤后的人工间一致性只在这个人的样本上算 —— 口径要写在报告里, 否则会被当成全局结论
		report.Notes = append(report.Notes, "已按 annotator="+annotator+" 过滤样本, 一致性口径仅限该标注员")
	}
	writeJSON(c, http.StatusOK, report)
}

// rubricScore 把 judge 的 rubric 分数转成金标侧的指针语义:
// 没开 rubric 或分数为 0(缺失) → nil, 表示"judge 这题没打分", 不能当成 0 分。
func rubricScore(hasRubric bool, value float64) *int {
	if !hasRubric || value <= 0 {
		return nil
	}
	score := int(value + 0.5)
	if score > 5 {
		score = 5
	}
	return &score
}

// validScore 允许 nil(不打分)或 1–5。
func validScore(value *int) bool {
	return value == nil || (*value >= 1 && *value <= 5)
}

// validScoreOrClear 额外允许 0, 表示"撤回这个分数"(PATCH 语义)。
func validScoreOrClear(value *int) bool {
	return value == nil || (*value >= 0 && *value <= 5)
}
