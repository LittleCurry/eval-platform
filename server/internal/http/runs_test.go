package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/store"
)

type stubRunStore struct {
	runs        []store.Run
	run         store.Run
	getErr      error
	caseResults []store.RunCaseResult
	caseErr     error
	flagCounts  map[string]int

	gotDatasetID int64
	gotProjectID int64
	gotLimit     int
	gotRunID     int64
	gotFlagged   bool
	// M3: 任务提交相关
	projectID      int64
	projectErr     error
	createErr      error
	createRef      store.RunJobRef
	gotCreateInput *store.CreateRunInput
}

func (s *stubRunStore) ListRuns(_ context.Context, datasetID, projectID int64, limit int) ([]store.Run, error) {
	s.gotDatasetID, s.gotProjectID, s.gotLimit = datasetID, projectID, limit
	return s.runs, nil
}

func (s *stubRunStore) GetRun(_ context.Context, id int64) (store.Run, error) {
	s.gotRunID = id
	if s.getErr != nil {
		return store.Run{}, s.getErr
	}
	run := s.run
	if run.ID == 0 {
		run.ID = id
	}
	return run, nil
}

func (s *stubRunStore) ListRunCaseResults(
	_ context.Context, runID int64, limit int, flaggedOnly bool,
) ([]store.RunCaseResult, error) {
	s.gotRunID, s.gotLimit, s.gotFlagged = runID, limit, flaggedOnly
	if s.caseErr != nil {
		return nil, s.caseErr
	}
	return s.caseResults, nil
}

func (s *stubRunStore) ListRunFlagCounts(_ context.Context, runID int64) (map[string]int, error) {
	s.gotRunID = runID
	return s.flagCounts, nil
}

func runRouter(t *testing.T, rs RunStore) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return NewRouter(Deps{Postgres: fakePinger{}, Qdrant: fakePinger{}, Runs: rs})
}

func TestListRunsPassesFiltersAndReturnsRuns(t *testing.T) {
	stub := &stubRunStore{runs: []store.Run{{ID: 3, Status: "succeeded", Metrics: map[string]any{"recall_at_k": 0.9}}}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs?dataset_id=3&project_id=1&limit=5", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if stub.gotDatasetID != 3 || stub.gotProjectID != 1 || stub.gotLimit != 5 {
		t.Fatalf("过滤参数未透传: dataset=%d project=%d limit=%d", stub.gotDatasetID, stub.gotProjectID, stub.gotLimit)
	}
	var body []store.Run
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON 数组: %v", err)
	}
	if len(body) != 1 || body[0].ID != 3 {
		t.Fatalf("响应内容不符: %+v", body)
	}
}

func TestListRunsDefaultLimit(t *testing.T) {
	stub := &stubRunStore{}
	r := runRouter(t, stub)

	doJSON(t, r, http.MethodGet, "/api/v1/runs", "")

	if stub.gotLimit != 50 {
		t.Fatalf("默认 limit 应为 50, 实际 %d", stub.gotLimit)
	}
}

func TestGetRunSuccess(t *testing.T) {
	stub := &stubRunStore{run: store.Run{ID: 3, Status: "succeeded", GitSHA: "1dbf358"}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/3", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var body store.Run
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.GitSHA != "1dbf358" || stub.gotRunID != 3 {
		t.Fatalf("响应不符: %+v (gotRunID=%d)", body, stub.gotRunID)
	}
}

func TestGetRunNotFound(t *testing.T) {
	stub := &stubRunStore{getErr: store.ErrNotFound}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/999", "")

	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

func TestGetRunBadID(t *testing.T) {
	r := runRouter(t, &stubRunStore{})

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/abc", "")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
}

func TestCaseResultsPassesLimitAndFlagged(t *testing.T) {
	stub := &stubRunStore{caseResults: []store.RunCaseResult{{CaseID: 1, QID: "zjc-001"}}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/3/case-results?limit=3&flagged=1", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if stub.gotRunID != 3 || stub.gotLimit != 3 || !stub.gotFlagged {
		t.Fatalf("参数未透传: run=%d limit=%d flagged=%v", stub.gotRunID, stub.gotLimit, stub.gotFlagged)
	}
}

func TestCaseResultsRunNotFound(t *testing.T) {
	// run 没有任何结果且 run 本身不存在 -> 404(区分"空结果"与"无此 run")
	stub := &stubRunStore{caseResults: nil, getErr: store.ErrNotFound}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/999/case-results", "")

	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

func TestCaseResultsEmptyForExistingRun(t *testing.T) {
	stub := &stubRunStore{caseResults: nil}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/3/case-results", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if w.Body.String() != "[]" {
		t.Fatalf("空结果应返回 [], 实际 %s", w.Body.String())
	}
}

func TestReportAggregatesMetricsWorstAndFlags(t *testing.T) {
	stub := &stubRunStore{
		run: store.Run{ID: 3, Status: "succeeded", Metrics: map[string]any{"recall_at_k": 0.913889}},
		caseResults: []store.RunCaseResult{
			{CaseID: 26, QID: "zjc-026", Metrics: map[string]any{"recall": 0.25}},
			{CaseID: 17, QID: "zjc-017", Metrics: map[string]any{"recall": 0.3333}},
		},
		flagCounts: map[string]int{"retrieval_miss": 2},
	}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/3/report?worst=2", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	runPart, ok := body["run"].(map[string]any)
	if !ok || runPart["status"] != "succeeded" {
		t.Fatalf("report.run 不符: %v", body["run"])
	}
	cases, ok := body["worst_cases"].([]any)
	if !ok || len(cases) != 2 {
		t.Fatalf("worst_cases 应为 2 条: %v", body["worst_cases"])
	}
	counts, ok := body["flag_counts"].(map[string]any)
	if !ok || counts["retrieval_miss"] != float64(2) {
		t.Fatalf("flag_counts 不符: %v", body["flag_counts"])
	}
	if stub.gotLimit != 2 {
		t.Fatalf("worst 参数未透传为 limit: %d", stub.gotLimit)
	}
}

func (s *stubRunStore) GetDatasetProject(_ context.Context, datasetID int64) (int64, error) {
	s.gotDatasetID = datasetID
	if s.projectErr != nil {
		return 0, s.projectErr
	}
	if s.projectID != 0 {
		return s.projectID, nil
	}
	return 1, nil
}

func (s *stubRunStore) CreateRunWithJob(_ context.Context, in store.CreateRunInput) (store.RunJobRef, error) {
	s.gotCreateInput = &in
	if s.createErr != nil {
		return store.RunJobRef{}, s.createErr
	}
	return s.createRef, nil
}
