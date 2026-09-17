package http

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/eval"
	"eval-platform/server/internal/store"
)

// pipelineProfileHandler 提供配置模板的 CRUD 与"提交前预览指纹"(M5-2)。
//
// 预览为什么重要: D7 的可复现靠 config_hash, 而这个值以往只有跑完才看得到。模板页把
// 它提前到提交前 —— 选好切分/top_k/是否启用生成判定, 立刻能算出**将来会落库的那个
// hash**, 也就顺带证明了"同配置同 hash"不是嘴上说说。
//
// 路由注意: 预览被放在顶层 /pipeline-preview 而不是 /pipeline-profiles/preview ——
// gin(以及 httprouter)在同一层不允许静态段与 :id 通配段共存, 会直接 panic。
type pipelineProfileHandler struct {
	store      PipelineProfileStore
	embedding  eval.EmbeddingConfig
	generation eval.GenerationConfig
	judge      eval.JudgeConfig
}

func newPipelineProfileHandler(
	s PipelineProfileStore,
	embedding eval.EmbeddingConfig,
	generation eval.GenerationConfig,
	judge eval.JudgeConfig,
) *pipelineProfileHandler {
	if embedding.Provider == "" {
		embedding = eval.DefaultEmbedding()
	}
	if generation.Provider == "" {
		generation = eval.DefaultGeneration()
	}
	if judge.Provider == "" {
		judge = eval.DefaultJudge()
	}
	return &pipelineProfileHandler{store: s, embedding: embedding, generation: generation, judge: judge}
}

// profileConfigBody 模板里的配置: 与 POST /runs 的旋钮一一对应。
type profileConfigBody struct {
	Chunking   *submitRunChunking   `json:"chunking"`
	TopK       int                  `json:"top_k"`
	Generation *submitRunGeneration `json:"generation"`
	Judge      *submitRunJudge      `json:"judge"`
}

type createProfileReq struct {
	ProjectID   int64             `json:"project_id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Config      profileConfigBody `json:"config"`
}

type updateProfileReq struct {
	Name        *string            `json:"name"`
	Description *string            `json:"description"`
	Config      *profileConfigBody `json:"config"`
}

// List GET /pipeline-profiles?project_id=
func (h *pipelineProfileHandler) List(c *gin.Context) {
	projectID, err := strconv.ParseInt(c.Query("project_id"), 10, 64)
	if err != nil || projectID <= 0 {
		writeErr(c, http.StatusBadRequest, "project_id 必填且为正整数")
		return
	}
	items, err := h.store.ListPipelineProfiles(c.Request.Context(), projectID)
	if err != nil {
		log.Printf("list pipeline profiles: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, items)
}

// Create POST /pipeline-profiles
func (h *pipelineProfileHandler) Create(c *gin.Context) {
	var req createProfileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.ProjectID <= 0 {
		writeErr(c, http.StatusBadRequest, "project_id 必填且为正整数")
		return
	}
	if req.Name == "" {
		writeErr(c, http.StatusBadRequest, "name 必填")
		return
	}
	config, err := h.configFromBody(req.Config, true)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err.Error())
		return
	}

	profile, err := h.store.CreatePipelineProfile(
		c.Request.Context(), req.ProjectID, req.Name, strings.TrimSpace(req.Description), config)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeErr(c, http.StatusNotFound, "项目不存在")
		case errors.Is(err, store.ErrConflict):
			writeErr(c, http.StatusConflict, "该项目下已存在同名模板")
		default:
			log.Printf("create pipeline profile: %v", err)
			writeErr(c, http.StatusInternalServerError, "创建失败")
		}
		return
	}
	writeJSON(c, http.StatusCreated, profile)
}

// Get GET /pipeline-profiles/:id
func (h *pipelineProfileHandler) Get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	profile, err := h.store.GetPipelineProfile(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "模板不存在")
			return
		}
		log.Printf("get pipeline profile %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, profile)
}

// Update PATCH /pipeline-profiles/:id
func (h *pipelineProfileHandler) Update(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req updateProfileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			writeErr(c, http.StatusBadRequest, "name 不能为空")
			return
		}
		req.Name = &trimmed
	}
	var config map[string]any
	if req.Config != nil {
		resolved, err := h.configFromBody(*req.Config, true)
		if err != nil {
			writeErr(c, http.StatusBadRequest, err.Error())
			return
		}
		config = resolved
	}

	profile, err := h.store.UpdatePipelineProfile(c.Request.Context(), id, req.Name, req.Description, config)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeErr(c, http.StatusNotFound, "模板不存在")
		case errors.Is(err, store.ErrConflict):
			writeErr(c, http.StatusConflict, "该项目下已存在同名模板")
		default:
			log.Printf("update pipeline profile %d: %v", id, err)
			writeErr(c, http.StatusInternalServerError, "更新失败")
		}
		return
	}
	writeJSON(c, http.StatusOK, profile)
}

// Delete DELETE /pipeline-profiles/:id
func (h *pipelineProfileHandler) Delete(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.store.DeletePipelineProfile(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "模板不存在")
			return
		}
		log.Printf("delete pipeline profile %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "删除失败")
		return
	}
	c.Status(http.StatusNoContent)
}

type previewReq struct {
	profileConfigBody
	CorpusID  int64 `json:"corpus_id"`
	DatasetID int64 `json:"dataset_id"`
}

// Preview POST /pipeline-preview
//
// 用与提交**同一套解析规则**算出快照与指纹, 并回传集合名(切分指纹决定)。
// 返回的 config_hash 就是将来落库的那个值 —— 用它可以在提交前确认"这次是新的实验
// 还是又跑了一遍老配置"。
func (h *pipelineProfileHandler) Preview(c *gin.Context) {
	var req previewReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.CorpusID <= 0 || req.DatasetID <= 0 {
		writeErr(c, http.StatusBadRequest, "corpus_id 与 dataset_id 必填(指纹里包含数据来源)")
		return
	}
	config, err := h.configFromBody(req.profileConfigBody, false)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err.Error())
		return
	}

	chunking := eval.DefaultChunking()
	if raw, ok := config["chunking"].(map[string]any); ok {
		chunking = eval.ChunkingConfig{
			Strategy:  stringField(raw, "strategy", chunking.Strategy),
			ChunkSize: intField(raw, "chunk_size", chunking.ChunkSize),
			Overlap:   intField(raw, "overlap", chunking.Overlap),
			MinChars:  intField(raw, "min_chars", chunking.MinChars),
		}
	}
	topK := 5
	if raw, ok := config["retrieval"].(map[string]any); ok {
		topK = intField(raw, "top_k", topK)
	}

	var generation *eval.GenerationConfig
	if raw, ok := config["generation"].(map[string]any); ok {
		resolved, err := resolveGenerationRequest(generationRequestFrom(raw), h.generation)
		if err != nil {
			writeErr(c, http.StatusBadRequest, err.Error())
			return
		}
		generation = resolved
	}
	var judge *eval.JudgeConfig
	if raw, ok := config["judge"].(map[string]any); ok {
		resolved, err := resolveJudgeRequest(judgeRequestFrom(raw), h.judge)
		if err != nil {
			writeErr(c, http.StatusBadRequest, err.Error())
			return
		}
		judge = resolved
	}
	if judge != nil && generation == nil {
		writeErr(c, http.StatusBadRequest, "启用 judge 时必须同时启用 generation(判定需要有答案)")
		return
	}

	snapshot := eval.BuildSnapshot(eval.SnapshotInput{
		Chunking:   chunking,
		Embedding:  h.embedding,
		CorpusID:   req.CorpusID,
		DatasetID:  req.DatasetID,
		TopK:       topK,
		Generation: generation,
		Judge:      judge,
	})
	configHash, err := eval.SnapshotHash(snapshot)
	if err != nil {
		log.Printf("hash preview snapshot: %v", err)
		writeErr(c, http.StatusInternalServerError, "配置快照生成失败")
		return
	}
	chunkingHash, err := eval.ChunkingHash(chunking)
	if err != nil {
		log.Printf("hash preview chunking: %v", err)
		writeErr(c, http.StatusInternalServerError, "切分指纹生成失败")
		return
	}

	writeJSON(c, http.StatusOK, gin.H{
		"snapshot":           snapshot,
		"config_hash":        configHash,
		"chunking_hash":      chunkingHash,
		"collection":         eval.CollectionName(req.CorpusID, chunkingHash),
		"generation_enabled": generation != nil,
		"judge_enabled":      judge != nil,
	})
}

// configFromBody 把请求体里的配置归一成落库形状(缺省段补齐, 未启用的段写 null)。
//
// requireChunking=true 时要求切分段存在(创建/更新模板走这条); 预览允许省略,
// 因为它可能只是想把"当前表单"算一版指纹。
func (h *pipelineProfileHandler) configFromBody(
	body profileConfigBody, requireChunking bool,
) (map[string]any, error) {
	if requireChunking && body.Chunking == nil {
		return nil, errors.New("config.chunking 必填")
	}
	chunking, err := resolveChunkingRequest(body.Chunking)
	if err != nil {
		return nil, err
	}
	topK, err := resolveTopK(body.TopK)
	if err != nil {
		return nil, err
	}
	generation, err := resolveGenerationRequest(body.Generation, h.generation)
	if err != nil {
		return nil, err
	}
	judge, err := resolveJudgeRequest(body.Judge, h.judge)
	if err != nil {
		return nil, err
	}
	if judge != nil && generation == nil {
		return nil, errors.New("启用 judge 时必须同时启用 generation(判定需要有答案)")
	}

	config := map[string]any{
		"chunking": map[string]any{
			"strategy":   chunking.Strategy,
			"chunk_size": chunking.ChunkSize,
			"overlap":    chunking.Overlap,
			"min_chars":  chunking.MinChars,
		},
		"retrieval": map[string]any{"top_k": topK},
		// 未启用写 null 而不是省略: 前端表单据此显示开关状态, 也让"模板 → 提交请求"
		// 保持一一对应(省略与 null 在 JSON 里虽然等价, 但显式 null 更好读)
		"generation": generationSectionOrNil(generation),
		"judge":      judgeSectionOrNil(judge),
	}
	return config, nil
}

func generationSectionOrNil(config *eval.GenerationConfig) any {
	if config == nil {
		return nil
	}
	return map[string]any{
		"provider":          config.Provider,
		"base_url":          config.BaseURL,
		"model":             config.Model,
		"prompt_id":         config.PromptID,
		"temperature":       config.Temperature,
		"max_tokens":        config.MaxTokens,
		"max_context_chars": config.MaxContextChars,
	}
}

func judgeSectionOrNil(config *eval.JudgeConfig) any {
	if config == nil {
		return nil
	}
	return map[string]any{
		"provider":          config.Provider,
		"base_url":          config.BaseURL,
		"model":             config.Model,
		"claims_prompt_id":  config.ClaimsPromptID,
		"rubric_prompt_id":  config.RubricPromptID,
		"temperature":       config.Temperature,
		"max_tokens":        config.MaxTokens,
		"max_context_chars": config.MaxContextChars,
		"enable_rubric":     config.EnableRubric,
		"max_claims":        config.MaxClaims,
	}
}

// generationRequestFrom / judgeRequestFrom 把已归一化的 map 还原成提交侧的结构体,
// 这样预览走的校验路径与真提交逐字相同。
func generationRequestFrom(section map[string]any) *submitRunGeneration {
	req := &submitRunGeneration{
		Provider: stringField(section, "provider", ""),
		BaseURL:  stringField(section, "base_url", ""),
		Model:    stringField(section, "model", ""),
		PromptID: stringField(section, "prompt_id", ""),
	}
	if value, ok := floatFieldOK(section, "temperature"); ok {
		req.Temperature = &value
	}
	if value, ok := intFieldOK(section, "max_tokens"); ok {
		req.MaxTokens = &value
	}
	if value, ok := intFieldOK(section, "max_context_chars"); ok {
		req.MaxContextChars = &value
	}
	return req
}

func judgeRequestFrom(section map[string]any) *submitRunJudge {
	req := &submitRunJudge{
		Provider:       stringField(section, "provider", ""),
		BaseURL:        stringField(section, "base_url", ""),
		Model:          stringField(section, "model", ""),
		ClaimsPromptID: stringField(section, "claims_prompt_id", ""),
		RubricPromptID: stringField(section, "rubric_prompt_id", ""),
	}
	if value, ok := floatFieldOK(section, "temperature"); ok {
		req.Temperature = &value
	}
	if value, ok := intFieldOK(section, "max_tokens"); ok {
		req.MaxTokens = &value
	}
	if value, ok := intFieldOK(section, "max_context_chars"); ok {
		req.MaxContextChars = &value
	}
	if value, ok := boolFieldOK(section, "enable_rubric"); ok {
		req.EnableRubric = &value
	}
	if value, ok := intFieldOK(section, "max_claims"); ok {
		req.MaxClaims = &value
	}
	return req
}
