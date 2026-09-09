package http

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/store"
)

// ---- fakes ----

type stubDatasetStore struct {
	createErr error
	getErr    error
}

func (s *stubDatasetStore) ListDatasets(ctx context.Context, projectID int64) ([]store.Dataset, error) {
	return nil, nil
}

func (s *stubDatasetStore) CreateDataset(ctx context.Context, projectID int64, name, description string) (store.Dataset, error) {
	if s.createErr != nil {
		return store.Dataset{}, s.createErr
	}
	return store.Dataset{ID: 1, ProjectID: projectID, Name: name, Description: description}, nil
}

func (s *stubDatasetStore) GetDataset(ctx context.Context, id int64) (store.Dataset, error) {
	if s.getErr != nil {
		return store.Dataset{}, s.getErr
	}
	return store.Dataset{ID: id, Name: "d"}, nil
}

func (s *stubDatasetStore) DeleteDataset(ctx context.Context, id int64) error { return nil }

type stubCaseStore struct {
	importErr    error
	importResult store.CaseImportResult
	gotInputs    []store.CaseInput
}

func (s *stubCaseStore) ListCases(ctx context.Context, datasetID int64) ([]store.Case, error) {
	return nil, nil
}

func (s *stubCaseStore) GetCase(ctx context.Context, id int64) (store.Case, error) {
	return store.Case{}, store.ErrNotFound
}

func (s *stubCaseStore) DeleteCase(ctx context.Context, id int64) error { return nil }

func (s *stubCaseStore) ImportValidCases(ctx context.Context, datasetID int64, inputs []store.CaseInput) (store.CaseImportResult, error) {
	s.gotInputs = inputs
	if s.importErr != nil {
		return store.CaseImportResult{}, s.importErr
	}
	return s.importResult, nil
}

// ---- helpers ----

func caseRouter(t *testing.T, datasets DatasetStore, cases CaseStore) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return NewRouter(Deps{
		Postgres: fakePinger{},
		Qdrant:   fakePinger{},
		Datasets: datasets,
		Cases:    cases,
	})
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// ---- tests: import 纯格式校验 ----

func TestImportPartialSuccess(t *testing.T) {
	// 第 1 行合法 → 传给 store; 第 2 行非 JSON → handler 层拦下
	cs := &stubCaseStore{importResult: store.CaseImportResult{Imported: 1}}
	r := caseRouter(t, &stubDatasetStore{}, cs)
	body := "{\"qid\":\"c-001\",\"question\":\"q?\",\"gold_anchors\":[{\"doc\":\"A01\"}],\"difficulty\":\"易\"}\nnot-json\n"
	w := doJSON(t, r, http.MethodPost, "/api/v1/datasets/1/cases/import", body)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if len(cs.gotInputs) != 1 || cs.gotInputs[0].QID != "c-001" {
		t.Fatalf("传给 store 的输入不符: %+v", cs.gotInputs)
	}
	if !contains(w.Body.String(), `"total":2`) ||
		!contains(w.Body.String(), `"imported":1`) ||
		!contains(w.Body.String(), `"不是合法 JSON 对象"`) {
		t.Fatalf("响应不符合预期: %s", w.Body.String())
	}
}

func TestImportDifficultyInvalid(t *testing.T) {
	cs := &stubCaseStore{}
	r := caseRouter(t, &stubDatasetStore{}, cs)
	body := "{\"qid\":\"c-001\",\"question\":\"q?\",\"difficulty\":\"超难\"}\n"
	w := doJSON(t, r, http.MethodPost, "/api/v1/datasets/1/cases/import", body)
	if len(cs.gotInputs) != 0 {
		t.Fatalf("非法行不应进入 store, got %d inputs", len(cs.gotInputs))
	}
	if !contains(w.Body.String(), `difficulty 必须是 易/中/难`) {
		t.Fatalf("缺少 difficulty 错误: %s", w.Body.String())
	}
}

func TestImportEmptyBody(t *testing.T) {
	r := caseRouter(t, &stubDatasetStore{}, &stubCaseStore{})
	w := doJSON(t, r, http.MethodPost, "/api/v1/datasets/1/cases/import", "   \n")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
}

func TestImportDatasetNotFound(t *testing.T) {
	cs := &stubCaseStore{importErr: store.ErrNotFound}
	r := caseRouter(t, &stubDatasetStore{}, cs)
	body := "{\"qid\":\"c-001\",\"question\":\"q?\"}\n"
	w := doJSON(t, r, http.MethodPost, "/api/v1/datasets/999/cases/import", body)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

func TestImportStoreErrorsMerged(t *testing.T) {
	cs := &stubCaseStore{importResult: store.CaseImportResult{
		Imported: 1,
		Errors: []store.CaseLineError{
			{Line: 3, QID: "c-003", Reason: "gold_anchors 引用的 doc 不存在: X99"},
		},
	}}
	r := caseRouter(t, &stubDatasetStore{}, cs)
	body := "{\"qid\":\"c-001\",\"question\":\"q?\"}\nnot-json\n{\"qid\":\"c-003\",\"question\":\"q?\",\"gold_anchors\":[{\"doc\":\"X99\"}]}\n"
	w := doJSON(t, r, http.MethodPost, "/api/v1/datasets/1/cases/import", body)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	// 合并: 行2(handler) + 行3(store), 按行号排序
	if !contains(w.Body.String(), `"line":2`) || !contains(w.Body.String(), `"line":3`) {
		t.Fatalf("errors 未按行合并排序: %s", w.Body.String())
	}
	if !contains(w.Body.String(), `"total":3`) {
		t.Fatalf("total 不对: %s", w.Body.String())
	}
}

func TestCreateDatasetNameRequired(t *testing.T) {
	r := caseRouter(t, &stubDatasetStore{}, &stubCaseStore{})
	w := doJSON(t, r, http.MethodPost, "/api/v1/datasets", `{"project_id":1,"name":""}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
}

func TestGetDatasetNotFound(t *testing.T) {
	r := caseRouter(t, &stubDatasetStore{getErr: store.ErrNotFound}, &stubCaseStore{})
	w := doJSON(t, r, http.MethodGet, "/api/v1/datasets/999", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}
