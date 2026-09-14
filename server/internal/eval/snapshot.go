// Package eval 承载评测领域的可复现要素: 配置快照 / 指纹 / 代码版本。
//
// 关键约定: 本包与 worker(Python) 侧的 app/eval/snapshot.py 必须产出**同构**快照与
// **同算法**指纹(排序键 + 紧凑 JSON + sha256), 否则"同配置"在两侧对不上。
// 该一致性由单测用固定期望值锁定(见 snapshot_test.go)。
package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ChunkingConfig 分块配置(与 worker 侧 ChunkingConfig 对齐)。
type ChunkingConfig struct {
	Strategy  string `json:"strategy"`
	ChunkSize int    `json:"chunk_size"`
	Overlap   int    `json:"overlap"`
	MinChars  int    `json:"min_chars"`
}

// DefaultChunking 与 worker 默认值保持一致。
func DefaultChunking() ChunkingConfig {
	return ChunkingConfig{Strategy: "headings", ChunkSize: 500, Overlap: 50, MinChars: 80}
}

// Validate 校验切分参数(与 worker 侧 __post_init__ 规则一致)。
func (c ChunkingConfig) Validate() error {
	switch c.Strategy {
	case "headings", "paragraph", "fixed":
	default:
		return fmt.Errorf("未知 strategy: %s", c.Strategy)
	}
	if c.ChunkSize <= 0 {
		return fmt.Errorf("chunk_size 必须为正整数")
	}
	if c.Overlap < 0 {
		return fmt.Errorf("overlap 不能为负")
	}
	if c.Overlap >= c.ChunkSize {
		return fmt.Errorf("overlap 必须小于 chunk_size")
	}
	if c.MinChars < 0 {
		return fmt.Errorf("min_chars 不能为负")
	}
	return nil
}

// EmbeddingConfig 向量模型配置(与 worker Settings 默认值对齐)。
type EmbeddingConfig struct {
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	Dim       int    `json:"dim"`
	BatchSize int    `json:"batch_size"`
}

// DefaultEmbedding 与 worker/.env.example 默认值一致(siliconflow bge-m3, 1024 维)。
func DefaultEmbedding() EmbeddingConfig {
	return EmbeddingConfig{
		Provider:  "siliconflow",
		BaseURL:   "https://api.siliconflow.cn/v1",
		Model:     "BAAI/bge-m3",
		Dim:       1024,
		BatchSize: 16,
	}
}

// GenerationConfig 生成侧配置(M4/D14; 与 worker 侧 build_generation_section 对齐)。
//
// 只有"启用了生成评测"的 run 才会把它写进快照 —— 见 D14:
// 为 nil 时不写 generation 键, 这样只跑检索的历史 run(M2/M3)指纹保持不变。
type GenerationConfig struct {
	Provider        string  `json:"provider"`
	BaseURL         string  `json:"base_url"`
	Model           string  `json:"model"`
	PromptID        string  `json:"prompt_id"`
	Temperature     float64 `json:"temperature"`
	MaxTokens       int     `json:"max_tokens"`
	MaxContextChars int     `json:"max_context_chars"`
}

// DefaultGeneration 与 worker 侧 GENERATION_* 默认值一致(SiliconFlow 提供的 DeepSeek, 复用同一个 key)。
func DefaultGeneration() GenerationConfig {
	return GenerationConfig{
		Provider:        "siliconflow",
		BaseURL:         "https://api.siliconflow.cn/v1",
		Model:           "deepseek-ai/DeepSeek-V3.2",
		PromptID:        "qa_zh_v1",
		Temperature:     0,
		MaxTokens:       512,
		MaxContextChars: 3000,
	}
}

// Validate 校验生成参数(在提交时就挡住明显非法的配置, 避免任务跑到一半才炸)。
func (g GenerationConfig) Validate() error {
	if strings.TrimSpace(g.Provider) == "" {
		return fmt.Errorf("generation.provider 必填")
	}
	if strings.TrimSpace(g.Model) == "" {
		return fmt.Errorf("generation.model 必填")
	}
	if strings.TrimSpace(g.PromptID) == "" {
		return fmt.Errorf("generation.prompt_id 必填(对应 worker/prompts/<id>.md)")
	}
	if g.Temperature < 0 || g.Temperature > 2 {
		return fmt.Errorf("generation.temperature 应在 0~2 之间")
	}
	if g.MaxTokens <= 0 {
		return fmt.Errorf("generation.max_tokens 必须为正整数")
	}
	if g.MaxContextChars <= 0 {
		return fmt.Errorf("generation.max_context_chars 必须为正整数")
	}
	return nil
}

// normalizeFloat 把整数值的浮点转成 int, 使 JSON 输出为 `0` 而不是 `0.0`。
//
// 跨语言指纹的第一个浮点陷阱: Go 编出 `0`, Python 的 json 编出 `0.0` -> 同配置不同指纹。
// worker 侧有对应的 normalize_float(见 app/eval/snapshot.py), 两侧必须同时改。
func normalizeFloat(value float64) any {
	if value == float64(int64(value)) {
		return int64(value)
	}
	return value
}

// generationSection 生成快照里的 generation 段(字段与顺序无关, 因为指纹按键排序)。
func generationSection(g GenerationConfig) map[string]any {
	return map[string]any{
		"provider":          g.Provider,
		"base_url":          g.BaseURL,
		"model":             g.Model,
		"prompt_id":         g.PromptID,
		"temperature":       normalizeFloat(g.Temperature),
		"max_tokens":        g.MaxTokens,
		"max_context_chars": g.MaxContextChars,
	}
}

// SnapshotInput 构建配置快照的输入。
type SnapshotInput struct {
	Chunking  ChunkingConfig
	Embedding EmbeddingConfig
	CorpusID  int64
	DatasetID int64
	TopK      int
	Reranker  bool
	Extras    map[string]any
	// Generation 为 nil 表示"只跑检索"(不写 generation 键, 见 D14)。
	Generation *GenerationConfig
}

// BuildSnapshot 生成全量配置快照(结构必须与 worker 侧一致)。
func BuildSnapshot(in SnapshotInput) map[string]any {
	extras := in.Extras
	if extras == nil {
		extras = map[string]any{}
	}
	snapshot := map[string]any{
		"chunking": map[string]any{
			"strategy":   in.Chunking.Strategy,
			"chunk_size": in.Chunking.ChunkSize,
			"overlap":    in.Chunking.Overlap,
			"min_chars":  in.Chunking.MinChars,
		},
		"embedding": map[string]any{
			"provider":   in.Embedding.Provider,
			"base_url":   in.Embedding.BaseURL,
			"model":      in.Embedding.Model,
			"dim":        in.Embedding.Dim,
			"batch_size": in.Embedding.BatchSize,
		},
		"retrieval": map[string]any{
			"top_k":    in.TopK,
			"reranker": map[string]any{"enabled": in.Reranker},
		},
		"data": map[string]any{
			"corpus_id":  in.CorpusID,
			"dataset_id": in.DatasetID,
		},
		"extras": extras,
	}
	if in.Generation != nil {
		snapshot["generation"] = generationSection(*in.Generation)
	}
	return snapshot
}

// SnapshotHash 配置指纹: 键排序 + 紧凑 JSON + sha256 (与 worker 同算法)。
func SnapshotHash(snapshot map[string]any) (string, error) {
	return hashJSON(snapshot)
}

// ChunkingHash 切分指纹: 只依赖切分配置, 用于 Qdrant collection 命名。
func ChunkingHash(cfg ChunkingConfig) (string, error) {
	return hashJSON(map[string]any{
		"strategy":   cfg.Strategy,
		"chunk_size": cfg.ChunkSize,
		"overlap":    cfg.Overlap,
		"min_chars":  cfg.MinChars,
	})
}

// CollectionName 与 worker 侧 collection_name 一致: corpus{id}_{hash 前 8 位}。
func CollectionName(corpusID int64, chunkingHash string) string {
	suffix := chunkingHash
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return fmt.Sprintf("corpus%d_%s", corpusID, suffix)
}

// GitSHA 当前代码版本(短 sha)。优先环境变量 EVAL_GIT_SHA, 便于容器/CI 场景。
func GitSHA(repoRoot string) string {
	if value := os.Getenv("EVAL_GIT_SHA"); value != "" {
		return value
	}
	root := repoRoot
	if root == "" {
		root = "."
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "unknown"
	}
	return sha
}

// hashJSON 统一哈希实现: encoding/json 对 map 会按键排序输出且无多余空格,
// 与 Python 的 json.dumps(..., sort_keys=True, separators=(",", ":")) 等价。
func hashJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
