package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"eval-platform/server/internal/eval"
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

// ---- M4 / D14: 提交时启用生成 ----

func TestSubmitRunWithGenerationWritesSnapshotSection(t *testing.T) {
	stub := &stubRunStore{projectID: 7, createRef: store.RunJobRef{RunID: 21, JobID: 22, Items: 60}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/runs",
		`{"dataset_id":4,"corpus_id":4,"top_k":5,"generation":{"model":"deepseek-ai/DeepSeek-V3.2","prompt_id":"qa_zh_v1","temperature":0}}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	body := submitBody(t, w.Body.String())
	if body["generation_enabled"] != true {
		t.Fatalf("响应应标明启用了生成: %v", body["generation_enabled"])
	}
	section, ok := stub.gotCreateInput.ConfigSnapshot["generation"].(map[string]any)
	if !ok {
		t.Fatalf("快照缺少 generation 段: %v", stub.gotCreateInput.ConfigSnapshot)
	}
	if section["prompt_id"] != "qa_zh_v1" || section["model"] != "deepseek-ai/DeepSeek-V3.2" {
		t.Fatalf("generation 段字段不符: %v", section)
	}
	if _, isFloat := section["temperature"].(float64); isFloat {
		t.Fatalf("温度为整数时应归一化(否则与 worker 侧指纹不一致): %T", section["temperature"])
	}
	// 未显式给出的字段取服务端默认值
	if numeric(t, section["max_tokens"]) != 512 || numeric(t, section["max_context_chars"]) != 3000 {
		t.Fatalf("未提供的字段应落到默认值: %v", section)
	}
}

func TestSubmitRunWithoutGenerationOmitsSection(t *testing.T) {
	// D14 红线: 不启用生成时快照字节级不变, 历史 run 的 config_hash 不被作废
	stub := &stubRunStore{projectID: 7, createRef: store.RunJobRef{RunID: 23, JobID: 24, Items: 60}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/runs", `{"dataset_id":4,"corpus_id":4,"top_k":5}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201", w.Code)
	}
	if _, ok := stub.gotCreateInput.ConfigSnapshot["generation"]; ok {
		t.Fatalf("未启用生成时不应写 generation 段: %v", stub.gotCreateInput.ConfigSnapshot)
	}
	if body := submitBody(t, w.Body.String()); body["generation_enabled"] != false {
		t.Fatalf("generation_enabled 应为 false: %v", body["generation_enabled"])
	}
}

// numeric 把快照里的数值统一成 float64 —— 整数会被 normalizeFloat 归一化成 int64,
// 直接与 float64 比较在 any 上永远不等。
func numeric(t *testing.T, value any) float64 {
	t.Helper()
	switch v := value.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	default:
		t.Fatalf("不是数值类型: %T (%v)", value, value)
		return 0
	}
}

func TestSubmitRunExplicitZeroTemperatureIsPreserved(t *testing.T) {
	// 用指针接收的动机: temperature 显式传 0 不能被服务端默认值覆盖
	stub := &stubRunStore{projectID: 7, createRef: store.RunJobRef{RunID: 25, JobID: 26, Items: 60}}
	r := runRouter(t, stub)

	doJSON(t, r, http.MethodPost, "/api/v1/runs",
		`{"dataset_id":4,"corpus_id":4,"generation":{"temperature":0,"max_tokens":64}}`)

	section := stub.gotCreateInput.ConfigSnapshot["generation"].(map[string]any)
	if got := numeric(t, section["temperature"]); got != 0 {
		t.Fatalf("显式 0 被覆盖了: %v", section["temperature"])
	}
	if got := numeric(t, section["max_tokens"]); got != 64 {
		t.Fatalf("显式 max_tokens 未生效: %v", section["max_tokens"])
	}
}

func TestSubmitRunBlankGenerationFieldFallsBackToDefault(t *testing.T) {
	// 语义约定: 字符串字段留空 = 未提供 -> 取服务端默认。
	// 服务端只校验"形状", prompt 文件是否存在由 worker 在开始跑题之前快速失败。
	stub := &stubRunStore{projectID: 7, createRef: store.RunJobRef{RunID: 27, JobID: 28, Items: 60}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/runs",
		`{"dataset_id":4,"corpus_id":4,"generation":{"prompt_id":"","model":"  "}}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	section := stub.gotCreateInput.ConfigSnapshot["generation"].(map[string]any)
	if section["prompt_id"] != eval.DefaultGeneration().PromptID {
		t.Fatalf("空 prompt_id 应回落到默认值: %v", section["prompt_id"])
	}
	if section["model"] != eval.DefaultGeneration().Model {
		t.Fatalf("空白 model 应回落到默认值: %v", section["model"])
	}
}

func TestSubmitRunRejectsInvalidGeneration(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"温度越界", `{"dataset_id":4,"corpus_id":4,"generation":{"temperature":9}}`},
		{"温度为负", `{"dataset_id":4,"corpus_id":4,"generation":{"temperature":-0.5}}`},
		{"max_tokens 非正", `{"dataset_id":4,"corpus_id":4,"generation":{"max_tokens":0}}`},
		{"上下文预算非正", `{"dataset_id":4,"corpus_id":4,"generation":{"max_context_chars":-1}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubRunStore{projectID: 7, createRef: store.RunJobRef{RunID: 1, JobID: 2, Items: 60}}
			r := runRouter(t, stub)

			w := doJSON(t, r, http.MethodPost, "/api/v1/runs", tc.body)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400 (body=%s)", w.Code, w.Body.String())
			}
			if stub.gotCreateInput != nil {
				t.Fatal("非法配置不应落库")
			}
		})
	}
}

// ---- M4-2 / D14: 提交时启用判定 ----

func TestSubmitRunWithJudgeWritesSnapshotSection(t *testing.T) {
	stub := &stubRunStore{projectID: 7, createRef: store.RunJobRef{RunID: 31, JobID: 32, Items: 60}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/runs",
		`{"dataset_id":4,"corpus_id":4,"top_k":5,`+
			`"generation":{"temperature":0},`+
			`"judge":{"claims_prompt_id":"judge_claims_zh_v1"}}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	body := submitBody(t, w.Body.String())
	if body["generation_enabled"] != true || body["judge_enabled"] != true {
		t.Fatalf("响应应标明两个阶段都启用: %v", body)
	}
	section, ok := stub.gotCreateInput.ConfigSnapshot["judge"].(map[string]any)
	if !ok {
		t.Fatalf("快照缺少 judge 段: %v", stub.gotCreateInput.ConfigSnapshot)
	}
	if section["claims_prompt_id"] != "judge_claims_zh_v1" {
		t.Fatalf("claims_prompt_id 未生效: %v", section)
	}
	// 未显式给出的字段取服务端默认值
	if section["rubric_prompt_id"] != eval.DefaultJudge().RubricPromptID {
		t.Fatalf("rubric_prompt_id 应落到默认值: %v", section)
	}
	if numeric(t, section["max_claims"]) != 12 {
		t.Fatalf("max_claims 应落到默认值: %v", section)
	}
	if _, isFloat := section["temperature"].(float64); isFloat {
		t.Fatalf("温度为整数时应归一化(否则与 worker 侧指纹不一致): %T", section["temperature"])
	}
}

func TestSubmitRunWithoutJudgeOmitsSection(t *testing.T) {
	// D14 红线: 不启用判定时快照字节级不变
	stub := &stubRunStore{projectID: 7, createRef: store.RunJobRef{RunID: 33, JobID: 34, Items: 60}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/runs",
		`{"dataset_id":4,"corpus_id":4,"generation":{"temperature":0}}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201", w.Code)
	}
	if _, ok := stub.gotCreateInput.ConfigSnapshot["judge"]; ok {
		t.Fatalf("未启用判定时不应写 judge 段: %v", stub.gotCreateInput.ConfigSnapshot)
	}
	if body := submitBody(t, w.Body.String()); body["judge_enabled"] != false {
		t.Fatalf("judge_enabled 应为 false: %v", body["judge_enabled"])
	}
}

func TestSubmitRunJudgeWithoutGenerationIsRejected(t *testing.T) {
	// 判定需要有答案: 这种提交是配置错误, 在入口就拒绝而不是让 worker 跑到一半失败
	stub := &stubRunStore{projectID: 7, createRef: store.RunJobRef{RunID: 35, JobID: 36, Items: 60}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/runs",
		`{"dataset_id":4,"corpus_id":4,"judge":{"claims_prompt_id":"judge_claims_zh_v1"}}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "generation") {
		t.Fatalf("错误信息应说明必须同时启用 generation: %s", w.Body.String())
	}
	if stub.gotCreateInput != nil {
		t.Fatal("被拒绝的提交不应落库")
	}
}

func TestSubmitRunJudgeExplicitFalseEnableRubricIsPreserved(t *testing.T) {
	// 用指针接收的动机: enable_rubric=false 与 temperature=0 都是有效取值, 不能被默认值覆盖
	stub := &stubRunStore{projectID: 7, createRef: store.RunJobRef{RunID: 37, JobID: 38, Items: 60}}
	r := runRouter(t, stub)

	doJSON(t, r, http.MethodPost, "/api/v1/runs",
		`{"dataset_id":4,"corpus_id":4,"generation":{"temperature":0},`+
			`"judge":{"enable_rubric":false,"temperature":0,"max_claims":3}}`)

	section := stub.gotCreateInput.ConfigSnapshot["judge"].(map[string]any)
	if section["enable_rubric"] != false {
		t.Fatalf("显式 false 被覆盖了: %v", section["enable_rubric"])
	}
	if numeric(t, section["max_claims"]) != 3 {
		t.Fatalf("显式 max_claims 未生效: %v", section["max_claims"])
	}
	if numeric(t, section["temperature"]) != 0 {
		t.Fatalf("显式 0 温度被覆盖了: %v", section["temperature"])
	}
}

func TestSubmitRunRejectsInvalidJudge(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"温度越界", `{"dataset_id":4,"corpus_id":4,"generation":{},"judge":{"temperature":9}}`},
		{"max_claims 非正", `{"dataset_id":4,"corpus_id":4,"generation":{},"judge":{"max_claims":0}}`},
		{"max_tokens 非正", `{"dataset_id":4,"corpus_id":4,"generation":{},"judge":{"max_tokens":0}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubRunStore{projectID: 7, createRef: store.RunJobRef{RunID: 1, JobID: 2, Items: 60}}
			r := runRouter(t, stub)

			w := doJSON(t, r, http.MethodPost, "/api/v1/runs", tc.body)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400 (body=%s)", w.Code, w.Body.String())
			}
			if stub.gotCreateInput != nil {
				t.Fatal("非法配置不应落库")
			}
		})
	}
}

func TestSubmitRunBlankJudgeFieldFallsBackToDefault(t *testing.T) {
	// 语义约定(与 generation 段一致): 字符串字段留空 = 未提供 -> 取服务端默认。
	// 注意"启用 rubric 但 rubric_prompt_id 为空"这条校验只可能在**服务端默认也为空**时触发
	// (即 JUDGE_RUBRIC_PROMPT_ID 被设成空), 该守卫由 eval 包的 TestJudgeValidate 覆盖。
	stub := &stubRunStore{projectID: 7, createRef: store.RunJobRef{RunID: 39, JobID: 40, Items: 60}}
	r := runRouter(t, stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/runs",
		`{"dataset_id":4,"corpus_id":4,"generation":{"temperature":0},`+
			`"judge":{"claims_prompt_id":"  ","model":""}}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	section := stub.gotCreateInput.ConfigSnapshot["judge"].(map[string]any)
	if section["claims_prompt_id"] != eval.DefaultJudge().ClaimsPromptID {
		t.Fatalf("空 claims_prompt_id 应回落到默认值: %v", section["claims_prompt_id"])
	}
	if section["model"] != eval.DefaultJudge().Model {
		t.Fatalf("空 model 应回落到默认值: %v", section["model"])
	}
}
