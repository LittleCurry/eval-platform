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

// stubProfiles 内存版配置模板存储(按项目/名字唯一, 与 DB 约束一致)。
type stubProfiles struct {
	items     []store.PipelineProfile
	nextID    int64
	listErr   error
	getErr    error
	createErr error
	updateErr error
	deleteErr error
	gotName   string
	gotConfig map[string]any
	gotDesc   *string
}

func newStubProfiles() *stubProfiles {
	return &stubProfiles{nextID: 1, items: []store.PipelineProfile{}}
}

func (s *stubProfiles) ListPipelineProfiles(_ context.Context, projectID int64) ([]store.PipelineProfile, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]store.PipelineProfile, 0)
	for _, item := range s.items {
		if item.ProjectID == projectID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *stubProfiles) GetPipelineProfile(_ context.Context, id int64) (store.PipelineProfile, error) {
	if s.getErr != nil {
		return store.PipelineProfile{}, s.getErr
	}
	for _, item := range s.items {
		if item.ID == id {
			return item, nil
		}
	}
	return store.PipelineProfile{}, store.ErrNotFound
}

func (s *stubProfiles) CreatePipelineProfile(
	_ context.Context, projectID int64, name, description string, config map[string]any,
) (store.PipelineProfile, error) {
	if s.createErr != nil {
		return store.PipelineProfile{}, s.createErr
	}
	s.gotName, s.gotConfig = name, config
	for _, item := range s.items {
		if item.ProjectID == projectID && item.Name == name {
			return store.PipelineProfile{}, store.ErrConflict
		}
	}
	profile := store.PipelineProfile{
		ID: s.nextID, ProjectID: projectID, Name: name, Description: description, Config: config,
	}
	s.nextID++
	s.items = append(s.items, profile)
	return profile, nil
}

func (s *stubProfiles) UpdatePipelineProfile(
	_ context.Context, id int64, name, description *string, config map[string]any,
) (store.PipelineProfile, error) {
	if s.updateErr != nil {
		return store.PipelineProfile{}, s.updateErr
	}
	s.gotName, s.gotConfig, s.gotDesc = "", config, description
	for index, item := range s.items {
		if item.ID != id {
			continue
		}
		if name != nil {
			item.Name = *name
		}
		if description != nil {
			item.Description = *description
		}
		if config != nil {
			item.Config = config
		}
		s.items[index] = item
		return item, nil
	}
	return store.PipelineProfile{}, store.ErrNotFound
}

func (s *stubProfiles) DeletePipelineProfile(_ context.Context, id int64) error {
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

func profileRouter(profiles PipelineProfileStore) http.Handler {
	gin.SetMode(gin.TestMode)
	return NewRouter(Deps{Postgres: fakePinger{}, Qdrant: fakePinger{}, Profiles: profiles})
}

// 合法的最小模板: 只跑检索(不启用生成/判定)
const minimalConfig = `{"chunking":{"strategy":"headings","chunk_size":500,"overlap":50,"min_chars":80},"top_k":5}`

func TestCreateProfileNormalisesConfig(t *testing.T) {
	stub := newStubProfiles()
	r := profileRouter(stub)

	w := doJSON(t, r, http.MethodPost, "/api/v1/pipeline-profiles",
		`{"project_id":1,"name":" 基线 ","description":"60 题 k=5","config":`+minimalConfig+`}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
	if stub.gotName != "基线" {
		t.Fatalf("name 应去掉首尾空格: %q", stub.gotName)
	}
	// 未启用的段必须显式写成 null, 前端表单据此显示开关状态
	generation, ok := stub.gotConfig["generation"]
	if !ok || generation != nil {
		t.Fatalf("未启用生成时应写 generation=null: %#v", stub.gotConfig)
	}
	if judge, ok := stub.gotConfig["judge"]; !ok || judge != nil {
		t.Fatalf("未启用判定时应写 judge=null: %#v", stub.gotConfig)
	}
	retrieval, ok := stub.gotConfig["retrieval"].(map[string]any)
	if !ok || retrieval["top_k"] != 5 {
		t.Fatalf("retrieval.top_k 应默认 5: %#v", stub.gotConfig)
	}
	chunking, ok := stub.gotConfig["chunking"].(map[string]any)
	if !ok || chunking["chunk_size"] != 500 || chunking["strategy"] != "headings" {
		t.Fatalf("chunking 未按请求保留: %#v", stub.gotConfig)
	}
}

func TestCreateProfileKeepsExplicitZeroAndFalse(t *testing.T) {
	// temperature=0(确定性采样) 与 enable_rubric=false 都是有意义的显式取值,
	// 绝不能被"缺省 = 用服务端默认"的逻辑覆盖掉
	stub := newStubProfiles()
	body := `{"project_id":1,"name":"t","config":` +
		`{"chunking":{"strategy":"headings","chunk_size":500,"overlap":50,"min_chars":80},"top_k":5,` +
		`"generation":{"provider":"siliconflow","model":"deepseek-ai/DeepSeek-V3.2","prompt_id":"qa_zh_v1","temperature":0},` +
		`"judge":{"model":"deepseek-ai/DeepSeek-V3.2","claims_prompt_id":"judge_claims_zh_v1","rubric_prompt_id":"judge_rubric_zh_v2","enable_rubric":false}}}`

	w := doJSON(t, profileRouter(stub), http.MethodPost, "/api/v1/pipeline-profiles", body)

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d (body=%s)", w.Code, w.Body.String())
	}
	generation := stub.gotConfig["generation"].(map[string]any)
	if generation["temperature"] != float64(0) {
		t.Fatalf("显式 temperature=0 必须保留: %#v", generation)
	}
	judge := stub.gotConfig["judge"].(map[string]any)
	if judge["enable_rubric"] != false {
		t.Fatalf("显式 enable_rubric=false 必须保留: %#v", judge)
	}
}

func TestCreateProfileRejectsJudgeWithoutGeneration(t *testing.T) {
	// 判定必须有答案: 提交侧拒绝, 模板侧也要拒绝(否则模板存下来就是个定时炸弹)
	stub := newStubProfiles()
	body := `{"project_id":1,"name":"t","config":` +
		`{"chunking":{"strategy":"headings","chunk_size":500,"overlap":50,"min_chars":80},` +
		`"judge":{"model":"m","claims_prompt_id":"p"}}}`

	w := doJSON(t, profileRouter(stub), http.MethodPost, "/api/v1/pipeline-profiles", body)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "同时启用 generation") {
		t.Fatalf("错误信息要说清原因: %s", w.Body.String())
	}
	if len(stub.items) != 0 {
		t.Fatal("校验失败不该落库")
	}
}

func TestCreateProfileValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"缺 project_id", `{"name":"t","config":` + minimalConfig + `}`, "project_id"},
		{"缺 name", `{"project_id":1,"config":` + minimalConfig + `}`, "name 必填"},
		{"缺 chunking", `{"project_id":1,"name":"t","config":{"top_k":5}}`, "config.chunking"},
		{"top_k 过大", `{"project_id":1,"name":"t","config":{"chunking":{"strategy":"headings","chunk_size":500,"overlap":50,"min_chars":80},"top_k":99}}`, "top_k"},
		{"切分非法", `{"project_id":1,"name":"t","config":{"chunking":{"strategy":"不存在的策略","chunk_size":500,"overlap":50,"min_chars":80}}}`, "strategy"},
	}
	for _, item := range cases {
		stub := newStubProfiles()
		w := doJSON(t, profileRouter(stub), http.MethodPost, "/api/v1/pipeline-profiles", item.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code = %d, want 400 (body=%s)", item.name, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), item.want) {
			t.Fatalf("%s: 错误信息应提到 %q, 实际 %s", item.name, item.want, w.Body.String())
		}
	}
}

func TestCreateProfileConflictAndMissingProject(t *testing.T) {
	stub := newStubProfiles()
	body := `{"project_id":1,"name":"dup","config":` + minimalConfig + `}`
	if w := doJSON(t, profileRouter(stub), http.MethodPost, "/api/v1/pipeline-profiles", body); w.Code != http.StatusCreated {
		t.Fatalf("首次创建应 201: %d", w.Code)
	}
	if w := doJSON(t, profileRouter(stub), http.MethodPost, "/api/v1/pipeline-profiles", body); w.Code != http.StatusConflict {
		t.Fatalf("重名应 409: %d", w.Code)
	}

	missingProject := newStubProfiles()
	missingProject.createErr = store.ErrNotFound
	if w := doJSON(t, profileRouter(missingProject), http.MethodPost, "/api/v1/pipeline-profiles", body); w.Code != http.StatusNotFound {
		t.Fatalf("项目不存在应 404: %d", w.Code)
	}
}

func TestListGetUpdateDeleteProfile(t *testing.T) {
	stub := newStubProfiles()
	doJSON(t, profileRouter(stub), http.MethodPost, "/api/v1/pipeline-profiles",
		`{"project_id":1,"name":"基线","config":`+minimalConfig+`}`)
	doJSON(t, profileRouter(stub), http.MethodPost, "/api/v1/pipeline-profiles",
		`{"project_id":2,"name":"别的项目","config":`+minimalConfig+`}`)

	w := doJSON(t, profileRouter(stub), http.MethodGet, "/api/v1/pipeline-profiles?project_id=1", "")
	var listed []store.PipelineProfile
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("响应不是 JSON 数组: %v", err)
	}
	if len(listed) != 1 || listed[0].Name != "基线" {
		t.Fatalf("应按项目过滤: %+v", listed)
	}
	if w := doJSON(t, profileRouter(stub), http.MethodGet, "/api/v1/pipeline-profiles", ""); w.Code != http.StatusBadRequest {
		t.Fatal("缺少 project_id 应 400")
	}

	if w := doJSON(t, profileRouter(stub), http.MethodGet, "/api/v1/pipeline-profiles/1", ""); w.Code != http.StatusOK {
		t.Fatalf("取详情应 200: %d", w.Code)
	}
	if w := doJSON(t, profileRouter(stub), http.MethodGet, "/api/v1/pipeline-profiles/999", ""); w.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404: %d", w.Code)
	}

	// 局部更新: 只改名字时配置必须原样保留
	w = doJSON(t, profileRouter(stub), http.MethodPatch, "/api/v1/pipeline-profiles/1", `{"name":"基线 v2"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("更新应 200: %d (body=%s)", w.Code, w.Body.String())
	}
	var updated store.PipelineProfile
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if updated.Name != "基线 v2" || updated.Config["chunking"] == nil {
		t.Fatalf("改名不该丢配置: %+v", updated)
	}
	if stub.gotConfig != nil {
		t.Fatal("未传 config 时不该改动配置")
	}

	if w := doJSON(t, profileRouter(stub), http.MethodPatch, "/api/v1/pipeline-profiles/1", `{"name":"  "}`); w.Code != http.StatusBadRequest {
		t.Fatal("空白名字应 400")
	}

	if w := doJSON(t, profileRouter(stub), http.MethodDelete, "/api/v1/pipeline-profiles/1", ""); w.Code != http.StatusNoContent {
		t.Fatalf("删除应 204: %d", w.Code)
	}
	if w := doJSON(t, profileRouter(stub), http.MethodDelete, "/api/v1/pipeline-profiles/1", ""); w.Code != http.StatusNotFound {
		t.Fatalf("重复删除应 404: %d", w.Code)
	}
}

func TestPreviewMatchesSubmitFingerprint(t *testing.T) {
	// 这条是 M5-2 的核心承诺: 预览出来的 config_hash 与真提交落库的**逐字相同**,
	// 否则"提交前先看指纹"毫无意义。
	defaults := eval.DefaultEmbedding()
	body := `{"corpus_id":4,"dataset_id":4,"chunking":{"strategy":"headings","chunk_size":500,"overlap":50,"min_chars":80},"top_k":5}`

	w := doJSON(t, profileRouter(newStubProfiles()), http.MethodPost, "/api/v1/pipeline-preview", body)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d (body=%s)", w.Code, w.Body.String())
	}
	var preview struct {
		ConfigHash        string         `json:"config_hash"`
		ChunkingHash      string         `json:"chunking_hash"`
		Collection        string         `json:"collection"`
		GenerationEnabled bool           `json:"generation_enabled"`
		JudgeEnabled      bool           `json:"judge_enabled"`
		Snapshot          map[string]any `json:"snapshot"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}

	// 提交侧同一批输入应当算出同一个 hash
	chunking := eval.DefaultChunking()
	expected, err := eval.SnapshotHash(eval.BuildSnapshot(eval.SnapshotInput{
		Chunking: chunking, Embedding: defaults, CorpusID: 4, DatasetID: 4, TopK: 5,
	}))
	if err != nil {
		t.Fatalf("算期望指纹失败: %v", err)
	}
	if preview.ConfigHash != expected {
		t.Fatalf("预览指纹与提交不一致:\n  预览 %s\n  提交 %s", preview.ConfigHash, expected)
	}
	if preview.GenerationEnabled || preview.JudgeEnabled {
		t.Fatal("未传生成/判定时两个开关都应为 false")
	}
	chunkingHash, err := eval.ChunkingHash(chunking)
	if err != nil {
		t.Fatalf("算切分指纹失败: %v", err)
	}
	if preview.ChunkingHash != chunkingHash || preview.Collection != eval.CollectionName(4, chunkingHash) {
		t.Fatalf("切分指纹/集合名不符: %+v", preview)
	}
	if preview.Snapshot["data"] == nil {
		t.Fatalf("快照应含 data 段: %#v", preview.Snapshot)
	}
}

func TestPreviewRequiresCorpusAndDataset(t *testing.T) {
	// 指纹里含 corpus_id/dataset_id, 少了它们算出来的 hash 没有意义
	for _, body := range []string{
		`{"chunking":{"strategy":"headings","chunk_size":500,"overlap":50,"min_chars":80}}`,
		`{"corpus_id":4,"chunking":{"strategy":"headings","chunk_size":500,"overlap":50,"min_chars":80}}`,
	} {
		w := doJSON(t, profileRouter(newStubProfiles()), http.MethodPost, "/api/v1/pipeline-preview", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("缺少 corpus_id/dataset_id 应 400: %d", w.Code)
		}
	}
}

func TestPreviewReusesSubmitValidation(t *testing.T) {
	// 预览与提交必须是同一套规则: judge 无 generation、切分非法都要在这里被挡住
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"判定缺生成",
			`{"corpus_id":4,"dataset_id":4,"chunking":{"strategy":"headings","chunk_size":500,"overlap":50,"min_chars":80},"judge":{"model":"m","claims_prompt_id":"p"}}`,
			"同时启用 generation",
		},
		{
			"切分非法",
			`{"corpus_id":4,"dataset_id":4,"chunking":{"strategy":"nope","chunk_size":500,"overlap":50,"min_chars":80}}`,
			"strategy",
		},
		{
			"top_k 过大",
			`{"corpus_id":4,"dataset_id":4,"chunking":{"strategy":"headings","chunk_size":500,"overlap":50,"min_chars":80},"top_k":99}`,
			"top_k",
		},
	}
	for _, item := range cases {
		w := doJSON(t, profileRouter(newStubProfiles()), http.MethodPost, "/api/v1/pipeline-preview", item.body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: code = %d, want 400 (body=%s)", item.name, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), item.want) {
			t.Fatalf("%s: 错误信息应提到 %q, 实际 %s", item.name, item.want, w.Body.String())
		}
	}
}

func TestPreviewWithGenerationAndJudgeChangesFingerprint(t *testing.T) {
	// 启用生成/判定 = 换了一场实验, 指纹必须变化(否则与 D14 的语义冲突)
	without := doJSON(t, profileRouter(newStubProfiles()), http.MethodPost, "/api/v1/pipeline-preview",
		`{"corpus_id":4,"dataset_id":4,"chunking":{"strategy":"headings","chunk_size":500,"overlap":50,"min_chars":80},"top_k":5}`)
	with := doJSON(t, profileRouter(newStubProfiles()), http.MethodPost, "/api/v1/pipeline-preview",
		`{"corpus_id":4,"dataset_id":4,"chunking":{"strategy":"headings","chunk_size":500,"overlap":50,"min_chars":80},"top_k":5,`+
			`"generation":{"provider":"siliconflow","model":"deepseek-ai/DeepSeek-V3.2","prompt_id":"qa_zh_v1","temperature":0},`+
			`"judge":{"model":"deepseek-ai/DeepSeek-V3.2","claims_prompt_id":"judge_claims_zh_v1"}}`)

	var a, b struct {
		ConfigHash        string `json:"config_hash"`
		GenerationEnabled bool   `json:"generation_enabled"`
		JudgeEnabled      bool   `json:"judge_enabled"`
	}
	if err := json.Unmarshal(without.Body.Bytes(), &a); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if err := json.Unmarshal(with.Body.Bytes(), &b); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if a.ConfigHash == b.ConfigHash {
		t.Fatal("启用生成/判定后指纹必须变化")
	}
	if !b.GenerationEnabled || !b.JudgeEnabled {
		t.Fatalf("开关应被识别: %+v", b)
	}
}

func TestProfileStoreErrorsSurfaced(t *testing.T) {
	stub := newStubProfiles()
	stub.listErr = errors.New("boom")
	if w := doJSON(t, profileRouter(stub), http.MethodGet, "/api/v1/pipeline-profiles?project_id=1", ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("查询失败应 500: %d", w.Code)
	}
	stub = newStubProfiles()
	stub.updateErr = store.ErrConflict
	if w := doJSON(t, profileRouter(stub), http.MethodPatch, "/api/v1/pipeline-profiles/1", `{"name":"x"}`); w.Code != http.StatusConflict {
		t.Fatalf("改名冲突应 409: %d", w.Code)
	}
}
