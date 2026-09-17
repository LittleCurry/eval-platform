package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/eval"
	"eval-platform/server/internal/store"
)

// stubHumanGold 内存版金标存储, 唯一键与 DB 一致: (run_id, case_id, annotator)。
type stubHumanGold struct {
	items     []store.HumanGoldScore
	nextID    int64
	listErr   error
	upsertErr error
	updateErr error
	deleteErr error

	gotAnnotator   string
	gotUpdateScore *int // 记录 PATCH 传下去的分数(0 = 撤回, 用来卡"清空"语义有没有被中间层吃掉)
}

func newStubHumanGold() *stubHumanGold {
	return &stubHumanGold{nextID: 1, items: []store.HumanGoldScore{}}
}

func (s *stubHumanGold) ListHumanGoldScores(
	_ context.Context, runID int64, annotator string,
) ([]store.HumanGoldScore, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	s.gotAnnotator = annotator
	out := make([]store.HumanGoldScore, 0)
	for _, item := range s.items {
		if runID != 0 && item.RunID != runID {
			continue
		}
		if annotator != "" && item.Annotator != annotator {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *stubHumanGold) UpsertHumanGoldScore(
	_ context.Context, in store.HumanGoldInput,
) (store.HumanGoldScore, error) {
	if s.upsertErr != nil {
		return store.HumanGoldScore{}, s.upsertErr
	}
	for index, item := range s.items {
		if item.RunID == in.RunID && item.CaseID == in.CaseID && item.Annotator == in.Annotator {
			// 与 DB 的 ON CONFLICT ... COALESCE(EXCLUDED.x, 原值) 一致:
			// verdict 必填直接覆盖, 其余字段只有"这次传了"才动
			item.Verdict = in.Verdict
			if in.Relevance != nil {
				item.Relevance = in.Relevance
			}
			if in.Helpfulness != nil {
				item.Helpfulness = in.Helpfulness
			}
			if in.Note != nil {
				item.Note = *in.Note
			}
			s.items[index] = item
			return item, nil
		}
	}
	item := store.HumanGoldScore{
		ID: s.nextID, ProjectID: in.ProjectID, RunID: in.RunID, CaseID: in.CaseID,
		Annotator: in.Annotator, Verdict: in.Verdict, Relevance: in.Relevance,
		Helpfulness: in.Helpfulness,
	}
	if in.Note != nil {
		item.Note = *in.Note
	}
	s.nextID++
	s.items = append(s.items, item)
	return item, nil
}

func (s *stubHumanGold) UpdateHumanGoldScore(
	_ context.Context, id int64, verdict *string, relevance, helpfulness *int,
	note *string, reviewed *bool,
) (store.HumanGoldScore, error) {
	if s.updateErr != nil {
		return store.HumanGoldScore{}, s.updateErr
	}
	s.gotUpdateScore = relevance
	for index, item := range s.items {
		if item.ID != id {
			continue
		}
		if verdict != nil {
			item.Verdict = *verdict
		}
		if relevance != nil {
			// 与 DB 的 CASE WHEN 一致: 0 = 撤回分数(置 NULL)
			if *relevance == 0 {
				item.Relevance = nil
			} else {
				value := *relevance
				item.Relevance = &value
			}
		}
		if helpfulness != nil {
			if *helpfulness == 0 {
				item.Helpfulness = nil
			} else {
				value := *helpfulness
				item.Helpfulness = &value
			}
		}
		if note != nil {
			item.Note = *note
		}
		if reviewed != nil {
			item.Reviewed = *reviewed
		}
		s.items[index] = item
		return item, nil
	}
	return store.HumanGoldScore{}, store.ErrNotFound
}

func (s *stubHumanGold) DeleteHumanGoldScore(_ context.Context, id int64) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	for index, item := range s.items {
		if item.ID == id {
			s.items = append(s.items[:index], s.items[index+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func goldRouter(gold HumanGoldStore, runs RunStore) http.Handler {
	gin.SetMode(gin.TestMode)
	return NewRouter(Deps{Postgres: fakePinger{}, Qdrant: fakePinger{}, HumanGold: gold, Runs: runs})
}

// goldRunStub 构造一次"已判定"的 run: 60 题, 4 道有金标。
//
//	zjc-011: judge 说"有幻觉"(unsupported 1), rubric 3/2
//	zjc-012: judge 说"没幻觉"(supported 1), rubric 5/4
//	zjc-013: judge 判了但 claims 为空 —— 没有可核查断言, 对幻觉没有意见
//	zjc-014: case_results 里根本没有这题
func goldRunStub() *stubRunStore {
	judged := func(labels ...string) map[string]any {
		claims := make([]any, 0, len(labels))
		for _, label := range labels {
			claims = append(claims, map[string]any{"label": label})
		}
		return map[string]any{"claims": claims}
	}
	withRubric := func(claims map[string]any, relevance, helpfulness float64) map[string]any {
		claims["rubric"] = map[string]any{"relevance": relevance, "helpfulness": helpfulness}
		return claims
	}
	return &stubRunStore{
		run: store.Run{ID: 155, Status: "succeeded", Metrics: map[string]any{"cases_total": float64(60)}},
		caseResults: []store.RunCaseResult{
			{CaseID: 11, QID: "zjc-011", Judge: withRubric(judged("unsupported"), 3, 2)},
			{CaseID: 12, QID: "zjc-012", Judge: withRubric(judged("supported"), 5, 4)},
			{CaseID: 13, QID: "zjc-013", Judge: judged()},
		},
	}
}

func goldRows() []store.HumanGoldScore {
	three, two, five, four := 3, 2, 5, 4
	return []store.HumanGoldScore{
		{ID: 1, RunID: 155, CaseID: 11, Annotator: "me", Verdict: "hallucinated",
			Relevance: &three, Helpfulness: &two},
		{ID: 2, RunID: 155, CaseID: 12, Annotator: "me", Verdict: "faithful",
			Relevance: &five, Helpfulness: &four},
		{ID: 3, RunID: 155, CaseID: 13, Annotator: "me", Verdict: "faithful",
			Relevance: &four, Helpfulness: &four},
		{ID: 4, RunID: 155, CaseID: 14, Annotator: "me", Verdict: "unclear"},
	}
}

// ---- CRUD ----

func TestHumanGoldUpsertAndList(t *testing.T) {
	gold := newStubHumanGold()
	r := goldRouter(gold, goldRunStub())

	w := doJSON(t, r, http.MethodPost, "/api/v1/human-gold",
		`{"run_id":155,"case_id":11,"annotator":" me ","verdict":"hallucinated","relevance":3,"helpfulness":2,"note":" 编了行数上限 "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("首次打分应 200: %d (%s)", w.Code, w.Body.String())
	}
	var created store.HumanGoldScore
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if created.Annotator != "me" {
		t.Fatalf("annotator 应去除首尾空格: %q", created.Annotator)
	}
	if created.Note != "编了行数上限" {
		t.Fatalf("note 应去除首尾空格: %q", created.Note)
	}
	if created.Relevance == nil || *created.Relevance != 3 {
		t.Fatalf("分数未落库: %+v", created)
	}
	if created.ProjectID != 1 {
		t.Fatalf("未传 project_id 时应落到默认项目 1: %d", created.ProjectID)
	}

	// 同一人同一题再 POST = 合并更新(连续录入, 改主意是常态), 不产生第二条记录
	w = doJSON(t, r, http.MethodPost, "/api/v1/human-gold",
		`{"run_id":155,"case_id":11,"annotator":"me","verdict":"faithful","helpfulness":3}`)
	if w.Code != http.StatusOK {
		t.Fatalf("重复打分应 200(覆盖): %d", w.Code)
	}
	if len(gold.items) != 1 {
		t.Fatalf("同一人同一题只能有一条: %d", len(gold.items))
	}
	if gold.items[0].Verdict != "faithful" {
		t.Fatalf("覆盖未生效: %+v", gold.items[0])
	}
	// 这次只传了 verdict 与 helpfulness: 上次的 relevance 与备注必须原样保留。
	// (整行覆盖的话, 校准报告里的分数对会凭空少掉 —— 真机就是这么发现的。)
	if gold.items[0].Relevance == nil || *gold.items[0].Relevance != 3 {
		t.Fatalf("没传的分数不该被冲掉: %+v", gold.items[0].Relevance)
	}
	if gold.items[0].Note != "编了行数上限" {
		t.Fatalf("没传的备注不该被冲掉: %q", gold.items[0].Note)
	}
	if gold.items[0].Helpfulness == nil || *gold.items[0].Helpfulness != 3 {
		t.Fatalf("传了的分数要更新: %+v", gold.items[0].Helpfulness)
	}

	// 另一个人对同一题打分 → 第二条(双人独立打分的前提)
	doJSON(t, r, http.MethodPost, "/api/v1/human-gold",
		`{"run_id":155,"case_id":11,"annotator":"bob","verdict":"unclear"}`)
	if len(gold.items) != 2 {
		t.Fatalf("不同 annotator 应各存一条: %d", len(gold.items))
	}

	w = doJSON(t, r, http.MethodGet, "/api/v1/human-gold?run_id=155", "")
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200: %d", w.Code)
	}
	var listed []store.HumanGoldScore
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("列表响应不是 JSON 数组: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("该 run 应有 2 条金标: %+v", listed)
	}

	doJSON(t, r, http.MethodGet, "/api/v1/human-gold?run_id=155&annotator=bob", "")
	if gold.gotAnnotator != "bob" {
		t.Fatalf("annotator 过滤未透传: %q", gold.gotAnnotator)
	}
	if w := doJSON(t, r, http.MethodGet, "/api/v1/human-gold", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("缺 run_id 应 400: %d", w.Code)
	}
}

func TestHumanGoldValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"缺 run/case", `{"annotator":"me","verdict":"faithful"}`, "run_id"},
		{"缺标注员", `{"run_id":155,"case_id":11,"verdict":"faithful"}`, "annotator 必填"},
		{"标注员全是空格", `{"run_id":155,"case_id":11,"annotator":"   ","verdict":"faithful"}`, "annotator 必填"},
		{"verdict 非法", `{"run_id":155,"case_id":11,"annotator":"me","verdict":"maybe"}`, "verdict 必须是"},
		{"verdict 缺失", `{"run_id":155,"case_id":11,"annotator":"me"}`, "verdict 必须是"},
		{"relevance 越界", `{"run_id":155,"case_id":11,"annotator":"me","verdict":"faithful","relevance":6}`, "relevance"},
		{"relevance 为 0", `{"run_id":155,"case_id":11,"annotator":"me","verdict":"faithful","relevance":0}`, "relevance"},
		{"helpfulness 越界", `{"run_id":155,"case_id":11,"annotator":"me","verdict":"faithful","helpfulness":9}`, "helpfulness"},
	}
	for _, item := range cases {
		gold := newStubHumanGold()
		w := doJSON(t, goldRouter(gold, goldRunStub()), http.MethodPost, "/api/v1/human-gold", item.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code = %d, want 400 (%s)", item.name, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), item.want) {
			t.Fatalf("%s: 错误信息应提到 %q, 实际 %s", item.name, item.want, w.Body.String())
		}
		if len(gold.items) != 0 {
			t.Fatalf("%s: 校验失败不该写入: %+v", item.name, gold.items)
		}
	}
}

func TestHumanGoldPatchAndDelete(t *testing.T) {
	gold := newStubHumanGold()
	gold.items = goldRows()[:1]
	r := goldRouter(gold, goldRunStub())

	// 改判 + 改分: 没传的字段不动
	w := doJSON(t, r, http.MethodPatch, "/api/v1/human-gold/1",
		`{"verdict":"faithful","helpfulness":4,"note":" 复核后改判 "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH 应 200: %d (%s)", w.Code, w.Body.String())
	}
	item := gold.items[0]
	if item.Verdict != "faithful" || item.Note != "复核后改判" {
		t.Fatalf("更新未生效/未去空格: %+v", item)
	}
	if item.Helpfulness == nil || *item.Helpfulness != 4 {
		t.Fatalf("helpfulness 应更新为 4: %+v", item.Helpfulness)
	}
	if item.Relevance == nil || *item.Relevance != 3 {
		t.Fatalf("没传 relevance 必须保持不变: %+v", item.Relevance)
	}

	// 传 0 = 撤回这个分数(置 NULL): 打错了分不必删掉整条记录重来
	if w := doJSON(t, r, http.MethodPatch, "/api/v1/human-gold/1", `{"relevance":0}`); w.Code != http.StatusOK {
		t.Fatalf("撤回分数应 200: %d (%s)", w.Code, w.Body.String())
	}
	if gold.gotUpdateScore == nil || *gold.gotUpdateScore != 0 {
		t.Fatalf("撤回语义被中间层吃掉了: %v", gold.gotUpdateScore)
	}
	if gold.items[0].Relevance != nil {
		t.Fatalf("0 应把分数置为 NULL: %+v", gold.items[0].Relevance)
	}
	if gold.items[0].Verdict != "faithful" {
		t.Fatalf("撤回分数不该动 verdict: %+v", gold.items[0])
	}

	if w := doJSON(t, r, http.MethodPatch, "/api/v1/human-gold/1", `{"verdict":"maybe"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("非法 verdict 应 400: %d", w.Code)
	}
	if w := doJSON(t, r, http.MethodPatch, "/api/v1/human-gold/1", `{"helpfulness":6}`); w.Code != http.StatusBadRequest {
		t.Fatalf("越界分数应 400: %d", w.Code)
	}
	if w := doJSON(t, r, http.MethodPatch, "/api/v1/human-gold/999", `{"verdict":"faithful"}`); w.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404: %d", w.Code)
	}

	if w := doJSON(t, r, http.MethodDelete, "/api/v1/human-gold/1", ""); w.Code != http.StatusNoContent {
		t.Fatalf("删除应 204: %d", w.Code)
	}
	if w := doJSON(t, r, http.MethodDelete, "/api/v1/human-gold/1", ""); w.Code != http.StatusNotFound {
		t.Fatalf("重复删除应 404: %d", w.Code)
	}
}

func TestHumanGoldStoreErrorsSurfaced(t *testing.T) {
	gold := newStubHumanGold()
	gold.listErr = errors.New("boom")
	if w := doJSON(t, goldRouter(gold, goldRunStub()), http.MethodGet,
		"/api/v1/human-gold?run_id=155", ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("查询失败应 500: %d", w.Code)
	}

	gold = newStubHumanGold()
	gold.upsertErr = store.ErrNotFound
	if w := doJSON(t, goldRouter(gold, goldRunStub()), http.MethodPost, "/api/v1/human-gold",
		`{"run_id":999,"case_id":999,"annotator":"me","verdict":"faithful"}`); w.Code != http.StatusNotFound {
		t.Fatalf("run/case 不存在应 404: %d", w.Code)
	}

	gold = newStubHumanGold()
	gold.deleteErr = errors.New("boom")
	if w := doJSON(t, goldRouter(gold, goldRunStub()), http.MethodDelete,
		"/api/v1/human-gold/1", ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("删除失败应 500: %d", w.Code)
	}
}

// ---- 校准报告 ----

func TestJudgeCalibrationReport(t *testing.T) {
	gold := newStubHumanGold()
	gold.items = goldRows()
	r := goldRouter(gold, goldRunStub())

	w := doJSON(t, r, http.MethodGet, "/api/v1/judge-calibration?run_id=155", "")
	if w.Code != http.StatusOK {
		t.Fatalf("校准报告应 200: %d (%s)", w.Code, w.Body.String())
	}
	var report eval.CalibrationReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("响应不是校准报告 JSON: %v", err)
	}

	if report.RunID != 155 || report.TotalCases != 60 || report.CasesWithGold != 4 {
		t.Fatalf("报告头部不符: %+v", report)
	}
	if report.Coverage != 0.0667 {
		t.Fatalf("覆盖率应为 4/60≈0.0667: %.4f", report.Coverage)
	}
	if report.PrimaryAnnotator != "me" {
		t.Fatalf("主标注员应为 me: %q", report.PrimaryAnnotator)
	}

	// 二分类: 11 题命中(TP)、12 题正确否决(TN); 13 题 judge 无结论、14 题人工"看不清"
	if report.Binary == nil {
		t.Fatal("应有二分类校准结果")
	}
	if report.Binary.Pairs != 2 || report.Binary.TruePositive != 1 || report.Binary.TrueNegative != 1 {
		t.Fatalf("二分类样本不符: %+v", report.Binary)
	}
	if report.Binary.FalsePositive != 0 || report.Binary.FalseNegative != 0 {
		t.Fatalf("不该有误判: %+v", report.Binary)
	}
	if report.Binary.Agreement != 1 || report.Binary.Kappa != 1 {
		t.Fatalf("2 题全对且两侧各占一半: 一致率与 κ 都应为 1, 实际 %.4f/%.4f",
			report.Binary.Agreement, report.Binary.Kappa)
	}
	if report.Binary.ExcludedUnclear != 1 {
		t.Fatalf("应由\"看不清\"排除 1 题: %+v", report.Binary)
	}
	if report.GoldJudged != 2 || report.GoldUnjudged != 2 {
		t.Fatalf("judge 有结论/无结论应为 2/2, 实际 %d/%d", report.GoldJudged, report.GoldUnjudged)
	}

	// 分数: helpfulness 人工(2,4) vs judge(2,4) —— 精确一致; relevance 人工(3,5) vs judge(3,5)
	if report.Helpfulness == nil || report.Helpfulness.Pairs != 2 {
		t.Fatalf("helpfulness 应有 2 对: %+v", report.Helpfulness)
	}
	if report.Helpfulness.MAE != 0 || report.Helpfulness.ExactAgreement != 1 {
		t.Fatalf("分数完全一致: MAE 0 / 一致率 1, 实际 %.4f/%.4f",
			report.Helpfulness.MAE, report.Helpfulness.ExactAgreement)
	}
	if report.Relevance == nil || report.Relevance.Bias != 0 {
		t.Fatalf("relevance 偏差应为 0: %+v", report.Relevance)
	}
	if report.InterAnnotator != nil {
		t.Fatalf("只有一位标注员时不该有人工间一致性: %+v", report.InterAnnotator)
	}

	joined := strings.Join(report.Notes, "\n")
	for _, want := range []string{"只有一位标注员", "少于 20 题", "覆盖率低于 50%", "看不清", "没有给出判定结论"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("提示里缺少 %q: %s", want, joined)
		}
	}
}

// TestJudgeCalibrationWithoutJudge 只跑检索的 run(没有判定): 不能给出"零幻觉"的假结论,
// 二分类段应当为空, 并说清原因。
func TestJudgeCalibrationWithoutJudge(t *testing.T) {
	gold := newStubHumanGold()
	gold.items = goldRows()
	runs := goldRunStub()
	runs.caseResults = []store.RunCaseResult{{CaseID: 11, QID: "zjc-011"}, {CaseID: 12, QID: "zjc-012"}}

	w := doJSON(t, goldRouter(gold, runs), http.MethodGet, "/api/v1/judge-calibration?run_id=155", "")
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d (%s)", w.Code, w.Body.String())
	}
	var report eval.CalibrationReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if report.Binary != nil {
		t.Fatalf("没有判定结果时不该有二分类结论: %+v", report.Binary)
	}
	if report.GoldJudged != 0 || report.GoldUnjudged != 4 {
		t.Fatalf("4 题都应记为\"judge 无结论\", 实际 %d/%d", report.GoldJudged, report.GoldUnjudged)
	}
	if report.Helpfulness != nil || report.Relevance != nil {
		t.Fatalf("没有 rubric 时不该有分数校准: %+v / %+v", report.Helpfulness, report.Relevance)
	}
	if !strings.Contains(strings.Join(report.Notes, "\n"), "没有给出判定结论") {
		t.Fatalf("要说清为什么没有校准结论: %v", report.Notes)
	}
}

// TestJudgeCalibrationAnnotatorFilter 按标注员过滤时, 报告要写明口径变了。
func TestJudgeCalibrationAnnotatorFilter(t *testing.T) {
	gold := newStubHumanGold()
	gold.items = goldRows()
	r := goldRouter(gold, goldRunStub())

	w := doJSON(t, r, http.MethodGet, "/api/v1/judge-calibration?run_id=155&annotator=me", "")
	var report eval.CalibrationReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if !strings.Contains(strings.Join(report.Notes, "\n"), "annotator=me") {
		t.Fatalf("过滤样本时要在报告里写明口径: %v", report.Notes)
	}
}

func TestJudgeCalibrationNoGoldYet(t *testing.T) {
	r := goldRouter(newStubHumanGold(), goldRunStub())
	w := doJSON(t, r, http.MethodGet, "/api/v1/judge-calibration?run_id=155", "")
	if w.Code != http.StatusOK {
		t.Fatalf("还没有金标也应 200(报告里说清即可): %d", w.Code)
	}
	var report eval.CalibrationReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if report.CasesWithGold != 0 || report.Binary != nil {
		t.Fatalf("没有金标不该有校准结果: %+v", report)
	}
	if !strings.Contains(strings.Join(report.Notes, "\n"), "还没有人工金标") {
		t.Fatalf("要说清先打分: %v", report.Notes)
	}

	if w := doJSON(t, r, http.MethodGet, "/api/v1/judge-calibration", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("缺 run_id 应 400: %d", w.Code)
	}
	notFound := &stubRunStore{getErr: store.ErrNotFound}
	if w := doJSON(t, goldRouter(newStubHumanGold(), notFound), http.MethodGet,
		"/api/v1/judge-calibration?run_id=999", ""); w.Code != http.StatusNotFound {
		t.Fatalf("run 不存在应 404: %d", w.Code)
	}
}

// TestJudgeCalibrationCountsInterAnnotator 两个人各打一遍: 主标注员只用于 judge 校准,
// 人工之间的一致性另外给 —— 这两件事混在一起会把"两人不一致"误读成"judge 不准"。
func TestJudgeCalibrationCountsInterAnnotator(t *testing.T) {
	gold := newStubHumanGold()
	rows := goldRows()
	gold.items = rows
	// bob 对 11、12 两题独立打分且与 me 不同: 11 题改判 faithful、12 题改判 hallucinated
	three := 3
	gold.items = append(gold.items,
		store.HumanGoldScore{ID: 11, RunID: 155, CaseID: 11, Annotator: "bob",
			Verdict: "faithful", Helpfulness: &three},
		store.HumanGoldScore{ID: 12, RunID: 155, CaseID: 12, Annotator: "bob",
			Verdict: "hallucinated", Helpfulness: &three},
	)
	r := goldRouter(gold, goldRunStub())

	w := doJSON(t, r, http.MethodGet, "/api/v1/judge-calibration?run_id=155", "")
	var report eval.CalibrationReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if report.PrimaryAnnotator != "me" {
		t.Fatalf("两人题数相同(4:2)时应取 more/字典序稳定结果, 实际 %q", report.PrimaryAnnotator)
	}
	if report.InterAnnotator == nil {
		t.Fatal("应有两个人之间的一致性")
	}
	if report.InterAnnotator.BinaryPairs != 2 {
		t.Fatalf("重合题应为 2: %+v", report.InterAnnotator)
	}
	// me=[阳性, 阴性], bob=[阴性, 阳性] → po=0, pe=0.5 → κ=-1
	if report.InterAnnotator.BinaryKappa == nil || *report.InterAnnotator.BinaryKappa != -1 {
		t.Fatalf("两人结论完全相反: κ 应为 -1, 实际 %v", report.InterAnnotator.BinaryKappa)
	}
	joined := strings.Join(report.Notes, "\n")
	if strings.Contains(joined, "只有一位标注员") {
		t.Fatalf("有两位标注员时不该提示只有一位: %s", joined)
	}
}

// ---- rubricScore(judge 分数 -> 金标指针语义) ----

func TestRubricScoreMapping(t *testing.T) {
	if got := rubricScore(false, 4); got != nil {
		t.Fatalf("没开 rubric 时应为 nil(judge 没打分): %v", *got)
	}
	if got := rubricScore(true, 0); got != nil {
		t.Fatalf("分数缺失(0)时应为 nil, 不能当成 0 分算进 MAE: %v", *got)
	}
	if got := rubricScore(true, 5); got == nil || *got != 5 {
		t.Fatalf("5 分应映射为 5: %v", got)
	}
	if got := rubricScore(true, 4.6); got == nil || *got != 5 {
		t.Fatalf("4.6 应四舍五入为 5: %v", got)
	}
	if got := rubricScore(true, 9); got == nil || *got != 5 {
		t.Fatalf("超出 5 分应夹到 5(尺度漂移时不至于污染均值): %v", got)
	}
}
