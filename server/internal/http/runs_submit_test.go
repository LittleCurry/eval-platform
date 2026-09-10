package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"eval-platform/server/internal/store"
)

func submitBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, raw)
	}
	return body
}

func TestSubmitRunSuccess(t *testing.T) {
	stub := &stubRunStore{
		projectID: 7,
		createRef: store.RunJobRef{RunID: 11, JobID: 12, Items: 30},
	}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/runs",
		`{"dataset_id":3,"corpus_id":4,"top_k":5}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	body := submitBody(t, w.Body.String())
	if body["run_id"] != float64(11) || body["job_id"] != float64(12) || body["items"] != float64(30) {
		t.Fatalf("响应字段不符: %v", body)
	}
	if body["status"] != "pending" {
		t.Fatalf("初始状态应为 pending: %v", body["status"])
	}
	if body["collection"] != "corpus4_5f45e034" {
		t.Fatalf("collection 应带切分指纹前 8 位: %v", body["collection"])
	}
	if body["config_hash"] == "" || body["chunking_hash"] != "5f45e03472e668aeb6b21965be6b23137522e50be45db9267646f4dce04fe16b" {
		t.Fatalf("指纹字段不符: %v", body)
	}

	// 落库输入必须带上派生出来的 project_id 与全量配置快照
	if stub.gotCreateInput == nil {
		t.Fatal("CreateRunWithJob 未被调用")
	}
	in := stub.gotCreateInput
	if in.ProjectID != 7 || in.DatasetID != 3 || in.CorpusID != 4 {
		t.Fatalf("落库入参不符: %+v", in)
	}
	if in.ConfigHash != body["config_hash"] {
		t.Fatalf("落库 config_hash 与响应不一致")
	}
	chunking, ok := in.ConfigSnapshot["chunking"].(map[string]any)
	if !ok || chunking["strategy"] != "headings" || chunking["chunk_size"] != 500 {
		t.Fatalf("快照缺少切分配置: %v", in.ConfigSnapshot["chunking"])
	}
	retrieval, ok := in.ConfigSnapshot["retrieval"].(map[string]any)
	if !ok || retrieval["top_k"] != 5 {
		t.Fatalf("快照缺少检索配置: %v", in.ConfigSnapshot["retrieval"])
	}
}

func TestSubmitRunExplicitProjectID(t *testing.T) {
	stub := &stubRunStore{createRef: store.RunJobRef{RunID: 1, JobID: 2, Items: 3}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/runs",
		`{"dataset_id":3,"corpus_id":4,"project_id":9}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201", w.Code)
	}
	if stub.gotCreateInput == nil || stub.gotCreateInput.ProjectID != 9 {
		t.Fatalf("显式 project_id 未被采用: %+v", stub.gotCreateInput)
	}
}

func TestSubmitRunValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"缺 dataset_id", `{"corpus_id":4}`},
		{"缺 corpus_id", `{"dataset_id":3}`},
		{"top_k 过大", `{"dataset_id":3,"corpus_id":4,"top_k":99}`},
		{"非法切分策略", `{"dataset_id":3,"corpus_id":4,"chunking":{"strategy":"magic","chunk_size":100,"overlap":10}}`},
		{"overlap 不小于 chunk_size", `{"dataset_id":3,"corpus_id":4,"chunking":{"strategy":"headings","chunk_size":100,"overlap":100}}`},
		{"body 非法", `not-json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubRunStore{createRef: store.RunJobRef{RunID: 1, JobID: 2, Items: 3}}
			r := runRouter(t, stub)
			w := doJSON(t, r, http.MethodPost, "/api/v1/runs", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400 (body=%s)", w.Code, w.Body.String())
			}
			if stub.gotCreateInput != nil {
				t.Fatal("校验失败时不应落库")
			}
		})
	}
}

func TestSubmitRunDatasetNotFound(t *testing.T) {
	stub := &stubRunStore{projectErr: store.ErrNotFound}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/runs", `{"dataset_id":999,"corpus_id":4}`)

	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

func TestSubmitRunEmptyDataset(t *testing.T) {
	stub := &stubRunStore{createErr: errors.New("评测集没有用例, 无法提交任务")}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/runs", `{"dataset_id":3,"corpus_id":4}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
}

func TestSubmitRunCustomChunking(t *testing.T) {
	stub := &stubRunStore{createRef: store.RunJobRef{RunID: 1, JobID: 2, Items: 3}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/runs",
		`{"dataset_id":3,"corpus_id":4,"chunking":{"strategy":"fixed","chunk_size":300,"overlap":30,"min_chars":10}}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	chunking := stub.gotCreateInput.ConfigSnapshot["chunking"].(map[string]any)
	if chunking["strategy"] != "fixed" || chunking["chunk_size"] != 300 || chunking["overlap"] != 30 {
		t.Fatalf("自定义切分配置未进快照: %v", chunking)
	}
	// 不同切分 -> 不同 chunking_hash -> 不同 collection
	body := submitBody(t, w.Body.String())
	if body["chunking_hash"] == "5f45e03472e668aeb6b21965be6b23137522e50be45db9267646f4dce04fe16b" {
		t.Fatal("切分配置变化后 chunking_hash 不应保持不变")
	}
}
