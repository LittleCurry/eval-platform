package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/eval"
	"eval-platform/server/internal/store"
)

// abStub 按 run id 返回不同的 run 与单题结果。
// 复用 stubRunStore(嵌入后其余方法照旧), 只覆盖这两个 —— 不必改动 runs_test.go。
type abStub struct {
	*stubRunStore
	runsByID  map[int64]store.Run
	casesByID map[int64][]store.RunCaseResult
}

func newABStub() *abStub {
	return &abStub{
		stubRunStore: &stubRunStore{},
		runsByID:     map[int64]store.Run{},
		casesByID:    map[int64][]store.RunCaseResult{},
	}
}

func (s *abStub) GetRun(_ context.Context, id int64) (store.Run, error) {
	run, ok := s.runsByID[id]
	if !ok {
		return store.Run{}, store.ErrNotFound
	}
	return run, nil
}

func (s *abStub) ListRunCaseResults(
	_ context.Context, runID int64, _ int, _ bool,
) ([]store.RunCaseResult, error) {
	return s.casesByID[runID], nil
}

func compareRouter(runs RunStore) http.Handler {
	gin.SetMode(gin.TestMode)
	return NewRouter(Deps{Postgres: fakePinger{}, Qdrant: fakePinger{}, Runs: runs})
}

func abCaseRow(qid string, recall float64, flags ...string) store.RunCaseResult {
	return store.RunCaseResult{
		QID:      qid,
		Category: "线索",
		Metrics:  map[string]any{"recall": recall, "reciprocal_rank": recall, "hit": recall, "precision": recall},
		Flags:    flags,
	}
}

func decodeAB(t *testing.T, raw string) eval.ABReport {
	t.Helper()
	var report eval.ABReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, raw)
	}
	return report
}

func TestCompareReportsFixedAndBrokeWithNoiseFloor(t *testing.T) {
	stub := newABStub()
	stub.runsByID[100] = store.Run{ID: 100, DatasetID: 4}
	stub.runsByID[112] = store.Run{ID: 112, DatasetID: 4}
	stub.casesByID[112] = []store.RunCaseResult{
		abCaseRow("zjc-003", 0, "retrieval_miss"),
		abCaseRow("zjc-006", 0, "retrieval_miss"),
		abCaseRow("zjc-027", 1),
	}
	stub.casesByID[100] = []store.RunCaseResult{
		abCaseRow("zjc-003", 1),
		abCaseRow("zjc-006", 1),
		abCaseRow("zjc-027", 0, "hallucination"),
	}

	w := doJSON(t, compareRouter(stub), http.MethodGet, "/api/v1/compare?left=112&right=100", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	report := decodeAB(t, w.Body.String())
	if report.Left != 112 || report.Right != 100 {
		t.Fatalf("run id 未回传: %+v", report)
	}
	if !report.Comparable || report.SharedCases != 3 {
		t.Fatalf("三次同题集应当可比: %+v (%s)", report, report.Reason)
	}
	if len(report.Fixed) != 2 || report.Fixed[0].QID != "zjc-003" {
		t.Fatalf("两题修好: %+v", report.Fixed)
	}
	if report.Fixed[0].FlagsLeft[0] != "retrieval_miss" || len(report.Fixed[0].FlagsRight) != 0 {
		t.Fatalf("修好题要带两侧标签: %+v", report.Fixed[0])
	}
	if len(report.Broke) != 1 || report.Broke[0].QID != "zjc-027" {
		t.Fatalf("一题变坏: %+v", report.Broke)
	}
	recall := report.Summary["recall"]
	if recall.Left != 0.333333 || recall.Right != 0.666667 {
		t.Fatalf("均值差计算不符: %+v", recall)
	}
	if recall.Delta <= 0 || recall.Improved != 2 || recall.Worsened != 1 {
		t.Fatalf("改善/恶化题数不符: %+v", recall)
	}
	if report.NoiseFloor != eval.DefaultNoiseFloor {
		t.Fatalf("必须回传噪声底, 否则前端没法解释「变化」: %v", report.NoiseFloor)
	}
}

func TestCompareIdenticalRunsShowNoDifference(t *testing.T) {
	// 负控: 演练对 #100/#101 同配置同结果 -> 不许报出任何差异
	same := []store.RunCaseResult{
		abCaseRow("q1", 1),
		abCaseRow("q2", 0, "retrieval_miss"),
	}
	stub := newABStub()
	stub.runsByID[100] = store.Run{ID: 100, DatasetID: 4}
	stub.runsByID[101] = store.Run{ID: 101, DatasetID: 4}
	stub.casesByID[100] = same
	stub.casesByID[101] = same

	w := doJSON(t, compareRouter(stub), http.MethodGet, "/api/v1/compare?left=100&right=101", "")

	report := decodeAB(t, w.Body.String())
	recall := report.Summary["recall"]
	if recall.Delta != 0 || recall.Significant {
		t.Fatalf("同配置不该有差异: %+v", recall)
	}
	if len(report.Fixed)+len(report.Broke)+len(report.Changed) != 0 {
		t.Fatalf("不该出现翻转题: %+v", report)
	}
}

func TestCompareDifferentDatasetIsIncomparableNotFatal(t *testing.T) {
	stub := newABStub()
	stub.runsByID[3] = store.Run{ID: 3, DatasetID: 3}
	stub.runsByID[100] = store.Run{ID: 100, DatasetID: 4}
	stub.casesByID[3] = []store.RunCaseResult{abCaseRow("zjc-001", 1)}
	stub.casesByID[100] = []store.RunCaseResult{abCaseRow("zjc-001", 1)}

	w := doJSON(t, compareRouter(stub), http.MethodGet, "/api/v1/compare?left=3&right=100", "")

	if w.Code != http.StatusOK {
		t.Fatalf("跨评测集应返回 200 + 说明, 而不是报错: %d", w.Code)
	}
	report := decodeAB(t, w.Body.String())
	if report.Comparable {
		t.Fatal("不同评测集必须判不可比")
	}
	if !strings.Contains(report.Reason, "dataset") {
		t.Fatalf("原因要说清是评测集不同: %q", report.Reason)
	}
	if len(report.Summary) != 0 {
		t.Fatalf("不可比时不该给指标, 免得被误读: %+v", report.Summary)
	}
}

func TestCompareTooFewSharedCasesIsIncomparable(t *testing.T) {
	stub := newABStub()
	stub.runsByID[7] = store.Run{ID: 7, DatasetID: 4}
	stub.runsByID[8] = store.Run{ID: 8, DatasetID: 4}
	stub.casesByID[7] = []store.RunCaseResult{abCaseRow("q1", 1), abCaseRow("q2", 1), abCaseRow("q3", 1)}
	stub.casesByID[8] = []store.RunCaseResult{abCaseRow("q1", 1), abCaseRow("q9", 1)}

	w := doJSON(t, compareRouter(stub), http.MethodGet, "/api/v1/compare?left=7&right=8", "")

	report := decodeAB(t, w.Body.String())
	if report.Comparable || !strings.Contains(report.Reason, "共同题目") {
		t.Fatalf("共同题目不足应判不可比: %+v (%q)", report, report.Reason)
	}
}

func TestCompareRejectsBadParameters(t *testing.T) {
	stub := newABStub()
	stub.runsByID[100] = store.Run{ID: 100, DatasetID: 4}

	cases := []string{
		"/api/v1/compare",
		"/api/v1/compare?left=100",
		"/api/v1/compare?left=abc&right=100",
		"/api/v1/compare?left=0&right=100",
		"/api/v1/compare?left=100&right=100",
	}
	for _, path := range cases {
		if w := doJSON(t, compareRouter(stub), http.MethodGet, path, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400, 实际 %d", path, w.Code)
		}
	}
}

func TestCompareMissingRunIsNotFound(t *testing.T) {
	stub := newABStub()
	stub.runsByID[100] = store.Run{ID: 100, DatasetID: 4}

	if w := doJSON(t, compareRouter(stub), http.MethodGet, "/api/v1/compare?left=100&right=999", ""); w.Code != http.StatusNotFound {
		t.Fatalf("right run 不存在应 404, 实际 %d", w.Code)
	}
	if w := doJSON(t, compareRouter(stub), http.MethodGet, "/api/v1/compare?left=999&right=100", ""); w.Code != http.StatusNotFound {
		t.Fatalf("left run 不存在应 404, 实际 %d", w.Code)
	}
}
