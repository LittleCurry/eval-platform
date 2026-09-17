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

// closureRunStub 需要"两次 run 各有各的单题结果", 而 runs_test.go 的 stub 对所有 id 返回同一份。
// 用嵌入 + 覆盖两个方法, 不改动既有测试文件(避免为了新测试去改已提交的 stub)。
type closureRunStub struct {
	*stubRunStore
	byRun map[int64][]store.RunCaseResult
	runs  map[int64]store.Run
}

func newClosureRunStub() *closureRunStub {
	return &closureRunStub{
		stubRunStore: &stubRunStore{},
		byRun:        map[int64][]store.RunCaseResult{},
		runs:         map[int64]store.Run{},
	}
}

func (s *closureRunStub) GetRun(_ context.Context, id int64) (store.Run, error) {
	run, ok := s.runs[id]
	if !ok {
		return store.Run{}, store.ErrNotFound
	}
	return run, nil
}

func (s *closureRunStub) ListRunCaseResults(
	_ context.Context, runID int64, _ int, _ bool,
) ([]store.RunCaseResult, error) {
	return s.byRun[runID], nil
}

// judgedRun 造一次"归因已写过"的 run: metrics 里带 attribution 键(D15 的凭据)。
func judgedRun(id, datasetID int64, hash string) store.Run {
	return store.Run{
		ID: id, DatasetID: datasetID, ConfigHash: hash, Status: "succeeded",
		Metrics: map[string]any{"attribution": map[string]any{"version": float64(1)}},
	}
}

func closureCaseRow(caseID int64, qid string, recall float64, flags ...string) store.RunCaseResult {
	return store.RunCaseResult{
		CaseID: caseID, QID: qid, Question: "问题 " + qid,
		Metrics: map[string]any{"recall": recall, "precision": 0.2},
		Flags:   flags,
	}
}

// closureRouter 组装一个只带闭环所需依赖的路由。
func closureRouter(annotations AnnotationStore, runs RunStore) http.Handler {
	gin.SetMode(gin.TestMode)
	return NewRouter(Deps{Postgres: fakePinger{}, Qdrant: fakePinger{},
		Runs: runs, Annotations: annotations})
}

func closureFixture() (*closureRunStub, *stubAnnotations) {
	runs := newClosureRunStub()
	runs.runs[112] = judgedRun(112, 4, "712f6b17")
	runs.runs[100] = judgedRun(100, 4, "4e767020")
	runs.byRun[112] = []store.RunCaseResult{
		closureCaseRow(61, "zjc-027", 0, "retrieval_miss"),
		closureCaseRow(62, "zjc-028", 0, "retrieval_miss"),
		closureCaseRow(63, "zjc-029", 0, "retrieval_partial"),
	}
	runs.byRun[100] = []store.RunCaseResult{
		closureCaseRow(61, "zjc-027", 1),                         // 修好了
		closureCaseRow(62, "zjc-028", 0, "retrieval_miss"),       // 没变
		closureCaseRow(63, "zjc-029", 0.5, "retrieval_low_rank"), // 换了病
	}
	annotations := newStubAnnotations()
	annotations.items = []store.Annotation{
		{ID: 1, ProjectID: 1, RunID: 112, CaseID: 61, Status: "fixed", Reason: "retrieval",
			Comment: "把 k 提到 5"},
		{ID: 2, ProjectID: 1, RunID: 112, CaseID: 62, Status: "fixed", Reason: "retrieval"},
		{ID: 3, ProjectID: 1, RunID: 112, CaseID: 63, Status: "open", Reason: "retrieval"},
	}
	return runs, annotations
}

func TestClosureReport(t *testing.T) {
	runs, annotations := closureFixture()
	w := doJSON(t, closureRouter(annotations, runs), http.MethodGet,
		"/api/v1/closure?baseline=112&candidate=100", "")

	if w.Code != http.StatusOK {
		t.Fatalf("闭环报告应 200: %d (%s)", w.Code, w.Body.String())
	}
	var report eval.ClosureReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("响应不是闭环报告 JSON: %v", err)
	}
	if report.Baseline != 112 || report.Candidate != 100 {
		t.Fatalf("方向必须是 baseline=112 -> candidate=100: %+v", report)
	}
	if !report.Comparable || report.SameConfig {
		t.Fatalf("两次配置不同, 应可比且非复现: %+v", report)
	}
	if report.Summary.Annotated != 3 || report.Summary.Improved != 1 ||
		report.Summary.Stable != 1 || report.Summary.Changed != 1 {
		t.Fatalf("结汇总不对: %+v", report.Summary)
	}
	if report.Summary.FixedTotal != 2 || report.Summary.EligibleForVerify != 1 {
		t.Fatalf("2 道 fixed 里只有 1 道真的修好: %+v", report.Summary)
	}
	if len(report.Records) != 3 {
		t.Fatalf("应有 3 条记录: %d", len(report.Records))
	}

	first := report.Records[0]
	if first.QID != "zjc-027" || first.Verdict != eval.ClosureImproved || !first.EligibleForVerify {
		t.Fatalf("可销单的题应在最前: %+v", first)
	}
	if first.AnnotationID != 1 {
		t.Fatalf("要带上标注 id, 前端才能一键销单: %+v", first)
	}
	if first.Comment != "把 k 提到 5" {
		t.Fatalf("标注评论要带出来(销单时要能回看当时怎么记的): %q", first.Comment)
	}
	if len(first.Evidence) == 0 {
		t.Fatal("每条记录都要带指标证据")
	}
	var recall *eval.ClosureEvidence
	for index := range first.Evidence {
		if first.Evidence[index].Metric == "recall" {
			recall = &first.Evidence[index]
		}
	}
	if recall == nil || recall.Left != 0 || recall.Right != 1 || recall.Direction != eval.EvidenceBetter {
		t.Fatalf("recall 证据应为 0 -> 1 且 better: %+v", recall)
	}

	// 换了病的那道题(open 状态): 结局是 changed, 且说清前后标签
	var changed eval.ClosureRecord
	for _, record := range report.Records {
		if record.QID == "zjc-029" {
			changed = record
		}
	}
	if changed.Verdict != eval.ClosureChanged {
		t.Fatalf("zjc-029 应是 changed: %+v", changed)
	}
	if !strings.Contains(changed.BlockedReason, "retrieval_low_rank") {
		t.Fatalf("要说清换成了什么病: %q", changed.BlockedReason)
	}
}

// TestClosureRequiresBaselineDirection 方向反了要给出相反的结论 —— 这正是接口要防的误用。
func TestClosureDirectionMatters(t *testing.T) {
	runs, annotations := closureFixture()
	// 反向: baseline=100(好的那次), candidate=112(坏的那次)
	reversed := newStubAnnotations()
	reversed.items = []store.Annotation{
		{ID: 9, ProjectID: 1, RunID: 100, CaseID: 61, Status: "fixed", Reason: "retrieval"},
	}
	w := doJSON(t, closureRouter(reversed, runs), http.MethodGet,
		"/api/v1/closure?baseline=100&candidate=112", "")
	var report eval.ClosureReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if report.Summary.Worsened != 1 || report.Summary.Improved != 0 {
		t.Fatalf("反着看应报\"变坏\": %+v", report.Summary)
	}
	_ = annotations
}

func TestClosureValidation(t *testing.T) {
	runs, annotations := closureFixture()
	r := closureRouter(annotations, runs)

	for _, item := range []struct{ name, path, want string }{
		{"缺 candidate", "/api/v1/closure?baseline=112", "baseline 与 candidate"},
		{"同一个 run", "/api/v1/closure?baseline=112&candidate=112", "不能是同一次 run"},
		{"状态非法", "/api/v1/closure?baseline=112&candidate=100&status=done", "status 必须是"},
	} {
		w := doJSON(t, r, http.MethodGet, item.path, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code = %d, want 400 (%s)", item.name, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), item.want) {
			t.Fatalf("%s: 错误信息应提到 %q, 实际 %s", item.name, item.want, w.Body.String())
		}
	}

	if w := doJSON(t, r, http.MethodGet, "/api/v1/closure?baseline=999&candidate=100", ""); w.Code != http.StatusNotFound {
		t.Fatalf("baseline 不存在应 404: %d", w.Code)
	}
	if w := doJSON(t, r, http.MethodGet, "/api/v1/closure?baseline=112&candidate=999", ""); w.Code != http.StatusNotFound {
		t.Fatalf("candidate 不存在应 404: %d", w.Code)
	}
	if !strings.Contains(doJSON(t, r, http.MethodGet, "/api/v1/closure?baseline=999&candidate=100", "").Body.String(),
		"baseline") {
		t.Fatal("404 要指明是哪一侧的 run")
	}
}

// TestClosureCrossDataset 跨评测集: 明确判不可比, 而不是给出一个看起来很专业的数字。
func TestClosureCrossDataset(t *testing.T) {
	runs, annotations := closureFixture()
	runs.runs[3] = judgedRun(3, 3, "b6598ed9")
	runs.byRun[3] = []store.RunCaseResult{closureCaseRow(1, "q-1", 1)}

	w := doJSON(t, closureRouter(annotations, runs), http.MethodGet,
		"/api/v1/closure?baseline=112&candidate=3", "")
	if w.Code != http.StatusOK {
		t.Fatalf("跨集应 200 + 说清原因: %d", w.Code)
	}
	var report eval.ClosureReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if report.Comparable || !strings.Contains(report.Reason, "同一个评测集") {
		t.Fatalf("跨集必须判不可比并说明: %+v", report)
	}
}

// TestClosureAttributionMissingSurfaced 一侧没做过归因时, 不能报"全修好了"。
func TestClosureAttributionMissingSurfaced(t *testing.T) {
	runs, annotations := closureFixture()
	runs.runs[100] = store.Run{ID: 100, DatasetID: 4, ConfigHash: "4e767020", Status: "succeeded"}

	w := doJSON(t, closureRouter(annotations, runs), http.MethodGet,
		"/api/v1/closure?baseline=112&candidate=100", "")
	var report eval.ClosureReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if report.Comparable || report.Summary.Improved != 0 {
		t.Fatalf("候选 run 没归因时不能报修好了: %+v", report)
	}
	if !strings.Contains(report.AttributionMissing, "make attribution") {
		t.Fatalf("要给可执行的补救指令: %q", report.AttributionMissing)
	}
}

// TestClosureNoAnnotations 基线还没标注过: 200 + 说清下一步。
func TestClosureNoAnnotations(t *testing.T) {
	runs, _ := closureFixture()
	w := doJSON(t, closureRouter(newStubAnnotations(), runs), http.MethodGet,
		"/api/v1/closure?baseline=112&candidate=100", "")
	if w.Code != http.StatusOK {
		t.Fatalf("应 200(报告里说清即可): %d", w.Code)
	}
	var report eval.ClosureReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if report.Summary.Annotated != 0 {
		t.Fatalf("没有标注应为 0: %+v", report.Summary)
	}
	if !strings.Contains(strings.Join(report.Notes, "\n"), "还没有任何人工标注") {
		t.Fatalf("要说清先去标注: %v", report.Notes)
	}
}

func TestClosureStatusFilterAndStoreErrors(t *testing.T) {
	runs, annotations := closureFixture()
	w := doJSON(t, closureRouter(annotations, runs), http.MethodGet,
		"/api/v1/closure?baseline=112&candidate=100&status=fixed", "")
	var report eval.ClosureReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if report.Summary.Annotated != 2 || len(report.Records) != 2 {
		t.Fatalf("只看 fixed 应只有 2 条: %+v", report.Summary)
	}

	broken := newStubAnnotations()
	broken.listErr = errors.New("boom")
	if w := doJSON(t, closureRouter(broken, runs), http.MethodGet,
		"/api/v1/closure?baseline=112&candidate=100", ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("查标注失败应 500: %d", w.Code)
	}
}
