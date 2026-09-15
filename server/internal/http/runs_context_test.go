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

// stubPoints 记录"取了哪个集合的哪些 id", 并返回预置正文。
type stubPoints struct {
	points        map[string]store.QdrantPoint
	err           error
	gotCollection string
	gotIDs        []string
	calls         int
}

func (s *stubPoints) RetrievePoints(
	_ context.Context, collection string, ids []string,
) (map[string]store.QdrantPoint, error) {
	s.calls++
	s.gotCollection, s.gotIDs = collection, ids
	if s.err != nil {
		return nil, s.err
	}
	return s.points, nil
}

// GetRunCaseResult 在这里扩展 stubRunStore(同包即可加方法, 不必改 runs_test.go)。
func (s *stubRunStore) GetRunCaseResult(
	_ context.Context, runID, caseID int64,
) (store.RunCaseResult, error) {
	s.gotRunID = runID
	if s.caseErr != nil {
		return store.RunCaseResult{}, s.caseErr
	}
	for _, item := range s.caseResults {
		if item.CaseID == caseID {
			return item, nil
		}
	}
	return store.RunCaseResult{}, store.ErrNotFound
}

type contextChunkBody struct {
	PointID string `json:"point_id"`
	DocID   string `json:"doc_id"`
	Section string `json:"section"`
	Text    string `json:"text"`
	Found   bool   `json:"found"`
}

type contextBody struct {
	Collection string             `json:"collection"`
	Chunks     []contextChunkBody `json:"chunks"`
	Error      string             `json:"error"`
}

func contextRouter(runs RunStore, points QdrantPointStore) http.Handler {
	gin.SetMode(gin.TestMode)
	return NewRouter(Deps{Postgres: fakePinger{}, Qdrant: fakePinger{}, Runs: runs, QdrantPoints: points})
}

// snapshotWithChunking 造一份与落库同形的快照: jsonb 往返后数字是 float64。
func snapshotWithChunking(t *testing.T, cfg eval.ChunkingConfig) map[string]any {
	t.Helper()
	return map[string]any{
		"chunking": map[string]any{
			"strategy":   cfg.Strategy,
			"chunk_size": float64(cfg.ChunkSize),
			"overlap":    float64(cfg.Overlap),
			"min_chars":  float64(cfg.MinChars),
		},
	}
}

func caseWithRetrieved(caseID int64, ids ...string) store.RunCaseResult {
	retrieved := make([]any, 0, len(ids))
	for index, id := range ids {
		retrieved = append(retrieved, map[string]any{
			"point_id": id, "doc_id": "B02", "score": 0.9 - float64(index)/10,
		})
	}
	return store.RunCaseResult{CaseID: caseID, QID: "zjc-027", Retrieved: retrieved}
}

func decodeContext(t *testing.T, raw string) contextBody {
	t.Helper()
	var body contextBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, raw)
	}
	return body
}

func TestCaseContextDerivesCollectionAndKeepsRetrievedOrder(t *testing.T) {
	cfg := eval.DefaultChunking()
	corpusID := int64(4)
	stub := &stubRunStore{
		run: store.Run{ID: 5, CorpusID: &corpusID, ConfigSnapshot: snapshotWithChunking(t, cfg)},
		caseResults: []store.RunCaseResult{{
			CaseID: 11,
			QID:    "zjc-027",
			Retrieved: []any{
				map[string]any{"point_id": "p1", "doc_id": "B02", "score": 0.9},
				map[string]any{"point_id": "p2", "doc_id": "B02", "score": 0.8},
				// 第 3 条故意不带 doc_id: 应从向量库 payload 回填(兼容历史数据)
				map[string]any{"point_id": "p3", "score": 0.7},
			},
		}},
	}
	points := &stubPoints{points: map[string]store.QdrantPoint{
		"p1": {PointID: "p1", DocID: "B02", Text: "超过 7 天无跟进会自动回收", Section: "自动回收"},
		"p2": {PointID: "p2", DocID: "B02", Text: "每人默认 200 条", Section: "领取与在跟上限"},
		"p3": {PointID: "p3", DocID: "A01", Text: "重复线索按手机号判重"},
	}}
	r := contextRouter(stub, points)

	w := doJSON(t, r, http.MethodGet, "/api/v1/runs/5/cases/11/context", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	hash, err := eval.ChunkingHash(cfg)
	if err != nil {
		t.Fatalf("算切分指纹失败: %v", err)
	}
	wantCollection := eval.CollectionName(corpusID, hash)
	if wantCollection != "corpus4_5f45e034" {
		t.Fatalf("默认切分配置的集合名应锁定为 corpus4_5f45e034, 实际 %s", wantCollection)
	}

	body := decodeContext(t, w.Body.String())
	if body.Collection != wantCollection || points.gotCollection != wantCollection {
		t.Fatalf("集合名不符: 响应=%s 请求=%s want=%s", body.Collection, points.gotCollection, wantCollection)
	}
	if len(points.gotIDs) != 3 || points.gotIDs[0] != "p1" || points.gotIDs[2] != "p3" {
		t.Fatalf("point id 未按 retrieved 顺序透传: %v", points.gotIDs)
	}
	if len(body.Chunks) != 3 {
		t.Fatalf("应返回 3 个 chunk, 实际 %d", len(body.Chunks))
	}
	if body.Chunks[0].Text != "超过 7 天无跟进会自动回收" || !body.Chunks[0].Found {
		t.Fatalf("第一个 chunk 正文不符: %+v", body.Chunks[0])
	}
	if body.Chunks[0].Section != "自动回收" || body.Chunks[2].DocID != "A01" {
		t.Fatalf("section/doc_id 未带出: %+v", body.Chunks)
	}
	if body.Error != "" {
		t.Fatalf("正常路径不该有 error 字段: %s", body.Error)
	}
}

func TestCaseContextMarksChunksMissingFromVectorStore(t *testing.T) {
	cfg := eval.DefaultChunking()
	corpusID := int64(4)
	stub := &stubRunStore{
		run:         store.Run{ID: 5, CorpusID: &corpusID, ConfigSnapshot: snapshotWithChunking(t, cfg)},
		caseResults: []store.RunCaseResult{caseWithRetrieved(11, "p1", "p2")},
	}
	points := &stubPoints{points: map[string]store.QdrantPoint{
		"p1": {PointID: "p1", Text: "有正文"},
	}}

	w := doJSON(t, contextRouter(stub, points), http.MethodGet, "/api/v1/runs/5/cases/11/context", "")

	body := decodeContext(t, w.Body.String())
	if w.Code != http.StatusOK || len(body.Chunks) != 2 {
		t.Fatalf("code=%d chunks=%d", w.Code, len(body.Chunks))
	}
	if !body.Chunks[0].Found || body.Chunks[0].Text != "有正文" {
		t.Fatalf("存在的 point 应带正文: %+v", body.Chunks[0])
	}
	if body.Chunks[1].Found || body.Chunks[1].Text != "" {
		t.Fatalf("缺失的 point 应标 found=false 且无正文: %+v", body.Chunks[1])
	}
	if body.Chunks[1].PointID != "p2" {
		t.Fatalf("缺失项也要保留 point_id 供前端对齐: %+v", body.Chunks[1])
	}
}

func TestCaseContextDegradesWhenVectorStoreFails(t *testing.T) {
	// 抽屉正文是锦上添花: 向量库挂了不该让整个报告页 5xx
	cfg := eval.DefaultChunking()
	corpusID := int64(4)
	stub := &stubRunStore{
		run:         store.Run{ID: 5, CorpusID: &corpusID, ConfigSnapshot: snapshotWithChunking(t, cfg)},
		caseResults: []store.RunCaseResult{caseWithRetrieved(11, "p1")},
	}
	points := &stubPoints{err: errors.New("connection refused")}

	w := doJSON(t, contextRouter(stub, points), http.MethodGet, "/api/v1/runs/5/cases/11/context", "")

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200(降级而不是报错)", w.Code)
	}
	body := decodeContext(t, w.Body.String())
	if !strings.Contains(body.Error, "取回 chunk 正文失败") {
		t.Fatalf("应带明确的降级原因: %q", body.Error)
	}
	if len(body.Chunks) != 1 || body.Chunks[0].Found {
		t.Fatalf("降级时仍要返回 chunk 列表(供前端显示缺失): %+v", body.Chunks)
	}
	if body.Collection == "" {
		t.Fatal("降级时也要回传集合名, 便于排障")
	}
}

func TestCaseContextDegradesWithoutVectorStoreDependency(t *testing.T) {
	cfg := eval.DefaultChunking()
	corpusID := int64(4)
	stub := &stubRunStore{
		run:         store.Run{ID: 5, CorpusID: &corpusID, ConfigSnapshot: snapshotWithChunking(t, cfg)},
		caseResults: []store.RunCaseResult{caseWithRetrieved(11, "p1")},
	}

	w := doJSON(t, contextRouter(stub, nil), http.MethodGet, "/api/v1/runs/5/cases/11/context", "")

	body := decodeContext(t, w.Body.String())
	if w.Code != http.StatusOK || !strings.Contains(body.Error, "向量库未配置") {
		t.Fatalf("code=%d error=%q", w.Code, body.Error)
	}
}

func TestCaseContextReportsMissingChunkingSection(t *testing.T) {
	corpusID := int64(4)
	stub := &stubRunStore{
		run:         store.Run{ID: 5, CorpusID: &corpusID, ConfigSnapshot: map[string]any{}},
		caseResults: []store.RunCaseResult{caseWithRetrieved(11, "p1")},
	}
	points := &stubPoints{}

	w := doJSON(t, contextRouter(stub, points), http.MethodGet, "/api/v1/runs/5/cases/11/context", "")

	body := decodeContext(t, w.Body.String())
	if w.Code != http.StatusOK || !strings.Contains(body.Error, "chunking") {
		t.Fatalf("快照缺 chunking 段要说清: code=%d error=%q", w.Code, body.Error)
	}
	if points.calls != 0 {
		t.Fatal("定位不到集合时不该去问向量库")
	}
}

func TestCaseContextNotFoundPaths(t *testing.T) {
	cfg := eval.DefaultChunking()
	corpusID := int64(4)

	runMissing := &stubRunStore{getErr: store.ErrNotFound, caseResults: []store.RunCaseResult{caseWithRetrieved(11, "p1")}}
	if w := doJSON(t, contextRouter(runMissing, &stubPoints{}), http.MethodGet,
		"/api/v1/runs/5/cases/11/context", ""); w.Code != http.StatusNotFound {
		t.Fatalf("run 不存在应 404, 实际 %d", w.Code)
	}

	caseMissing := &stubRunStore{
		run:         store.Run{ID: 5, CorpusID: &corpusID, ConfigSnapshot: snapshotWithChunking(t, cfg)},
		caseResults: []store.RunCaseResult{caseWithRetrieved(11, "p1")},
	}
	if w := doJSON(t, contextRouter(caseMissing, &stubPoints{}), http.MethodGet,
		"/api/v1/runs/5/cases/999/context", ""); w.Code != http.StatusNotFound {
		t.Fatalf("该题没有结果应 404, 实际 %d", w.Code)
	}
}

func TestCaseContextRejectsBadIDs(t *testing.T) {
	stub := &stubRunStore{}
	if w := doJSON(t, contextRouter(stub, &stubPoints{}), http.MethodGet,
		"/api/v1/runs/abc/cases/11/context", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("非法 run id 应 400, 实际 %d", w.Code)
	}
}

func TestPointIDsOfDedupesAndKeepsOrder(t *testing.T) {
	retrieved := []any{
		map[string]any{"point_id": "p2", "doc_id": "A"},
		map[string]any{"point_id": "p1", "doc_id": "B"},
		map[string]any{"point_id": "p2", "doc_id": "A"},
		map[string]any{"doc_id": "C"}, // 缺 point_id: 跳过
		"not-an-object",               // 形态不符: 跳过
	}

	ids := pointIDsOf(retrieved)

	if len(ids) != 2 || ids[0] != "p2" || ids[1] != "p1" {
		t.Fatalf("应去重并保持顺序: %v", ids)
	}
}
