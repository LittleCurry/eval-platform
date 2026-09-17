package http

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/eval"
	"eval-platform/server/internal/store"
)

type runHandler struct {
	store      RunStore
	embedding  eval.EmbeddingConfig
	generation eval.GenerationConfig
	judge      eval.JudgeConfig
}

func newRunHandler(
	s RunStore,
	embedding eval.EmbeddingConfig,
	generation eval.GenerationConfig,
	judge eval.JudgeConfig,
) *runHandler {
	if embedding.Provider == "" {
		embedding = eval.DefaultEmbedding()
	}
	if generation.Provider == "" {
		generation = eval.DefaultGeneration()
	}
	if judge.Provider == "" {
		judge = eval.DefaultJudge()
	}
	return &runHandler{store: s, embedding: embedding, generation: generation, judge: judge}
}

// queryInt 读取整型查询参数; 缺失或非法时用默认值。
func queryInt(c *gin.Context, key string, def int) int {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return value
}

// List GET /runs?dataset_id=&project_id=&limit=
func (h *runHandler) List(c *gin.Context) {
	datasetID, _ := strconv.ParseInt(c.Query("dataset_id"), 10, 64)
	projectID, _ := strconv.ParseInt(c.Query("project_id"), 10, 64)
	limit := queryInt(c, "limit", 50)

	runs, err := h.store.ListRuns(c.Request.Context(), datasetID, projectID, limit)
	if err != nil {
		log.Printf("list runs: %v", err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	if runs == nil {
		runs = []store.Run{} // 保证响应是 [] 而不是 null
	}
	writeJSON(c, http.StatusOK, runs)
}

// Get GET /runs/:id
func (h *runHandler) Get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	run, err := h.store.GetRun(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "运行记录不存在")
			return
		}
		log.Printf("get run %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	writeJSON(c, http.StatusOK, run)
}

// CaseResults GET /runs/:id/case-results?limit=&flagged=1
func (h *runHandler) CaseResults(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	limit := queryInt(c, "limit", 200)
	flagged := c.Query("flagged") == "1" || c.Query("flagged") == "true"
	ctx := c.Request.Context()

	items, err := h.store.ListRunCaseResults(ctx, id, limit, flagged)
	if err != nil {
		log.Printf("list run case results %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	if items == nil {
		items = []store.RunCaseResult{} // 保证响应是 [] 而不是 null
	}
	// 空结果要区分"run 不存在"与"run 没有结果": 前者 404
	if len(items) == 0 {
		if _, err := h.store.GetRun(ctx, id); errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "运行记录不存在")
			return
		} else if err != nil {
			log.Printf("get run %d: %v", id, err)
			writeErr(c, http.StatusInternalServerError, "查询失败")
			return
		}
	}
	writeJSON(c, http.StatusOK, items)
}

// Report GET /runs/:id/report?worst=10
// 服务端聚合: run 详情 + 指标 + 最差 N 题 + 归因标签统计(前端不必自己算)。
func (h *runHandler) Report(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	worst := queryInt(c, "worst", 10)
	ctx := c.Request.Context()

	run, err := h.store.GetRun(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "运行记录不存在")
			return
		}
		log.Printf("get run %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	// ListRunCaseResults 已按 recall 升序 -> 前 N 条即最差用例
	worstCases, err := h.store.ListRunCaseResults(ctx, id, worst, false)
	if err != nil {
		log.Printf("list worst cases %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}
	flagCounts, err := h.store.ListRunFlagCounts(ctx, id)
	if err != nil {
		log.Printf("flag counts %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	writeJSON(c, http.StatusOK, gin.H{
		"run":         run,
		"metrics":     run.Metrics,
		"worst_cases": worstCases,
		"flag_counts": flagCounts,
	})
}

// ---- M3: 任务提交 ----

type submitRunChunking struct {
	Strategy  string `json:"strategy"`
	ChunkSize int    `json:"chunk_size"`
	Overlap   int    `json:"overlap"`
	MinChars  int    `json:"min_chars"`
}

// submitRunGeneration 提交时的生成配置(M4)。
//
// 字段用**指针**: 只有这样才能区分"没传"(取服务端默认)与"显式传了 0"。
// temperature 的 0 是有意义的取值(确定性采样), 用零值判断会把用户的显式 0 覆盖掉。
// 对象存在 = 启用生成评测(D14: 此时才会往快照里写 generation 段)。
type submitRunGeneration struct {
	Provider        string   `json:"provider"`
	BaseURL         string   `json:"base_url"`
	Model           string   `json:"model"`
	PromptID        string   `json:"prompt_id"`
	Temperature     *float64 `json:"temperature"`
	MaxTokens       *int     `json:"max_tokens"`
	MaxContextChars *int     `json:"max_context_chars"`
}

// submitRunJudge 提交时的判定配置(M4-2)。字段同样用**指针**区分"没传"与"显式传了 0/false":
// temperature=0 与 enable_rubric=false 都是有意义的取值, 用零值判断会被默认值覆盖。
// 对象存在 = 启用判定(D14: 此时才会往快照里写 judge 段); 注意判定必须有答案,
// 因此提交侧要求同时启用 generation。
type submitRunJudge struct {
	Provider        string   `json:"provider"`
	BaseURL         string   `json:"base_url"`
	Model           string   `json:"model"`
	ClaimsPromptID  string   `json:"claims_prompt_id"`
	RubricPromptID  string   `json:"rubric_prompt_id"`
	Temperature     *float64 `json:"temperature"`
	MaxTokens       *int     `json:"max_tokens"`
	MaxContextChars *int     `json:"max_context_chars"`
	EnableRubric    *bool    `json:"enable_rubric"`
	MaxClaims       *int     `json:"max_claims"`
}

type submitRunReq struct {
	DatasetID  int64                `json:"dataset_id"`
	CorpusID   int64                `json:"corpus_id"`
	ProjectID  int64                `json:"project_id"`
	TopK       int                  `json:"top_k"`
	Chunking   *submitRunChunking   `json:"chunking"`
	Generation *submitRunGeneration `json:"generation"`
	Judge      *submitRunJudge      `json:"judge"`
}

// resolveGeneration 把请求里的生成配置与服务端默认值合并并校验; 未启用时返回 nil。
func (h *runHandler) resolveGeneration(req *submitRunGeneration) (*eval.GenerationConfig, error) {
	// 解析规则在 submit_config.go, 与配置模板预览共用同一套(M5-2)
	return resolveGenerationRequest(req, h.generation)
}

// resolveJudge 把请求里的判定配置与服务端默认值合并并校验; 未启用时返回 nil。
func (h *runHandler) resolveJudge(req *submitRunJudge) (*eval.JudgeConfig, error) {
	// 解析规则在 submit_config.go, 与配置模板预览共用同一套(M5-2)
	return resolveJudgeRequest(req, h.judge)
}

// Submit POST /runs
// 提交一次评测任务: 建 run + job + 全部 case 的 job_items, 立即返回(不等评测跑完)。
// 这是"评测任务异步化"的入口: HTTP 只负责落库与入队, 执行交给 worker。
func (h *runHandler) Submit(c *gin.Context) {
	var req submitRunReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeErr(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.DatasetID <= 0 {
		writeErr(c, http.StatusBadRequest, "dataset_id 必填且为正整数")
		return
	}
	if req.CorpusID <= 0 {
		writeErr(c, http.StatusBadRequest, "corpus_id 必填且为正整数")
		return
	}

	topK, err := resolveTopK(req.TopK)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err.Error())
		return
	}
	req.TopK = topK

	// 切分/生成的解析规则都在 submit_config.go, 与配置模板预览共用(M5-2)
	chunking, err := resolveChunkingRequest(req.Chunking)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err.Error())
		return
	}

	// 生成配置: 未传 generation 对象 = 只跑检索(D14: 此时快照不含 generation 段)
	generation, err := h.resolveGeneration(req.Generation)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err.Error())
		return
	}

	// 判定配置: 未传 judge 对象 = 不做判定
	judge, err := h.resolveJudge(req.Judge)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err.Error())
		return
	}
	if judge != nil && generation == nil {
		// 没有答案就没有可核查的对象: 与其让 worker 跑到一半失败, 不如提交时就拒绝
		writeErr(c, http.StatusBadRequest, "启用 judge 时必须同时启用 generation(判定需要有答案)")
		return
	}

	ctx := c.Request.Context()
	// project_id 不传时从数据集推导; 传了也在建 run 的事务里与数据来源核对(M7-2),
	// 所以这里不再需要"信任前端传什么"。
	projectID := req.ProjectID
	if projectID <= 0 {
		derived, err := h.store.GetDatasetProject(ctx, req.DatasetID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(c, http.StatusNotFound, "数据集不存在")
				return
			}
			log.Printf("get dataset project %d: %v", req.DatasetID, err)
			writeErr(c, http.StatusInternalServerError, "查询失败")
			return
		}
		projectID = derived
	}

	snapshot := eval.BuildSnapshot(eval.SnapshotInput{
		Chunking:   chunking,
		Embedding:  h.embedding,
		CorpusID:   req.CorpusID,
		DatasetID:  req.DatasetID,
		TopK:       req.TopK,
		Generation: generation,
		Judge:      judge,
	})
	configHash, err := eval.SnapshotHash(snapshot)
	if err != nil {
		log.Printf("hash snapshot: %v", err)
		writeErr(c, http.StatusInternalServerError, "配置快照生成失败")
		return
	}
	chunkingHash, err := eval.ChunkingHash(chunking)
	if err != nil {
		log.Printf("hash chunking: %v", err)
		writeErr(c, http.StatusInternalServerError, "切分指纹生成失败")
		return
	}

	ref, err := h.store.CreateRunWithJob(ctx, store.CreateRunInput{
		ProjectID:      projectID,
		DatasetID:      req.DatasetID,
		CorpusID:       req.CorpusID,
		ConfigSnapshot: snapshot,
		ConfigHash:     configHash,
		GitSHA:         eval.GitSHA(""),
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeErr(c, http.StatusNotFound, "数据集不存在")
		case errors.Is(err, store.ErrProjectMismatch):
			// 跨项目混用是"静默错误": 指标照样算得出来, 但那是两个项目的数据拼出来的
			writeErr(c, http.StatusBadRequest,
				"数据集与语料不属于同一个项目(或 project_id 与它们不一致): "+
					"一次实验只能在一个项目内进行, 否则报告里会出现来源不明的数字")
		case strings.Contains(err.Error(), "没有用例"):
			writeErr(c, http.StatusBadRequest, err.Error())
		default:
			log.Printf("create run: %v", err)
			writeErr(c, http.StatusInternalServerError, "提交任务失败")
		}
		return
	}

	writeJSON(c, http.StatusCreated, gin.H{
		"run_id":             ref.RunID,
		"job_id":             ref.JobID,
		"items":              ref.Items,
		"status":             "pending",
		"top_k":              req.TopK,
		"config_hash":        configHash,
		"chunking_hash":      chunkingHash,
		"generation_enabled": generation != nil,
		"judge_enabled":      judge != nil,
		"collection":         eval.CollectionName(req.CorpusID, chunkingHash),
	})
}

// Reclaim POST /jobs/reclaim?older_than_seconds=60
// 接管"心跳超时"的僵尸任务(worker 被强杀的场景): 其 running 微任务放回 pending, 任务回队列。
// 运维/故障演练入口; 常驻 worker 也会周期性自动调用。
func (h *runHandler) Reclaim(c *gin.Context) {
	olderThan := 60.0
	if raw := c.Query("older_than_seconds"); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 {
			writeErr(c, http.StatusBadRequest, "older_than_seconds 必须是非负数字")
			return
		}
		olderThan = value
	}

	result, err := h.store.ReclaimStaleJobs(c.Request.Context(), olderThan)
	if err != nil {
		log.Printf("reclaim stale jobs: %v", err)
		writeErr(c, http.StatusInternalServerError, "接管失败")
		return
	}
	writeJSON(c, http.StatusOK, gin.H{
		"jobs":               result.Jobs,
		"items":              result.Items,
		"older_than_seconds": olderThan,
	})
}

// staleAfterSeconds 心跳超过该秒数即视为"疑似 worker 掉线"。
const staleAfterSeconds = 60.0

// Progress GET /runs/:id/progress
// 供前端展示进度条与"疑似掉线"提示; 没有队列任务的 run(M2 CLI 直跑) 返回 job=null。
func (h *runHandler) Progress(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	run, err := h.store.GetRun(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(c, http.StatusNotFound, "运行记录不存在")
			return
		}
		log.Printf("get run %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	job, err := h.store.GetJobByRun(ctx, id)
	if err != nil {
		log.Printf("get job by run %d: %v", id, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	// 兜底: 老任务(或异常路径)可能没有初始化 progress, 用 job_items 实时统计补齐
	if job != nil {
		if _, ok := job.Progress["total"]; !ok {
			if progress, err := h.store.JobProgress(ctx, job.ID); err == nil {
				job.Progress = map[string]any{
					"pending": progress["pending"], "running": progress["running"],
					"succeeded": progress["succeeded"], "failed": progress["failed"],
					"total": progress["total"],
				}
			}
		}
	}

	stale := false
	var jobPayload any
	if job != nil {
		jobPayload = job
		if job.Status == "running" &&
			(job.HeartbeatAt == nil ||
				time.Since(*job.HeartbeatAt) > time.Duration(staleAfterSeconds)*time.Second) {
			stale = true
		}
	}

	writeJSON(c, http.StatusOK, gin.H{
		"run_id":              run.ID,
		"status":              run.Status,
		"job":                 jobPayload,
		"stale":               stale,
		"stale_after_seconds": staleAfterSeconds,
	})
}
