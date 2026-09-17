package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/store"
)

// stubAnnotations 内存版标注存储(与 DB 约束一致: (run_id, case_id) 唯一)。
type stubAnnotations struct {
	items     []store.Annotation
	nextID    int64
	listErr   error
	createErr error
	updateErr error
	deleteErr error
	statsErr  error
}

func newStubAnnotations() *stubAnnotations {
	return &stubAnnotations{nextID: 1, items: []store.Annotation{}}
}

func (s *stubAnnotations) ListAnnotations(
	_ context.Context, f store.AnnotationFilter,
) ([]store.Annotation, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]store.Annotation, 0)
	for _, item := range s.items {
		switch {
		case f.RunID != 0 && item.RunID != f.RunID:
			continue
		case f.CaseID != 0 && item.CaseID != f.CaseID:
			continue
		case f.Status != "" && item.Status != f.Status:
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *stubAnnotations) GetAnnotationByRunCase(
	_ context.Context, runID, caseID int64,
) (store.Annotation, error) {
	for _, item := range s.items {
		if item.RunID == runID && item.CaseID == caseID {
			return item, nil
		}
	}
	return store.Annotation{}, store.ErrNotFound
}

func (s *stubAnnotations) CreateAnnotation(
	_ context.Context, projectID, runID, caseID int64,
	status, reason, comment, assignee, createdBy string,
) (store.Annotation, error) {
	if s.createErr != nil {
		return store.Annotation{}, s.createErr
	}
	item := store.Annotation{
		ID: s.nextID, ProjectID: projectID, RunID: runID, CaseID: caseID,
		Status: status, Reason: reason, Comment: comment, Assignee: assignee, CreatedBy: createdBy,
	}
	s.nextID++
	s.items = append(s.items, item)
	return item, nil
}

func (s *stubAnnotations) UpdateAnnotation(
	_ context.Context, id int64, status, reason, comment, assignee *string,
) (store.Annotation, error) {
	if s.updateErr != nil {
		return store.Annotation{}, s.updateErr
	}
	for index, item := range s.items {
		if item.ID != id {
			continue
		}
		if status != nil {
			item.Status = *status
		}
		if reason != nil {
			item.Reason = *reason
		}
		if comment != nil {
			item.Comment = *comment
		}
		if assignee != nil {
			item.Assignee = *assignee
		}
		s.items[index] = item
		return item, nil
	}
	return store.Annotation{}, store.ErrNotFound
}

func (s *stubAnnotations) DeleteAnnotation(_ context.Context, id int64) error {
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

func (s *stubAnnotations) AggregateAnnotationStats(
	_ context.Context, runID int64,
) (store.AnnotationStats, error) {
	if s.statsErr != nil {
		return store.AnnotationStats{}, s.statsErr
	}
	stats := store.AnnotationStats{ByStatus: map[string]int{}, ByReason: map[string]int{}}
	for _, item := range s.items {
		if item.RunID != runID {
			continue
		}
		stats.Total++
		stats.ByStatus[item.Status]++
		reason := item.Reason
		if strings.TrimSpace(reason) == "" {
			reason = "unclassified"
		}
		stats.ByReason[reason]++
	}
	return stats, nil
}

// 复用 runs_test.go 的 stubRunStore 提供"单题结果"(归因建议要用)。
func annotationsRouter(annotations AnnotationStore, runs RunStore) http.Handler {
	gin.SetMode(gin.TestMode)
	return NewRouter(Deps{Postgres: fakePinger{}, Qdrant: fakePinger{}, Annotations: annotations, Runs: runs})
}

func runsStubWithFlags(qid string, flags ...string) *stubRunStore {
	return &stubRunStore{caseResults: []store.RunCaseResult{{QID: qid, CaseID: 42, Flags: flags}}}
}

// runsStubJudged 带判定结果: "幻觉"这种结论必须有判定可依, 否则建议会说"无法确认"。
func runsStubJudged(qid string, flags ...string) *stubRunStore {
	return &stubRunStore{caseResults: []store.RunCaseResult{{
		QID: qid, CaseID: 42, Flags: flags,
		Judge: map[string]any{"claims": []any{map[string]any{"label": "unsupported"}}},
	}}}
}

// ---- 状态机(纯函数) ----

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{"open", "fixed", true},
		{"open", "wontfix", true},
		{"fixed", "verified", true},
		{"fixed", "open", true}, // 复核发现标错了, 允许回退
		{"verified", "open", true},
		{"wontfix", "open", true},
		{"open", "open", true}, // 幂等: 重复提交同一状态
		{"fixed", "fixed", true},
		{"open", "verified", false},  // 不能跳过 fixed
		{"verified", "fixed", false}, // 不许把"已验证"悄悄降级
		{"verified", "wontfix", false},
		{"wontfix", "fixed", false},
		{"", "open", false}, // 未知来源状态一律拒绝
	}
	for _, item := range cases {
		if got := canTransition(item.from, item.to); got != item.want {
			t.Fatalf("%s → %s: got %v, want %v", item.from, item.to, got, item.want)
		}
	}
}

func TestReasonFamilyFollowsPrimaryFlag(t *testing.T) {
	cases := []struct {
		flags []string
		want  string
	}{
		{[]string{"retrieval_miss"}, "retrieval"},
		{[]string{"retrieval_low_rank", "retrieval_partial"}, "retrieval"},
		{[]string{"hallucination"}, "hallucination"},
		{[]string{"generation_quality"}, "generation"},
		{[]string{"off_topic", "hallucination"}, "generation"}, // 主因优先: off_topic 在前
		{[]string{"anchor_incomplete", "retrieval_miss"}, "dataset"},
		{[]string{"no_gold"}, "dataset"},
		{[]string{"brand_new_flag"}, "unknown"},
		{nil, ""},
	}
	for _, item := range cases {
		if got := reasonFamily(item.flags); got != item.want {
			t.Fatalf("flags=%v: got %q, want %q", item.flags, got, item.want)
		}
	}
}

func TestSuggestionRationaleMentionsPrimaryFlag(t *testing.T) {
	if got := suggestionRationale([]string{"retrieval_low_rank"}); !strings.Contains(got, "排序") {
		t.Fatalf("排序问题应提到排序: %q", got)
	}
	if got := suggestionRationale(nil); !strings.Contains(got, "没有给这道题打任何归因标签") {
		t.Fatalf("无标签要说清: %q", got)
	}
}

// ---- 标注 CRUD ----

func TestCreateThenUpdateAnnotationThroughStateMachine(t *testing.T) {
	ann := newStubAnnotations()
	r := annotationsRouter(ann, runsStubWithFlags("zjc-027"))

	w := doJSON(t, r, http.MethodPost, "/api/v1/annotations",
		`{"run_id":155,"case_id":42,"reason":"retrieval","comment":"排位太靠后"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("首次打标应 201: %d (%s)", w.Code, w.Body.String())
	}
	var created store.Annotation
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if created.Status != "open" {
		t.Fatalf("默认状态应为 open: %+v", created)
	}
	if created.Reason != "retrieval" || created.Comment != "排位太靠后" {
		t.Fatalf("字段未落库: %+v", created)
	}

	// 同一题再 POST 一次 = 更新(工作台"点一下打标", 不必先查)
	w = doJSON(t, r, http.MethodPost, "/api/v1/annotations",
		`{"run_id":155,"case_id":42,"status":"fixed","comment":"已把 top_k 提到 8"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("再次打标应 200(更新): %d (%s)", w.Code, w.Body.String())
	}
	if len(ann.items) != 1 {
		t.Fatalf("同一题在同一个 run 里只能有一条标注: %d", len(ann.items))
	}
	if ann.items[0].Status != "fixed" || ann.items[0].Comment != "已把 top_k 提到 8" {
		t.Fatalf("更新未生效: %+v", ann.items[0])
	}
}

func TestAnnotationUpsertPreservesUnmentionedFields(t *testing.T) {
	// 工作台经常只带一个 status(点一下"标记已修")。那种请求**不能**把 reason/comment 抹掉 ——
	// 真机冒烟时就是因为这个, 统计里冒出了 unclassified。
	ann := newStubAnnotations()
	r := annotationsRouter(ann, runsStubWithFlags("zjc-027"))
	doJSON(t, r, http.MethodPost, "/api/v1/annotations",
		`{"run_id":155,"case_id":42,"reason":"retrieval","comment":"排位第5","assignee":"me"}`)

	w := doJSON(t, r, http.MethodPost, "/api/v1/annotations",
		`{"run_id":155,"case_id":42,"status":"fixed"}`)

	if w.Code != http.StatusOK {
		t.Fatalf("应走更新分支: %d", w.Code)
	}
	item := ann.items[0]
	if item.Status != "fixed" {
		t.Fatalf("状态应更新: %+v", item)
	}
	if item.Reason != "retrieval" || item.Comment != "排位第5" || item.Assignee != "me" {
		t.Fatalf("没提到的字段必须保留: %+v", item)
	}

	// 显式传空串才是"清空"
	doJSON(t, r, http.MethodPost, "/api/v1/annotations",
		`{"run_id":155,"case_id":42,"comment":""}`)
	if ann.items[0].Comment != "" {
		t.Fatalf("显式空串应清空评论: %+v", ann.items[0])
	}
	if ann.items[0].Reason != "retrieval" {
		t.Fatalf("清空评论不该动归因: %+v", ann.items[0])
	}
}

func TestAnnotationPostRejectsIllegalStateJump(t *testing.T) {
	ann := newStubAnnotations()
	ann.items = append(ann.items, store.Annotation{
		ID: 1, ProjectID: 1, RunID: 155, CaseID: 42, Status: "open",
	})
	r := annotationsRouter(ann, runsStubWithFlags("zjc-027"))

	w := doJSON(t, r, http.MethodPost, "/api/v1/annotations",
		`{"run_id":155,"case_id":42,"status":"verified"}`)

	if w.Code != http.StatusConflict {
		t.Fatalf("open → verified 应 409: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "状态流转非法") {
		t.Fatalf("错误信息要说明原因: %s", w.Body.String())
	}
}

func TestAnnotationPatchAndDelete(t *testing.T) {
	ann := newStubAnnotations()
	ann.items = append(ann.items, store.Annotation{
		ID: 7, ProjectID: 1, RunID: 155, CaseID: 42, Status: "open", Reason: "",
	})
	r := annotationsRouter(ann, runsStubWithFlags("zjc-027"))

	w := doJSON(t, r, http.MethodPatch, "/api/v1/annotations/7",
		`{"status":"fixed","reason":"retrieval","assignee":" me "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("open → fixed 应 200: %d (%s)", w.Code, w.Body.String())
	}
	var updated store.Annotation
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if updated.Status != "fixed" || updated.Assignee != "me" {
		t.Fatalf("更新未生效/未去空格: %+v", updated)
	}

	if w := doJSON(t, r, http.MethodPatch, "/api/v1/annotations/7", `{"status":"verified"}`); w.Code != http.StatusOK {
		t.Fatalf("fixed → verified 应 200: %d", w.Code)
	}
	if w := doJSON(t, r, http.MethodPatch, "/api/v1/annotations/7", `{"status":"fixed"}`); w.Code != http.StatusConflict {
		t.Fatalf("verified → fixed 应 409: %d", w.Code)
	}
	if w := doJSON(t, r, http.MethodPatch, "/api/v1/annotations/999", `{"status":"open"}`); w.Code != http.StatusNotFound {
		t.Fatalf("不存在的标注应 404: %d", w.Code)
	}

	if w := doJSON(t, r, http.MethodDelete, "/api/v1/annotations/7", ""); w.Code != http.StatusNoContent {
		t.Fatalf("删除应 204: %d", w.Code)
	}
	if w := doJSON(t, r, http.MethodDelete, "/api/v1/annotations/7", ""); w.Code != http.StatusNotFound {
		t.Fatalf("重复删除应 404: %d", w.Code)
	}
}

func TestAnnotationValidationErrors(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   string
	}{
		{"缺 run/case", http.MethodPost, "/api/v1/annotations", `{"comment":"x"}`, "run_id"},
		{"状态非法", http.MethodPost, "/api/v1/annotations", `{"run_id":155,"case_id":42,"status":"done"}`, "status 必须是"},
		{"归因非法", http.MethodPost, "/api/v1/annotations", `{"run_id":155,"case_id":42,"reason":"magic"}`, "reason 必须是"},
		{"补丁状态非法", http.MethodPatch, "/api/v1/annotations/1", `{"status":"done"}`, "status 必须是"},
	}
	for _, item := range cases {
		ann := newStubAnnotations()
		ann.items = append(ann.items, store.Annotation{ID: 1, ProjectID: 1, RunID: 155, CaseID: 42, Status: "open"})
		w := doJSON(t, annotationsRouter(ann, runsStubWithFlags("q")), item.method, item.path, item.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code = %d, want 400 (%s)", item.name, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), item.want) {
			t.Fatalf("%s: 错误信息应提到 %q, 实际 %s", item.name, item.want, w.Body.String())
		}
	}
}

func TestAnnotationListFiltersAndStats(t *testing.T) {
	ann := newStubAnnotations()
	ann.items = []store.Annotation{
		{ID: 1, ProjectID: 1, RunID: 155, CaseID: 42, Status: "open", Reason: "retrieval"},
		{ID: 2, ProjectID: 1, RunID: 155, CaseID: 43, Status: "fixed", Reason: "hallucination"},
		{ID: 3, ProjectID: 1, RunID: 100, CaseID: 42, Status: "verified", Reason: ""},
	}
	r := annotationsRouter(ann, runsStubWithFlags("q"))

	w := doJSON(t, r, http.MethodGet, "/api/v1/annotations?run_id=155", "")
	var listed []store.Annotation
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("响应不是 JSON 数组: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("按 run 过滤应 2 条: %+v", listed)
	}
	if w := doJSON(t, r, http.MethodGet, "/api/v1/annotations?run_id=155&status=fixed", ""); w.Code != http.StatusOK {
		t.Fatalf("带 status 过滤应 200: %d", w.Code)
	}
	if w := doJSON(t, r, http.MethodGet, "/api/v1/annotations?status=nope", ""); w.Code != http.StatusBadRequest {
		t.Fatal("非法 status 应 400")
	}

	w = doJSON(t, r, http.MethodGet, "/api/v1/annotation-stats?run_id=155", "")
	var stats store.AnnotationStats
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatalf("统计响应不是 JSON: %v", err)
	}
	if stats.Total != 2 || stats.ByStatus["open"] != 1 || stats.ByStatus["fixed"] != 1 {
		t.Fatalf("状态统计不符: %+v", stats)
	}
	if stats.ByReason["retrieval"] != 1 || stats.ByReason["hallucination"] != 1 {
		t.Fatalf("归因统计不符: %+v", stats)
	}
	if w := doJSON(t, r, http.MethodGet, "/api/v1/annotation-stats", ""); w.Code != http.StatusBadRequest {
		t.Fatal("缺 run_id 应 400")
	}
}

func TestAnnotationSuggestionFromFlags(t *testing.T) {
	cases := []struct {
		name       string
		flags      []string
		judged     bool
		wantReason string
	}{
		{"检索漏召回", []string{"retrieval_miss"}, false, "retrieval"},
		{"幻觉(有判定)", []string{"hallucination"}, true, "hallucination"},
		{"主因是排序", []string{"retrieval_low_rank"}, false, "retrieval"},
		{"无标签", nil, false, ""},
	}
	for _, item := range cases {
		ann := newStubAnnotations()
		runs := runsStubWithFlags("zjc-001", item.flags...)
		if item.judged {
			runs = runsStubJudged("zjc-001", item.flags...)
		}
		r := annotationsRouter(ann, runs)
		w := doJSON(t, r, http.MethodGet, "/api/v1/annotation-suggestion?run_id=155&case_id=42", "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: code = %d", item.name, w.Code)
		}
		var body struct {
			Reason    string   `json:"reason"`
			Rationale string   `json:"rationale"`
			Flags     []string `json:"flags"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: 响应不是 JSON: %v", item.name, err)
		}
		if body.Reason != item.wantReason {
			t.Fatalf("%s: reason = %q, want %q", item.name, body.Reason, item.wantReason)
		}
		if body.Rationale == "" {
			t.Fatalf("%s: 建议必须带一句人话说明", item.name)
		}
	}

	// 参数校验与 404
	ann := newStubAnnotations()
	r := annotationsRouter(ann, runsStubWithFlags("q"))
	if w := doJSON(t, r, http.MethodGet, "/api/v1/annotation-suggestion?run_id=155", ""); w.Code != http.StatusBadRequest {
		t.Fatal("缺 case_id 应 400")
	}
	empty := &stubRunStore{}
	if w := doJSON(t, annotationsRouter(ann, empty), http.MethodGet,
		"/api/v1/annotation-suggestion?run_id=155&case_id=42", ""); w.Code != http.StatusNotFound {
		t.Fatal("该题没有结果应 404")
	}
}

func TestAnnotationStoreErrorsSurfaced(t *testing.T) {
	ann := newStubAnnotations()
	ann.listErr = errors.New("boom")
	if w := doJSON(t, annotationsRouter(ann, runsStubWithFlags("q")), http.MethodGet,
		"/api/v1/annotations?run_id=155", ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("查询失败应 500: %d", w.Code)
	}
	ann = newStubAnnotations()
	ann.createErr = store.ErrNotFound
	if w := doJSON(t, annotationsRouter(ann, runsStubWithFlags("q")), http.MethodPost,
		"/api/v1/annotations", `{"run_id":999,"case_id":999}`); w.Code != http.StatusNotFound {
		t.Fatalf("run/case 不存在应 404: %d", w.Code)
	}
}

func TestAnnotationSuggestionRefusesHallucinationWithoutJudge(t *testing.T) {
	// 护栏: 标签说"幻觉"但这道题没有判定结果(例如历史数据/配置改动)时,
	// 不能把机器的标签当结论照抄给人工 —— 那会把标注工作变成"追认机器的错误"。
	ann := newStubAnnotations()
	r := annotationsRouter(ann, runsStubWithFlags("zjc-001", "hallucination"))

	w := doJSON(t, r, http.MethodGet, "/api/v1/annotation-suggestion?run_id=155&case_id=42", "")

	var body struct {
		Reason    string   `json:"reason"`
		Rationale string   `json:"rationale"`
		Flags     []string `json:"flags"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if body.Reason != "unknown" {
		t.Fatalf("无判定时应建议 unknown, 实际 %q", body.Reason)
	}
	if !strings.Contains(body.Rationale, "无法确认幻觉") {
		t.Fatalf("要说清为什么给不出建议: %q", body.Rationale)
	}
	if len(body.Flags) != 1 || body.Flags[0] != "hallucination" {
		t.Fatalf("原始标签仍要回传(人工需要看到机器说了什么): %v", body.Flags)
	}
}
