package http

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"eval-platform/server/internal/eval"
	"eval-platform/server/internal/store"
)

// runContextHandler 提供"某题检索到的 chunk 正文"(M4-4.1)。
//
// 单独成一个 handler(而不是塞进 runs.go): 它比报告路径多一个向量库依赖,
// 分开后两边都能各自注入 fake, 报告接口也不必为它背上向量库的可用性要求。
type runContextHandler struct {
	runs   RunStore
	points QdrantPointStore
}

func newRunContextHandler(runs RunStore, points QdrantPointStore) *runContextHandler {
	return &runContextHandler{runs: runs, points: points}
}

// contextChunk 一个 chunk 的正文(按 retrieved 顺序返回)。
// Found=false 表示向量库里查不到这个 point(集合被重建/切分配置变更), 前端据此降级。
type contextChunk struct {
	PointID string `json:"point_id"`
	DocID   string `json:"doc_id,omitempty"`
	Score   any    `json:"score,omitempty"`
	Section string `json:"section,omitempty"`
	Text    string `json:"text,omitempty"`
	Found   bool   `json:"found"`
}

// CaseContext GET /runs/:id/cases/:case_id/context
//
// 返回该题检索到的 chunk 正文, **按 retrieved 的顺序**排列, 前端可直接与答案并排。
// 集合名由 corpus_id + 快照里的切分指纹推导, 因此历史 run 也能取回当时那份正文
// —— 这也是"不把正文落库"的前提(D17)。
//
// 向量库不可用/集合缺失时不返回 5xx: 抽屉正文是锦上添花, 不该让整个报告页报错;
// 响应里带 error 字段, 前端显示"正文暂不可用"并保留其余内容。
func (h *runContextHandler) CaseContext(c *gin.Context) {
	runID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeErr(c, http.StatusBadRequest, "run id 非法")
		return
	}
	caseID, err := strconv.ParseInt(c.Param("case_id"), 10, 64)
	if err != nil {
		writeErr(c, http.StatusBadRequest, "case_id 非法")
		return
	}

	ctx := c.Request.Context()
	run, err := h.runs.GetRun(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(c, http.StatusNotFound, "运行记录不存在")
		return
	}
	if err != nil {
		log.Printf("case context %d: get run: %v", runID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	row, err := h.runs.GetRunCaseResult(ctx, runID, caseID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(c, http.StatusNotFound, "该题在本次 run 里没有结果")
		return
	}
	if err != nil {
		log.Printf("case context %d/%d: get case result: %v", runID, caseID, err)
		writeErr(c, http.StatusInternalServerError, "查询失败")
		return
	}

	ids := pointIDsOf(row.Retrieved)

	collection, collectionErr := collectionNameOf(run)
	if collectionErr != nil {
		// 快照缺 chunking 段: 这是数据问题, 明确说清而不是假装"没有正文"
		log.Printf("case context %d/%d: %v", runID, caseID, collectionErr)
		c.JSON(http.StatusOK, gin.H{
			"collection": "",
			"chunks":     chunksOf(row.Retrieved, nil),
			"error":      collectionErr.Error(),
		})
		return
	}

	if h.points == nil {
		c.JSON(http.StatusOK, gin.H{
			"collection": collection,
			"chunks":     chunksOf(row.Retrieved, nil),
			"error":      "向量库未配置, 无法取回 chunk 正文",
		})
		return
	}

	points, err := h.points.RetrievePoints(ctx, collection, ids)
	if err != nil {
		log.Printf("case context %d/%d: retrieve points: %v", runID, caseID, err)
		c.JSON(http.StatusOK, gin.H{
			"collection": collection,
			"chunks":     chunksOf(row.Retrieved, nil),
			"error":      "取回 chunk 正文失败: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"collection": collection,
		"chunks":     chunksOf(row.Retrieved, points),
	})
}

// pointIDsOf 从落库的 retrieved 里取 point_id(保持顺序并去重)。
func pointIDsOf(retrieved []any) []string {
	out := make([]string, 0, len(retrieved))
	seen := make(map[string]bool, len(retrieved))
	for _, raw := range retrieved {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := item["point_id"].(string)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// chunksOf 把 retrieved 与向量库返回的正文按 point_id 对齐(顺序仍是 retrieved 的顺序)。
// points 为 nil 时所有条目的 Found 都是 false, 前端据此显示"正文缺失"。
func chunksOf(retrieved []any, points map[string]store.QdrantPoint) []contextChunk {
	out := make([]contextChunk, 0, len(retrieved))
	for _, raw := range retrieved {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := item["point_id"].(string)
		if id == "" {
			continue
		}
		chunk := contextChunk{PointID: id, Score: item["score"]}
		if docID, ok := item["doc_id"].(string); ok {
			chunk.DocID = docID
		}
		if point, ok := points[id]; ok {
			chunk.Found = true
			chunk.Text = point.Text
			chunk.Section = point.Section
			if chunk.DocID == "" {
				chunk.DocID = point.DocID
			}
		}
		out = append(out, chunk)
	}
	return out
}

// collectionNameOf 从 run 推导 Qdrant 集合名: corpus{id}_{切分指纹前 8 位}。
func collectionNameOf(run store.Run) (string, error) {
	if run.CorpusID == nil {
		return "", errors.New("该 run 未记录语料库 id, 无法定位集合")
	}
	cfg, err := chunkingOf(run.ConfigSnapshot)
	if err != nil {
		return "", err
	}
	hash, err := eval.ChunkingHash(cfg)
	if err != nil {
		return "", err
	}
	return eval.CollectionName(*run.CorpusID, hash), nil
}

// chunkingOf 从配置快照读切分配置; 缺字段时回落到与 worker 一致的默认值。
func chunkingOf(snapshot map[string]any) (eval.ChunkingConfig, error) {
	section, ok := snapshot["chunking"].(map[string]any)
	if !ok || section == nil {
		return eval.ChunkingConfig{}, errors.New("快照缺少 chunking 段, 无法定位集合")
	}
	cfg := eval.DefaultChunking()
	if strategy, ok := section["strategy"].(string); ok && strategy != "" {
		cfg.Strategy = strategy
	}
	cfg.ChunkSize = intField(section, "chunk_size", cfg.ChunkSize)
	cfg.Overlap = intField(section, "overlap", cfg.Overlap)
	cfg.MinChars = intField(section, "min_chars", cfg.MinChars)
	if err := cfg.Validate(); err != nil {
		return eval.ChunkingConfig{}, err
	}
	return cfg, nil
}

// intField 读 jsonb 里的数字字段(jsonb 解出来是 float64)。
func intField(section map[string]any, key string, def int) int {
	switch value := section[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	default:
		return def
	}
}
